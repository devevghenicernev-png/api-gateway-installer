package lint

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

func TestLint_CleanConfig(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{Name: "billing", Path: "/billing", Port: 3000, Enabled: true},
	}
	got := Lint(&cfg)
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %d: %+v", len(got), got)
	}
}

func TestLint_DuplicateAPIName(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{Name: "billing", Path: "/a", Port: 3000, Enabled: true},
		{Name: "billing", Path: "/b", Port: 3001, Enabled: true},
	}
	got := Lint(&cfg)
	if !findCode(got, "L101") {
		t.Errorf("missing L101 (duplicate name): %+v", got)
	}
}

func TestLint_NoUpstream(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{Name: "billing", Path: "/b", Enabled: true}}
	got := Lint(&cfg)
	if !findCode(got, "L102") {
		t.Errorf("missing L102 (no upstream): %+v", got)
	}
}

func TestLint_PathCollision(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{Name: "billing", Path: "/api/x", Port: 3000, Enabled: true},
		{Name: "audit", Path: "/api/x", Port: 3001, Enabled: true},
	}
	got := Lint(&cfg)
	if !findCode(got, "L103") {
		t.Errorf("missing L103 (path collision): %+v", got)
	}
}

func TestLint_CanaryWeightOutOfRange(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{
		Name: "billing", Path: "/b", Port: 3000, Enabled: true,
		Canary: &config.Canary{
			Weight:    150,
			Upstreams: []config.Upstream{{Address: "c:1"}},
		},
	}}
	got := Lint(&cfg)
	if !findCode(got, "L104") {
		t.Errorf("missing L104 (canary weight): %+v", got)
	}
}

func TestLint_CanaryNoUpstreams(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{
		Name: "billing", Path: "/b", Port: 3000, Enabled: true,
		Canary: &config.Canary{Weight: 10},
	}}
	got := Lint(&cfg)
	if !findCode(got, "L105") {
		t.Errorf("missing L105 (canary no upstreams): %+v", got)
	}
}

func TestLint_BlueGreenInvalidActive(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{
		Name: "billing", Path: "/b", Port: 3000, Enabled: true,
		BlueGreen: &config.BlueGreen{
			Blue:   []config.Upstream{{Address: "x:1"}},
			Green:  []config.Upstream{{Address: "y:1"}},
			Active: "purple",
		},
	}}
	got := Lint(&cfg)
	if !findCode(got, "L106") {
		t.Errorf("missing L106 (blue-green active): %+v", got)
	}
}

func TestLint_LetsEncryptNoEmail(t *testing.T) {
	cfg := config.Defaults()
	cfg.TLS.Strategy = "letsencrypt"
	cfg.TLS.Domains = []string{"api.example.com"}
	got := Lint(&cfg)
	if !findCode(got, "L201") {
		t.Errorf("missing L201 (no email): %+v", got)
	}
}

func TestLint_LetsEncryptOCSPWarning(t *testing.T) {
	cfg := config.Defaults()
	cfg.TLS.Strategy = "letsencrypt"
	cfg.TLS.Domains = []string{"api.example.com"}
	cfg.TLS.Email = "a@b.com"
	cfg.TLS.OCSPStapling = true
	got := Lint(&cfg)
	if !findCode(got, "L203") {
		t.Errorf("missing L203 (LE+OCSP warning): %+v", got)
	}
}

func TestLint_JWTNoSecretOrJWKS(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{
		Name: "billing", Path: "/b", Port: 3000, Enabled: true,
		JWT: &config.JWT{Algorithm: "RS256"},
	}}
	got := Lint(&cfg)
	if !findCode(got, "L401") {
		t.Errorf("missing L401 (jwt incomplete): %+v", got)
	}
}

func TestLint_MTLSNoCAFile(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{
		Name: "billing", Path: "/b", Port: 3000, Enabled: true,
		MTLS: &config.MTLS{},
	}}
	got := Lint(&cfg)
	if !findCode(got, "L402") {
		t.Errorf("missing L402 (mtls no ca): %+v", got)
	}
}

func TestLint_SessionWithoutGlobalConfig(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{
		Name: "billing", Path: "/b", Port: 3000, Enabled: true, Session: true,
	}}
	got := Lint(&cfg)
	if !findCode(got, "L403") {
		t.Errorf("missing L403 (session w/o global): %+v", got)
	}
}

func TestLint_RBACEnforceNoTokens(t *testing.T) {
	cfg := config.Defaults()
	cfg.Security.RBACEnforce = true
	cfg.APIs = []config.API{{Name: "billing", Path: "/b", Port: 3000, Enabled: true}}
	got := Lint(&cfg)
	if !findCode(got, "L501") {
		t.Errorf("missing L501 (rbac no tokens): %+v", got)
	}
}

func TestLint_ConsumerGroupTypo(t *testing.T) {
	cfg := config.Defaults()
	cfg.Security.ConsumerGroups = []config.ConsumerGroup{{Name: "ops"}}
	cfg.Security.Consumers = []config.Consumer{
		{ID: "alice", Groups: []string{"opss"}}, // typo
	}
	cfg.APIs = []config.API{{Name: "billing", Path: "/b", Port: 3000, Enabled: true}}
	got := Lint(&cfg)
	if !findCode(got, "L502") {
		t.Errorf("missing L502 (group typo): %+v", got)
	}
}

func TestLint_HasErrors(t *testing.T) {
	if HasErrors(nil) {
		t.Errorf("nil should not have errors")
	}
	if HasErrors([]Finding{{Severity: SeverityWarning}}) {
		t.Errorf("warnings-only should not count as errors")
	}
	if !HasErrors([]Finding{{Severity: SeverityError}}) {
		t.Errorf("error-severity finding should count")
	}
}

func TestLint_SortOrderErrorsFirst(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{Name: "billing", Path: "/x", Enabled: true}, // no upstream → L102 error
	}
	cfg.Webhook.Enabled = true
	cfg.Webhook.Port = 0 // → L301 warning
	got := Lint(&cfg)
	if len(got) < 2 {
		t.Fatalf("need both findings, got: %+v", got)
	}
	if got[0].Severity != SeverityError {
		t.Errorf("first finding should be error, got %v", got[0])
	}
	// Subsequent must not be Error after a Warning appears.
	sawWarning := false
	for _, f := range got {
		if f.Severity == SeverityWarning {
			sawWarning = true
		} else if sawWarning && f.Severity == SeverityError {
			t.Errorf("errors interleaved with warnings")
		}
	}
}

// ---------- helpers ----------

func findCode(fs []Finding, code string) bool {
	for _, f := range fs {
		if f.Code == code {
			return true
		}
	}
	return false
}

func TestSeverityString(t *testing.T) {
	if SeverityError.String() != "error" || SeverityWarning.String() != "warning" {
		t.Errorf("severity strings drifted")
	}
	if !strings.Contains(SeverityError.String(), "error") {
		t.Errorf("error string sanity")
	}
}
