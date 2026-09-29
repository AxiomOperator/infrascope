package systems

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/hub/monitorloc"
	"github.com/henrygd/beszel/internal/hub/transport"
	"github.com/henrygd/beszel/internal/hub/utils"
	"github.com/henrygd/beszel/internal/hub/ws"

	"github.com/henrygd/beszel/internal/entities/container"
	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/entities/smart"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/entities/systemd"
	"github.com/henrygd/beszel/internal/entities/zfs"

	"github.com/henrygd/beszel"

	"github.com/blang/semver"
	"github.com/fxamacker/cbor/v2"
	"github.com/lxzan/gws"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"
	"golang.org/x/crypto/ssh"
)

type System struct {
	Id   string `db:"id"`
	Host string `db:"host"`
	Port string `db:"port"`
	// Status is read and written by the updater, record hooks and API handlers.
	// Once the system is shared, use GetStatus/setStatus/swapStatus.
	Status         string                     `db:"status"`
	manager        *SystemManager             // Manager that this system belongs to
	client         atomic.Pointer[ssh.Client] // SSH client for fetching data
	sshTransport   *transport.SSHTransport    // SSH transport for requests; guarded by sshMu
	data           *system.CombinedData       // system data from agent; guarded by mu
	ctx            context.Context            // Context for stopping the updater
	cancel         context.CancelFunc         // Stops and removes system from updater
	wsConn         *ws.WsConn                 // Handler for agent WebSocket connection; guarded by mu
	agentVersion   semver.Version             // Agent version; guarded by mu
	updateTicker   *time.Ticker               // Ticker for updating the system
	detailsFetched atomic.Bool                // True if static system details have been fetched and saved
	smartFetching  atomic.Bool                // True if SMART devices are currently being fetched
	smartInterval  time.Duration              // Interval for periodic SMART data updates; guarded by mu
	zfsFetching    atomic.Bool                // True if ZFS pools are currently being fetched
	zfsInterval    time.Duration              // Interval for periodic ZFS detail data updates; guarded by mu

	// mu guards Status, wsConn, data, agentVersion, smartInterval and zfsInterval.
	mu sync.RWMutex
	// sshMu guards sshTransport.
	sshMu sync.Mutex
	// updateMu serializes update(), which runs from the updater ticker and from
	// record hooks (resume from pending).
	updateMu sync.Mutex

	// A fresh connection needs a full monitor configuration sync.
	monitorsNeedSync atomic.Bool
	// Serialize persistence from scheduled updates and resumes through commit.
	recordsMu sync.Mutex
	// Protected by recordsMu; realtime reads don't consume probes.
	lastSavedMonitorProbe map[string]int64
}

// GetStatus returns the system's current status.
func (sys *System) GetStatus() string {
	sys.mu.RLock()
	defer sys.mu.RUnlock()
	return sys.Status
}

func (sys *System) setStatus(status string) {
	sys.mu.Lock()
	sys.Status = status
	sys.mu.Unlock()
}

// swapStatus sets the status and returns the previous one atomically.
func (sys *System) swapStatus(status string) string {
	sys.mu.Lock()
	defer sys.mu.Unlock()
	prev := sys.Status
	sys.Status = status
	return prev
}

func (sys *System) getWsConn() *ws.WsConn {
	sys.mu.RLock()
	defer sys.mu.RUnlock()
	return sys.wsConn
}

func (sys *System) setWsConn(conn *ws.WsConn) {
	sys.mu.Lock()
	sys.wsConn = conn
	sys.mu.Unlock()
}

// clearWsConn removes conn if it is still the system's connection.
func (sys *System) clearWsConn(conn *ws.WsConn) {
	sys.mu.Lock()
	if sys.wsConn == conn {
		sys.wsConn = nil
	}
	sys.mu.Unlock()
}

func (sys *System) getData() *system.CombinedData {
	sys.mu.RLock()
	defer sys.mu.RUnlock()
	return sys.data
}

func (sys *System) setData(data *system.CombinedData) {
	sys.mu.Lock()
	sys.data = data
	sys.mu.Unlock()
}

func (sys *System) getAgentVersion() semver.Version {
	sys.mu.RLock()
	defer sys.mu.RUnlock()
	return sys.agentVersion
}

func (sys *System) setAgentVersion(version semver.Version) {
	sys.mu.Lock()
	sys.agentVersion = version
	sys.mu.Unlock()
}

func (sm *SystemManager) NewSystem(systemId string) *System {
	system := &System{
		Id:   systemId,
		data: &system.CombinedData{},
	}
	system.ctx, system.cancel = system.getContext(sm.ctx)
	return system
}

// StartUpdater starts the system updater.
// It first fetches the data from the agent then updates the records.
// If the data is not found or the system is down, it sets the system down.
func (sys *System) StartUpdater() {
	// Channel that can be used to set the system down. Currently only used to
	// allow a short delay for reconnection after websocket connection is closed.
	var downChan chan struct{}

	// Add random jitter to first WebSocket connection to prevent
	// clustering if all agents are started at the same time.
	// SSH connections during hub startup are already staggered.
	var jitter <-chan time.Time
	wsConn := sys.getWsConn()
	if wsConn != nil {
		jitter = getJitter()
		// use the websocket connection's down channel to set the system down
		downChan = wsConn.DownChan
	} else {
		// if the system does not have a websocket connection, wait before updating
		// to allow the agent to connect via websocket (makes sure fingerprint is set).
		if !waitForContext(sys.ctx, 11*time.Second) {
			return
		}

	}

	// update immediately if system is not paused (only for ws connections)
	// we'll wait a minute before connecting via SSH to prioritize ws connections
	if sys.GetStatus() != paused && sys.ctx.Err() == nil {
		if err := sys.update(); err != nil {
			_ = sys.setDown(err)
		}
	}

	sys.updateTicker = time.NewTicker(time.Duration(interval) * time.Millisecond)
	// Go 1.23+ will automatically stop the ticker when the system is garbage collected, however we seem to need this or testing/synctest will block even if calling runtime.GC()
	defer sys.updateTicker.Stop()

	for {
		select {
		case <-sys.ctx.Done():
			return
		case <-sys.updateTicker.C:
			if err := sys.update(); err != nil {
				_ = sys.setDown(err)
			}
		case <-downChan:
			sys.clearWsConn(wsConn)
			downChan = nil
			_ = sys.setDown(nil)
		case <-jitter:
			sys.updateTicker.Reset(time.Duration(interval) * time.Millisecond)
			if err := sys.update(); err != nil {
				_ = sys.setDown(err)
			}
		}
	}
}

// update updates the system data and records.
func (sys *System) update() error {
	sys.updateMu.Lock()
	defer sys.updateMu.Unlock()
	if sys.GetStatus() == paused {
		sys.handlePaused()
		return nil
	}
	options := common.DataRequestOptions{
		CacheTimeMs: uint16(interval),
	}
	// fetch system details if not already fetched
	if !sys.detailsFetched.Load() {
		options.IncludeDetails = true
	}

	data, err := sys.fetchDataFromAgent(options)
	if err != nil {
		return err
	}

	// ensure deprecated fields from older agents are migrated to current fields
	migrateDeprecatedFields(data, !sys.detailsFetched.Load())
	sys.setData(data)

	// create system records
	_, err = sys.createRecords(data)

	// if details were included and fetched successfully, mark details as fetched and update smart interval if set by agent
	if err == nil && data.Details != nil {
		sys.detailsFetched.Store(true)
		// update smart interval if it's set on the agent side
		if smartInterval := data.Details.SmartInterval; smartInterval > 0 {
			sys.mu.Lock()
			sys.smartInterval = smartInterval
			sys.mu.Unlock()
			sys.manager.hub.Logger().Info("SMART interval updated from agent details", "system", sys.Id, "interval", smartInterval.String())
			// make sure we reset expiration of lastFetch to remain as long as the new smart interval
			// to prevent premature expiration leading to new fetch if interval is different.
			sys.manager.smartFetchMap.UpdateExpiration(sys.Id, smartInterval+time.Minute)
		}
		// update zfs interval if it's set on the agent side
		if zfsInterval := data.Details.ZfsInterval; zfsInterval > 0 {
			sys.mu.Lock()
			sys.zfsInterval = zfsInterval
			sys.mu.Unlock()
			sys.manager.hub.Logger().Info("ZFS interval updated from agent details", "system", sys.Id, "interval", zfsInterval.String())
			sys.manager.zfsFetchMap.UpdateExpiration(sys.Id, zfsInterval+time.Minute)
		}
	}

	// Fetch and save SMART devices when system first comes online or at intervals
	if backgroundSmartFetchEnabled() && sys.detailsFetched.Load() {
		sys.mu.Lock()
		if sys.smartInterval <= 0 {
			sys.smartInterval = time.Hour
		}
		sys.mu.Unlock()
		if sys.shouldFetchSmart() && sys.smartFetching.CompareAndSwap(false, true) {
			sys.manager.hub.Logger().Info("SMART fetch", "system", sys.Id, "interval", sys.smartFetchInterval().String())
			go func() {
				defer sys.smartFetching.Store(false)
				_ = sys.FetchAndSaveSmartDevices()
			}()
		}
	}

	// Fetch and save ZFS pool details when system first comes online or at intervals
	if backgroundZfsFetchEnabled() && sys.detailsFetched.Load() && sys.supportsZfsData() {
		sys.mu.Lock()
		if sys.zfsInterval <= 0 {
			sys.zfsInterval = time.Hour
		}
		sys.mu.Unlock()
		if sys.shouldFetchZfs() && sys.zfsFetching.CompareAndSwap(false, true) {
			sys.manager.hub.Logger().Info("ZFS fetch", "system", sys.Id, "interval", sys.zfsFetchInterval().String())
			go func() {
				defer sys.zfsFetching.Store(false)
				_ = sys.FetchAndSaveZfsPools(false)
			}()
		}
	}

	return err
}

func (sys *System) handlePaused() {
	wsConn := sys.getWsConn()
	if wsConn == nil {
		// if the system is paused and there's no websocket connection, remove the system
		sys.manager.removeInstance(sys)
	} else {
		// Send a ping to the agent to keep the connection alive if the system is paused
		if err := wsConn.Ping(); err != nil {
			sys.manager.hub.Logger().Warn("Failed to ping agent", "system", sys.Id, "err", err)
			sys.manager.removeInstance(sys)
		}
	}
}

// createRecords updates the system record and adds system_stats and container_stats records
func (sys *System) createRecords(data *system.CombinedData) (*core.Record, error) {
	sys.recordsMu.Lock()
	defer sys.recordsMu.Unlock()

	systemRecord, err := sys.getRecord(sys.manager.hub)
	if err != nil {
		return nil, err
	}
	hub := sys.manager.hub
	savedMonitorProbes := make(map[string]int64)
	err = hub.RunInTransaction(func(txApp core.App) error {
		// add system_stats record
		systemStatsCollection, err := txApp.FindCachedCollectionByNameOrId("system_stats")
		if err != nil {
			return err
		}
		systemStatsRecord := core.NewRecord(systemStatsCollection)
		systemStatsRecord.Set("system", systemRecord.Id)
		systemStatsRecord.Set("stats", statsRecordData{Stats: data.Stats, DashboardTemp: data.Info.DashboardTemp})
		systemStatsRecord.Set("type", "1m")
		if err := txApp.SaveNoValidate(systemStatsRecord); err != nil {
			return err
		}

		// add containers and container_stats records
		if len(data.Containers) > 0 {
			if data.Containers[0].Id != "" {
				if err := createContainerRecords(txApp, data.Containers, sys.Id); err != nil {
					return err
				}
			}
			containerStatsCollection, err := txApp.FindCachedCollectionByNameOrId("container_stats")
			if err != nil {
				return err
			}
			containerStatsRecord := core.NewRecord(containerStatsCollection)
			containerStatsRecord.Set("system", systemRecord.Id)
			containerStatsRecord.Set("stats", data.Containers)
			containerStatsRecord.Set("type", "1m")
			if err := txApp.SaveNoValidate(containerStatsRecord); err != nil {
				return err
			}
		}

		// Update systemd service records when the agent reports a fresh snapshot.
		// The length check keeps snapshots from older agents working, while the
		// explicit marker lets newer agents report that a fresh snapshot is empty.
		if data.SystemdServicesUpdated || len(data.SystemdServices) > 0 {
			if err := createSystemdStatsRecords(txApp, data.SystemdServices, sys.Id); err != nil {
				return err
			}
		}

		// add system details record
		if data.Details != nil {
			if err := createSystemDetailsRecord(txApp, data.Details, sys.Id); err != nil {
				return err
			}
			// sync display name with hostname if enabled (details are fetched once per agent connection)
			if syncNames, _ := utils.GetEnv("SYNC_SYSTEM_NAMES"); syncNames == "true" && data.Details.Hostname != "" {
				systemRecord.Set("name", data.Details.Hostname)
			}
		}

		if data.Monitors != nil {
			if err := sys.updateNetworkMonitorsRecords(txApp, data.Monitors, savedMonitorProbes); err != nil {
				return err
			}
		}

		if err := sys.syncZfsPoolHealth(txApp, data.Stats.ZfsPools); err != nil {
			return err
		}

		// update system record (do this last because it triggers alerts and we need above records to be inserted first)
		systemRecord.Set("status", up)
		systemRecord.Set(downReasonField, "")
		// Distinguish an idle GPU from a system without GPU data (#2312)
		info := struct {
			system.Info
			GpuPct *float64 `json:"g,omitempty"`
		}{Info: data.Info}
		if len(data.Stats.GPUData) > 0 {
			info.GpuPct = &data.Info.GpuPct
		}
		systemRecord.Set("info", info)
		if err := txApp.SaveNoValidate(systemRecord); err != nil {
			return err
		}
		return nil
	})

	// Publish only successful inserts after the entire transaction commits.
	if err == nil && len(savedMonitorProbes) > 0 {
		if sys.lastSavedMonitorProbe == nil {
			sys.lastSavedMonitorProbe = savedMonitorProbes
		} else {
			for id, timestamp := range savedMonitorProbes {
				sys.lastSavedMonitorProbe[id] = timestamp
			}
		}
	}
	// A non-nil report includes cached results for all remaining monitors.
	if err == nil && data.Monitors != nil {
		for id := range sys.lastSavedMonitorProbe {
			if _, exists := data.Monitors[id]; !exists {
				delete(sys.lastSavedMonitorProbe, id)
			}
		}
	}
	// Feed monitor checks to the status engine. createRecords only handles
	// default-interval results, which are the only ones that drain checks.
	if err == nil && len(data.Monitors) > 0 {
		if engine := hub.Uptime(); engine != nil {
			engine.ObserveResults(sys.Id, data.Monitors, sys.getAgentVersion().LT(beszel.MinVersionMonitorChecks))
		}
	}
	if err == nil {
		if alertErr := hub.HandleNetworkMonitorAlerts(systemRecord, data.Monitors); alertErr != nil {
			hub.Logger().Error("Error handling network monitor alerts", "err", alertErr)
		}
	}
	return systemRecord, err
}

// statsRecordData is the stats JSON of a 1m system_stats record. It adds the
// dashboard temperature (the PRIMARY_SENSOR, or the agent's pick of the
// hottest sensor), so temperature alerts average the same value they trigger
// on. Rollups do not keep it.
type statsRecordData struct {
	system.Stats
	DashboardTemp float64 `json:"dt,omitempty"`
}

func createSystemDetailsRecord(app core.App, data *system.Details, systemId string) error {
	collectionName := "system_details"
	params := dbx.Params{
		"id":       systemId,
		"system":   systemId,
		"hostname": data.Hostname,
		"kernel":   data.Kernel,
		"cores":    data.Cores,
		"threads":  data.Threads,
		"cpu":      data.CpuModel,
		"os":       data.Os,
		"os_name":  data.OsName,
		"arch":     data.Arch,
		"memory":   data.MemoryTotal,
		"podman":   data.Podman,
		"updated":  time.Now().UTC(),
	}
	result, err := app.DB().Update(collectionName, params, dbx.HashExp{"id": systemId}).Execute()
	if err != nil {
		return err
	}
	if rowsAffected, _ := result.RowsAffected(); rowsAffected == 0 {
		_, err = app.DB().Insert(collectionName, params).Execute()
	}
	return err
}

func createSystemdStatsRecords(app core.App, data []*systemd.Service, systemId string) error {
	if len(data) == 0 {
		_, err := app.DB().NewQuery(
			"DELETE FROM systemd_services WHERE system = {:system}",
		).Bind(dbx.Params{"system": systemId}).Execute()
		return err
	}
	// shared params for all records
	params := dbx.Params{
		"system":  systemId,
		"updated": time.Now().UTC().UnixMilli(),
	}

	valueStrings := make([]string, 0, len(data))
	skipped := 0
	for i, service := range data {
		// Agent payloads can contain null entries. Skip them rather than failing
		// the whole stats transaction, which would mark the system down.
		if service == nil {
			skipped++
			continue
		}
		suffix := fmt.Sprintf("%d", i)
		valueStrings = append(valueStrings, fmt.Sprintf("({:id%[1]s}, {:system}, {:name%[1]s}, {:state%[1]s}, {:sub%[1]s}, {:cpu%[1]s}, {:cpuPeak%[1]s}, {:memory%[1]s}, {:memPeak%[1]s}, {:updated})", suffix))
		params["id"+suffix] = MakeStableHashId(systemId, service.Name)
		params["name"+suffix] = service.Name
		params["state"+suffix] = service.State
		params["sub"+suffix] = service.Sub
		params["cpu"+suffix] = service.Cpu
		params["cpuPeak"+suffix] = service.CpuPeak
		params["memory"+suffix] = service.Mem
		params["memPeak"+suffix] = service.MemPeak
	}
	if skipped > 0 {
		app.Logger().Warn("Skipping null systemd services in agent snapshot", "system", systemId, "count", skipped)
	}
	if len(valueStrings) == 0 {
		return nil
	}
	queryString := fmt.Sprintf(
		"INSERT INTO systemd_services (id, system, name, state, sub, cpu, cpuPeak, memory, memPeak, updated) VALUES %s ON CONFLICT(id) DO UPDATE SET system = excluded.system, name = excluded.name, state = excluded.state, sub = excluded.sub, cpu = excluded.cpu, cpuPeak = excluded.cpuPeak, memory = excluded.memory, memPeak = excluded.memPeak, updated = excluded.updated",
		strings.Join(valueStrings, ","),
	)
	if _, err := app.DB().NewQuery(queryString).Bind(params).Execute(); err != nil {
		return err
	}
	// A snapshot with null entries is incomplete: the nulls may stand for
	// services still on the host, so keep existing rows (retention removes
	// stale ones).
	if skipped > 0 {
		return nil
	}
	// Remove services the agent no longer reports. Every row in this batch shares the
	// same updated timestamp, so anything older no longer exists on the host. Left in
	// place these rows survive until the retention sweep and surface inconsistently
	// across the dashboard, the services table, and alerts.
	_, err := app.DB().NewQuery(
		"DELETE FROM systemd_services WHERE system = {:system} AND updated < {:updated}",
	).Bind(dbx.Params{"system": systemId, "updated": params["updated"]}).Execute()
	return err
}

func (sys *System) updateNetworkMonitorsRecords(app core.App, monitorResults map[string]monitor.Result, savedProbes map[string]int64) error {
	return SaveMonitorResults(app, sys.Id, monitorResults, sys.lastSavedMonitorProbe, savedProbes)
}

// SaveMonitorResults stores default-interval monitor results of a system, or
// of the hub when systemID is empty: the result fields of each
// network_monitors record and one 1m network_monitor_stats row per monitor
// with a new probe, whose system is the reporting location. Only results of
// monitors with that location are stored. The record fields of a
// multi-location monitor combine the latest results of its locations (see
// combineLocationResults). lastSaved holds the LastProbeAt of the latest stats row
// saved per monitor (read only; nil for none); the LastProbeAt of each row
// saved now is added to savedProbes, which the caller should merge into
// lastSaved once the surrounding transaction commits. Checks in the results
// are ignored; the caller feeds them to the uptime engine.
func SaveMonitorResults(app core.App, systemID string, monitorResults map[string]monitor.Result, lastSaved, savedProbes map[string]int64) error {
	if len(monitorResults) == 0 {
		return nil
	}
	monitorResults, multi, err := ownedMonitorResults(app, systemID, monitorResults)
	if err != nil || len(monitorResults) == 0 {
		return err
	}
	systemId := systemID
	const monitorCollectionName = "network_monitors"

	// If realtime updates are active, we save via PocketBase records to trigger realtime events.
	// Otherwise we can do a more efficient direct update via SQL
	realtimeActive := utils.RealtimeActiveForCollection(app, monitorCollectionName, func(filterQuery string) bool {
		return !strings.Contains(filterQuery, "system") || strings.Contains(filterQuery, systemId)
	})

	now := time.Now().UTC()
	nowMilli := now.UnixMilli()
	nowString := now.Format(types.DefaultDateLayout)
	var db dbx.Builder
	var updateQuery *dbx.Query
	if !realtimeActive {
		db = app.DB()
		monitorFields := []string{"res", "resMin1h", "resMax1h", "resAvg1h", "loss1h", "updated"}
		setClauses := make([]string, len(monitorFields))
		for i, f := range monitorFields {
			setClauses[i] = fmt.Sprintf("%s={:%s}", f, f)
		}
		// Results omit certInfo unless it changed, so keep the stored value.
		setClauses = append(setClauses, "certInfo=COALESCE({:certInfo}, certInfo)")
		queryString := fmt.Sprintf("UPDATE %s SET %s WHERE id={:id}", monitorCollectionName, strings.Join(setClauses, ", "))
		updateQuery = db.NewQuery(queryString)
	}

	// update network_monitors records
	for id, result := range monitorResults {
		if m, ok := multi[id]; ok {
			result = combineLocationResults(id, monitorloc.FromSystemID(systemID), result, m.locations, m.interval, now)
		}
		monitorData := map[string]any{
			"id":       id,
			"res":      result.AvgResponse,
			"resAvg1h": result.AvgResponse1h,
			"resMin1h": result.MinResponse1h,
			"resMax1h": result.MaxResponse1h,
			"loss1h":   result.PacketLoss1h,
			"updated":  nowString,
		}
		switch realtimeActive {
		case true:
			var record *core.Record
			record, err = app.FindRecordById(monitorCollectionName, id)
			if err == nil {
				if result.Cert != nil {
					monitorData["certInfo"] = result.Cert
				}
				record.Load(monitorData)
				err = app.SaveNoValidate(record)
			}
		default:
			monitorData["certInfo"] = nil
			if result.Cert != nil {
				var cert []byte
				if cert, err = json.Marshal(result.Cert); err == nil {
					monitorData["certInfo"] = string(cert)
				}
			}
			if err == nil {
				_, err = updateQuery.Bind(dbx.Params(monitorData)).Execute()
			}
		}
		if err != nil {
			app.Logger().Warn("Failed to update monitor", "system", systemId, "monitor", id, "err", err)
		}
	}

	// handle stats collection — one record per monitor
	const statsCollectionName = "network_monitor_stats"

	var statsCollection *core.Collection
	if realtimeActive {
		statsCollection, _ = app.FindCachedCollectionByNameOrId(statsCollectionName)
	}

	for monitorId, result := range monitorResults {
		// Compare identity, not ordering, so agent clock changes don't stall writes.
		if result.LastProbeAt == lastSaved[monitorId] {
			continue
		}
		statsRecordData := map[string]any{
			"system":        systemId,
			"monitor":       monitorId,
			"type":          "1m",
			"created":       nowMilli,
			"res_min":       result.MinResponse,
			"res_max":       result.MaxResponse,
			"total_count":   result.TotalCount,
			"success_count": result.SuccessCount,
			"res_sum":       result.ResponseSum,
		}
		switch realtimeActive {
		case true:
			record := core.NewRecord(statsCollection)
			record.Load(statsRecordData)
			err = app.SaveNoValidate(record)
		default:
			statsRecordData["id"] = security.PseudorandomStringWithAlphabet(10, core.DefaultIdAlphabet)
			_, err = db.Insert(statsCollectionName, dbx.Params(statsRecordData)).Execute()
		}
		if err != nil {
			app.Logger().Error("Failed to update monitor stats", "system", systemId, "monitor", monitorId, "err", err)
		} else {
			savedProbes[monitorId] = result.LastProbeAt
		}
	}

	return nil
}

// multiLocationMonitor is what SaveMonitorResults needs to combine the
// results of a multi-location monitor.
type multiLocationMonitor struct {
	locations []string
	interval  time.Duration
}

// ownedMonitorResults returns the results of monitors with systemID (the hub
// when empty) as one of their locations, and the multi-location monitors
// among them. Results for other monitors, which an agent must not write, are
// dropped.
func ownedMonitorResults(app core.App, systemID string, results map[string]monitor.Result) (map[string]monitor.Result, map[string]multiLocationMonitor, error) {
	ids := make([]any, 0, len(results))
	for id := range results {
		ids = append(ids, id)
	}
	records, err := FindLocationMonitors(app, systemID, dbx.In("id", ids...))
	if err != nil {
		return nil, nil, err
	}
	filtered := make(map[string]monitor.Result, len(records))
	multi := map[string]multiLocationMonitor{}
	for _, record := range records {
		filtered[record.Id] = results[record.Id]
		if locations := monitorloc.Of(record); len(locations) > 1 {
			multi[record.Id] = multiLocationMonitor{locations: locations, interval: time.Duration(record.GetInt("interval")) * time.Second}
		}
	}
	for id := range results {
		if _, ok := filtered[id]; !ok {
			app.Logger().Debug("Ignoring result of a monitor of another system", "system", systemID, "monitor", id)
		}
	}
	return filtered, multi, nil
}

// locationResult is the latest result of one location of a monitor.
type locationResult struct {
	result monitor.Result
	at     time.Time
}

// locationResults holds the latest results of the locations of
// multi-location monitors, by monitor and location. It is shared by the hub
// collector and all agents and rebuilt after a restart.
var locationResults = struct {
	sync.Mutex
	monitors map[string]map[string]locationResult
}{monitors: map[string]map[string]locationResult{}}

// combineLocationResults stores the result of a location and returns the
// values of the monitor record, combined from the recent results of all its
// locations: response times are averaged, the one-hour minimum and maximum
// are the extremes, and the one-hour loss is the highest of any location.
// Results older than three intervals (at least three minutes) are left out.
func combineLocationResults(monitorID, location string, result monitor.Result, locations []string, interval time.Duration, now time.Time) monitor.Result {
	maxAge := 3*max(interval, time.Minute) + time.Minute
	locationResults.Lock()
	defer locationResults.Unlock()
	latest := locationResults.monitors[monitorID]
	if latest == nil {
		latest = map[string]locationResult{}
		locationResults.monitors[monitorID] = latest
	}
	latest[location] = locationResult{result: result, at: now}
	for loc, entry := range latest {
		if !slices.Contains(locations, loc) || now.Sub(entry.at) > maxAge {
			delete(latest, loc)
		}
	}
	combined := result
	var avgSum, avg1hSum, avgCount, avg1hCount int64
	for _, entry := range latest {
		r := entry.result
		if r.AvgResponse > 0 {
			avgSum += r.AvgResponse
			avgCount++
		}
		if r.AvgResponse1h > 0 {
			avg1hSum += r.AvgResponse1h
			avg1hCount++
		}
		if r.MinResponse1h > 0 && (combined.MinResponse1h <= 0 || r.MinResponse1h < combined.MinResponse1h) {
			combined.MinResponse1h = r.MinResponse1h
		}
		combined.MaxResponse1h = max(combined.MaxResponse1h, r.MaxResponse1h)
		combined.PacketLoss1h = max(combined.PacketLoss1h, r.PacketLoss1h)
	}
	combined.AvgResponse, combined.AvgResponse1h = 0, 0
	if avgCount > 0 {
		combined.AvgResponse = avgSum / avgCount
	}
	if avg1hCount > 0 {
		combined.AvgResponse1h = avg1hSum / avg1hCount
	}
	return combined
}

// createContainerRecords creates container records
func createContainerRecords(app core.App, data []*container.Stats, systemId string) error {
	if len(data) == 0 {
		return nil
	}
	// shared params for all records
	params := dbx.Params{
		"system":  systemId,
		"updated": time.Now().UTC().UnixMilli(),
	}
	valueStrings := make([]string, 0, len(data))
	for i, container := range data {
		suffix := fmt.Sprintf("%d", i)
		valueStrings = append(valueStrings, fmt.Sprintf("({:id%[1]s}, {:system}, {:name%[1]s}, {:image%[1]s}, {:ports%[1]s}, {:status%[1]s}, {:health%[1]s}, {:cpu%[1]s}, {:memory%[1]s}, {:net%[1]s}, {:updateAvailable%[1]s}, {:updated})", suffix))
		params["id"+suffix] = container.Id
		params["name"+suffix] = container.Name
		params["image"+suffix] = container.Image
		params["ports"+suffix] = container.Ports
		params["status"+suffix] = container.Status
		params["health"+suffix] = container.Health
		params["cpu"+suffix] = container.Cpu
		params["memory"+suffix] = container.Mem
		netBytes := container.Bandwidth[0] + container.Bandwidth[1]
		if netBytes == 0 {
			netBytes = uint64((container.NetworkSent + container.NetworkRecv) * 1024 * 1024)
		}
		params["net"+suffix] = netBytes
		params["updateAvailable"+suffix] = container.UpdateAvailable
	}
	queryString := fmt.Sprintf(
		"INSERT INTO containers (id, system, name, image, ports, status, health, cpu, memory, net, updatable, updated) VALUES %s ON CONFLICT(id) DO UPDATE SET system = excluded.system, name = excluded.name, image = excluded.image, ports = excluded.ports, status = excluded.status, health = excluded.health, cpu = excluded.cpu, memory = excluded.memory, net = excluded.net, updatable = excluded.updatable, updated = excluded.updated",
		strings.Join(valueStrings, ","),
	)
	_, err := app.DB().NewQuery(queryString).Bind(params).Execute()
	return err
}

// getRecord retrieves the system record from the database.
// If the record is not found, it removes the system from the manager. Other
// errors (e.g. a locked database) are returned so the next update can retry.
func (sys *System) getRecord(app core.App) (*core.Record, error) {
	record, err := app.FindRecordById("systems", sys.Id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && record == nil) {
		sys.manager.removeInstance(sys)
		return nil, fmt.Errorf("system record %s not found", sys.Id)
	}
	if err != nil {
		return nil, err
	}
	return record, nil
}

// HasUser checks if the given user is in the system's users list.
// Returns true if SHARE_ALL_SYSTEMS is enabled (any authenticated user can access any system).
func (sys *System) HasUser(app core.App, user *core.Record) bool {
	if user == nil {
		return false
	}
	if v, _ := utils.GetEnv("SHARE_ALL_SYSTEMS"); v == "true" {
		return true
	}
	var recordData = struct {
		Users string
	}{}
	err := app.DB().NewQuery("SELECT users FROM systems WHERE id={:id}").
		Bind(dbx.Params{"id": sys.Id}).
		One(&recordData)
	if err != nil || recordData.Users == "" {
		return false
	}
	return strings.Contains(recordData.Users, user.Id)
}

// setDown marks a system as down in the database.
// It takes the original error that caused the system to go down and returns any error
// encountered during the process of updating the system status.
// It is a no-op if the system's context has been cancelled.
func (sys *System) setDown(originalError error) error {
	if status := sys.GetStatus(); status == down || status == paused {
		return nil
	}
	// the updater can race shutdown, and the app may already be disposed by the
	// time we get here, so don't touch the database once the context is cancelled
	if sys.ctx != nil && sys.ctx.Err() != nil {
		return sys.ctx.Err()
	}
	record, err := sys.getRecord(sys.manager.hub)
	if err != nil {
		return err
	}
	reason := ""
	if originalError != nil {
		sys.manager.hub.Logger().Error("System down", "system", record.GetString("name"), "err", originalError)
		reason = originalError.Error()
		if len(reason) > maxDownReasonLength {
			reason = reason[:maxDownReasonLength]
		}
	}
	sys.detailsFetched.Store(false)
	record.Set("status", down)
	record.Set(downReasonField, reason)
	return sys.manager.hub.SaveNoValidate(record)
}

func (sys *System) getContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if sys.ctx == nil {
		sys.ctx, sys.cancel = context.WithCancel(ctx)
	}
	return sys.ctx, sys.cancel
}

// request sends a request to the agent, trying WebSocket first, then SSH.
// This is the unified request method that uses the transport abstraction.
func (sys *System) request(ctx context.Context, action common.WebSocketAction, req any, dest any) error {
	// Try WebSocket first
	if wsConn := sys.getWsConn(); wsConn != nil && wsConn.IsConnected() {
		wsTransport := transport.NewWebSocketTransport(wsConn)
		if err := wsTransport.Request(ctx, action, req, dest); err == nil {
			return nil
		} else if !shouldFallbackToSSH(err) {
			return err
		} else if shouldCloseWebSocket(err) {
			sys.closeWebSocketConnection()
		}
	}

	// Fall back to SSH if WebSocket fails
	sshTransport, err := sys.ensureSSHTransport()
	if err != nil {
		return err
	}
	err = sshTransport.RequestWithRetry(ctx, action, req, dest, 1)
	// Keep legacy SSH client/version fields in sync for other code paths.
	client := sshTransport.GetClient()
	if previous := sys.client.Swap(client); client != nil && client != previous {
		sys.monitorsNeedSync.Store(true)
	}
	if client != nil {
		sys.setAgentVersion(sshTransport.GetAgentVersion())
	}
	return err
}

func shouldFallbackToSSH(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, gws.ErrConnClosed) {
		return true
	}
	return errors.Is(err, transport.ErrWebSocketNotConnected)
}

func shouldCloseWebSocket(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, gws.ErrConnClosed) || errors.Is(err, transport.ErrWebSocketNotConnected)
}

// ensureSSHTransport returns the system's SSH transport, creating it if needed.
// It fails without a usable SSH configuration (e.g. no hub key), so callers
// never dial with a nil signer.
func (sys *System) ensureSSHTransport() (*transport.SSHTransport, error) {
	sys.sshMu.Lock()
	defer sys.sshMu.Unlock()
	if sys.sshTransport == nil {
		if sys.manager == nil {
			return nil, errNoSSHConfig
		}
		config, err := sys.sshClientConfig()
		if err != nil {
			return nil, err
		}
		sys.sshTransport = transport.NewSSHTransport(transport.SSHTransportConfig{
			Host:    sys.Host,
			Port:    sys.Port,
			Config:  config,
			Timeout: 4 * time.Second,
		})
	}
	// Sync client state with transport
	if client := sys.client.Load(); client != nil {
		sys.sshTransport.SetClient(client)
		sys.sshTransport.SetAgentVersion(sys.getAgentVersion())
	}
	return sys.sshTransport, nil
}

// fetchDataFromAgent attempts to fetch data from the agent, prioritizing WebSocket if available.
// Each fetch decodes into a new struct: CBOR leaves fields the agent omits
// untouched, and real-time and regular updates may fetch concurrently.
func (sys *System) fetchDataFromAgent(options common.DataRequestOptions) (*system.CombinedData, error) {
	if wsConn := sys.getWsConn(); wsConn != nil && wsConn.IsConnected() {
		wsData, err := sys.fetchDataViaWebSocket(options)
		if err == nil {
			sys.syncPendingNetworkMonitors()
			return wsData, nil
		}
		// A slow collection doesn't mean the connection is broken. Closing it
		// would force the agent into a reconnect loop, so only report the error.
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		// close the WebSocket connection if error and try SSH
		sys.closeWebSocketConnection()
	}

	sshData, err := sys.fetchDataViaSSH(options)
	if err != nil {
		return nil, err
	}
	sys.syncPendingNetworkMonitors()
	return sshData, nil
}

// wsDataRequestTimeout bounds how long to wait for stats over WebSocket. Agent
// collection can legitimately take several seconds (e.g. a slow `zpool list`),
// so this must be well above the request manager's 5s default.
var wsDataRequestTimeout = 30 * time.Second

func (sys *System) fetchDataViaWebSocket(options common.DataRequestOptions) (*system.CombinedData, error) {
	wsConn := sys.getWsConn()
	if wsConn == nil || !wsConn.IsConnected() {
		return nil, errors.New("no websocket connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), wsDataRequestTimeout)
	defer cancel()
	wsTransport := transport.NewWebSocketTransport(wsConn)
	data := &system.CombinedData{}
	if err := wsTransport.Request(ctx, common.GetData, options, data); err != nil {
		return nil, err
	}
	return data, nil
}

// FetchContainerInfoFromAgent fetches container info from the agent
func (sys *System) FetchContainerInfoFromAgent(containerID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result string
	err := sys.request(ctx, common.GetContainerInfo, common.ContainerInfoRequest{ContainerID: containerID}, &result)
	return result, err
}

// FetchContainerLogsFromAgent fetches container logs from the agent
func (sys *System) FetchContainerLogsFromAgent(containerID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result string
	err := sys.request(ctx, common.GetContainerLogs, common.ContainerLogsRequest{ContainerID: containerID}, &result)
	return result, err
}

// FetchSystemdInfoFromAgent fetches detailed systemd service information from the agent
func (sys *System) FetchSystemdInfoFromAgent(serviceName string) (systemd.ServiceDetails, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result systemd.ServiceDetails
	err := sys.request(ctx, common.GetSystemdInfo, common.SystemdInfoRequest{ServiceName: serviceName}, &result)
	return result, err
}

// FetchSmartDataFromAgent fetches SMART data from the agent.
func (sys *System) FetchSmartDataFromAgent() (smart.SmartDataResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if sys.getAgentVersion().LT(beszel.MinVersionAgentResponse) {
		var data map[string]smart.SmartData
		err := sys.request(ctx, common.GetSmartData, nil, &data)
		return smart.SmartDataResponse{Data: data}, err
	}
	var result smart.SmartDataResponse
	err := sys.request(ctx, common.GetSmartData, nil, &result)
	return result, err
}

// FetchPackageUpdatesFromAgent fetches the list of pending package updates from the agent.
func (sys *System) FetchPackageUpdatesFromAgent() (system.PackageUpdates, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result system.PackageUpdates
	err := sys.request(ctx, common.GetPackageUpdates, nil, &result)
	return result, err
}

// FetchZfsDataFromAgent fetches ZFS detail data from the agent.
func (sys *System) FetchZfsDataFromAgent(force bool) (*zfs.ZfsData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var result zfs.ZfsData
	err := sys.request(ctx, common.GetZfsData, common.ZfsDataRequest{Force: force}, &result)
	return &result, err
}

func MakeStableHashId(strings ...string) string {
	hash := fnv.New32a()
	for _, str := range strings {
		hash.Write([]byte(str))
	}
	return fmt.Sprintf("%x", hash.Sum32())
}

// fetchDataViaSSH handles fetching data using SSH.
func (sys *System) fetchDataViaSSH(options common.DataRequestOptions) (*system.CombinedData, error) {
	data := &system.CombinedData{}
	err := sys.runSSHOperation(4*time.Second, 1, func(session *ssh.Session) (bool, error) {
		agentVersion := sys.getAgentVersion()
		stdout, err := session.StdoutPipe()
		if err != nil {
			return false, err
		}
		stdin, stdinErr := session.StdinPipe()
		if err := session.Shell(); err != nil {
			return false, err
		}

		// reset in case of retry after a partial decode
		*data = system.CombinedData{}

		if agentVersion.GTE(beszel.MinVersionAgentResponse) && stdinErr == nil {
			req := common.HubRequest[any]{Action: common.GetData, Data: options}
			_ = cbor.NewEncoder(stdin).Encode(req)
			_ = stdin.Close()

			var resp common.AgentResponse
			if decErr := cbor.NewDecoder(stdout).Decode(&resp); decErr == nil && resp.SystemData != nil {
				*data = *resp.SystemData
				if err := session.Wait(); err != nil {
					return false, err
				}
				return false, nil
			}
		}

		var decodeErr error
		if agentVersion.GTE(beszel.MinVersionCbor) {
			decodeErr = cbor.NewDecoder(stdout).Decode(data)
		} else {
			decodeErr = json.NewDecoder(stdout).Decode(data)
		}

		if decodeErr != nil {
			return true, decodeErr
		}

		if err := session.Wait(); err != nil {
			return false, err
		}

		return false, nil
	})
	if err != nil {
		return nil, err
	}

	return data, nil
}

// runSSHOperation establishes an SSH session and executes the provided operation.
// The operation can request a retry by returning true as the first return value.
func (sys *System) runSSHOperation(timeout time.Duration, retries int, operation func(*ssh.Session) (bool, error)) error {
	for attempt := 0; attempt <= retries; attempt++ {
		if sys.client.Load() == nil || sys.GetStatus() == down {
			if err := sys.createSSHClient(); err != nil {
				return err
			}
		}

		session, err := sys.createSessionWithTimeout(timeout)
		if err != nil {
			if attempt >= retries {
				return err
			}
			sys.manager.hub.Logger().Warn("Session closed. Retrying...", "host", sys.Host, "port", sys.Port, "err", err)
			sys.closeSSHConnection()
			continue
		}

		// Bound the whole operation. A half-open TCP connection (a dead peer that
		// never sends RST/FIN) or a wedged agent that accepts the session but
		// never writes a response would otherwise block the read forever. Because
		// StartUpdater runs update() synchronously on its ticker, that stalls the
		// per-system updater indefinitely with no error and no re-dial until the
		// hub is restarted (issue #2041). On timeout we tear down the connection
		// so the blocked read unwinds and the system is re-dialed on the next tick.
		retry, opErr := runWithTimeout(sshOperationTimeout, func() (bool, error) {
			defer session.Close()
			return operation(session)
		}, sys.closeSSHConnection)

		if opErr == nil {
			return nil
		}

		if retry {
			sys.closeSSHConnection()
			if attempt < retries {
				continue
			}
		}

		return opErr
	}

	return fmt.Errorf("ssh operation failed")
}

// sshOperationTimeout bounds a single SSH data exchange (send request, read
// response, wait for the remote command to exit). It is more generous than the
// session-creation timeout to tolerate briefly slow agents, but is kept well
// under the collection interval so a stalled connection is detected and
// re-dialed within one cycle (see issue #2041).
const sshOperationTimeout = 20 * time.Second

// runWithTimeout runs op in a goroutine and returns its result, or, if op does
// not finish within timeout, calls onTimeout (used to tear down the connection
// so a blocked op can unwind) and returns a retryable timeout error. This
// guarantees the caller can never block indefinitely on a dead SSH connection.
func runWithTimeout(timeout time.Duration, op func() (bool, error), onTimeout func()) (retry bool, err error) {
	type opResult struct {
		retry bool
		err   error
	}
	// Buffered so the op goroutine never leaks even when we return on timeout.
	done := make(chan opResult, 1)
	go func() {
		r, e := op()
		done <- opResult{retry: r, err: e}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case res := <-done:
		return res.retry, res.err
	case <-timer.C:
		if onTimeout != nil {
			onTimeout()
		}
		return true, fmt.Errorf("ssh operation timed out after %s", timeout)
	}
}

// createSSHClient creates a new SSH client for the system
func (s *System) createSSHClient() error {
	config, err := s.sshClientConfig()
	if err != nil {
		return err
	}
	network := "tcp"
	host := s.Host
	if strings.HasPrefix(host, "/") {
		network = "unix"
	} else {
		host = net.JoinHostPort(host, s.Port)
	}
	client, err := dialSSHWithKeepAlive(network, host, config)
	s.client.Store(client)
	if err != nil {
		return err
	}
	agentVersion, _ := extractAgentVersion(string(client.Conn.ServerVersion()))
	s.setAgentVersion(agentVersion)
	s.monitorsNeedSync.Store(true)
	s.manager.resetFailedSmartFetchState(s.Id)
	s.manager.resetFailedZfsFetchState(s.Id)
	return nil
}

// sshKeepAliveInterval is the TCP keep-alive idle interval for SSH connections
// to agents. Enabling OS-level keep-alives lets the hub eventually detect a
// dead peer on an otherwise idle connection instead of trusting it forever.
// This is a backstop for genuine network death; an application-level wedge
// (agent process hung while its kernel keeps ACKing) is caught by the
// per-operation timeout in runSSHOperation instead (see issue #2041).
const sshKeepAliveInterval = 30 * time.Second

// dialSSHWithKeepAlive dials an SSH connection like ssh.Dial, but enables TCP
// keep-alive on the underlying connection so half-open connections are
// eventually detected by the operating system.
func dialSSHWithKeepAlive(network, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	dialer := net.Dialer{
		Timeout:   config.Timeout,
		KeepAlive: sshKeepAliveInterval,
	}
	conn, err := dialer.Dial(network, addr)
	if err != nil {
		return nil, err
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// createSessionWithTimeout creates a new SSH session with a timeout to avoid hanging
// in case of network issues
func (sys *System) createSessionWithTimeout(timeout time.Duration) (*ssh.Session, error) {
	client := sys.client.Load()
	if client == nil {
		return nil, fmt.Errorf("client not initialized")
	}

	ctx, cancel := context.WithTimeout(sys.ctx, timeout)
	defer cancel()

	sessionChan := make(chan *ssh.Session, 1)
	errChan := make(chan error, 1)

	go func() {
		if session, err := client.NewSession(); err != nil {
			errChan <- err
		} else {
			sessionChan <- session
		}
	}()

	select {
	case session := <-sessionChan:
		return session, nil
	case err := <-errChan:
		return nil, err
	case <-ctx.Done():
		return nil, fmt.Errorf("timeout")
	}
}

// closeSSHConnection closes the SSH connection but keeps the system in the manager
func (sys *System) closeSSHConnection() {
	sys.sshMu.Lock()
	sshTransport := sys.sshTransport
	sys.sshMu.Unlock()
	if sshTransport != nil {
		sshTransport.Close()
	}
	if client := sys.client.Swap(nil); client != nil {
		client.Close()
	}
}

// closeWebSocketConnection closes the WebSocket connection but keeps the system in the manager
// to allow updating via SSH. It will be removed if the WS connection is re-established.
// The system will be set as down a few seconds later if the connection is not re-established.
func (sys *System) closeWebSocketConnection() {
	if wsConn := sys.getWsConn(); wsConn != nil {
		wsConn.Close(nil)
	}
}

// extractAgentVersion extracts the beszel version from SSH server version string
func extractAgentVersion(versionString string) (semver.Version, error) {
	_, after, _ := strings.Cut(versionString, "_")
	return semver.Parse(after)
}

// getJitter returns a channel that will be triggered after a random delay
// between 51% and 95% of the interval.
// This is used to stagger the initial WebSocket connections to prevent clustering.
func getJitter() <-chan time.Time {
	minPercent := 51
	maxPercent := 95
	jitterRange := maxPercent - minPercent
	msDelay := (interval * minPercent / 100) + rand.Intn(interval*jitterRange/100)
	return time.After(time.Duration(msDelay) * time.Millisecond)
}

// migrateDeprecatedFields moves values from deprecated fields to their new locations if the new
// fields are not already populated. Deprecated fields and refs may be removed at least 30 days
// and one minor version release after the release that includes the migration.
//
// This is run when processing incoming system data from agents, which may be on older versions.
func migrateDeprecatedFields(cd *system.CombinedData, createDetails bool) {
	// migration added 0.19.0
	if cd.Stats.Bandwidth[0] == 0 && cd.Stats.Bandwidth[1] == 0 {
		cd.Stats.Bandwidth[0] = uint64(cd.Stats.NetworkSent * 1024 * 1024)
		cd.Stats.Bandwidth[1] = uint64(cd.Stats.NetworkRecv * 1024 * 1024)
		cd.Stats.NetworkSent, cd.Stats.NetworkRecv = 0, 0
	}
	// migration added 0.19.0
	if cd.Info.BandwidthBytes == 0 {
		cd.Info.BandwidthBytes = uint64(cd.Info.Bandwidth * 1024 * 1024)
		cd.Info.Bandwidth = 0
	}
	// migration added 0.19.0
	if cd.Stats.DiskIO[0] == 0 && cd.Stats.DiskIO[1] == 0 {
		cd.Stats.DiskIO[0] = uint64(cd.Stats.DiskReadPs * 1024 * 1024)
		cd.Stats.DiskIO[1] = uint64(cd.Stats.DiskWritePs * 1024 * 1024)
		cd.Stats.DiskReadPs, cd.Stats.DiskWritePs = 0, 0
	}
	// migration added 0.19.0 - Move deprecated Info fields to Details struct
	if cd.Details == nil && cd.Info.Hostname != "" {
		if createDetails {
			cd.Details = &system.Details{
				Hostname:    cd.Info.Hostname,
				Kernel:      cd.Info.KernelVersion,
				Cores:       cd.Info.Cores,
				Threads:     cd.Info.Threads,
				CpuModel:    cd.Info.CpuModel,
				Podman:      cd.Info.Podman,
				Os:          cd.Info.Os,
				MemoryTotal: uint64(cd.Stats.Mem * 1024 * 1024 * 1024),
			}
		}
		// zero the deprecated fields to prevent saving them in systems.info DB json payload
		cd.Info.Hostname = ""
		cd.Info.KernelVersion = ""
		cd.Info.Cores = 0
		cd.Info.CpuModel = ""
		cd.Info.Podman = false
		cd.Info.Os = 0
	}
}
