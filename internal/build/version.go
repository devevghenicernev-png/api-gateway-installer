// Package build holds ldflags-injected build identifiers.
//
// Values are set via -X github.com/.../internal/build.Version=... at link time.
// Defaults below are what `go run` and unstamped builds report.
package build

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func Info() (string, string, string) { return Version, Commit, Date }
