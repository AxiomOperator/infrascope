package systems

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/hub/ws"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/hub/expirymap"
	"github.com/henrygd/beszel/internal/hub/uptime"

	"github.com/henrygd/beszel/internal/common"

	"github.com/henrygd/beszel"

	"github.com/blang/semver"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/store"
	"golang.org/x/crypto/ssh"
)

// System status constants
const (
	up      string = "up"      // System is online and responding
	down    string = "down"    // System is offline or not responding
	paused  string = "paused"  // System monitoring is paused
	pending string = "pending" // System is waiting on initial connection result

	// interval is the default update interval in milliseconds (60 seconds)
	interval int = 60_000
	// interval int = 10_000 // Debug interval for faster updates

	// sessionTimeout is the maximum time to wait for SSH connections
	sessionTimeout = 4 * time.Second
)

// errSystemExists is returned when attempting to add a system that already exists
var errSystemExists = errors.New("system exists")

// errNoSSHConfig is returned when SSH is needed but the hub has no usable key.
var errNoSSHConfig = errors.New("SSH is unavailable: no hub SSH key")

// SystemManager manages a collection of monitored systems and their connections.
// It handles system lifecycle, status updates, and maintains both SSH and WebSocket connections.
type SystemManager struct {
	hub                 hubLike                               // Hub interface for database and alert operations
	systems             *store.Store[string, *System]         // Thread-safe store of active systems
	sshConfig           *ssh.ClientConfig                     // SSH client configuration for system connections; guarded by sshConfigMu
	sshConfigMu         sync.Mutex                            // Guards lazy creation of sshConfig
	lifecycleMu         sync.Mutex                            // Makes adding/replacing/removing systems atomic
	lastConfirmed       sync.Map                              // System ID -> last confirmed status (up or down), kept across pending
	smartFetchMap       *expirymap.ExpiryMap[smartFetchState] // Stores last SMART fetch time/result; TTL is only for cleanup
	zfsFetchMap         *expirymap.ExpiryMap[zfsFetchState]   // Stores last ZFS fetch time/result; TTL is only for cleanup
	realtimeMutex       sync.Mutex                            // Protects all realtime worker and subscription state
	activeSubscriptions map[string]*subscriptionInfo          // Realtime subscriptions keyed by system ID
	realtimeWorkerStop  chan struct{}                         // Stops the current realtime worker generation
	realtimeWorkerRun   bool                                  // Whether a realtime worker has been started
	ctx                 context.Context                       // Cancelled when the app terminates
	cancel              context.CancelFunc                    // Cancels ctx and all child system contexts
}

// hubLike defines the interface requirements for the hub dependency.
// It extends core.App with system-specific functionality.
type hubLike interface {
	core.App
	GetSSHKey(dataDir string) (ssh.Signer, error)
	HandleSystemAlerts(systemRecord *core.Record, data *system.CombinedData) error
	HandleNetworkMonitorAlerts(systemRecord *core.Record, results map[string]monitor.Result) error
	HandleStatusAlerts(status string, systemRecord *core.Record) error
	HandleContainerAlerts(systemRecord *core.Record, data *system.CombinedData, fetchLogs func(containerID string) (string, error)) error
	CancelPendingStatusAlerts(systemID string)
	CancelPendingContainerAlerts(systemID string)
	// Uptime returns the monitor status engine, or nil when there is none.
	Uptime() *uptime.Engine
}

// NewSystemManager creates a new SystemManager instance with the provided hub.
// The hub must implement the hubLike interface to provide database and alert functionality.
func NewSystemManager(hub hubLike) *SystemManager {
	sm := &SystemManager{
		systems:             store.New(map[string]*System{}),
		hub:                 hub,
		smartFetchMap:       expirymap.New[smartFetchState](time.Hour),
		zfsFetchMap:         expirymap.New[zfsFetchState](time.Hour),
		activeSubscriptions: make(map[string]*subscriptionInfo),
	}
	sm.ctx, sm.cancel = context.WithCancel(context.Background())
	return sm
}

// GetSystem returns a system by ID from the store
func (sm *SystemManager) GetSystem(systemID string) (*System, error) {
	sys, ok := sm.systems.GetOk(systemID)
	if !ok {
		return nil, fmt.Errorf("system not found")
	}
	return sys, nil
}

// Initialize sets up the system manager by binding event hooks and starting existing systems.
// It configures SSH client settings and begins monitoring all non-paused systems from the database.
// Systems are started with staggered delays to prevent overwhelming the hub during startup.
func (sm *SystemManager) Initialize() error {
	sm.bindEventHooks()

	// Initialize SSH client configuration
	err := sm.createSSHClientConfig()
	if err != nil {
		return err
	}

	// Load existing systems from database (excluding paused ones)
	var systems []*System
	err = sm.hub.DB().NewQuery("SELECT id, host, port, status FROM systems WHERE status != 'paused'").All(&systems)
	if err != nil || len(systems) == 0 {
		return err
	}

	// Start systems in background with staggered timing
	go func() {
		// Calculate staggered delay between system starts (max 2 seconds per system)
		delta := interval / max(1, len(systems))
		delta = min(delta, 2_000)
		sleepTime := time.Duration(delta) * time.Millisecond

		for _, system := range systems {
			if !waitForContext(sm.ctx, sleepTime) {
				return
			}
			_ = sm.AddSystem(system)
		}
	}()
	return nil
}

// bindEventHooks registers event handlers for system and fingerprint record changes.
// These hooks ensure the system manager stays synchronized with database changes.
func (sm *SystemManager) bindEventHooks() {
	sm.hub.OnRecordCreate("systems").BindFunc(sm.onRecordCreate)
	sm.hub.OnRecordAfterCreateSuccess("systems").BindFunc(sm.onRecordAfterCreateSuccess)
	sm.hub.OnRecordUpdate("systems").BindFunc(sm.onRecordUpdate)
	sm.hub.OnRecordAfterUpdateSuccess("systems").BindFunc(sm.onRecordAfterUpdateSuccess)
	sm.hub.OnRecordAfterDeleteSuccess("systems").BindFunc(sm.onRecordAfterDeleteSuccess)
	sm.hub.OnRecordAfterUpdateSuccess("fingerprints").BindFunc(sm.onTokenRotated)
	sm.hub.OnRealtimeSubscribeRequest().BindFunc(sm.onRealtimeSubscribeRequest)
	sm.hub.OnRealtimeConnectRequest().BindFunc(sm.onRealtimeConnectRequest)
	sm.hub.OnTerminate().BindFunc(sm.onTerminate)
}

// onTerminate cancels SystemManager context on app shutdown
func (sm *SystemManager) onTerminate(e *core.TerminateEvent) error {
	sm.cancel()
	sm.stopRealtimeWorker()
	return e.Next()
}

// onTokenRotated handles fingerprint token rotation events.
// When a system's authentication token is rotated, any existing WebSocket connection
// must be closed to force re-authentication with the new token.
func (sm *SystemManager) onTokenRotated(e *core.RecordEvent) error {
	systemID := e.Record.GetString("system")
	system, ok := sm.systems.GetOk(systemID)
	if !ok {
		return e.Next()
	}
	// No need to close connection if not connected via websocket
	if system.getWsConn() == nil {
		return e.Next()
	}
	system.setDown(nil)
	sm.RemoveSystem(systemID)
	return e.Next()
}

// onRecordCreate is called before a new system record is committed to the database.
// It initializes the record with default values: empty info and pending status.
func (sm *SystemManager) onRecordCreate(e *core.RecordEvent) error {
	e.Record.Set("info", system.Info{})
	e.Record.Set("status", pending)
	return e.Next()
}

// onRecordAfterCreateSuccess is called after a new system record is successfully created.
// It adds the new system to the manager to begin monitoring.
func (sm *SystemManager) onRecordAfterCreateSuccess(e *core.RecordEvent) error {
	if err := sm.AddRecord(e.Record, nil); err != nil {
		e.App.Logger().Error("Error adding record", "err", err)
	}
	return e.Next()
}

// onRecordUpdate is called before a system record is updated in the database.
// It clears system info when the status is changed to paused.
func (sm *SystemManager) onRecordUpdate(e *core.RecordEvent) error {
	// A pinned host key belongs to the previous address; trust the new one on
	// first use.
	if original := e.Record.Original(); original != nil &&
		(original.GetString("host") != e.Record.GetString("host") || original.GetString("port") != e.Record.GetString("port")) {
		e.Record.Set(hostKeyField, "")
	}
	if e.Record.GetString("status") == paused {
		var prevInfo system.Info
		e.Record.UnmarshalJSONField("info", &prevInfo)
		e.Record.Set("info", system.Info{AgentVersion: prevInfo.AgentVersion})
	}
	return e.Next()
}

// onRecordAfterUpdateSuccess handles system record updates after they're committed to the database.
// It manages system lifecycle based on status changes and triggers appropriate alerts.
// Status transitions are handled as follows:
// - paused: Closes SSH connection and deactivates alerts
// - pending: Starts monitoring (reuses WebSocket if available)
// - up: Triggers system alerts
// - down: Cancels pending container alerts and triggers status change alerts
func (sm *SystemManager) onRecordAfterUpdateSuccess(e *core.RecordEvent) error {
	// Dependency updates only change the server-managed suppressedBy field.
	if dependencyUpdate(e.Record) {
		return e.Next()
	}
	newStatus := e.Record.GetString("status")
	prevStatus := pending
	system, ok := sm.systems.GetOk(e.Record.Id)
	if ok {
		prevStatus = system.swapStatus(newStatus)
	}
	// Remember the last confirmed state across pending, so a system that was
	// up before an edit or resume still alerts when it then goes down.
	lastConfirmed := prevStatus
	if prevStatus == pending {
		if v, found := sm.lastConfirmed.Load(e.Record.Id); found {
			lastConfirmed = v.(string)
		}
	}
	if newStatus == up || newStatus == down {
		sm.lastConfirmed.Store(e.Record.Id, newStatus)
	}
	// Monitors of a system that is not up have no current results.
	if !ok || prevStatus != newStatus {
		if engine := sm.hub.Uptime(); engine != nil {
			engine.SystemStatusChanged(e.Record.Id, newStatus)
		}
	}

	switch newStatus {
	case paused:
		if ok {
			// Pause monitoring but keep system in manager for potential resume
			system.closeSSHConnection()
		}
		_ = deactivateAlerts(e.App, e.Record.Id, false)
		sm.hub.CancelPendingStatusAlerts(e.Record.Id)
		sm.hub.CancelPendingContainerAlerts(e.Record.Id)
		return e.Next()
	case pending:
		// Keep an active status alert until connectivity is confirmed. This lets
		// pending -> up resolve it and send the recovery notification after a
		// system address or other connection setting is changed.
		_ = deactivateAlerts(e.App, e.Record.Id, true)
		// Resume monitoring, preferring existing WebSocket connection
		if ok && system.getWsConn() != nil {
			go system.update()
			return e.Next()
		}
		// Start new monitoring session
		if err := sm.AddRecord(e.Record, nil); err != nil {
			e.App.Logger().Error("Error adding record", "err", err)
		}
		return e.Next()
	case down:
		// Docker state is unknown while the system is unreachable. Do not let a
		// delayed container-health alert fire from the last received snapshot.
		sm.hub.CancelPendingContainerAlerts(e.Record.Id)
	}

	// Handle systems not in manager
	if !ok {
		return sm.AddRecord(e.Record, nil)
	}

	// Trigger system alerts when system comes online
	if newStatus == up {
		data := system.getData()
		if err := sm.hub.HandleSystemAlerts(e.Record, data); err != nil {
			e.App.Logger().Error("Error handling system alerts", "err", err)
		}
		if err := sm.hub.HandleContainerAlerts(e.Record, data, system.FetchContainerLogsFromAgent); err != nil {
			e.App.Logger().Error("Error handling container alerts", "err", err)
		}
	}

	// A connection-setting update moves a down system through pending before it
	// comes up, so recover active status alerts on any non-up -> up transition.
	// Likewise, up -> pending -> down alerts once: the down is judged against the
	// last confirmed status rather than pending. (down -> pending -> down does
	// not alert again; the preserved triggered alert also suppresses it.)
	if (newStatus == down && prevStatus != down && lastConfirmed == up) || (newStatus == up && prevStatus != up) {
		if err := sm.hub.HandleStatusAlerts(newStatus, e.Record); err != nil {
			e.App.Logger().Error("Error handling status alerts", "err", err)
		}
	}
	return e.Next()
}

// dependencyUpdate reports whether a saved system record changed only its
// suppressedBy field (and the update time), as the hub's dependency updates do.
func dependencyUpdate(record *core.Record) bool {
	original := record.Original()
	if original == nil || original.IsNew() || record.GetString("suppressedBy") == original.GetString("suppressedBy") {
		return false
	}
	for _, field := range record.Collection().Fields {
		name := field.GetName()
		if name == "suppressedBy" || name == "updated" {
			continue
		}
		if !reflect.DeepEqual(record.Get(name), original.Get(name)) {
			return false
		}
	}
	return true
}

// onRecordAfterDeleteSuccess is called after a system record is successfully deleted.
// It removes the system from the manager and cleans up all associated resources.
func (sm *SystemManager) onRecordAfterDeleteSuccess(e *core.RecordEvent) error {
	sm.RemoveSystem(e.Record.Id)
	sm.lastConfirmed.Delete(e.Record.Id)
	return e.Next()
}

// AddSystem adds a system to the manager and starts monitoring it.
// It validates required fields, initializes the system context, and starts the update goroutine.
// Returns error if a system with the same ID already exists.
func (sm *SystemManager) AddSystem(sys *System) error {
	sm.lifecycleMu.Lock()
	defer sm.lifecycleMu.Unlock()
	return sm.addSystemLocked(sys)
}

// addSystemLocked adds sys unless a system with its ID exists. The caller must
// hold lifecycleMu.
func (sm *SystemManager) addSystemLocked(sys *System) error {
	if sm.systems.Has(sys.Id) {
		return errSystemExists
	}
	if sys.Id == "" || sys.Host == "" {
		return errors.New("system missing required fields")
	}

	// Initialize system for monitoring
	sys.manager = sm
	sys.ctx, sys.cancel = sys.getContext(sm.ctx)
	sys.setData(&system.CombinedData{})
	sm.systems.Set(sys.Id, sys)

	// Start monitoring in background
	go sys.StartUpdater()
	return nil
}

// RemoveSystem removes a system from the manager and cleans up all associated resources.
// It cancels the system's context, closes all connections, and removes it from the store.
// Returns an error if the system is not found.
func (sm *SystemManager) RemoveSystem(systemID string) error {
	sm.lifecycleMu.Lock()
	defer sm.lifecycleMu.Unlock()
	system, ok := sm.systems.GetOk(systemID)
	if !ok {
		return errors.New("system not found")
	}
	sm.removeSystemLocked(system)
	return nil
}

// removeInstance removes sys only if it is still the managed instance for its
// ID, so a replaced system's updater cannot remove its replacement.
func (sm *SystemManager) removeInstance(sys *System) {
	sm.lifecycleMu.Lock()
	defer sm.lifecycleMu.Unlock()
	if current, ok := sm.systems.GetOk(sys.Id); ok && current == sys {
		sm.removeSystemLocked(sys)
	}
}

// removeSystemLocked stops and removes system. The caller must hold lifecycleMu.
func (sm *SystemManager) removeSystemLocked(system *System) {
	// Stop the update goroutine
	if system.cancel != nil {
		system.cancel()
	}

	// Clean up all connections
	system.closeSSHConnection()
	system.closeWebSocketConnection()
	sm.systems.Remove(system.Id)
}

// AddRecord creates a System instance from a database record and adds it to the manager.
// If a system with the same ID already exists, it's removed first to ensure clean state.
// If no system instance is provided, a new one is created.
// This method is typically called when systems are created or their status changes to pending.
// Replacing is atomic: concurrent calls never leave two updaters for one system.
func (sm *SystemManager) AddRecord(record *core.Record, system *System) (err error) {
	// Create new system if none provided
	if system == nil {
		system = sm.NewSystem(record.Id)
	}

	// Populate system from record
	system.setStatus(record.GetString("status"))
	system.Host = record.GetString("host")
	system.Port = record.GetString("port")
	system.autoDiscover.Store(record.GetBool("autoDiscover"))

	sm.lifecycleMu.Lock()
	defer sm.lifecycleMu.Unlock()
	// Remove existing system to ensure clean state
	if existing, ok := sm.systems.GetOk(record.Id); ok {
		sm.removeSystemLocked(existing)
	}
	return sm.addSystemLocked(system)
}

// AddWebSocketSystem creates and adds a system with an established WebSocket connection.
// This method is called when an agent connects via WebSocket with valid authentication.
// The system is immediately added to monitoring with the provided connection and version info.
func (sm *SystemManager) AddWebSocketSystem(systemId string, agentVersion semver.Version, wsConn *ws.WsConn) error {
	systemRecord, err := sm.hub.FindRecordById("systems", systemId)
	if err != nil {
		return err
	}
	sm.resetFailedSmartFetchState(systemId)

	system := sm.NewSystem(systemId)
	system.setWsConn(wsConn)
	system.setAgentVersion(agentVersion)
	system.monitorsNeedSync.Store(true)

	if err := sm.AddRecord(systemRecord, system); err != nil {
		return err
	}

	// Sync network monitors to the newly connected agent
	go system.syncPendingNetworkMonitors()

	return nil
}

// resetFailedSmartFetchState clears only failed SMART cooldown entries so a fresh
// agent reconnect retries SMART discovery immediately after configuration changes.
func (sm *SystemManager) resetFailedSmartFetchState(systemID string) {
	state, ok := sm.smartFetchMap.GetOk(systemID)
	if ok && !state.Successful {
		sm.smartFetchMap.Remove(systemID)
	}
}

// GetMonitorConfigsForSystem returns the configs of all enabled monitors with
// the system as one of their locations. Monitors with malformed HTTP options
// are skipped rather than probed with defaults.
func (sm *SystemManager) GetMonitorConfigsForSystem(systemID string) ([]monitor.Config, error) {
	records, err := FindLocationMonitors(sm.hub, systemID, dbx.HashExp{"enabled": true})
	if err != nil {
		return nil, err
	}
	configs := make([]monitor.Config, 0, len(records))
	for _, record := range records {
		// Push monitors are hub-only; the hooks never assign them a system.
		if record.GetString("protocol") == monitor.ProtocolPush {
			continue
		}
		config, err := MonitorConfigFromRecord(sm.hub, record)
		if err != nil {
			sm.hub.Logger().Warn("skipping monitor with invalid config", "system", systemID, "monitor", record.Id, "err", err)
			continue
		}
		configs = append(configs, config)
	}
	return configs, nil
}

// resetFailedZfsFetchState clears only failed ZFS cooldown entries so a fresh
// agent reconnect retries ZFS discovery immediately after configuration changes.
func (sm *SystemManager) resetFailedZfsFetchState(systemID string) {
	state, ok := sm.zfsFetchMap.GetOk(systemID)
	if ok && !state.Successful {
		sm.zfsFetchMap.Remove(systemID)
	}
}

// createSSHClientConfig initializes the SSH client configuration for connecting to an agent's server
func (sm *SystemManager) createSSHClientConfig() error {
	sm.sshConfigMu.Lock()
	defer sm.sshConfigMu.Unlock()
	return sm.createSSHClientConfigLocked()
}

// getSSHConfig returns the shared SSH client configuration, creating it on
// first use. Host key verification is added per system (see sshClientConfig).
func (sm *SystemManager) getSSHConfig() (*ssh.ClientConfig, error) {
	sm.sshConfigMu.Lock()
	defer sm.sshConfigMu.Unlock()
	if sm.sshConfig == nil {
		if err := sm.createSSHClientConfigLocked(); err != nil {
			return nil, err
		}
	}
	return sm.sshConfig, nil
}

func (sm *SystemManager) createSSHClientConfigLocked() error {
	if sm.hub == nil {
		return errNoSSHConfig
	}
	privateKey, err := sm.hub.GetSSHKey("")
	if err != nil {
		return err
	}
	if privateKey == nil {
		return errNoSSHConfig
	}

	sm.sshConfig = &ssh.ClientConfig{
		User: "u",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(privateKey),
		},
		Config: ssh.Config{
			Ciphers:      common.DefaultCiphers,
			KeyExchanges: common.DefaultKeyExchanges,
			MACs:         common.DefaultMACs,
		},
		// Replaced per system by a trust-on-first-use check (see sshClientConfig).
		HostKeyCallback: rejectHostKey,
		ClientVersion:   fmt.Sprintf("SSH-2.0-%s_%s", beszel.AppName, beszel.Version),
		Timeout:         sessionTimeout,
	}
	return nil
}

// deactivateAlerts finds triggered alerts for a system and sets them to inactive.
// Status alerts can be preserved while connection changes are pending so that a
// confirmed recovery still produces an "up" notification.
// Monitor incidents remain open: a missing observation does not establish recovery.
func deactivateAlerts(app core.App, systemID string, preserveStatusAlert bool) error {
	// Note: Direct SQL updates don't trigger SSE, so we use the PocketBase API
	// _, err := app.DB().NewQuery(fmt.Sprintf("UPDATE alerts SET triggered = false WHERE system = '%s'", systemID)).Execute()

	alerts, err := app.FindRecordsByFilter("alerts", fmt.Sprintf("system = '%s' && triggered = 1 && name != 'NetworkMonitorLoss'", systemID), "", -1, 0)
	if err != nil {
		return err
	}

	for _, alert := range alerts {
		if preserveStatusAlert && alert.GetString("name") == "Status" {
			continue
		}
		alert.Set("triggered", false)
		if err := app.SaveNoValidate(alert); err != nil {
			return err
		}
	}
	return nil
}

// waitForContext waits for delay or returns early when ctx is cancelled.
func waitForContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
