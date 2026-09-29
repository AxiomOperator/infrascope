package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver"
	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/monitorsecrets"
	"github.com/henrygd/beszel/internal/hub/systems"
	"github.com/henrygd/beszel/internal/hub/utils"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"
)

const (
	pushTokenLength   = 32
	pushTokenAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	// defaultHubMonitorMinInterval is the shortest interval of a hub monitor in
	// seconds, unless HUB_MONITOR_MIN_INTERVAL overrides it.
	defaultHubMonitorMinInterval = 10
)

// monitorServerFields are network_monitors fields written only by the hub.
var monitorServerFields = []string{
	"status", "statusChanged", "lastCheck", "lastError", "lastStatusCode", "recent", "uptime", "state", "certState", "alertState",
	"res", "resAvg1h", "resMin1h", "resMax1h", "loss1h", "certInfo", "updated",
}

// monitorSecretFields are network_monitors fields shown only to users who can edit the monitor.
var monitorSecretFields = []string{"httpSecrets", "pushToken"}

// bindNetworkMonitorsEvents validates monitor records and keeps agent and hub monitor state in sync.
func bindNetworkMonitorsEvents(hub *Hub) {
	hub.OnRecordCreateRequest("network_monitors").BindFunc(func(e *core.RecordRequestEvent) error {
		if err := prepareMonitorRecord(e, nil); err != nil {
			return err
		}
		return e.Next()
	})

	// sync monitor to its runner on creation and persist the first result immediately when available
	hub.OnRecordAfterCreateSuccess("network_monitors").BindFunc(func(e *core.RecordEvent) error {
		err := e.Next()
		if err != nil {
			return err
		}
		hub.uptime.Upsert(e.Record)
		systemID := e.Record.GetString("system")
		if systemID == "" {
			hub.hubMonitors.Sync(e.Record)
			return nil
		}
		if !e.Record.GetBool("enabled") {
			return nil
		}
		// If connected, run the monitor immediately. Paused systems may be absent
		// from the manager; their monitors will sync when they reconnect.
		system, err := hub.sm.GetSystem(systemID)
		if err == nil && system.GetStatus() == "up" {
			// A copy, so the response enrichment cannot race the save.
			go hub.upsertNetworkMonitor(e.Record.Fresh(), true)
		}
		return nil
	})

	// Updates happen in place. When the runner changes (another system, or
	// between a system and the hub) the monitor moves from the old to the new runner.
	hub.OnRecordUpdateRequest("network_monitors").BindFunc(func(e *core.RecordRequestEvent) error {
		original := e.Record.Original()
		if err := prepareMonitorRecord(e, original); err != nil {
			return err
		}
		if err := e.Next(); err != nil {
			return err
		}
		hub.syncUpdatedMonitor(original, e.Record)
		return nil
	})

	// Model-level, so the engine also sees updates made outside the API. It
	// runs after commit and also after the engine's own saves, which Upsert
	// tolerates (it only writes when the record differs from its state).
	hub.OnRecordAfterUpdateSuccess("network_monitors").BindFunc(func(e *core.RecordEvent) error {
		hub.uptime.Upsert(e.Record)
		return e.Next()
	})

	// remove monitor from its runner on delete
	hub.OnRecordAfterDeleteSuccess("network_monitors").BindFunc(func(e *core.RecordEvent) error {
		hub.uptime.Remove(e.Record.Id)
		systemID := e.Record.GetString("system")
		if systemID == "" {
			hub.hubMonitors.Remove(e.Record.Id)
		} else if err := hub.deleteNetworkMonitor(systemID, e.Record.Id); err != nil {
			hub.Logger().Warn("failed to delete monitor on agent", "system", systemID, "monitor", e.Record.Id, "err", err)
		}
		return e.Next()
	})

	// Model-level, so every write path stores secrets sealed.
	sealSecrets := func(e *core.RecordEvent) error {
		if err := hub.sealMonitorSecrets(e.Record); err != nil {
			return err
		}
		return e.Next()
	}
	hub.OnRecordCreate("network_monitors").BindFunc(sealSecrets)
	hub.OnRecordUpdate("network_monitors").BindFunc(func(e *core.RecordEvent) error {
		if err := keepAlertStateFields(e.App, e.Record); err != nil {
			return err
		}
		return sealSecrets(e)
	})

	// Enrich runs for API responses, realtime events and expanded relations.
	hub.OnRecordEnrich("network_monitors").BindFunc(func(e *core.RecordEnrichEvent) error {
		if !canSeeMonitorSecrets(e.App, e.RequestInfo, e.Record) {
			e.Record.Hide(monitorSecretFields...)
			return e.Next()
		}
		// Users who can see secrets receive them in plaintext.
		if raw := e.Record.GetString("httpSecrets"); monitorsecrets.IsSealedJSON(raw) {
			plaintext, err := systems.HTTPSecretsJSON(e.App, e.Record)
			if err != nil {
				e.App.Logger().Error("Failed to decrypt monitor secrets", "monitor", e.Record.Id, "err", err)
				e.Record.Hide("httpSecrets")
			} else {
				e.Record.Set("httpSecrets", types.JSONRaw(plaintext))
			}
		}
		return e.Next()
	})

	checkMonitorAccess := func(e *core.RecordRequestEvent) error {
		if err := checkReferencedMonitors(e); err != nil {
			return err
		}
		return e.Next()
	}
	for _, collection := range []string{"status_pages", "monitor_maintenance"} {
		hub.OnRecordCreateRequest(collection).BindFunc(checkMonitorAccess)
		hub.OnRecordUpdateRequest(collection).BindFunc(checkMonitorAccess)
	}
}

// monitorInputError is a validation error of a submitted monitor, shown to the client.
type monitorInputError string

func (e monitorInputError) Error() string { return string(e) }

// prepareMonitorRecord validates and normalizes a network_monitors record
// submitted through the API. original is nil for new records.
func prepareMonitorRecord(e *core.RecordRequestEvent, original *core.Record) error {
	err := prepareMonitor(e.App, e.Record, original, e.Auth, e.HasSuperuserAuth())
	var inputErr monitorInputError
	if errors.As(err, &inputErr) {
		return e.BadRequestError(inputErr.Error(), nil)
	}
	return err
}

// prepareMonitor validates and normalizes a submitted network_monitors
// record: the API hooks and the importer use it. original is nil for new
// records; auth is the requester (nil for superusers). Invalid input is
// reported as a monitorInputError.
func prepareMonitor(app core.App, record, original *core.Record, auth *core.Record, superuser bool) error {
	isNew := original == nil
	protocol := record.GetString("protocol")
	systemID := record.GetString("system")

	// Clients cannot set result and status fields.
	for _, field := range monitorServerFields {
		if isNew {
			record.Set(field, nil)
		} else {
			record.Set(field, original.Get(field))
		}
	}
	if isNew {
		record.Set("status", "unknown")
	}

	// Sealed secrets are only accepted unchanged (e.g. a client echoing
	// the stored value); clients submit plaintext.
	if raw := record.GetString("httpSecrets"); monitorsecrets.IsSealedJSON(raw) &&
		(isNew || raw != original.GetString("httpSecrets")) {
		return monitorInputError("Invalid http secrets")
	}

	// Clear options the protocol does not use.
	if !monitor.UsesPort(protocol) {
		record.Set("port", 0)
	}
	if protocol != monitor.ProtocolDNS {
		record.Set("server", "")
	}
	if protocol != monitor.ProtocolHTTP {
		record.Set("http", nil)
		if protocol != monitor.ProtocolPostgres && protocol != monitor.ProtocolRedis {
			record.Set("httpSecrets", nil)
		}
	}

	if monitor.IsAgentOnlyProtocol(protocol) && systemID == "" {
		return monitorInputError("Docker monitors require a system")
	}
	if systemID != "" && monitor.IsCheckProtocol(protocol) {
		if version, ok := systemAgentVersion(app, systemID); ok && version.LT(beszel.MinVersionMonitorChecks) {
			return monitorInputError(fmt.Sprintf("%s monitors require agent version %s or newer (the system runs %s)",
				protocol, beszel.MinVersionMonitorChecks, version))
		}
	}

	if protocol == monitor.ProtocolPush {
		if systemID != "" {
			return monitorInputError("Push monitors cannot run on a system")
		}
		record.Set("target", "")
		token := ""
		if !isNew {
			token = original.GetString("pushToken")
		}
		if token == "" {
			token = security.RandomStringWithAlphabet(pushTokenLength, pushTokenAlphabet)
		}
		record.Set("pushToken", token)
	} else {
		record.Set("pushToken", "")
		if strings.TrimSpace(record.GetString("target")) == "" {
			return monitorInputError("Target is required")
		}
	}

	if loss := record.GetFloat("lossThreshold"); math.IsNaN(loss) || loss < 0 || loss >= 100 {
		return monitorInputError("Loss threshold must be at least 0 and below 100")
	}
	if latency := record.GetFloat("latencyThreshold"); math.IsNaN(latency) || latency < 0 {
		return monitorInputError("Latency threshold must be at least 0")
	}

	if systemID == "" {
		if minInterval := hubMonitorMinInterval(); record.GetInt("interval") < minInterval {
			return monitorInputError(fmt.Sprintf("Hub monitors must use an interval of at least %d seconds", minInterval))
		}
		users := record.GetStringSlice("users")
		if len(users) == 0 {
			return monitorInputError("Hub monitors must have at least one user")
		}
		// Rules check the stored users; new, changed or moved hub monitors must keep the requester.
		ownershipChanged := isNew || systemID != original.GetString("system") ||
			!slices.Equal(users, original.GetStringSlice("users"))
		if ownershipChanged && !superuser && (auth == nil || !slices.Contains(users, auth.Id)) {
			return monitorInputError("Hub monitors must include you as a user")
		}
	} else {
		// Access to agent monitors follows the system's users.
		record.Set("users", []string{})
	}

	config, err := systems.MonitorConfigFromRecord(app, record)
	if err == nil && config.Port == 0 && monitor.UsesPort(protocol) {
		config.Port = monitor.DefaultPort(protocol, config.Check)
		record.Set("port", config.Port)
	}
	if err == nil {
		err = config.Validate()
	}
	if err != nil {
		return monitorInputError(err.Error())
	}
	// Store only known options, each in its field, and only those the
	// protocol uses. The model hook seals the secrets before they are written.
	if protocol == monitor.ProtocolHTTP {
		fields, secrets := systems.SplitHTTPOptions(config.HTTP)
		record.Set("http", nilIfZero(fields))
		record.Set("httpSecrets", nilIfZero(secrets))
		record.Set("check", nil)
	} else {
		fields, secrets := systems.SplitCheckOptions(config.Check)
		record.Set("check", nilIfZero(fields))
		if protocol == monitor.ProtocolPostgres || protocol == monitor.ProtocolRedis {
			record.Set("httpSecrets", nilIfZero(secrets))
		}
	}
	return nil
}

// systemAgentVersion returns the agent version a system last reported.
func systemAgentVersion(app core.App, systemID string) (semver.Version, bool) {
	system, err := app.FindRecordById("systems", systemID)
	if err != nil {
		return semver.Version{}, false
	}
	var info struct {
		Version string `json:"v"`
	}
	if err := json.Unmarshal([]byte(system.GetString("info")), &info); err != nil || info.Version == "" {
		return semver.Version{}, false
	}
	version, err := semver.Parse(strings.TrimPrefix(info.Version, "v"))
	return version, err == nil
}

// validPushToken reports whether token has the format of generated push tokens.
func validPushToken(token string) bool {
	if len(token) != pushTokenLength {
		return false
	}
	for _, c := range token {
		if !strings.ContainsRune(pushTokenAlphabet, c) {
			return false
		}
	}
	return true
}

// pushTokenInUse reports whether another monitor has the push token.
func pushTokenInUse(app core.App, token, id string) bool {
	var count int
	err := app.DB().Select("count(*)").From("network_monitors").
		Where(dbx.HashExp{"pushToken": token}).AndWhere(dbx.Not(dbx.HashExp{"id": id})).Row(&count)
	return err != nil || count > 0
}

// alertStateFields are hidden network_monitors fields that only the alert
// manager writes, with direct updates. Record saves keep their stored values.
var alertStateFields = []string{"certState", "alertState"}

// keepAlertStateFields sets the alert state fields of record to their
// stored values, so a save of a record loaded earlier cannot revert them.
func keepAlertStateFields(app core.App, record *core.Record) error {
	var row struct {
		CertState  sql.NullString `db:"certState"`
		AlertState sql.NullString `db:"alertState"`
	}
	err := app.DB().Select(alertStateFields...).From("network_monitors").
		Where(dbx.HashExp{"id": record.Id}).One(&row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for field, value := range map[string]sql.NullString{"certState": row.CertState, "alertState": row.AlertState} {
		if value.Valid && value.String != "" {
			record.Set(field, types.JSONRaw(value.String))
		} else {
			record.Set(field, nil)
		}
	}
	return nil
}

// nilIfZero returns nil for a zero value so empty options are stored as null.
func nilIfZero(value any) any {
	if reflect.ValueOf(value).IsZero() {
		return nil
	}
	return value
}

// hubMonitorMinInterval returns the shortest allowed hub monitor interval in seconds.
func hubMonitorMinInterval() int {
	if value, ok := utils.GetEnv("HUB_MONITOR_MIN_INTERVAL"); ok {
		if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
			return seconds
		}
	}
	return defaultHubMonitorMinInterval
}

// canSeeMonitorSecrets reports whether the requester may see a monitor's
// secret fields: superusers, and non-readonly users who can access the
// monitor through its system or, for hub monitors, its users. Access granted
// only by SHARE_ALL_SYSTEMS does not reveal secrets.
func canSeeMonitorSecrets(app core.App, info *core.RequestInfo, record *core.Record) bool {
	if info == nil || info.Auth == nil {
		return false
	}
	if info.HasSuperuserAuth() {
		return true
	}
	if info.Auth.GetString("role") == "readonly" {
		return false
	}
	systemID := record.GetString("system")
	if systemID == "" {
		return slices.Contains(record.GetStringSlice("users"), info.Auth.Id)
	}
	system, err := app.FindRecordById("systems", systemID)
	return err == nil && slices.Contains(system.GetStringSlice("users"), info.Auth.Id)
}

// checkReferencedMonitors rejects status pages and maintenance windows that
// reference monitors the requester cannot view.
func checkReferencedMonitors(e *core.RecordRequestEvent) error {
	if e.HasSuperuserAuth() {
		return nil
	}
	ids := e.Record.GetStringSlice("monitors")
	if len(ids) == 0 {
		return nil
	}
	collection, err := e.App.FindCachedCollectionByNameOrId("network_monitors")
	if err != nil {
		return err
	}
	info, err := e.RequestInfo()
	if err != nil {
		return err
	}
	monitors, err := e.App.FindRecordsByIds(collection, ids)
	if err != nil {
		return err
	}
	for _, record := range monitors {
		if ok, err := e.App.CanAccessRecord(record, info, collection.ViewRule); err != nil || !ok {
			return e.BadRequestError("You do not have access to all selected monitors", err)
		}
	}
	return nil
}

// syncUpdatedMonitor moves an updated monitor between runners when needed and
// syncs its config to the current runner.
func (h *Hub) syncUpdatedMonitor(original, record *core.Record) {
	oldSystem, newSystem := original.GetString("system"), record.GetString("system")
	if oldSystem != newSystem {
		if oldSystem == "" {
			h.hubMonitors.Remove(record.Id)
		} else if err := h.deleteNetworkMonitor(oldSystem, record.Id); err != nil {
			// The old agent drops the monitor on its next full sync.
			h.Logger().Warn("failed to delete moved monitor on agent", "system", oldSystem, "monitor", record.Id, "err", err)
		}
	}
	if newSystem == "" {
		h.hubMonitors.Sync(record)
		return
	}
	var err error
	if record.GetBool("enabled") {
		// run a monitor that was paused or is new to the agent immediately
		runNow := !original.GetBool("enabled") || oldSystem != newSystem
		err = h.upsertNetworkMonitor(record, runNow)
	} else if oldSystem == newSystem {
		// if the monitor is paused, remove it from the agent
		err = h.deleteNetworkMonitor(newSystem, record.Id)
	}
	if err != nil {
		h.Logger().Warn("failed to sync updated monitor", "system", newSystem, "monitor", record.Id, "err", err)
	}
}

// setMonitorResultFields stores the latest monitor result values on the record.
func setMonitorResultFields(record *core.Record, result monitor.Result) {
	nowString := time.Now().UTC().Format(types.DefaultDateLayout)
	record.Set("res", result.AvgResponse)
	record.Set("resAvg1h", result.AvgResponse1h)
	record.Set("resMin1h", result.MinResponse1h)
	record.Set("resMax1h", result.MaxResponse1h)
	record.Set("loss1h", result.PacketLoss1h)
	if result.Cert != nil {
		record.Set("certInfo", result.Cert)
	}
	record.Set("updated", nowString)
}

// upsertNetworkMonitor creates or updates the record's monitor on the target system. If runNow
// is true, it will also trigger an immediate monitor run and update the record with the result.
func (h *Hub) upsertNetworkMonitor(record *core.Record, runNow bool) error {
	systemID := record.GetString("system")
	system, err := h.sm.GetSystem(systemID)
	if err != nil {
		return err
	}
	config, err := systems.MonitorConfigFromRecord(h, record)
	if err != nil {
		return err
	}
	result, err := system.UpsertNetworkMonitor(config, runNow)
	if err != nil || result == nil {
		return err
	}
	setMonitorResultFields(record, *result)
	return h.App.SaveNoValidate(record)
}

// deleteNetworkMonitor removes a monitor from the given system's agent.
func (h *Hub) deleteNetworkMonitor(systemID, monitorID string) error {
	system, err := h.sm.GetSystem(systemID)
	if err != nil {
		return err
	}
	return system.DeleteNetworkMonitor(monitorID)
}
