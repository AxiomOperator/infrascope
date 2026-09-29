package netmon

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
)

// Manager manages network monitor configurations and task lifetimes.
type Manager struct {
	mu          sync.RWMutex
	monitors    map[string]*monitorTask // keyed by monitor ID
	probe       monitorProbe
	certCheck   certChecker
	resumeGuard monitorResumeGuard
	// defaultIntervalMs is the GetResults duration whose results update monitor
	// records on the hub. Only requests for it consume unsent certificate info
	// and check events.
	defaultIntervalMs uint16
	onCheck           func(id string, event monitor.CheckEvent)
}

// Option configures a Manager.
type Option func(*managerOptions)

type managerOptions struct {
	userAgent   string
	concurrency int
	onCheck     func(id string, event monitor.CheckEvent)
	probes      map[string]ProbeFunc
}

// WithProbe runs monitors of protocol with probe instead of the built-in
// probe, such as the agent's docker container check.
func WithProbe(protocol string, probe ProbeFunc) Option {
	return func(o *managerOptions) {
		if o.probes == nil {
			o.probes = map[string]ProbeFunc{}
		}
		o.probes[protocol] = probe
	}
}

// WithUserAgent sets the User-Agent of HTTP probes. A User-Agent header in a
// monitor's HTTP options still takes precedence.
func WithUserAgent(userAgent string) Option {
	return func(o *managerOptions) { o.userAgent = userAgent }
}

// WithConcurrency limits how many probes run at once across all monitors.
// Zero or less means unlimited.
func WithConcurrency(n int) Option {
	return func(o *managerOptions) { o.concurrency = n }
}

// WithOnCheck sets a callback run after each recorded check, including
// immediate and external checks. It runs outside locks on the checking
// goroutine, so it should return quickly.
func WithOnCheck(onCheck func(id string, event monitor.CheckEvent)) Option {
	return func(o *managerOptions) { o.onCheck = onCheck }
}

// NewManager returns a Manager that probes monitors over the network.
// defaultIntervalMs is the GetResults duration (in ms) used for the regular
// stats interval; only GetResults calls with that duration consume unsent
// certificate info and check events.
func NewManager(defaultIntervalMs uint16, opts ...Option) *Manager {
	options := managerOptions{userAgent: networkMonitorUserAgent}
	for _, opt := range opts {
		opt(&options)
	}
	probe := networkMonitorProbe(newHTTPProber(options.userAgent), options.probes)
	if options.concurrency > 0 {
		probe = limitProbe(probe, make(chan struct{}, options.concurrency))
	}
	pm := newManagerWithProbe(probe, defaultIntervalMs)
	pm.onCheck = options.onCheck
	return pm
}

func newManagerWithProbe(probe monitorProbe, defaultIntervalMs uint16) *Manager {
	return &Manager{monitors: make(map[string]*monitorTask), probe: probe, certCheck: checkCert, defaultIntervalMs: defaultIntervalMs}
}

// newTask requires mu. It replaces existing (which may be nil) with a task for
// config, keeping its history.
func (pm *Manager) newTask(config monitor.Config, existing *monitorTask) *monitorTask {
	task := newMonitorTaskFromExisting(config, existing)
	task.resumeGuard = &pm.resumeGuard
	task.onCheck = pm.onCheck
	pm.resumeGuard.start()
	pm.monitors[config.ID] = task
	return task
}

// SyncMonitors replaces all monitor tasks with the given configs.
func (pm *Manager) SyncMonitors(configs []monitor.Config) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Build set of new keys
	newKeys := make(map[string]monitor.Config, len(configs))
	for _, cfg := range configs {
		if cfg.ID == "" {
			continue
		}
		newKeys[cfg.ID] = cfg
	}

	// Stop removed monitors
	for key, task := range pm.monitors {
		if _, exists := newKeys[key]; !exists {
			task.cancel()
			delete(pm.monitors, key)
		}
	}

	// Start new monitors and restart tasks whose config changed.
	for key, cfg := range newKeys {
		task, exists := pm.monitors[key]
		if exists && task.config.Equal(cfg) {
			continue
		}
		if exists {
			task.cancel()
		}
		pm.startMonitor(pm.newTask(cfg, task))
	}
	if len(pm.monitors) == 0 {
		pm.resumeGuard.shutdown()
	}
}

// HandleSyncRequest applies a full or incremental monitor sync request.
func (pm *Manager) HandleSyncRequest(req monitor.SyncRequest) (monitor.SyncResponse, error) {
	switch req.Action {
	case monitor.SyncActionReplace:
		pm.SyncMonitors(req.Configs)
		return monitor.SyncResponse{}, nil
	case monitor.SyncActionUpsert:
		result, err := pm.UpsertMonitor(req.Config, req.RunNow)
		if err != nil {
			return monitor.SyncResponse{}, err
		}
		if result == nil {
			return monitor.SyncResponse{}, nil
		}
		return monitor.SyncResponse{Result: *result}, nil
	case monitor.SyncActionDelete:
		if req.Config.ID == "" {
			return monitor.SyncResponse{}, errors.New("missing monitor ID for delete")
		}
		pm.DeleteMonitor(req.Config.ID)
		return monitor.SyncResponse{}, nil
	default:
		return monitor.SyncResponse{}, fmt.Errorf("unknown monitor sync action: %d", req.Action)
	}
}

// UpsertMonitor creates or replaces a single monitor task.
func (pm *Manager) UpsertMonitor(config monitor.Config, runNow bool) (*monitor.Result, error) {
	if config.ID == "" {
		return nil, errors.New("missing monitor ID")
	}

	pm.mu.Lock()
	task, exists := pm.monitors[config.ID]
	if exists && task.config.Equal(config) {
		pm.mu.Unlock()
		if !runNow {
			return nil, nil
		}
		return pm.runNow(task), nil
	}
	if exists {
		task.cancel()
	}
	task = pm.newTask(config, task)
	pm.mu.Unlock()

	if runNow {
		result := pm.runNow(task)
		pm.startMonitor(task)
		return result, nil
	}
	pm.startMonitor(task)
	return nil, nil
}

// runNow runs a probe and any due certificate check concurrently, so the
// response fits within the hub's single probe timeout budget. Push monitors
// are not probed; their latest result is returned instead.
func (pm *Manager) runNow(task *monitorTask) *monitor.Result {
	if task.config.Protocol == monitor.ProtocolPush {
		if result, ok := task.history.result(time.Minute, time.Now()); ok {
			return &result
		}
		return nil
	}
	var wg sync.WaitGroup
	wg.Go(func() { task.refreshCert(pm.certCheck) })
	result := task.run(pm.probe, true)
	wg.Wait()
	if result != nil {
		result.Cert = task.certInfo()
	}
	return result
}

// RecordExternal records an outcome for a monitor as if it had been probed,
// updating history and queuing a check event. It is meant for push monitors.
func (pm *Manager) RecordExternal(id string, out Outcome) error {
	// Look up again if the task is replaced concurrently.
	for range 3 {
		pm.mu.RLock()
		task := pm.monitors[id]
		pm.mu.RUnlock()
		if task == nil {
			break
		}
		if task.recordExternal(out) {
			return nil
		}
	}
	return fmt.Errorf("unknown monitor: %s", id)
}

// DeleteMonitor stops and removes a single monitor task.
func (pm *Manager) DeleteMonitor(id string) {
	if id == "" {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if task, exists := pm.monitors[id]; exists {
		task.cancel()
		delete(pm.monitors, id)
	}
	if len(pm.monitors) == 0 {
		pm.resumeGuard.shutdown()
	}
}

// GetResults returns aggregated results for all monitors over the last supplied duration in ms.
func (pm *Manager) GetResults(durationMs uint16) map[string]monitor.Result {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	results := make(map[string]monitor.Result, len(pm.monitors))
	now := time.Now()
	duration := time.Duration(durationMs) * time.Millisecond

	for _, task := range pm.monitors {
		result, ok := task.history.result(duration, now)

		if !ok {
			continue
		}
		// Only the default interval updates monitor records on the hub, so
		// realtime requests must not consume the unsent certificate or checks.
		if durationMs == pm.defaultIntervalMs {
			result.Cert = task.takeUnsentCert()
			result.Checks, result.Dropped = task.takeUnsentChecks()
		}
		results[task.config.ID] = result
	}

	return results
}

// Stop stops all monitor tasks.
func (pm *Manager) Stop() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for key, task := range pm.monitors {
		task.cancel()
		delete(pm.monitors, key)
	}
	pm.resumeGuard.shutdown()
}
