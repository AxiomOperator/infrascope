package alerts

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/monitorloc"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// Monitor threshold alerts
//
// Any monitor (hub or agent) can alert on its own one-hour packet loss and
// average response time, configured on the monitor:
//
//   - MonitorLoss: lossThreshold (percent, 0 = off) is exceeded by loss1h.
//   - MonitorLatency: latencyThreshold (ms, 0 = off) is exceeded by resAvg1h.
//
// They are evaluated after each save of monitor results (the hub collector
// and agent updates), from the values stored on the monitor, which combine
// the locations of multi-location monitors. An alert opens when the value exceeds the threshold
// and resolves when it is back at or below it; the value of the history row
// is the measured value (percent or ms). History rows and recipients follow
// the other monitor alerts (see alerts_monitors.go). Whether an alert is
// open is kept in the hidden, server-managed network_monitors.alertState
// field. Disabling a monitor or clearing a threshold resolves its alert
// silently.
//
// Like down alerts, threshold alerts are held during a monitor's maintenance
// window (SetMaintenanceCheck): nothing is opened, resolved or notified. The
// first evaluation after the window notifies a condition that still holds
// once; one that cleared during the window is not notified.
const (
	alertNameMonitorLoss    = "MonitorLoss"
	alertNameMonitorLatency = "MonitorLatency"
)

// monitorAlertState is stored in the hidden network_monitors.alertState field.
type monitorAlertState struct {
	Loss    bool `json:"loss,omitempty"`
	Latency bool `json:"latency,omitempty"`
}

func (s monitorAlertState) active() bool { return s.Loss || s.Latency }

// monitorThreshold describes one threshold alert of a monitor.
type monitorThreshold struct {
	name, field string
	// value returns the measured value, and whether it can be evaluated.
	value func(record *core.Record) (float64, bool)
	// flag returns the state flag of the alert.
	flag func(state *monitorAlertState) *bool
}

var monitorThresholds = []monitorThreshold{
	{
		name: alertNameMonitorLoss, field: "lossThreshold",
		value: func(record *core.Record) (float64, bool) {
			loss := record.GetFloat("loss1h")
			return loss, loss >= 0 && loss <= 100
		},
		flag: func(state *monitorAlertState) *bool { return &state.Loss },
	},
	{
		name: alertNameMonitorLatency, field: "latencyThreshold",
		value: func(record *core.Record) (float64, bool) {
			// resAvg1h is in microseconds; without successful checks it is not positive.
			avg := record.GetFloat("resAvg1h")
			return avg / 1000, avg > 0
		},
		flag: func(state *monitorAlertState) *bool { return &state.Latency },
	},
}

// SetMaintenanceCheck sets the function that reports whether a monitor is in
// a maintenance window. Call it before results are handled.
func (am *AlertManager) SetMaintenanceCheck(fn func(monitorID string, now time.Time) bool) {
	am.inMaintenance = fn
}

// HandleMonitorResults evaluates the threshold alerts of the monitors with
// a system (or the hub, for an empty systemID) as a location, whose results
// from there were just saved. Call it after the saving transaction committed. Errors are logged.
//
// The results predict transitions from cached thresholds and state without
// database work; a predicted transition is rechecked from the stored values
// in a transaction.
func (am *AlertManager) HandleMonitorResults(systemID string, results map[string]monitor.Result) {
	if len(results) == 0 {
		return
	}
	monitors, err := am.monitorThresholds.get(systemID)
	if err != nil {
		am.hub.Logger().Error("Failed to load monitor thresholds", "err", err)
		return
	}
	now := time.Now()
	evaluated := false
	for id, result := range results {
		entry, ok := monitors[id]
		if !ok || !entry.transitionPending(result, now) {
			continue
		}
		// State is held during maintenance and reevaluated after it.
		if am.inMaintenance != nil && am.inMaintenance(id, now) {
			continue
		}
		evaluated = true
		if err := am.evaluateMonitorThresholds(id, result, now); err != nil {
			am.hub.Logger().Error("Failed to evaluate monitor thresholds", "monitor", id, "err", err)
		}
	}
	if evaluated {
		am.monitorThresholds.invalidate(systemID)
	}
}

// evaluateMonitorThresholds opens and resolves the threshold alerts of a
// monitor from its stored one-hour values. result tells whether those values
// cover enough checks to be evaluated.
func (am *AlertManager) evaluateMonitorThresholds(monitorID string, result monitor.Result, now time.Time) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	var messages []AlertMessageData
	err = am.hub.RunInTransaction(func(tx core.App) error {
		messages = nil
		record, err := tx.FindRecordById("network_monitors", monitorID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var state monitorAlertState
		_ = record.UnmarshalJSONField("alertState", &state)
		original := state
		enabled := record.GetBool("enabled")
		ready := enabled && monitorResultReady(result, record.GetInt("interval"), now)
		var target *monitorTarget
		for _, threshold := range monitorThresholds {
			active := threshold.flag(&state)
			limit := record.GetFloat(threshold.field)
			if !enabled || limit <= 0 {
				// Turned off: resolve silently.
				if *active {
					if err := resolveMonitorHistory(tx, monitorID, threshold.name, now); err != nil {
						return err
					}
					*active = false
				}
				continue
			}
			value, ok := threshold.value(record)
			if !ready || !ok {
				continue
			}
			triggered := value > limit
			if triggered == *active {
				continue
			}
			if target == nil {
				loaded, err := loadMonitorTarget(tx, record)
				if err != nil {
					return err
				}
				target = &loaded
			}
			if triggered {
				open, err := openMonitorHistoryUsers(tx, monitorID, threshold.name)
				if err != nil {
					return err
				}
				for _, user := range target.users {
					if open[user] {
						continue
					}
					if err := createMonitorHistory(tx, user, *target, threshold.name, value); err != nil {
						return err
					}
				}
			} else if err := resolveMonitorHistory(tx, monitorID, threshold.name, now); err != nil {
				return err
			}
			for _, user := range target.users {
				messages = append(messages, am.monitorThresholdMessage(user, *target, threshold.name, triggered, value, limit))
			}
			*active = triggered
		}
		if state == original {
			return nil
		}
		return saveMonitorAlertState(tx, monitorID, state)
	})
	if err != nil {
		return err
	}
	am.sendMonitorMessages(messages)
	return nil
}

// saveMonitorAlertState updates only alertState, without hooks, so
// concurrent writes of other fields are kept.
func saveMonitorAlertState(app core.App, monitorID string, state monitorAlertState) error {
	var value any
	if state.active() {
		encoded, err := json.Marshal(state)
		if err != nil {
			return err
		}
		value = string(encoded)
	}
	_, err := app.DB().Update("network_monitors", dbx.Params{"alertState": value}, dbx.HashExp{"id": monitorID}).Execute()
	return err
}

func (am *AlertManager) monitorThresholdMessage(user string, target monitorTarget, name string, triggered bool, value, limit float64) AlertMessageData {
	var title, message string
	comparison := "exceeds"
	if !triggered {
		comparison = "is at or below"
	}
	switch name {
	case alertNameMonitorLoss:
		title = fmt.Sprintf("%s: packet loss above threshold", target.name)
		if !triggered {
			title = fmt.Sprintf("%s: packet loss recovered", target.name)
		}
		message = fmt.Sprintf("Loss of %s over the past hour is %.2f%%, which %s the %.2f%% threshold.", target.label(), value, comparison, limit)
	default:
		title = fmt.Sprintf("%s: response time above threshold", target.name)
		if !triggered {
			title = fmt.Sprintf("%s: response time recovered", target.name)
		}
		message = fmt.Sprintf("Average response time of %s over the past hour is %.0f ms, which %s the %.0f ms threshold.", target.label(), value, comparison, limit)
	}
	return AlertMessageData{
		UserID: user, SystemID: target.systemID,
		Title: title, Message: message,
		Link: am.hub.MakeLink("monitors"), LinkText: "View monitors",
	}
}

// resolveMonitorThresholdsOnUpdate silently resolves the threshold alerts of
// a monitor that was disabled or whose thresholds were cleared.
func (am *AlertManager) resolveMonitorThresholdsOnUpdate(e *core.RecordEvent) error {
	original := e.Record.Original()
	turnedOff := original.GetBool("enabled") && !e.Record.GetBool("enabled")
	for _, threshold := range monitorThresholds {
		if original.GetFloat(threshold.field) > 0 && e.Record.GetFloat(threshold.field) <= 0 {
			turnedOff = true
		}
	}
	if err := e.Next(); err != nil || !turnedOff {
		return err
	}
	defer am.monitorThresholds.invalidate(e.Record.GetString("system"))
	return e.App.RunInTransaction(func(tx core.App) error {
		record, err := tx.FindRecordById("network_monitors", e.Record.Id)
		if err != nil {
			return err
		}
		var state monitorAlertState
		_ = record.UnmarshalJSONField("alertState", &state)
		original := state
		now := time.Now().UTC()
		for _, threshold := range monitorThresholds {
			active := threshold.flag(&state)
			if *active && (!record.GetBool("enabled") || record.GetFloat(threshold.field) <= 0) {
				if err := resolveMonitorHistory(tx, record.Id, threshold.name, now); err != nil {
					return err
				}
				*active = false
			}
		}
		if state == original {
			return nil
		}
		return saveMonitorAlertState(tx, record.Id, state)
	})
}

// monitorThresholdEntry is the cached threshold configuration and alert
// state of an enabled monitor.
type monitorThresholdEntry struct {
	interval      int
	loss, latency float64
	state         monitorAlertState
	// multi is set for monitors with several locations, whose stored values
	// combine all locations and cannot be predicted from one result.
	multi bool
}

// transitionPending reports whether result may open or resolve an alert.
// It does no IO.
func (m monitorThresholdEntry) transitionPending(result monitor.Result, now time.Time) bool {
	if m.loss <= 0 && m.state.Loss || m.latency <= 0 && m.state.Latency {
		return true
	}
	if !monitorResultReady(result, m.interval, now) {
		return false
	}
	if m.multi {
		return true
	}
	if m.loss > 0 && (result.PacketLoss1h > m.loss) != m.state.Loss {
		return true
	}
	if m.latency > 0 && result.AvgResponse1h > 0 && (float64(result.AvgResponse1h)/1000 > m.latency) != m.state.Latency {
		return true
	}
	return false
}

// monitorThresholdCache holds, per location system ("" for the hub), the
// enabled monitors with a threshold or an open threshold alert. Returned
// maps are immutable; changes invalidate the cache.
type monitorThresholdCache struct {
	app     core.App
	mu      sync.Mutex
	systems map[string]map[string]monitorThresholdEntry
}

func newMonitorThresholdCache(app core.App) *monitorThresholdCache {
	c := &monitorThresholdCache{app: app, systems: map[string]map[string]monitorThresholdEntry{}}
	invalidate := func(e *core.RecordEvent) error {
		c.invalidate(e.Record.GetString("system"))
		return e.Next()
	}
	app.OnRecordAfterCreateSuccess("network_monitors").BindFunc(invalidate)
	app.OnRecordAfterDeleteSuccess("network_monitors").BindFunc(invalidate)
	app.OnRecordAfterUpdateSuccess("network_monitors").BindFunc(func(e *core.RecordEvent) error {
		old := e.Record.Original()
		// Result and status saves also invoke this hook; they keep the entry.
		if old.GetString("system") != e.Record.GetString("system") ||
			!slices.Equal(monitorloc.Of(old), monitorloc.Of(e.Record)) ||
			old.GetBool("enabled") != e.Record.GetBool("enabled") ||
			old.GetInt("interval") != e.Record.GetInt("interval") ||
			old.GetFloat("lossThreshold") != e.Record.GetFloat("lossThreshold") ||
			old.GetFloat("latencyThreshold") != e.Record.GetFloat("latencyThreshold") {
			c.invalidate(old.GetString("system"))
		}
		return e.Next()
	})
	return c
}

// invalidate drops the cached monitors. Monitors can have several
// locations, so every location is dropped, not only systemID's.
func (c *monitorThresholdCache) invalidate(systemID string) {
	c.mu.Lock()
	clear(c.systems)
	c.mu.Unlock()
}

func (c *monitorThresholdCache) get(systemID string) (map[string]monitorThresholdEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if monitors, ok := c.systems[systemID]; ok {
		return monitors, nil
	}
	// Loaded under the lock, so an invalidation cannot be overwritten by an
	// older query result.
	records, err := c.app.FindAllRecords("network_monitors", dbx.HashExp{"enabled": true},
		dbx.NewExp("lossThreshold > 0 OR latencyThreshold > 0 OR (alertState IS NOT NULL AND alertState NOT IN ('', 'null', '{}'))"))
	if err != nil {
		return nil, err
	}
	location := monitorloc.FromSystemID(systemID)
	monitors := make(map[string]monitorThresholdEntry, len(records))
	for _, record := range records {
		locations := monitorloc.Of(record)
		if !slices.Contains(locations, location) {
			continue
		}
		entry := monitorThresholdEntry{
			interval: record.GetInt("interval"), loss: record.GetFloat("lossThreshold"),
			latency: record.GetFloat("latencyThreshold"), multi: len(locations) > 1,
		}
		_ = record.UnmarshalJSONField("alertState", &entry.state)
		monitors[record.Id] = entry
	}
	c.systems[systemID] = monitors
	return monitors, nil
}
