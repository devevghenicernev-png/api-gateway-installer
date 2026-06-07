package tls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"
)

// ---------- Cache round-trip tests ----------

func TestOCSPCache_StoreLookupPurge(t *testing.T) {
	dir := t.TempDir()
	cache, err := OpenOCSPCache(filepath.Join(dir, "ocsp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer cache.Close()

	fp := []byte{0xaa, 0xbb, 0xcc}
	// Fresh entry — 1h in the future.
	exp := time.Now().Add(1 * time.Hour)
	if err := cache.Store(fp, OCSPStatusGood, exp); err != nil {
		t.Fatalf("store: %v", err)
	}
	status, gotExp, hit := cache.Lookup(fp)
	if !hit {
		t.Fatalf("miss on fresh entry")
	}
	if status != OCSPStatusGood {
		t.Fatalf("status: got %v", status)
	}
	if gotExp.Unix() != exp.Unix() {
		t.Fatalf("expiresAt: got %v want %v", gotExp, exp)
	}

	// Stale entry — Lookup must report hit=false even though row exists.
	stale := []byte{0xde, 0xad}
	if err := cache.Store(stale, OCSPStatusGood, time.Now().Add(-1*time.Second)); err != nil {
		t.Fatalf("store stale: %v", err)
	}
	if _, _, hit := cache.Lookup(stale); hit {
		t.Fatalf("expected stale entry to miss")
	}

	// Purge drops the stale entry; fresh survives.
	if n, _ := cache.Purge(); n != 1 {
		t.Fatalf("purge: dropped %d, want 1", n)
	}
	if size := cache.Size(); size != 1 {
		t.Fatalf("size after purge: %d, want 1", size)
	}
}

func TestOCSPCache_NilSafe(t *testing.T) {
	var c *OCSPCache
	if status, _, hit := c.Lookup([]byte("x")); hit || status != OCSPStatusUnknown {
		t.Fatalf("nil Lookup: got status=%v hit=%v", status, hit)
	}
	if err := c.Store([]byte("x"), OCSPStatusGood, time.Now()); err != nil {
		t.Fatalf("nil Store: %v", err)
	}
	if n, err := c.Purge(); err != nil || n != 0 {
		t.Fatalf("nil Purge: n=%d err=%v", n, err)
	}
	if c.Size() != 0 {
		t.Fatalf("nil Size != 0")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

func TestExpiryFromResponse(t *testing.T) {
	now := time.Now()
	// nextUpdate sooner than cap → nextUpdate wins.
	e := ExpiryFromResponse(now.Add(30*time.Minute), 12*time.Hour)
	if e.Unix() != now.Add(30*time.Minute).Unix() {
		t.Errorf("nextUpdate-wins: %v", e)
	}
	// nextUpdate later than cap → cap wins.
	e = ExpiryFromResponse(now.Add(48*time.Hour), 12*time.Hour)
	if e.Sub(now) > 13*time.Hour {
		t.Errorf("cap-wins: %v (delta %v)", e, e.Sub(now))
	}
	// zero nextUpdate → cap wins.
	e = ExpiryFromResponse(time.Time{}, 1*time.Hour)
	if e.Sub(now) > 65*time.Minute || e.Sub(now) < 55*time.Minute {
		t.Errorf("zero-nextUpdate: %v (delta %v)", e, e.Sub(now))
	}
	// non-positive cap → 12h default.
	e = ExpiryFromResponse(time.Time{}, 0)
	if e.Sub(now) > 13*time.Hour || e.Sub(now) < 11*time.Hour {
		t.Errorf("default-cap: %v (delta %v)", e, e.Sub(now))
	}
}

// ---------- FetchOCSP against a fake responder ----------

// testPKI is a minimal CA + child cert built in-memory; everything
// needed to verify an OCSP response is here.
type testPKI struct {
	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey
	leaf    *x509.Certificate
	leafKey *ecdsa.PrivateKey
}

func newTestPKI(t *testing.T, ocspURL string) *testPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "apigw-test-ca"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(1 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTmpl, &caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	leafTmpl := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "client-1"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(1 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		OCSPServer:   []string{ocspURL},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return &testPKI{caCert: caCert, caKey: caKey, leaf: leaf, leafKey: leafKey}
}

// newOCSPResponder returns an httptest server that signs OCSP responses
// for the given PKI with the given status.
func newOCSPResponder(t *testing.T, pki *testPKI, status int, nextUpdate time.Time) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		req, err := ocsp.ParseRequest(body)
		if err != nil {
			http.Error(w, "parse: "+err.Error(), 400)
			return
		}
		tmpl := ocsp.Response{
			Status:       status,
			SerialNumber: req.SerialNumber,
			ThisUpdate:   time.Now().Add(-1 * time.Minute),
			NextUpdate:   nextUpdate,
		}
		if status == ocsp.Revoked {
			tmpl.RevokedAt = time.Now().Add(-5 * time.Minute)
			tmpl.RevocationReason = ocsp.KeyCompromise
		}
		resp, err := ocsp.CreateResponse(pki.caCert, pki.caCert, tmpl, pki.caKey)
		if err != nil {
			http.Error(w, "create: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/ocsp-response")
		_, _ = w.Write(resp)
	}))
	return srv
}

func TestFetchOCSP_Good(t *testing.T) {
	pki := newTestPKI(t, "PLACEHOLDER")
	srv := newOCSPResponder(t, pki, ocsp.Good, time.Now().Add(1*time.Hour))
	defer srv.Close()
	pki.leaf.OCSPServer = []string{srv.URL}

	status, nextUpdate, err := FetchOCSP(pki.leaf, pki.caCert, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("FetchOCSP: %v", err)
	}
	if status != OCSPStatusGood {
		t.Fatalf("status: got %v want Good", status)
	}
	if nextUpdate.IsZero() {
		t.Fatalf("nextUpdate not set")
	}
}

func TestFetchOCSP_Revoked(t *testing.T) {
	pki := newTestPKI(t, "PLACEHOLDER")
	srv := newOCSPResponder(t, pki, ocsp.Revoked, time.Now().Add(1*time.Hour))
	defer srv.Close()
	pki.leaf.OCSPServer = []string{srv.URL}

	status, _, err := FetchOCSP(pki.leaf, pki.caCert, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("FetchOCSP: %v", err)
	}
	if status != OCSPStatusRevoked {
		t.Fatalf("status: got %v want Revoked", status)
	}
}

func TestFetchOCSP_NoResponder(t *testing.T) {
	pki := newTestPKI(t, "")
	pki.leaf.OCSPServer = nil
	_, _, err := FetchOCSP(pki.leaf, pki.caCert, nil)
	if err != ErrNoResponder {
		t.Fatalf("err: got %v want ErrNoResponder", err)
	}
}

func TestFetchOCSP_NilIssuer(t *testing.T) {
	pki := newTestPKI(t, "http://example.invalid/ocsp")
	pki.leaf.OCSPServer = []string{"http://example.invalid/ocsp"}
	_, _, err := FetchOCSP(pki.leaf, nil, nil)
	if err == nil {
		t.Fatalf("want error")
	}
}

func TestFetchOCSP_ResponderUnreachable(t *testing.T) {
	pki := newTestPKI(t, "http://127.0.0.1:1/ocsp") // RFC reserved → connection refused
	pki.leaf.OCSPServer = []string{"http://127.0.0.1:1/ocsp"}
	_, _, err := FetchOCSP(pki.leaf, pki.caCert, &http.Client{Timeout: 100 * time.Millisecond})
	if err == nil {
		t.Fatalf("want network error")
	}
}

func TestFetchOCSP_ResponderBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("oops"))
	}))
	defer srv.Close()
	pki := newTestPKI(t, srv.URL)
	pki.leaf.OCSPServer = []string{srv.URL}
	_, _, err := FetchOCSP(pki.leaf, pki.caCert, &http.Client{Timeout: 1 * time.Second})
	if err == nil {
		t.Fatalf("want error on HTTP 500")
	}
}

func TestFetchOCSP_TriesAllResponders(t *testing.T) {
	// First responder returns 500, second returns valid Good response.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer bad.Close()

	pki := newTestPKI(t, "PLACEHOLDER")
	good := newOCSPResponder(t, pki, ocsp.Good, time.Now().Add(1*time.Hour))
	defer good.Close()
	pki.leaf.OCSPServer = []string{bad.URL, good.URL}
	status, _, err := FetchOCSP(pki.leaf, pki.caCert, &http.Client{Timeout: 1 * time.Second})
	if err != nil {
		t.Fatalf("FetchOCSP: %v", err)
	}
	if status != OCSPStatusGood {
		t.Fatalf("status: got %v want Good", status)
	}
}

func TestCertFingerprint_Deterministic(t *testing.T) {
	pki := newTestPKI(t, "http://x")
	a := CertFingerprint(pki.leaf)
	b := CertFingerprint(pki.leaf)
	if !bytes.Equal(a, b) {
		t.Fatalf("fingerprint non-deterministic")
	}
	if len(a) != 32 {
		t.Fatalf("len=%d want 32 (sha256)", len(a))
	}
	if HexFingerprint(pki.leaf) == "" {
		t.Fatalf("hex empty")
	}
}

func TestFromOCSPStatus(t *testing.T) {
	if FromOCSPStatus(ocsp.Good) != OCSPStatusGood {
		t.Errorf("good")
	}
	if FromOCSPStatus(ocsp.Revoked) != OCSPStatusRevoked {
		t.Errorf("revoked")
	}
	if FromOCSPStatus(ocsp.Unknown) != OCSPStatusUnknown {
		t.Errorf("unknown")
	}
	if FromOCSPStatus(999) != OCSPStatusUnknown {
		t.Errorf("default → unknown")
	}
}

func TestOCSPStatusString(t *testing.T) {
	cases := map[OCSPStatus]string{
		OCSPStatusGood:    "good",
		OCSPStatusRevoked: "revoked",
		OCSPStatusUnknown: "unknown",
	}
	for s, want := range cases {
		if s.String() != want {
			t.Errorf("%d.String() = %q want %q", s, s.String(), want)
		}
	}
}
