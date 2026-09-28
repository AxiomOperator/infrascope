package netmon

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
)

func (pm *Manager) startMonitor(task *monitorTask) {
	// Push monitors are fed by RecordExternal and never probe.
	if task.config.Protocol == monitor.ProtocolPush {
		return
	}
	interval := time.Duration(task.config.Interval) * time.Second
	if interval < time.Second {
		interval = 30 * time.Second
	}
	retryInterval := time.Duration(task.config.RetryInterval) * time.Second
	delay := getStagger(interval.Milliseconds())
	slog.Debug("starting monitor task", "target", task.config.Target, "delay", delay, "interval", interval)
	// Certificate checks piggyback on probe ticks, so they run at most once per
	// probe interval after they become due.
	go runMonitorSchedule(task.ctx, interval, retryInterval, delay, func() bool {
		if _, allowed := task.resumeGuard.snapshot(); allowed {
			task.runProbe(pm.probe)
			task.refreshCert(pm.certCheck)
		}
		return task.lastCheckFailed()
	})
}

// runMonitorSchedule owns only timing. Checks run serially, and slow checks
// naturally drop missed ticks rather than building an execution backlog.
// run reports whether the latest check failed; while it does, checks repeat
// every retryInterval instead of interval (if retryInterval is set).
func runMonitorSchedule(ctx context.Context, interval, retryInterval, delay time.Duration, run func() (failed bool)) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	if ctx.Err() != nil {
		return
	}
	next := func(failed bool) time.Duration {
		if failed && retryInterval > 0 {
			return retryInterval
		}
		return interval
	}
	current := next(run())
	ticker := time.NewTicker(current)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			// Only reset on change, so a steady state keeps its fixed cadence.
			if d := next(run()); d != current {
				current = d
				ticker.Reset(current)
			}
		}
	}
}

// getStagger returns an initial delay between half an interval and one interval.
func getStagger(intervalMilli int64) time.Duration {
	delay := rand.Intn(int(intervalMilli))
	if delay < int(intervalMilli)/2 {
		delay += int(intervalMilli) / 2
	}
	return time.Duration(delay) * time.Millisecond
}
