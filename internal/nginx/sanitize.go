package nginx

import (
	"fmt"
	"regexp"
	"strings"
)

// safeNameRE constrains entity names — these flow into log identifiers,
// directory names, and (via templates) systemd unit specifiers. The CLI
// add-commands validate against a similar regex; we re-check here as
// defense-in-depth so a hand-edited config.yaml can't break out.
var safeNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

// safePathRE keeps URL paths to characters nginx can interpret safely.
// Allows: lowercase + digits, /, -, _, dot. Rejects: ; { } \r \n " ' \
// space (the canonical nginx-config-injection vectors).
var safePathRE = regexp.MustCompile(`^/[a-zA-Z0-9._/-]*$`)

// sanitizeName returns an error if `name` contains characters nginx will
// either misinterpret or treat as a directive boundary.
func sanitizeName(name string) error {
	if !safeNameRE.MatchString(name) {
		return fmt.Errorf("name %q is invalid: must match %s", name, safeNameRE.String())
	}
	return nil
}

// sanitizePath enforces a strict allowlist on URL paths emitted into
// `location` directives. Any character that could close the directive
// (`;`, `{`, `}`) or inject a new one (`\n`) is rejected outright.
//
// The check happens at generator time, not at CLI parse time, so a
// malicious user who edited /etc/apigw/config.yaml directly still trips
// this gate before nginx ever sees the bad config.
func sanitizePath(path string) error {
	if path == "" {
		return fmt.Errorf("path is empty")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("path %q must start with /", path)
	}
	// Cheap explicit blacklist for the highest-signal characters — keeps
	// the rejection message specific even when the regex would catch them.
	for _, bad := range []rune{';', '{', '}', '\n', '\r', '"', '\'', '\\', 0} {
		if strings.ContainsRune(path, bad) {
			return fmt.Errorf("path %q contains illegal character %q", path, bad)
		}
	}
	if !safePathRE.MatchString(path) {
		return fmt.Errorf("path %q is invalid: must match %s", path, safePathRE.String())
	}
	return nil
}

// sanitizeServerName accepts a single nginx server_name value (no spaces;
// "_" is a valid catch-all). Multiple names are joined by the caller; we
// validate each token.
func sanitizeServerName(name string) error {
	if name == "_" {
		return nil
	}
	if name == "" {
		return fmt.Errorf("server_name is empty")
	}
	// hostname-like: letters, digits, dot, hyphen.
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '-' || r == '*'
		if !ok {
			return fmt.Errorf("server_name %q contains illegal character %q", name, r)
		}
	}
	return nil
}
