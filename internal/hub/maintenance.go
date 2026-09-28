package hub

import (
	"slices"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/alerts"
	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/pocketbase/core"
)

const maintenanceCollection = "monitor_maintenance"

// maintenanceWindow is a monitor_maintenance record.
type maintenanceWindow struct {
	windowType string
	start, end time.Time
	monitors   []string
}

// maintenanceWindows keeps the monitor maintenance windows in memory for the
// uptime engine, which asks for every monitor on each tick. Windows are read
// on first use and kept current by record hooks. They are evaluated like
// quiet hours (alerts.WindowActive).
type maintenanceWindows struct {
	app core.App

	mu      sync.RWMutex
	loaded  bool
	windows map[string]maintenanceWindow
}

func newMaintenanceWindows(app core.App) *maintenanceWindows {
	m := &maintenanceWindows{app: app}
	set := func(e *core.RecordEvent) error {
		m.set(e.Record)
		return e.Next()
	}
	app.OnRecordAfterCreateSuccess(maintenanceCollection).BindFunc(set)
	app.OnRecordAfterUpdateSuccess(maintenanceCollection).BindFunc(set)
	app.OnRecordAfterDeleteSuccess(maintenanceCollection).BindFunc(func(e *core.RecordEvent) error {
		m.mu.Lock()
		delete(m.windows, e.Record.Id)
		m.mu.Unlock()
		return e.Next()
	})
	return m
}

func windowFromRecord(record *core.Record) maintenanceWindow {
	return maintenanceWindow{
		windowType: record.GetString("type"),
		start:      record.GetDateTime("start").Time(),
		end:        record.GetDateTime("end").Time(),
		monitors:   record.GetStringSlice("monitors"),
	}
}

// set stores a created or updated window. Before the first load it does
// nothing, since the load reads the committed record.
func (m *maintenanceWindows) set(record *core.Record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded {
		m.windows[record.Id] = windowFromRecord(record)
	}
}

// load reads all windows. Requires m.mu held for writing.
func (m *maintenanceWindows) load() error {
	records, err := m.app.FindAllRecords(maintenanceCollection)
	if err != nil {
		return err
	}
	m.windows = make(map[string]maintenanceWindow, len(records))
	for _, record := range records {
		m.windows[record.Id] = windowFromRecord(record)
	}
	m.loaded = true
	return nil
}

// Active reports whether a window that lists the monitor is active at now.
func (m *maintenanceWindows) Active(monitorID string, now time.Time) bool {
	m.mu.RLock()
	if !m.loaded {
		m.mu.RUnlock()
		m.mu.Lock()
		if !m.loaded {
			if err := m.load(); err != nil {
				m.app.Logger().Warn("Failed to load maintenance windows", "err", err)
			}
		}
		m.mu.Unlock()
		m.mu.RLock()
	}
	defer m.mu.RUnlock()
	for _, window := range m.windows {
		if slices.Contains(window.monitors, monitorID) && alerts.WindowActive(window.windowType, window.start, window.end, now) {
			return true
		}
	}
	return false
}

// transitionQueue passes monitor status changes to a handler on a separate
// goroutine, in order, so the uptime engine never waits for notifications.
type transitionQueue struct {
	handle func([]uptime.Transition)

	mu      sync.Mutex
	pending []uptime.Transition
	running bool
	// idle is closed when no batch is pending or being handled (for tests).
	idle chan struct{}
}

func newTransitionQueue(handle func([]uptime.Transition)) *transitionQueue {
	idle := make(chan struct{})
	close(idle)
	return &transitionQueue{handle: handle, idle: idle}
}

// push queues transitions. A worker goroutine runs while any are queued.
func (q *transitionQueue) push(transitions []uptime.Transition) {
	if len(transitions) == 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = append(q.pending, transitions...)
	if !q.running {
		q.running = true
		q.idle = make(chan struct{})
		go q.run()
	}
}

func (q *transitionQueue) run() {
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

// wait blocks until the queue is empty and idle or the timeout elapses.
func (q *transitionQueue) wait(timeout time.Duration) bool {
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
