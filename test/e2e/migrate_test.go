//go:build e2e

package e2e_test

import (
	"strings"
	"testing"
	"time"

	"github.com/devevghenicernev-png/apigw/test/e2e/harness"
)

// TestMigrate seeds /etc/api-gateway/apis.json (legacy bash format) and
// verifies `apigw migrate` translates it into the new config and leaves
// the old file renamed-but-present. Runs against the first distro only —
// migration logic is filesystem-shaped and OS-agnostic.
func TestMigrate(t *testing.T) {
	distros := pickDistros()
	distro := distros[0]
	image := harness.BuildImage(t, distro)
	c := harness.Launch(t, image, hostBinary(t))

	c.MustExec(5*time.Second, "systemctl", "start", "nginx")

	// Seed: an apis.json with two entries + one deployment config.
	const legacyAPIs = `{
  "apis": [
    {"name": "billing", "path": "/billing", "port": 8081, "description": "billing svc", "enabled": true, "type": "api"},
    {"name": "ai-ollama", "path": "/ai/ollama", "port": 11434, "description": "Ollama", "enabled": true, "type": "ai-model"}
  ]
}`
	c.MustExec(5*time.Second, "/bin/sh", "-c",
		`mkdir -p /etc/api-gateway/deployments && cat > /etc/api-gateway/apis.json <<'EOF'
`+legacyAPIs+`
EOF`)

	const legacyDeploy = `{
  "service_name": "hello",
  "github_repo": "https://github.com/owner/hello",
  "branch": "main",
  "port": 3000,
  "build_command": "auto",
  "start_command": "auto",
  "runtime": "auto",
  "webhook_secret": "deadbeefdeadbeefdeadbeefdeadbeef",
  "auto_deploy": true,
  "process_manager": "systemd",
  "status": "configured"
}`
	c.MustExec(5*time.Second, "/bin/sh", "-c",
		`cat > /etc/api-gateway/deployments/hello.json <<'EOF'
`+legacyDeploy+`
EOF`)

	// Minimal new-side install so /etc/apigw exists.
	c.CopyIn(t, "docker/install.yml", "/tmp/install.yml")
	c.MustExec(60*time.Second, "apigw", "install",
		"--config-file", "/tmp/install.yml", "--yes")

	out := c.MustExec(60*time.Second, "apigw", "migrate",
		"--skip-backup", "--skip-shim", "--yes")
	if !strings.Contains(out, "migration complete") {
		t.Fatalf("migrate did not finish:\n%s", out)
	}

	// Verify the new config picked up both entries.
	listing := c.MustExec(10*time.Second, "apigw", "api", "list", "--json")
	for _, want := range []string{"billing", "ai-ollama"} {
		if !strings.Contains(listing, want) {
			t.Fatalf("api %q missing after migrate:\n%s", want, listing)
		}
	}
	deploys := c.MustExec(10*time.Second, "apigw", "deploy", "list", "--json")
	if !strings.Contains(deploys, "hello") {
		t.Fatalf("deploy hello missing after migrate:\n%s", deploys)
	}

	// Legacy apis.json must be renamed-but-present.
	out = c.MustExec(5*time.Second, "/bin/sh", "-c",
		`ls /etc/api-gateway/apis.json.migrated-* 2>/dev/null | wc -l`)
	if strings.TrimSpace(out) == "0" {
		t.Fatal("expected legacy apis.json to be renamed with .migrated-<ts>")
	}

	// Idempotency — second migrate should report no changes.
	out = c.MustExec(30*time.Second, "apigw", "migrate", "--skip-backup", "--skip-shim", "--yes")
	if !strings.Contains(out, "nothing to do") &&
		!strings.Contains(out, "No legacy installation detected") {
		t.Fatalf("second migrate should be a no-op, got:\n%s", out)
	}
}
