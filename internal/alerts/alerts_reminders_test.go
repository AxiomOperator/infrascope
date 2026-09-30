//go:build testing

package alerts_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/alerts"
	beszelTests "github.com/henrygd/beszel/internal/tests"
	pbTests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlertReminders(t *testing.T) {
	f := newAckFixture(t)
	setUserSettings(t, f.hub, f.owner.Id, `{"emails":["owner@example.com"],"reminderMinutes":5}`)
	am := alerts.NewTestAlertManagerWithoutWorker(f.hub)
	require.NoError(t, am.SetAckSecret([]byte("seed")))
	start := f.ownerRow.GetDateTime("created").Time()
	mailer := f.hub.TestMailer
	sent := func() int { return mailer.TotalSend() }

	// Not due before the interval.
	require.NoError(t, am.SendAlertRemindersAt(start.Add(4*time.Minute)))
	assert.Equal(t, 0, sent())

	require.NoError(t, am.SendAlertRemindersAt(start.Add(5*time.Minute)))
	require.Equal(t, 1, sent())
	message := mailer.LastMessage()
	assert.Equal(t, "Reminder: web CPU alert (still active for 5m)", message.Subject)
	assert.Contains(t, message.Text, "Reminder 1 of 24")
	assert.Contains(t, message.Text, "Acknowledge: http://infrascope.test/api/beszel/ack/")
	row := f.reload(t, f.ownerRow)
	assert.Equal(t, 1, row.GetInt("reminderCount"))
	assert.Equal(t, start.Add(5*time.Minute).Unix(), row.GetDateTime("remindedAt").Time().Unix())

	// The interval restarts from the last reminder.
	require.NoError(t, am.SendAlertRemindersAt(start.Add(9*time.Minute)))
	assert.Equal(t, 1, sent())
	require.NoError(t, am.SendAlertRemindersAt(start.Add(10*time.Minute)))
	assert.Equal(t, 2, sent())

	// Rows of users without reminders are left alone.
	assert.Equal(t, 0, f.reload(t, f.readonlyRow).GetInt("reminderCount"))

	// Reminders stop at the cap.
	row = f.reload(t, f.ownerRow)
	row.Set("reminderCount", 23)
	require.NoError(t, f.hub.Save(row))
	require.NoError(t, am.SendAlertRemindersAt(start.Add(15*time.Minute)))
	assert.Equal(t, 3, sent())
	assert.Contains(t, mailer.LastMessage().Text, "Reminder 24 of 24")
	require.NoError(t, am.SendAlertRemindersAt(start.Add(60*time.Minute)))
	assert.Equal(t, 3, sent())
}

func TestAlertRemindersStop(t *testing.T) {
	cases := map[string]func(t *testing.T, f *ackFixture, am *alerts.AlertManager){
		"acknowledged": func(t *testing.T, f *ackFixture, _ *alerts.AlertManager) {
			row := f.reload(t, f.ownerRow)
			row.Set("acknowledgedAt", time.Now())
			row.Set("acknowledgedBy", f.owner.Id)
			require.NoError(t, f.hub.Save(row))
		},
		"resolved": func(t *testing.T, f *ackFixture, _ *alerts.AlertManager) {
			row := f.reload(t, f.ownerRow)
			row.Set("resolved", time.Now())
			require.NoError(t, f.hub.Save(row))
		},
		"quiet hours": func(t *testing.T, f *ackFixture, _ *alerts.AlertManager) {
			_, err := beszelTests.CreateRecord(f.hub, "quiet_hours", map[string]any{
				"user": f.owner.Id, "type": "one-time",
				"start": time.Now().Add(-time.Hour), "end": time.Now().Add(2 * time.Hour),
			})
			require.NoError(t, err)
		},
		"off": func(t *testing.T, f *ackFixture, _ *alerts.AlertManager) {
			setUserSettings(t, f.hub, f.owner.Id, `{"emails":["owner@example.com"],"reminderMinutes":0}`)
		},
		"monitor in maintenance": func(t *testing.T, f *ackFixture, am *alerts.AlertManager) {
			monitorRow(t, f)
			am.SetMaintenanceCheck(func(string, time.Time) bool { return true })
		},
		"monitor behind a down parent": func(t *testing.T, f *ackFixture, am *alerts.AlertManager) {
			monitorRow(t, f)
			am.SetDependencyChecks(func(string) bool { return true }, nil)
		},
		"system behind a down parent": func(t *testing.T, f *ackFixture, am *alerts.AlertManager) {
			row := f.reload(t, f.ownerRow)
			row.Set("name", "Status")
			require.NoError(t, f.hub.Save(row))
			am.SetDependencyChecks(nil, func(string) bool { return true })
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAckFixture(t)
			setUserSettings(t, f.hub, f.owner.Id, `{"emails":["owner@example.com"],"reminderMinutes":5}`)
			am := alerts.NewTestAlertManagerWithoutWorker(f.hub)
			setup(t, f, am)
			require.NoError(t, am.SendAlertRemindersAt(time.Now().Add(10*time.Minute)))
			assert.Equal(t, 0, f.hub.TestMailer.TotalSend())
			assert.Equal(t, 0, f.reload(t, f.ownerRow).GetInt("reminderCount"), "suppressed reminders are not counted")
		})
	}
}

// monitorRow turns the owner's row into a MonitorDown alert of a monitor.
func monitorRow(t *testing.T, f *ackFixture) {
	monitor, err := beszelTests.CreateRecord(f.hub, "network_monitors", map[string]any{
		"name": "API", "protocol": "http", "target": "https://example.com", "users": []string{f.owner.Id}, "interval": 60,
	})
	require.NoError(t, err)
	row := f.reload(t, f.ownerRow)
	row.Set("name", "MonitorDown")
	row.Set("monitor", monitor.Id)
	row.Set("monitor_name", "API")
	require.NoError(t, f.hub.Save(row))
}

func TestReminderSettingsValidation(t *testing.T) {
	f := newAckFixture(t)
	settings, err := f.hub.FindFirstRecordByData("user_settings", "user", f.owner.Id)
	require.NoError(t, err)
	url := "/api/collections/user_settings/records/" + settings.Id
	for minutes, status := range map[string]int{"0": 200, "5": 200, "1440": 200, "3": 400, "1441": 400, "7.5": 400} {
		expected := `"reminderMinutes":` + minutes
		if status == 400 {
			expected = "reminder interval must be 0 (off) or between 5 and 1440 minutes"
		}
		scenario := beszelTests.ApiScenario{
			Name: "reminderMinutes " + minutes, Method: http.MethodPatch, URL: url,
			Body:           jsonReader(map[string]any{"settings": map[string]any{"reminderMinutes": jsonNumber(minutes)}}),
			Headers:        map[string]string{"Authorization": f.ownerTok},
			ExpectedStatus: status, ExpectedContent: []string{expected},
			TestAppFactory: func(testing.TB) *pbTests.TestApp { return f.hub.TestApp },
		}
		scenario.Test(t)
	}
}

func jsonNumber(s string) json.RawMessage { return json.RawMessage(s) }
