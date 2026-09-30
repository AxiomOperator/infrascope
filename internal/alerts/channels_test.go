//go:build testing

package alerts_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/alerts"
	"github.com/henrygd/beszel/internal/hub/monitorsecrets"
	beszelTests "github.com/henrygd/beszel/internal/tests"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbTests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type channelFixture struct {
	hub  *beszelTests.TestHub
	user *core.Record
	am   *alerts.AlertManager
}

func newChannelFixture(t *testing.T) *channelFixture {
	t.Helper()
	hub, user := beszelTests.GetHubWithUser(t)
	t.Cleanup(hub.Cleanup)
	return &channelFixture{hub: hub, user: user, am: alerts.NewTestAlertManagerWithoutWorker(hub)}
}

func (f *channelFixture) channel(t *testing.T, fields map[string]any) *core.Record {
	t.Helper()
	data := map[string]any{"user": f.user.Id, "enabled": true, "minSeverity": "info"}
	for k, v := range fields {
		data[k] = v
	}
	if _, ok := data["name"]; !ok {
		data["name"] = "channel"
	}
	record, err := beszelTests.CreateRecord(f.hub, "notification_channels", data)
	require.NoError(t, err)
	return record
}

func (f *channelFixture) emailChannel(t *testing.T, address string, fields map[string]any) *core.Record {
	t.Helper()
	data := map[string]any{"type": "email", "config": map[string]any{"addresses": []string{address}}, "name": address}
	for k, v := range fields {
		data[k] = v
	}
	return f.channel(t, data)
}

// recipients returns the recipients of the emails sent since reset.
func (f *channelFixture) recipients() []string {
	var to []string
	for _, message := range f.hub.TestMailer.Messages() {
		for _, address := range message.To {
			to = append(to, address.Address)
		}
	}
	slices.Sort(to)
	return to
}

func TestLegacySettingsWithoutChannels(t *testing.T) {
	f := newChannelFixture(t)
	setUserSettings(t, f.hub, f.user.Id, `{"emails":["legacy@example.com"]}`)
	require.NoError(t, f.am.SendAlert(alerts.AlertMessageData{UserID: f.user.Id, Title: "T", Message: "M", Severity: alerts.SeverityInfo}))
	assert.Equal(t, []string{"legacy@example.com"}, f.recipients())

	// Once the user has a channel, legacy settings are ignored.
	f.hub.TestMailer.Reset()
	f.emailChannel(t, "channel@example.com", map[string]any{"isDefault": true})
	require.NoError(t, f.am.SendAlert(alerts.AlertMessageData{UserID: f.user.Id, Title: "T", Message: "M"}))
	assert.Equal(t, []string{"channel@example.com"}, f.recipients())
}

func TestChannelRoutingDelivery(t *testing.T) {
	f := newChannelFixture(t)
	f.emailChannel(t, "info@example.com", map[string]any{"isDefault": true, "minSeverity": "info"})
	f.emailChannel(t, "crit@example.com", map[string]any{"isDefault": true, "minSeverity": "critical"})
	f.emailChannel(t, "off@example.com", map[string]any{"isDefault": true, "enabled": false})
	extra := f.emailChannel(t, "extra@example.com", map[string]any{"isDefault": false, "minSeverity": "critical"})
	disabled := f.emailChannel(t, "disabled@example.com", map[string]any{"enabled": false})
	other, err := beszelTests.CreateUser(f.hub, "other@example.com", "password123")
	require.NoError(t, err)
	foreign, err := beszelTests.CreateRecord(f.hub, "notification_channels", map[string]any{
		"user": other.Id, "name": "theirs", "type": "email", "enabled": true, "isDefault": true,
		"config": map[string]any{"addresses": []string{"theirs@example.com"}},
	})
	require.NoError(t, err)

	for _, tc := range []struct {
		name     string
		severity alerts.Severity
		channels []string
		want     []string
	}{
		{"info", alerts.SeverityInfo, nil, []string{"info@example.com"}},
		{"warning", alerts.SeverityWarning, nil, []string{"info@example.com"}},
		{"critical", alerts.SeverityCritical, nil, []string{"crit@example.com", "info@example.com"}},
		{"explicit", alerts.SeverityInfo, []string{extra.Id}, []string{"extra@example.com"}},
		{"explicit disabled", alerts.SeverityCritical, []string{disabled.Id}, nil},
		{"foreign explicit falls back", alerts.SeverityInfo, []string{foreign.Id}, []string{"info@example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.hub.TestMailer.Reset()
			require.NoError(t, f.am.SendAlert(alerts.AlertMessageData{
				UserID: f.user.Id, Title: "T", Message: "M", Severity: tc.severity, Channels: tc.channels,
			}))
			assert.Equal(t, tc.want, f.recipients())
		})
	}
}

func TestStatusRecoveryFollowsAlertRouting(t *testing.T) {
	f := newChannelFixture(t)
	f.emailChannel(t, "default@example.com", map[string]any{"isDefault": true})
	explicit := f.emailChannel(t, "pager@example.com", map[string]any{"isDefault": false})
	systems, err := beszelTests.CreateSystems(f.hub, 1, f.user.Id, "up")
	require.NoError(t, err)
	alert, err := beszelTests.CreateRecord(f.hub, "alerts", map[string]any{
		"name": "Status", "system": systems[0].Id, "user": f.user.Id, "channels": []string{explicit.Id}, "severity": "info",
	})
	require.NoError(t, err)
	var data alerts.CachedAlertData
	data.PopulateFromRecord(alert)
	assert.Equal(t, alerts.SeverityInfo, data.Severity)

	// Down, a reminder, then up all reach the alert's explicit channel only.
	require.NoError(t, f.am.SetAlertTriggered(data, true))
	history, err := f.hub.FindFirstRecordByFilter("alerts_history", "alert_id={:id}", dbx.Params{"id": alert.Id})
	require.NoError(t, err)
	assert.Equal(t, "info", history.GetString("severity"), "history records the alert's severity")

	setUserSettings(t, f.hub, f.user.Id, `{"reminderMinutes":5}`)
	f.hub.TestMailer.Reset()
	require.NoError(t, f.am.SendAlertRemindersAt(history.GetDateTime("created").Time().Add(6*time.Minute)))
	assert.Equal(t, []string{"pager@example.com"}, f.recipients())
}

func TestHistorySeverityDefaults(t *testing.T) {
	f := newChannelFixture(t)
	systems, err := beszelTests.CreateSystems(f.hub, 1, f.user.Id, "up")
	require.NoError(t, err)
	for name, want := range map[string]string{"Status": "critical", "CPU": "warning"} {
		alert, err := beszelTests.CreateRecord(f.hub, "alerts", map[string]any{"name": name, "system": systems[0].Id, "user": f.user.Id, "value": 10})
		require.NoError(t, err)
		var data alerts.CachedAlertData
		data.PopulateFromRecord(alert)
		require.NoError(t, f.am.SetAlertTriggered(data, true))
		history, err := f.hub.FindFirstRecordByFilter("alerts_history", "alert_id={:id}", dbx.Params{"id": alert.Id})
		require.NoError(t, err)
		assert.Equal(t, want, history.GetString("severity"), name)
	}
}

func TestCriticalBypassesQuietHours(t *testing.T) {
	f := newChannelFixture(t)
	f.emailChannel(t, "me@example.com", map[string]any{"isDefault": true})
	now := time.Now().UTC()
	_, err := beszelTests.CreateRecord(f.hub, "quiet_hours", map[string]any{
		"user": f.user.Id, "type": "one-time", "start": now.Add(-time.Hour), "end": now.Add(time.Hour),
	})
	require.NoError(t, err)
	send := func(severity alerts.Severity) int {
		f.hub.TestMailer.Reset()
		require.NoError(t, f.am.SendAlert(alerts.AlertMessageData{UserID: f.user.Id, Title: "T", Message: "M", Severity: severity}))
		return f.hub.TestMailer.TotalSend()
	}
	assert.Equal(t, 0, send(alerts.SeverityCritical), "quiet hours silence everything by default")
	assert.Equal(t, 0, send(alerts.SeverityWarning))

	setUserSettings(t, f.hub, f.user.Id, `{"criticalBypassQuietHours":true}`)
	assert.Equal(t, 1, send(alerts.SeverityCritical))
	assert.Equal(t, 0, send(alerts.SeverityWarning))
}

func TestBrowserChannelUsesSender(t *testing.T) {
	f := newChannelFixture(t)
	f.channel(t, map[string]any{"type": "browser", "isDefault": true, "minSeverity": "warning"})
	var mu sync.Mutex
	var got []alerts.AlertPushMessage
	var users []string
	f.am.SetBrowserSender(func(_ context.Context, userID string, msg alerts.AlertPushMessage) error {
		mu.Lock()
		defer mu.Unlock()
		users = append(users, userID)
		got = append(got, msg)
		return nil
	})
	require.NoError(t, f.am.SendAlert(alerts.AlertMessageData{UserID: f.user.Id, Title: "info", Message: "M", Severity: alerts.SeverityInfo}))
	require.Empty(t, got, "below the channel threshold")
	require.NoError(t, f.am.SendAlert(alerts.AlertMessageData{
		UserID: f.user.Id, SystemID: "sys1", Title: "down", Message: "body", Link: "http://x/system/sys1",
		Severity: alerts.SeverityCritical, AlertType: "Status",
	}))
	require.Len(t, got, 1)
	assert.Equal(t, []string{f.user.Id}, users)
	assert.Equal(t, alerts.AlertPushMessage{Title: "down", Body: "body", URL: "http://x/system/sys1", Tag: "Status:sys1", Urgent: true}, got[0])

	// Without a sender, browser channels are skipped without failing delivery.
	f.am.SetBrowserSender(nil)
	require.NoError(t, f.am.SendAlert(alerts.AlertMessageData{UserID: f.user.Id, Title: "x", Message: "M", Severity: alerts.SeverityCritical}))
}

func TestChannelTemplates(t *testing.T) {
	f := newChannelFixture(t)
	f.emailChannel(t, "plain@example.com", map[string]any{"isDefault": true})
	f.emailChannel(t, "custom@example.com", map[string]any{"isDefault": true,
		"template": map[string]any{"title": "[{{.Severity}}] {{.Name}}", "body": "{{.Message}} ({{.Status}}) {{.Value}}"}})
	setUserSettings(t, f.hub, f.user.Id, `{"templates":{"title":"G: {{.Title}}"}}`)
	send := func() map[string]string {
		f.hub.TestMailer.Reset()
		require.NoError(t, f.am.SendAlert(alerts.AlertMessageData{
			UserID: f.user.Id, Title: "web down", Message: "Connection lost", Link: "http://link",
			Severity: alerts.SeverityCritical, Name: "web", Value: "down", Status: "triggered",
		}))
		subjects := map[string]string{}
		for _, message := range f.hub.TestMailer.Messages() {
			subjects[message.To[0].Address] = message.Subject + "|" + message.Text
		}
		return subjects
	}
	got := send()
	assert.Equal(t, "G: web down|Connection lost\n\nhttp://link", got["plain@example.com"], "user title template, built-in body")
	assert.Equal(t, "[critical] web|Connection lost (triggered) down", got["custom@example.com"], "channel template wins")

	// A template failing at runtime falls back to the built-in message.
	setUserSettings(t, f.hub, f.user.Id, `{"templates":{"title":"{{.Missing}}"}}`)
	got = send()
	assert.Equal(t, "web down|Connection lost\n\nhttp://link", got["plain@example.com"])
}

func TestChannelURLSealedAndHidden(t *testing.T) {
	f := newChannelFixture(t)
	var delivered atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { delivered.Add(1) }))
	defer server.Close()
	secretURL := "generic+" + server.URL + "/hook?token=s3cret"
	record := f.channel(t, map[string]any{"type": "shoutrrr", "isDefault": true, "config": map[string]any{"url": secretURL}})

	var raw string
	require.NoError(t, f.hub.DB().NewQuery("SELECT config FROM notification_channels WHERE id={:id}").Bind(dbx.Params{"id": record.Id}).Row(&raw))
	assert.Contains(t, raw, monitorsecrets.Prefix)
	assert.NotContains(t, raw, "s3cret")

	// Delivery opens the sealed URL (admins may reach internal addresses).
	f.user.Set("role", "admin")
	require.NoError(t, f.hub.Save(f.user))
	require.NoError(t, f.am.SendAlert(alerts.AlertMessageData{UserID: f.user.Id, Title: "T", Message: "M"}))
	assert.EqualValues(t, 1, delivered.Load())

	ownerToken, err := f.user.NewAuthToken()
	require.NoError(t, err)
	superuser, err := beszelTests.CreateSuperuser(f.hub, "admin@example.com", "password123")
	require.NoError(t, err)
	superToken, err := superuser.NewAuthToken()
	require.NoError(t, err)
	other, err := beszelTests.CreateUser(f.hub, "other@example.com", "password123")
	require.NoError(t, err)
	otherToken, err := other.NewAuthToken()
	require.NoError(t, err)
	url := "/api/collections/notification_channels/records/" + record.Id
	factory := func(testing.TB) *pbTests.TestApp { return f.hub.TestApp }
	scenarios := []beszelTests.ApiScenario{
		{Name: "owner sees plaintext", Method: http.MethodGet, URL: url, Headers: map[string]string{"Authorization": ownerToken},
			ExpectedStatus: 200, ExpectedContent: []string{"s3cret"}, TestAppFactory: factory},
		{Name: "superuser does not", Method: http.MethodGet, URL: url, Headers: map[string]string{"Authorization": superToken},
			ExpectedStatus: 200, ExpectedContent: []string{`"url":""`}, NotExpectedContent: []string{"s3cret", monitorsecrets.Prefix}, TestAppFactory: factory},
		{Name: "other user cannot view", Method: http.MethodGet, URL: url, Headers: map[string]string{"Authorization": otherToken},
			ExpectedStatus: 404, ExpectedContent: []string{"wasn't found"}, TestAppFactory: factory},
		{Name: "sealed value from elsewhere rejected", Method: http.MethodPost, URL: "/api/collections/notification_channels/records",
			Headers:        map[string]string{"Authorization": ownerToken},
			Body:           jsonBody(t, map[string]any{"user": f.user.Id, "name": "x", "type": "shoutrrr", "config": map[string]any{"url": monitorsecrets.Prefix + "abc"}}),
			ExpectedStatus: 400, ExpectedContent: []string{"Invalid notification URL"}, TestAppFactory: factory},
		{Name: "invalid template rejected", Method: http.MethodPost, URL: "/api/collections/notification_channels/records",
			Headers: map[string]string{"Authorization": ownerToken},
			Body: jsonBody(t, map[string]any{"user": f.user.Id, "name": "x", "type": "email", "config": map[string]any{"addresses": []string{"a@example.com"}},
				"template": map[string]any{"body": "{{range 5}}x{{end}}"}}),
			ExpectedStatus: 400, ExpectedContent: []string{"range is not allowed"}, TestAppFactory: factory},
		{Name: "invalid email rejected", Method: http.MethodPost, URL: "/api/collections/notification_channels/records",
			Headers:        map[string]string{"Authorization": ownerToken},
			Body:           jsonBody(t, map[string]any{"user": f.user.Id, "name": "x", "type": "email", "config": map[string]any{"addresses": []string{"nope"}}}),
			ExpectedStatus: 400, ExpectedContent: []string{"Invalid email address"}, TestAppFactory: factory},
		{Name: "invalid settings template rejected", Method: http.MethodPost, URL: "/api/beszel/notification-templates/preview",
			Headers: map[string]string{"Authorization": ownerToken}, Body: jsonBody(t, map[string]any{"title": "{{.Nope}}"}),
			ExpectedStatus: 400, ExpectedContent: []string{"Nope"}, TestAppFactory: factory},
		{Name: "preview renders sample", Method: http.MethodPost, URL: "/api/beszel/notification-templates/preview",
			Headers: map[string]string{"Authorization": ownerToken}, Body: jsonBody(t, map[string]any{"title": "{{upper .Name}}", "body": "{{.Value}}"}),
			ExpectedStatus: 200, ExpectedContent: []string{`"title":"WEB-01"`, `"body":"92.40%"`}, TestAppFactory: factory},
		{Name: "preview requires auth", Method: http.MethodPost, URL: "/api/beszel/notification-templates/preview",
			Body: jsonBody(t, map[string]any{"title": "x"}), ExpectedStatus: 401, ExpectedContent: []string{"requires valid"}, TestAppFactory: factory},
		{Name: "test other user's channel", Method: http.MethodPost, URL: "/api/beszel/notification-channels/" + record.Id + "/test",
			Headers: map[string]string{"Authorization": otherToken}, ExpectedStatus: 404, ExpectedContent: []string{"wasn't found"}, TestAppFactory: factory},
		{Name: "test own channel", Method: http.MethodPost, URL: "/api/beszel/notification-channels/" + record.Id + "/test",
			Headers: map[string]string{"Authorization": ownerToken}, ExpectedStatus: 200, ExpectedContent: []string{`"err":false`}, TestAppFactory: factory,
			AfterTestFunc: func(t testing.TB, _ *pbTests.TestApp, _ *http.Response) { assert.EqualValues(t, 2, delivered.Load()) }},
	}
	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}

func TestSettingsTemplateValidation(t *testing.T) {
	f := newChannelFixture(t)
	settings, err := f.hub.FindFirstRecordByFilter("user_settings", "user={:user}", dbx.Params{"user": f.user.Id})
	require.NoError(t, err)
	token, err := f.user.NewAuthToken()
	require.NoError(t, err)
	factory := func(testing.TB) *pbTests.TestApp { return f.hub.TestApp }
	url := "/api/collections/user_settings/records/" + settings.Id
	for _, scenario := range []beszelTests.ApiScenario{
		{Name: "invalid", Method: http.MethodPatch, URL: url, Headers: map[string]string{"Authorization": token},
			Body:           jsonBody(t, map[string]any{"settings": map[string]any{"templates": map[string]any{"body": "{{.Title"}}}),
			ExpectedStatus: 400, ExpectedContent: []string{"Invalid body template"}, TestAppFactory: factory},
		{Name: "valid", Method: http.MethodPatch, URL: url, Headers: map[string]string{"Authorization": token},
			Body:           jsonBody(t, map[string]any{"settings": map[string]any{"templates": map[string]any{"title": "{{.Title}}"}, "criticalBypassQuietHours": true}}),
			ExpectedStatus: 200, ExpectedContent: []string{`"criticalBypassQuietHours":true`}, TestAppFactory: factory},
		{Name: "bypass must be bool", Method: http.MethodPatch, URL: url, Headers: map[string]string{"Authorization": token},
			Body:           jsonBody(t, map[string]any{"settings": map[string]any{"criticalBypassQuietHours": "yes"}}),
			ExpectedStatus: 400, ExpectedContent: []string{"must be a boolean"}, TestAppFactory: factory},
	} {
		scenario.Test(t)
	}
}

func TestUpsertUserAlertsRouting(t *testing.T) {
	f := newChannelFixture(t)
	mine := f.emailChannel(t, "me@example.com", nil)
	other, err := beszelTests.CreateUser(f.hub, "other@example.com", "password123")
	require.NoError(t, err)
	theirs, err := beszelTests.CreateRecord(f.hub, "notification_channels", map[string]any{
		"user": other.Id, "name": "theirs", "type": "browser", "enabled": true,
	})
	require.NoError(t, err)
	systems, err := beszelTests.CreateSystems(f.hub, 1, f.user.Id, "up")
	require.NoError(t, err)
	token, err := f.user.NewAuthToken()
	require.NoError(t, err)
	factory := func(testing.TB) *pbTests.TestApp { return f.hub.TestApp }
	body := func(channels []string, severity string) *strings.Reader {
		return jsonBody(t, map[string]any{"name": "CPU", "value": 80, "min": 1, "systems": []string{systems[0].Id},
			"overwrite": true, "channels": channels, "severity": severity})
	}
	for _, scenario := range []beszelTests.ApiScenario{
		{Name: "foreign channel", Method: http.MethodPost, URL: "/api/beszel/user-alerts", Headers: map[string]string{"Authorization": token},
			Body: body([]string{theirs.Id}, "critical"), ExpectedStatus: 400, ExpectedContent: []string{"Invalid notification channels"}, TestAppFactory: factory},
		{Name: "bad severity", Method: http.MethodPost, URL: "/api/beszel/user-alerts", Headers: map[string]string{"Authorization": token},
			Body: body(nil, "urgent"), ExpectedStatus: 400, ExpectedContent: []string{"Invalid severity"}, TestAppFactory: factory},
		{Name: "own channel", Method: http.MethodPost, URL: "/api/beszel/user-alerts", Headers: map[string]string{"Authorization": token},
			Body: body([]string{mine.Id}, "critical"), ExpectedStatus: 200, ExpectedContent: []string{"success"}, TestAppFactory: factory,
			AfterTestFunc: func(t testing.TB, _ *pbTests.TestApp, _ *http.Response) {
				alert, err := f.hub.FindFirstRecordByFilter("alerts", "name='CPU' && user={:user}", dbx.Params{"user": f.user.Id})
				require.NoError(t, err)
				assert.Equal(t, "critical", alert.GetString("severity"))
				assert.Equal(t, []string{mine.Id}, alert.GetStringSlice("channels"))
			}},
		{Name: "alerts collection rejects foreign channel", Method: http.MethodPost, URL: "/api/collections/alerts/records",
			Headers:        map[string]string{"Authorization": token},
			Body:           jsonBody(t, map[string]any{"name": "Memory", "system": systems[0].Id, "user": f.user.Id, "channels": []string{theirs.Id}}),
			ExpectedStatus: 400, ExpectedContent: []string{"Invalid notification channels"}, TestAppFactory: factory},
	} {
		scenario.Test(t)
	}
}

func jsonBody(t testing.TB, v any) *strings.Reader {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return strings.NewReader(string(data))
}
