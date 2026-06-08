// Package serve implements `apigw dashboard serve` — the consolidated
// daemon. One process, one events.Hub, four producers (webhook server, deploy
// worker, TLS expiry ticker, status ticker), two HTTP listeners (dashboard +
// webhook). What the architecture brief calls "one in-process event hub".
package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/dashboard"
	"github.com/devevghenicernev-png/apigw/internal/events"
	"github.com/devevghenicernev-png/apigw/internal/metrics"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/system"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
)

type opts struct {
	addr        string
	webhookAddr string
	skipWebhook bool
}

func NewCmdServe(f *cmdutil.Factory) *cobra.Command {
	o := &opts{}
	cmd := &cobra.Command{
		Use:    "serve",
		Short:  "Run the live dashboard + webhook + worker (used by systemd; not for humans)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return run(c.Context(), f, o)
		},
	}
	cmd.Flags().StringVar(&o.addr, "addr", ":9080", "dashboard listen address")
	cmd.Flags().StringVar(&o.webhookAddr, "webhook-addr", ":9000", "webhook listen address")
	cmd.Flags().BoolVar(&o.skipWebhook, "no-webhook", false, "don't run the embedded webhook server (use external)")
	return cmd
}

func run(ctx context.Context, f *cmdutil.Factory, o *opts) error {
	logger := f.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Central Prometheus collector. Threaded into every producer so the
	// /metrics endpoint reflects the whole process — webhook server,
	// deploy queue, events hub, nginx manager.
	prom := metrics.New()

	hub := events.New()
	hub.Metrics = prom
	publish := func(topic, evType string, data []byte) {
		hub.Publish(topic, evType, data)
	}

	// Dashboard HTTP server (owns the hub, serves UI + SSE + JSON API).
	dash := dashboard.New(o.addr, hub, reloadConfig, logger, prom)

	// Wire the integrated security layer (RBAC + audit + policy + approvals
	// + tenants + alerts). Done lazily so legacy installs without a
	// `security:` block in config.yaml still boot in unauthenticated-legacy
	// mode (no admin API gate, no audit log).
	if cfg, err := reloadConfig(); err == nil {
		if sec, err := dashboard.NewSecurity(cfg.Security, cfg.Tenants, cfg.Alerts, logger); err == nil {
			dash.Sec = sec
			defer sec.Close()
		} else {
			logger.Warn("security init failed; running in legacy mode", "err", err)
		}
	}

	// Always open the queue + worker — the dashboard's manual-redeploy
	// endpoint enqueues into the same queue webhooks use. --no-webhook
	// only skips the HTTP receiver (no public webhook endpoint).
	var (
		whSrv  *webhook.Server
		worker *webhook.Worker
		q      *webhook.Queue
	)
	{
		var err error
		q, err = webhook.OpenQueue()
		if err != nil {
			return fmt.Errorf("open queue: %w", err)
		}
		defer q.Close()
		dash.Queue = q

		worker = &webhook.Worker{
			Queue:        q,
			Logger:       logger,
			ConfigFn:     reloadConfig,
			Publish:      publish,
			ApplyMetrics: prom,
		}
		if dash.Sec != nil {
			worker.AlertHook = dash.Sec.FireAlert
			if dash.Sec.Approvals != nil {
				worker.PruneApprovals = func() (int, error) {
					return dash.Sec.Approvals.PruneExpired(30 * 24 * time.Hour)
				}
			}
		}
	}
	if !o.skipWebhook {
		webhookAddr, err := webhook.ResolveListenAddr(o.webhookAddr)
		if err != nil {
			return err
		}
		whSrv = webhook.New(webhookAddr, makeWebhookHandler(q, logger), logger)
		whSrv.Publish = publish
		whSrv.Metrics = prom
	}

	logger.Info("apigw dashboard starting",
		slog.String("dashboard", o.addr),
		slog.String("webhook", o.webhookAddr),
		slog.Bool("webhook_skipped", o.skipWebhook))

	// Notify systemd we're ready and start watchdog heartbeats. No-ops
	// when launched outside Type=notify (manual `apigw dashboard serve`).
	_ = system.SdNotifyReady()
	_ = system.WatchdogTick(ctx)
	defer func() { _ = system.SdNotifyStopping() }()

	errCh := make(chan error, 4)
	go func() { errCh <- dash.ListenAndServe(ctx) }()
	// Worker always runs — it drains both webhook deliveries and the
	// dashboard's manual-redeploy enqueues.
	go func() { errCh <- worker.Run(ctx) }()
	if whSrv != nil {
		go func() { errCh <- whSrv.ListenAndServe(ctx) }()
	}
	go runTLSTicker(ctx, publish, logger, dash.Sec)
	go runStatusTicker(ctx, publish, logger)
	go runSecurityReloader(ctx, dash.Sec, logger)
	if cfg, err := reloadConfig(); err == nil {
		go metrics.RunExporter(ctx, prom, metrics.ExporterConfig{
			DogStatsDAddr: cfg.Metrics.DogStatsDAddr,
			StatsDAddr:    cfg.Metrics.StatsDAddr,
			GraphiteAddr:  cfg.Metrics.GraphiteAddr,
			Prefix:        cfg.Metrics.Prefix,
			FlushSeconds:  cfg.Metrics.FlushSeconds,
		}, logger)
	}
	if dash.Sec != nil {
		go runGitOpsReconciler(ctx, logger, dash.Sec)
	}
	// Optional Raft cluster — empty NodeID = single-host mode.
	if cfg, err := reloadConfig(); err == nil && cfg.Cluster.NodeID != "" {
		node, cerr := runClusterNode(ctx, cfg.Cluster, logger)
		if cerr != nil {
			logger.Warn("cluster init failed; standalone mode", "err", cerr)
		} else if node != nil {
			defer func() { _ = node.Shutdown() }()
		}
	}
	// nginx access log → events.Hub topic "nginx.access". No-op if the
	// file doesn't exist (host without nginx installed yet).
	if err := nginx.TailAccessLog(ctx, publish, logger); err != nil {
		logger.Warn("start nginx access tail", slog.String("err", err.Error()))
	}
	// Live aggregate ticker — 1s rps/p50/p95/p99/err_rate on topic
	// "metrics.tick", consumed by the dashboard UI and `apigw events --topic
	// metrics.tick` from CLI. Empty when no access log activity flows.
	go metrics.RunLiveTicker(ctx, hub, logger)

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

// runTLSTicker publishes cert expiry once an hour. Topic "tls.expiry".
//
// Hourly is plenty — certs change on day boundaries. Publishing on tick
// (rather than only on rotation) means subscribers attaching mid-day see
// current state via the ring buffer.
//
// When Security/Alerts is configured, certs within `tls_expiry_warn_days`
// (default 30) fire a `cert.expiring` alert; certs past NotAfter fire
// `cert.expired` (critical). Each cert is alerted at most once per
// 24-hour window via the firedRecently map to avoid notification storms.
func runTLSTicker(ctx context.Context, publish func(topic, evType string, data []byte), logger *slog.Logger, sec *dashboard.Security) {
	tick := time.NewTicker(1 * time.Hour)
	defer tick.Stop()
	firedRecently := map[string]time.Time{}
	warnDays := 30
	emit := func() {
		certs, err := apitls.ListCerts()
		if err != nil {
			return
		}
		// Pull current warn-days from config in case operator changed it.
		if cfg, err := reloadConfig(); err == nil && cfg.Alerts.TLSExpiryWarnDays > 0 {
			warnDays = cfg.Alerts.TLSExpiryWarnDays
		}
		now := time.Now()
		for _, c := range certs {
			body, _ := json.Marshal(c)
			publish("tls.expiry", "tls", body)

			if sec == nil {
				continue
			}
			key := c.Domain
			last := firedRecently[key]
			if now.Sub(last) < 24*time.Hour {
				continue
			}
			daysLeft := int(time.Until(c.NotAfter).Hours() / 24)
			switch {
			case daysLeft < 0:
				sec.FireAlert("cert.expired", "critical",
					fmt.Sprintf("Cert expired: %s", c.Domain),
					fmt.Sprintf("Certificate for %s expired on %s (%d days ago). Renewal failed or never ran.", c.Domain, c.NotAfter.Format("2006-01-02"), -daysLeft),
					"cert/"+c.Domain)
				firedRecently[key] = now
			case daysLeft <= warnDays:
				sec.FireAlert("cert.expiring", "warning",
					fmt.Sprintf("Cert expiring: %s (%d days)", c.Domain, daysLeft),
					fmt.Sprintf("Certificate for %s expires on %s. Auto-renewal should kick in around %d days before.", c.Domain, c.NotAfter.Format("2006-01-02"), warnDays/2),
					"cert/"+c.Domain)
				firedRecently[key] = now
			}
		}
	}
	emit() // immediate on start so first subscriber sees current state
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			emit()
		}
	}
}

// runGitOpsReconciler wires the gitops puller when config.GitOps.RepoURL is
// set. The reconciler clones the repo on Interval and invokes apply() with
// the checkout root. apply() copies <root>/<path>/config.yaml onto the
// configured config path (atomic via cfg.Save()).
func runGitOpsReconciler(ctx context.Context, logger *slog.Logger, sec *dashboard.Security) {
	cfg, err := reloadConfig()
	if err != nil || cfg.GitOps.RepoURL == "" {
		return
	}
	interval := time.Duration(cfg.GitOps.IntervalSec) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	g := gitopsCfg{
		RepoURL:   cfg.GitOps.RepoURL,
		Branch:    cfg.GitOps.Branch,
		Path:      cfg.GitOps.Path,
		Interval:  interval,
		HTTPToken: cfg.GitOps.HTTPToken,
		SSHKey:    cfg.GitOps.SSHKeyFile,
		SSHPass:   cfg.GitOps.SSHKeyPass,
	}
	logger.Info("gitops reconciler starting", "repo", g.RepoURL, "branch", g.Branch, "interval", g.Interval)
	runGitOps(ctx, g, logger, sec)
}

// runStatusTicker republishes deploy systemd state every 5s. Topic
// "status.deploy". Catches external systemctl actions (operator stopping a
// unit manually) that the worker wouldn't otherwise emit.
func runStatusTicker(ctx context.Context, publish func(topic, evType string, data []byte), logger *slog.Logger) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	emit := func() {
		cfg, err := reloadConfig()
		if err != nil {
			return
		}
		for _, d := range cfg.Deploys {
			body, _ := json.Marshal(map[string]any{
				"deploy": d.Name,
				"status": d.LastStatus,
				"sha":    d.LastSHA,
				"ts":     time.Now().Unix(),
			})
			publish("status.deploy", "state", body)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			emit()
		}
	}
}

// makeWebhookHandler is the same closure as `apigw webhook serve` but with
// `q` already in scope — kept here so the dashboard's serve is a single
// self-contained entrypoint.
func makeWebhookHandler(q *webhook.Queue, logger *slog.Logger) webhook.PayloadHandler {
	return func(ctx context.Context, deployName, event string, body []byte) error {
		if webhook.IsPing(event) || event != "push" {
			return nil
		}
		branch, sha, repo, err := webhook.ParsePush(body)
		if err != nil {
			if err == webhook.ErrIgnore {
				return nil
			}
			return err
		}
		cfg, err := reloadConfig()
		if err != nil {
			return err
		}
		d := cfg.FindDeploy(deployName)
		if d == nil {
			return nil
		}
		if d.Branch != "" && d.Branch != branch {
			return nil
		}
		_, err = q.Enqueue(webhook.Job{
			Deploy: deployName,
			Event:  event,
			Repo:   repo,
			Branch: branch,
			SHA:    sha,
		})
		return err
	}
}

// reloadConfig bypasses Factory's memoised cache — long-running server must
// see updates from CLI-side writes.
func reloadConfig() (*config.Config, error) {
	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	cfg, ok := c.(*config.Config)
	if !ok {
		return nil, fmt.Errorf("config: unexpected type %T", c)
	}
	return cfg, nil
}
