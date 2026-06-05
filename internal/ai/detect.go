package ai

import (
	"fmt"
	"net"
	"os/exec"
	"time"
)

// DetectionResult is what Detect() returns. Either field can be true
// independently — e.g. vLLM lives as a Python package (Binary=false) but
// can be running on its port (Listening=true).
type DetectionResult struct {
	Provider     Provider
	Port         int
	BinaryFound  bool   // executable resolved on $PATH
	BinaryPath   string // absolute path if found
	Listening    bool   // someone is binding the port
	ServiceFound bool   // systemd unit is loaded (not necessarily active)
	Active       bool   // systemd unit is active
}

// Installed reports whether apigw can use the provider right now — either
// the binary is on PATH or the service responds on its port. False means
// `apigw ai add` should error with the install hint instead of silently
// registering a broken upstream.
func (r DetectionResult) Installed() bool {
	return r.BinaryFound || r.Listening
}

// Detect probes the given provider at `port` (0 = use Catalog default).
//
// Cheap (~50 ms): a $PATH lookup, a 250 ms TCP probe, an optional
// `systemctl is-active` call. Safe to invoke during interactive flows.
func Detect(p Provider, port int) (DetectionResult, error) {
	info, ok := Catalog[p]
	if !ok {
		return DetectionResult{}, fmt.Errorf("unknown provider %q", p)
	}
	if port == 0 {
		port = info.DefaultPort
	}
	res := DetectionResult{Provider: p, Port: port}

	if info.Binary != "" {
		if path, err := exec.LookPath(info.Binary); err == nil {
			res.BinaryFound = true
			res.BinaryPath = path
		}
	}

	if listening, err := probePort(port); err == nil && listening {
		res.Listening = true
	}

	if info.ServiceName != "" {
		// `systemctl status` is heavy; is-active is a 1ms check.
		if err := exec.Command("systemctl", "list-unit-files", "--no-legend", info.ServiceName).Run(); err == nil {
			res.ServiceFound = true
		}
		if err := exec.Command("systemctl", "is-active", "--quiet", info.ServiceName).Run(); err == nil {
			res.Active = true
		}
	}
	return res, nil
}

// DetectAll runs Detect for every provider in Catalog. Used by `apigw ai
// list --all` (Phase 6.1) to show "you have Ollama installed but haven't
// added it yet" hints.
func DetectAll() map[Provider]DetectionResult {
	out := make(map[Provider]DetectionResult, len(Catalog))
	for p := range Catalog {
		if r, err := Detect(p, 0); err == nil {
			out[p] = r
		}
	}
	return out
}

// probePort returns true iff a TCP connect to 127.0.0.1:<port> succeeds
// within 250 ms.
func probePort(port int) (bool, error) {
	d := net.Dialer{Timeout: 250 * time.Millisecond}
	c, err := d.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false, nil
	}
	_ = c.Close()
	return true, nil
}
