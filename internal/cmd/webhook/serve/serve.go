// Package serve implements `apigw webhook serve` — the long-running server
// the systemd unit invokes. Users normally don't run this directly.
//
// IMPORTANT: this is a long-lived process, so we cannot use Factory.Config
// (the lazy memoised loader) — we'd serve a snapshot frozen at startup and
// miss subsequent `apigw deploy add` writes. Both the HTTP handler and the
// worker re-load config on every iteration via reloadConfig() below.
package serve

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/system"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
)

func NewCmdServe(f *cmdutil.Factory) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:    "serve",
		Short:  "Run the webhook server + worker (used by systemd; not for humans)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return run(c.Context(), f, addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":9000", "listen address")
	return cmd
}

func run(ctx context.Context, f *cmdutil.Factory, addr string) error {
	listenAddr, err := webhook.ResolveListenAddr(addr)
	if err != nil {
		return err
	}

	q, err := webhook.OpenQueue()
	if err != nil {
		return fmt.Errorf("open queue: %w", err)
	}
	defer q.Close()

	logger := f.Logger
	if logger == nil {
		logger = slog.Default()
	}

	srv := webhook.New(listenAddr, makeHandler(q, logger), logger)
	worker := &webhook.Worker{
		Queue:    q,
		Logger:   logger,
		ConfigFn: reloadConfig, // fresh read every poll
	}

	logger.Info("apigw webhook starting",
		slog.String("addr", listenAddr),
		slog.String("queue", webhook.QueueDBPath))

	_ = system.SdNotifyReady()
	_ = system.WatchdogTick(ctx)
	defer func() { _ = system.SdNotifyStopping() }()

	errCh := make(chan error, 2)
	go func() { errCh <- srv.ListenAndServe(ctx) }()
	go func() { errCh <- worker.Run(ctx) }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

// reloadConfig bypasses Factory's lazy cache. The long-running server MUST
// see updates to /etc/apigw/config.yaml made by other apigw invocations
// (e.g. `apigw deploy add` from a CLI shell on the same host).
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

// makeHandler returns the PayloadHandler the HTTP server invokes. The
// closure re-reads config every call so a deploy added after the server
// started is immediately routable.
func makeHandler(q *webhook.Queue, logger *slog.Logger) webhook.PayloadHandler {
	return func(ctx context.Context, deployName, event string, body []byte) error {
		if webhook.IsPing(event) {
			return nil
		}
		if event != "push" {
			return nil
		}
		branch, sha, repo, err := webhook.ParsePush(body)
		if err != nil {
			if err == webhook.ErrIgnore {
				return nil
			}
			return fmt.Errorf("parse push: %w", err)
		}

		cfg, err := reloadConfig()
		if err != nil {
			return fmt.Errorf("reload config: %w", err)
		}
		d := cfg.FindDeploy(deployName)
		if d == nil {
			logger.Info("webhook for unknown deploy",
				slog.String("deploy", deployName))
			return nil
		}
		if d.Branch != "" && d.Branch != branch {
			logger.Info("branch filtered",
				slog.String("deploy", deployName),
				slog.String("configured", d.Branch),
				slog.String("payload", branch))
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
