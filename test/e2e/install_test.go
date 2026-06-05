//go:build e2e

package e2e_test

import (
	"strings"
	"testing"
	"time"

	"github.com/devevghenicernev-png/apigw/test/e2e/harness"
)

// TestScenarios is the entry point. We run the full life-cycle inside one
// container per distro because each step exercises real systemd units the
// previous step installed. Splitting into per-test containers would mean
// 60s of systemd boot × 6 scenarios = 6 minutes wasted per distro.
func TestScenarios(t *testing.T) {
	for _, distro := range pickDistros() {
		distro := distro
		t.Run(string(distro), func(t *testing.T) {
			t.Parallel()
			image := harness.BuildImage(t, distro)
			c := harness.Launch(t, image, hostBinary(t))

			t.Run("version", func(t *testing.T) { testVersion(t, c) })
			t.Run("install", func(t *testing.T) { testInstall(t, c) })
			t.Run("api_add_curl_remove", func(t *testing.T) { testAPI(t, c) })
			t.Run("tls_self_signed", func(t *testing.T) { testTLSSelfSigned(t, c) })
			t.Run("doctor", func(t *testing.T) { testDoctor(t, c) })
			t.Run("backup_restore", func(t *testing.T) { testBackupRestore(t, c) })
		})
	}
}

// ----- step 1: version ----------------------------------------------------

func testVersion(t *testing.T, c *harness.Container) {
	out := c.MustExec(5*time.Second, "apigw", "version")
	if !strings.Contains(out, "apigw") {
		t.Fatalf("expected `apigw <ver>` line, got:\n%s", out)
	}
}

// ----- step 2: install (HTTP only, unattended) ----------------------------

func testInstall(t *testing.T, c *harness.Container) {
	// Drop install.yml answers + start nginx (apigw install assumes nginx is
	// already installed; the Dockerfiles install it but leave it stopped).
	c.MustExec(5*time.Second, "systemctl", "start", "nginx")
	c.CopyIn(t, "docker/install.yml", "/tmp/install.yml")
	out := c.MustExec(60*time.Second,
		"apigw", "install",
		"--config-file", "/tmp/install.yml",
		"--yes",
	)
	if !strings.Contains(out, "apigw is ready") {
		t.Fatalf("install did not finish cleanly:\n%s", out)
	}
	// nginx must be alive.
	c.MustExec(5*time.Second, "systemctl", "is-active", "--quiet", "nginx")
}

// ----- step 3: api lifecycle ----------------------------------------------

func testAPI(t *testing.T, c *harness.Container) {
	// Spin a tiny upstream so `curl /api/hello` returns 200.
	c.MustExec(5*time.Second, "/bin/sh", "-c",
		`(while true; do printf "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello" | nc -lq1 -p 3000 >/dev/null 2>&1 || true; done) &`)
	// Some images don't ship netcat — fall back to a python one-liner.
	c.MustExec(5*time.Second, "/bin/sh", "-c",
		`command -v nc >/dev/null || command -v python3 >/dev/null && python3 -c '`+
			`import http.server,socketserver,threading;`+
			`h=http.server.BaseHTTPRequestHandler;`+
			`h.do_GET=lambda s:(s.send_response(200),s.end_headers(),s.wfile.write(b"hello"));`+
			`t=threading.Thread(target=lambda:socketserver.TCPServer(("",3000),h).serve_forever(),daemon=True);t.start();`+
			`import time;time.sleep(3600)' &`)

	c.MustExec(15*time.Second, "apigw", "api", "add", "hello",
		"--port", "3000", "--path", "/api/hello", "--yes")
	body, code := c.HTTPGet(t, "/api/hello")
	if code != 200 {
		t.Fatalf("expected 200 from /api/hello, got %d (body=%q)", code, body)
	}
	if !strings.Contains(body, "hello") {
		t.Fatalf("expected body to contain 'hello', got %q", body)
	}

	c.MustExec(10*time.Second, "apigw", "api", "remove", "hello", "--yes")
	_, code = c.HTTPGet(t, "/api/hello")
	if code != 404 {
		t.Fatalf("expected 404 after remove, got %d", code)
	}
}

// ----- step 4: tls self-signed --------------------------------------------

func testTLSSelfSigned(t *testing.T, c *harness.Container) {
	out := c.MustExec(60*time.Second,
		"apigw", "tls", "enable", "self-signed",
		"--cn", "apigw.local",
		"--no-timer",
		"--yes",
	)
	if !strings.Contains(out, "certificate installed") {
		t.Fatalf("tls enable did not report install:\n%s", out)
	}
	// nginx is now listening on 443 with the new cert.
	out = c.MustExec(10*time.Second,
		"curl", "-sS", "-k", "-o", "/dev/null", "-w", "%{http_code}",
		"https://127.0.0.1/",
	)
	if !strings.HasPrefix(strings.TrimSpace(out), "4") &&
		!strings.HasPrefix(strings.TrimSpace(out), "2") {
		t.Fatalf("expected 2xx/4xx over TLS, got %q", out)
	}
}

// ----- step 5: doctor -----------------------------------------------------

func testDoctor(t *testing.T, c *harness.Container) {
	// Doctor exits non-zero on warns/fails; we accept exit 1 (warn) because
	// some checks (disk free, TLS-expiry) flag info-level on a fresh box.
	out, err := c.Exec(30*time.Second, "apigw", "doctor")
	if err != nil {
		// Exit 1 = warn — still acceptable; exit 2 = fail isn't.
		if !strings.Contains(err.Error(), "exit status 1") {
			t.Fatalf("doctor failed:\n%s\nerr=%v", out, err)
		}
	}
	if !strings.Contains(out, "nginx-config") {
		t.Fatalf("doctor output missing core checks:\n%s", out)
	}
}

// ----- step 6: backup + restore -------------------------------------------

func testBackupRestore(t *testing.T, c *harness.Container) {
	// Pack to /tmp/backup.tar.gz.
	out := c.MustExec(30*time.Second,
		"apigw", "backup", "--out", "/tmp/backup.tar.gz",
	)
	if !strings.Contains(out, "Backup created") {
		t.Fatalf("backup did not report create:\n%s", out)
	}
	// Force-restore over the same paths — content-identical, so even
	// without --force it would no-op, but we exercise the flag.
	out = c.MustExec(30*time.Second,
		"apigw", "restore", "/tmp/backup.tar.gz",
		"--force",
		"--yes",
	)
	if !strings.Contains(out, "restored") {
		t.Fatalf("restore did not report success:\n%s", out)
	}
}
