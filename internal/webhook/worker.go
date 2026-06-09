package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/deploy"
)

// LeaseDuration is how long a worker holds a claim on a job before another
// (or the same after restart) is allowed to retry it.
//
// 15 min is longer than any reasonable build window for the sub-1 GB Pi
// targets. If a deploy takes longer, the operator is doing something we
// don't optimise for.
const LeaseDuration = 15 * time.Minute

// PollInterval is how often the worker checks for new jobs when idle.
//
// 500 ms is a happy medium: a webhook turns into an Apply call within 1 s
// of validation, and idle CPU is negligible. Workers are bound to a single
// host — no need for distributed cron-style scheduling.
const PollInterval = 500 * time.Millisecond

// DeadLetterRetention is how long a permanently-failed job stays around for
// inspection before the maintenance pass evicts it. 14 days mirrors GitHub's
// own webhook delivery log retention.
const DeadLetterRetention = 14 * 24 * time.Hour

// MaintenanceInterval is how often the worker runs PruneDeadLetters +
// Compact. Daily is plenty — these operations rewrite the file, so doing
// them per-poll would thrash disk.
const MaintenanceInterval = 24 * time.Hour

// Worker drains the queue and runs deploys. One per process.
//
// We DON'T run multiple workers in parallel — deploys serialise per deploy
// via deploy.JobQueue anyway, and the global rate is gated by `npm install`
// throughput. Adding workers wouldn't go faster; it would just race.
type Worker struct {
	Queue    *Queue
	Logger   *slog.Logger
	ConfigFn func() (*config.Config, error) // re-read config each pass for fresh deploy fields

	// Publish, when non-nil, broadcasts state transitions + build stdout to
	// the events.Hub. The dashboard uses these to drive its live badges and
	// log viewer. Optional — webhook-only mode skips it.
	Publish func(topic, evType string, data []byte)

	// ApplyMetrics, when non-nil, is threaded into every ApplyRequest the
	// worker submits — the central Prometheus collector records counts +
	// durations there.
	ApplyMetrics deploy.ApplyMetrics

	// AlertHook, when non-nil, is invoked on terminal deploy failures. The
	// dashboard wires this to its alerts dispatcher (PagerDuty, Slack, …)
	// so on-call sees a failed prod deploy without watching journald.
	AlertHook func(category, severity, title, detail, resource string)

	// PruneApprovals, when non-nil, runs daily as part of the
	// maintenance ticker. Wired by the dashboard to drop expired
	// pending change requests and old applied/rejected records so
	// approvals.db doesn't grow without bound. Returns the count of
	// rows removed.
	PruneApprovals func() (int, error)

	// inProcessJobs serialises Apply() calls per deploy name. Created ONCE
	// (lazily on first job) so the single-flight guarantee holds across
	// every job the worker pulls. Previously a fresh JobQueue per
	// processOne broke serialisation — two near-simultaneous jobs for the
	// same deploy could both Submit concurrently.
	inProcessJobs *deploy.JobQueue
}

// Run blocks until ctx is cancelled. Polls Queue every PollInterval; for
// every claimed job, runs deploy.Apply and Ack/Fail accordingly.
//
// Crash semantics: a power loss between Claim and Ack leaves the lease in
// place. After LeaseDuration the next Peek picks the job up again — that's
// the "at-least-once" guarantee. The deploy.Apply call is idempotent
// (Clone short-circuits on SHA match), so re-running is safe.
func (w *Worker) Run(ctx context.Context) error {
	if w.Queue == nil || w.ConfigFn == nil {
		return errors.New("worker: Queue and ConfigFn are required")
	}
	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}
	tick := time.NewTicker(PollInterval)
	defer tick.Stop()
	sweep := time.NewTicker(2 * time.Minute)
	defer sweep.Stop()
	maint := time.NewTicker(MaintenanceInterval)
	defer maint.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sweep.C:
			if err := w.Queue.SweepStaleLeases(); err != nil {
				logger.Warn("sweep leases", slog.String("err", err.Error()))
			}
		case <-maint.C:
			// Prune dead-letters > 14d, then compact the bbolt file so disk
			// usage tracks live data, not historical churn.
			pruned, perr := w.Queue.PruneDeadLetters(DeadLetterRetention)
			if perr != nil {
				logger.Warn("prune dead-letters", slog.String("err", perr.Error()))
			}
			saved, cerr := w.Queue.Compact()
			if cerr != nil {
				logger.Warn("compact queue", slog.String("err", cerr.Error()))
			}
			if pruned > 0 || saved > 0 {
				logger.Info("queue maintenance",
					slog.Int("pruned", pruned),
					slog.Int64("compacted_bytes_saved", saved))
			}
			// Approvals: prune expired pending requests + apply/reject
			// records older than 30d. Avoids unbounded bbolt growth in
			// long-running installs.
			if w.PruneApprovals != nil {
				if n, aerr := w.PruneApprovals(); aerr != nil {
					logger.Warn("prune approvals", slog.String("err", aerr.Error()))
				} else if n > 0 {
					logger.Info("approvals pruned", slog.Int("count", n))
				}
			}
		case <-tick.C:
			w.processOne(ctx, logger)
		}
	}
}

func (w *Worker) processOne(ctx context.Context, logger *slog.Logger) {
	job, err := w.Queue.Peek()
	if err != nil {
		logger.Warn("peek", slog.String("err", err.Error()))
		return
	}
	if job == nil {
		return
	}
	if err := w.Queue.Claim(job.Key, LeaseDuration); err != nil {
		logger.Warn("claim", slog.String("err", err.Error()), slog.String("key", job.Key))
		return
	}

	cfg, err := w.ConfigFn()
	if err != nil {
		// Could not load config — release the claim, try again next tick.
		if _, ferr := w.Queue.Fail(job.Key, "config load: "+err.Error()); ferr != nil {
			logger.Warn("queue fail on config load",
				slog.String("err", ferr.Error()),
				slog.String("key", job.Key))
		}
		return
	}
	d := cfg.FindDeploy(job.Deploy)
	if d == nil {
		// Deploy was removed between webhook and now. Ack to drop the job.
		logger.Info("deploy gone, dropping job",
			slog.String("deploy", job.Deploy),
			slog.String("key", job.Key))
		_ = w.Queue.Ack(job.Key)
		return
	}

	logger.Info("apply",
		slog.String("deploy", job.Deploy),
		slog.String("sha", job.SHA),
		slog.String("key", job.Key),
		slog.Int("retries", job.Retries))

	w.emitState(job.Deploy, "building", job.SHA, "")

	// Pipe build output through a publisher → topic deploy.<name>.stdout.
	// Dashboards subscribe to that topic and append each line in rAF.
	logsink := newPublishingSink(w.Publish, job.Deploy)

	if w.inProcessJobs == nil {
		w.inProcessJobs = deploy.NewJobQueue()
	}
	res, derr := w.inProcessJobs.Submit(ctx, deploy.ApplyRequest{
		Name:        d.Name,
		Repo:        d.Repo,
		Branch:      pickBranch(d.Branch, job.Branch),
		Port:        d.Port,
		RuntimeHint: d.Runtime,
		Build:       d.Build,
		Start:       d.Start,
		HealthPath:  d.HealthPath,
		Logsink:     logsink,
		Metrics:     w.ApplyMetrics,
	}, job.SHA)

	if derr != nil {
		if serr := cfg.SetDeployStatus(d.Name, "", "failed", derr.Error()); serr != nil {
			logger.Warn("set deploy status",
				slog.String("deploy", d.Name),
				slog.String("err", serr.Error()))
		}
		if cerr := cfg.Save(); cerr != nil {
			logger.Warn("config save after failure",
				slog.String("deploy", d.Name),
				slog.String("err", cerr.Error()))
		}
		deadLettered, ferr := w.Queue.Fail(job.Key, derr.Error())
		if ferr != nil {
			logger.Warn("queue fail",
				slog.String("err", ferr.Error()),
				slog.String("key", job.Key))
		}
		w.emitState(d.Name, "failed", job.SHA, derr.Error())
		if w.AlertHook != nil {
			// Transient failures → warning (retry will run). DLQ →
			// critical (permanent, requires operator). We split the
			// channel so on-call routing can suppress retries while
			// still paging on the permanent failure.
			if deadLettered {
				w.AlertHook("webhook.dead_letter", "critical",
					"Deploy permanently failed (dead-letter): "+d.Name,
					"sha "+job.SHA+" exhausted retries — "+derr.Error()+
						"\nInspect with `apigw webhook status` and either "+
						"`apigw deploy run "+d.Name+" --force` or delete the dead job.",
					"deploy/"+d.Name)
			} else {
				w.AlertHook("deploy.failed", "warning",
					"Deploy failed (will retry): "+d.Name,
					"sha "+job.SHA+" — "+derr.Error(),
					"deploy/"+d.Name)
			}
		}
		logger.Error("apply failed",
			slog.String("deploy", d.Name),
			slog.String("err", derr.Error()),
			slog.Int("retries_will_be", job.Retries+1))
		return
	}

	if serr := cfg.SetDeployStatus(d.Name, res.SHA, statusFor(res), ""); serr != nil {
		logger.Warn("set deploy status",
			slog.String("deploy", d.Name),
			slog.String("err", serr.Error()))
	}
	if cerr := cfg.Save(); cerr != nil {
		logger.Warn("config save after success",
			slog.String("deploy", d.Name),
			slog.String("err", cerr.Error()))
	}
	if err := w.Queue.Ack(job.Key); err != nil {
		logger.Warn("ack", slog.String("err", err.Error()))
	}
	w.emitState(d.Name, "ok", res.SHA, "")
	logger.Info("apply ok",
		slog.String("deploy", d.Name),
		slog.String("sha", res.SHA),
		slog.Bool("skipped", res.Skipped),
		slog.Duration("took", res.Duration))
}

// emitState publishes a deploy state transition on the hub. No-op when the
// worker isn't wired to a hub.
func (w *Worker) emitState(deployName, status, sha, errMsg string) {
	if w.Publish == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"deploy": deployName,
		"status": status,
		"sha":    sha,
		"error":  errMsg,
		"ts":     time.Now().Unix(),
	})
	w.Publish("deploy."+deployName+".state", "state", payload)
}

// pickBranch returns the configured branch when set, otherwise the payload's.
//
// Branch-mismatch filtering happens at enqueue time (the webhook receiver
// in serve.go) so by the time we get here we know the configured branch is
// the right one to deploy. This function is intentionally simple — the
// previous "compare and prefer" logic was dead code masquerading as
// guidance.
func pickBranch(configured, fromPayload string) string {
	if configured != "" {
		return configured
	}
	return fromPayload
}

// statusFor maps an ApplyResult to the LastStatus string the config stores.
func statusFor(res deploy.ApplyResult) string {
	if res.Skipped {
		return "ok" // skipped == "already up to date, no work" — still OK
	}
	return "ok"
}
