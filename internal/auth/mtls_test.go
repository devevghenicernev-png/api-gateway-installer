package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"testing"
	"time"
)

// mkCert produces a self-signed cert with the given CN + SANs, returning
// its PEM-encoded form ready to drop into the X-Apigw-Mtls-Cert header.
func mkCert(t *testing.T, cn string, sans []string) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     sans,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestMTLS_NoVerifyRejected(t *testing.T) {
	h := http.Header{}
	h.Set("X-Apigw-Mtls-Verify", "NONE")
	res, _, _ := VerifyMTLS(MTLSConfig{CAFile: "/etc/apigw/ca.pem"}, h)
	if res != MTLSNoVerify {
		t.Fatalf("expected MTLSNoVerify; got %v", res)
	}
}

func TestMTLS_OptionalAllowsMissing(t *testing.T) {
	h := http.Header{}
	h.Set("X-Apigw-Mtls-Verify", "NONE")
	res, _, _ := VerifyMTLS(MTLSConfig{CAFile: "/etc/apigw/ca.pem", Optional: true}, h)
	if res != MTLSOK {
		t.Fatalf("expected MTLSOK with Optional; got %v", res)
	}
}

func TestMTLS_CNMatch(t *testing.T) {
	cert := mkCert(t, "client.example.com", nil)
	h := http.Header{}
	h.Set("X-Apigw-Mtls-Verify", "SUCCESS")
	h.Set("X-Apigw-Mtls-Cert", cert)

	res, cn, _ := VerifyMTLS(MTLSConfig{
		CAFile:   "/etc/apigw/ca.pem",
		AllowCNs: []string{"client.example.com"},
	}, h)
	if res != MTLSOK {
		t.Fatalf("expected OK; got %v cn=%s", res, cn)
	}
}

func TestMTLS_CNWildcard(t *testing.T) {
	cert := mkCert(t, "svc-7.dev.example.com", nil)
	h := http.Header{}
	h.Set("X-Apigw-Mtls-Verify", "SUCCESS")
	h.Set("X-Apigw-Mtls-Cert", cert)

	res, _, why := VerifyMTLS(MTLSConfig{
		CAFile:   "/etc/apigw/ca.pem",
		AllowCNs: []string{"*.dev.example.com"},
	}, h)
	if res != MTLSOK {
		t.Fatalf("expected OK on wildcard; got %v (%s)", res, why)
	}
}

func TestMTLS_CNRejected(t *testing.T) {
	cert := mkCert(t, "rogue.example.com", nil)
	h := http.Header{}
	h.Set("X-Apigw-Mtls-Verify", "SUCCESS")
	h.Set("X-Apigw-Mtls-Cert", cert)

	res, _, _ := VerifyMTLS(MTLSConfig{
		CAFile:   "/etc/apigw/ca.pem",
		AllowCNs: []string{"client.example.com"},
	}, h)
	if res != MTLSNotAllowed {
		t.Fatalf("expected MTLSNotAllowed; got %v", res)
	}
}

func TestMTLS_SANMatch(t *testing.T) {
	cert := mkCert(t, "noisy-cn-no-one-cares", []string{"api.example.com"})
	h := http.Header{}
	h.Set("X-Apigw-Mtls-Verify", "SUCCESS")
	h.Set("X-Apigw-Mtls-Cert", cert)

	res, _, _ := VerifyMTLS(MTLSConfig{
		CAFile:    "/etc/apigw/ca.pem",
		AllowCNs:  []string{"explicit-cn"},
		AllowSANs: []string{"api.example.com"},
	}, h)
	if res != MTLSOK {
		t.Fatalf("expected OK via SAN; got %v", res)
	}
}

func TestMTLS_FingerprintPin(t *testing.T) {
	cert := mkCert(t, "anybody", nil)
	// Compute the expected fingerprint the same way VerifyMTLS would.
	block, _ := pem.Decode([]byte(cert))
	parsed, _ := x509.ParseCertificate(block.Bytes)
	fp := sha256Fingerprint(parsed.Raw)

	h := http.Header{}
	h.Set("X-Apigw-Mtls-Verify", "SUCCESS")
	h.Set("X-Apigw-Mtls-Cert", cert)

	res, _, _ := VerifyMTLS(MTLSConfig{
		CAFile:            "/etc/apigw/ca.pem",
		AllowFingerprints: []string{fp},
	}, h)
	if res != MTLSOK {
		t.Fatalf("expected OK on fingerprint pin; got %v", res)
	}

	// Wrong fingerprint rejected.
	res2, _, _ := VerifyMTLS(MTLSConfig{
		AllowFingerprints: []string{"deadbeef" + fp[8:]},
	}, h)
	if res2 != MTLSNotAllowed {
		t.Fatalf("wrong fingerprint should reject; got %v", res2)
	}
}

func TestMTLS_DNOnlyFallback(t *testing.T) {
	h := http.Header{}
	h.Set("X-Apigw-Mtls-Verify", "SUCCESS")
	h.Set("X-Apigw-Mtls-SDN", "CN=client.example.com,O=Acme,C=US")
	// No cert body.
	res, cn, _ := VerifyMTLS(MTLSConfig{
		AllowCNs: []string{"client.example.com"},
	}, h)
	if res != MTLSOK {
		t.Fatalf("expected DN-only OK; got %v cn=%s", res, cn)
	}
}
