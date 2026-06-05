package tls

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-acme/lego/v4/registration"
)

// User is apigw's ACME account. Persisted at /var/lib/apigw/acme/.
//
// The account key is generated once and reused for every certificate ever
// issued by this host. Re-registering on every cert would burn the
// "10 accounts per IP per 3 hours" Let's Encrypt rate limit fast — and there
// is no benefit. See ARCHITECTURE.md § "Account key reuse".
type User struct {
	Email        string                 `json:"email"`
	Registration *registration.Resource `json:"registration"`

	key crypto.PrivateKey
}

// GetEmail / GetRegistration / GetPrivateKey implement registration.User.
// lego's lego.NewConfig(u) requires the interface.
func (u *User) GetEmail() string                          { return u.Email }
func (u *User) GetRegistration() *registration.Resource   { return u.Registration }
func (u *User) GetPrivateKey() crypto.PrivateKey          { return u.key }

// LoadOrCreateAccount returns the existing ACME account from `dir`, or
// generates and persists a fresh ECDSA P-256 key if one does not exist.
//
// The key file is written 0600. The metadata file (registration URI + email)
// is 0600 too — it leaks the email otherwise.
//
// Idempotent — calling twice with the same dir and email is a no-op the
// second time.
func LoadOrCreateAccount(dir, email string) (*User, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}

	keyPath := filepath.Join(dir, "account.key")
	metaPath := filepath.Join(dir, "account.json")

	key, err := loadOrGenerateKey(keyPath)
	if err != nil {
		return nil, err
	}

	u := &User{Email: email, key: key}

	// Best-effort load of previous registration. If the file is missing or
	// corrupt, we'll re-register on the next obtain — that's harmless.
	if b, rerr := os.ReadFile(metaPath); rerr == nil {
		_ = json.Unmarshal(b, u)
	}

	// If the on-disk email doesn't match the requested one, prefer the new
	// one and trigger a re-registration on next use.
	if u.Email != email {
		u.Email = email
		u.Registration = nil
	}
	return u, nil
}

// Save persists the user metadata (NOT the key — that already lives at
// account.key). Call after a successful lego registration.Register().
func (u *User) Save(dir string) error {
	b, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal account: %w", err)
	}
	path := filepath.Join(dir, "account.json")
	if err := atomicWrite(path, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func loadOrGenerateKey(path string) (crypto.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, fmt.Errorf("decode pem: %s", path)
		}
		switch block.Type {
		case "EC PRIVATE KEY":
			return x509.ParseECPrivateKey(block.Bytes)
		case "PRIVATE KEY":
			return x509.ParsePKCS8PrivateKey(block.Bytes)
		default:
			return nil, fmt.Errorf("unsupported PEM type %q in %s", block.Type, path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ecdsa key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	out := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := atomicWrite(path, out, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return key, nil
}

// atomicWrite is the shared write-tmp-then-rename helper used across the tls
// package. Mode is applied to the final file (after rename).
func atomicWrite(path string, b []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
