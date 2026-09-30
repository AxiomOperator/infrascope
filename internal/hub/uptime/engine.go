package uptime

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/monitorloc"
	"github.com/pocketbase/dbx"
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
// Locations
//
// A monitor is checked from one or more locations (the hub or agents, see
// monitorloc). The state machine above runs per location: each location has
// its own failure streak, confirmed status and unknown/paused hold (its
// agent disconnected, went stale or was paused). The monitor's state is
// derived from its locations:
//
//   - With a single location, it is that location's state, exactly as above.
//   - Locations held unknown or paused, or without any check yet, are
//     excluded. With fewer known locations than the quorum, the monitor is
//     held unknown (paused when every location is paused) and keeps its
//     confirmed status, like any hold.
//   - It is confirmed down when at least quorum locations are confirmed down,
//     and confirmed up otherwise (once any known location confirmed a status).
//   - It is pending while any known location fails but it is not confirmed
//     down. Its error then names the failing locations ("down from 1 of 3
//     locations: web1: timeout").
//
// Status changes caused by a location becoming unknown or paused never
// notify, like other holds. Every location's checks are recorded in recent.
// The status of each location is written to network_monitors.locationStatus.
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
// Dependencies
//
// A monitor can depend on up to five parent monitors (network_monitors.
// dependsOn, for example the router in front of it). A parent counts as down
// only while its displayed status is "down" (not pending, unknown, paused or
// maintenance). While any parent is down, the monitor's notifications are
// suppressed like during maintenance: notified is not updated, so a change
// that still holds when the last parent recovers is notified once then, and
// a down period that ended while suppressed is not notified at all. The
// monitor's displayed status, segments and uptime are unaffected (downtime
// behind a down parent still counts), and the names of its down parents are
// written to network_monitors.suppressedBy. The engine keeps a parent ->
// children index, so a parent status change re-evaluates its children, and
// reports the parents whose down state changed to WithDependencyListener,
// for systems that depend on monitors.
//
// Persistence
//
// Status changes, segments and the state JSON are queued while the engine
// lock is held and written in order, in one transaction per drain, without
// holding the lock. Check results (lastCheck, lastError, lastStatusCode,
// recent) and uptime are written by Flush. Transitions are passed to the
// notifier after the drain that persists them, without engine locks held,
// serially and in order.

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
	// ready holds persisted transitions awaiting delivery, oldest first.
	ready []Transition
	// dependents maps a monitor to the monitors that depend on it.
	dependents map[string]map[string]struct{}
	// depChanged holds monitors whose down state changed, for onDependency.
	depChanged []string
	// onDependency receives monitors whose down state changed.
	onDependency func([]string)
	// loading is set while Load reconciles, which never notifies.
	loading bool

	// notifyMu serializes notifier calls, so transitions are delivered in order.
	notifyMu sync.Mutex

	// writeMu serializes drains so queued writes are persisted in order.
	writeMu sync.Mutex
}

// Option configures an Engine.
type Option func(*Engine)

// WithNotifier sets the function that receives confirmed status changes. It
// is called after the changes are persisted, without engine locks held, one
// call at a time and in the order the changes occurred. It should return
// quickly, since it delays delivery of later changes.
func WithNotifier(fn func([]Transition)) Option {
	return func(e *Engine) { e.notifier = fn }
}

// WithMaintenanceCheck sets the function that reports whether a monitor is in
// a maintenance window. It is called without engine locks held.
func WithMaintenanceCheck(fn func(monitorID string, now time.Time) bool) Option {
	return func(e *Engine) { e.inMaintenance = fn }
}

// WithDependencyListener sets the function that receives the monitors whose
// down state (displayed status "down" or not) changed, after the change is
// persisted and without engine locks held. It should return quickly.
func WithDependencyListener(fn func(monitorIDs []string)) Option {
	return func(e *Engine) { e.onDependency = fn }
}

// WithNow overrides the clock, for tests.
func WithNow(fn func() time.Time) Option {
	return func(e *Engine) { e.now = fn }
}

// New creates an engine. Call Load before feeding it results.
func New(app core.App, opts ...Option) *Engine {
	e := &Engine{app: app, now: time.Now, monitors: map[string]*monitorState{}, dependents: map[string]map[string]struct{}{}}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// monitorState is the in-memory state of one monitor. Guarded by Engine.mu.
type monitorState struct {
	// systemID is the primary location's system ("" for the hub only).
	id, systemID, name, target, protocol string
	enabled, notify                      bool
	retries                              int
	interval                             time.Duration
	// locations are the monitor's locations, in order, and quorum how many
	// of them must confirm it down.
	locations []string
	quorum    int
	// locs holds the state of each location.
	locs map[string]*locState
	// names are the display names of the locations, for errors.
	names map[string]string
	// dependsOn are the monitor's parent monitors.
	dependsOn []string
	// suppressedBy names the parents that are down, as last queued for persistence.
	suppressedBy string

	// p is the monitor's state, derived from its locations by aggregate.
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
	// locKey is the location statuses last queued for persistence.
	locKey   string
	uptimeAt time.Time
}

// locState is the state of one location of a monitor. Guarded by Engine.mu.
type locState struct {
	p locPersisted
	// hold is "unknown" or "paused" while the location's results are unavailable.
	hold string
	// lastSeen is when the hub last received a check (or loaded the monitor).
	lastSeen       time.Time
	lastCheck      int64
	lastError      string
	lastStatusCode uint16
	// res is the response time of the last check in microseconds, -1 when it failed.
	res int64
}

// resetStreak forgets the location's failure streak.
func (ls *locState) resetStreak() {
	ls.p.FailStreak = 0
	ls.p.PendingSince = 0
	ls.p.PendingError = ""
	ls.p.PendingStatusCode = 0
}

// known reports whether the location counts towards the quorum: it is not
// held and has a status.
func (ls *locState) known() bool {
	return ls.hold == "" && (ls.p.Confirmed != "" || ls.p.FailStreak > 0)
}

// status returns the displayed status of the location.
func (ls *locState) status(enabled bool) string {
	switch {
	case !enabled:
		return StatusPaused
	case ls.hold != "":
		return ls.hold
	case ls.p.FailStreak > 0 && ls.p.Confirmed != StatusDown:
		return StatusPending
	case ls.p.Confirmed == "":
		return StatusUnknown
	}
	return ls.p.Confirmed
}

// multi reports whether the monitor has more than one location.
func (st *monitorState) multi() bool { return len(st.locations) > 1 }

// locationName returns the display name of a location.
func (st *monitorState) locationName(location string) string {
	if name := st.names[location]; name != "" {
		return name
	}
	if location == monitorloc.Hub {
		return "Hub"
	}
	return location
}

// failingMessage describes the failing locations of a multi-location
// monitor, with each location's first (pending) or last error.
func (st *monitorState) failingMessage(failing []string, last bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "down from %d of %d locations: ", len(failing), len(st.locations))
	for i, location := range failing {
		ls := st.locs[location]
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(st.locationName(location))
		err := ls.p.PendingError
		if last {
			err = ls.lastError
		}
		if err != "" {
			b.WriteString(": ")
			b.WriteString(err)
		}
	}
	return truncateError(b.String())
}

// aggregate derives the monitor's hold and state from its locations (see
// Locations above). Requires e.mu.
func (st *monitorState) aggregate() {
	if !st.multi() {
		ls := st.locs[st.locations[0]]
		st.hold = ls.hold
		st.p.FailStreak = ls.p.FailStreak
		st.p.PendingSince = ls.p.PendingSince
		st.p.PendingError = ls.p.PendingError
		st.p.PendingStatusCode = ls.p.PendingStatusCode
		st.p.Confirmed = ls.p.Confirmed
		st.p.DownSince = ls.p.DownSince
		st.p.Locations = nil
		return
	}
	// A new map each time, so queued state is never modified.
	st.p.Locations = make(map[string]locPersisted, len(st.locations))
	known, down := 0, 0
	allPaused, anyConfirmed := true, false
	var failing []string
	var downSince int64
	for _, location := range st.locations {
		ls := st.locs[location]
		st.p.Locations[location] = ls.p
		if ls.hold != StatusPaused {
			allPaused = false
		}
		if !ls.known() {
			continue
		}
		known++
		anyConfirmed = anyConfirmed || ls.p.Confirmed != ""
		if ls.p.FailStreak > 0 {
			failing = append(failing, location)
		}
		if ls.p.Confirmed == StatusDown {
			down++
			if downSince == 0 || (ls.p.PendingSince > 0 && ls.p.PendingSince < downSince) {
				downSince = ls.p.PendingSince
			}
		}
	}
	st.p.FailStreak, st.p.PendingSince, st.p.PendingError, st.p.PendingStatusCode = 0, 0, "", 0
	if known < st.quorum {
		st.hold = StatusUnknown
		if allPaused {
			st.hold = StatusPaused
		}
		return
	}
	st.hold = ""
	if len(failing) > 0 {
		st.p.FailStreak = len(failing)
		for _, location := range failing {
			ls := st.locs[location]
			if st.p.PendingSince == 0 || (ls.p.PendingSince > 0 && ls.p.PendingSince < st.p.PendingSince) {
				st.p.PendingSince = ls.p.PendingSince
			}
		}
		st.p.PendingError = st.failingMessage(failing, false)
		st.p.PendingStatusCode = st.locs[failing[0]].p.PendingStatusCode
	}
	switch {
	case down >= st.quorum:
		if st.p.Confirmed != StatusDown {
			st.p.DownSince = downSince
		}
		st.p.Confirmed = StatusDown
	case anyConfirmed:
		st.p.Confirmed = StatusUp
	}
}

// failingLocations returns the known locations that are failing, in order.
func (st *monitorState) failingLocations() []string {
	var failing []string
	for _, location := range st.locations {
		if ls := st.locs[location]; ls.known() && ls.p.FailStreak > 0 {
			failing = append(failing, location)
		}
	}
	return failing
}

// LocationStatus is the status of one location of a monitor, stored in
// network_monitors.locationStatus keyed by location.
type LocationStatus struct {
	Status string `json:"status"`
	// LastCheck is the latest check of the location in Unix milliseconds.
	LastCheck      int64  `json:"lastCheck,omitempty"`
	LastError      string `json:"lastError,omitempty"`
	LastStatusCode uint16 `json:"lastStatusCode,omitempty"`
	// Res is the response time of the latest check in microseconds, -1 when it failed.
	Res int64 `json:"res,omitempty"`
}

// locationStatus returns the status of every location.
func (st *monitorState) locationStatus() map[string]LocationStatus {
	statuses := make(map[string]LocationStatus, len(st.locations))
	for _, location := range st.locations {
		ls := st.locs[location]
		statuses[location] = LocationStatus{
			Status:         ls.status(st.enabled),
			LastCheck:      ls.lastCheck,
			LastError:      ls.lastError,
			LastStatusCode: ls.lastStatusCode,
			Res:            ls.res,
		}
	}
	return statuses
}

// locationKey identifies the locations' statuses, to detect changes.
func (st *monitorState) locationKey() string {
	var b strings.Builder
	for _, location := range st.locations {
		b.WriteString(location)
		b.WriteByte('=')
		b.WriteString(st.locs[location].status(st.enabled))
		b.WriteByte(';')
	}
	return b.String()
}

// syncLocations adds state for new locations, held unknown until their first
// check, and drops removed ones. A single-location monitor moved to another
// location keeps its confirmed status. Requires e.mu.
func (st *monitorState) syncLocations(now time.Time) {
	if st.locs == nil {
		st.locs = map[string]*locState{}
	}
	var moved *locState
	if len(st.locs) == 1 && len(st.locations) == 1 {
		for location, ls := range st.locs {
			if location != st.locations[0] {
				moved = ls
				delete(st.locs, location)
			}
		}
	}
	for location := range st.locs {
		if !slices.Contains(st.locations, location) {
			delete(st.locs, location)
		}
	}
	for _, location := range st.locations {
		if _, ok := st.locs[location]; ok {
			continue
		}
		ls := &locState{hold: StatusUnknown, lastSeen: now}
		if moved != nil {
			ls.p = moved.p
			ls.resetStreak()
		}
		st.locs[location] = ls
	}
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

// staleAfter is how long a monitor may go without checks before it is unknown.
func (st *monitorState) staleAfter() time.Duration {
	return 2*max(st.interval, agentFetchInterval) + 2*agentFetchInterval
}

// Observe applies a monitor's checks, oldest first, to its hub location, or
// to its only location when the hub is not one of its locations.
func (e *Engine) Observe(monitorID string, events []monitor.CheckEvent) {
	if len(events) == 0 {
		return
	}
	e.observe("", map[string][]monitor.CheckEvent{monitorID: events})
}

// ObserveResults applies the checks of an agent's (or, for an empty
// systemID, the hub's) default-interval results to that location. Results
// for monitors without that location are ignored. legacy agents do not
// report individual checks, so one check is synthesised from each result's
// window (see ChecksFromResult).
func (e *Engine) ObserveResults(systemID string, results map[string]monitor.Result, legacy bool) {
	checks := make(map[string][]monitor.CheckEvent, len(results))
	for id, result := range results {
		if events := ChecksFromResult(result, legacy); len(events) > 0 {
			checks[id] = events
		}
	}
	if len(checks) > 0 {
		e.observe(monitorloc.FromSystemID(systemID), checks)
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

// observe applies checks from location, or from each monitor's default
// location (see Observe) when location is empty.
func (e *Engine) observe(location string, checks map[string][]monitor.CheckEvent) {
	now := e.now()
	maintenance := e.maintenanceStates(slices.Collect(maps.Keys(checks)), now)

	e.mu.Lock()
	for id, events := range checks {
		st, ok := e.monitors[id]
		if !ok || !st.enabled {
			continue
		}
		loc := location
		if loc == "" {
			switch {
			case slices.Contains(st.locations, monitorloc.Hub):
				loc = monitorloc.Hub
			case len(st.locations) == 1:
				loc = st.locations[0]
			}
		}
		ls, ok := st.locs[loc]
		if !ok {
			continue
		}
		if inMaint, ok := maintenance[id]; ok {
			st.p.Maintenance = inMaint
		}
		for _, event := range events {
			e.applyCheck(st, loc, event, now)
		}
		ls.lastSeen = now
	}
	e.mu.Unlock()
	e.drain()
}

// applyCheck applies one check of a location to the state machine. Requires e.mu.
func (e *Engine) applyCheck(st *monitorState, location string, event monitor.CheckEvent, now time.Time) {
	ls := st.locs[location]
	// Agents without individual checks repeat their latest probe time until the next probe.
	if event.At != 0 && event.At == ls.lastCheck {
		return
	}
	at := event.At
	if at <= 0 || at > now.UnixMilli() {
		at = now.UnixMilli()
	}
	at = max(at, st.segment.start)

	// A check ends an unknown or system-paused hold.
	ls.hold = ""
	failed := event.Failed()
	if failed {
		ls.p.FailStreak++
		if ls.p.FailStreak == 1 {
			ls.p.PendingSince = at
			ls.p.PendingError = truncateError(event.Err)
			ls.p.PendingStatusCode = event.StatusCode
		}
		if ls.p.Confirmed != StatusDown && ls.p.FailStreak > st.retries {
			ls.p.Confirmed = StatusDown
			ls.p.DownSince = ls.p.PendingSince
		}
		ls.lastError = truncateError(event.Err)
		ls.res = -1
	} else {
		ls.resetStreak()
		ls.p.Confirmed = StatusUp
		ls.lastError = ""
		ls.res = max(event.ResponseUs, 0)
	}
	ls.lastCheck = event.At
	if ls.lastCheck <= 0 {
		ls.lastCheck = at
	}
	ls.lastStatusCode = event.StatusCode
	st.aggregate()

	check := RecentCheck{At: at / 1000, State: RecentUp}
	if failed {
		check.State = RecentPending
		if st.p.Confirmed == StatusDown {
			check.State = RecentDown
		}
		check.ResponseMs = -1
	} else {
		check.ResponseMs = float64(event.ResponseUs/10) / 100
	}
	st.lastCheck = ls.lastCheck
	st.lastError = ls.lastError
	if st.multi() {
		st.lastError = ""
		if failing := st.failingLocations(); len(failing) > 0 {
			st.lastError = st.failingMessage(failing, true)
		}
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
	wasDown := st.status == StatusDown
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

	suppressedBy := e.suppressionOf(st)
	suppressionChanged := suppressedBy != st.suppressedBy
	st.suppressedBy = suppressedBy

	if transition := e.evaluateNotify(st, at); transition != nil && allowNotify {
		e.notices = append(e.notices, *transition)
	}

	// A location status change writes the check fields, which include them.
	locChanged := false
	if key := st.locationKey(); key != st.locKey {
		st.locKey = key
		st.checksDirty = true
		locChanged = true
	}
	if status != st.status || !st.p.equal(st.saved) || locChanged || suppressionChanged {
		fields := map[string]any{}
		if suppressionChanged {
			fields["suppressedBy"] = suppressedBy
		}
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
	if (status == StatusDown) != wasDown {
		e.dependencyChanged(st.id, at)
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
	fields["locationStatus"] = st.locationStatus()
	st.checksDirty = false
}

// evaluateNotify updates the notified status when the confirmed status
// changed and returns the transition to report, if any. Requires e.mu.
func (e *Engine) evaluateNotify(st *monitorState, at int64) *Transition {
	if !st.enabled || st.hold != "" || st.p.Maintenance || st.suppressedBy != "" {
		return nil
	}
	confirmed := st.p.Confirmed
	if confirmed == "" || confirmed == st.p.Notified {
		return nil
	}
	prev := st.p.Notified
	st.p.Notified = confirmed
	if prev == "" && confirmed == StatusUp {
		return nil
	}
	transition := &Transition{
		Notify:    st.notify,
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

// MarkUnknown sets all locations of monitors to unknown, for example when
// their agent stops reporting. It never notifies.
func (e *Engine) MarkUnknown(monitorIDs []string) {
	e.MarkLocationUnknown("", monitorIDs)
}

// MarkLocationUnknown sets one location of monitors to unknown (all their
// locations when location is empty), for example when an agent cannot run
// them. It never notifies.
func (e *Engine) MarkLocationUnknown(location string, monitorIDs []string) {
	e.mu.Lock()
	queued := e.hold(monitorIDs, location, StatusUnknown, e.now().UnixMilli())
	e.mu.Unlock()
	if queued {
		e.tryDrain()
	}
}

// hold holds location (every location when empty) of enabled monitors.
// Requires e.mu.
func (e *Engine) hold(monitorIDs []string, location, status string, at int64) bool {
	before := len(e.queue)
	for _, id := range monitorIDs {
		st, ok := e.monitors[id]
		if !ok || !st.enabled {
			continue
		}
		changed := false
		for _, loc := range st.locations {
			ls := st.locs[loc]
			if (location != "" && loc != location) || ls.hold == status {
				continue
			}
			ls.hold = status
			ls.resetStreak()
			changed = true
		}
		if changed {
			st.aggregate()
			e.reconcile(st, at, false)
		}
	}
	return len(e.queue) > before
}

// SystemStatusChanged updates the monitors checked from a system after its
// status changed: "paused" pauses that location, other statuses except "up"
// make it unknown. "up" turns locations paused with the system into unknown
// until their next check.
func (e *Engine) SystemStatusChanged(systemID, status string) {
	if systemID == "" {
		return
	}
	e.mu.Lock()
	var ids []string
	for id, st := range e.monitors {
		ls, ok := st.locs[systemID]
		if ok && (status != StatusUp || ls.hold == StatusPaused) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	hold := StatusUnknown
	if status == StatusPaused {
		hold = StatusPaused
	}
	queued := e.hold(ids, systemID, hold, e.now().UnixMilli())
	e.mu.Unlock()
	if queued {
		e.tryDrain()
	}
}

// Upsert adds or reconfigures a monitor from its record after it was created
// or updated. Disabled monitors are paused; re-enabled monitors and added
// locations are unknown until their next check. It restores the record's
// status fields if they differ from the engine's (for example after a
// concurrent edit).
func (e *Engine) Upsert(record *core.Record) {
	now := e.now()
	var loaded *monitorState
	var names map[string]string
	if len(monitorloc.Of(record)) > 1 {
		names = e.locationNames(monitorloc.Of(record))
	}
	e.mu.Lock()
	_, exists := e.monitors[record.Id]
	e.mu.Unlock()
	if !exists {
		// Read outside the lock; the open segment is only present for monitors
		// the engine missed (for example created before Load).
		loaded = e.newStateFromRecord(record, now, names)
		if segment, err := e.findOpenSegment(record.Id); err == nil {
			loaded.segment = segment
		}
	}

	e.mu.Lock()
	st, ok := e.monitors[record.Id]
	if !ok {
		if loaded == nil {
			loaded = e.newStateFromRecord(record, now, names)
		}
		st = loaded
		e.monitors[st.id] = st
		e.link(st.id, nil, st.dependsOn)
		e.reconcile(st, now.UnixMilli(), false)
	} else {
		wasEnabled := st.enabled
		oldDeps, oldName, prevSuppressed := st.dependsOn, st.displayName(), st.suppressedBy
		st.applyConfig(record)
		e.link(st.id, oldDeps, st.dependsOn)
		st.names = names
		st.syncLocations(now)
		for _, ls := range st.locs {
			if st.enabled && !wasEnabled {
				ls.hold = StatusUnknown
				ls.lastSeen = now
			}
			if !st.enabled || ls.hold != "" {
				ls.resetStreak()
			}
		}
		st.aggregate()
		// A change removing the down parents notifies what they suppressed.
		e.reconcile(st, now.UnixMilli(), prevSuppressed != "")
		if st.status == StatusDown && st.displayName() != oldName {
			// Children name their down parents.
			e.dependencyChanged(st.id, now.UnixMilli())
		}
	}
	// Restore status fields overwritten with stale values.
	var stored persistedState
	_ = record.UnmarshalJSONField("state", &stored)
	if record.GetString("status") != st.status || !stored.equal(st.saved) || record.GetString("suppressedBy") != st.suppressedBy {
		fields := map[string]any{"status": st.status, "state": st.saved, "suppressedBy": st.suppressedBy}
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

// locationNames returns the display names of locations: "Hub" and the names
// of the systems. It reads the database, so call it without e.mu held.
func (e *Engine) locationNames(locations []string) map[string]string {
	names := make(map[string]string, len(locations))
	for _, location := range locations {
		if location == monitorloc.Hub {
			names[location] = "Hub"
			continue
		}
		var name string
		if err := e.app.DB().Select("name").From("systems").Where(dbx.HashExp{"id": location}).Row(&name); err == nil && name != "" {
			names[location] = name
		}
	}
	return names
}

// Remove forgets a deleted monitor. Its segments are deleted with the record.
// Monitors depending on it no longer do.
func (e *Engine) Remove(monitorID string) {
	e.mu.Lock()
	st, ok := e.monitors[monitorID]
	if ok {
		e.link(monitorID, st.dependsOn, nil)
		delete(e.monitors, monitorID)
		if st.status == StatusDown {
			e.dependencyChanged(monitorID, e.now().UnixMilli())
		}
	}
	queued := len(e.queue) > 0
	e.mu.Unlock()
	if queued {
		e.tryDrain()
	}
}

// link updates the dependency index for a monitor whose parents changed
// from old to deps. Requires e.mu.
func (e *Engine) link(monitorID string, old, deps []string) {
	for _, parent := range old {
		if children := e.dependents[parent]; children != nil {
			delete(children, monitorID)
			if len(children) == 0 {
				delete(e.dependents, parent)
			}
		}
	}
	for _, parent := range deps {
		if parent == monitorID {
			continue
		}
		children := e.dependents[parent]
		if children == nil {
			children = map[string]struct{}{}
			e.dependents[parent] = children
		}
		children[monitorID] = struct{}{}
	}
}

// downParents returns the names of the monitors among ids whose displayed
// status is down, in order. Requires e.mu.
func (e *Engine) downParents(ids []string, self string) []string {
	var names []string
	for _, id := range ids {
		if parent, ok := e.monitors[id]; ok && id != self && parent.status == StatusDown {
			names = append(names, parent.displayName())
		}
	}
	return names
}

// suppressionOf returns the suppressedBy value of an enabled monitor: its
// down parents' names, comma separated. Requires e.mu.
func (e *Engine) suppressionOf(st *monitorState) string {
	if !st.enabled {
		return ""
	}
	return JoinSuppressedBy(e.downParents(st.dependsOn, st.id))
}

// JoinSuppressedBy formats parent names for a suppressedBy field.
func JoinSuppressedBy(names []string) string {
	return truncateText(strings.Join(names, ", "), maxSuppressedByLength)
}

// dependencyChanged re-evaluates the children of a monitor whose down state
// (or name while down) changed and queues it for the dependency listener.
// Requires e.mu.
func (e *Engine) dependencyChanged(monitorID string, at int64) {
	if e.onDependency != nil {
		e.depChanged = append(e.depChanged, monitorID)
	}
	children := slices.Sorted(maps.Keys(e.dependents[monitorID]))
	for _, id := range children {
		if child, ok := e.monitors[id]; ok {
			// A child's status does not depend on its parents, so this never
			// cascades further (and terminates even with cycles).
			e.reconcile(child, at, !e.loading)
		}
	}
}

// Suppressed reports whether a monitor's notifications are suppressed
// because one of its parents is down.
func (e *Engine) Suppressed(monitorID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.monitors[monitorID]
	return ok && st.suppressedBy != ""
}

// DownDependencies returns the names of the monitors among ids that are
// down, in order, for records other than monitors that depend on them.
func (e *Engine) DownDependencies(ids []string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.downParents(ids, "")
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
		// Push monitors never go stale: the hub monitor runner records a
		// failed check for each missed push deadline instead.
		if st.protocol == monitor.ProtocolPush {
			continue
		}
		changed := false
		for _, ls := range st.locs {
			if ls.hold == "" && !ls.lastSeen.IsZero() && now.Sub(ls.lastSeen) > st.staleAfter() {
				ls.hold = StatusUnknown
				ls.resetStreak()
				changed = true
			}
		}
		if changed {
			stale = append(stale, id)
		}
	}
	for _, id := range stale {
		st := e.monitors[id]
		st.aggregate()
		e.reconcile(st, nowMs, false)
	}
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
