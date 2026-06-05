// Package alerts dispatches operational events to external systems —
// PagerDuty, Slack, Microsoft Teams, generic webhooks, email (SMTP).
//
// Event categories (config picks which triggers go where):
//
//	cert.expiring        — TLS cert within --tls-warn-days (default 30)
//	cert.expired         — TLS cert is past NotAfter
//	deploy.failed        — apigw deploy apply returned an error
//	deploy.health_lost   — deploy was running, suddenly 5xx-storm
//	webhook.queue_growing — pending jobs > threshold for > 5min
//	webhook.dead_letter  — job moved to dead-letter queue
//	audit.anomaly        — N denied actions by same user in M minutes
//	cluster.peer_lost    — Raft peer unreachable (E17)
//
// Each notifier is independent. Failure to deliver to PagerDuty doesn't
// stop Slack delivery. All deliveries are audited.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// Event is what triggers fire. Severity drives routing: "warning" fires
// Slack/email; "critical" also wakes PagerDuty.
type Event struct {
	Category string            // cert.expiring / deploy.failed / …
	Severity string            // info | warning | critical
	Title    string            // human summary
	Detail   string            // markdown body (Slack/Teams), plaintext for email
	Resource string            // affected entity
	Metadata map[string]string // extra tags
	Time     time.Time
}

// Notifier sends a single Event. Each provider implements this.
type Notifier interface {
	Name() string
	Send(ctx context.Context, e Event) error
}

// Dispatcher fans out events to every configured Notifier. Failures are
// logged but don't propagate — alert delivery is best-effort.
type Dispatcher struct {
	mu        sync.RWMutex
	notifiers []Notifier
	log       Logger
}

type Logger interface {
	Info(msg string, fields ...any)
	Warn(msg string, fields ...any)
	Error(msg string, fields ...any)
}

func New(logger Logger) *Dispatcher { return &Dispatcher{log: logger} }

func (d *Dispatcher) Register(n Notifier) {
	d.mu.Lock()
	d.notifiers = append(d.notifiers, n)
	d.mu.Unlock()
}

// Fire dispatches in parallel. Returns once every notifier has either
// finished or returned an error (logged). Context cancels the lot.
func (d *Dispatcher) Fire(ctx context.Context, e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	d.mu.RLock()
	ns := append([]Notifier(nil), d.notifiers...)
	d.mu.RUnlock()
	var wg sync.WaitGroup
	for _, n := range ns {
		wg.Add(1)
		go func(n Notifier) {
			defer wg.Done()
			if err := n.Send(ctx, e); err != nil {
				d.log.Warn("alert delivery failed", "notifier", n.Name(), "err", err)
			}
		}(n)
	}
	wg.Wait()
}

// ---------- Slack ----------

type SlackNotifier struct {
	WebhookURL string
	HTTP       *http.Client
}

func NewSlack(url string) *SlackNotifier {
	return &SlackNotifier{WebhookURL: url, HTTP: &http.Client{Timeout: 5 * time.Second}}
}

func (s *SlackNotifier) Name() string { return "slack" }

func (s *SlackNotifier) Send(ctx context.Context, e Event) error {
	body := map[string]any{
		"text": fmt.Sprintf("*[%s] %s*\n%s", strings.ToUpper(e.Severity), e.Title, e.Detail),
	}
	return postJSON(ctx, s.HTTP, s.WebhookURL, body)
}

// ---------- Microsoft Teams ----------

type TeamsNotifier struct {
	WebhookURL string
	HTTP       *http.Client
}

func NewTeams(url string) *TeamsNotifier {
	return &TeamsNotifier{WebhookURL: url, HTTP: &http.Client{Timeout: 5 * time.Second}}
}

func (t *TeamsNotifier) Name() string { return "teams" }

func (t *TeamsNotifier) Send(ctx context.Context, e Event) error {
	body := map[string]any{
		"@type":      "MessageCard",
		"@context":   "https://schema.org/extensions",
		"summary":    e.Title,
		"themeColor": severityToColor(e.Severity),
		"sections": []map[string]any{{
			"activityTitle":    e.Title,
			"activitySubtitle": e.Resource,
			"text":             e.Detail,
		}},
	}
	return postJSON(ctx, t.HTTP, t.WebhookURL, body)
}

// ---------- PagerDuty Events v2 ----------

type PagerDutyNotifier struct {
	IntegrationKey string
	HTTP           *http.Client
}

func NewPagerDuty(key string) *PagerDutyNotifier {
	return &PagerDutyNotifier{IntegrationKey: key, HTTP: &http.Client{Timeout: 5 * time.Second}}
}

func (p *PagerDutyNotifier) Name() string { return "pagerduty" }

func (p *PagerDutyNotifier) Send(ctx context.Context, e Event) error {
	if e.Severity != "critical" && e.Severity != "error" {
		// PD only for critical — warnings go to Slack/email.
		return nil
	}
	body := map[string]any{
		"routing_key":  p.IntegrationKey,
		"event_action": "trigger",
		"payload": map[string]any{
			"summary":   e.Title,
			"source":    "apigw",
			"severity":  e.Severity,
			"component": e.Resource,
			"group":     e.Category,
			"custom_details": map[string]any{
				"detail":   e.Detail,
				"metadata": e.Metadata,
			},
		},
	}
	return postJSON(ctx, p.HTTP, "https://events.pagerduty.com/v2/enqueue", body)
}

// ---------- Generic webhook ----------

type WebhookNotifier struct {
	URL    string
	HTTP   *http.Client
	Header map[string]string
}

func NewWebhook(url string, headers map[string]string) *WebhookNotifier {
	return &WebhookNotifier{URL: url, Header: headers, HTTP: &http.Client{Timeout: 5 * time.Second}}
}

func (w *WebhookNotifier) Name() string { return "webhook" }

func (w *WebhookNotifier) Send(ctx context.Context, e Event) error {
	body, _ := json.Marshal(e)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.Header {
		req.Header.Set(k, v)
	}
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook %s: status %d", w.URL, resp.StatusCode)
	}
	return nil
}

// ---------- Email (SMTP) ----------

type EmailNotifier struct {
	SMTPHost string
	SMTPPort int
	Username string
	Password string
	From     string
	To       []string
}

func (e *EmailNotifier) Name() string { return "email" }

func (n *EmailNotifier) Send(_ context.Context, e Event) error {
	addr := fmt.Sprintf("%s:%d", n.SMTPHost, n.SMTPPort)
	subject := fmt.Sprintf("[apigw][%s] %s", strings.ToUpper(e.Severity), e.Title)
	body := fmt.Sprintf("Subject: %s\r\nFrom: %s\r\nTo: %s\r\n\r\n%s\n\nResource: %s",
		subject, n.From, strings.Join(n.To, ", "), e.Detail, e.Resource)
	var auth smtp.Auth
	if n.Username != "" {
		auth = smtp.PlainAuth("", n.Username, n.Password, n.SMTPHost)
	}
	return smtp.SendMail(addr, auth, n.From, n.To, []byte(body))
}

// ---------- helpers ----------

func postJSON(ctx context.Context, c *http.Client, url string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("alert post %s: status %d", url, resp.StatusCode)
	}
	return nil
}

func severityToColor(sev string) string {
	switch sev {
	case "critical", "error":
		return "FF0000"
	case "warning":
		return "FFA500"
	}
	return "00AA00"
}
