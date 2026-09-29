//go:build testing

package alerts_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	beszelTests "github.com/henrygd/beszel/internal/tests"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// saveThresholdResult stores one-hour values like SaveMonitorResults and
// evaluates them. latencyMs is the average response time in ms.
func (env *monitorAlertEnv) saveThresholdResult(t *testing.T, record *core.Record, loss, latencyMs float64, samples int64) {
	t.Helper()
	avg := int64(latencyMs * 1000)
	_, err := env.hub.DB().Update("network_monitors", dbx.Params{"loss1h": loss, "resAvg1h": avg}, dbx.HashExp{"id": record.Id}).Execute()
	require.NoError(t, err)
	result := monitor.Result{LastProbeAt: time.Now().UnixMilli(), SampleCount: samples, PacketLoss1h: loss, AvgResponse1h: avg}
	env.am.HandleMonitorResults(record.GetString("system"), map[string]monitor.Result{record.Id: result})
}

func (env *monitorAlertEnv) setThresholds(t *testing.T, record *core.Record, loss, latency float64) *core.Record {
	t.Helper()
	fresh, err := env.hub.FindRecordById("network_monitors", record.Id)
	require.NoError(t, err)
	fresh.Set("lossThreshold", loss)
	fresh.Set("latencyThreshold", latency)
	require.NoError(t, env.hub.Save(fresh))
	return fresh
}

func alertStateOf(t *testing.T, env *monitorAlertEnv, id string) string {
	t.Helper()
	var state string
	require.NoError(t, env.hub.DB().NewQuery("SELECT COALESCE(alertState, '') FROM network_monitors WHERE id = {:id}").
		Bind(dbx.Params{"id": id}).Row(&state))
	return state
}

func TestMonitorThresholdAlertsHubMonitor(t *testing.T) {
	env := newMonitorAlertEnv(t)
	mailer := env.hub.TestMailer
	record := env.setThresholds(t, env.hub1, 10, 200)
	sent := mailer.TotalSend()

	env.saveThresholdResult(t, record, 5, 100, 60)
	assert.Equal(t, sent, mailer.TotalSend(), "below both thresholds")
	assert.Empty(t, env.history(t, record.Id, "MonitorLoss"))

	// Too few checks are not evaluated.
	env.saveThresholdResult(t, record, 50, 100, 2)
	assert.Equal(t, sent, mailer.TotalSend(), "warm-up")

	env.saveThresholdResult(t, record, 20, 100, 60)
	require.Equal(t, sent+1, mailer.TotalSend())
	message := mailer.LastMessage()
	assert.Equal(t, "u2@example.com", message.To[0].Address)
	assert.Equal(t, "https://example.com: packet loss above threshold", message.Subject)
	assert.Contains(t, message.Text, "20.00%")
	rows := env.history(t, record.Id, "MonitorLoss")
	require.Len(t, rows, 1)
	assert.Equal(t, env.user2.Id, rows[0].GetString("user"))
	assert.Equal(t, record.Id, rows[0].GetString("monitor"))
	assert.Empty(t, rows[0].GetString("system"))
	assert.EqualValues(t, 20, rows[0].GetFloat("value"))
	assert.JSONEq(t, `{"loss":true}`, alertStateOf(t, env, record.Id))

	// Still above: no repeat.
	env.saveThresholdResult(t, record, 25, 100, 60)
	assert.Equal(t, sent+1, mailer.TotalSend())

	env.saveThresholdResult(t, record, 25, 350, 60)
	require.Equal(t, sent+2, mailer.TotalSend())
	assert.Equal(t, "https://example.com: response time above threshold", mailer.LastMessage().Subject)
	latency := env.history(t, record.Id, "MonitorLatency")
	require.Len(t, latency, 1)
	assert.EqualValues(t, 350, latency[0].GetFloat("value"))

	// At the threshold is back to normal.
	env.saveThresholdResult(t, record, 10, 350, 60)
	require.Equal(t, sent+3, mailer.TotalSend())
	assert.Equal(t, "https://example.com: packet loss recovered", mailer.LastMessage().Subject)
	assert.Equal(t, 0, openCount(env.history(t, record.Id, "MonitorLoss")))
	assert.Equal(t, 1, openCount(env.history(t, record.Id, "MonitorLatency")))
	assert.JSONEq(t, `{"latency":true}`, alertStateOf(t, env, record.Id))

	// A save of a record loaded before still sees the cleared latency threshold resolve silently.
	env.setThresholds(t, record, 10, 0)
	assert.Equal(t, sent+3, mailer.TotalSend())
	assert.Equal(t, 0, openCount(env.history(t, record.Id, "MonitorLatency")))
	assert.Empty(t, alertStateOf(t, env, record.Id))
}

func TestMonitorThresholdAlertsAgentMonitor(t *testing.T) {
	env := newMonitorAlertEnv(t)
	mailer := env.hub.TestMailer
	record := env.setThresholds(t, env.agent, 5, 0)
	sent := mailer.TotalSend()

	// Agent results arrive through HandleNetworkMonitorAlerts.
	avg := int64(1000)
	_, err := env.hub.DB().Update("network_monitors", dbx.Params{"loss1h": 40, "resAvg1h": avg}, dbx.HashExp{"id": record.Id}).Execute()
	require.NoError(t, err)
	system, err := env.hub.FindRecordById("systems", env.system.Id)
	require.NoError(t, err)
	results := map[string]monitor.Result{record.Id: {LastProbeAt: time.Now().UnixMilli(), SampleCount: 60, PacketLoss1h: 40, AvgResponse1h: avg}}
	require.NoError(t, env.am.HandleNetworkMonitorAlerts(system, results))
	require.Equal(t, sent+1, mailer.TotalSend())
	assert.Equal(t, "u1@example.com", mailer.LastMessage().To[0].Address)
	assert.Contains(t, mailer.LastMessage().Text, "Gateway on ")
	rows := env.history(t, record.Id, "MonitorLoss")
	require.Len(t, rows, 1)
	assert.Equal(t, env.system.Id, rows[0].GetString("system"))

	// Results reported by another system are ignored.
	other := env.setThresholds(t, env.hub1, 5, 0)
	env.am.HandleMonitorResults(env.system.Id, map[string]monitor.Result{other.Id: results[record.Id]})
	assert.Empty(t, env.history(t, other.Id, "MonitorLoss"))

	// Disabling the monitor resolves silently.
	record, err = env.hub.FindRecordById("network_monitors", record.Id)
	require.NoError(t, err)
	record.Set("enabled", false)
	require.NoError(t, env.hub.Save(record))
	assert.Equal(t, sent+1, mailer.TotalSend())
	assert.Equal(t, 0, openCount(env.history(t, record.Id, "MonitorLoss")))
	assert.Empty(t, alertStateOf(t, env, record.Id))
}

func TestMonitorThresholdAlertsQuietHoursAndDelete(t *testing.T) {
	env := newMonitorAlertEnv(t)
	mailer := env.hub.TestMailer
	record := env.setThresholds(t, env.hub1, 0, 100)
	now := time.Now().UTC()
	_, err := beszelTests.CreateRecord(env.hub, "quiet_hours", map[string]any{
		"user": env.user2.Id, "type": "one-time", "start": now.Add(-time.Hour), "end": now.Add(time.Hour),
	})
	require.NoError(t, err)
	sent := mailer.TotalSend()
	env.saveThresholdResult(t, record, 0, 500, 60)
	assert.Equal(t, sent, mailer.TotalSend(), "quiet hours silence threshold alerts")
	assert.Equal(t, 1, openCount(env.history(t, record.Id, "MonitorLatency")), "silenced alerts are still recorded")

	require.NoError(t, env.hub.Delete(record))
	assert.Equal(t, 0, openCount(env.history(t, record.Id, "MonitorLatency")))
}

func TestMonitorThresholdAlertsHeldDuringMaintenance(t *testing.T) {
	env := newMonitorAlertEnv(t)
	mailer := env.hub.TestMailer
	var maintenance atomic.Bool
	env.am.SetMaintenanceCheck(func(id string, _ time.Time) bool { return maintenance.Load() && id == env.hub1.Id })
	record := env.setThresholds(t, env.hub1, 10, 200)
	sent := mailer.TotalSend()

	// A condition arising during maintenance is neither recorded nor notified.
	maintenance.Store(true)
	env.saveThresholdResult(t, record, 50, 500, 60)
	assert.Equal(t, sent, mailer.TotalSend())
	assert.Empty(t, env.history(t, record.Id, "MonitorLoss"))
	assert.Empty(t, env.history(t, record.Id, "MonitorLatency"))
	assert.Empty(t, alertStateOf(t, env, record.Id))

	// Loss cleared during the window; latency still holds when it ends: one notification.
	env.saveThresholdResult(t, record, 0, 500, 60)
	maintenance.Store(false)
	env.saveThresholdResult(t, record, 0, 500, 60)
	require.Equal(t, sent+1, mailer.TotalSend())
	assert.Equal(t, "https://example.com: response time above threshold", mailer.LastMessage().Subject)
	assert.Empty(t, env.history(t, record.Id, "MonitorLoss"))
	assert.Equal(t, 1, openCount(env.history(t, record.Id, "MonitorLatency")))
	env.saveThresholdResult(t, record, 0, 500, 60)
	assert.Equal(t, sent+1, mailer.TotalSend(), "notified once")

	// An open alert is not resolved during maintenance either.
	maintenance.Store(true)
	env.saveThresholdResult(t, record, 0, 100, 60)
	assert.Equal(t, sent+1, mailer.TotalSend())
	assert.Equal(t, 1, openCount(env.history(t, record.Id, "MonitorLatency")))
	maintenance.Store(false)
	env.saveThresholdResult(t, record, 0, 100, 60)
	require.Equal(t, sent+2, mailer.TotalSend())
	assert.Equal(t, "https://example.com: response time recovered", mailer.LastMessage().Subject)
	assert.Equal(t, 0, openCount(env.history(t, record.Id, "MonitorLatency")))
}
