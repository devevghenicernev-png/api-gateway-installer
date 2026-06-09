// Server-side publish helper for the SSE-driven dashboard cache
// invalidation flow introduced in v0.5.4.
//
// Every admin handler that mutates config.yaml emits a `cfg.change`
// event tagged with the section that just changed. The React dashboard
// subscribes to this topic and invalidates the matching React Query
// cache slot, so panels update in near-real-time (~10ms) instead of
// waiting for the previous 10s poll. List endpoints can then drop
// their refetchInterval to a 60s SSE-disconnected fallback, slashing
// idle traffic by ~6x.
//
// We deliberately publish AFTER the nginx reload succeeds so SSE
// consumers don't see "apis changed" until the change is actually
// live behind nginx. A failed reload (rare; nginx -t catches most)
// keeps the previous state advertised, which matches operator
// expectations.

package dashboard

import (
	"encoding/json"
)

// publishCfgChange tells SSE subscribers that the named config
// section just mutated. `section` is one of:
//
//	apis | deploys | tls | streams | consumers | tokens |
//	sso  | tuning  | approvals | webhook | any
//
// Use "any" for handlers that touch state outside the section list
// (e.g. config import / rollback). The React side invalidates every
// admin/* React Query key on "any".
//
// No-op when Hub is nil (test fixtures that build a bare Server
// without an events.Hub still work; only the SSE optimisation is
// missing).
func (s *Server) publishCfgChange(section string) {
	if s == nil || s.Hub == nil {
		return
	}
	payload, _ := json.Marshal(map[string]string{"section": section})
	s.Hub.Publish("cfg.change", "cfg.change", payload)
}
