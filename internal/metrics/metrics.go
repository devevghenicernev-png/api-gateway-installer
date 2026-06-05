// Package metrics owns apigw's Prometheus surface. Lives in its own
// package so producers (webhook.Server, events.Hub, deploy.Apply,
// nginx.Manager) can import it without dragging in the whole dashboard
// stack — and so we can keep one canonical metric-name set.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the apigw Prometheus surface. Producers receive a *Metrics
// (or nil to skip). Every counter/gauge has labels documented at the call
// site — see internal/webhook/server.go and internal/events/hub.go for
// the canonical instrumentation patterns.
//
// We deliberately do NOT emit per-request HTTP latency histograms (nginx's
// stub_status doesn't expose them and we don't want to fork the request
// path). For per-route latency, install nginx-vts-exporter or similar.
type Metrics struct {
	WebhooksAccepted    *prometheus.CounterVec // labels: deploy
	WebhooksReplayed    prometheus.Counter
	WebhookHMACFails    prometheus.Counter
	WebhookUnsigned     prometheus.Counter
	EventsPublished     *prometheus.CounterVec   // labels: topic
	DeployApplyTotal    *prometheus.CounterVec   // labels: deploy, status
	DeployApplyDuration *prometheus.HistogramVec // labels: deploy
	SSEClients          prometheus.Gauge
	NginxReloadTotal    *prometheus.CounterVec // labels: result
	registry            *prometheus.Registry
}

// New constructs the metrics collector and registers it with a fresh
// Prometheus registry. Caller mounts /metrics via Handler().
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
		WebhooksAccepted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "apigw_webhooks_accepted_total",
			Help: "Webhook deliveries accepted (HMAC verified + enqueued).",
		}, []string{"deploy"}),
		WebhooksReplayed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "apigw_webhooks_replayed_total",
			Help: "Webhook deliveries dropped as in-window replays.",
		}),
		WebhookHMACFails: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "apigw_webhook_hmac_fails_total",
			Help: "Webhook deliveries rejected with 401 (signature mismatch).",
		}),
		WebhookUnsigned: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "apigw_webhook_unsigned_total",
			Help: "Webhook deliveries rejected with 401 (X-Hub-Signature-256 missing).",
		}),
		EventsPublished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "apigw_events_published_total",
			Help: "Events fanned out by the in-process hub.",
		}, []string{"topic"}),
		DeployApplyTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "apigw_deploy_apply_total",
			Help: "Deploy Apply() calls grouped by deploy and result.",
		}, []string{"deploy", "status"}),
		DeployApplyDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "apigw_deploy_apply_duration_seconds",
			Help:    "Deploy Apply() wall-clock duration per deploy.",
			Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800},
		}, []string{"deploy"}),
		SSEClients: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "apigw_sse_clients",
			Help: "Currently-attached SSE clients on the dashboard.",
		}),
		NginxReloadTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "apigw_nginx_reload_total",
			Help: "nginx config writes grouped by result (ok|rollback|validate_fail).",
		}, []string{"result"}),
	}
	reg.MustRegister(
		m.WebhooksAccepted, m.WebhooksReplayed, m.WebhookHMACFails, m.WebhookUnsigned,
		m.EventsPublished, m.DeployApplyTotal, m.DeployApplyDuration,
		m.SSEClients, m.NginxReloadTotal,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler returns an http.Handler exposing the current values in Prometheus
// text format. Mount at /metrics on the dashboard server.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		Registry:      m.registry,
	})
}

// Helpers below let producers skip a nil-check on every increment. Calling
// any helper on a nil *Metrics is a no-op.

// IncWebhooksAccepted increments by deploy name.
func (m *Metrics) IncWebhooksAccepted(deploy string) {
	if m == nil {
		return
	}
	m.WebhooksAccepted.WithLabelValues(deploy).Inc()
}
func (m *Metrics) IncWebhooksReplayed() {
	if m == nil {
		return
	}
	m.WebhooksReplayed.Inc()
}
func (m *Metrics) IncWebhookHMACFails() {
	if m == nil {
		return
	}
	m.WebhookHMACFails.Inc()
}
func (m *Metrics) IncWebhookUnsigned() {
	if m == nil {
		return
	}
	m.WebhookUnsigned.Inc()
}
func (m *Metrics) IncEventsPublished(topic string) {
	if m == nil {
		return
	}
	m.EventsPublished.WithLabelValues(topic).Inc()
}
func (m *Metrics) IncDeployApply(deploy, status string) {
	if m == nil {
		return
	}
	m.DeployApplyTotal.WithLabelValues(deploy, status).Inc()
}
func (m *Metrics) ObserveDeployApplyDuration(deploy string, seconds float64) {
	if m == nil {
		return
	}
	m.DeployApplyDuration.WithLabelValues(deploy).Observe(seconds)
}
func (m *Metrics) SetSSEClients(n int) {
	if m == nil {
		return
	}
	m.SSEClients.Set(float64(n))
}
func (m *Metrics) IncNginxReload(result string) {
	if m == nil {
		return
	}
	m.NginxReloadTotal.WithLabelValues(result).Inc()
}
