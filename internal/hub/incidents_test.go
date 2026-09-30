//go:build testing

package hub

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIncidentRules(t *testing.T) {
	env := newMonitorTestEnv(t)
	bindIncidentHooks(env.hub)
	other := createMonitorTestUser(t, env.hub, "other@example.com", "user")
	readonly := createMonitorTestUser(t, env.hub, "readonly@example.com", "readonly")
	monitor, err := createTestRecord(env.hub, "network_monitors", map[string]any{
		"users": []string{env.owner.Id, readonly.Id}, "target": "https://example.com", "protocol": "http", "interval": 60,
	})
	require.NoError(t, err)
	foreignSystem := env.addSystem(t, "Foreign", other.Id)
	page, err := createTestRecord(env.hub, "status_pages", map[string]any{"user": env.owner.Id, "slug": "owner", "title": "Owner"})
	require.NoError(t, err)
	otherPage, err := createTestRecord(env.hub, "status_pages", map[string]any{"user": other.Id, "slug": "other", "title": "Other"})
	require.NoError(t, err)

	post := func(auth *core.Record, collection string, body map[string]any) (int, map[string]any) {
		t.Helper()
		response := env.request(t, http.MethodPost, "/api/collections/"+collection+"/records", auth, body)
		var data map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &data)
		return response.Code, data
	}
	incidentBody := func(user string, fields map[string]any) map[string]any {
		body := map[string]any{"user": user, "title": "Outage", "status": "investigating", "impact": "major"}
		for key, value := range fields {
			body[key] = value
		}
		return body
	}

	// Owners create incidents with their components and pages; auto is server-managed.
	code, data := post(env.owner, "incidents", incidentBody(env.owner.Id, map[string]any{
		"monitors": []string{monitor.Id}, "systems": []string{env.system.Id}, "statusPages": []string{page.Id},
		"auto": true, "autoKey": "monitor:" + monitor.Id,
	}))
	require.Equal(t, http.StatusOK, code, data)
	incident, err := env.hub.FindRecordById("incidents", data["id"].(string))
	require.NoError(t, err)
	assert.False(t, incident.GetBool("auto"))
	assert.Empty(t, incident.GetString("autoKey"))
	assert.False(t, incident.GetDateTime("startedAt").IsZero())
	assert.True(t, incident.GetDateTime("resolvedAt").IsZero())
	assert.NotContains(t, data, "autoKey")

	for name, body := range map[string]map[string]any{
		"another user's page":   incidentBody(env.owner.Id, map[string]any{"statusPages": []string{otherPage.Id}}),
		"a foreign system":      incidentBody(env.owner.Id, map[string]any{"systems": []string{foreignSystem.Id}}),
		"another user as owner": incidentBody(other.Id, nil),
	} {
		code, data := post(env.owner, "incidents", body)
		assert.Equal(t, http.StatusBadRequest, code, name, data)
	}
	code, data = post(other, "incidents", incidentBody(other.Id, map[string]any{"monitors": []string{monitor.Id}}))
	assert.Equal(t, http.StatusBadRequest, code, "a foreign monitor: %v", data)
	code, _ = post(readonly, "incidents", incidentBody(readonly.Id, nil))
	assert.Equal(t, http.StatusBadRequest, code, "readonly users cannot create incidents")

	// Other users neither see nor change the incident.
	response := env.request(t, http.MethodGet, "/api/collections/incidents/records", other, nil)
	assert.Contains(t, response.Body.String(), `"totalItems":0`)
	response = env.request(t, http.MethodPatch, "/api/collections/incidents/records/"+incident.Id, other, map[string]any{"title": "x"})
	assert.Equal(t, http.StatusNotFound, response.Code)
	// Clients cannot turn an incident into an automatic one.
	response = env.request(t, http.MethodPatch, "/api/collections/incidents/records/"+incident.Id, env.owner, map[string]any{"auto": true})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	incident, err = env.hub.FindRecordById("incidents", incident.Id)
	require.NoError(t, err)
	assert.False(t, incident.GetBool("auto"))

	// Updates: only the incident owner adds them, and they set the incident status.
	code, _ = post(other, "incident_updates", map[string]any{"incident": incident.Id, "status": "identified", "message": "x"})
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = post(readonly, "incident_updates", map[string]any{"incident": incident.Id, "status": "identified", "message": "x"})
	assert.Equal(t, http.StatusBadRequest, code)
	code, data = post(env.owner, "incident_updates", map[string]any{
		"incident": incident.Id, "status": "resolved", "message": "Fixed", "author": other.Id,
	})
	require.Equal(t, http.StatusOK, code, data)
	assert.Equal(t, env.owner.Id, data["author"])
	incident, err = env.hub.FindRecordById("incidents", incident.Id)
	require.NoError(t, err)
	assert.Equal(t, "resolved", incident.GetString("status"))
	assert.False(t, incident.GetDateTime("resolvedAt").IsZero())
	updateID := data["id"].(string)

	response = env.request(t, http.MethodPatch, "/api/collections/incident_updates/records/"+updateID, env.owner, map[string]any{"message": "y"})
	assert.Equal(t, http.StatusForbidden, response.Code, "updates cannot be edited")
	response = env.request(t, http.MethodGet, "/api/collections/incident_updates/records", other, nil)
	assert.Contains(t, response.Body.String(), `"totalItems":0`)
	response = env.request(t, http.MethodGet, "/api/collections/incident_updates/records", env.owner, nil)
	assert.Contains(t, response.Body.String(), `"totalItems":1`)

	// Reopening clears resolvedAt.
	response = env.request(t, http.MethodPatch, "/api/collections/incidents/records/"+incident.Id, env.owner, map[string]any{"status": "monitoring"})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	incident, err = env.hub.FindRecordById("incidents", incident.Id)
	require.NoError(t, err)
	assert.True(t, incident.GetDateTime("resolvedAt").IsZero())

	// Deleting an incident deletes its updates.
	response = env.request(t, http.MethodDelete, "/api/collections/incidents/records/"+incident.Id, env.owner, nil)
	require.Equal(t, http.StatusNoContent, response.Code)
	count, err := env.hub.CountRecords("incident_updates")
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestIncidentCollectionRules(t *testing.T) {
	hub, testApp, err := createTestHub(t)
	require.NoError(t, err)
	defer cleanupTestHub(hub, testApp)
	const isOwner = `@request.auth.id != "" && user = @request.auth.id`
	incidents, err := hub.FindCollectionByNameOrId("incidents")
	require.NoError(t, err)
	assert.Equal(t, isOwner, *incidents.ListRule)
	assert.Equal(t, isOwner+` && @request.auth.role != "readonly"`, *incidents.CreateRule)
	updates, err := hub.FindCollectionByNameOrId("incident_updates")
	require.NoError(t, err)
	const isIncidentOwner = `@request.auth.id != "" && incident.user = @request.auth.id`
	assert.Equal(t, isIncidentOwner, *updates.ListRule)
	assert.Equal(t, isIncidentOwner, *updates.ViewRule)
	assert.Equal(t, isIncidentOwner+` && @request.auth.role != "readonly"`, *updates.CreateRule)
	assert.Nil(t, updates.UpdateRule)
}

// incidentsOf returns the incidents of the owner, oldest first.
func incidentsOf(t *testing.T, app core.App, owner string) []*core.Record {
	t.Helper()
	return orderedRecords(t, app, "incidents", dbx.HashExp{"user": owner})
}

func incidentUpdates(t *testing.T, app core.App, incident string) []*core.Record {
	t.Helper()
	return orderedRecords(t, app, "incident_updates", dbx.HashExp{"incident": incident})
}

// orderedRecords returns the matching records in insertion order.
func orderedRecords(t *testing.T, app core.App, collection string, where dbx.Expression) []*core.Record {
	t.Helper()
	var records []*core.Record
	require.NoError(t, app.RecordQuery(collection).AndWhere(where).OrderBy("rowid ASC").All(&records))
	return records
}

func TestAutoIncidentsForMonitors(t *testing.T) {
	env := newMonitorAlertEnv(t)
	bindIncidentHooks(env.hub)
	web := env.hubMonitorRecord(false)
	web.Set("retries", 1)
	require.NoError(t, env.hub.Save(web))
	api := env.hubMonitorRecord(false)
	api.Set("name", "API")
	require.NoError(t, env.hub.Save(api))
	unlisted := env.hubMonitorRecord(false)
	page, err := createTestRecord(env.hub, "status_pages", map[string]any{
		"user": env.owner.Id, "slug": "auto", "title": "Auto", "autoIncidents": true, "monitors": []string{web.Id},
	})
	require.NoError(t, err)
	mixed, err := createTestRecord(env.hub, "status_pages", map[string]any{
		"user": env.owner.Id, "slug": "mixed", "title": "Mixed", "autoIncidents": true, "monitors": []string{api.Id, web.Id},
	})
	require.NoError(t, err)
	_, err = createTestRecord(env.hub, "status_pages", map[string]any{
		"user": env.owner.Id, "slug": "manual", "title": "Manual", "monitors": []string{unlisted.Id},
	})
	require.NoError(t, err)
	for _, id := range []string{web.Id, api.Id, unlisted.Id} {
		env.check(id, true)
	}
	manual, err := createTestRecord(env.hub, "incidents", map[string]any{
		"user": env.owner.Id, "title": "Planned work", "status": "identified", "impact": "minor", "monitors": []string{web.Id},
	})
	require.NoError(t, err)

	// A page without autoIncidents opens nothing.
	env.check(unlisted.Id, false)
	env.check(unlisted.Id, false)
	require.Equal(t, uptime.StatusDown, env.hub.uptime.Status(unlisted.Id))
	require.Len(t, incidentsOf(t, env.hub, env.owner.Id), 1)

	// A pending failure opens nothing; a confirmed down opens one incident
	// for both pages. The first page is down entirely: major impact.
	env.check(web.Id, false)
	require.Len(t, incidentsOf(t, env.hub, env.owner.Id), 1)
	env.check(web.Id, false)
	incidents := incidentsOf(t, env.hub, env.owner.Id)
	require.Len(t, incidents, 2)
	auto := incidents[1]
	assert.True(t, auto.GetBool("auto"))
	assert.Equal(t, "Web is down", auto.GetString("title"))
	assert.Equal(t, "investigating", auto.GetString("status"))
	assert.Equal(t, "major", auto.GetString("impact"))
	assert.ElementsMatch(t, []string{page.Id, mixed.Id}, auto.GetStringSlice("statusPages"))
	assert.Equal(t, []string{web.Id}, auto.GetStringSlice("monitors"))
	require.Len(t, incidentUpdates(t, env.hub, auto.Id), 1)

	// Further failures and a repeated down do not duplicate it.
	env.check(web.Id, false)
	require.NoError(t, openAutoIncidents(env.hub, incidentComponent{field: "monitors", id: web.Id}, time.Now()))
	require.Len(t, incidentsOf(t, env.hub, env.owner.Id), 2)

	// Recovery resolves the automatic incident only.
	env.check(web.Id, true)
	auto, err = env.hub.FindRecordById("incidents", auto.Id)
	require.NoError(t, err)
	assert.Equal(t, "resolved", auto.GetString("status"))
	assert.False(t, auto.GetDateTime("resolvedAt").IsZero())
	updates := incidentUpdates(t, env.hub, auto.Id)
	require.Len(t, updates, 2)
	assert.Equal(t, "resolved", updates[1].GetString("status"))
	assert.Equal(t, "Web has recovered.", updates[1].GetString("message"))
	manual, err = env.hub.FindRecordById("incidents", manual.Id)
	require.NoError(t, err)
	assert.Equal(t, "identified", manual.GetString("status"))

	// A new outage opens a new incident. Only one of the mixed page's two
	// monitors is down: minor impact.
	env.check(api.Id, false)
	env.check(api.Id, false)
	incidents = incidentsOf(t, env.hub, env.owner.Id)
	require.Len(t, incidents, 3)
	assert.Equal(t, "API is down", incidents[2].GetString("title"))
	assert.Equal(t, "minor", incidents[2].GetString("impact"))
	assert.Equal(t, []string{mixed.Id}, incidents[2].GetStringSlice("statusPages"))
}

func TestAutoIncidentsForSystems(t *testing.T) {
	env := newMonitorAlertEnv(t)
	bindIncidentHooks(env.hub)
	bindDependencyEvents(env.hub)
	bindSystemIncidentEvents(env.hub)
	router := env.hubMonitorRecord(false)
	env.check(router.Id, true)
	host := env.addSystem(t, "Host", env.owner.Id)
	behind := env.addSystem(t, "Behind", env.owner.Id)
	behind.Set("dependsOn", []string{router.Id})
	require.NoError(t, env.hub.Save(behind))
	_, err := createTestRecord(env.hub, "status_pages", map[string]any{
		"user": env.owner.Id, "slug": "servers", "title": "Servers", "autoIncidents": true,
		"systems": []string{host.Id, behind.Id},
	})
	require.NoError(t, err)

	setStatus := func(system *core.Record, status string) {
		t.Helper()
		record, err := env.hub.FindRecordById("systems", system.Id)
		require.NoError(t, err)
		record.Set("status", status)
		require.NoError(t, env.hub.Save(record))
	}
	setStatus(host, "up")
	setStatus(behind, "up")
	require.Empty(t, incidentsOf(t, env.hub, env.owner.Id))

	setStatus(host, "down")
	incidents := incidentsOf(t, env.hub, env.owner.Id)
	require.Len(t, incidents, 1)
	assert.Equal(t, "Host is down", incidents[0].GetString("title"))
	assert.Equal(t, []string{host.Id}, incidents[0].GetStringSlice("systems"))
	assert.Equal(t, "minor", incidents[0].GetString("impact"))

	// Saves without a status change and down -> pending -> down do not duplicate it.
	setStatus(host, "down")
	setStatus(host, "pending")
	setStatus(host, "down")
	require.Len(t, incidentsOf(t, env.hub, env.owner.Id), 1)

	setStatus(host, "up")
	incident, err := env.hub.FindRecordById("incidents", incidents[0].Id)
	require.NoError(t, err)
	assert.Equal(t, "resolved", incident.GetString("status"))

	// A system behind a down parent opens its incident once the parent
	// recovers, if it is still down.
	env.check(router.Id, false)
	env.check(router.Id, false)
	require.True(t, env.hub.dependencies.wait(5*time.Second))
	setStatus(behind, "down")
	require.Len(t, incidentsOf(t, env.hub, env.owner.Id), 1)
	env.check(router.Id, true)
	require.True(t, env.hub.dependencies.wait(5*time.Second))
	incidents = incidentsOf(t, env.hub, env.owner.Id)
	require.Len(t, incidents, 2)
	assert.Equal(t, "Behind is down", incidents[1].GetString("title"))
}

func TestStatusPageIncidents(t *testing.T) {
	env := newStatusPageTestEnv(t)
	bindIncidentHooks(env.hub)
	monitor := env.monitor(t, map[string]any{"name": "Web"})
	page := env.page(t, "incidents", true, []string{monitor.Id}, nil)
	otherPage := env.page(t, "other", true, []string{monitor.Id}, nil)
	now := statusPageTestNow

	incident := func(title, status, impact string, started, resolved time.Time, pages ...string) *core.Record {
		t.Helper()
		data := map[string]any{
			"user": env.owner.Id, "title": title, "status": status, "impact": impact,
			"statusPages": pages, "startedAt": started, "monitors": []string{monitor.Id},
		}
		if !resolved.IsZero() {
			data["resolvedAt"] = resolved
		}
		return env.create(t, "incidents", data)
	}
	update := func(incident *core.Record, status, message string) {
		t.Helper()
		env.create(t, "incident_updates", map[string]any{
			"incident": incident.Id, "status": status, "message": message, "author": env.owner.Id,
		})
	}
	active := incident("Database slow", "investigating", "major", now.Add(-time.Hour), time.Time{}, page.Id)
	update(active, "investigating", "Looking into it <b>now</b>")
	update(active, "identified", "Found the cause")
	minor := incident("Minor glitch", "monitoring", "minor", now.Add(-30*time.Minute), time.Time{}, page.Id)
	recent := incident("Old outage", "resolved", "critical", now.Add(-72*time.Hour), now.Add(-48*time.Hour), page.Id, otherPage.Id)
	incident("Ancient outage", "resolved", "major", now.Add(-40*24*time.Hour), now.Add(-30*24*time.Hour), page.Id)
	incident("Other page", "investigating", "critical", now, time.Time{}, otherPage.Id)
	// Another user's incident never appears on the owner's page.
	env.create(t, "incidents", map[string]any{
		"user": env.other.Id, "title": "Foreign", "status": "investigating", "impact": "critical", "statusPages": []string{page.Id},
	})
	_ = minor
	_ = recent

	response := env.get(t, "incidents", nil, "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	raw := response.Body.String()
	for _, secret := range []string{active.Id, env.owner.Id, page.Id, "autoKey", "author", "Foreign", "Ancient", "Other page"} {
		assert.NotContains(t, raw, secret)
	}
	var body struct {
		Overall   string `json:"overall"`
		Incidents struct {
			Active []map[string]any `json:"active"`
			Recent []map[string]any `json:"recent"`
		} `json:"incidents"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body.Incidents.Active, 2)
	assert.Equal(t, "Minor glitch", body.Incidents.Active[0]["title"], "most recently started first")
	first := body.Incidents.Active[1]
	assert.ElementsMatch(t, []string{"title", "status", "impact", "startedAt", "updates"}, keys(first))
	assert.Equal(t, "major", first["impact"])
	updates := first["updates"].([]any)
	require.Len(t, updates, 2)
	newest := updates[0].(map[string]any)
	assert.ElementsMatch(t, []string{"status", "message", "created"}, keys(newest))
	assert.Equal(t, "Found the cause", newest["message"], "newest update first")
	assert.Equal(t, "Looking into it <b>now</b>", updates[1].(map[string]any)["message"], "messages are published as written")
	require.Len(t, body.Incidents.Recent, 1)
	assert.Equal(t, "Old outage", body.Incidents.Recent[0]["title"])
	assert.NotEmpty(t, body.Incidents.Recent[0]["resolvedAt"])
	// The monitor is up, but a major incident is active.
	assert.Equal(t, overallDegraded, body.Overall)

	// Without incidents the page has empty lists.
	empty := env.page(t, "empty", true, []string{monitor.Id}, nil)
	data := env.getPage(t, empty.GetString("slug"), nil)
	assert.NotNil(t, data.Incidents.Active)
	assert.NotNil(t, data.Incidents.Recent)
	assert.Equal(t, uptime.StatusUp, data.Overall)
}

func TestOverallWithIncidents(t *testing.T) {
	major := []publicStatusIncident{{Impact: "major"}}
	critical := []publicStatusIncident{{Impact: "critical"}}
	minor := []publicStatusIncident{{Impact: "minor"}, {Impact: "none"}}
	for _, tc := range []struct {
		overall  string
		active   []publicStatusIncident
		expected string
	}{
		{uptime.StatusUp, nil, uptime.StatusUp},
		{uptime.StatusUp, minor, uptime.StatusUp},
		{uptime.StatusUp, major, overallDegraded},
		{uptime.StatusUnknown, critical, overallDegraded},
		{uptime.StatusMaintenance, major, overallDegraded},
		{overallDegraded, critical, overallDegraded},
		{uptime.StatusDown, major, uptime.StatusDown},
	} {
		assert.Equal(t, tc.expected, overallWithIncidents(tc.overall, tc.active), "%s %v", tc.overall, tc.active)
	}
}
