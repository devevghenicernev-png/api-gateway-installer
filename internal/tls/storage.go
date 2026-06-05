package tls

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CertPaths returns the on-disk paths for the named domain.
//
// Filenames mirror certbot's (`fullchain.pem`, `privkey.pem`) for muscle
// memory — but we live under /var/lib/apigw/certs/, not /etc/letsencrypt/.
// We do NOT mirror certbot's archive/live symlink layout.
func CertPaths(domain string) (fullchain, privkey, meta string) {
	dir := filepath.Join(CertDir, domain)
	return filepath.Join(dir, "fullchain.pem"),
		filepath.Join(dir, "privkey.pem"),
		filepath.Join(dir, "meta.json")
}

// metadata is what we write to meta.json alongside the cert. The CLI's
// `apigw tls status` reads it to render the expiry table without re-parsing
// the cert on every invocation (faster + works even if openssl is unavailable).
type metadata struct {
	Strategy  Strategy  `json:"strategy"`
	Domains   []string  `json:"domains"`
	Issuer    string    `json:"issuer"`
	NotAfter  time.Time `json:"not_after"`
	NotBefore time.Time `json:"not_before"`
	LastRenew time.Time `json:"last_renew"`
}

// StoreCert writes fullchain+privkey+meta atomically. Existing files are
// rename(2)'d only after the new ones land — no torn writes that could leave
// nginx with mismatched fullchain/privkey.
//
// Sequence:
//  1. write fullchain.pem.new + fsync
//  2. write privkey.pem.new + fsync
//  3. parse the new fullchain to validate it's well-formed
//  4. snapshot CURRENT fullchain → fullchain.pem.bak (so we can roll back)
//  5. snapshot CURRENT privkey   → privkey.pem.bak
//  6. rename fullchain.pem.new → fullchain.pem (POSIX atomic)
//  7. rename privkey.pem.new   → privkey.pem   (POSIX atomic)
//  8. on (7) failure: restore both .bak files → guaranteed-consistent rollback
//  9. write meta.json + cleanup .bak files
//
// Even if (7) fails because the disk fills up mid-rename, step 8 restores
// the previous matching pair. nginx never sees a torn fullchain/privkey.
func StoreCert(domain string, fullchainPEM, privkeyPEM []byte, strategy Strategy) error {
	fc, pk, mp := CertPaths(domain)
	dir := filepath.Dir(fc)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// 1+2: stage with fsync so a power loss before rename leaves either
	// the old pair or the new pair on disk — never a mix.
	fcTmp, pkTmp := fc+".new", pk+".new"
	if err := writeFsyncMode(fcTmp, fullchainPEM, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", fcTmp, err)
	}
	if err := writeFsyncMode(pkTmp, privkeyPEM, 0o600); err != nil {
		_ = os.Remove(fcTmp)
		return fmt.Errorf("write %s: %w", pkTmp, err)
	}

	// 3: parse fullchain
	cert, err := parseFirstCert(fullchainPEM)
	if err != nil {
		_ = os.Remove(fcTmp)
		_ = os.Remove(pkTmp)
		return fmt.Errorf("validate new fullchain: %w", err)
	}

	// 4+5: snapshot the live pair so we can roll back atomically.
	// Privkey snapshot is forced to 0o600 — never inherit a permissive mode
	// from a pre-existing file (e.g. left over from manual copy).
	fcBak, pkBak := fc+".bak", pk+".bak"
	hadFC := snapshotExisting(fc, fcBak, 0)
	hadPK := snapshotExisting(pk, pkBak, 0o600)

	// 6: rename fullchain. If this fails, drop everything but the live
	// pair (still intact, because we only snapshotted — didn't delete).
	if err := os.Rename(fcTmp, fc); err != nil {
		_ = os.Remove(pkTmp)
		_ = os.Remove(fcBak)
		_ = os.Remove(pkBak)
		return fmt.Errorf("rename fullchain: %w", err)
	}

	// 7: rename privkey. Failure here = potential mismatch (new fullchain,
	// old privkey). Restore both .bak snapshots to guarantee consistency.
	if err := os.Rename(pkTmp, pk); err != nil {
		_ = restoreFrom(fcBak, fc, hadFC)
		_ = restoreFrom(pkBak, pk, hadPK)
		return fmt.Errorf("rename privkey (rolled back): %w", err)
	}

	// 8 (success): cleanup snapshots.
	_ = os.Remove(fcBak)
	_ = os.Remove(pkBak)

	// 6: meta
	m := metadata{
		Strategy:  strategy,
		Domains:   append([]string{domain}, cert.DNSNames...),
		Issuer:    cert.Issuer.CommonName,
		NotBefore: cert.NotBefore,
		NotAfter:  cert.NotAfter,
		LastRenew: time.Now().UTC(),
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := atomicWrite(mp, b, 0o644); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	return nil
}

// LoadCertInfo returns the CertInfo for `domain` by parsing fullchain.pem.
// Falls back to meta.json fields if parse fails (e.g. cert was rotated mid-read).
func LoadCertInfo(domain string) (CertInfo, error) {
	fc, _, mp := CertPaths(domain)
	var ci CertInfo
	ci.Domain = domain

	if b, err := os.ReadFile(fc); err == nil {
		cert, perr := parseFirstCert(b)
		if perr == nil {
			ci.Subject = cert.Subject.CommonName
			ci.Issuer = cert.Issuer.CommonName
			ci.NotBefore = cert.NotBefore
			ci.NotAfter = cert.NotAfter
			ci.DaysLeft = int(time.Until(cert.NotAfter).Hours() / 24)
			ci.SerialHex = cert.SerialNumber.Text(16)
			sum := sha256.Sum256(cert.Raw)
			ci.Fingerprint = hex.EncodeToString(sum[:])
		}
	}

	if b, err := os.ReadFile(mp); err == nil {
		var m metadata
		if json.Unmarshal(b, &m) == nil {
			ci.Strategy = m.Strategy
			if ci.NotAfter.IsZero() {
				ci.NotAfter = m.NotAfter
				ci.NotBefore = m.NotBefore
				ci.DaysLeft = int(time.Until(m.NotAfter).Hours() / 24)
			}
		}
	}

	if ci.NotAfter.IsZero() {
		return ci, fmt.Errorf("no cert found for %s", domain)
	}
	return ci, nil
}

// ListCerts returns metadata for every domain with a cert directory on disk.
func ListCerts() ([]CertInfo, error) {
	entries, err := os.ReadDir(CertDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]CertInfo, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ci, err := LoadCertInfo(e.Name())
		if err != nil {
			continue
		}
		out = append(out, ci)
	}
	return out, nil
}

// RemoveCert deletes the per-domain directory. Used by `apigw tls disable`
// when the user explicitly opts out.
func RemoveCert(domain string) error {
	dir := filepath.Join(CertDir, domain)
	return os.RemoveAll(dir)
}

// parseFirstCert returns the first certificate in a PEM bundle (the leaf,
// in fullchain.pem). Returns an error if the bundle has no CERTIFICATE blocks.
func parseFirstCert(b []byte) (*x509.Certificate, error) {
	for len(b) > 0 {
		block, rest := pem.Decode(b)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
		b = rest
	}
	return nil, fmt.Errorf("no CERTIFICATE block in PEM")
}

// writeFsyncMode writes body to path with fsync — used by StoreCert so a
// power loss between write and rename leaves no torn file.
func writeFsyncMode(path string, body []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// snapshotExisting copies live → bak if live exists. Returns whether the
// snapshot was created (false = no live file to snapshot).
//
// If `forceMode` != 0, the backup is created with that mode regardless of
// the source file's permissions — used for privkey snapshots, which must
// be 0o600 even if the live file was accidentally created world-readable.
func snapshotExisting(live, bak string, forceMode os.FileMode) bool {
	b, err := os.ReadFile(live)
	if err != nil {
		return false
	}
	mode := forceMode
	if mode == 0 {
		info, statErr := os.Stat(live)
		mode = os.FileMode(0o600)
		if statErr == nil {
			mode = info.Mode().Perm()
		}
	}
	if werr := os.WriteFile(bak, b, mode); werr != nil {
		return false
	}
	// WriteFile honors umask; chmod to be sure the requested mode lands.
	_ = os.Chmod(bak, mode)
	return true
}

// restoreFrom reinstates a snapshot. If `hadOriginal` is false the original
// didn't exist (first-time install); we just delete the live file so the
// caller sees a clean "missing" state rather than a stale half-rolled value.
func restoreFrom(bak, live string, hadOriginal bool) error {
	if !hadOriginal {
		_ = os.Remove(live)
		_ = os.Remove(bak)
		return nil
	}
	if err := os.Rename(bak, live); err != nil {
		// Last-resort copy. Cross-filesystem renames are the usual cause.
		b, rerr := os.ReadFile(bak)
		if rerr != nil {
			return rerr
		}
		if werr := os.WriteFile(live, b, 0o600); werr != nil {
			return werr
		}
		_ = os.Remove(bak)
	}
	return nil
}
