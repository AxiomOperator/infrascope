package netmon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
)

const (
	monitorFailureLogInterval = 5 * time.Minute
	// maxUnsentChecks bounds the check events kept between default interval results.
	maxUnsentChecks = 120
)

// monitorTask coordinates a probe and its history for one immutable configuration.
type monitorTask struct {
	config         monitor.Config
	ctx            context.Context
	cancel         context.CancelFunc
	history        *monitorHistory
	resumeGuard    *monitorResumeGuard
	runMu          sync.Mutex
	inflight       *monitorRun
	lastFailureLog int64 // Unix nanoseconds
	lastFailed     bool  // the latest published check failed
	onCheck        func(id string, event monitor.CheckEvent)
	// unsentChecks holds scheduled and external check events, oldest first, until
	// a default interval result reports them. Guarded by runMu.
	unsentChecks  []monitor.CheckEvent
	droppedChecks uint32

	certMu        sync.Mutex
	cert          *monitor.CertInfo
	certUnsent    bool // cert has not been included in a stats result yet
	certChecking  bool
	nextCertCheck time.Time
}

type monitorRun struct {
	done   chan struct{}
	result *monitor.Result // published by closing done; never mutated afterwards
}

// monitorPublication describes a check recorded in history.
type monitorPublication struct {
	result     monitor.Result
	event      monitor.CheckEvent
	logFailure bool
}

func newMonitorTask(config monitor.Config) *monitorTask {
	ctx, cancel := context.WithCancel(context.Background())
	task := &monitorTask{config: config, ctx: ctx, history: newMonitorHistory()}
	// Serialize cancellation with publication, so canceled probes cannot enter
	// history copied into a replacement task.
	task.cancel = func() {
		task.runMu.Lock()
		cancel()
		task.runMu.Unlock()
	}
	return task
}

func newMonitorTaskFromExisting(config monitor.Config, existing *monitorTask) *monitorTask {
	task := newMonitorTask(config)
	if existing != nil {
		task.history = existing.history.clone()
		// Keep the last known certificate, but check again soon for the new config.
		// The hub already stores it, so it is not marked unsent.
		if config.Target == existing.config.Target {
			task.cert = existing.certInfo()
		}
		existing.runMu.Lock()
		task.unsentChecks = append([]monitor.CheckEvent(nil), existing.unsentChecks...)
		task.droppedChecks = existing.droppedChecks
		existing.runMu.Unlock()
	}
	return task
}

// runProbe runs a scheduled check; see run.
func (task *monitorTask) runProbe(probe monitorProbe) *monitor.Result {
	return task.run(probe, false)
}

// run shares an in-flight check between scheduled and immediate requests.
// Every completed check contributes exactly one sample and one check event,
// regardless of how many callers were waiting for it. No task or history lock
// is held during network I/O.
//
// A check started by an immediate request reports its event only in that
// request's result (Checks), not in later default interval results, so each
// event is reported once. Callers that join an in-flight check receive no
// Checks, since the event is reported by whoever started it.
func (task *monitorTask) run(probe monitorProbe, immediate bool) *monitor.Result {
	task.runMu.Lock()
	if task.ctx.Err() != nil {
		task.runMu.Unlock()
		return nil
	}
	if run := task.inflight; run != nil {
		task.runMu.Unlock()
		select {
		case <-task.ctx.Done():
			return nil
		case <-run.done:
			if task.ctx.Err() != nil {
				return nil
			}
			return copyMonitorResult(run.result)
		}
	}
	run := &monitorRun{done: make(chan struct{})}
	task.inflight = run
	task.runMu.Unlock()

	generation, _ := task.resumeGuard.snapshot()
	out := probe(task.ctx, task.config)
	var published *monitorPublication
	task.runMu.Lock()
	currentGeneration, _ := task.resumeGuard.snapshot()
	if task.ctx.Err() == nil && generation == currentGeneration {
		published = task.publishLocked(out, time.Now(), !immediate)
		run.result = &published.result
	}

	task.inflight = nil
	close(run.done)
	task.runMu.Unlock()
	if published != nil {
		task.afterPublish(published, out.Err)
	}
	if published == nil || task.ctx.Err() != nil {
		return nil
	}
	result := copyMonitorResult(run.result)
	if immediate {
		result.Checks = []monitor.CheckEvent{published.event}
	}
	return result
}

// recordExternal records an outcome reported from outside the task, such as a
// push, as if the task had probed it. It reports false if the task was canceled.
func (task *monitorTask) recordExternal(out Outcome) bool {
	task.runMu.Lock()
	if task.ctx.Err() != nil {
		task.runMu.Unlock()
		return false
	}
	published := task.publishLocked(out, time.Now(), true)
	task.runMu.Unlock()
	task.afterPublish(published, out.Err)
	return true
}

// publishLocked requires runMu. It records the outcome in history and, when
// queue is set, in the unsent check events.
func (task *monitorTask) publishLocked(out Outcome, now time.Time, queue bool) *monitorPublication {
	published := &monitorPublication{event: monitor.CheckEvent{At: now.UnixMilli(), ResponseUs: out.ResponseUs, StatusCode: out.StatusCode}}
	if out.Err != nil {
		published.event.ResponseUs = -1
		published.event.Err = checkErrString(out.Err)
		logAt := now.UnixNano()
		if task.lastFailureLog == 0 || logAt < task.lastFailureLog || logAt-task.lastFailureLog >= int64(monitorFailureLogInterval) {
			published.logFailure = true
			task.lastFailureLog = logAt
		}
	} else {
		task.lastFailureLog = 0
	}
	task.lastFailed = out.Err != nil
	if queue {
		task.queueCheckLocked(published.event)
	}
	published.result = task.history.record(monitorSample{responseUs: published.event.ResponseUs, timestamp: now})
	return published
}

// queueCheckLocked requires runMu. The oldest event is dropped when full.
func (task *monitorTask) queueCheckLocked(event monitor.CheckEvent) {
	if n := len(task.unsentChecks); n > 0 && task.unsentChecks[n-1].Err == event.Err {
		// Share repeated error text instead of keeping a copy per event.
		event.Err = task.unsentChecks[n-1].Err
	}
	if len(task.unsentChecks) >= maxUnsentChecks {
		n := copy(task.unsentChecks, task.unsentChecks[1:])
		task.unsentChecks = task.unsentChecks[:n]
		task.droppedChecks++
	}
	task.unsentChecks = append(task.unsentChecks, event)
}

// afterPublish runs the check callback and failure log outside locks.
func (task *monitorTask) afterPublish(published *monitorPublication, err error) {
	if task.onCheck != nil {
		task.onCheck(task.config.ID, published.event)
	}
	if published.logFailure {
		slog.Warn("monitor failed", "err", err, "target", task.config.Target, "protocol", task.config.Protocol)
	}
}

// lastCheckFailed reports whether the latest published check failed.
func (task *monitorTask) lastCheckFailed() bool {
	task.runMu.Lock()
	defer task.runMu.Unlock()
	return task.lastFailed
}

// takeUnsentChecks returns and clears unsent check events, with repeated
// errors omitted (see monitor.CheckEvent), and the number of dropped events.
func (task *monitorTask) takeUnsentChecks() ([]monitor.CheckEvent, uint32) {
	task.runMu.Lock()
	checks, dropped := task.unsentChecks, task.droppedChecks
	task.unsentChecks, task.droppedChecks = nil, 0
	task.runMu.Unlock()
	monitor.CompactCheckErrors(checks)
	return checks, dropped
}

// refreshCert checks the certificate of an HTTPS target when due. A failed
// check keeps the last known certificate and retries sooner, as does a
// certificate that expires before the next regular check, so renewals show up
// quickly. Concurrent callers skip rather than wait, and no lock is held during
// network I/O.
func (task *monitorTask) refreshCert(check certChecker) {
	if check == nil || !certCheckEnabled(task.config) {
		return
	}
	task.certMu.Lock()
	if task.certChecking || time.Now().Before(task.nextCertCheck) {
		task.certMu.Unlock()
		return
	}
	task.certChecking = true
	task.certMu.Unlock()

	// Bound the check by the probe timeout, so an immediate request running it
	// alongside a probe fits the hub's request budget.
	ctx, cancel := context.WithTimeout(task.ctx, task.config.ProbeTimeout())
	info, err := check(ctx, task.config.Target)
	cancel()

	task.certMu.Lock()
	defer task.certMu.Unlock()
	task.certChecking = false
	if task.ctx.Err() != nil {
		return
	}
	if err != nil {
		task.nextCertCheck = time.Now().Add(certCheckRetryInterval)
		slog.Warn("certificate check failed", "err", err, "target", task.config.Target)
		return
	}
	task.cert = &info
	task.certUnsent = true
	now := time.Now()
	interval := certCheckInterval
	if time.UnixMilli(info.Expires).Before(now.Add(certCheckInterval)) {
		interval = certCheckRetryInterval
	}
	task.nextCertCheck = now.Add(interval)
}

// certInfo returns a copy of the latest certificate info, or nil if unknown.
func (task *monitorTask) certInfo() *monitor.CertInfo {
	task.certMu.Lock()
	defer task.certMu.Unlock()
	if task.cert == nil {
		return nil
	}
	cert := *task.cert
	return &cert
}

// takeUnsentCert returns the latest certificate info once after each successful
// check, so unchanged info is not resent with every stats result.
func (task *monitorTask) takeUnsentCert() *monitor.CertInfo {
	task.certMu.Lock()
	defer task.certMu.Unlock()
	if !task.certUnsent {
		return nil
	}
	task.certUnsent = false
	cert := *task.cert
	return &cert
}

func copyMonitorResult(result *monitor.Result) *monitor.Result {
	if result == nil {
		return nil
	}
	copy := *result
	return &copy
}
