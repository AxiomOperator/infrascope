package uptime

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/henrygd/beszel/internal/hub/monitorloc"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

const (
	monitorsCollection = "network_monitors"
	eventsCollection   = "monitor_events"
)

type opKind uint8

const (
	opUpdate opKind = iota // set fields of the monitor record
	opOpen                 // insert an open monitor_events row
	opClose                // set the end of a monitor_events row
)

// op is a queued write.
type op struct {
	kind       opKind
	monitorID  string
	segmentID  string
	status     string
	start, end int64
	err        string
	statusCode uint16
	fields     map[string]any
}

// drain writes queued operations in order and then notifies their
// transitions. It blocks while another drain is running.
func (e *Engine) drain() {
	e.writeMu.Lock()
	e.drainLocked()
}

// tryDrain drains unless a drain is already running, in which case that drain
// writes the queued operations before it returns. Hooks use it since they may
// run inside a drain (after the engine's own record saves).
func (e *Engine) tryDrain() {
	if e.writeMu.TryLock() {
		e.drainLocked()
	}
}

// drainLocked drains with writeMu held and releases it.
func (e *Engine) drainLocked() {
	var notices []Transition
	var deps []string
	for {
		e.mu.Lock()
		ops := e.queue
		notices = append(notices, e.notices...)
		deps = append(deps, e.depChanged...)
		e.queue, e.notices, e.depChanged = nil, nil, nil
		if len(ops) == 0 {
			// Drains are serialized by writeMu, so appending here keeps
			// persisted transitions in the order they occurred.
			e.ready = append(e.ready, notices...)
			// Release writeMu while holding mu, so an operation queued after
			// this check always finds writeMu free for its own drain.
			e.writeMu.Unlock()
			e.mu.Unlock()
			break
		}
		live := make(map[string]bool, len(ops))
		for _, o := range ops {
			_, live[o.monitorID] = e.monitors[o.monitorID]
		}
		e.mu.Unlock()
		e.write(ops, live)
	}
	e.deliver()
	if len(deps) > 0 && e.onDependency != nil {
		e.onDependency(slices.Compact(deps))
	}
}

// deliver passes persisted transitions to the notifier, in order and one
// call at a time. If another goroutine is delivering, it also delivers the
// transitions queued now, so a notifier that re-enters the engine (and drains)
// cannot deadlock.
func (e *Engine) deliver() {
	for e.notifyMu.TryLock() {
		e.mu.Lock()
		ready := e.ready
		e.ready = nil
		e.mu.Unlock()
		if len(ready) > 0 && e.notifier != nil {
			e.notifier(ready)
		}
		e.notifyMu.Unlock()
		// Transitions queued while notifyMu was held were left for this loop.
		e.mu.Lock()
		more := len(e.ready) > 0
		e.mu.Unlock()
		if !more {
			return
		}
	}
}

// write persists ops in one transaction. Operations of removed monitors are
// skipped. Failures are logged, since the in-memory state stays authoritative
// and later updates rewrite status and state.
func (e *Engine) write(ops []op, live map[string]bool) {
	// Merge record updates so each monitor record is saved once, after its segments.
	updates := map[string]map[string]any{}
	var order []string
	for _, o := range ops {
		if o.kind != opUpdate || !live[o.monitorID] {
			continue
		}
		fields, ok := updates[o.monitorID]
		if !ok {
			fields = map[string]any{}
			updates[o.monitorID] = fields
			order = append(order, o.monitorID)
		}
		for key, value := range o.fields {
			fields[key] = value
		}
	}
	err := e.app.RunInTransaction(func(txApp core.App) error {
		var events *core.Collection
		for _, o := range ops {
			if !live[o.monitorID] {
				continue
			}
			var err error
			switch o.kind {
			case opOpen:
				if events == nil {
					if events, err = txApp.FindCachedCollectionByNameOrId(eventsCollection); err != nil {
						return err
					}
				}
				record := core.NewRecord(events)
				record.Id = o.segmentID
				record.Set("monitor", o.monitorID)
				record.Set("status", o.status)
				record.Set("start", o.start)
				record.Set("end", 0)
				record.Set("error", o.err)
				record.Set("statusCode", o.statusCode)
				err = txApp.SaveNoValidate(record)
			case opClose:
				var record *core.Record
				record, err = txApp.FindRecordById(eventsCollection, o.segmentID)
				if err == nil {
					record.Set("end", o.end)
					err = txApp.SaveNoValidate(record)
				}
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				txApp.Logger().Warn("Failed to write monitor event", "monitor", o.monitorID, "err", err)
			}
		}
		for _, id := range order {
			record, err := txApp.FindRecordById(monitorsCollection, id)
			if err != nil {
				if !errors.Is(err, sql.ErrNoRows) {
					txApp.Logger().Warn("Failed to load monitor", "monitor", id, "err", err)
				}
				continue
			}
			record.Load(updates[id])
			if err := txApp.SaveNoValidate(record); err != nil {
				txApp.Logger().Warn("Failed to update monitor status", "monitor", id, "err", err)
			}
		}
		return nil
	})
	if err != nil {
		e.app.Logger().Error("Failed to persist monitor status", "err", err)
	}
}

// Load reads all monitors, their state and open segments. It replaces any
// state loaded before and emits no notifications.
func (e *Engine) Load() error {
	records, err := e.app.FindAllRecords(monitorsCollection)
	if err != nil {
		return err
	}
	var rows []struct {
		ID      string `db:"id"`
		Monitor string `db:"monitor"`
		Status  string `db:"status"`
		Start   int64  `db:"start"`
	}
	err = e.app.DB().Select("id", "monitor", "status", "start").From(eventsCollection).
		Where(dbx.HashExp{"end": 0}).OrderBy("start ASC").All(&rows)
	if err != nil {
		return err
	}
	// Keep the latest open segment per monitor.
	segments := make(map[string]openSegment, len(rows))
	for _, row := range rows {
		segments[row.Monitor] = openSegment{id: row.ID, status: row.Status, start: row.Start}
	}
	names := make(map[string]map[string]string)
	for _, record := range records {
		if locations := monitorloc.Of(record); len(locations) > 1 {
			names[record.Id] = e.locationNames(locations)
		}
	}

	now := e.now()
	e.mu.Lock()
	e.monitors = make(map[string]*monitorState, len(records))
	e.dependents = map[string]map[string]struct{}{}
	for _, record := range records {
		st := e.newStateFromRecord(record, now, names[record.Id])
		st.segment = segments[record.Id]
		e.monitors[st.id] = st
		e.link(st.id, nil, st.dependsOn)
	}
	e.loading = true
	for _, record := range records {
		// Repair records whose status does not match their configuration.
		e.reconcile(e.monitors[record.Id], now.UnixMilli(), false)
	}
	e.loading = false
	e.mu.Unlock()
	e.drain()
	return nil
}

// newStateFromRecord builds monitor state from a stored record. names are
// the display names of its locations.
func (e *Engine) newStateFromRecord(record *core.Record, now time.Time, names map[string]string) *monitorState {
	st := &monitorState{id: record.Id, names: names}
	st.applyConfig(record)
	_ = record.UnmarshalJSONField("state", &st.p)
	st.status = record.GetString("status")
	st.suppressedBy = record.GetString("suppressedBy")
	st.statusChanged = record.GetDateTime("statusChanged").Time()
	hold := ""
	switch st.status {
	case StatusUnknown, StatusPaused:
		if st.enabled {
			hold = st.status
		}
	}
	var stored map[string]LocationStatus
	_ = record.UnmarshalJSONField("locationStatus", &stored)
	st.locs = make(map[string]*locState, len(st.locations))
	for _, location := range st.locations {
		ls := &locState{hold: hold, lastSeen: now}
		if st.multi() {
			ls.p = st.p.Locations[location]
		} else {
			ls.p = locPersisted{
				FailStreak: st.p.FailStreak, PendingSince: st.p.PendingSince, PendingError: st.p.PendingError,
				PendingStatusCode: st.p.PendingStatusCode, Confirmed: st.p.Confirmed, DownSince: st.p.DownSince,
			}
		}
		if status, ok := stored[location]; ok {
			ls.lastCheck, ls.lastError, ls.lastStatusCode, ls.res = status.LastCheck, status.LastError, status.LastStatusCode, status.Res
		}
		st.locs[location] = ls
	}
	if !st.multi() {
		st.p.Locations = nil
	}
	// A held multi-location monitor keeps its stored state until its
	// locations report.
	if hold == "" || !st.multi() {
		st.aggregate()
	} else {
		st.hold = hold
	}
	st.saved = st.p
	st.locKey = locationKeyOf(st.locations, stored)
	st.lastCheck = int64(record.GetFloat("lastCheck"))
	st.lastError = record.GetString("lastError")
	st.lastStatusCode = uint16(record.GetInt("lastStatusCode"))
	_ = record.UnmarshalJSONField("recent", &st.recent)
	if extra := len(st.recent) - recentSize; extra > 0 {
		st.recent = st.recent[extra:]
	}
	return st
}

// locationKeyOf is locationKey of stored location statuses. Locations
// without a stored status are unknown.
func locationKeyOf(locations []string, stored map[string]LocationStatus) string {
	var b strings.Builder
	for _, location := range locations {
		status := stored[location].Status
		if status == "" {
			status = StatusUnknown
		}
		b.WriteString(location)
		b.WriteByte('=')
		b.WriteString(status)
		b.WriteByte(';')
	}
	return b.String()
}

// applyConfig copies the configuration fields of a record.
func (st *monitorState) applyConfig(record *core.Record) {
	st.systemID = record.GetString("system")
	st.name = record.GetString("name")
	st.target = record.GetString("target")
	st.protocol = record.GetString("protocol")
	st.enabled = record.GetBool("enabled")
	st.notify = record.GetBool("notify")
	st.retries = max(record.GetInt("retries"), 0)
	st.interval = time.Duration(record.GetInt("interval")) * time.Second
	st.locations = monitorloc.Of(record)
	st.quorum = monitorloc.Quorum(record)
	st.dependsOn = record.GetStringSlice("dependsOn")
}

// findOpenSegment returns the latest open segment of a monitor.
func (e *Engine) findOpenSegment(monitorID string) (openSegment, error) {
	var row struct {
		ID     string `db:"id"`
		Status string `db:"status"`
		Start  int64  `db:"start"`
	}
	err := e.app.DB().Select("id", "status", "start").From(eventsCollection).
		Where(dbx.HashExp{"monitor": monitorID, "end": 0}).
		OrderBy("start DESC").Limit(1).One(&row)
	if err != nil {
		return openSegment{}, err
	}
	return openSegment{id: row.ID, status: row.Status, start: row.Start}, nil
}

// loadSegments returns the segments of a monitor that overlap the longest uptime window.
func (e *Engine) loadSegments(monitorID string, now time.Time) ([]Segment, error) {
	from := now.Add(-uptimeWindows[len(uptimeWindows)-1]).UnixMilli()
	var segments []Segment
	err := e.app.DB().NewQuery(
		"SELECT status, start, end FROM monitor_events WHERE monitor = {:monitor} AND (end = 0 OR end > {:from}) AND start < {:now}",
	).Bind(dbx.Params{"monitor": monitorID, "from": from, "now": now.UnixMilli()}).All(&segments)
	return segments, err
}
