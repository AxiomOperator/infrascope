package hub

import (
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/monitorloc"
	"github.com/henrygd/beszel/internal/hub/systems"
	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/henrygd/beszel/internal/hub/utils"
	"github.com/henrygd/beszel/internal/netmon"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// hubMonitorRunner runs the monitors with the hub as one of their locations
// (see monitorloc), including push monitors.
type hubMonitorRunner interface {
	// Sync starts, reconfigures or, for a disabled record, stops the record's monitor.
	Sync(record *core.Record)
	// Remove stops the monitor with the given id, if it runs.
	Remove(id string)
}

// noopHubMonitorRunner is the runner used when HUB_MONITORS=false. Hub
// monitor records can be created and edited, but are not probed.
type noopHubMonitorRunner struct{}

func (noopHubMonitorRunner) Sync(*core.Record) {}

func (noopHubMonitorRunner) Remove(string) {}

const (
	// hubMonitorResultsMs is the GetResults window whose results the collector
	// stores. It matches the agents' default interval.
	hubMonitorResultsMs = 60_000
	// hubMonitorCollectOffset places collection away from the */10 rollup cron.
	hubMonitorCollectOffset = 37 * time.Second
	// pushDeadlineTick is how often push deadlines are checked.
	pushDeadlineTick = time.Second
	// minPushGrace is the shortest allowance for late pushes.
	minPushGrace = 10 * time.Second
	// noPushError is the error of the failure recorded for a missed push.
	noPushError = "no push received"
)

// newHubMonitorRunner returns the runner selected by HUB_MONITORS (enabled
// unless "false"). onSaved, if not nil, receives the results after each save.
func newHubMonitorRunner(app core.App, engine *uptime.Engine, onSaved func(map[string]monitor.Result)) hubMonitorRunner {
	if value, _ := utils.GetEnv("HUB_MONITORS"); value == "false" {
		return noopHubMonitorRunner{}
	}
	r := newHubRunner(app, engine)
	r.onSaved = onSaved
	return r
}

// hubRunner probes hub monitors with a netmon.Manager, feeds every check to
// the uptime engine as it completes, stores result stats every minute and
// receives push monitor checks.
//
// Checks reach the engine only through the manager's check callback (probes,
// immediate runs, pushes and missed push deadlines alike). The collector
// ignores the Checks of the results it stores, so no check is applied twice.
//
// The runner is inert until start: Sync and Remove before it do nothing, and
// start loads all enabled hub monitors.
type hubRunner struct {
	app    core.App
	engine *uptime.Engine
	mgr    *netmon.Manager

	// mu guards the fields below and serializes Sync, Remove and start.
	mu      sync.Mutex
	started bool
	stopped bool
	// running holds the enabled hub monitors known to the manager.
	running map[string]bool
	// tokens maps push tokens of enabled push monitors to monitor IDs.
	tokens map[string]string
	// pushes holds the deadline state of enabled push monitors.
	pushes map[string]*pushState
	limits *pushLimiter
	stop   chan struct{}
	wg     sync.WaitGroup

	// Checks from the manager callback are queued and applied to the engine
	// by the dispatcher, so probe goroutines never wait on engine writes.
	checkMu     sync.Mutex
	checks      []queuedCheck
	checkSignal chan struct{}
	// observeMu serializes applying queued checks, keeping their order.
	observeMu sync.Mutex

	// savedProbes holds the LastProbeAt of the latest stats row per monitor.
	// Only the collector uses it (collectMu).
	collectMu   sync.Mutex
	savedProbes map[string]int64
	// onSaved, if set, receives the results after each committed save.
	onSaved func(results map[string]monitor.Result)
}

type queuedCheck struct {
	id    string
	event monitor.CheckEvent
}

// pushState tracks when a push monitor last received a push or missed one.
type pushState struct {
	interval time.Duration
	// base is the latest push, the last missed deadline minus the grace, or
	// the time monitoring started. The next failure is due after
	// base + interval + grace.
	base time.Time
}

// grace is how late a push may arrive: a tenth of the interval, at least minPushGrace.
func (p *pushState) grace() time.Duration {
	return max(minPushGrace, p.interval/10)
}

func newHubRunner(app core.App, engine *uptime.Engine) *hubRunner {
	r := &hubRunner{
		app:         app,
		engine:      engine,
		running:     map[string]bool{},
		tokens:      map[string]string{},
		pushes:      map[string]*pushState{},
		checkSignal: make(chan struct{}, 1),
		savedProbes: map[string]int64{},
	}
	r.mgr = netmon.NewManager(hubMonitorResultsMs,
		netmon.WithUserAgent("Beszel-Hub/"+beszel.Version),
		netmon.WithConcurrency(32),
		netmon.WithOnCheck(r.onCheck),
	)
	return r
}

// start loads all enabled hub monitors and starts the dispatcher, the
// collector and the push deadline loop. Call it after the engine loaded.
func (r *hubRunner) start() error {
	all, err := r.app.FindAllRecords("network_monitors", dbx.HashExp{"enabled": true})
	if err != nil {
		return err
	}
	records := slices.DeleteFunc(all, func(record *core.Record) bool {
		return !monitorloc.Has(record, monitorloc.Hub)
	})
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return nil
	}
	r.started = true
	r.stop = make(chan struct{})
	r.limits = newPushLimiter()
	configs := make([]monitor.Config, 0, len(records))
	for _, record := range records {
		// Agent-only monitors cannot run on the hub; the hooks never store
		// them without a system.
		if monitor.IsAgentOnlyProtocol(record.GetString("protocol")) {
			continue
		}
		config, err := systems.MonitorConfigFromRecord(r.app, record)
		if err != nil {
			r.app.Logger().Warn("Skipping hub monitor with invalid config", "monitor", record.Id, "err", err)
			continue
		}
		configs = append(configs, config)
		r.running[record.Id] = true
		if config.Protocol == monitor.ProtocolPush {
			r.trackPushLocked(record, now)
		}
	}
	r.mgr.SyncMonitors(configs)

	r.wg.Add(3)
	go r.dispatch()
	go r.collectLoop()
	go r.deadlineLoop()
	return nil
}

// Stop stops the background loops, stores the latest results, stops probing
// and applies the checks still queued.
func (r *hubRunner) Stop() {
	r.mu.Lock()
	if !r.started || r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	close(r.stop)
	r.mu.Unlock()

	r.wg.Wait()
	r.limits.stop()
	if err := r.collect(); err != nil {
		r.app.Logger().Warn("Failed to save hub monitor results", "err", err)
	}
	r.mgr.Stop()
	r.applyChecks()
}

// Sync implements hubMonitorRunner.
func (r *hubRunner) Sync(record *core.Record) {
	if !monitorloc.Has(record, monitorloc.Hub) || !record.GetBool("enabled") || monitor.IsAgentOnlyProtocol(record.GetString("protocol")) {
		r.Remove(record.Id)
		return
	}
	config, err := systems.MonitorConfigFromRecord(r.app, record)
	if err != nil {
		r.app.Logger().Warn("Failed to sync hub monitor", "monitor", record.Id, "err", err)
		r.Remove(record.Id)
		return
	}

	r.mu.Lock()
	if !r.started || r.stopped {
		r.mu.Unlock()
		return
	}
	isNew := !r.running[record.Id]
	r.running[record.Id] = true
	r.removeTokenLocked(record.Id)
	if config.Protocol == monitor.ProtocolPush {
		r.trackPushLocked(record, time.Now())
	} else {
		delete(r.pushes, record.Id)
	}
	// Upsert under mu, so a concurrent Remove cannot be overtaken.
	_, err = r.mgr.UpsertMonitor(config, false)
	r.mu.Unlock()
	if err != nil {
		r.app.Logger().Warn("Failed to sync hub monitor", "monitor", record.Id, "err", err)
		return
	}
	// Probe new and re-enabled monitors right away, so their first status
	// appears without waiting for the staggered schedule. The check reaches
	// the engine through the callback; the request does not wait for it.
	if isNew && config.Protocol != monitor.ProtocolPush {
		go func() { _, _ = r.mgr.UpsertMonitor(config, true) }()
	}
}

// Remove implements hubMonitorRunner.
func (r *hubRunner) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || r.stopped {
		return
	}
	delete(r.running, id)
	delete(r.pushes, id)
	r.removeTokenLocked(id)
	r.mgr.DeleteMonitor(id)
}

// trackPushLocked registers the token of a push monitor and keeps or starts
// its deadline. Requires mu.
func (r *hubRunner) trackPushLocked(record *core.Record, now time.Time) {
	if token := record.GetString("pushToken"); token != "" {
		r.tokens[token] = record.Id
	}
	interval := time.Duration(record.GetInt("interval")) * time.Second
	if state, ok := r.pushes[record.Id]; ok {
		state.interval = interval
		return
	}
	// A restarted hub or re-enabled monitor allows one full interval from
	// now, or from the last check if that is later.
	base := now
	if lastCheck := time.UnixMilli(int64(record.GetFloat("lastCheck"))); lastCheck.After(base) {
		base = lastCheck
	}
	r.pushes[record.Id] = &pushState{interval: interval, base: base}
}

// removeTokenLocked forgets the push token of a monitor. Requires mu.
func (r *hubRunner) removeTokenLocked(id string) {
	for token, monitorID := range r.tokens {
		if monitorID == id {
			delete(r.tokens, token)
		}
	}
}

// monitorForToken returns the ID of the enabled push monitor with the token.
func (r *hubRunner) monitorForToken(token string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.tokens[token]
	return id, ok
}

// recordPush records a push as a check of the monitor and restarts its deadline.
func (r *hubRunner) recordPush(id string, out netmon.Outcome) error {
	r.mu.Lock()
	state, ok := r.pushes[id]
	if ok {
		state.base = time.Now()
	}
	r.mu.Unlock()
	if !ok {
		return errors.New("unknown push monitor")
	}
	return r.mgr.RecordExternal(id, out)
}

// checkPushDeadlines records one failure for each push monitor whose push is
// overdue, and restarts its deadline so the next failure follows one interval
// later. With retries n, a monitor is down after n+1 missed intervals.
func (r *hubRunner) checkPushDeadlines(now time.Time) {
	var due []string
	r.mu.Lock()
	for id, state := range r.pushes {
		grace := state.grace()
		if now.Sub(state.base) > state.interval+grace {
			due = append(due, id)
			state.base = now.Add(-grace)
		}
	}
	r.mu.Unlock()
	for _, id := range due {
		_ = r.mgr.RecordExternal(id, netmon.Outcome{ResponseUs: -1, Err: errors.New(noPushError)})
	}
}

// onCheck is the manager's check callback. It only queues the check.
func (r *hubRunner) onCheck(id string, event monitor.CheckEvent) {
	r.checkMu.Lock()
	r.checks = append(r.checks, queuedCheck{id: id, event: event})
	r.checkMu.Unlock()
	select {
	case r.checkSignal <- struct{}{}:
	default:
	}
}

// applyChecks feeds queued checks to the engine, oldest first. Checks queued
// before it is called are applied when it returns.
func (r *hubRunner) applyChecks() {
	r.observeMu.Lock()
	defer r.observeMu.Unlock()
	r.checkMu.Lock()
	checks := r.checks
	r.checks = nil
	r.checkMu.Unlock()
	if len(checks) == 0 {
		return
	}
	byMonitor := map[string][]monitor.CheckEvent{}
	var order []string
	for _, check := range checks {
		if _, ok := byMonitor[check.id]; !ok {
			order = append(order, check.id)
		}
		byMonitor[check.id] = append(byMonitor[check.id], check.event)
	}
	for _, id := range order {
		r.engine.Observe(id, byMonitor[id])
	}
}

func (r *hubRunner) dispatch() {
	defer r.wg.Done()
	for {
		select {
		case <-r.stop:
			return
		case <-r.checkSignal:
			r.applyChecks()
		}
	}
}

// collectLoop stores results every minute, at hubMonitorCollectOffset past the minute.
func (r *hubRunner) collectLoop() {
	defer r.wg.Done()
	now := time.Now()
	next := now.Truncate(time.Minute).Add(hubMonitorCollectOffset)
	if !next.After(now) {
		next = next.Add(time.Minute)
	}
	timer := time.NewTimer(next.Sub(now))
	defer timer.Stop()
	select {
	case <-r.stop:
		return
	case <-timer.C:
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := r.collect(); err != nil {
			r.app.Logger().Warn("Failed to save hub monitor results", "err", err)
		}
		select {
		case <-r.stop:
			return
		case <-ticker.C:
		}
	}
}

// collect stores the default-interval results of all hub monitors. Their
// checks were already applied through the callback and are dropped here.
func (r *hubRunner) collect() error {
	results, err := r.saveResults()
	if err == nil && len(results) > 0 && r.onSaved != nil {
		// Outside collectMu: alert delivery may be slow.
		r.onSaved(results)
	}
	return err
}

// saveResults stores the current results and returns those it stored.
func (r *hubRunner) saveResults() (map[string]monitor.Result, error) {
	r.collectMu.Lock()
	defer r.collectMu.Unlock()
	results := r.mgr.GetResults(hubMonitorResultsMs)
	for id, result := range results {
		result.Checks, result.Dropped = nil, 0
		results[id] = result
	}
	// Forget monitors that no longer run.
	for id := range r.savedProbes {
		if _, ok := results[id]; !ok {
			delete(r.savedProbes, id)
		}
	}
	if len(results) == 0 {
		return nil, nil
	}
	saved := map[string]int64{}
	err := r.app.RunInTransaction(func(txApp core.App) error {
		return systems.SaveMonitorResults(txApp, "", results, r.savedProbes, saved)
	})
	if err != nil {
		return nil, err
	}
	for id, at := range saved {
		r.savedProbes[id] = at
	}
	return results, nil
}

func (r *hubRunner) deadlineLoop() {
	defer r.wg.Done()
	ticker := time.NewTicker(pushDeadlineTick)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case now := <-ticker.C:
			r.checkPushDeadlines(now)
		}
	}
}

// startHubMonitors starts the hub monitor runner, if enabled, and stops it
// when the app terminates. Call it after the uptime engine loaded and before
// startUptimeEngine, so the runner's last checks are flushed by the engine.
func (h *Hub) startHubMonitors() error {
	runner, ok := h.hubMonitors.(*hubRunner)
	if !ok {
		return nil
	}
	if err := runner.start(); err != nil {
		return err
	}
	h.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		runner.Stop()
		return e.Next()
	})
	return nil
}
