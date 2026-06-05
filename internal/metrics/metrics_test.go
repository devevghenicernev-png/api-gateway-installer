package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMetrics_HandlerExposesExpectedMetricNames pins the public Prometheus
// surface so a refactor renaming a counter trips CI rather than silently
// breaking a user's scrape config.
func TestMetrics_HandlerExposesExpectedMetricNames(t *testing.T) {
	m := New()
	m.IncWebhooksAccepted("hello")
	m.IncWebhooksReplayed()
	m.IncWebhookHMACFails()
	m.IncWebhookUnsigned()
	m.IncEventsPublished("deploy.hello.stdout")
	m.IncDeployApply("hello", "ok")
	m.ObserveDeployApplyDuration("hello", 42.0)
	m.SetSSEClients(3)
	m.IncNginxReload("ok")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, name := range []string{
		"apigw_webhooks_accepted_total",
		"apigw_webhooks_replayed_total",
		"apigw_webhook_hmac_fails_total",
		"apigw_webhook_unsigned_total",
		"apigw_events_published_total",
		"apigw_deploy_apply_total",
		"apigw_deploy_apply_duration_seconds",
		"apigw_sse_clients",
		"apigw_nginx_reload_total",
		"go_goroutines",
		"process_resident_memory_bytes",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("/metrics missing %q", name)
		}
	}
}

func TestNilMetrics_HelpersAreNoOps(t *testing.T) {
	// Producers receive `*Metrics = nil` in tests / metrics-disabled mode;
	// helpers must accept that without panicking.
	var m *Metrics
	m.IncWebhooksAccepted("x")
	m.IncWebhooksReplayed()
	m.IncEventsPublished("topic")
	m.IncDeployApply("d", "ok")
	m.ObserveDeployApplyDuration("d", 1.0)
	m.SetSSEClients(0)
	m.IncNginxReload("ok")
}
