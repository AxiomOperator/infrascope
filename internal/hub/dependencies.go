package hub

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// Dependency-aware alerts
//
// Monitors and systems can depend on up to maxDependencies monitors
// (dependsOn). While any of them is down, the record's notifications are
// suppressed and suppressedBy names the down parents (see the uptime
// engine's Dependencies section for monitors). For systems:
//
//   - Status down alerts are dropped while a parent is down, and sent once
//     when the parents recover if the system is still down. Up alerts follow
//     a down alert that was sent.
//   - suppressedBy is recomputed on every save of a system and whenever a
//     parent's down state changes (refreshSystemDependencies).
//
// Uptime and status history are unchanged: downtime behind a down parent
// still counts. Status pages show the records' own statuses.
const (
	maxDependencies = 5
	// maxDependencyDepth limits the chains walked for cycle detection.
	maxDependencyDepth = 10
)

// bindDependencyEvents validates the dependencies of systems and keeps their
// suppressedBy field current. Monitors are validated by prepareMonitor.
func bindDependencyEvents(hub *Hub) {
	validate := func(e *core.RecordRequestEvent) error {
		if err := prepareDependencies(e.App, e.Record, e.Record.Original(), e.Auth, e.HasSuperuserAuth(), false); err != nil {
			return e.BadRequestError(err.Error(), nil)
		}
		return e.Next()
	}
	hub.OnRecordCreateRequest("systems").BindFunc(validate)
	hub.OnRecordUpdateRequest("systems").BindFunc(validate)

	// Model-level, so every save (including status updates and client
	// writes) stores the current value.
	setSuppressedBy := func(e *core.RecordEvent) error {
		e.Record.Set("suppressedBy", hub.systemSuppressedBy(e.Record.GetStringSlice("dependsOn")))
		return e.Next()
	}
	hub.OnRecordCreate("systems").BindFunc(setSuppressedBy)
	hub.OnRecordUpdate("systems").BindFunc(setSuppressedBy)
}

// systemSuppressedBy returns the suppressedBy value of a system depending on deps.
func (h *Hub) systemSuppressedBy(deps []string) string {
	if len(deps) == 0 || h.uptime == nil {
		return ""
	}
	return uptime.JoinSuppressedBy(h.uptime.DownDependencies(deps))
}

// systemSuppressed reports whether the status alerts of a system are
// suppressed because a monitor it depends on is down.
func (h *Hub) systemSuppressed(systemID string) bool {
	record, err := h.FindRecordById("systems", systemID)
	if err != nil {
		return false
	}
	return h.systemSuppressedBy(record.GetStringSlice("dependsOn")) != ""
}

// refreshSystemDependencies updates the systems that depend on monitors
// whose down state changed, and sends the status alerts suppressed while
// they were down.
func (h *Hub) refreshSystemDependencies(monitorIDs []string) {
	if len(monitorIDs) == 0 {
		return
	}
	// Few systems have dependencies, so they are filtered here.
	records, err := h.FindAllRecords("systems", dbx.NewExp("dependsOn NOT IN ('', '[]')"))
	if err != nil {
		h.Logger().Error("Failed to load dependent systems", "err", err)
		return
	}
	for _, record := range records {
		deps := record.GetStringSlice("dependsOn")
		if !slices.ContainsFunc(monitorIDs, func(id string) bool { return slices.Contains(deps, id) }) {
			continue
		}
		suppressedBy := h.systemSuppressedBy(record.GetStringSlice("dependsOn"))
		wasSuppressed := record.GetString("suppressedBy") != ""
		if suppressedBy != record.GetString("suppressedBy") {
			record.Set("suppressedBy", suppressedBy)
			if err := h.SaveNoValidate(record); err != nil {
				h.Logger().Error("Failed to update system dependencies", "system", record.Id, "err", err)
			}
		}
		if suppressedBy == "" && h.AlertManager != nil {
			h.HandleSystemDependencyRecovered(record)
		}
		// A system still down once its parents recover opens the automatic
		// incident held back while they were down.
		if suppressedBy == "" && wasSuppressed && record.GetString("status") == uptime.StatusDown {
			component := incidentComponent{field: "systems", id: record.Id}
			if err := openAutoIncidents(h, component, time.Now()); err != nil {
				h.Logger().Error("Failed to update automatic incidents", "system", record.Id, "err", err)
			}
		}
	}
}

// prepareDependencies validates the dependsOn field of a monitor or system
// submitted by auth (nil for superusers): at most maxDependencies monitors,
// that the requester can view, and for monitors neither the monitor itself
// nor a cycle. Unchanged dependencies are not checked again. It also keeps
// the server-managed suppressedBy field. Errors are monitorInputError.
func prepareDependencies(app core.App, record, original *core.Record, auth *core.Record, superuser, isMonitor bool) error {
	if original != nil {
		record.Set("suppressedBy", original.GetString("suppressedBy"))
	} else {
		record.Set("suppressedBy", "")
	}
	deps := record.GetStringSlice("dependsOn")
	var previous []string
	if original != nil {
		previous = original.GetStringSlice("dependsOn")
		if slices.Equal(deps, previous) {
			return nil
		}
	}
	if len(deps) > maxDependencies {
		return monitorInputError(fmt.Sprintf("A record can depend on at most %d monitors", maxDependencies))
	}
	if len(deps) == 0 {
		return nil
	}
	if isMonitor && slices.Contains(deps, record.Id) {
		return monitorInputError("A monitor cannot depend on itself")
	}
	collection, err := app.FindCachedCollectionByNameOrId("network_monitors")
	if err != nil {
		return err
	}
	parents, err := app.FindRecordsByIds(collection, deps)
	if err != nil {
		return err
	}
	if len(parents) != len(deps) {
		return monitorInputError("Unknown dependency")
	}
	if !superuser {
		info := &core.RequestInfo{Auth: auth}
		for _, parent := range parents {
			if slices.Contains(previous, parent.Id) {
				continue
			}
			if ok, err := app.CanAccessRecord(parent, info, collection.ViewRule); err != nil || !ok {
				return monitorInputError("You do not have access to all selected dependencies")
			}
		}
	}
	if isMonitor && record.Id != "" {
		if err := checkDependencyCycle(app, record.Id, deps); err != nil {
			return err
		}
	}
	return nil
}

// checkDependencyCycle rejects dependencies of a monitor that lead back to
// it, or chains deeper than maxDependencyDepth.
func checkDependencyCycle(app core.App, monitorID string, deps []string) error {
	level := deps
	seen := map[string]bool{}
	for depth := 1; len(level) > 0; depth++ {
		if depth > maxDependencyDepth {
			return monitorInputError(fmt.Sprintf("Dependency chains can be at most %d monitors deep", maxDependencyDepth))
		}
		var next []string
		for _, id := range level {
			if id == monitorID {
				return monitorInputError("Dependencies cannot form a cycle")
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			var raw string
			if err := app.DB().Select("dependsOn").From("network_monitors").Where(dbx.HashExp{"id": id}).Row(&raw); err != nil {
				continue
			}
			var parents []string
			if json.Unmarshal([]byte(raw), &parents) == nil {
				next = append(next, parents...)
			}
		}
		level = next
	}
	return nil
}

// idQueue passes monitor ids to a handler on a separate goroutine, so the
// uptime engine never waits for dependent systems to update.
type idQueue struct {
	handle func([]string)

	mu      sync.Mutex
	pending []string
	running bool
	// idle is closed when no ids are pending or being handled (for tests).
	idle chan struct{}
}

func newIDQueue(handle func([]string)) *idQueue {
	idle := make(chan struct{})
	close(idle)
	return &idQueue{handle: handle, idle: idle}
}

// push queues ids. A worker goroutine runs while any are queued.
func (q *idQueue) push(ids []string) {
	if len(ids) == 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, id := range ids {
		if !slices.Contains(q.pending, id) {
			q.pending = append(q.pending, id)
		}
	}
	if !q.running {
		q.running = true
		q.idle = make(chan struct{})
		go q.run()
	}
}

func (q *idQueue) run() {
	for {
		q.mu.Lock()
		batch := q.pending
		q.pending = nil
		if len(batch) == 0 {
			q.running = false
			close(q.idle)
			q.mu.Unlock()
			return
		}
		q.mu.Unlock()
		q.handle(batch)
	}
}

// wait blocks until the queue is idle or the timeout elapses.
func (q *idQueue) wait(timeout time.Duration) bool {
	q.mu.Lock()
	idle := q.idle
	q.mu.Unlock()
	select {
	case <-idle:
		return true
	case <-time.After(timeout):
		return false
	}
}
