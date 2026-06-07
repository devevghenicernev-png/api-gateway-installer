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

// buildVariantSplit builds the upstreams + split_clients entry for an
// API with Variants. Returns nil split when no variant has a positive
// weight (no traffic to split) — caller treats as "no-op, fall back to
// primary upstream".
//
// Variants are emitted in declaration order. The LAST one with a
// non-zero weight becomes the wildcard (`*`) so rounding errors and
// drift don't drop requests. Variants with zero weight are skipped
// entirely.
func buildVariantSplit(a config.API) (*splitClientsEntry, []upstreamEntry) {
	type kept struct {
		name   string
		weight int
		ups    []config.Upstream
	}
	var keep []kept
	for _, v := range a.Variants {
		if v.Name == "" || v.Weight <= 0 || len(v.Upstreams) == 0 {
			continue
		}
		keep = append(keep, kept{name: v.Name, weight: v.Weight, ups: v.Upstreams})
	}
	if len(keep) < 2 {
		// Need at least two variants to mean anything as a split.
		return nil, nil
	}

	var ups []upstreamEntry
	splits := make([]variantSplit, 0, len(keep))
	for i, k := range keep {
		upName := "apigw_" + a.Name + "_" + k.name
		if up, ok := buildUpstream(a.Name+"_"+k.name, 0, k.ups, a.LoadBalance, a.HealthCheck); ok {
			ups = append(ups, up)
		}
		// Last variant gets the wildcard so rounding leftovers don't
		// vanish into nginx's default-empty bucket.
		isLast := i == len(keep)-1
		splits = append(splits, variantSplit{
			Pct:          k.weight,
			UpstreamName: upName,
			Wildcard:     isLast,
		})
	}

	splitName := "apigw_" + a.Name + "_variant"
	return &splitClientsEntry{
		Name:       splitName,
		HashSource: "$request_id",
		TargetName: splitName,
		Variants:   splits,
	}, ups
}

// variantsActive reports whether the API will be served via a Variants
// split — i.e. at least two variants have positive weight + non-empty
// upstream pool. Mirrors the guard in buildVariantSplit. The apis loop
// uses it to skip the primary upstream block (dead config when variants
// drive routing).
func variantsActive(a config.API) bool {
	if len(a.Variants) < 2 {
		return false
	}
	live := 0
	for _, v := range a.Variants {
		if v.Name == "" || v.Weight <= 0 || len(v.Upstreams) == 0 {
			continue
		}
		live++
	}
	return live >= 2
}
