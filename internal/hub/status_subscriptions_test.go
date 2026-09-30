//go:build testing

package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/mailer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type subscriptionTestEnv struct {
	*statusPageTestEnv
	mailer *pbtests.TestMailer
	now    time.Time
}

// newSubscriptionTestEnv returns a status page env with mail configured and
// a subscription clock at statusPageTestNow.
func newSubscriptionTestEnv(t *testing.T) *subscriptionTestEnv {
	t.Helper()
	base := newStatusPageTestEnv(t)
	env := &subscriptionTestEnv{statusPageTestEnv: base, now: statusPageTestNow}
	env.hub.statusSubscriptions.now = func() time.Time { return env.now }
	env.hub.statusSubscriptions.secret = []byte("test-subscription-key")
	env.setMail(t, true)
	testApp, ok := env.hub.App.(*pbtests.TestApp)
	require.True(t, ok)
	env.mailer = testApp.TestMailer
	env.mailer.Reset()
	return env
}

func (env *subscriptionTestEnv) setMail(t *testing.T, enabled bool) {
	t.Helper()
	settings := env.hub.Settings()
	settings.SMTP.Enabled = enabled
	settings.SMTP.Host = "smtp.example.com"
	settings.Meta.AppURL = "https://hub.example.com"
	settings.Meta.SenderAddress = "status@example.com"
	require.NoError(t, env.hub.Save(settings))
}

func (env *subscriptionTestEnv) do(t *testing.T, method, url, ip string, auth *core.Record, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if ip != "" {
		request.RemoteAddr = ip + ":1234"
	}
	if auth != nil {
		request.Header.Set("Authorization", authToken(t, auth))
	}
	response := httptest.NewRecorder()
	env.handler.ServeHTTP(response, request)
	return response
}

func (env *subscriptionTestEnv) subscribe(t *testing.T, slug, ip, email string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email})
	return env.do(t, http.MethodPost, "/api/beszel/status-pages/"+slug+"/subscribe", ip, nil, string(body))
}

func (env *subscriptionTestEnv) subscribers(t *testing.T, pageID string) []*core.Record {
	t.Helper()
	records, err := env.hub.FindAllRecords(statusSubscribersCollection, dbx.HashExp{"page": pageID})
	require.NoError(t, err)
	return records
}

// sent drains the queue at the env clock and returns and resets the sent messages.
func (env *subscriptionTestEnv) sent() []*mailer.Message {
	env.hub.statusSubscriptions.drain(env.now)
	messages := env.mailer.Messages()
	env.mailer.Reset()
	return messages
}

// addSubscriber stores a subscriber directly.
func (env *subscriptionTestEnv) addSubscriber(t *testing.T, pageID, email string, confirmed bool) *core.Record {
	t.Helper()
	return env.create(t, statusSubscribersCollection, map[string]any{
		"page": pageID, "email": email, "confirmed": confirmed, "createdAt": env.now,
	})
}

var confirmLinkPattern = regexp.MustCompile(`https://hub\.example\.com/api/beszel/status-subscriptions/confirm/([A-Za-z0-9_-]+)`)
var unsubscribeLinkPattern = regexp.MustCompile(`https://hub\.example\.com/api/beszel/status-subscriptions/unsubscribe/([A-Za-z0-9_.-]+)`)

func TestStatusSubscribe(t *testing.T) {
	env := newSubscriptionTestEnv(t)
	page := env.page(t, "subs", true, nil, map[string]any{"allowSubscriptions": true})

	response := env.subscribe(t, "subs", "10.0.0.1", "  Reader@Example.COM ")
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	generic := response.Body.String()
	assert.Contains(t, generic, subscribeAcceptedMessage)

	records := env.subscribers(t, page.Id)
	require.Len(t, records, 1)
	subscriber := records[0]
	assert.Equal(t, "reader@example.com", subscriber.GetString("email"))
	assert.False(t, subscriber.GetBool("confirmed"))
	assert.Len(t, subscriber.GetString("tokenHash"), 64)

	messages := env.sent()
	require.Len(t, messages, 1)
	message := messages[0]
	assert.Equal(t, "reader@example.com", message.To[0].Address)
	assert.Equal(t, "[Status of subs] Confirm your subscription", message.Subject)
	match := confirmLinkPattern.FindStringSubmatch(message.Text)
	require.NotNil(t, match, message.Text)
	token := match[1]
	// Only the SHA-256 of the 32-byte token is stored.
	assert.Len(t, token, 43)
	assert.Equal(t, hashSubscriptionToken(token), subscriber.GetString("tokenHash"))
	assert.NotContains(t, subscriber.GetString("tokenHash"), token)
	assert.Regexp(t, unsubscribeLinkPattern, message.Text)
	assert.Equal(t, "List-Unsubscribe=One-Click", message.Headers["List-Unsubscribe-Post"])
	assert.True(t, strings.HasPrefix(message.Headers["List-Unsubscribe"], "<https://hub.example.com/api/beszel/status-subscriptions/unsubscribe/"))

	// Subscribing again refreshes the token; the third confirmation email a
	// day is the last one, while the response stays the same.
	for i := range 3 {
		response := env.subscribe(t, "subs", "10.0.1."+strconv.Itoa(i), "reader@example.com")
		assert.Equal(t, http.StatusAccepted, response.Code)
		assert.Equal(t, generic, response.Body.String())
	}
	assert.Len(t, env.sent(), 2, "at most 3 confirmation emails per address and day")
	refreshed, err := env.hub.FindRecordById(statusSubscribersCollection, subscriber.Id)
	require.NoError(t, err)
	assert.NotEqual(t, subscriber.GetString("tokenHash"), refreshed.GetString("tokenHash"))
	assert.Len(t, env.subscribers(t, page.Id), 1)

	// Confirmed subscribers get nothing, with the same response.
	env.addSubscriber(t, page.Id, "confirmed@example.com", true)
	response = env.subscribe(t, "subs", "10.0.2.1", "confirmed@example.com")
	assert.Equal(t, http.StatusAccepted, response.Code)
	assert.Equal(t, generic, response.Body.String())
	assert.Empty(t, env.sent())

	// Invalid addresses are rejected.
	for _, email := range []string{"", "nope", "a@b", "Name <a@example.com>", strings.Repeat("a", 250) + "@example.com"} {
		response := env.subscribe(t, "subs", "10.0.3."+strconv.Itoa(len(email)%200), email)
		assert.Equal(t, http.StatusBadRequest, response.Code, email)
	}
}

func TestStatusSubscribeIgnored(t *testing.T) {
	env := newSubscriptionTestEnv(t)
	enabled := env.page(t, "enabled", true, nil, map[string]any{"allowSubscriptions": true})
	disabled := env.page(t, "disabled", true, nil, nil)
	env.page(t, "private", false, nil, map[string]any{"allowSubscriptions": true})

	generic := env.subscribe(t, "enabled", "10.0.0.1", "a@example.com").Body.String()
	env.sent()
	generic = strings.TrimSpace(generic)

	// Honeypot.
	response := env.do(t, http.MethodPost, "/api/beszel/status-pages/enabled/subscribe", "10.0.0.2", nil,
		`{"email":"bot@example.com","website":"https://spam.example.com"}`)
	assert.Equal(t, http.StatusAccepted, response.Code)
	assert.Equal(t, generic, strings.TrimSpace(response.Body.String()))
	// Disabled page.
	response = env.subscribe(t, "disabled", "10.0.0.3", "b@example.com")
	assert.Equal(t, http.StatusAccepted, response.Code)
	assert.Equal(t, generic, strings.TrimSpace(response.Body.String()))
	assert.Empty(t, env.subscribers(t, disabled.Id))
	// Private and unknown pages do not exist.
	assert.Equal(t, http.StatusNotFound, env.subscribe(t, "private", "10.0.0.4", "c@example.com").Code)
	assert.Equal(t, http.StatusNotFound, env.subscribe(t, "missing", "10.0.0.5", "c@example.com").Code)
	// Without SMTP nothing is stored or sent.
	env.setMail(t, false)
	response = env.subscribe(t, "enabled", "10.0.0.6", "d@example.com")
	assert.Equal(t, http.StatusAccepted, response.Code)
	env.setMail(t, true)

	records := env.subscribers(t, enabled.Id)
	require.Len(t, records, 1)
	assert.Equal(t, "a@example.com", records[0].GetString("email"))
	assert.Empty(t, env.sent())
}

func TestStatusSubscribeLimits(t *testing.T) {
	env := newSubscriptionTestEnv(t)
	page := env.page(t, "limits", true, nil, map[string]any{"allowSubscriptions": true})

	// 5 requests per IP and hour.
	for i := range 5 {
		assert.Equal(t, http.StatusAccepted, env.subscribe(t, "limits", "10.1.0.1", "ip"+strconv.Itoa(i)+"@example.com").Code)
	}
	response := env.subscribe(t, "limits", "10.1.0.1", "ip5@example.com")
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.NotEmpty(t, response.Header().Get("Retry-After"))

	// 100 requests per page and hour.
	for i := 5; i < 100; i++ {
		assert.Equal(t, http.StatusAccepted, env.subscribe(t, "limits", "10.2.0."+strconv.Itoa(i), "p"+strconv.Itoa(i)+"@example.com").Code)
	}
	assert.Equal(t, http.StatusTooManyRequests, env.subscribe(t, "limits", "10.3.0.1", "last@example.com").Code)
	assert.Len(t, env.subscribers(t, page.Id), 100)

	// Full pages take no new subscribers.
	full := env.page(t, "full", true, nil, map[string]any{"allowSubscriptions": true})
	previous := maxSubscribersPerPage
	maxSubscribersPerPage = 2
	t.Cleanup(func() { maxSubscribersPerPage = previous })
	env.addSubscriber(t, full.Id, "one@example.com", true)
	env.addSubscriber(t, full.Id, "two@example.com", false)
	env.sent()
	response = env.subscribe(t, "full", "10.4.0.1", "three@example.com")
	assert.Equal(t, http.StatusAccepted, response.Code)
	assert.Len(t, env.subscribers(t, full.Id), 2)
	// An unconfirmed subscriber of a full page can still get a new link.
	assert.Equal(t, http.StatusAccepted, env.subscribe(t, "full", "10.4.0.2", "two@example.com").Code)
	assert.Len(t, env.sent(), 1)
}

func TestStatusSubscriptionConfirmAndUnsubscribe(t *testing.T) {
	env := newSubscriptionTestEnv(t)
	page := env.page(t, "confirm", true, nil, map[string]any{"allowSubscriptions": true})
	require.Equal(t, http.StatusAccepted, env.subscribe(t, "confirm", "10.0.0.1", "reader@example.com").Code)
	messages := env.sent()
	require.Len(t, messages, 1)
	token := confirmLinkPattern.FindStringSubmatch(messages[0].Text)[1]
	unsubscribeToken := unsubscribeLinkPattern.FindStringSubmatch(messages[0].Text)[1]
	confirmURL := "/api/beszel/status-subscriptions/confirm/" + token

	// GET only shows the confirmation page.
	response := env.do(t, http.MethodGet, confirmURL, "", nil, "")
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), `<form method="post">`)
	assert.Contains(t, response.Body.String(), "Status of confirm")
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	subscriber := env.subscribers(t, page.Id)[0]
	assert.False(t, subscriber.GetBool("confirmed"))

	response = env.do(t, http.MethodPost, confirmURL, "", nil, "")
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "Subscription confirmed")
	subscriber = env.subscribers(t, page.Id)[0]
	assert.True(t, subscriber.GetBool("confirmed"))
	assert.False(t, subscriber.GetDateTime("confirmedAt").IsZero())
	assert.Empty(t, subscriber.GetString("tokenHash"))
	// The token cannot be used again; unknown tokens are invalid.
	assert.Equal(t, http.StatusBadRequest, env.do(t, http.MethodPost, confirmURL, "", nil, "").Code)
	assert.Equal(t, http.StatusBadRequest, env.do(t, http.MethodGet, "/api/beszel/status-subscriptions/confirm/nope", "", nil, "").Code)

	// Expired tokens are invalid.
	require.Equal(t, http.StatusAccepted, env.subscribe(t, "confirm", "10.0.0.2", "late@example.com").Code)
	late := confirmLinkPattern.FindStringSubmatch(env.sent()[0].Text)[1]
	env.now = env.now.Add(unconfirmedSubscriberTTL)
	assert.Equal(t, http.StatusBadRequest, env.do(t, http.MethodPost, "/api/beszel/status-subscriptions/confirm/"+late, "", nil, "").Code)

	// Unsubscribing: GET does not delete, POST (as one-click) does.
	unsubscribeURL := "/api/beszel/status-subscriptions/unsubscribe/" + unsubscribeToken
	response = env.do(t, http.MethodGet, unsubscribeURL, "", nil, "")
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), `<form method="post">`)
	_, err := env.hub.FindRecordById(statusSubscribersCollection, subscriber.Id)
	require.NoError(t, err)

	forged := subscriber.Id + ".AAAA"
	assert.Equal(t, http.StatusBadRequest, env.do(t, http.MethodPost, "/api/beszel/status-subscriptions/unsubscribe/"+forged, "", nil, "").Code)
	_, err = env.hub.FindRecordById(statusSubscribersCollection, subscriber.Id)
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, unsubscribeURL, strings.NewReader("List-Unsubscribe=One-Click"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusOK, recorder.Code)
	_, err = env.hub.FindRecordById(statusSubscribersCollection, subscriber.Id)
	assert.Error(t, err)
	// Again is fine.
	assert.Equal(t, http.StatusOK, env.do(t, http.MethodPost, unsubscribeURL, "", nil, "").Code)
}

func TestPurgeUnconfirmedSubscribers(t *testing.T) {
	env := newSubscriptionTestEnv(t)
	page := env.page(t, "purge", true, nil, map[string]any{"allowSubscriptions": true})
	old := env.addSubscriber(t, page.Id, "old@example.com", false)
	old.Set("createdAt", env.now.Add(-unconfirmedSubscriberTTL-time.Minute))
	require.NoError(t, env.hub.Save(old))
	oldConfirmed := env.addSubscriber(t, page.Id, "confirmed@example.com", true)
	oldConfirmed.Set("createdAt", env.now.Add(-30*24*time.Hour))
	require.NoError(t, env.hub.Save(oldConfirmed))
	recent := env.addSubscriber(t, page.Id, "recent@example.com", false)

	require.NoError(t, purgeUnconfirmedSubscribers(env.hub, env.now))
	var ids []string
	for _, record := range env.subscribers(t, page.Id) {
		ids = append(ids, record.Id)
	}
	assert.ElementsMatch(t, []string{oldConfirmed.Id, recent.Id}, ids)
}

func TestStatusSubscriptionIncidentNotifications(t *testing.T) {
	env := newSubscriptionTestEnv(t)
	bindIncidentHooks(env.hub)
	bindStatusSubscriptionHooks(env.hub)
	page := env.page(t, "incidents", true, nil, map[string]any{"allowSubscriptions": true})
	disabled := env.page(t, "quiet", true, nil, nil)
	env.addSubscriber(t, page.Id, "reader@example.com", true)
	env.addSubscriber(t, page.Id, "pending@example.com", false)
	env.addSubscriber(t, disabled.Id, "other@example.com", true)

	incident := env.create(t, incidentsCollection, map[string]any{
		"user": env.owner.Id, "title": "Database outage", "status": "investigating", "impact": "major",
		"statusPages": []string{page.Id, disabled.Id},
	})
	require.NoError(t, addIncidentUpdate(env.hub, incident.Id, "investigating", "We are looking into it."))
	// Changes are coalesced until incidentNoticeDelay has passed.
	assert.Empty(t, env.sent())
	env.now = env.now.Add(incidentNoticeDelay)
	messages := env.sent()
	require.Len(t, messages, 1, "only confirmed subscribers of pages with subscriptions")
	assert.Equal(t, "reader@example.com", messages[0].To[0].Address)
	assert.Equal(t, "[Status of incidents] Database outage: Investigating", messages[0].Subject)
	assert.Contains(t, messages[0].Text, "We are looking into it.")
	assert.Contains(t, messages[0].Text, "https://hub.example.com/status/incidents")
	assert.Regexp(t, unsubscribeLinkPattern, messages[0].Text)
	assert.NotEmpty(t, messages[0].Headers["List-Unsubscribe"])

	require.NoError(t, addIncidentUpdate(env.hub, incident.Id, "identified", "A disk failed."))
	env.now = env.now.Add(incidentNoticeDelay)
	messages = env.sent()
	require.Len(t, messages, 1)
	assert.Equal(t, "[Status of incidents] Database outage: Identified", messages[0].Subject)
	assert.Contains(t, messages[0].Text, "A disk failed.")

	// Resolving by editing the incident also notifies.
	incident, err := env.hub.FindRecordById(incidentsCollection, incident.Id)
	require.NoError(t, err)
	incident.Set("status", incidentResolved)
	require.NoError(t, env.hub.Save(incident))
	env.now = env.now.Add(incidentNoticeDelay)
	messages = env.sent()
	require.Len(t, messages, 1)
	assert.Equal(t, "[Status of incidents] Database outage: Resolved", messages[0].Subject)

	// Title edits do not; private pages get nothing.
	incident.Set("title", "Renamed")
	require.NoError(t, env.hub.Save(incident))
	page.Set("public", false)
	require.NoError(t, env.hub.Save(page))
	require.NoError(t, addIncidentUpdate(env.hub, incident.Id, "resolved", "Postmortem"))
	env.now = env.now.Add(incidentNoticeDelay)
	assert.Empty(t, env.sent())

	subscriber := env.subscribers(t, page.Id)
	for _, record := range subscriber {
		if record.GetString("email") == "reader@example.com" {
			assert.False(t, record.GetDateTime("lastSentAt").IsZero())
		}
	}
}

func TestStatusSubscriptionComponentNotifications(t *testing.T) {
	env := newSubscriptionTestEnv(t)
	bindStatusSubscriptionHooks(env.hub)
	monitor := env.monitor(t, map[string]any{"name": "API"})
	page := env.page(t, "components", true, []string{monitor.Id}, map[string]any{"allowSubscriptions": true, "systems": []string{env.system.Id}})
	auto := env.page(t, "auto", true, []string{monitor.Id}, map[string]any{"allowSubscriptions": true, "autoIncidents": true})
	env.addSubscriber(t, page.Id, "reader@example.com", true)
	env.addSubscriber(t, auto.Id, "auto@example.com", true)
	subs := env.hub.statusSubscriptions
	transition := func(status string) {
		subs.monitorTransitions([]uptime.Transition{{MonitorID: monitor.Id, Status: status, At: env.now}})
	}

	transition(uptime.StatusDown)
	messages := env.sent()
	require.Len(t, messages, 1, "pages with autoIncidents get incident emails instead")
	assert.Equal(t, "reader@example.com", messages[0].To[0].Address)
	assert.Equal(t, "[Status of components] API is down", messages[0].Subject)
	transition(uptime.StatusUp)
	messages = env.sent()
	require.Len(t, messages, 1)
	assert.Equal(t, "[Status of components] API has recovered", messages[0].Subject)

	// Flaps within the window send nothing more...
	env.now = env.now.Add(time.Minute)
	transition(uptime.StatusDown)
	assert.Empty(t, env.sent())
	transition(uptime.StatusUp)
	assert.Empty(t, env.sent())
	env.now = env.now.Add(time.Minute)
	transition(uptime.StatusDown)
	assert.Empty(t, env.sent())
	// ...until it has passed, when a component still down is reported.
	env.now = statusPageTestNow.Add(componentNoticeWindow)
	messages = env.sent()
	require.Len(t, messages, 1)
	assert.Contains(t, messages[0].Subject, "API is down")
	transition(uptime.StatusUp)
	assert.Len(t, env.sent(), 1)

	// Systems notify with their public name.
	env.system.Set("status", uptime.StatusDown)
	require.NoError(t, env.hub.Save(env.system))
	messages = env.sent()
	require.Len(t, messages, 1)
	assert.Equal(t, "[Status of components] secret-system-name is down", messages[0].Subject)

	// Without SMTP nothing is queued or sent.
	env.setMail(t, false)
	env.now = env.now.Add(componentNoticeWindow)
	env.system.Set("status", uptime.StatusUp)
	require.NoError(t, env.hub.Save(env.system))
	transition(uptime.StatusDown)
	assert.Empty(t, env.sent())
}

func TestStatusSubscriberList(t *testing.T) {
	env := newSubscriptionTestEnv(t)
	page := env.page(t, "owned", true, nil, map[string]any{"allowSubscriptions": true})
	confirmed := env.addSubscriber(t, page.Id, "a@example.com", true)
	env.addSubscriber(t, page.Id, "b@example.com", false)
	readonly := createMonitorTestUser(t, env.hub, "readonly@example.com", "readonly")
	url := "/api/beszel/status-pages/" + page.Id + "/subscribers"

	response := env.do(t, http.MethodGet, url, "", env.owner, "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var list struct {
		Total     int                `json:"total"`
		Confirmed int                `json:"confirmed"`
		Items     []statusSubscriber `json:"items"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &list))
	assert.Equal(t, 2, list.Total)
	assert.Equal(t, 1, list.Confirmed)
	require.Len(t, list.Items, 2)
	assert.NotContains(t, response.Body.String(), "tokenHash")

	assert.Equal(t, http.StatusNotFound, env.do(t, http.MethodGet, url, "", env.other, "").Code)
	assert.Equal(t, http.StatusNotFound, env.do(t, http.MethodGet, url, "", readonly, "").Code)
	assert.Equal(t, http.StatusUnauthorized, env.do(t, http.MethodGet, url, "", nil, "").Code)
	// The collection itself is superuser-only.
	assert.NotEqual(t, http.StatusOK, env.do(t, http.MethodGet, "/api/collections/status_subscribers/records", "", env.owner, "").Code)

	assert.Equal(t, http.StatusNotFound, env.do(t, http.MethodDelete, url+"/"+confirmed.Id, "", env.other, "").Code)
	assert.Equal(t, http.StatusForbidden, env.do(t, http.MethodDelete, url+"/"+confirmed.Id, "", readonly, "").Code)
	assert.Equal(t, http.StatusNoContent, env.do(t, http.MethodDelete, url+"/"+confirmed.Id, "", env.owner, "").Code)
	assert.Len(t, env.subscribers(t, page.Id), 1)
	assert.Equal(t, http.StatusNotFound, env.do(t, http.MethodDelete, url+"/"+confirmed.Id, "", env.owner, "").Code)

	// The public page tells whether subscriptions are available.
	assert.True(t, env.getPage(t, "owned", nil).Subscriptions)
	env.setMail(t, false)
	assert.False(t, env.getPage(t, "owned", nil).Subscriptions)
}

func TestStatusSubscriptionKeyFromHubKey(t *testing.T) {
	env := newSubscriptionTestEnv(t)
	subs := env.hub.statusSubscriptions
	subs.secret = nil
	token, err := subs.unsubscribeToken("abc")
	require.NoError(t, err)
	id, ok := subs.verifyUnsubscribeToken(token)
	assert.True(t, ok)
	assert.Equal(t, "abc", id)
	// The key is derived from the hub key, so it is stable.
	subs.secret = nil
	again, err := subs.unsubscribeToken("abc")
	require.NoError(t, err)
	assert.Equal(t, token, again)
	_, ok = subs.verifyUnsubscribeToken("abd" + token[3:])
	assert.False(t, ok)
}
