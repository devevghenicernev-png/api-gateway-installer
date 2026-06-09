package diag

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/config"
	apideploy "github.com/devevghenicernev-png/apigw/internal/deploy"
	"github.com/devevghenicernev-png/apigw/internal/fips"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/paths"
	"github.com/devevghenicernev-png/apigw/internal/system/svcmgr"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
)

// svcmgrActive is a thin indirection so we can mock svcmgr in tests without
// importing it from every check function.
func svcmgrActive() svcmgr.Kind { return svcmgr.Active().Kind() }

// All returns the canonical check set apigw doctor runs. The slice ordering
// is intentional — system requirements first, then config integrity, then
// per-deploy details — so the doctor table reads top-down by abstraction
// level.
func All(cfg *config.Config) []Check {
	return []Check{
		{
			ID:          "platform",
			Description: "OS and binary architecture sanity",
			Run:         checkPlatform,
		},
		{
			ID:          "nginx-binary",
			Description: "nginx is installed and resolvable",
			Run:         checkNginxBinary,
		},
		{
			ID:          "nginx-config",
			Description: "nginx -t passes on the live config",
			Run:         checkNginxConfig,
		},
		{
			ID:          "nginx-managed-banner",
			Description: "apigw-written nginx site has its managed banner",
			Run:         checkManagedBanner,
		},
		{
			ID:          "systemctl",
			Description: "systemctl is present (we drive every unit through it)",
			Run:         checkSystemctl,
		},
		{
			ID:          "ports-http",
			Description: "something is bound to the public HTTP port",
			Run:         portCheck("http", listenPort(cfg.Listen.HTTPPort, 80)),
		},
		{
			ID:          "ports-https",
			Description: "something is bound to the public HTTPS port (if TLS enabled)",
			Run:         portsHTTPSCheck(cfg),
		},
		{
			ID:          "tls-expiry",
			Description: "no certificate expires within 14 days",
			Run:         checkTLSExpiry,
		},
		{
			ID:          "config-perms",
			Description: "/etc/apigw permissions are restrictive",
			Run:         checkConfigPerms,
		},
		{
			ID:          "webhook-secrets",
			Description: "every deploy with webhooks has a secret on disk",
			Run:         webhookSecretsCheck(cfg),
		},
		{
			ID:          "ssh-deploy-key",
			Description: "SSH deploy key exists when any deploy uses ssh:// or git@",
			Run:         sshDeployKeyCheck(cfg),
		},
		{
			ID:          "deploy-units",
			Description: "every enabled deploy has a systemd unit active",
			Run:         deployUnitsCheck(cfg),
		},
		{
			ID:          "queue-depth",
			Description: "webhook queue has no stuck dead-letters",
			Run:         queueDepthCheck,
		},
		{
			ID:          "disk-free",
			Description: "/var/lib/apigw has at least 1 GiB free",
			Run:         diskFreeCheck,
		},
	}
}

// ---------- individual checks ----------

func checkPlatform(_ context.Context) Result {
	msg := fmt.Sprintf("%s/%s (Go %s)", runtime.GOOS, runtime.GOARCH, runtime.Version())
	if fips.Enabled() {
		msg += " — FIPS mode (BoringCrypto)"
	}
	return Result{Level: LevelOK, Name: "platform", Message: msg}
}

func checkNginxBinary(_ context.Context) Result {
	path, err := exec.LookPath("nginx")
	if err != nil {
		return Result{
			Level:   LevelFail,
			Name:    "nginx-binary",
			Message: "nginx not on PATH",
			Fix:     installHint("nginx"),
		}
	}
	return Result{
		Level:   LevelOK,
		Name:    "nginx-binary",
		Message: path,
	}
}

// installHint returns the platform-appropriate install command for `pkg`.
// Linux assumes Debian/Ubuntu apt; macOS assumes Homebrew; Alpine apk.
func installHint(pkg string) string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install " + pkg
	case "linux":
		if _, err := exec.LookPath("apk"); err == nil {
			return "sudo apk add " + pkg
		}
		if _, err := exec.LookPath("dnf"); err == nil {
			return "sudo dnf install -y " + pkg
		}
		return "sudo apt-get install -y " + pkg
	}
	return "install " + pkg + " for your platform"
}

func checkNginxConfig(ctx context.Context) Result {
	cmd := exec.CommandContext(ctx, "nginx", "-t")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Result{
			Level:   LevelFail,
			Name:    "nginx-config",
			Message: "nginx -t failed",
			Fix:     "sudo nginx -t",
			Details: map[string]string{"stderr": stderr.String()},
		}
	}
	return Result{Level: LevelOK, Name: "nginx-config", Message: "nginx -t ok"}
}

// checkManagedBanner verifies that apigw's nginx site still begins with the
// `# MANAGED BY apigw …` line. If someone hand-edited the file, the banner
// (or its hash) won't match — we flag the divergence so an unexpected
// `apigw api add` doesn't blow away their tweaks unannounced.
func checkManagedBanner(_ context.Context) Result {
	b, err := os.ReadFile(nginx.SitePath)
	if errors.Is(err, os.ErrNotExist) {
		return Result{
			Level:   LevelInfo,
			Name:    "nginx-managed-banner",
			Message: "no apigw-managed nginx site found (run `apigw install`)",
		}
	}
	if err != nil {
		return Result{Level: LevelWarn, Name: "nginx-managed-banner", Message: err.Error()}
	}
	first := firstLine(b)
	if !strings.HasPrefix(first, "# MANAGED BY apigw") {
		return Result{
			Level:   LevelWarn,
			Name:    "nginx-managed-banner",
			Message: "site has been hand-edited (banner missing)",
			Fix:     "apigw api reload  # to reset to a managed config",
		}
	}
	// Optional follow-up: hash check. The banner records a 16-char SHA prefix;
	// re-derive from the body and warn on mismatch.
	if expected := extractHash(first); expected != "" {
		actual := bannerlessHash(b)
		if actual != expected {
			return Result{
				Level:   LevelWarn,
				Name:    "nginx-managed-banner",
				Message: "body hash drift — file was edited after apigw wrote it",
				Fix:     "apigw api reload",
			}
		}
	}
	return Result{Level: LevelOK, Name: "nginx-managed-banner", Message: "banner intact"}
}

func checkSystemctl(_ context.Context) Result {
	// Use svcmgr to report the actual supervisor in use rather than treating
	// "no systemctl" as a hard failure — apigw now also supports launchd
	// (macOS) and OpenRC (Alpine).
	kind := svcmgrKind()
	if kind == "systemd" {
		return Result{Level: LevelOK, Name: "service-supervisor", Message: "systemd"}
	}
	if kind == "launchd" {
		return Result{Level: LevelOK, Name: "service-supervisor", Message: "launchd (macOS)"}
	}
	if kind == "openrc" {
		return Result{Level: LevelOK, Name: "service-supervisor", Message: "OpenRC"}
	}
	return Result{
		Level:   LevelFail,
		Name:    "service-supervisor",
		Message: "no supported service supervisor detected",
		Fix:     "apigw targets systemd (Debian 12+/Ubuntu 22.04+), launchd (macOS), or OpenRC (Alpine)",
	}
}

func svcmgrKind() string { return string(svcmgrActive()) }

// startNginxHint returns the platform-appropriate "start nginx" command.
func startNginxHint() string {
	switch svcmgrActive() {
	case svcmgr.Systemd:
		return "sudo systemctl start nginx"
	case svcmgr.Launchd:
		return "sudo brew services start nginx  (or `nginx`)"
	case svcmgr.OpenRC:
		return "sudo rc-service nginx start"
	}
	return "start nginx for your platform"
}

func portCheck(label string, port int) func(context.Context) Result {
	return func(_ context.Context) Result {
		if port == 0 {
			return Result{Level: LevelInfo, Name: "ports-" + label, Message: "no port configured"}
		}
		listening, who := whoListens(port)
		if !listening {
			return Result{
				Level:   LevelWarn,
				Name:    "ports-" + label,
				Message: fmt.Sprintf(":%d not listening", port),
				Fix:     startNginxHint(),
			}
		}
		return Result{
			Level:   LevelOK,
			Name:    "ports-" + label,
			Message: fmt.Sprintf(":%d held by %s", port, who),
		}
	}
}

func portsHTTPSCheck(cfg *config.Config) func(context.Context) Result {
	return func(ctx context.Context) Result {
		if cfg.TLS.Strategy == "" || cfg.TLS.Strategy == "none" {
			return Result{Level: LevelInfo, Name: "ports-https", Message: "TLS disabled — port 443 not checked"}
		}
		port := listenPort(cfg.Listen.HTTPSPort, 443)
		return portCheck("https", port)(ctx)
	}
}

func checkTLSExpiry(_ context.Context) Result {
	certs, err := apitls.ListCerts()
	if err != nil {
		return Result{Level: LevelInfo, Name: "tls-expiry", Message: "no certs found"}
	}
	if len(certs) == 0 {
		return Result{Level: LevelInfo, Name: "tls-expiry", Message: "no certs installed"}
	}
	minDays := 9999
	worstDomain := ""
	for _, c := range certs {
		if c.DaysLeft < minDays {
			minDays = c.DaysLeft
			worstDomain = c.Domain
		}
	}
	switch {
	case minDays < 0:
		return Result{
			Level:   LevelFail,
			Name:    "tls-expiry",
			Message: fmt.Sprintf("%s expired %d days ago", worstDomain, -minDays),
			Fix:     "apigw tls renew --force",
		}
	case minDays < 14:
		return Result{
			Level:   LevelWarn,
			Name:    "tls-expiry",
			Message: fmt.Sprintf("%s expires in %d days", worstDomain, minDays),
			Fix:     "apigw tls renew",
		}
	}
	return Result{
		Level:   LevelOK,
		Name:    "tls-expiry",
		Message: fmt.Sprintf("min %d days (%s)", minDays, worstDomain),
	}
}

func checkConfigPerms(_ context.Context) Result {
	dir := paths.ConfigDir()
	st, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return Result{Level: LevelInfo, Name: "config-perms", Message: dir + " not present yet"}
	}
	if err != nil {
		return Result{Level: LevelWarn, Name: "config-perms", Message: err.Error()}
	}
	mode := st.Mode().Perm()
	// Look for world-write; we don't require strict 0700.
	if mode&0o002 != 0 {
		return Result{
			Level:   LevelFail,
			Name:    "config-perms",
			Message: fmt.Sprintf("%s is world-writable (%04o)", dir, mode),
			Fix:     "sudo chmod 0755 " + dir,
		}
	}
	return Result{Level: LevelOK, Name: "config-perms", Message: fmt.Sprintf("mode %04o", mode)}
}

func webhookSecretsCheck(cfg *config.Config) func(context.Context) Result {
	return func(_ context.Context) Result {
		if !cfg.Webhook.Enabled {
			return Result{Level: LevelInfo, Name: "webhook-secrets", Message: "webhooks disabled"}
		}
		var missing []string
		for _, d := range cfg.Deploys {
			if _, err := webhook.LoadSecret(d.Name); err != nil {
				missing = append(missing, d.Name)
			}
		}
		if len(missing) > 0 {
			return Result{
				Level:   LevelWarn,
				Name:    "webhook-secrets",
				Message: fmt.Sprintf("no secret yet for: %s", strings.Join(missing, ", ")),
				Fix:     "apigw webhook setup " + missing[0],
			}
		}
		return Result{Level: LevelOK, Name: "webhook-secrets", Message: fmt.Sprintf("%d deploy(s)", len(cfg.Deploys))}
	}
}

func sshDeployKeyCheck(cfg *config.Config) func(context.Context) Result {
	return func(_ context.Context) Result {
		needsKey := false
		for _, d := range cfg.Deploys {
			r := d.Repo
			if strings.HasPrefix(r, "git@") || strings.HasPrefix(r, "ssh://") {
				needsKey = true
				break
			}
		}
		if !needsKey {
			return Result{Level: LevelInfo, Name: "ssh-deploy-key", Message: "no SSH-clone deploys"}
		}
		if _, err := os.Stat(apideploy.SSHKeyPath()); err != nil {
			return Result{
				Level:   LevelFail,
				Name:    "ssh-deploy-key",
				Message: "SSH deploy needed but key missing",
				Fix:     "apigw deploy ssh-key",
			}
		}
		return Result{Level: LevelOK, Name: "ssh-deploy-key", Message: apideploy.SSHKeyPath()}
	}
}

func deployUnitsCheck(cfg *config.Config) func(context.Context) Result {
	return func(_ context.Context) Result {
		if len(cfg.Deploys) == 0 {
			return Result{Level: LevelInfo, Name: "deploy-units", Message: "no deployments"}
		}
		inactive := []string{}
		for _, d := range cfg.Deploys {
			if d.Enabled && !apideploy.IsActive(d.Name) {
				inactive = append(inactive, d.Name)
			}
		}
		if len(inactive) > 0 {
			return Result{
				Level:   LevelWarn,
				Name:    "deploy-units",
				Message: fmt.Sprintf("inactive units: %s", strings.Join(inactive, ", ")),
				Fix:     "apigw deploy run " + inactive[0],
			}
		}
		return Result{Level: LevelOK, Name: "deploy-units", Message: fmt.Sprintf("%d active", len(cfg.Deploys))}
	}
}

func queueDepthCheck(_ context.Context) Result {
	// Short timeout: when the dashboard/webhook is running it holds the
	// exclusive bbolt flock; we don't want doctor to stall for 5s on every
	// healthy box that has the dashboard up.
	q, err := webhook.OpenQueueTimeout(300 * time.Millisecond)
	if err != nil {
		if errors.Is(err, webhook.ErrQueueLocked) {
			return Result{Level: LevelOK, Name: "queue-depth", Message: "busy — held by running dashboard"}
		}
		return Result{Level: LevelInfo, Name: "queue-depth", Message: "queue not opened"}
	}
	defer q.Close()
	queued, dead, _ := q.Depth()
	if dead > 0 {
		return Result{
			Level:   LevelWarn,
			Name:    "queue-depth",
			Message: fmt.Sprintf("%d dead-letter job(s)", dead),
			Fix:     "apigw webhook status  # to inspect; manual cleanup via bbolt",
		}
	}
	if queued > 20 {
		return Result{
			Level:   LevelWarn,
			Name:    "queue-depth",
			Message: fmt.Sprintf("%d queued — worker may be stuck", queued),
			Fix:     "systemctl restart apigw-dashboard",
		}
	}
	return Result{Level: LevelOK, Name: "queue-depth", Message: fmt.Sprintf("%d queued, %d dead", queued, dead)}
}

// diskFreeCheck reports free bytes on the filesystem holding /var/lib/apigw.
// Pure stdlib via syscall.Statfs would force a platform import — instead we
// shell to `df -P -k` which works on every Linux + macOS distro we target.
func diskFreeCheck(_ context.Context) Result {
	dir := paths.StateDir()
	if _, err := os.Stat(dir); err != nil {
		dir = filepath.Dir(dir) // parent (e.g. /var/lib)
	}
	cmd := exec.Command("df", "-P", "-k", dir)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return Result{Level: LevelInfo, Name: "disk-free", Message: "df not available"}
	}
	// df -P -k output: Filesystem 1024-blocks Used Available Capacity Mounted
	// (skip header). We want column 4 (1k-blocks free) of row 2.
	lines := strings.Split(out.String(), "\n")
	if len(lines) < 2 {
		return Result{Level: LevelInfo, Name: "disk-free", Message: "no df data"}
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return Result{Level: LevelInfo, Name: "disk-free", Message: "df parse failed"}
	}
	var freeKB int64
	_, _ = fmt.Sscanf(fields[3], "%d", &freeKB)
	freeMiB := freeKB / 1024
	switch {
	case freeMiB < 256:
		return Result{
			Level:   LevelFail,
			Name:    "disk-free",
			Message: fmt.Sprintf("only %d MiB free", freeMiB),
			Fix:     "free up space in /var/lib/apigw or repurpose smaller deploys",
		}
	case freeMiB < 1024:
		return Result{
			Level:   LevelWarn,
			Name:    "disk-free",
			Message: fmt.Sprintf("%d MiB free", freeMiB),
		}
	}
	return Result{
		Level:   LevelOK,
		Name:    "disk-free",
		Message: fmt.Sprintf("%d MiB free", freeMiB),
	}
}

// ---------- helpers ----------

func listenPort(p, fallback int) int {
	if p == 0 {
		return fallback
	}
	return p
}

// whoListens reports whether anything binds to the local port. The
// "who" string is best-effort — we don't shell out to lsof in v1.
func whoListens(port int) (bool, string) {
	d := net.Dialer{Timeout: 250 * time.Millisecond}
	c, err := d.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		_ = c.Close()
		return true, "localhost"
	}
	c, err = d.Dial("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err == nil {
		_ = c.Close()
		return true, "all interfaces"
	}
	return false, ""
}

func firstLine(b []byte) string {
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		return string(b)
	}
	return string(b[:i])
}

// extractHash plucks `hash=<hex16>` out of the managed banner so we can
// re-verify on disk. Returns "" if absent — older banners didn't include
// the hash.
func extractHash(s string) string {
	const key = "hash="
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	rest := s[i+len(key):]
	end := strings.IndexAny(rest, " \t")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func bannerlessHash(b []byte) string {
	// Drop the leading "# MANAGED BY..." line and hash everything else,
	// matching what internal/nginx/generator.go does at write time.
	if i := bytes.IndexByte(b, '\n'); i >= 0 && bytes.HasPrefix(b, []byte("# MANAGED")) {
		b = b[i+1:]
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}
