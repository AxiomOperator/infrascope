// Package systemevents records the status history of systems in the
// system_events collection: one row per period with one status (up, down,
// paused or pending), with start and end in Unix milliseconds and end 0 while
// the period is open. A system has at most one open row, whose status is the
// system's current status.
package systemevents

import (
	"slices"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

const (
	// Collection is the name of the system status history collection.
	Collection        = "system_events"
	systemsCollection = "systems"
)

// statuses are the recorded system statuses; other values are ignored.
var statuses = []string{"up", "down", "paused", "pending"}

// Segment is a system_events row.
type Segment struct {
	ID     string `db:"id"`
	System string `db:"system"`
	Status string `db:"status"`
	Start  int64  `db:"start"`
	End    int64  `db:"end"`
}

type openSegment struct {
	id, status string
	start      int64
}

// Recorder opens and closes system_events rows as system statuses change.
// It caches the open row of each system, so the frequent saves of systems
// whose status did not change cost no queries.
type Recorder struct {
	mu   sync.Mutex
	open map[string]openSegment
	now  func() time.Time
}

// New returns a recorder.
func New() *Recorder {
	return &Recorder{open: map[string]openSegment{}, now: time.Now}
}

// SetNow replaces the clock (for tests).
func (r *Recorder) SetNow(now func() time.Time) {
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}

// Bind records the status of systems on create and on every update, and
// forgets deleted systems (their rows are deleted by cascade).
func (r *Recorder) Bind(app core.App) {
	observe := func(e *core.RecordEvent) error {
		if err := r.Observe(e.App, e.Record.Id, e.Record.GetString("status")); err != nil {
			e.App.Logger().Error("Failed to record system status", "system", e.Record.Id, "err", err)
		}
		return e.Next()
	}
	app.OnRecordAfterCreateSuccess(systemsCollection).BindFunc(observe)
	app.OnRecordAfterUpdateSuccess(systemsCollection).BindFunc(observe)
	app.OnRecordAfterDeleteSuccess(systemsCollection).BindFunc(func(e *core.RecordEvent) error {
		r.mu.Lock()
		delete(r.open, e.Record.Id)
		r.mu.Unlock()
		return e.Next()
	})
}

// Observe records that systemID has status now. It closes the open row when
// its status differs and opens a row for the new status.
func (r *Recorder) Observe(app core.App, systemID, status string) error {
	if !slices.Contains(statuses, status) {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.open[systemID]
	if !ok {
		segments, err := openSegments(app, dbx.HashExp{"system": systemID})
		if err != nil {
			return err
		}
		if n := len(segments); n > 0 {
			// Close all but the latest open row (normally there is only one).
			if err := closeExtra(app, segments); err != nil {
				return err
			}
			last := segments[n-1]
			current = openSegment{id: last.ID, status: last.Status, start: last.Start}
			ok = true
		}
	}
	if ok && current.status == status {
		r.open[systemID] = current
		return nil
	}
	at := r.now().UnixMilli()
	if ok {
		at = max(at, current.start)
	}
	next, err := r.transition(app, systemID, current, ok, status, at)
	if err != nil {
		delete(r.open, systemID)
		return err
	}
	r.open[systemID] = next
	return nil
}

// transition closes current (when hasCurrent) at at and opens a row with status at at.
func (r *Recorder) transition(app core.App, systemID string, current openSegment, hasCurrent bool, status string, at int64) (openSegment, error) {
	next := openSegment{id: core.GenerateDefaultRandomId(), status: status, start: at}
	err := app.RunInTransaction(func(tx core.App) error {
		if hasCurrent {
			if _, err := tx.DB().Update(Collection, dbx.Params{"end": at}, dbx.HashExp{"id": current.id}).Execute(); err != nil {
				return err
			}
		}
		_, err := tx.DB().Insert(Collection, dbx.Params{
			"id": next.id, "system": systemID, "status": status, "start": at, "end": 0,
		}).Execute()
		return err
	})
	return next, err
}

// Reconcile makes the open rows match the current status of every system,
// for example after the hub was stopped. An open row whose status still
// matches is kept, so restarts do not split periods. A row whose status no
// longer matches is closed when the system record was last updated (the
// latest time its status can have changed), and systems without an open row
// get one starting now.
func (r *Recorder) Reconcile(app core.App) error {
	var systems []struct {
		ID      string `db:"id"`
		Status  string `db:"status"`
		Updated string `db:"updated"`
	}
	if err := app.DB().Select("id", "status", "updated").From(systemsCollection).All(&systems); err != nil {
		return err
	}
	segments, err := openSegments(app, nil)
	if err != nil {
		return err
	}
	bySystem := map[string][]Segment{}
	for _, segment := range segments {
		bySystem[segment.System] = append(bySystem[segment.System], segment)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UnixMilli()
	for _, system := range systems {
		open := bySystem[system.ID]
		if len(open) > 0 {
			if err := closeExtra(app, open); err != nil {
				return err
			}
		}
		if !slices.Contains(statuses, system.Status) {
			continue
		}
		if len(open) == 0 {
			next, err := r.transition(app, system.ID, openSegment{}, false, system.Status, now)
			if err != nil {
				return err
			}
			r.open[system.ID] = next
			continue
		}
		last := open[len(open)-1]
		current := openSegment{id: last.ID, status: last.Status, start: last.Start}
		if current.status == system.Status {
			r.open[system.ID] = current
			continue
		}
		at := now
		if updated, err := types.ParseDateTime(system.Updated); err == nil && !updated.IsZero() {
			at = min(at, updated.Time().UnixMilli())
		}
		next, err := r.transition(app, system.ID, current, true, system.Status, max(at, current.start))
		if err != nil {
			return err
		}
		r.open[system.ID] = next
	}
	return nil
}

// openSegments returns the open rows matching where, oldest first per system.
func openSegments(app core.App, where dbx.Expression) ([]Segment, error) {
	query := app.DB().Select("id", "system", "status", "start", "end").From(Collection).
		Where(dbx.HashExp{"end": 0}).OrderBy("system", "start", "id")
	if where != nil {
		query.AndWhere(where)
	}
	var segments []Segment
	err := query.All(&segments)
	return segments, err
}

// closeExtra closes every open row of one system but the last, each when the next starts.
func closeExtra(app core.App, segments []Segment) error {
	for i := 0; i < len(segments)-1; i++ {
		end := max(segments[i+1].Start, segments[i].Start)
		if _, err := app.DB().Update(Collection, dbx.Params{"end": end}, dbx.HashExp{"id": segments[i].ID}).Execute(); err != nil {
			return err
		}
	}
	return nil
}

// Load returns the rows of systemIDs that overlap [since, until), in start order.
func Load(app core.App, systemIDs []string, since, until int64) ([]Segment, error) {
	if len(systemIDs) == 0 {
		return nil, nil
	}
	ids := make([]any, len(systemIDs))
	for i, id := range systemIDs {
		ids[i] = id
	}
	var segments []Segment
	err := app.DB().Select("id", "system", "status", "start", "end").From(Collection).
		Where(dbx.In("system", ids...)).
		AndWhere(dbx.NewExp("([[end]] = 0 OR [[end]] > {:since}) AND [[start]] < {:until}", dbx.Params{"since": since, "until": until})).
		OrderBy("start").
		All(&segments)
	return segments, err
}
