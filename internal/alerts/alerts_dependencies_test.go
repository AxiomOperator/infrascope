//go:build testing

package alerts_test

import (
	"sync/atomic"
	"testing"

	"github.com/henrygd/beszel/internal/alerts"
	beszelTests "github.com/henrygd/beszel/internal/tests"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitorThresholdAlertsHeldWhileParentDown(t *testing.T) {
	env := newMonitorAlertEnv(t)
	mailer := env.hub.TestMailer
	var suppressed atomic.Bool
	env.am.SetDependencyChecks(func(id string) bool { return suppressed.Load() && id == env.hub1.Id }, nil)
	record := env.setThresholds(t, env.hub1, 10, 200)
	sent := mailer.TotalSend()

	// A condition arising while a parent is down is neither recorded nor notified.
	suppressed.Store(true)
	env.saveThresholdResult(t, record, 50, 500, 60)
	assert.Equal(t, sent, mailer.TotalSend())
	assert.Empty(t, env.history(t, record.Id, "MonitorLoss"))
	assert.Empty(t, alertStateOf(t, env, record.Id))

	// Loss cleared meanwhile; latency still holds when the parent recovers: one notification.
	env.saveThresholdResult(t, record, 0, 500, 60)
	suppressed.Store(false)
	env.saveThresholdResult(t, record, 0, 500, 60)
	require.Equal(t, sent+1, mailer.TotalSend())
	assert.Empty(t, env.history(t, record.Id, "MonitorLoss"))
	assert.Equal(t, 1, openCount(env.history(t, record.Id, "MonitorLatency")))
}

// dependencySystem stores a system with a status alert of the user.
func dependencySystem(t *testing.T, hub *beszelTests.TestHub, userID string) (*core.Record, *core.Record) {
	t.Helper()
	setStatusAlertEmail(t, hub, userID, "test@example.com")
	system, err := beszelTests.CreateRecord(hub, "systems", map[string]any{
		"name": "behind-router", "status": "up", "host": "127.0.0.1", "users": []string{userID},
	})
	require.NoError(t, err)
	alert, err := beszelTests.CreateRecord(hub, "alerts", map[string]any{
		"name": "Status", "system": system.Id, "user": userID, "min": 1,
	})
	require.NoError(t, err)
	return system, alert
}

func TestStatusAlertSuppressedWhileParentDown(t *testing.T) {
	hub, user := beszelTests.GetHubWithUser(t)
	defer hub.Cleanup()
	system, alert := dependencySystem(t, hub, user.Id)
	am := alerts.NewTestAlertManagerWithoutWorker(hub)
	var suppressed atomic.Bool
	am.SetDependencyChecks(nil, func(id string) bool { return suppressed.Load() && id == system.Id })
	sent := hub.TestMailer.TotalSend()

	// The delayed down alert is dropped while the parent is down.
	suppressed.Store(true)
	system.Set("status", "down")
	require.NoError(t, am.HandleStatusAlerts("down", system))
	am.ForceExpirePendingAlerts()
	_, err := am.ProcessPendingAlerts()
	require.NoError(t, err)
	assert.Equal(t, sent, hub.TestMailer.TotalSend())
	alert, err = hub.FindRecordById("alerts", alert.Id)
	require.NoError(t, err)
	assert.False(t, alert.GetBool("triggered"))

	// Still suppressed: nothing.
	am.HandleSystemDependencyRecovered(system)
	assert.Equal(t, sent, hub.TestMailer.TotalSend())

	// The parent recovers with the system still down: notified once.
	suppressed.Store(false)
	am.HandleSystemDependencyRecovered(system)
	require.Equal(t, sent+1, hub.TestMailer.TotalSend())
	assert.Contains(t, hub.TestMailer.LastMessage().Subject, "is down")
	am.HandleSystemDependencyRecovered(system)
	assert.Equal(t, sent+1, hub.TestMailer.TotalSend(), "notified once")
	alert, err = hub.FindRecordById("alerts", alert.Id)
	require.NoError(t, err)
	assert.True(t, alert.GetBool("triggered"))
}

func TestStatusAlertRecoveredWhileParentDown(t *testing.T) {
	hub, user := beszelTests.GetHubWithUser(t)
	defer hub.Cleanup()
	system, _ := dependencySystem(t, hub, user.Id)
	am := alerts.NewTestAlertManagerWithoutWorker(hub)
	var suppressed atomic.Bool
	suppressed.Store(true)
	am.SetDependencyChecks(nil, func(string) bool { return suppressed.Load() })
	sent := hub.TestMailer.TotalSend()

	require.NoError(t, am.HandleStatusAlerts("down", system))
	am.ForceExpirePendingAlerts()
	_, err := am.ProcessPendingAlerts()
	require.NoError(t, err)
	// The system recovers before its parent: no down and no up notification.
	system.Set("status", "up")
	require.NoError(t, am.HandleStatusAlerts("up", system))
	suppressed.Store(false)
	am.HandleSystemDependencyRecovered(system)
	assert.Equal(t, sent, hub.TestMailer.TotalSend())
}
