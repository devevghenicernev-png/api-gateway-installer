//go:build e2e

package e2e_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/devevghenicernev-png/apigw/test/e2e/harness"
)

// TestWebhook spins one container per distro and exercises the receiver:
// generate a secret, sign a payload, POST it, expect 202 — then forge a
// signature and expect 401. Separate from TestScenarios because it needs
// `apigw deploy add --no-start` first which would muddy the install flow.
func TestWebhook(t *testing.T) {
	for _, distro := range pickDistros() {
		distro := distro
		t.Run(string(distro), func(t *testing.T) {
			t.Parallel()
			image := harness.BuildImage(t, distro)
			c := harness.Launch(t, image, hostBinary(t))

			// Minimal install just to materialise /etc/apigw + nginx.
			c.MustExec(5*time.Second, "systemctl", "start", "nginx")
			c.CopyIn(t, "docker/install.yml", "/tmp/install.yml")
			c.MustExec(60*time.Second, "apigw", "install",
				"--config-file", "/tmp/install.yml", "--yes")

			// Register a deploy WITHOUT cloning so we have a name for the
			// webhook route. --no-start means apigw won't try to fetch the
			// (fake) repo.
			c.MustExec(15*time.Second,
				"apigw", "deploy", "add", "hello",
				"--repo", "https://github.com/owner/repo",
				"--port", "0",
				"--runtime", "static",
				"--no-start", "--yes",
			)

			// Start the dashboard daemon (which embeds the webhook server).
			c.MustExec(30*time.Second, "apigw", "dashboard", "start")
			waitForListen(t, c, 9000, 10*time.Second)

			// "copy it now" is unique to the secret banner; the next line is
			// the hex secret. Earlier match (`grep -A1 'Secret'`) tripped on
			// the help text's "3. paste Payload URL and Secret from above"
			// section and printed the wrong line as the "secret".
			secret := c.MustExec(10*time.Second, "/bin/sh", "-c",
				"apigw webhook setup hello --yes | grep -A1 'copy it now' | tail -1 | awk '{print $1}'")
			secret = strings.TrimSpace(secret)
			if len(secret) < 16 {
				t.Fatalf("secret looks bogus: %q", secret)
			}

			body := `{"ref":"refs/heads/main","after":"abc1234","repository":{"clone_url":"https://github.com/owner/repo","full_name":"owner/repo","default_branch":"main"}}`
			sig := sign(secret, body)
			out := c.MustExec(15*time.Second,
				"curl", "-sS", "-X", "POST",
				"-H", "Content-Type: application/json",
				"-H", "X-GitHub-Event: push",
				"-H", "X-GitHub-Delivery: abc-123",
				"-H", "X-Hub-Signature-256: "+sig,
				"--data", body,
				"-o", "/dev/null", "-w", "%{http_code}",
				"http://127.0.0.1:9000/webhook/hello",
			)
			code := strings.TrimSpace(out)
			if code != "202" && code != "200" {
				t.Fatalf("expected 202/200, got %s", code)
			}

			// Now forge a signature; expect 401.
			out = c.MustExec(15*time.Second,
				"curl", "-sS", "-X", "POST",
				"-H", "Content-Type: application/json",
				"-H", "X-GitHub-Event: push",
				"-H", "X-GitHub-Delivery: forged-1",
				"-H", "X-Hub-Signature-256: sha256=deadbeef",
				"--data", body,
				"-o", "/dev/null", "-w", "%{http_code}",
				"http://127.0.0.1:9000/webhook/hello",
			)
			code = strings.TrimSpace(out)
			if code != "401" {
				t.Fatalf("expected 401 on forged signature, got %s", code)
			}
		})
	}
}

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// waitForListen blocks until something is bound to `port` inside the
// container, or the deadline expires.
func waitForListen(t *testing.T, c *harness.Container, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := c.Exec(2*time.Second, "/bin/sh", "-c",
			`ss -lnt sport = :`+itoa(port)+` 2>/dev/null | grep -q LISTEN && echo yes || echo no`)
		if err == nil && strings.Contains(out, "yes") {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("nothing listening on :%d after %s", port, timeout)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [16]byte
	idx := len(b)
	for i > 0 {
		idx--
		b[idx] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		idx--
		b[idx] = '-'
	}
	return string(b[idx:])
}
