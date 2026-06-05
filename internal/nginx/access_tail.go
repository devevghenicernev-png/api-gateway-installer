package nginx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nxadm/tail"
)

// AccessLogPath is the canonical nginx access log location on Debian/Ubuntu.
// Apt's nginx package writes here; the file is rotated by logrotate, and
// `nxadm/tail` with ReOpen handles both rename-style and truncate-style rotation.
const AccessLogPath = "/var/log/nginx/access.log"

// TailAccessLog publishes one event per access log line to the given hub.
//
// Topic: "nginx.access". Payload (single-line JSON):
//
//	{"line": "<raw log line>", "ts": <unix>}
//
// Returns nil + no goroutine when the access log file doesn't exist (e.g.
// host without nginx installed, or fresh install before the first request).
// Caller cancels via ctx.
//
// We deliberately don't parse the log format here — operators run different
// `log_format` directives, and the dashboard's JS can split on spaces if it
// wants structured data. Keeping the producer dumb means no formats break.
func TailAccessLog(ctx context.Context, publish func(topic, evType string, data []byte), logger *slog.Logger) error {
	if _, err := os.Stat(AccessLogPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	t, err := tail.TailFile(AccessLogPath, tail.Config{
		Follow:    true,
		ReOpen:    true,
		MustExist: true,
		Logger:    tail.DiscardingLogger,
		// SeekInfo: nil — default is start-from-end, which is what we want
		// for live tailing. Restarts don't replay the whole file.
	})
	if err != nil {
		return err
	}
	go func() {
		defer func() { _ = t.Stop() }()
		for {
			select {
			case <-ctx.Done():
				return
			case line, ok := <-t.Lines:
				if !ok {
					return
				}
				if line.Err != nil {
					if logger != nil {
						logger.Warn("nginx access tail", slog.String("err", line.Err.Error()))
					}
					continue
				}
				payload, _ := json.Marshal(map[string]any{
					"line": strings.TrimRight(line.Text, "\r"),
					"ts":   time.Now().Unix(),
				})
				publish("nginx.access", "access", payload)
			}
		}
	}()
	return nil
}
