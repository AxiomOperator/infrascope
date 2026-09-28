//go:build testing

package hub

import (
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type monitorAlertEnv struct {
	*monitorTestEnv
	t      *testing.T
	mailer interface{ TotalSend() int }
	// clock is the time of the last check; each check is a second later.
	clock time.Time
}

func newMonitorAlertEnv(t *testing.T) *monitorAlertEnv {
	t.Helper()
	env := newMonitorTestEnv(t)
	testApp, ok := env.hub.App.(*pbtests.TestApp)
	require.True(t, ok)
	_, err := createTestRecord(env.hub, "user_settings", map[string]any{
		"user": env.owner.Id, "settings": map[string]any{"emails": []string{"owner@example.com"}, "webhooks": []string{}},
	})
	require.NoError(t, err)
	return &monitorAlertEnv{monitorTestEnv: env, t: t, mailer: testApp.TestMailer, clock: time.Now().Add(-time.Hour)}
}

// hubMonitorRecord creates a hub monitor at model level (no request validation).
func (env *monitorAlertEnv) hubMonitorRecord(notify bool) *core.Record {
	env.t.Helper()
	record, err := createTestRecord(env.hub, "network_monitors", map[string]any{
		"users": []string{env.owner.Id}, "name": "Web", "target": "https://example.com", "protocol": "http",
		"interval": 60, "enabled": true, "notify": notify,
	})
	require.NoError(env.t, err)
	return record
}

// check applies one check and waits for its notifications.
func (env *monitorAlertEnv) check(id string, ok bool) {
	env.t.Helper()
	env.clock = env.clock.Add(time.Second)
	event := monitor.CheckEvent{At: env.clock.UnixMilli(), ResponseUs: 1000}
	if !ok {
		event = monitor.CheckEvent{At: env.clock.UnixMilli(), ResponseUs: -1, Err: "timeout"}
	}
	env.hub.uptime.Observe(id, []monitor.CheckEvent{event})
	env.settle()
}

func (env *monitorAlertEnv) tick() {
	env.t.Helper()
	env.hub.uptime.Tick(time.Now())
	env.settle()
}

func (env *monitorAlertEnv) settle() {
	env.t.Helper()
	require.True(env.t, env.hub.monitorNotices.wait(5*time.Second), "notifications not delivered")
}

func (env *monitorAlertEnv) openDown(id string) int64 {
	env.t.Helper()
	count, err := env.hub.CountRecords("alerts_history", dbx.HashExp{"alert_id": id, "name": "MonitorDown", "resolved": ""})
	require.NoError(env.t, err)
	return count
}

func (env *monitorAlertEnv) maintenanceWindow(windowType string, start, end time.Time, monitors ...string) *core.Record {
	env.t.Helper()
	record, err := createTestRecord(env.hub, "monitor_maintenance", map[string]any{
		"user": env.owner.Id, "title": "Upgrade", "type": windowType, "start": start, "end": end, "monitors": monitors,
	})
	require.NoError(env.t, err)
	return record
}

func TestEngineNotifiesMonitorAlerts(t *testing.T) {
	env := newMonitorAlertEnv(t)
	notify := env.hubMonitorRecord(true)
	silent := env.hubMonitorRecord(false)
	sent := env.mailer.TotalSend()

	env.check(notify.Id, true)
	env.check(silent.Id, true)
	assert.Equal(t, sent, env.mailer.TotalSend(), "a first up is silent")

	env.check(notify.Id, false)
	env.check(notify.Id, false)
	assert.Equal(t, uptime.StatusDown, env.hub.uptime.Status(notify.Id))
	assert.Equal(t, sent+1, env.mailer.TotalSend(), "one notification per outage")
	assert.EqualValues(t, 1, env.openDown(notify.Id))

	env.check(silent.Id, false)
	assert.Equal(t, uptime.StatusDown, env.hub.uptime.Status(silent.Id))
	assert.Equal(t, sent+1, env.mailer.TotalSend(), "notify=false sends nothing")
	assert.EqualValues(t, 0, env.openDown(silent.Id))

	env.check(notify.Id, true)
	assert.Equal(t, sent+2, env.mailer.TotalSend())
	assert.EqualValues(t, 0, env.openDown(notify.Id))
}

func TestMaintenanceSuppressesNotifications(t *testing.T) {
	env := newMonitorAlertEnv(t)
	down := env.hubMonitorRecord(true)
	recovered := env.hubMonitorRecord(true)
	other := env.hubMonitorRecord(true)
	for _, id := range []string{down.Id, recovered.Id, other.Id} {
		env.check(id, true)
	}
	sent := env.mailer.TotalSend()

	// A window created at runtime is picked up by the next tick, for listed monitors only.
	now := time.Now()
	window := env.maintenanceWindow("one-time", now.Add(-time.Hour), now.Add(time.Hour), down.Id, recovered.Id)
	env.tick()
	assert.Equal(t, uptime.StatusMaintenance, env.hub.uptime.Status(down.Id))
	assert.Equal(t, uptime.StatusMaintenance, env.hub.uptime.Status(recovered.Id))
	assert.Equal(t, uptime.StatusUp, env.hub.uptime.Status(other.Id))

	env.check(down.Id, false)
	env.check(down.Id, false)
	env.check(recovered.Id, false)
	env.check(recovered.Id, true)
	assert.Equal(t, uptime.StatusMaintenance, env.hub.uptime.Status(down.Id))
	assert.Equal(t, sent, env.mailer.TotalSend(), "maintenance suppresses notifications")

	// Deleting the window ends maintenance: the still-down monitor notifies once,
	// the one that recovered during maintenance sends nothing.
	require.NoError(t, env.hub.Delete(window))
	env.tick()
	assert.Equal(t, uptime.StatusDown, env.hub.uptime.Status(down.Id))
	assert.Equal(t, uptime.StatusUp, env.hub.uptime.Status(recovered.Id))
	assert.Equal(t, sent+1, env.mailer.TotalSend())
	assert.EqualValues(t, 1, env.openDown(down.Id))
	assert.EqualValues(t, 0, env.openDown(recovered.Id))
	env.tick()
	env.check(down.Id, false)
	assert.Equal(t, sent+1, env.mailer.TotalSend())
}

func TestMaintenanceWindowsActive(t *testing.T) {
	env := newMonitorAlertEnv(t)
	record := env.hubMonitorRecord(true)
	now := time.Now().UTC()
	windows := env.hub.maintenance
	assert.False(t, windows.Active(record.Id, now))

	// Daily windows compare UTC times of day, whatever the stored date.
	day := now.AddDate(0, -2, 0)
	daily := env.maintenanceWindow("daily", day.Add(-time.Hour), day.Add(time.Hour), record.Id)
	assert.True(t, windows.Active(record.Id, now))
	assert.False(t, windows.Active(record.Id, now.Add(3*time.Hour)))
	assert.False(t, windows.Active("other", now))

	// Updates are applied.
	daily.Set("start", day.Add(2*time.Hour))
	daily.Set("end", day.Add(4*time.Hour))
	require.NoError(t, env.hub.Save(daily))
	assert.False(t, windows.Active(record.Id, now))
	assert.True(t, windows.Active(record.Id, now.Add(3*time.Hour)))

	// A fresh cache loads existing windows.
	fresh := &maintenanceWindows{app: env.hub}
	assert.True(t, fresh.Active(record.Id, now.Add(3*time.Hour)))

	require.NoError(t, env.hub.Delete(daily))
	assert.False(t, windows.Active(record.Id, now.Add(3*time.Hour)))
}
