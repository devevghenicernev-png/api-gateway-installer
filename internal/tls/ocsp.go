// OCSP (RFC 6960) client + bbolt cache. We use this to validate
// client certificates on mTLS-protected APIs: at request time the
// dashboard handler fetches the OCSP status of the presented cert
// from its issuer's responder (URL from the cert's AIA extension)
// and rejects revoked certs. Responses are cached in a small bbolt
// DB keyed by cert SHA-256 fingerprint, with TTL bounded by the
// responder's nextUpdate.
//
// We do NOT do server-side OCSP stapling here — nginx handles that
// natively when ssl_stapling is on. The dashboard's only OCSP role
// is the client-cert path.
package tls

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
	"golang.org/x/crypto/ocsp"
)

// OCSPStatus mirrors x/crypto/ocsp constants in a serialisable form.
// Stored as int8 in the bbolt cache; mapping is stable across versions.
type OCSPStatus int8

const (
	OCSPStatusGood    OCSPStatus = 0
	OCSPStatusRevoked OCSPStatus = 1
	OCSPStatusUnknown OCSPStatus = 2
)

func (s OCSPStatus) String() string {
	switch s {
	case OCSPStatusGood:
		return "good"
	case OCSPStatusRevoked:
		return "revoked"
	case OCSPStatusUnknown:
		return "unknown"
	}
	return "?"
}

// FromOCSPStatus converts the x/crypto/ocsp constant into our enum.
func FromOCSPStatus(s int) OCSPStatus {
	switch s {
	case ocsp.Good:
		return OCSPStatusGood
	case ocsp.Revoked:
		return OCSPStatusRevoked
	default:
		return OCSPStatusUnknown
	}
}

// OCSPResult is what the verifier hands back to the caller: status +
// when the cache entry should expire (whichever is sooner — responder
// nextUpdate or operator-supplied TTL cap).
type OCSPResult struct {
	Status    OCSPStatus
	ExpiresAt time.Time
	Reason    string // free-form: "cache hit", "fresh fetch", "soft-failed: network"
}

var ocspBucket = []byte("ocsp_responses")

// OCSPCache wraps a bbolt DB. One instance per process; safe for
// concurrent use (bbolt is goroutine-safe under its own lock).
type OCSPCache struct {
	db *bbolt.DB
}

// OpenOCSPCache opens (or creates) the cache at path. Parent dirs are
// created. File mode 0o600 — responses contain no secrets but the cache
// lives next to audit.db / approvals.db with the same posture.
func OpenOCSPCache(path string) (*OCSPCache, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open ocsp.db %s: %w", path, err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(ocspBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init bucket: %w", err)
	}
	return &OCSPCache{db: db}, nil
}

func (c *OCSPCache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// cacheEntry is the on-disk shape. JSON for human-debuggability — sizes
// are tiny (~30 bytes/entry) and forward-compat is more important than
// a wire-format savings.
type cacheEntry struct {
	Status    OCSPStatus `json:"status"`
	ExpiresAt int64      `json:"expires_at"` // unix seconds
}

// Lookup returns (status, expiresAt, true) if the cert has a fresh
// cache entry. Stale entries are NOT auto-purged on read — the cache
// just reports `false`. Purge runs opportunistically on Store.
func (c *OCSPCache) Lookup(fingerprint []byte) (OCSPStatus, time.Time, bool) {
	if c == nil || c.db == nil {
		return OCSPStatusUnknown, time.Time{}, false
	}
	var ent cacheEntry
	hit := false
	_ = c.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(ocspBucket)
		if b == nil {
			return nil
		}
		raw := b.Get(fingerprint)
		if raw == nil {
			return nil
		}
		if err := json.Unmarshal(raw, &ent); err != nil {
			return nil
		}
		hit = true
		return nil
	})
	if !hit {
		return OCSPStatusUnknown, time.Time{}, false
	}
	expires := time.Unix(ent.ExpiresAt, 0)
	if time.Now().After(expires) {
		return ent.Status, expires, false
	}
	return ent.Status, expires, true
}

// Store records a verifier result. ExpiresAt is the cap; the entry
// is removed lazily on the next Lookup that finds it expired.
func (c *OCSPCache) Store(fingerprint []byte, status OCSPStatus, expiresAt time.Time) error {
	if c == nil || c.db == nil {
		return nil
	}
	body, err := json.Marshal(cacheEntry{Status: status, ExpiresAt: expiresAt.Unix()})
	if err != nil {
		return err
	}
	return c.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(ocspBucket)
		if b == nil {
			return errors.New("ocsp: bucket missing")
		}
		return b.Put(fingerprint, body)
	})
}

// Purge drops every entry whose ExpiresAt has passed. Safe to call on a
// schedule (cheap — bbolt walks an in-memory B+ tree).
func (c *OCSPCache) Purge() (int, error) {
	if c == nil || c.db == nil {
		return 0, nil
	}
	dropped := 0
	err := c.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(ocspBucket)
		if b == nil {
			return nil
		}
		now := time.Now().Unix()
		var toDelete [][]byte
		_ = b.ForEach(func(k, v []byte) error {
			var ent cacheEntry
			if json.Unmarshal(v, &ent) != nil {
				toDelete = append(toDelete, append([]byte(nil), k...))
				return nil
			}
			if ent.ExpiresAt < now {
				toDelete = append(toDelete, append([]byte(nil), k...))
			}
			return nil
		})
		for _, k := range toDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		dropped = len(toDelete)
		return nil
	})
	return dropped, err
}

// Size returns the number of entries in the cache (fresh + stale).
func (c *OCSPCache) Size() int {
	if c == nil || c.db == nil {
		return 0
	}
	n := 0
	_ = c.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(ocspBucket)
		if b == nil {
			return nil
		}
		n = b.Stats().KeyN
		return nil
	})
	return n
}

// CertFingerprint is the SHA-256 of the DER bytes — same format we use
// for cert pinning in mtls.AllowFingerprints.
func CertFingerprint(cert *x509.Certificate) []byte {
	sum := sha256.Sum256(cert.Raw)
	return sum[:]
}

// HexFingerprint returns the cache-key fingerprint as a lowercase hex
// string. Convenience for logging and CLI output.
func HexFingerprint(cert *x509.Certificate) string {
	return hex.EncodeToString(CertFingerprint(cert))
}

// ErrNoResponder is returned by FetchOCSP when the cert carries no AIA
// OCSP URL — the caller decides whether that's hard-fail or soft-fail.
var ErrNoResponder = errors.New("ocsp: cert has no responder URL (AIA)")

// FetchOCSP queries the cert's OCSP responder for revocation status.
// `issuer` MUST be the actual issuer cert (the responder needs its
// public key to validate the signed response). `client` is the HTTP
// client used; pass nil for a sensible default (5s timeout).
//
// Returns the status and the responder's nextUpdate (zero time if the
// responder didn't supply one — the caller should impose an upper TTL).
func FetchOCSP(cert, issuer *x509.Certificate, client *http.Client) (OCSPStatus, time.Time, error) {
	if len(cert.OCSPServer) == 0 {
		return OCSPStatusUnknown, time.Time{}, ErrNoResponder
	}
	if issuer == nil {
		return OCSPStatusUnknown, time.Time{}, errors.New("ocsp: nil issuer cert")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	reqBody, err := ocsp.CreateRequest(cert, issuer, nil)
	if err != nil {
		return OCSPStatusUnknown, time.Time{}, fmt.Errorf("create ocsp request: %w", err)
	}

	// Try each responder URL until one answers. Short circuit on first
	// well-formed response — that's the same behaviour browsers use.
	var lastErr error
	for _, responder := range cert.OCSPServer {
		req, err := http.NewRequest(http.MethodPost, responder, bytes.NewReader(reqBody))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/ocsp-request")
		req.Header.Set("Accept", "application/ocsp-response")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("responder %s: HTTP %d", responder, resp.StatusCode)
			continue
		}
		parsed, perr := ocsp.ParseResponseForCert(body, cert, issuer)
		if perr != nil {
			lastErr = fmt.Errorf("parse ocsp response from %s: %w", responder, perr)
			continue
		}
		return FromOCSPStatus(parsed.Status), parsed.NextUpdate, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no responder answered")
	}
	return OCSPStatusUnknown, time.Time{}, lastErr
}

// ExpiryFromResponse computes the cache expiry given a responder's
// nextUpdate and an operator-supplied cap. Returns the earlier of the
// two; if nextUpdate is zero (responder didn't supply), the cap wins.
// A non-positive cap defaults to 12h.
func ExpiryFromResponse(nextUpdate time.Time, capDur time.Duration) time.Time {
	if capDur <= 0 {
		capDur = 12 * time.Hour
	}
	cap := time.Now().Add(capDur)
	if nextUpdate.IsZero() {
		return cap
	}
	if nextUpdate.Before(cap) {
		return nextUpdate
	}
	return cap
}
