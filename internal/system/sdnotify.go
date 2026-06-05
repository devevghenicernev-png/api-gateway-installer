package system

import (
	"context"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
)

// SdNotifyReady tells systemd we're up and ready to serve. Safe to call
// from any process — no-op when NOTIFY_SOCKET isn't set (i.e. not started
// by systemd Type=notify). Used by `apigw dashboard serve` and `apigw
// webhook serve` immediately after their HTTP listener binds.
func SdNotifyReady() error {
	_, err := daemon.SdNotify(false, daemon.SdNotifyReady)
	return err
}

// SdNotifyStopping tells systemd we're about to exit. Same no-op semantics
// as SdNotifyReady; gives systemd a heads-up so it doesn't classify a
// graceful Ctrl-C as a fault and trip Restart=on-failure.
func SdNotifyStopping() error {
	_, err := daemon.SdNotify(false, daemon.SdNotifyStopping)
	return err
}

// WatchdogTick maintains the systemd `WatchdogSec=30s` heartbeat. Spawns
// a goroutine that calls SdNotify(WATCHDOG=1) at half the configured
// interval (so a missed tick still leaves margin). The goroutine exits
// cleanly on ctx.Done().
//
// Returns nil + no goroutine when systemd didn't configure a watchdog
// (NOTIFY_SOCKET unset or WATCHDOG_USEC env missing).
func WatchdogTick(ctx context.Context) error {
	interval, err := daemon.SdWatchdogEnabled(false)
	if err != nil {
		return err
	}
	if interval == 0 {
		return nil
	}
	tick := interval / 2
	if tick < time.Second {
		tick = time.Second
	}
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_, _ = daemon.SdNotify(false, daemon.SdNotifyWatchdog)
			}
		}
	}()
	return nil
}
