//go:build testing

package hub

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/systems"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addSystem stores a paused system with the given users.
func (env *monitorTestEnv) addSystem(t *testing.T, name string, users ...string) *core.Record {
	t.Helper()
	system, err := createTestRecord(env.hub, "systems", map[string]any{
		"name": name, "host": "localhost", "port": "45877", "status": "paused", "users": users,
	})
	require.NoError(t, err)
	return system
}

func (env *monitorTestEnv) postMonitor(t *testing.T, auth *core.Record, body map[string]any) (int, string) {
	t.Helper()
	response := env.request(t, http.MethodPost, "/api/collections/network_monitors/records", auth, body)
	return response.Code, response.Body.String()
}

func TestMonitorLocationsValidation(t *testing.T) {
	env := newMonitorTestEnv(t)
	other := env.addSystem(t, "Other", env.owner.Id)
	stranger := createMonitorTestUser(t, env.hub, "stranger@example.com", "user")
	foreign := env.addSystem(t, "Foreign", stranger.Id)
	base := func(fields map[string]any) map[string]any {
		// Create rules check the submitted primary system.
		body := map[string]any{"system": env.system.Id, "target": "1.1.1.1", "protocol": "icmp", "interval": 60, "enabled": true}
		for key, value := range fields {
			body[key] = value
		}
		return body
	}

	record := env.createMonitor(t, base(map[string]any{
		"system": env.system.Id, "locations": []string{"hub", env.system.Id, other.Id}, "users": []string{env.owner.Id},
	}))
	assert.Equal(t, env.system.Id, record.GetString("system"), "the first agent location is the primary")
	assert.Equal(t, []string{env.system.Id, other.Id}, record.GetStringSlice("locationSystems"))
	assert.Equal(t, 2, record.GetInt("quorum"), "the default quorum is a majority")
	assert.Equal(t, []string{env.owner.Id}, record.GetStringSlice("users"))

	agents := env.createMonitor(t, base(map[string]any{"system": other.Id, "locations": []string{other.Id, env.system.Id}, "quorum": 1, "users": []string{env.owner.Id}}))
	assert.Equal(t, other.Id, agents.GetString("system"))
	assert.Equal(t, 1, agents.GetInt("quorum"))
	assert.Empty(t, agents.GetStringSlice("users"), "users are only kept with the hub as a location")

	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"inaccessible system", base(map[string]any{"locations": []string{env.system.Id, foreign.Id}}), "access to all selected locations"},
		{"unknown system", base(map[string]any{"locations": []string{env.system.Id, "nope"}}), "Unknown location"},
		{"quorum above locations", base(map[string]any{"locations": []string{env.system.Id, other.Id}, "quorum": 3}), "Quorum must be between 1 and 2"},
		{"hub without users", base(map[string]any{"locations": []string{"hub", env.system.Id}}), "at least one user"},
		{"push on several locations", map[string]any{"system": env.system.Id, "protocol": "push", "interval": 60, "locations": []string{"hub", env.system.Id}, "users": []string{env.owner.Id}}, "hub only"},
		{"docker on the hub", base(map[string]any{"protocol": "docker", "target": "web", "locations": []string{"hub", env.system.Id}, "users": []string{env.owner.Id}}), "cannot run on the hub"},
		{"too many locations", base(map[string]any{"locations": []string{"hub", env.system.Id, other.Id, "a", "b", "c", "d", "e", "f", "g", "h"}}), "at most 10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := env.postMonitor(t, env.owner, tc.body)
			assert.Equal(t, http.StatusBadRequest, code, body)
			assert.Contains(t, body, tc.want)
		})
	}

	// Clients that only know "system" move a monitor between single locations.
	single := env.createMonitor(t, base(map[string]any{"system": env.system.Id}))
	assert.JSONEq(t, `["`+env.system.Id+`"]`, single.GetString("locations"))
	response := env.updateMonitor(t, single.Id, env.owner, map[string]any{"system": other.Id})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	single, err := env.hub.FindRecordById("network_monitors", single.Id)
	require.NoError(t, err)
	assert.JSONEq(t, `["`+other.Id+`"]`, single.GetString("locations"))
	assert.Equal(t, []string{other.Id}, single.GetStringSlice("locationSystems"))

	// A quorum left over from more locations falls back to the default.
	response = env.updateMonitor(t, record.Id, env.owner, map[string]any{"locations": []string{"hub"}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	record, err = env.hub.FindRecordById("network_monitors", record.Id)
	require.NoError(t, err)
	assert.Equal(t, 1, record.GetInt("quorum"))
	assert.Empty(t, record.GetString("system"))
}

func TestMonitorLocationAccess(t *testing.T) {
	env := newMonitorTestEnv(t)
	member := createMonitorTestUser(t, env.hub, "member@example.com", "user")
	outsider := createMonitorTestUser(t, env.hub, "outsider@example.com", "user")
	other := env.addSystem(t, "Other", env.owner.Id, member.Id)
	record := env.createMonitor(t, map[string]any{
		"system": env.system.Id, "locations": []string{env.system.Id, other.Id}, "target": "https://example.com", "protocol": "http", "interval": 60,
		"httpSecrets": map[string]any{"basicPass": "hunter2"},
	})
	_, err := createTestRecord(env.hub, "network_monitor_stats", map[string]any{
		"system": other.Id, "monitor": record.Id, "type": "1m", "created": time.Now().UnixMilli(),
	})
	require.NoError(t, err)

	view := func(t *testing.T, auth *core.Record) (int, string) {
		t.Helper()
		response := env.request(t, http.MethodGet, "/api/collections/network_monitors/records/"+record.Id, auth, nil)
		return response.Code, response.Body.String()
	}
	stats := func(t *testing.T, auth *core.Record) int {
		t.Helper()
		response := env.request(t, http.MethodGet, "/api/collections/network_monitor_stats/records", auth, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var list struct{ TotalItems int }
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &list))
		return list.TotalItems
	}

	// A member of a non-primary location can view the monitor and its stats,
	// without its secrets, and cannot change or delete it.
	code, body := view(t, member)
	assert.Equal(t, http.StatusOK, code, body)
	assert.NotContains(t, body, "hunter2")
	assert.Equal(t, 1, stats(t, member))
	response := env.updateMonitor(t, record.Id, member, map[string]any{"name": "mine"})
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	response = env.request(t, http.MethodDelete, "/api/collections/network_monitors/records/"+record.Id, member, nil)
	assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())

	code, body = view(t, env.owner)
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "hunter2")

	code, _ = view(t, outsider)
	assert.Equal(t, http.StatusNotFound, code)
	assert.Zero(t, stats(t, outsider))

	t.Run("SHARE_ALL_SYSTEMS", func(t *testing.T) {
		t.Setenv("SHARE_ALL_SYSTEMS", "true")
		require.NoError(t, env.hub.SetCollectionAuthSettings())
		t.Cleanup(func() {
			t.Setenv("SHARE_ALL_SYSTEMS", "")
			_ = env.hub.SetCollectionAuthSettings()
		})
		code, body := view(t, outsider)
		assert.Equal(t, http.StatusOK, code, body)
		assert.NotContains(t, body, "hunter2")
		assert.Equal(t, 1, stats(t, outsider))
	})

	response = env.request(t, http.MethodDelete, "/api/collections/network_monitors/records/"+record.Id, env.owner, nil)
	assert.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
}

func TestMonitorLocationChangesSyncAgents(t *testing.T) {
	env := newMonitorTestEnv(t)
	other := env.addSystem(t, "Other", env.owner.Id)
	agentA := connectFakeMonitorAgent(t, env.hub, env.system.Id)
	agentB := connectFakeMonitorAgent(t, env.hub, other.Id)

	record := env.createMonitor(t, map[string]any{
		"system": env.system.Id, "locations": []string{env.system.Id}, "target": "1.1.1.1", "protocol": "icmp", "interval": 60, "enabled": true,
	})

	// Adding a location runs the monitor there too.
	response := env.updateMonitor(t, record.Id, env.owner, map[string]any{"locations": []string{env.system.Id, other.Id}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	req := nextSyncRequest(t, agentA)
	assert.Equal(t, monitor.SyncActionUpsert, req.Action)
	assert.False(t, req.RunNow)
	req = nextSyncRequest(t, agentB)
	assert.Equal(t, monitor.SyncActionUpsert, req.Action)
	assert.True(t, req.RunNow, "a monitor new to the agent runs immediately")
	for _, systemID := range []string{env.system.Id, other.Id} {
		configs, err := env.hub.sm.GetMonitorConfigsForSystem(systemID)
		require.NoError(t, err)
		require.Len(t, configs, 1, systemID)
		assert.Equal(t, record.Id, configs[0].ID)
	}

	// Removing a location deletes it there; the hub now runs it too.
	response = env.updateMonitor(t, record.Id, env.owner, map[string]any{
		"locations": []string{"hub", other.Id}, "users": []string{env.owner.Id},
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	req = nextSyncRequest(t, agentA)
	assert.Equal(t, monitor.SyncActionDelete, req.Action)
	assert.Equal(t, record.Id, req.Config.ID)
	req = nextSyncRequest(t, agentB)
	assert.Equal(t, monitor.SyncActionUpsert, req.Action)
	configs, err := env.hub.sm.GetMonitorConfigsForSystem(env.system.Id)
	require.NoError(t, err)
	assert.Empty(t, configs)

	// Disabling deletes it from every agent location.
	response = env.updateMonitor(t, record.Id, env.owner, map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	req = nextSyncRequest(t, agentB)
	assert.Equal(t, monitor.SyncActionDelete, req.Action)

	// Deleting the monitor removes it from its agents.
	stored, err := env.hub.FindRecordById("network_monitors", record.Id)
	require.NoError(t, err)
	require.NoError(t, env.hub.Delete(stored))
	req = nextSyncRequest(t, agentB)
	assert.Equal(t, monitor.SyncActionDelete, req.Action)
	assert.Empty(t, agentA)
	assert.Empty(t, agentB)
}

func TestSaveMonitorResultsFromLocations(t *testing.T) {
	env := newMonitorTestEnv(t)
	other := env.addSystem(t, "Other", env.owner.Id)
	third := env.addSystem(t, "Third", env.owner.Id)
	record := env.createMonitor(t, map[string]any{
		"system": env.system.Id, "locations": []string{env.system.Id, other.Id}, "target": "1.1.1.1", "protocol": "icmp", "interval": 60, "enabled": true,
	})
	save := func(systemID string, result monitor.Result) {
		t.Helper()
		require.NoError(t, systems.SaveMonitorResults(env.hub, systemID, map[string]monitor.Result{record.Id: result}, nil, map[string]int64{}))
	}
	save(env.system.Id, monitor.Result{AvgResponse: 10_000, AvgResponse1h: 12_000, MinResponse1h: 5_000, MaxResponse1h: 20_000, PacketLoss1h: 0, LastProbeAt: 1, TotalCount: 1, SuccessCount: 1})
	save(other.Id, monitor.Result{AvgResponse: 30_000, AvgResponse1h: 28_000, MinResponse1h: 8_000, MaxResponse1h: 90_000, PacketLoss1h: 50, LastProbeAt: 1, TotalCount: 2, SuccessCount: 1})
	// Systems that are not a location cannot write results.
	save(third.Id, monitor.Result{AvgResponse: 1, PacketLoss1h: 100, LastProbeAt: 1, TotalCount: 1})
	save("", monitor.Result{AvgResponse: 1, PacketLoss1h: 100, LastProbeAt: 1, TotalCount: 1})

	stored, err := env.hub.FindRecordById("network_monitors", record.Id)
	require.NoError(t, err)
	assert.Equal(t, 20_000.0, stored.GetFloat("res"), "response times are averaged")
	assert.Equal(t, 20_000.0, stored.GetFloat("resAvg1h"))
	assert.Equal(t, 5_000.0, stored.GetFloat("resMin1h"))
	assert.Equal(t, 90_000.0, stored.GetFloat("resMax1h"))
	assert.Equal(t, 50.0, stored.GetFloat("loss1h"), "loss is the highest of any location")

	rows, err := env.hub.FindAllRecords("network_monitor_stats", dbx.HashExp{"monitor": record.Id})
	require.NoError(t, err)
	locations := map[string]int{}
	for _, row := range rows {
		locations[row.GetString("system")] = row.GetInt("total_count")
	}
	assert.Equal(t, map[string]int{env.system.Id: 1, other.Id: 2}, locations, "one stats row per location")
}

func TestDeletedSystemLeavesMonitorLocations(t *testing.T) {
	env := newMonitorTestEnv(t)
	other := env.addSystem(t, "Other", env.owner.Id)
	multi := env.createMonitor(t, map[string]any{
		"system": env.system.Id, "locations": []string{env.system.Id, other.Id, "hub"}, "users": []string{env.owner.Id},
		"target": "1.1.1.1", "protocol": "icmp", "interval": 60, "quorum": 3,
	})
	single := env.createMonitor(t, map[string]any{"system": env.system.Id, "target": "1.1.1.1", "protocol": "icmp", "interval": 60})

	require.NoError(t, env.hub.Delete(env.system))
	_, err := env.hub.FindRecordById("network_monitors", single.Id)
	assert.Error(t, err, "a monitor of only that system is deleted with it")
	multi, err = env.hub.FindRecordById("network_monitors", multi.Id)
	require.NoError(t, err)
	assert.JSONEq(t, `["`+other.Id+`","hub"]`, multi.GetString("locations"))
	assert.Equal(t, other.Id, multi.GetString("system"))
	assert.Equal(t, []string{other.Id}, multi.GetStringSlice("locationSystems"))
	assert.Equal(t, 2, multi.GetInt("quorum"), "a quorum above the remaining locations falls back to the default")
}
