// Package webhook is apigw's GitHub-webhook receiver: HTTP handler with
// constant-time HMAC verification, bounded delivery-id LRU for replay
// protection, and a bbolt-backed persistent job queue feeding the deploy
// engine.
//
// Three callers exist:
//
//   - `apigw webhook serve` (the systemd unit) runs server + worker forever.
//   - `apigw deploy add` / `run` write to the queue too (via deploy.JobQueue
//     directly — they don't need HMAC).
//   - The dashboard subscribes to events emitted from server + worker.
//
// HMAC verification follows GitHub's spec
// (https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries):
// HMAC-SHA256 over the raw body using the per-deploy secret; compare with
// crypto/hmac.Equal — never == or bytes.Equal. See hmac.go.
package webhook

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/paths"
)

// SecretDir is where per-deploy HMAC secrets live (defaults to
// /etc/apigw/webhooks, honors APIGW_CONFIG_DIR). 0700 root:root by design;
// the secrets themselves are 0600. We chose filesystem over systemd-creds as
// the default per ARCHITECTURE.md §"Secrets matrix" — Phase 7 will add an
// opt-in `--use-creds` flag that re-encrypts these.
func SecretDir() string { return filepath.Join(paths.ConfigDir(), "webhooks") }

// SecretBytes is the length of a generated HMAC secret in bytes (256 bits).
//
// GitHub accepts any string but the spec recommends 20+ random bytes. 32
// gives us 256 bits of entropy — well above the conservative bar.
const SecretBytes = 32

// RotationGrace is how long the PREVIOUS secret remains valid after a
// rotation, so in-flight GitHub deliveries signed with the old key still
// verify until the new secret is configured on GitHub's side.
//
// 5 minutes matches GitHub's webhook retry window; longer would extend
// the time an attacker who once captured the old secret can replay.
var RotationGrace = 5 * time.Minute

// SecretPath returns <ConfigDir>/webhooks/<deploy>.secret.
func SecretPath(deploy string) string {
	return filepath.Join(SecretDir(), deploy+".secret")
}

// PrevSecretPath returns <ConfigDir>/webhooks/<deploy>.secret.prev. The file
// is touched at rotation time; mtime drives the grace-window check.
func PrevSecretPath(deploy string) string {
	return filepath.Join(SecretDir(), deploy+".secret.prev")
}

// EnsureSecret returns the existing secret for `deploy`, or generates a new
// one if no file exists. Idempotent — call from any code path that needs a
// secret without risking accidental rotation.
//
// The returned string is the raw secret (hex). Persisted file content is the
// raw hex without a trailing newline so a careful operator can `cat` it.
func EnsureSecret(deploy string) (string, error) {
	path := SecretPath(deploy)
	if b, err := os.ReadFile(path); err == nil {
		s := strings.TrimSpace(string(b))
		if len(s) >= 16 { // sanity: too short → regenerate
			return s, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	s, err := generate()
	if err != nil {
		return "", err
	}
	if err := writeSecret(path, s); err != nil {
		return "", err
	}
	return s, nil
}

// RotateSecret unconditionally regenerates the secret for `deploy` and
// returns the new value. Used by `apigw webhook rotate-secret`.
//
// Grace period: the OLD secret is moved to <deploy>.secret.prev and remains
// valid for RotationGrace (5 min). In-flight GitHub deliveries signed with
// the previous key keep verifying so the operator can update GitHub's UI
// without dropping deliveries.
//
// Atomic: write new tmp → fsync → mv old → .prev → rename new → live. If
// any step fails the live file is untouched.
func RotateSecret(deploy string) (string, error) {
	s, err := generate()
	if err != nil {
		return "", err
	}
	live := SecretPath(deploy)
	prev := PrevSecretPath(deploy)
	// Preserve current as .prev when one exists. Touch mtime so the
	// grace-window check reads accurately.
	if cur, err := os.ReadFile(live); err == nil {
		if werr := writeSecret(prev, strings.TrimSpace(string(cur))); werr != nil {
			return "", fmt.Errorf("save previous secret: %w", werr)
		}
		now := time.Now()
		_ = os.Chtimes(prev, now, now)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read live secret: %w", err)
	}
	if err := writeSecret(live, s); err != nil {
		return "", err
	}
	return s, nil
}

// LoadValidSecrets returns every secret that should currently verify a
// webhook delivery: the live secret plus (if present and within
// RotationGrace) the previous one. Server.go iterates these and accepts
// the first hex-matching signature.
//
// Returns nil + error only when there's no live secret at all.
func LoadValidSecrets(deploy string) ([][]byte, error) {
	live, err := LoadSecret(deploy)
	if err != nil {
		return nil, err
	}
	out := [][]byte{[]byte(live)}
	if b, err := os.ReadFile(PrevSecretPath(deploy)); err == nil {
		if st, serr := os.Stat(PrevSecretPath(deploy)); serr == nil {
			if time.Since(st.ModTime()) <= RotationGrace {
				out = append(out, []byte(strings.TrimSpace(string(b))))
			} else {
				// Expired — best-effort cleanup so stale .prev files don't accrete.
				_ = os.Remove(PrevSecretPath(deploy))
			}
		}
	}
	return out, nil
}

// LoadSecret reads a previously-generated secret. Returns an error if the
// file doesn't exist — callers should call EnsureSecret unless they're sure
// the secret was created earlier.
func LoadSecret(deploy string) (string, error) {
	b, err := os.ReadFile(SecretPath(deploy))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// ListSecrets returns the deploy names that have a secret on disk.
func ListSecrets() ([]string, error) {
	entries, err := os.ReadDir(SecretDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".secret")
		if name == e.Name() {
			continue // not a .secret file
		}
		out = append(out, name)
	}
	return out, nil
}

// RemoveSecret deletes the secret file for `deploy` (and any .prev grace
// copy). Called from `apigw deploy remove --purge`.
func RemoveSecret(deploy string) error {
	_ = os.Remove(PrevSecretPath(deploy))
	err := os.Remove(SecretPath(deploy))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func generate() (string, error) {
	b := make([]byte, SecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func writeSecret(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(value); err != nil {
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
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
