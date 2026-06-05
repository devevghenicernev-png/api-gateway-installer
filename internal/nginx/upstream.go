package nginx

import (
	"fmt"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// buildUpstream converts the user-facing config (legacy Port OR new
// Upstreams pool with LB strategy) into a single upstreamEntry the
// _http.tmpl renders.
//
// Rules:
//   - cfg.Upstreams takes priority over Port (multi-server mode).
//   - Empty Port AND empty Upstreams → no upstream block (caller skips).
//   - LoadBalance "round_robin" or "" → no directive (nginx default).
//   - LoadBalance "least_conn" / "ip_hash" / "random" → matching directive.
//
// Each server picks MaxFails/FailTimeout from (in priority order): its own
// per-server fields, then the API-level HealthCheck, then sane defaults
// (3 / "10s").
func buildUpstream(name string, legacyPort int, pool []config.Upstream, lb string, hc *config.HealthCheck) (upstreamEntry, bool) {
	defMaxFails, defFailTimeout := 3, "10s"
	if hc != nil {
		if hc.MaxFails > 0 {
			defMaxFails = hc.MaxFails
		}
		if hc.FailTimeout != "" {
			defFailTimeout = hc.FailTimeout
		}
	}

	servers := []upstreamServer{}
	if len(pool) > 0 {
		for _, u := range pool {
			addr := strings.TrimSpace(u.Address)
			if addr == "" {
				continue
			}
			s := upstreamServer{
				Address:     addr,
				Weight:      u.Weight,
				Backup:      u.Backup,
				Down:        u.Down,
				MaxFails:    defMaxFails,
				FailTimeout: defFailTimeout,
			}
			if u.MaxFails > 0 {
				s.MaxFails = u.MaxFails
			}
			if u.FailTimeout != "" {
				s.FailTimeout = u.FailTimeout
			}
			servers = append(servers, s)
		}
	} else if legacyPort > 0 {
		servers = append(servers, upstreamServer{
			Address:     fmt.Sprintf("127.0.0.1:%d", legacyPort),
			MaxFails:    defMaxFails,
			FailTimeout: defFailTimeout,
		})
	}
	if len(servers) == 0 {
		return upstreamEntry{}, false
	}

	lbDirective := ""
	switch strings.ToLower(lb) {
	case "", "round_robin", "round-robin":
		// nginx default — no directive emitted
	case "least_conn":
		lbDirective = "least_conn"
	case "ip_hash":
		lbDirective = "ip_hash"
	case "random":
		lbDirective = "random"
	default:
		// Unknown strategies fall back to round-robin — the generator
		// already validates at apply time via sanitize step (TODO).
	}

	return upstreamEntry{
		Name:        name,
		LoadBalance: lbDirective,
		Servers:     servers,
		Keepalive:   16,
	}, true
}
