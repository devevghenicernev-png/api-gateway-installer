package deploy

import (
	"bytes"
	"io"
	"regexp"
)

// secretScrubber wraps an io.Writer and rewrites likely-credential values
// before forwarding bytes downstream. Used to keep `npm install` /
// `set -x` style debug output from leaking tokens into journald or the
// SSE log feed.
//
// It is deliberately heuristic — full secret detection is a lost cause in
// arbitrary build output. The rules are:
//
//  1. KEY=VALUE pairs where KEY matches an obvious secret name
//     (PASS, PASSWORD, SECRET, TOKEN, KEY, CRED*).
//  2. Authorization-header lines: "Authorization: <anything>" → masked.
//  3. URLs with embedded user:pass — "://user:pass@" → "://user:***@".
//
// Replacement always renders "***" — fixed length so layout-based attacks
// can't infer secret length from output. Detection is line-oriented; bytes
// are buffered to a newline before scrub-and-forward so a multi-byte regex
// doesn't fragment.
type secretScrubber struct {
	w   io.Writer
	buf bytes.Buffer
}

func newSecretScrubber(w io.Writer) *secretScrubber {
	return &secretScrubber{w: w}
}

func (s *secretScrubber) Write(p []byte) (int, error) {
	n := len(p)
	s.buf.Write(p)
	for {
		idx := bytes.IndexByte(s.buf.Bytes(), '\n')
		if idx < 0 {
			break
		}
		line := s.buf.Next(idx + 1)
		if _, err := s.w.Write(scrubBytes(line)); err != nil {
			return n, err
		}
	}
	return n, nil
}

// Flush writes any partial trailing line. Callers should invoke this in a
// defer when they're done feeding data; it never blocks.
func (s *secretScrubber) Flush() error {
	if s.buf.Len() == 0 {
		return nil
	}
	out := scrubBytes(s.buf.Bytes())
	s.buf.Reset()
	_, err := s.w.Write(out)
	return err
}

var (
	// Match KEY=VALUE where KEY ends with a secret-sounding word.
	reEnvSecret = regexp.MustCompile(`(?i)([A-Z0-9_]*(?:PASS|PASSWORD|SECRET|TOKEN|KEY|CRED|APIKEY)[A-Z0-9_]*)\s*[:=]\s*\S+`)
	// Authorization header dumps (curl -v, Express middleware logging).
	// `.+` (not `\S+`) so we eat both the scheme word and the credential
	// after it, all the way to end-of-line — `Authorization: Bearer xyz` ⇒
	// `Authorization: ***` (no token survives).
	reAuthHeader = regexp.MustCompile(`(?i)(authorization\s*:\s*).+`)
	// userinfo in URL.
	reURLCreds = regexp.MustCompile(`(://[^/@\s:]+):[^@\s]+@`)
)

func scrubBytes(line []byte) []byte {
	line = reEnvSecret.ReplaceAll(line, []byte("$1=***"))
	line = reAuthHeader.ReplaceAll(line, []byte("$1***"))
	line = reURLCreds.ReplaceAll(line, []byte("$1:***@"))
	return line
}
