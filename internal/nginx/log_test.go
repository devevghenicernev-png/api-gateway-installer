package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

func logAPI(format, file string) config.API {
	return config.API{
		Name:          "billing",
		Path:          "/billing",
		Enabled:       true,
		Port:          3000,
		AccessLog:     format,
		AccessLogFile: file,
	}
}

func TestRender_Log_JSONDefaultPath(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{logAPI("json", "")}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(serverBody), "access_log /var/log/nginx/access.log apigw_json;") {
		t.Errorf("missing json log line:\n%s", serverBody)
	}
}

func TestRender_Log_JSONCustomFile(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{logAPI("json", "/var/log/apigw/billing.log")}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(serverBody), "access_log /var/log/apigw/billing.log apigw_json;") {
		t.Errorf("custom file with json:\n%s", serverBody)
	}
}

func TestRender_Log_CombinedFormat(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{logAPI("combined", "")}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(serverBody), "access_log /var/log/nginx/access.log combined;") {
		t.Errorf("combined format:\n%s", serverBody)
	}
}

func TestRender_Log_OffMode(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{logAPI("off", "/this/is/ignored")}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(serverBody), "access_log off;") {
		t.Errorf("off mode:\n%s", serverBody)
	}
}

func TestRender_Log_FileOnlyKeepsDefaultFormat(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{logAPI("", "/var/log/x.log")}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(serverBody), "access_log /var/log/x.log;") {
		t.Errorf("file-only override:\n%s", serverBody)
	}
}
