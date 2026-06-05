// Package backup packs apigw's state into a tar.gz and restores it.
//
// What's IN: /etc/apigw, /var/lib/apigw/acme, /var/lib/apigw/certs, plus
// (optional) /var/lib/apigw/jobs.db. Per-deploy release dirs are NOT
// backed up — they rebuild from git, so including them just makes the
// archive 10× larger.
//
// What's OUT: /var/lib/apigw/<name>/releases/, /var/log/apigw, anything
// under /tmp, secrets that we generate fresh on first use (account.key
// excepted — that one IS in the backup because regenerating it costs an
// ACME registration).
//
// Manifest is a sidecar JSON inside the tar: schema version + file list +
// SHA-256 per file. Restore refuses to extract an archive whose schema is
// newer than the binary's, and verifies hashes as it goes.
package backup

import "time"

// SchemaVersion is the on-disk manifest schema. Bump when the layout of
// what we pack changes incompatibly. Restore validates this.
const SchemaVersion = 1

// Manifest is the sidecar JSON written as `manifest.json` at the root of
// the tar. Stable shape — operators may script against it.
type Manifest struct {
	SchemaVersion int        `json:"schema_version"`
	APIGWVersion  string     `json:"apigw_version"`
	APIGWCommit   string     `json:"apigw_commit"`
	CreatedAt     time.Time  `json:"created_at"`
	Host          string     `json:"host"`
	Files         []FileSpec `json:"files"`

	// IncludesQueue records whether bbolt jobs.db was packed — restore
	// skips it on a clean host even if present, unless --include-queue.
	IncludesQueue bool `json:"includes_queue"`
}

// FileSpec describes one packed file. Mode is the original 0o400-style
// permission; restore preserves it. SHA256 lets us detect corruption.
type FileSpec struct {
	Path   string `json:"path"`   // logical path inside the archive
	Origin string `json:"origin"` // absolute path on the source host
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256"`
}

// roots is the canonical set of source paths backed up. Defined once so
// pack + describe + the doctor check agree on what's inside.
var roots = []string{
	"/etc/apigw",
	"/var/lib/apigw/acme",
	"/var/lib/apigw/certs",
}

// optionalRoots are only packed when the caller opts in. jobs.db is here
// because restoring it onto a fresh host could re-trigger old deploys.
var optionalRoots = []string{
	"/var/lib/apigw/jobs.db",
}
