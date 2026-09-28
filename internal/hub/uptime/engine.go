package uptime

import (
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/pocketbase/pocketbase/core"
)

// State machine
//
// Each monitor tracks an underlying confirmed status (up/down) derived from
// its checks, and a displayed status stored in network_monitors.status:
//
//   - A successful check resets the failure streak and confirms "up".
//   - A failed check increments the failure streak. While the streak is at
//     most the monitor's retries, and the monitor is not already confirmed
//     down, the monitor is "pending" and keeps its confirmed status
//     underneath. The next failure confirms "down"; the down period starts
//     at the first failure of the streak.
//   - During a maintenance window (WithMaintenanceCheck) the displayed status
//     is "maintenance", while checks keep updating the confirmed status.
//   - "unknown" (agent disconnected, stale results, system down) and "paused"
//     (monitor disabled, system paused) hold the displayed status until the
//     next check (or re-enable). They reset the failure streak but keep the
//     confirmed and notified status.
//
// Displayed status precedence: paused (disabled) > maintenance > unknown or
// paused hold > pending > confirmed ("unknown" before the first confirmation).
//
// Notifications
//
// notified (persisted in the state JSON) is the last confirmed status that
// was notified or silently accepted. Whenever the confirmed status differs
// from it, while the monitor is not held unknown/paused and not in
// maintenance, notified is updated and a Transition is emitted when the
// monitor has notify enabled. Therefore:
//
//   - pending never notifies; pending -> up is silent.
//   - unknown and paused never notify, and after them only a confirmed
//     status that differs from notified does (no "up" on agent reconnect).
//   - A change hidden by maintenance is notified once when the window ends.
//   - With notify disabled, notified still follows the confirmed status, so
//     enabling notify later does not report stale changes.
//   - A new monitor (no notified status) accepts a first "up" silently but
//     notifies a first "down", so a monitor that is down when created alerts.
//
// Segments
//
// monitor_events holds one open row (end = 0) per monitor. When the displayed
// status changes among up, down, maintenance, unknown and paused, the open
// row is closed and a new one opened at the same time. Pending does not open
// a segment. A down segment starts at the first failure of its streak (but
// not before the segment it replaces) and stores that failure's error.
//
// Persistence
//
// Status changes, segments and the state JSON are queued while the engine
// lock is held and written in order, in one transaction per drain, without
// holding the lock. Check results (lastCheck, lastError, lastStatusCode,
// recent) and uptime are written by Flush. Transitions are passed to the
// notifier after the drain that persists them, without any lock held.

// Engine derives monitor status from check results. It is safe for
// concurrent use.
type Engine struct {
	app           core.App
	notifier      func([]Transition)
	inMaintenance func(monitorID string, now time.Time) bool
	now           func() time.Time

	mu       sync.Mutex
	monitors map[string]*monitorState
	queue    []op
	notices  []Transition

	// writeMu serializes drains so queued writes are persisted in order.
	writeMu sync.Mutex
}

// Option configures an Engine.
type Option func(*Engine)

// WithNotifier sets the function that receives confirmed status changes. It
// is called after the changes are persisted, without engine locks held.
func WithNotifier(fn func([]Transition)) Option {
	return func(e *Engine) { e.notifier = fn }
}

// WithMaintenanceCheck sets the function that reports whether a monitor is in
// a maintenance window. It is called without engine locks held.
func WithMaintenanceCheck(fn func(monitorID string, now time.Time) bool) Option {
	return func(e *Engine) { e.inMaintenance = fn }
}

// WithNow overrides the clock, for tests.
func WithNow(fn func() time.Time) Option {
	return func(e *Engine) { e.now = fn }
}

// New creates an engine. Call Load before feeding it results.
func New(app core.App, opts ...Option) *Engine {
	e := &Engine{app: app, now: time.Now, monitors: map[string]*monitorState{}}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// monitorState is the in-memory state of one monitor. Guarded by Engine.mu.
type monitorState struct {
	id, systemID, name, target, protocol string
	enabled, notify                      bool
	retries                              int
	interval                             time.Duration

	p persistedState
	// saved is the state JSON last queued for persistence.
	saved persistedState
	// hold is "unknown" or "paused" while probe results are unavailable.
	hold string
	// status is the displayed status last queued for persistence.
	status        string
	statusChanged time.Time
	segment       openSegment

	lastCheck      int64
	lastError      string
	lastStatusCode uint16
	recent         []RecentCheck
	// checksDirty marks check fields that Flush has not written yet.
	checksDirty bool
	// lastSeen is when the hub last received a check (or loaded the monitor).
	lastSeen time.Time
	uptimeAt time.Time
}

type openSegment struct {
	id, status string
	start      int64
}

// displayStatus returns the status to show for the current state.
func (st *monitorState) displayStatus() string {
	switch {
	case !st.enabled:
		return StatusPaused
	case st.p.Maintenance:
		return StatusMaintenance
	case st.hold != "":
		return st.hold
	case st.p.FailStreak > 0 && st.p.Confirmed != StatusDown:
		return StatusPending
	case st.p.Confirmed == "":
		return StatusUnknown
	}
	return st.p.Confirmed
}

func (st *monitorState) displayName() string {
	if st.name != "" {
		return st.name
	}
	return st.target
}

// resetStreak forgets the current failure streak.
func (st *monitorState) resetStreak() {
	st.p.FailStreak = 0
	st.p.PendingSince = 0
	st.p.PendingError = ""
	st.p.PendingStatusCode = 0
}

// staleAfter is how long a monitor may go without checks before it is unknown.
func (st *monitorState) staleAfter() time.Duration {
	return 2*max(st.interval, agentFetchInterval) + 2*agentFetchInterval
}

// Observe applies a monitor's checks, oldest first.
func (e *Engine) Observe(monitorID string, events []monitor.CheckEvent) {
	if len(events) == 0 {
		return
	}
	e.observe("", false, map[string][]monitor.CheckEvent{monitorID: events})
}

// ObserveResults applies the checks of an agent's default-interval results.
// Results for monitors that do not belong to systemID are ignored. legacy
// agents do not report individual checks, so one check is synthesised from
// each result's window (see ChecksFromResult).
func (e *Engine) ObserveResults(systemID string, results map[string]monitor.Result, legacy bool) {
	checks := make(map[string][]monitor.CheckEvent, len(results))
	for id, result := range results {
		if events := ChecksFromResult(result, legacy); len(events) > 0 {
			checks[id] = events
		}
	}
	if len(checks) > 0 {
		e.observe(systemID, true, checks)
	}
}

// ChecksFromResult returns the checks of an agent result with omitted errors
// restored. For legacy agents, which do not report checks, it synthesises one
// check from the result window: a failure when no probe succeeded, a success
// with the average response otherwise, and none without probes.
func ChecksFromResult(result monitor.Result, legacy bool) []monitor.CheckEvent {
	if len(result.Checks) > 0 {
		events := slices.Clone(result.Checks)
		monitor.ExpandCheckErrors(events)
		return events
	}
	if !legacy || result.TotalCount == 0 {
		return nil
	}
	event := monitor.CheckEvent{At: result.LastProbeAt}
	if result.SuccessCount == 0 {
		event.ResponseUs = -1
		event.Err = legacyFailureError
		return []monitor.CheckEvent{event}
	}
	event.ResponseUs = max(result.AvgResponse, 0)
	if event.ResponseUs == 0 && result.ResponseSum > 0 {
		event.ResponseUs = result.ResponseSum / result.SuccessCount
	}
	return []monitor.CheckEvent{event}
}

func (e *Engine) observe(systemID string, checkSystem bool, checks map[string][]monitor.CheckEvent) {
	now := e.now()
	maintenance := e.maintenanceStates(slices.Collect(maps.Keys(checks)), now)

	e.mu.Lock()
	for id, events := range checks {
		st, ok := e.monitors[id]
		if !ok || !st.enabled || (checkSystem && st.systemID != systemID) {
			continue
		}
		if inMaint, ok := maintenance[id]; ok {
			st.p.Maintenance = inMaint
		}
		for _, event := range events {
			e.applyCheck(st, event, now)
		}
		st.lastSeen = now
	}
	e.mu.Unlock()
	e.drain()
}

// applyCheck applies one check to the state machine. Requires e.mu.
func (e *Engine) applyCheck(st *monitorState, event monitor.CheckEvent, now time.Time) {
	// Agents without individual checks repeat their latest probe time until the next probe.
	if event.At != 0 && event.At == st.lastCheck {
		return
	}
	at := event.At
	if at <= 0 || at > now.UnixMilli() {
		at = now.UnixMilli()
	}
	at = max(at, st.segment.start)

	// A check ends an unknown or system-paused hold.
	st.hold = ""
	check := RecentCheck{At: at / 1000, State: RecentUp}
	if event.Failed() {
		st.p.FailStreak++
		if st.p.FailStreak == 1 {
			st.p.PendingSince = at
			st.p.PendingError = truncateError(event.Err)
			st.p.PendingStatusCode = event.StatusCode
		}
		if st.p.Confirmed != StatusDown && st.p.FailStreak > st.retries {
			st.p.Confirmed = StatusDown
			st.p.DownSince = st.p.PendingSince
		}
		check.State = RecentPending
		if st.p.Confirmed == StatusDown {
			check.State = RecentDown
		}
		check.ResponseMs = -1
		st.lastError = truncateError(event.Err)
	} else {
		st.resetStreak()
		st.p.Confirmed = StatusUp
		check.ResponseMs = float64(event.ResponseUs/10) / 100
		st.lastError = ""
	}
	st.lastCheck = event.At
	if st.lastCheck <= 0 {
		st.lastCheck = at
	}
	st.lastStatusCode = event.StatusCode
	st.recent = append(st.recent, check)
	if extra := len(st.recent) - recentSize; extra > 0 {
		st.recent = slices.Delete(st.recent, 0, extra)
	}
	st.checksDirty = true
	e.reconcile(st, at, true)
}

// reconcile queues the segment, status and state changes of st at time at
// (Unix milliseconds) and a notification when due. Requires e.mu.
func (e *Engine) reconcile(st *monitorState, at int64, allowNotify bool) {
	status := st.displayStatus()
	segmentStatus := status
	if status == StatusPending {
		segmentStatus = st.segment.status
	}
	if segmentStatus != "" && segmentStatus != st.segment.status {
		start := max(at, st.segment.start)
		next := openSegment{id: core.GenerateDefaultRandomId(), status: segmentStatus, start: start}
		row := op{kind: opOpen, monitorID: st.id, segmentID: next.id, status: segmentStatus}
		if segmentStatus == StatusDown && st.p.PendingSince > 0 {
			next.start = max(st.p.PendingSince, st.segment.start)
			row.err = st.p.PendingError
			row.statusCode = st.p.PendingStatusCode
		}
		row.start = next.start
		if st.segment.id != "" {
			e.queue = append(e.queue, op{kind: opClose, monitorID: st.id, segmentID: st.segment.id, end: next.start})
		}
		e.queue = append(e.queue, row)
		st.segment = next
	}

	if transition := e.evaluateNotify(st, at); transition != nil && allowNotify {
		e.notices = append(e.notices, *transition)
	}

	if status != st.status || st.p != st.saved {
		fields := map[string]any{}
		if status != st.status {
			st.status = status
			st.statusChanged = time.UnixMilli(at).UTC()
			fields["status"] = status
			fields["statusChanged"] = st.statusChanged
		}
		st.p.Version = stateVersion
		st.saved = st.p
		fields["state"] = st.p
		e.queueUpdate(st, fields)
	}
}

// queueUpdate queues fields of the monitor record together with pending check fields. Requires e.mu.
func (e *Engine) queueUpdate(st *monitorState, fields map[string]any) {
	if st.checksDirty {
		st.addCheckFields(fields)
	}
	e.queue = append(e.queue, op{kind: opUpdate, monitorID: st.id, fields: fields})
}

// addCheckFields adds the check result fields and marks them written. Requires e.mu.
func (st *monitorState) addCheckFields(fields map[string]any) {
	fields["lastCheck"] = st.lastCheck
	fields["lastError"] = st.lastError
	fields["lastStatusCode"] = st.lastStatusCode
	fields["recent"] = slices.Clone(st.recent)
	st.checksDirty = false
}

// evaluateNotify updates the notified status when the confirmed status
// changed and returns the transition to report, if any. Requires e.mu.
func (e *Engine) evaluateNotify(st *monitorState, at int64) *Transition {
	if !st.enabled || st.hold != "" || st.p.Maintenance {
		return nil
	}
	confirmed := st.p.Confirmed
	if confirmed == "" || confirmed == st.p.Notified {
		return nil
	}
	prev := st.p.Notified
	st.p.Notified = confirmed
	if !st.notify || (prev == "" && confirmed == StatusUp) {
		return nil
	}
	transition := &Transition{
		MonitorID: st.id,
		SystemID:  st.systemID,
		Name:      st.displayName(),
		Target:    st.target,
		Status:    confirmed,
		Prev:      prev,
		At:        time.UnixMilli(at).UTC(),
	}
	if st.p.DownSince > 0 {
		transition.DownSince = time.UnixMilli(st.p.DownSince).UTC()
	}
	if confirmed == StatusDown {
		transition.Err = st.p.PendingError
		transition.StatusCode = st.p.PendingStatusCode
	}
	return transition
}

// MarkUnknown sets monitors to unknown, for example when their agent stops
// reporting. It never notifies.
func (e *Engine) MarkUnknown(monitorIDs []string) {
	e.mu.Lock()
	queued := e.hold(monitorIDs, StatusUnknown, e.now().UnixMilli())
	e.mu.Unlock()
	if queued {
		e.tryDrain()
	}
}

// hold holds the displayed status of enabled monitors. Requires e.mu.
func (e *Engine) hold(monitorIDs []string, status string, at int64) bool {
	before := len(e.queue)
	for _, id := range monitorIDs {
		st, ok := e.monitors[id]
		if !ok || !st.enabled || st.hold == status {
			continue
		}
		st.hold = status
		st.resetStreak()
		e.reconcile(st, at, false)
	}
	return len(e.queue) > before
}

// SystemStatusChanged updates the monitors of a system after its status
// changed: "paused" pauses them, other statuses except "up" make them
// unknown. "up" turns monitors paused with the system into unknown until
// their next check.
func (e *Engine) SystemStatusChanged(systemID, status string) {
	if systemID == "" {
		return
	}
	e.mu.Lock()
	var ids []string
	for id, st := range e.monitors {
		if st.systemID == systemID && (status != StatusUp || st.hold == StatusPaused) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	hold := StatusUnknown
	if status == StatusPaused {
		hold = StatusPaused
	}
	queued := e.hold(ids, hold, e.now().UnixMilli())
	e.mu.Unlock()
	if queued {
		e.tryDrain()
	}
}

// Upsert adds or reconfigures a monitor from its record after it was created
// or updated. Disabled monitors are paused; re-enabled or moved monitors are
// unknown until their next check. It restores the record's status fields if
// they differ from the engine's (for example after a concurrent edit).
func (e *Engine) Upsert(record *core.Record) {
	now := e.now()
	var loaded *monitorState
	e.mu.Lock()
	_, exists := e.monitors[record.Id]
	e.mu.Unlock()
	if !exists {
		// Read outside the lock; the open segment is only present for monitors
		// the engine missed (for example created before Load).
		loaded = e.newStateFromRecord(record, now)
		if segment, err := e.findOpenSegment(record.Id); err == nil {
			loaded.segment = segment
		}
	}

	e.mu.Lock()
	st, ok := e.monitors[record.Id]
	if !ok {
		if loaded == nil {
			loaded = e.newStateFromRecord(record, now)
		}
		st = loaded
		e.monitors[st.id] = st
		e.reconcile(st, now.UnixMilli(), false)
	} else {
		wasEnabled, oldSystem := st.enabled, st.systemID
		st.applyConfig(record)
		if st.enabled && (!wasEnabled || oldSystem != st.systemID) {
			st.hold = StatusUnknown
			st.lastSeen = now
		}
		if !st.enabled || st.hold != "" {
			st.resetStreak()
		}
		e.reconcile(st, now.UnixMilli(), false)
	}
	// Restore status fields overwritten with stale values.
	var stored persistedState
	_ = record.UnmarshalJSONField("state", &stored)
	if record.GetString("status") != st.status || stored != st.saved {
		fields := map[string]any{"status": st.status, "state": st.saved}
		if !st.statusChanged.IsZero() {
			fields["statusChanged"] = st.statusChanged
		}
		e.queueUpdate(st, fields)
	}
	queued := len(e.queue) > 0
	e.mu.Unlock()
	if queued {
		e.tryDrain()
	}
}

// Remove forgets a deleted monitor. Its segments are deleted with the record.
func (e *Engine) Remove(monitorID string) {
	e.mu.Lock()
	delete(e.monitors, monitorID)
	e.mu.Unlock()
}

// Tick applies time-based changes: maintenance windows starting or ending and
// monitors without recent checks becoming unknown.
func (e *Engine) Tick(now time.Time) {
	e.mu.Lock()
	ids := make([]string, 0, len(e.monitors))
	for id, st := range e.monitors {
		if st.enabled {
			ids = append(ids, id)
		}
	}
	e.mu.Unlock()
	slices.Sort(ids)
	maintenance := e.maintenanceStates(ids, now)

	e.mu.Lock()
	nowMs := now.UnixMilli()
	var stale []string
	for _, id := range ids {
		st, ok := e.monitors[id]
		if !ok || !st.enabled {
			continue
		}
		if inMaint, ok := maintenance[id]; ok && inMaint != st.p.Maintenance {
			st.p.Maintenance = inMaint
			e.reconcile(st, nowMs, true)
		}
		// Push monitors have their own deadlines (push handling extends Tick).
		if st.protocol == monitor.ProtocolPush {
			continue
		}
		if st.hold == "" && !st.lastSeen.IsZero() && now.Sub(st.lastSeen) > st.staleAfter() {
			stale = append(stale, id)
		}
	}
	e.hold(stale, StatusUnknown, nowMs)
	e.mu.Unlock()
	e.drain()
}

// maintenanceStates evaluates the maintenance check for ids without locks held.
func (e *Engine) maintenanceStates(ids []string, now time.Time) map[string]bool {
	if e.inMaintenance == nil {
		return nil
	}
	states := make(map[string]bool, len(ids))
	for _, id := range ids {
		states[id] = e.inMaintenance(id, now)
	}
	return states
}

// Flush persists check results not yet written and refreshes uptime
// percentages older than five minutes.
func (e *Engine) Flush() {
	now := e.now()
	e.mu.Lock()
	var due []string
	for id, st := range e.monitors {
		if st.segment.id != "" && now.Sub(st.uptimeAt) >= uptimeRefresh {
			due = append(due, id)
		}
	}
	e.mu.Unlock()

	uptimes := make(map[string]Uptime, len(due))
	for _, id := range due {
		segments, err := e.loadSegments(id, now)
		if err != nil {
			e.app.Logger().Warn("Failed to load monitor events", "monitor", id, "err", err)
			continue
		}
		uptimes[id] = UptimeFromSegments(segments, now)
	}

	e.mu.Lock()
	for id, st := range e.monitors {
		fields := map[string]any{}
		if uptime, ok := uptimes[id]; ok {
			fields["uptime"] = uptime
			st.uptimeAt = now
		}
		if st.checksDirty {
			st.addCheckFields(fields)
		}
		if len(fields) > 0 {
			e.queue = append(e.queue, op{kind: opUpdate, monitorID: id, fields: fields})
		}
	}
	e.mu.Unlock()
	e.drain()
}

// Run calls Tick every 10 seconds and Flush every minute until stop is
// closed, then flushes once more. It blocks.
func (e *Engine) Run(stop <-chan struct{}) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	flush := time.NewTicker(time.Minute)
	defer flush.Stop()
	for {
		select {
		case <-stop:
			e.Flush()
			return
		case <-tick.C:
			e.Tick(e.now())
		case <-flush.C:
			e.Flush()
		}
	}
}

// Status returns the displayed status of a monitor, or "" if unknown to the engine.
func (e *Engine) Status(monitorID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if st, ok := e.monitors[monitorID]; ok {
		return st.status
	}
	return ""
}
