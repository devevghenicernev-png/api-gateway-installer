package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// TestRender_MultiUpstreamPool covers F8: when API.Upstreams is non-empty
// the generator emits a pool with the requested load-balance strategy +
// per-server weight/backup/down.
func TestRender_MultiUpstreamPool(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{
			Name:        "billing",
			Path:        "/billing",
			Enabled:     true,
			LoadBalance: "least_conn",
			Upstreams: []config.Upstream{
				{Address: "10.0.0.1:3000", Weight: 5},
				{Address: "10.0.0.2:3000", Weight: 1},
				{Address: "10.0.0.3:3000", Backup: true},
			},
		},
	}
	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	for _, want := range []string{
		"upstream apigw_billing {",
		"least_conn;",
		"server 10.0.0.1:3000 weight=5",
		"server 10.0.0.2:3000 weight=1",
		"server 10.0.0.3:3000",
		"backup",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("http config missing %q\n--- emit ---\n%s", want, h)
		}
	}
}

// TestRender_LegacyPortStillWorks verifies a single-port API still
// produces a one-server pool. Backward compat is non-negotiable.
func TestRender_LegacyPortStillWorks(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{Name: "hello", Port: 3000, Path: "/hello", Enabled: true},
	}
	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(httpBody), "server 127.0.0.1:3000") {
		t.Fatalf("legacy Port should produce one server entry; got:\n%s", httpBody)
	}
}

// TestRender_GRPCEmitsGrpcPass covers F9: when API.GRPC is true the
// location block uses grpc_pass + grpc_set_header instead of proxy_pass.
func TestRender_GRPCEmitsGrpcPass(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{Name: "rpc", Port: 50051, Path: "/rpc", Enabled: true, GRPC: true},
	}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	if !strings.Contains(s, "grpc_pass grpc://apigw_rpc;") {
		t.Errorf("grpc_pass missing\n--- emit ---\n%s", s)
	}
	if strings.Contains(s, "proxy_pass http://apigw_rpc;") {
		t.Errorf("proxy_pass should NOT appear for gRPC API\n--- emit ---\n%s", s)
	}
	if !strings.Contains(s, "grpc_set_header Host") {
		t.Errorf("grpc_set_header missing")
	}
}
