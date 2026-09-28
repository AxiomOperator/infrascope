//go:build testing

package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusPageTestNow is the fixed time of status page tests.
var statusPageTestNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

type statusPageTestEnv struct {
	hub     *Hub
	handler http.Handler
	owner   *core.Record
	other   *core.Record
	system  *core.Record
}

// newStatusPageTestEnv creates a hub without the monitor hooks, so records
// keep the crafted status fields and events.
func newStatusPageTestEnv(t *testing.T) *statusPageTestEnv {
	t.Helper()
	hub, testApp, err := createTestHub(t)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupTestHub(hub, testApp) })
	hub.statusPages.now = func() time.Time { return statusPageTestNow }
	owner := createMonitorTestUser(t, hub, "owner@example.com", "user")
	other := createMonitorTestUser(t, hub, "other@example.com", "user")
	system, err := createTestRecord(hub, "systems", map[string]any{
		"name": "secret-system-name", "host": "localhost", "port": "45876",
		"status": "paused", "users": []string{owner.Id},
	})
	require.NoError(t, err)
	router, err := apis.NewRouter(hub)
	require.NoError(t, err)
	require.NoError(t, hub.registerApiRoutes(&core.ServeEvent{App: hub, Router: router}))
	handler, err := router.BuildMux()
	require.NoError(t, err)
	return &statusPageTestEnv{hub: hub, handler: handler, owner: owner, other: other, system: system}
}

func (env *statusPageTestEnv) create(t *testing.T, collection string, data map[string]any) *core.Record {
	t.Helper()
	record, err := createTestRecord(env.hub, collection, data)
	require.NoError(t, err)
	return record
}

// monitor creates a hub monitor of the owner.
func (env *statusPageTestEnv) monitor(t *testing.T, fields map[string]any) *core.Record {
	t.Helper()
	data := map[string]any{
		"users": []string{env.owner.Id}, "target": "https://example.com", "protocol": "http",
		"interval": 60, "enabled": true, "status": "up",
	}
	for key, value := range fields {
		data[key] = value
	}
	return env.create(t, "network_monitors", data)
}

func (env *statusPageTestEnv) page(t *testing.T, slug string, public bool, monitors []string, fields map[string]any) *core.Record {
	t.Helper()
	data := map[string]any{
		"user": env.owner.Id, "slug": slug, "title": "Status of " + slug, "description": "desc",
		"public": public, "monitors": monitors,
	}
	for key, value := range fields {
		data[key] = value
	}
	return env.create(t, "status_pages", data)
}

// get requests a status page from ip ("" for the default test address) as auth (nil for a guest).
func (env *statusPageTestEnv) get(t *testing.T, slug string, auth *core.Record, ip string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/beszel/status-pages/"+slug, nil)
	if auth != nil {
		request.Header.Set("Authorization", authToken(t, auth))
	}
	if ip != "" {
		request.RemoteAddr = ip + ":1234"
	}
	response := httptest.NewRecorder()
	env.handler.ServeHTTP(response, request)
	return response
}

func (env *statusPageTestEnv) getPage(t *testing.T, slug string, auth *core.Record) publicStatusPage {
	t.Helper()
	env.hub.statusPages.cache.Remove(slug)
	response := env.get(t, slug, auth, "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var page publicStatusPage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	return page
}

func monitorNames(page publicStatusPage) []string {
	names := make([]string, len(page.Monitors))
	for i, m := range page.Monitors {
		names[i] = m.Name
	}
	return names
}

func TestStatusPagePublicResponse(t *testing.T) {
	env := newStatusPageTestEnv(t)
	agent := env.create(t, "network_monitors", map[string]any{
		"system": env.system.Id, "name": "Gateway", "target": "10.9.8.7", "protocol": "icmp",
		"interval": 60, "enabled": true, "status": "up", "res": 12345,
		"lastError": "secret error text", "lastStatusCode": 503,
		"uptime": map[string]any{"d1": 99.5, "d7": 98.25, "d30": nil},
	})
	push := env.monitor(t, map[string]any{
		"name": "", "protocol": "push", "target": "", "pushToken": "secretpushtoken123", "status": "down",
		"lastError": "secret error text",
	})
	unnamed := env.monitor(t, map[string]any{"name": "", "target": "https://secret-target.example", "status": "pending"})
	disabled := env.monitor(t, map[string]any{"name": "Disabled", "enabled": false, "status": "up"})
	env.page(t, "public", true, []string{unnamed.Id, agent.Id, push.Id, disabled.Id}, nil)

	response := env.get(t, "public", nil, "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, "public, max-age=30", response.Header().Get("Cache-Control"))
	raw := response.Body.String()
	for _, secret := range []string{
		agent.Id, push.Id, unnamed.Id, disabled.Id, env.system.Id, "secret-system-name", env.owner.Id,
		"10.9.8.7", "secret-target.example", "secret error text", "secretpushtoken123", "503", `"res"`, `"target"`,
	} {
		assert.NotContains(t, raw, secret)
	}

	var page publicStatusPage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	assert.Equal(t, "Status of public", page.Title)
	assert.Equal(t, "desc", page.Description)
	assert.Equal(t, statusPageTestNow.UnixMilli(), page.Updated)
	assert.False(t, page.ShowResponseTimes)
	assert.Equal(t, []string{"Monitor 1", "Gateway", "Monitor 3", "Disabled"}, monitorNames(page))
	assert.Equal(t, []string{"pending", "up", "down", "paused"}, []string{
		page.Monitors[0].Status, page.Monitors[1].Status, page.Monitors[2].Status, page.Monitors[3].Status,
	})
	assert.Equal(t, overallDegraded, page.Overall)
	require.NotNil(t, page.Monitors[1].Uptime.D1)
	assert.Equal(t, 99.5, *page.Monitors[1].Uptime.D1)
	assert.Equal(t, 98.25, *page.Monitors[1].Uptime.D7)
	assert.Nil(t, page.Monitors[1].Uptime.D30)
	assert.Nil(t, page.Monitors[0].Uptime.D1)
	for _, m := range page.Monitors {
		assert.Len(t, m.Days, statusPageDays)
	}
	assert.Empty(t, page.Maintenance)
	assert.Contains(t, raw, `"maintenance":[]`)

	// Every key of the contract is present.
	var generic map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &generic))
	for _, key := range []string{"title", "description", "updated", "overall", "showResponseTimes", "monitors", "maintenance"} {
		assert.Contains(t, generic, key)
	}
	monitor := generic["monitors"].([]any)[1].(map[string]any)
	assert.ElementsMatch(t, []string{"name", "status", "uptime", "days"}, keys(monitor))
	day := monitor["days"].([]any)[0].(map[string]any)
	assert.ElementsMatch(t, []string{"d", "up", "st"}, keys(day))
}

func keys(m map[string]any) []string {
	result := make([]string, 0, len(m))
	for key := range m {
		result = append(result, key)
	}
	return result
}

func TestStatusPageTargetsAndResponseTimes(t *testing.T) {
	env := newStatusPageTestEnv(t)
	named := env.monitor(t, map[string]any{"name": "API", "target": "https://api.example.com", "res": 12345})
	unnamed := env.monitor(t, map[string]any{"name": "", "target": "https://web.example.com", "res": 0})
	down := env.monitor(t, map[string]any{"name": "Down", "status": "down", "res": 5000})
	env.page(t, "details", true, []string{named.Id, unnamed.Id, down.Id}, map[string]any{"showTargets": true, "showResponseTimes": true})

	page := env.getPage(t, "details", nil)
	assert.True(t, page.ShowResponseTimes)
	assert.Equal(t, []string{"API", "https://web.example.com", "Down"}, monitorNames(page))
	assert.Equal(t, "https://api.example.com", page.Monitors[0].Target)
	assert.Equal(t, "https://web.example.com", page.Monitors[1].Target)
	// res is converted from microseconds to milliseconds and omitted without a response.
	assert.Equal(t, 12.35, page.Monitors[0].Res)
	assert.Zero(t, page.Monitors[1].Res)
	assert.Zero(t, page.Monitors[2].Res)
}

func TestStatusPageAccess(t *testing.T) {
	env := newStatusPageTestEnv(t)
	monitor := env.monitor(t, map[string]any{"name": "Web"})
	env.page(t, "private", false, []string{monitor.Id}, nil)
	superuser := env.create(t, core.CollectionNameSuperusers, map[string]any{"email": "admin@example.com", "password": "testtesttest"})

	for name, auth := range map[string]*core.Record{"guest": nil, "other user": env.other} {
		response := env.get(t, "private", auth, "")
		assert.Equal(t, http.StatusNotFound, response.Code, name)
		assert.JSONEq(t, `{"status":404,"message":"Status page not found.","data":{}}`, response.Body.String(), name)
	}
	for name, auth := range map[string]*core.Record{"owner": env.owner, "superuser": superuser} {
		response := env.get(t, "private", auth, "")
		require.Equal(t, http.StatusOK, response.Code, name)
		assert.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), name)
		assert.Contains(t, response.Body.String(), `"name":"Web"`, name)
	}
	// Previews are never cached.
	_, cached := env.hub.statusPages.cache.GetOk("private")
	assert.False(t, cached)
	assert.Equal(t, http.StatusNotFound, env.get(t, "private", nil, "").Code)

	assert.Equal(t, http.StatusNotFound, env.get(t, "missing", nil, "").Code)
	assert.Equal(t, http.StatusNotFound, env.get(t, "missing", env.owner, "").Code)
}

func TestStatusPageDropsInaccessibleMonitors(t *testing.T) {
	env := newStatusPageTestEnv(t)
	agent := env.create(t, "network_monitors", map[string]any{
		"system": env.system.Id, "name": "Agent", "target": "1.1.1.1", "protocol": "icmp", "interval": 60, "enabled": true, "status": "up",
	})
	hubMonitor := env.monitor(t, map[string]any{"name": "Hub"})
	deleted := env.monitor(t, map[string]any{"name": "Deleted"})
	notMine := env.create(t, "network_monitors", map[string]any{
		"users": []string{env.other.Id}, "name": "Foreign", "target": "https://example.org", "protocol": "http", "interval": 60, "enabled": true, "status": "up",
	})
	env.page(t, "access", true, []string{agent.Id, hubMonitor.Id, deleted.Id, notMine.Id}, nil)

	assert.Equal(t, []string{"Agent", "Hub", "Deleted"}, monitorNames(env.getPage(t, "access", nil)))

	require.NoError(t, env.hub.Delete(deleted))
	assert.Equal(t, []string{"Agent", "Hub"}, monitorNames(env.getPage(t, "access", nil)))

	// Revoking the owner's access to the system drops its monitors.
	env.system.Set("users", []string{env.other.Id})
	require.NoError(t, env.hub.Save(env.system))
	assert.Equal(t, []string{"Hub"}, monitorNames(env.getPage(t, "access", nil)))

	hubMonitor.Set("users", []string{env.other.Id})
	require.NoError(t, env.hub.Save(hubMonitor))
	page := env.getPage(t, "access", nil)
	assert.Empty(t, page.Monitors)
	assert.Equal(t, uptime.StatusUnknown, page.Overall)
}

func TestStatusPageShareAllSystems(t *testing.T) {
	t.Setenv("SHARE_ALL_SYSTEMS", "true")
	env := newStatusPageTestEnv(t)
	notMine := env.create(t, "network_monitors", map[string]any{
		"users": []string{env.other.Id}, "name": "Shared", "target": "https://example.org", "protocol": "http", "interval": 60, "enabled": true, "status": "up",
	})
	env.page(t, "shared", true, []string{notMine.Id}, nil)
	assert.Equal(t, []string{"Shared"}, monitorNames(env.getPage(t, "shared", nil)))
}

func TestStatusPageDailyBuckets(t *testing.T) {
	env := newStatusPageTestEnv(t)
	monitor := env.monitor(t, map[string]any{"name": "Web"})
	at := func(day, hour int) int64 {
		return time.Date(2026, 9, day, hour, 0, 0, 0, time.UTC).UnixMilli()
	}
	for _, event := range []map[string]any{
		{"status": "maintenance", "start": at(20, 0), "end": at(21, 0)},
		{"status": "unknown", "start": at(21, 0), "end": at(26, 0)},
		{"status": "up", "start": at(26, 0), "end": at(27, 10)},
		{"status": "down", "start": at(27, 10), "end": at(27, 11), "error": "secret error text"},
		{"status": "up", "start": at(27, 11), "end": 0},
	} {
		event["monitor"] = monitor.Id
		env.create(t, "monitor_events", event)
	}
	// Events of monitors not on the page and events before the range are ignored.
	other := env.monitor(t, map[string]any{"name": "Other"})
	env.create(t, "monitor_events", map[string]any{"monitor": other.Id, "status": "down", "start": at(28, 0), "end": 0})
	env.create(t, "monitor_events", map[string]any{"monitor": monitor.Id, "status": "down", "start": at(1, 0) - 200*24*3600*1000, "end": at(1, 0) - 100*24*3600*1000})
	env.page(t, "days", true, []string{monitor.Id}, nil)

	page := env.getPage(t, "days", nil)
	require.Len(t, page.Monitors, 1)
	days := page.Monitors[0].Days
	require.Len(t, days, statusPageDays)
	assert.Equal(t, "2026-07-01", days[0].D)
	assert.Equal(t, "2026-09-28", days[89].D)

	byDate := map[string]publicStatusDay{}
	for _, day := range days {
		byDate[day.D] = day
	}
	yesterday := byDate["2026-09-27"]
	assert.Equal(t, dayDown, yesterday.St)
	require.NotNil(t, yesterday.Up)
	assert.InDelta(t, 95.833, *yesterday.Up, 0.001)

	today := byDate["2026-09-28"]
	assert.Equal(t, dayUp, today.St)
	require.NotNil(t, today.Up)
	assert.Equal(t, 100.0, *today.Up)

	maint := byDate["2026-09-20"]
	assert.Equal(t, dayMaint, maint.St)
	assert.Nil(t, maint.Up)

	for _, date := range []string{"2026-07-01", "2026-09-19", "2026-09-21", "2026-09-25"} {
		assert.Equal(t, dayNone, byDate[date].St, date)
		assert.Nil(t, byDate[date].Up, date)
	}
	assert.Equal(t, dayUp, byDate["2026-09-26"].St)
}

func TestStatusPageOverall(t *testing.T) {
	tests := []struct {
		statuses []string
		want     string
	}{
		{nil, "unknown"},
		{[]string{"paused"}, "unknown"},
		{[]string{"up", "up"}, "up"},
		{[]string{"up", "paused"}, "up"},
		{[]string{"down", "down"}, "down"},
		{[]string{"down", "paused"}, "down"},
		{[]string{"up", "down"}, "degraded"},
		{[]string{"up", "pending"}, "degraded"},
		{[]string{"up", "maintenance"}, "maintenance"},
		{[]string{"pending", "maintenance"}, "maintenance"},
		{[]string{"down", "maintenance"}, "degraded"},
		{[]string{"up", "unknown"}, "unknown"},
		{[]string{"unknown"}, "unknown"},
	}
	for _, tt := range tests {
		monitors := make([]publicStatusMonitor, len(tt.statuses))
		for i, status := range tt.statuses {
			monitors[i].Status = status
		}
		assert.Equal(t, tt.want, overallStatus(monitors), "%v", tt.statuses)
	}
}

func TestStatusPageMaintenance(t *testing.T) {
	env := newStatusPageTestEnv(t)
	monitor := env.monitor(t, map[string]any{"name": "Web"})
	offPage := env.monitor(t, map[string]any{"name": "Off page"})
	window := func(title, windowType string, start, end time.Time, show bool, monitors ...string) {
		env.create(t, "monitor_maintenance", map[string]any{
			"user": env.owner.Id, "title": title, "description": title + " details", "type": windowType,
			"start": start, "end": end, "monitors": monitors, "showOnStatusPages": show,
		})
	}
	now := statusPageTestNow
	window("Active", "one-time", now.Add(-time.Hour), now.Add(time.Hour), true, monitor.Id, offPage.Id)
	window("Daily", "daily", time.Date(2026, 1, 1, 15, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 16, 30, 0, 0, time.UTC), true, monitor.Id)
	window("Next week", "one-time", now.Add(6*24*time.Hour), now.Add(6*24*time.Hour+time.Hour), true, monitor.Id)
	window("Hidden", "one-time", now.Add(-time.Hour), now.Add(time.Hour), false, monitor.Id)
	window("Other monitor", "one-time", now.Add(-time.Hour), now.Add(time.Hour), true, offPage.Id)
	window("Ended", "one-time", now.Add(-2*time.Hour), now.Add(-time.Hour), true, monitor.Id)
	window("Too far", "one-time", now.Add(8*24*time.Hour), now.Add(9*24*time.Hour), true, monitor.Id)
	// another user's window on the same monitor is not published on the owner's page
	env.create(t, "monitor_maintenance", map[string]any{
		"user": env.other.Id, "title": "Someone else", "description": "private notes", "type": "one-time",
		"start": now.Add(-time.Hour), "end": now.Add(time.Hour), "monitors": []string{monitor.Id}, "showOnStatusPages": true,
	})
	env.page(t, "maint", true, []string{monitor.Id}, nil)

	response := env.get(t, "maint", nil, "")
	require.Equal(t, http.StatusOK, response.Code)
	assert.NotContains(t, response.Body.String(), "Off page")
	assert.NotContains(t, response.Body.String(), "private notes")
	var page publicStatusPage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	assert.Equal(t, []publicStatusMaintenance{
		{Title: "Active", Description: "Active details", Start: "2026-09-28T11:00:00.000Z", End: "2026-09-28T13:00:00.000Z", Active: true},
		{Title: "Daily", Description: "Daily details", Start: "2026-09-28T15:00:00.000Z", End: "2026-09-28T16:30:00.000Z", Active: false},
		{Title: "Next week", Description: "Next week details", Start: "2026-10-04T12:00:00.000Z", End: "2026-10-04T13:00:00.000Z", Active: false},
	}, page.Maintenance)
}

func TestStatusPageNextOccurrence(t *testing.T) {
	day := func(hour, minute int) time.Time { return time.Date(2026, 1, 1, hour, minute, 0, 0, time.UTC) }
	now := statusPageTestNow // 12:00
	tests := []struct {
		name       string
		start, end time.Time
		wantStart  string
		wantEnd    string
		ok         bool
	}{
		{"active today", day(11, 0), day(13, 0), "2026-09-28T11:00:00Z", "2026-09-28T13:00:00Z", true},
		{"later today", day(15, 0), day(16, 0), "2026-09-28T15:00:00Z", "2026-09-28T16:00:00Z", true},
		{"tomorrow", day(9, 0), day(10, 0), "2026-09-29T09:00:00Z", "2026-09-29T10:00:00Z", true},
		{"crosses midnight, next", day(23, 0), day(1, 0), "2026-09-28T23:00:00Z", "2026-09-29T01:00:00Z", true},
		{"crosses midnight, active", day(10, 0), day(9, 0), "2026-09-28T10:00:00Z", "2026-09-29T09:00:00Z", true},
		{"equal times", day(10, 0), day(10, 0), "", "", false},
	}
	for _, tt := range tests {
		start, end, ok := nextOccurrence("daily", tt.start, tt.end, now)
		require.Equal(t, tt.ok, ok, tt.name)
		if !ok {
			continue
		}
		assert.Equal(t, tt.wantStart, start.Format(time.RFC3339), tt.name)
		assert.Equal(t, tt.wantEnd, end.Format(time.RFC3339), tt.name)
	}
}

func TestStatusPageCache(t *testing.T) {
	env := newStatusPageTestEnv(t)
	monitor := env.monitor(t, map[string]any{"name": "Web"})
	page := env.page(t, "cached", true, []string{monitor.Id}, nil)

	first := env.get(t, "cached", nil, "")
	require.Equal(t, http.StatusOK, first.Code)
	page.Set("title", "Changed")
	page.Set("public", false)
	require.NoError(t, env.hub.Save(page))

	second := env.get(t, "cached", nil, "")
	require.Equal(t, http.StatusOK, second.Code)
	assert.Equal(t, first.Body.String(), second.Body.String())
	assert.Equal(t, "public, max-age=30", second.Header().Get("Cache-Control"))

	// After expiry the page is read again.
	env.hub.statusPages.cache.Remove("cached")
	assert.Equal(t, http.StatusNotFound, env.get(t, "cached", nil, "").Code)
}

func TestStatusPageRateLimit(t *testing.T) {
	env := newStatusPageTestEnv(t)
	for i := range statusPageRateLimit {
		response := env.get(t, "missing", nil, "203.0.113.5")
		require.Equal(t, http.StatusNotFound, response.Code, i)
	}
	response := env.get(t, "missing", nil, "203.0.113.5")
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	retryAfter, err := strconv.Atoi(response.Header().Get("Retry-After"))
	require.NoError(t, err)
	assert.True(t, retryAfter >= 1 && retryAfter <= 60, retryAfter)
	assert.Contains(t, response.Body.String(), `"status":429`)

	// Other clients are not affected.
	assert.Equal(t, http.StatusNotFound, env.get(t, "missing", nil, "203.0.113.6").Code)
}
