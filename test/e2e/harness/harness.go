// Package harness contains the small Docker driver the e2e scenarios use
// to spin a privileged systemd container, copy the host-built apigw binary
// in, and exec commands inside.
//
// We deliberately don't pull in testcontainers-go: it brings ryuk, lifecycle
// reapers, generic API surface we don't need. Shelling out to the docker
// CLI is ~300 LoC and exactly mirrors what an operator would do locally.
package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Distro names the Dockerfile suffix under test/e2e/docker/.
// New distros = add a constant + a Dockerfile; nothing else to touch.
type Distro string

const (
	DistroDebian12 Distro = "debian12"
	DistroUbuntu22 Distro = "ubuntu22"
	DistroUbuntu24 Distro = "ubuntu24"
)

// AllDistros is the canonical matrix the CI workflow iterates.
func AllDistros() []Distro { return []Distro{DistroDebian12, DistroUbuntu22, DistroUbuntu24} }

// BuildImage builds the systemd-in-Docker image for `distro` and returns
// the tag. Cached by `docker build` itself across runs (apt layers don't
// invalidate on apigw rebuilds).
//
// Returns the tag the test should run. Errors via t.Fatal — callers don't
// need to defer cleanup; built images are durable.
func BuildImage(t *testing.T, distro Distro) string {
	t.Helper()
	tag := fmt.Sprintf("apigw-e2e:%s", distro)
	dockerfile := filepath.Join(dockerDir(t), fmt.Sprintf("Dockerfile.%s", distro))
	if _, err := os.Stat(dockerfile); err != nil {
		t.Fatalf("missing %s: %v", dockerfile, err)
	}
	cmd := exec.Command("docker", "build",
		"-t", tag,
		"-f", dockerfile,
		dockerDir(t),
	)
	out, err := combinedRun(cmd, 5*time.Minute)
	if err != nil {
		t.Fatalf("docker build %s: %v\n%s", distro, err, out)
	}
	return tag
}

// Container is one running e2e container. Always created via Launch().
type Container struct {
	t   *testing.T
	ID  string
	Tag string
}

// Launch starts a privileged systemd container from `image` with the host
// binary at hostBinary mounted as /usr/local/bin/apigw. Stops + removes on
// t.Cleanup.
//
// The container's stdout/stderr stream to t.Log so test output keeps the
// systemd boot sequence inline — invaluable when something fails on
// distro N but passes on N-1.
func Launch(t *testing.T, image, hostBinary string) *Container {
	t.Helper()
	if err := mustHaveDocker(); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	abs, err := filepath.Abs(hostBinary)
	if err != nil {
		t.Fatalf("abs %s: %v", hostBinary, err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("host binary %s: %v", abs, err)
	}
	name := "apigw-e2e-" + randHex(6)
	args := []string{
		"run", "-d",
		"--name", name,
		"--privileged",
		"--cgroupns=host",
		"--tmpfs", "/run",
		"--tmpfs", "/run/lock",
		"-v", "/sys/fs/cgroup:/sys/fs/cgroup:rw",
		"-v", fmt.Sprintf("%s:/usr/local/bin/apigw:ro", abs),
		"--stop-signal", "SIGRTMIN+3",
		"-p", "0:80", // host-random
		"-p", "0:9080",
		image,
	}
	cmd := exec.Command("docker", args...)
	out, err := combinedRun(cmd, 30*time.Second)
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	id := strings.TrimSpace(out)
	c := &Container{t: t, ID: id, Tag: image}
	t.Cleanup(func() { c.Stop() })

	// Block until systemd has actually booted — `systemctl is-system-running`
	// returns "running" or "degraded" once units have stabilised. We accept
	// either; "starting" is the only state that means "not ready".
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		out, err := c.exec(5*time.Second, "systemctl", "is-system-running", "--wait")
		state := strings.TrimSpace(out)
		if err == nil || state == "running" || state == "degraded" {
			return c
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("systemd did not reach a ready state within deadline")
	return nil
}

// Stop kills + removes the container. Safe to call repeatedly.
func (c *Container) Stop() {
	if c == nil || c.ID == "" {
		return
	}
	_ = exec.Command("docker", "rm", "-f", c.ID).Run()
	c.ID = ""
}

// Exec runs cmd... inside the container. Returns combined stdout+stderr
// and an error iff the exit code was non-zero. Test failures use t.Fatalf
// with the captured output — much easier to triage than abstract errors.
func (c *Container) Exec(timeout time.Duration, cmd ...string) (string, error) {
	c.t.Helper()
	out, err := c.exec(timeout, cmd...)
	return out, err
}

// MustExec is Exec + t.Fatalf on error. Use when the command MUST pass.
func (c *Container) MustExec(timeout time.Duration, cmd ...string) string {
	c.t.Helper()
	out, err := c.exec(timeout, cmd...)
	if err != nil {
		c.t.Fatalf("docker exec %v: %v\n%s", cmd, err, out)
	}
	return out
}

func (c *Container) exec(timeout time.Duration, args ...string) (string, error) {
	full := append([]string{"exec", c.ID}, args...)
	cmd := exec.Command("docker", full...)
	return combinedRun(cmd, timeout)
}

// HTTPGet curls inside the container — avoids racing with host port-mapping
// resolution that varies by Docker version on macOS vs Linux.
//
// Returns body + status code. We use --http1.1 because some Debian curl
// builds disable h2 by default.
func (c *Container) HTTPGet(t *testing.T, path string) (string, int) {
	t.Helper()
	out := c.MustExec(15*time.Second,
		"curl", "-sS", "-o", "/tmp/curl-body", "-w", "%{http_code}",
		"--http1.1",
		"http://127.0.0.1"+path,
	)
	code := atoi(strings.TrimSpace(out))
	body := c.MustExec(5*time.Second, "cat", "/tmp/curl-body")
	return body, code
}

// CopyIn drops `src` from the host at `dst` inside the container. Used to
// seed install.yml + migrate fixtures.
func (c *Container) CopyIn(t *testing.T, src, dst string) {
	t.Helper()
	cmd := exec.Command("docker", "cp", src, c.ID+":"+dst)
	out, err := combinedRun(cmd, 30*time.Second)
	if err != nil {
		t.Fatalf("docker cp: %v\n%s", err, out)
	}
}

// ---------- helpers ----------

func dockerDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not resolve caller path")
	}
	// test/e2e/harness/harness.go → test/e2e/docker
	return filepath.Join(filepath.Dir(filepath.Dir(file)), "docker")
}

func mustHaveDocker() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("docker not on PATH")
	}
	if err := exec.Command("docker", "version").Run(); err != nil {
		return fmt.Errorf("docker daemon not reachable: %w", err)
	}
	return nil
}

func combinedRun(cmd *exec.Cmd, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
