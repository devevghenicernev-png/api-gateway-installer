package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

func bufAPI(b *config.Buffering) config.API {
	return config.API{
		Name:      "billing",
		Path:      "/billing",
		Enabled:   true,
		Port:      3000,
		Buffering: b,
	}
}

func boolPtr(b bool) *bool { return &b }

// TestRender_Buffering_DefaultsWhenAbsent — no overrides → only the
// legacy `proxy_buffering off;` line, no other buffering directives.
func TestRender_Buffering_DefaultsWhenAbsent(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{bufAPI(nil)}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	if !strings.Contains(s, "proxy_buffering                    off;") {
		t.Errorf("missing default proxy_buffering off\n%s", s)
	}
	for _, leak := range []string{
		"proxy_request_buffering",
		"client_body_buffer_size",
		"proxy_buffer_size",
		"proxy_buffers",
	} {
		if strings.Contains(s, leak) {
			t.Errorf("%q leaked without overrides:\n%s", leak, s)
		}
	}
}

// TestRender_Buffering_ResponseOn — operator explicitly requests
// response buffering on (overriding apigw's off-by-default).
func TestRender_Buffering_ResponseOn(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{bufAPI(&config.Buffering{Response: boolPtr(true)})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(serverBody), "proxy_buffering                    on;") {
		t.Errorf("expected proxy_buffering on:\n%s", serverBody)
	}
}

// TestRender_Buffering_RequestOff — streaming uploads: request body
// passed through as it arrives.
func TestRender_Buffering_RequestOff(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{bufAPI(&config.Buffering{Request: boolPtr(false)})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(serverBody), "proxy_request_buffering            off;") {
		t.Errorf("missing proxy_request_buffering off:\n%s", serverBody)
	}
}

// TestRender_Buffering_AllTuning — every size knob set.
func TestRender_Buffering_AllTuning(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{bufAPI(&config.Buffering{
		ClientBodyBufferSize: "16k",
		ProxyBufferSize:      "64k",
		ProxyBuffers:         "8 16k",
	})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	for _, want := range []string{
		"client_body_buffer_size            16k;",
		"proxy_buffer_size                  64k;",
		"proxy_buffers                      8 16k;",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
}

// TestRender_Buffering_FullCombo — streaming uploads + bumped buffers.
func TestRender_Buffering_FullCombo(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{bufAPI(&config.Buffering{
		Request:              boolPtr(false),
		Response:             boolPtr(false),
		ClientBodyBufferSize: "1m",
		ProxyBufferSize:      "128k",
		ProxyBuffers:         "16 128k",
	})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	for _, want := range []string{
		"proxy_buffering                    off;",
		"proxy_request_buffering            off;",
		"client_body_buffer_size            1m;",
		"proxy_buffer_size                  128k;",
		"proxy_buffers                      16 128k;",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
}
