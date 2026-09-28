//go:build testing

package alerts_test

import (
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/alerts"
	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/uptime"
	beszelTests "github.com/henrygd/beszel/internal/tests"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type monitorAlertEnv struct {
	hub          *beszelTests.TestHub
	am           *alerts.AlertManager
	user1, user2 *core.Record
	system       *core.Record
	agent, hub1  *core.Record // agent monitor (system users) and hub monitor (user2)
}

func setUserEmail(t *testing.T, hub *beszelTests.TestHub, user *core.Record, email string) {
	t.Helper()
	settings, err := hub.FindFirstRecordByFilter("user_settings", "user={:user}", dbx.Params{"user": user.Id})
	if err != nil {
		settings, err = beszelTests.CreateRecord(hub, "user_settings", map[string]any{"user": user.Id})
		require.NoError(t, err)
	}
	settings.Set("settings", map[string]any{"emails": []string{email}, "webhooks": []string{}})
	require.NoError(t, hub.Save(settings))
}

func newMonitorAlertEnv(t *testing.T) *monitorAlertEnv {
	t.Helper()
	hub, user1 := beszelTests.GetHubWithUser(t)
	t.Cleanup(hub.Cleanup)
	setUserEmail(t, hub, user1, "u1@example.com")
	user2, err := beszelTests.CreateUser(hub, "u2@example.com", "password")
	require.NoError(t, err)
	setUserEmail(t, hub, user2, "u2@example.com")
	systems, err := beszelTests.CreateSystems(hub, 1, user1.Id, "paused")
	require.NoError(t, err)
	agent, err := beszelTests.CreateRecord(hub, "network_monitors", map[string]any{
		"system": systems[0].Id, "name": "Gateway", "target": "10.0.0.1", "protocol": "icmp", "interval": 60, "enabled": true, "notify": true,
	})
	require.NoError(t, err)
	hubMonitor, err := beszelTests.CreateRecord(hub, "network_monitors", map[string]any{
		"users": []string{user2.Id}, "target": "https://example.com", "protocol": "http", "interval": 60, "enabled": true, "notify": true,
	})
	require.NoError(t, err)
	return &monitorAlertEnv{
		hub: hub, am: alerts.NewTestAlertManagerWithoutWorker(hub),
		user1: user1, user2: user2, system: systems[0], agent: agent, hub1: hubMonitor,
	}
}

func (env *monitorAlertEnv) history(t *testing.T, monitorID, name string) []*core.Record {
	t.Helper()
	records, err := env.hub.FindAllRecords("alerts_history", dbx.HashExp{"alert_id": monitorID, "name": name})
	require.NoError(t, err)
	return records
}

func openCount(records []*core.Record) int {
	count := 0
	for _, record := range records {
		if record.GetDateTime("resolved").IsZero() {
			count++
		}
	}
	return count
}

func downTransition(record *core.Record, name string) uptime.Transition {
	now := time.Now().UTC()
	return uptime.Transition{
		MonitorID: record.Id, SystemID: record.GetString("system"), Name: name, Target: record.GetString("target"),
		Status: uptime.StatusDown, Prev: uptime.StatusUp, At: now, DownSince: now.Add(-time.Minute),
		Err: "connection refused", StatusCode: 503,
	}
}

func upTransition(record *core.Record, name string, outage time.Duration) uptime.Transition {
	now := time.Now().UTC()
	return uptime.Transition{
		MonitorID: record.Id, SystemID: record.GetString("system"), Name: name, Target: record.GetString("target"),
		Status: uptime.StatusUp, Prev: uptime.StatusDown, At: now, DownSince: now.Add(-outage),
	}
}

func TestMonitorTransitionRecipientsAndHistory(t *testing.T) {
	env := newMonitorAlertEnv(t)
	mailer := env.hub.TestMailer
	sent := mailer.TotalSend()

	// Agent monitors notify the system's users.
	env.am.HandleMonitorTransitions([]uptime.Transition{downTransition(env.agent, "Gateway")})
	require.Equal(t, sent+1, mailer.TotalSend())
	message := mailer.LastMessage()
	assert.Equal(t, "u1@example.com", message.To[0].Address)
	assert.Equal(t, "Gateway is down", message.Subject)
	assert.Contains(t, message.Text, "connection refused")
	assert.Contains(t, message.Text, "503")
	assert.Contains(t, message.Text, "/monitors")
	rows := env.history(t, env.agent.Id, "MonitorDown")
	require.Len(t, rows, 1)
	assert.Equal(t, env.user1.Id, rows[0].GetString("user"))
	assert.Equal(t, env.system.Id, rows[0].GetString("system"))
	assert.Equal(t, env.agent.Id, rows[0].GetString("monitor"))
	assert.Equal(t, "Gateway", rows[0].GetString("monitor_name"))

	// A repeated down notification is idempotent.
	env.am.HandleMonitorTransitions([]uptime.Transition{downTransition(env.agent, "Gateway")})
	assert.Equal(t, sent+1, mailer.TotalSend())
	assert.Len(t, env.history(t, env.agent.Id, "MonitorDown"), 1)

	// Hub monitors notify the monitor's users and have no system.
	env.am.HandleMonitorTransitions([]uptime.Transition{downTransition(env.hub1, "https://example.com")})
	require.Equal(t, sent+2, mailer.TotalSend())
	assert.Equal(t, "u2@example.com", mailer.LastMessage().To[0].Address)
	rows = env.history(t, env.hub1.Id, "MonitorDown")
	require.Len(t, rows, 1)
	assert.Equal(t, env.user2.Id, rows[0].GetString("user"))
	assert.Empty(t, rows[0].GetString("system"))
	assert.Equal(t, "https://example.com", rows[0].GetString("monitor_name"))

	// Up resolves the incident and reports the outage duration.
	env.am.HandleMonitorTransitions([]uptime.Transition{upTransition(env.agent, "Gateway", 65*time.Minute)})
	require.Equal(t, sent+3, mailer.TotalSend())
	message = mailer.LastMessage()
	assert.Equal(t, "Gateway is up", message.Subject)
	assert.Contains(t, message.Text, "after 1h 5m")
	rows = env.history(t, env.agent.Id, "MonitorDown")
	require.Len(t, rows, 1)
	assert.Equal(t, 0, openCount(rows))
	// The hub monitor's incident is unaffected.
	assert.Equal(t, 1, openCount(env.history(t, env.hub1.Id, "MonitorDown")))

	// A new outage opens a new row.
	env.am.HandleMonitorTransitions([]uptime.Transition{downTransition(env.agent, "Gateway")})
	assert.Equal(t, sent+4, mailer.TotalSend())
	assert.Len(t, env.history(t, env.agent.Id, "MonitorDown"), 2)
}

func TestMonitorTransitionQuietHours(t *testing.T) {
	env := newMonitorAlertEnv(t)
	mailer := env.hub.TestMailer
	// user1 also receives hub monitor alerts.
	env.hub1.Set("users", []string{env.user1.Id})
	require.NoError(t, env.hub.Save(env.hub1))
	now := time.Now().UTC()

	// A system window silences that system's monitors only.
	window, err := beszelTests.CreateRecord(env.hub, "quiet_hours", map[string]any{
		"user": env.user1.Id, "system": env.system.Id, "type": "one-time", "start": now.Add(-time.Hour), "end": now.Add(time.Hour),
	})
	require.NoError(t, err)
	sent := mailer.TotalSend()
	env.am.HandleMonitorTransitions([]uptime.Transition{downTransition(env.agent, "Gateway")})
	assert.Equal(t, sent, mailer.TotalSend(), "system quiet hours silence agent monitors")
	assert.Equal(t, 1, openCount(env.history(t, env.agent.Id, "MonitorDown")), "silenced alerts are still recorded")
	env.am.HandleMonitorTransitions([]uptime.Transition{downTransition(env.hub1, "Web")})
	assert.Equal(t, sent+1, mailer.TotalSend(), "system quiet hours do not silence hub monitors")

	// A global window silences everything.
	require.NoError(t, env.hub.Delete(window))
	_, err = beszelTests.CreateRecord(env.hub, "quiet_hours", map[string]any{
		"user": env.user1.Id, "type": "one-time", "start": now.Add(-time.Hour), "end": now.Add(time.Hour),
	})
	require.NoError(t, err)
	env.am.HandleMonitorTransitions([]uptime.Transition{
		upTransition(env.agent, "Gateway", time.Minute), upTransition(env.hub1, "Web", time.Minute),
	})
	assert.Equal(t, sent+1, mailer.TotalSend())
	assert.Equal(t, 0, openCount(env.history(t, env.agent.Id, "MonitorDown")))
	assert.Equal(t, 0, openCount(env.history(t, env.hub1.Id, "MonitorDown")))
}

func TestMonitorHistoryIgnoredByAlertHooks(t *testing.T) {
	env := newMonitorAlertEnv(t)
	env.am.HandleMonitorTransitions([]uptime.Transition{downTransition(env.agent, "Gateway")})
	// An alerts record deleted with an id matching the monitor must not resolve monitor history.
	alert, err := beszelTests.CreateRecord(env.hub, "alerts", map[string]any{
		"name": "CPU", "system": env.system.Id, "user": env.user1.Id, "value": 80, "min": 1, "triggered": true,
	})
	require.NoError(t, err)
	require.NoError(t, env.hub.Delete(alert))
	assert.Equal(t, 1, openCount(env.history(t, env.agent.Id, "MonitorDown")))

	// Deleting the monitor resolves its incidents.
	require.NoError(t, env.hub.Delete(env.agent))
	assert.Equal(t, 0, openCount(env.history(t, env.agent.Id, "MonitorDown")))
}

func setCert(t *testing.T, hub *beszelTests.TestHub, record *core.Record, days int, expires time.Time) {
	t.Helper()
	record, err := hub.FindRecordById("network_monitors", record.Id)
	require.NoError(t, err)
	record.Set("certExpiryDays", days)
	record.Set("certInfo", monitor.CertInfo{Expires: expires.UnixMilli(), Issuer: "Test CA"})
	require.NoError(t, hub.SaveNoValidate(record))
}

func TestMonitorCertAlerts(t *testing.T) {
	env := newMonitorAlertEnv(t)
	mailer := env.hub.TestMailer
	now := time.Now()
	sent := mailer.TotalSend()

	// Disabled certificate alerts (certExpiryDays 0) never notify.
	setCert(t, env.hub, env.hub1, 0, now.Add(2*24*time.Hour))
	require.NoError(t, env.am.CheckMonitorCertsAt(now))
	assert.Equal(t, sent, mailer.TotalSend())
	assert.Empty(t, env.history(t, env.hub1.Id, "MonitorCert"))

	// Outside the window nothing happens.
	setCert(t, env.hub, env.hub1, 14, now.Add(30*24*time.Hour))
	require.NoError(t, env.am.CheckMonitorCertsAt(now))
	assert.Equal(t, sent, mailer.TotalSend())

	// Within the window it notifies once per certificate.
	setCert(t, env.hub, env.hub1, 14, now.Add(5*24*time.Hour+time.Hour))
	require.NoError(t, env.am.CheckMonitorCertsAt(now))
	require.Equal(t, sent+1, mailer.TotalSend())
	message := mailer.LastMessage()
	assert.Equal(t, "u2@example.com", message.To[0].Address)
	assert.Equal(t, "Certificate for https://example.com expires in 5 days", message.Subject)
	assert.Contains(t, message.Text, "Test CA")
	rows := env.history(t, env.hub1.Id, "MonitorCert")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 5, rows[0].GetInt("value"))
	assert.Equal(t, env.hub1.Id, rows[0].GetString("monitor"))
	require.NoError(t, env.am.CheckMonitorCertsAt(now.Add(time.Hour)))
	assert.Equal(t, sent+1, mailer.TotalSend())
	assert.Len(t, env.history(t, env.hub1.Id, "MonitorCert"), 1)

	// A renewed certificate resolves the alert silently.
	setCert(t, env.hub, env.hub1, 14, now.Add(90*24*time.Hour))
	require.NoError(t, env.am.CheckMonitorCertsAt(now))
	assert.Equal(t, sent+1, mailer.TotalSend())
	rows = env.history(t, env.hub1.Id, "MonitorCert")
	require.Len(t, rows, 1)
	assert.Equal(t, 0, openCount(rows))

	// An expired certificate on an agent monitor notifies the system users with a negative value.
	setCert(t, env.hub, env.agent, 7, now.Add(-36*time.Hour))
	require.NoError(t, env.am.CheckMonitorCertsAt(now))
	require.Equal(t, sent+2, mailer.TotalSend())
	message = mailer.LastMessage()
	assert.Equal(t, "u1@example.com", message.To[0].Address)
	assert.Equal(t, "Certificate for Gateway has expired", message.Subject)
	rows = env.history(t, env.agent.Id, "MonitorCert")
	require.Len(t, rows, 1)
	assert.LessOrEqual(t, rows[0].GetFloat("value"), 0.0)
	assert.Equal(t, env.system.Id, rows[0].GetString("system"))

	// Turning certificate alerts off resolves open alerts.
	setCert(t, env.hub, env.agent, 0, now.Add(-36*time.Hour))
	require.NoError(t, env.am.CheckMonitorCertsAt(now))
	assert.Equal(t, 0, openCount(env.history(t, env.agent.Id, "MonitorCert")))
	assert.Equal(t, sent+2, mailer.TotalSend())
}

func TestWindowActive(t *testing.T) {
	at := func(hour, minute int) time.Time { return time.Date(2026, 3, 1, hour, minute, 0, 0, time.UTC) }
	// Daily windows keep only their UTC time of day; the stored date is irrelevant.
	daily := func(startH, startM, endH, endM int) (time.Time, time.Time) {
		return time.Date(2025, 7, 4, startH, startM, 0, 0, time.UTC), time.Date(2025, 7, 4, endH, endM, 0, 0, time.UTC)
	}
	for _, tc := range []struct {
		name       string
		windowType string
		start, end time.Time
		now        time.Time
		want       bool
	}{
		{"one-time inside", alerts.WindowOneTime, at(9, 0), at(11, 0), at(10, 0), true},
		{"one-time start inclusive", alerts.WindowOneTime, at(9, 0), at(11, 0), at(9, 0), true},
		{"one-time end exclusive", alerts.WindowOneTime, at(9, 0), at(11, 0), at(11, 0), false},
		{"one-time before", alerts.WindowOneTime, at(9, 0), at(11, 0), at(8, 59), false},
		{"one-time spans days", alerts.WindowOneTime, at(9, 0), at(9, 0).Add(48 * time.Hour), at(9, 0).Add(30 * time.Hour), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, alerts.WindowActive(tc.windowType, tc.start, tc.end, tc.now))
		})
	}
	for _, tc := range []struct {
		name                       string
		startH, startM, endH, endM int
		now                        time.Time
		want                       bool
	}{
		{"daily inside", 9, 0, 17, 0, at(12, 0), true},
		{"daily start inclusive", 9, 0, 17, 0, at(9, 0), true},
		{"daily end exclusive", 9, 0, 17, 0, at(17, 0), false},
		{"daily outside", 9, 0, 17, 0, at(18, 0), false},
		{"daily crossing midnight late", 23, 0, 1, 30, at(23, 30), true},
		{"daily crossing midnight early", 23, 0, 1, 30, at(1, 0), true},
		{"daily crossing midnight outside", 23, 0, 1, 30, at(2, 0), false},
		{"daily equal times never match", 9, 0, 9, 0, at(9, 0), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, end := daily(tc.startH, tc.startM, tc.endH, tc.endM)
			assert.Equal(t, tc.want, alerts.WindowActive(alerts.WindowDaily, start, end, tc.now))
		})
	}

	// UI convention: a daily window 22:00-02:00 chosen in UTC+2 on the creation
	// date is stored as 20:00Z-00:00Z (the stored date's offset applied). It is
	// active at 23:30 local (21:30Z) and not at 03:00 local (01:00Z).
	zone := time.FixedZone("UTC+2", 2*3600)
	start := time.Date(2026, 3, 1, 22, 0, 0, 0, zone).UTC()
	end := time.Date(2026, 3, 1, 2, 0, 0, 0, zone).UTC()
	assert.True(t, alerts.WindowActive(alerts.WindowDaily, start, end, time.Date(2026, 3, 9, 23, 30, 0, 0, zone)))
	assert.True(t, alerts.WindowActive(alerts.WindowDaily, start, end, time.Date(2026, 3, 9, 1, 59, 0, 0, zone)))
	assert.False(t, alerts.WindowActive(alerts.WindowDaily, start, end, time.Date(2026, 3, 9, 3, 0, 0, 0, zone)))
	assert.False(t, alerts.WindowActive(alerts.WindowDaily, start, end, time.Date(2026, 3, 9, 21, 0, 0, 0, zone)))
}
