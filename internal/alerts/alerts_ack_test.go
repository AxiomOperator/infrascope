//go:build testing

package alerts_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/alerts"
	beszelTests "github.com/henrygd/beszel/internal/tests"
	"github.com/pocketbase/pocketbase/core"
	pbTests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type ackFixture struct {
	hub                   *beszelTests.TestHub
	owner, other, ro      *core.Record
	ownerTok, otherTok    string
	roTok                 string
	system                *core.Record
	ownerRow, readonlyRow *core.Record
}

func newAckFixture(t *testing.T) *ackFixture {
	t.Helper()
	hub, owner := beszelTests.GetHubWithUser(t)
	t.Cleanup(hub.Cleanup)
	f := &ackFixture{hub: hub, owner: owner}
	var err error
	f.other, err = beszelTests.CreateUser(hub, "other@example.com", "password123")
	require.NoError(t, err)
	f.ro, err = beszelTests.CreateUserWithRole(hub, "readonly@example.com", "password123", "readonly")
	require.NoError(t, err)
	f.ownerTok, _ = owner.NewAuthToken()
	f.otherTok, _ = f.other.NewAuthToken()
	f.roTok, _ = f.ro.NewAuthToken()
	f.system, err = beszelTests.CreateRecord(hub, "systems", map[string]any{
		"name": "web", "host": "127.0.0.1", "users": []string{owner.Id, f.other.Id, f.ro.Id},
	})
	require.NoError(t, err)
	f.ownerRow = f.historyRow(t, owner.Id, "CPU")
	f.readonlyRow = f.historyRow(t, f.ro.Id, "CPU")
	settings := hub.Settings()
	settings.Meta.AppURL = "http://infrascope.test"
	require.NoError(t, hub.Save(settings))
	return f
}

func (f *ackFixture) historyRow(t *testing.T, user, name string) *core.Record {
	t.Helper()
	record, err := beszelTests.CreateRecord(f.hub, "alerts_history", map[string]any{
		"user": user, "system": f.system.Id, "name": name, "value": 90, "alert_id": "alert" + name,
	})
	require.NoError(t, err)
	return record
}

func (f *ackFixture) reload(t *testing.T, record *core.Record) *core.Record {
	t.Helper()
	fresh, err := f.hub.FindRecordById("alerts_history", record.Id)
	require.NoError(t, err)
	return fresh
}

func (f *ackFixture) factory(testing.TB) *pbTests.TestApp { return f.hub.TestApp }

func TestAcknowledgeAlertApi(t *testing.T) {
	f := newAckFixture(t)
	ackURL := "/api/beszel/alerts-history/" + f.ownerRow.Id + "/ack"
	unackURL := "/api/beszel/alerts-history/" + f.ownerRow.Id + "/unack"

	scenarios := []beszelTests.ApiScenario{
		{
			Name: "no auth", Method: http.MethodPost, URL: ackURL,
			ExpectedStatus: 401, ExpectedContent: []string{"requires valid"}, TestAppFactory: f.factory,
		},
		{
			Name: "other user", Method: http.MethodPost, URL: ackURL,
			Headers:        map[string]string{"Authorization": f.otherTok},
			ExpectedStatus: 404, ExpectedContent: []string{"Alert not found"}, TestAppFactory: f.factory,
			AfterTestFunc: func(t testing.TB, _ *pbTests.TestApp, _ *http.Response) {
				assert.True(t, f.reload(t.(*testing.T), f.ownerRow).GetDateTime("acknowledgedAt").IsZero())
			},
		},
		{
			Name: "readonly owner", Method: http.MethodPost, URL: "/api/beszel/alerts-history/" + f.readonlyRow.Id + "/ack",
			Headers:        map[string]string{"Authorization": f.roTok},
			ExpectedStatus: 403, ExpectedContent: []string{"not allowed"}, TestAppFactory: f.factory,
		},
		{
			Name: "note too long", Method: http.MethodPost, URL: ackURL,
			Body:           jsonReader(map[string]any{"note": strings.Repeat("x", 1001)}),
			Headers:        map[string]string{"Authorization": f.ownerTok},
			ExpectedStatus: 400, ExpectedContent: []string{"at most 1000"}, TestAppFactory: f.factory,
		},
		{
			Name: "owner acknowledges with note", Method: http.MethodPost, URL: ackURL,
			Body:           jsonReader(map[string]any{"note": "  looking into it  "}),
			Headers:        map[string]string{"Authorization": f.ownerTok},
			ExpectedStatus: 200, ExpectedContent: []string{`"ackNote":"looking into it"`, f.owner.Id}, TestAppFactory: f.factory,
			AfterTestFunc: func(t testing.TB, _ *pbTests.TestApp, _ *http.Response) {
				row := f.reload(t.(*testing.T), f.ownerRow)
				assert.False(t, row.GetDateTime("acknowledgedAt").IsZero())
				assert.Equal(t, f.owner.Id, row.GetString("acknowledgedBy"))
				assert.Equal(t, "looking into it", row.GetString("ackNote"))
			},
		},
		{
			Name: "owner acknowledges again without body", Method: http.MethodPost, URL: ackURL,
			Headers:        map[string]string{"Authorization": f.ownerTok},
			ExpectedStatus: 200, ExpectedContent: []string{`"ackNote":"looking into it"`}, TestAppFactory: f.factory,
		},
		{
			Name: "other user cannot unacknowledge", Method: http.MethodPost, URL: unackURL,
			Headers:        map[string]string{"Authorization": f.otherTok},
			ExpectedStatus: 404, ExpectedContent: []string{"Alert not found"}, TestAppFactory: f.factory,
		},
		{
			Name: "owner unacknowledges", Method: http.MethodPost, URL: unackURL,
			Headers:        map[string]string{"Authorization": f.ownerTok},
			ExpectedStatus: 200, ExpectedContent: []string{`"acknowledgedAt":null`}, TestAppFactory: f.factory,
			AfterTestFunc: func(t testing.TB, _ *pbTests.TestApp, _ *http.Response) {
				row := f.reload(t.(*testing.T), f.ownerRow)
				assert.True(t, row.GetDateTime("acknowledgedAt").IsZero())
				assert.Empty(t, row.GetString("acknowledgedBy"))
				assert.Empty(t, row.GetString("ackNote"))
			},
		},
		{
			Name: "history rows cannot be updated directly", Method: http.MethodPatch,
			URL:            "/api/collections/alerts_history/records/" + f.ownerRow.Id,
			Body:           jsonReader(map[string]any{"acknowledgedBy": f.owner.Id, "reminderCount": 0}),
			Headers:        map[string]string{"Authorization": f.ownerTok},
			ExpectedStatus: 403, ExpectedContent: []string{"superusers"}, TestAppFactory: f.factory,
		},
	}
	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}

func TestAckLink(t *testing.T) {
	f := newAckFixture(t)
	// The link key is derived from the hub key in the data dir.
	_, err := f.hub.GetSSHKey("")
	require.NoError(t, err)
	am := f.hub.Hub.AlertManager

	valid, err := am.AckToken(f.ownerRow.Id, f.owner.Id, time.Now().Add(time.Hour))
	require.NoError(t, err)
	expired, err := am.AckToken(f.ownerRow.Id, f.owner.Id, time.Now().Add(-time.Minute))
	require.NoError(t, err)
	readonly, err := am.AckToken(f.readonlyRow.Id, f.ro.Id, time.Now().Add(time.Hour))
	require.NoError(t, err)
	// A token for another user's row is refused even when signed.
	crossUser, err := am.AckToken(f.ownerRow.Id, f.other.Id, time.Now().Add(time.Hour))
	require.NoError(t, err)
	tampered := strings.Replace(valid, f.ownerRow.Id, f.readonlyRow.Id, 1)

	noStore := func(t testing.TB, res *http.Response) {
		assert.Equal(t, "no-store", res.Header.Get("Cache-Control"))
		assert.Equal(t, "no-referrer", res.Header.Get("Referrer-Policy"))
	}
	notAcknowledged := func(t testing.TB, _ *pbTests.TestApp, res *http.Response) {
		noStore(t, res)
		assert.True(t, f.reload(t.(*testing.T), f.ownerRow).GetDateTime("acknowledgedAt").IsZero())
	}
	invalid := func(method, name, token string) beszelTests.ApiScenario {
		return beszelTests.ApiScenario{
			Name: method + " " + name, Method: method, URL: "/api/beszel/ack/" + token,
			ExpectedStatus: 400, ExpectedContent: []string{"invalid or has expired"},
			NotExpectedContent: []string{"<script"}, TestAppFactory: f.factory,
			AfterTestFunc: notAcknowledged,
		}
	}
	confirm := beszelTests.ApiScenario{
		Name: "GET shows a confirmation page and acknowledges nothing", Method: http.MethodGet, URL: "/api/beszel/ack/" + valid,
		ExpectedStatus:     200,
		ExpectedContent:    []string{"&lt;b&gt;web&lt;/b&gt; CPU alert", `<form method="post">`, "Acknowledge</button>"},
		NotExpectedContent: []string{"<b>web</b>", valid},
		TestAppFactory:     f.factory,
		AfterTestFunc:      notAcknowledged,
	}
	var firstAck time.Time
	post := func(name string) beszelTests.ApiScenario {
		return beszelTests.ApiScenario{
			Name: "POST " + name, Method: http.MethodPost, URL: "/api/beszel/ack/" + valid,
			ExpectedStatus: 303, TestAppFactory: f.factory,
			AfterTestFunc: func(t testing.TB, _ *pbTests.TestApp, res *http.Response) {
				noStore(t, res)
				assert.Equal(t, "http://infrascope.test/system/"+f.system.Id+"?ack=1", res.Header.Get("Location"))
				row := f.reload(t.(*testing.T), f.ownerRow)
				at := row.GetDateTime("acknowledgedAt").Time()
				assert.False(t, at.IsZero())
				assert.Equal(t, f.owner.Id, row.GetString("acknowledgedBy"))
				// Replays keep the first acknowledgement.
				if firstAck.IsZero() {
					firstAck = at
				}
				assert.Equal(t, firstAck, at)
			},
		}
	}
	// The title is escaped, never taken from the request.
	f.system.Set("name", "<b>web</b>")
	require.NoError(t, f.hub.Save(f.system))

	scenarios := []beszelTests.ApiScenario{
		invalid(http.MethodGet, "expired", expired),
		invalid(http.MethodPost, "expired", expired),
		invalid(http.MethodGet, "tampered", tampered),
		invalid(http.MethodPost, "tampered", tampered),
		invalid(http.MethodGet, "garbage", "%3Cscript%3Ealert(1)%3C%2Fscript%3E"),
		invalid(http.MethodGet, "readonly user", readonly),
		invalid(http.MethodPost, "readonly user", readonly),
		invalid(http.MethodPost, "other user", crossUser),
		confirm,
		confirm,
		post("acknowledges"),
		post("replayed"),
	}
	for _, scenario := range scenarios {
		scenario.Test(t)
	}
	assert.True(t, f.reload(t, f.readonlyRow).GetDateTime("acknowledgedAt").IsZero())
}

func TestNotificationsIncludeAckLink(t *testing.T) {
	f := newAckFixture(t)
	am := f.hub.Hub.AlertManager
	require.NoError(t, am.SetAckSecret([]byte("seed")))
	setUserSettings(t, f.hub, f.owner.Id, `{"emails":["owner@example.com"]}`)

	require.NoError(t, am.SendAlert(alerts.AlertMessageData{
		UserID: f.owner.Id, SystemID: f.system.Id, Title: "web CPU above threshold",
		Message: "CPU averaged 90%", Link: "http://infrascope.test/system/x", HistoryID: f.ownerRow.Id,
	}))
	require.NoError(t, am.SendAlert(alerts.AlertMessageData{
		UserID: f.owner.Id, SystemID: f.system.Id, Title: "web CPU below threshold",
		Message: "CPU averaged 10%", Link: "http://infrascope.test/system/x",
	}))
	messages := f.hub.TestMailer.Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, messages[0].Text, "Acknowledge: http://infrascope.test/api/beszel/ack/"+f.ownerRow.Id+"."+f.owner.Id+".")
	assert.NotContains(t, messages[1].Text, "Acknowledge:")
}

func setUserSettings(t *testing.T, hub *beszelTests.TestHub, userID, settings string) {
	t.Helper()
	record, err := hub.FindFirstRecordByData("user_settings", "user", userID)
	if err != nil {
		collection, err := hub.FindCollectionByNameOrId("user_settings")
		require.NoError(t, err)
		record = core.NewRecord(collection)
		record.Set("user", userID)
	}
	record.Set("settings", settings)
	require.NoError(t, hub.Save(record))
}

func TestAlertNotesRules(t *testing.T) {
	f := newAckFixture(t)
	notesURL := "/api/collections/alert_notes/records"
	note, err := beszelTests.CreateRecord(f.hub, "alert_notes", map[string]any{
		"alert": f.ownerRow.Id, "author": f.owner.Id, "text": "restarted nginx",
	})
	require.NoError(t, err)

	scenarios := []beszelTests.ApiScenario{
		{
			Name: "owner lists notes", Method: http.MethodGet, URL: notesURL,
			Headers:        map[string]string{"Authorization": f.ownerTok},
			ExpectedStatus: 200, ExpectedContent: []string{`"totalItems":1`, "restarted nginx"}, TestAppFactory: f.factory,
		},
		{
			Name: "other user sees none", Method: http.MethodGet, URL: notesURL,
			Headers:        map[string]string{"Authorization": f.otherTok},
			ExpectedStatus: 200, ExpectedContent: []string{`"totalItems":0`}, NotExpectedContent: []string{"restarted nginx"}, TestAppFactory: f.factory,
		},
		{
			Name: "owner adds note", Method: http.MethodPost, URL: notesURL,
			Body:           jsonReader(map[string]any{"alert": f.ownerRow.Id, "author": f.owner.Id, "text": "fixed"}),
			Headers:        map[string]string{"Authorization": f.ownerTok},
			ExpectedStatus: 200, ExpectedContent: []string{`"text":"fixed"`}, TestAppFactory: f.factory,
		},
		{
			Name: "other user cannot add note", Method: http.MethodPost, URL: notesURL,
			Body:           jsonReader(map[string]any{"alert": f.ownerRow.Id, "author": f.other.Id, "text": "hi"}),
			Headers:        map[string]string{"Authorization": f.otherTok},
			ExpectedStatus: 400, ExpectedContent: []string{"Failed to create record"}, TestAppFactory: f.factory,
		},
		{
			Name: "owner cannot impersonate author", Method: http.MethodPost, URL: notesURL,
			Body:           jsonReader(map[string]any{"alert": f.ownerRow.Id, "author": f.other.Id, "text": "hi"}),
			Headers:        map[string]string{"Authorization": f.ownerTok},
			ExpectedStatus: 400, ExpectedContent: []string{"Failed to create record"}, TestAppFactory: f.factory,
		},
		{
			Name: "readonly owner cannot add note", Method: http.MethodPost, URL: notesURL,
			Body:           jsonReader(map[string]any{"alert": f.readonlyRow.Id, "author": f.ro.Id, "text": "hi"}),
			Headers:        map[string]string{"Authorization": f.roTok},
			ExpectedStatus: 400, ExpectedContent: []string{"Failed to create record"}, TestAppFactory: f.factory,
		},
		{
			Name: "other user cannot delete note", Method: http.MethodDelete, URL: notesURL + "/" + note.Id,
			Headers:        map[string]string{"Authorization": f.otherTok},
			ExpectedStatus: 404, ExpectedContent: []string{"wasn't found"}, TestAppFactory: f.factory,
		},
		{
			Name: "notes cannot be edited", Method: http.MethodPatch, URL: notesURL + "/" + note.Id,
			Body:           jsonReader(map[string]any{"text": "changed"}),
			Headers:        map[string]string{"Authorization": f.ownerTok},
			ExpectedStatus: 403, ExpectedContent: []string{"superusers"}, TestAppFactory: f.factory,
		},
		{
			Name: "owner deletes note", Method: http.MethodDelete, URL: notesURL + "/" + note.Id,
			Headers:        map[string]string{"Authorization": f.ownerTok},
			ExpectedStatus: 204, TestAppFactory: f.factory,
		},
	}
	for _, scenario := range scenarios {
		scenario.Test(t)
	}

	// Deleting the history row deletes its notes.
	_, err = beszelTests.CreateRecord(f.hub, "alert_notes", map[string]any{
		"alert": f.ownerRow.Id, "author": f.owner.Id, "text": "cascade",
	})
	require.NoError(t, err)
	require.NoError(t, f.hub.Delete(f.reload(t, f.ownerRow)))
	count, err := f.hub.CountRecords("alert_notes")
	require.NoError(t, err)
	assert.EqualValues(t, 0, count)
}
