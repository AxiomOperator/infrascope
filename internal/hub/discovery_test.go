//go:build testing

package hub

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	esystem "github.com/henrygd/beszel/internal/entities/system"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDiscoveryTestEnv returns a monitor test env whose system has discovery on.
func newDiscoveryTestEnv(t *testing.T, traefik bool) *monitorTestEnv {
	t.Helper()
	env := newMonitorTestEnv(t)
	bindDockerDiscoveryEvents(env.hub)
	env.system.Set("autoDiscover", true)
	env.system.Set("autoDiscoverTraefik", traefik)
	env.system.Set("host", "10.0.0.5")
	require.NoError(t, env.hub.Save(env.system))
	return env
}

func managedMonitors(t *testing.T, app core.App, systemID string) map[string]*core.Record {
	t.Helper()
	records, err := app.FindAllRecords("network_monitors", dbx.HashExp{"managedSystem": systemID})
	require.NoError(t, err)
	byKey := map[string]*core.Record{}
	for _, record := range records {
		byKey[record.GetString("managedKey")] = record
	}
	return byKey
}

func discoveryErrors(t *testing.T, app core.App, systemID string) []discoveryError {
	t.Helper()
	record, err := app.FindRecordById("systems", systemID)
	require.NoError(t, err)
	var problems []discoveryError
	if raw := record.GetString("discoveryErrors"); raw != "" && raw != "null" {
		require.NoError(t, json.Unmarshal([]byte(raw), &problems))
	}
	return problems
}

func TestBuildDiscoveredSpec(t *testing.T) {
	env := newDiscoveryTestEnv(t, false)
	tests := []struct {
		name    string
		dm      esystem.DiscoveredMonitor
		wantErr string
		fields  map[string]any
		managed []string
	}{
		{
			name:    "http from published port",
			dm:      esystem.DiscoveredMonitor{Container: "web", Key: "default", Port: 8080, Labels: map[string]string{"type": "http"}},
			fields:  map[string]any{"protocol": "http", "target": "http://localhost:8080"},
			managed: []string{"locations", "protocol", "target"},
		},
		{
			name: "hub location uses the system host and users",
			dm: esystem.DiscoveredMonitor{Container: "db", Key: "pg", Port: 15432,
				Labels: map[string]string{"type": "postgres", "location": "hub", "interval": "2m", "name": "DB"}},
			fields:  map[string]any{"protocol": "postgres", "target": "10.0.0.5", "port": 15432, "interval": 120, "name": "DB", "users": []string{env.owner.Id}},
			managed: []string{"interval", "locations", "name", "port", "protocol", "target"},
		},
		{
			name:    "docker defaults to the container",
			dm:      esystem.DiscoveredMonitor{Container: "web", Key: "default", Labels: map[string]string{"type": "docker"}},
			fields:  map[string]any{"protocol": "docker", "target": "web"},
			managed: []string{"locations", "protocol", "target"},
		},
		{
			name:    "type inferred from url",
			dm:      esystem.DiscoveredMonitor{Container: "web", Key: "a", Labels: map[string]string{"target": "https://x.example", "keyword": "ok", "accepted_codes": "200-299, 301", "notify": "false"}},
			fields:  map[string]any{"protocol": "http", "target": "https://x.example", "notify": false},
			managed: []string{"acceptedCodes", "keyword", "locations", "notify", "protocol", "target"},
		},
		{name: "bad id", dm: esystem.DiscoveredMonitor{Container: "web", Key: "a b", Labels: map[string]string{"type": "icmp"}}, wantErr: "invalid monitor id"},
		{name: "missing type", dm: esystem.DiscoveredMonitor{Container: "web", Key: "a", Labels: map[string]string{"target": "x"}}, wantErr: "type is required"},
		{name: "unknown field", dm: esystem.DiscoveredMonitor{Container: "web", Key: "a", Labels: map[string]string{"type": "tcp", "colour": "red"}}, wantErr: "unknown label field"},
		{name: "unknown type", dm: esystem.DiscoveredMonitor{Container: "web", Key: "a", Labels: map[string]string{"type": "gopher"}}, wantErr: "unknown type"},
		{name: "push", dm: esystem.DiscoveredMonitor{Container: "web", Key: "a", Labels: map[string]string{"type": "push"}}, wantErr: "push"},
		{name: "bad location", dm: esystem.DiscoveredMonitor{Container: "web", Key: "a", Labels: map[string]string{"type": "icmp", "location": "moon"}}, wantErr: "invalid location"},
		{name: "http without port", dm: esystem.DiscoveredMonitor{Container: "web", Key: "a", Labels: map[string]string{"type": "http"}}, wantErr: "publishes no TCP port"},
		{name: "bad interval", dm: esystem.DiscoveredMonitor{Container: "web", Key: "a", Labels: map[string]string{"type": "icmp", "interval": "soon"}}, wantErr: "invalid interval"},
		{name: "keyword on tcp", dm: esystem.DiscoveredMonitor{Container: "web", Key: "a", Port: 1, Labels: map[string]string{"type": "tcp", "keyword": "x"}}, wantErr: "only applies to http"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := buildDiscoveredSpec(env.system, tt.dm)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			for field, want := range tt.fields {
				assert.Equal(t, want, spec.fields[field], field)
			}
			assert.Equal(t, tt.managed, spec.managed)
		})
	}
}

func TestReconcileDockerDiscovery(t *testing.T) {
	env := newDiscoveryTestEnv(t, false)
	app := env.hub
	systemID := env.system.Id
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	// An unmanaged monitor on the system is never touched.
	unmanaged := env.createMonitor(t, map[string]any{"system": systemID, "name": "web", "target": "1.1.1.1", "protocol": "icmp", "interval": 60, "enabled": true})

	discovery := &esystem.Discovery{Monitors: []esystem.DiscoveredMonitor{
		{Container: "web", Key: "default", Port: 8080, Labels: map[string]string{"type": "http"}},
		{Container: "db", Key: "pg", Port: 5432, Labels: map[string]string{"type": "postgres", "location": "hub", "name": "Postgres"}},
		{Container: "bad", Key: "default", Labels: map[string]string{"type": "gopher"}},
		{Container: "proxy", Key: "traefik.app.app.example.com", Traefik: true, Labels: map[string]string{"type": "http", "target": "https://app.example.com"}},
	}}
	require.NoError(t, env.hub.reconcileDockerDiscovery(systemID, discovery, start))

	monitors := managedMonitors(t, app, systemID)
	require.Len(t, monitors, 2, "traefik monitors need autoDiscoverTraefik")
	web := monitors["docker:web:default"]
	require.NotNil(t, web)
	assert.Equal(t, "web 2", web.GetString("name"), "the name of the unmanaged monitor is taken")
	assert.Equal(t, "http://localhost:8080", web.GetString("target"))
	assert.Equal(t, systemID, web.GetString("system"))
	assert.Equal(t, managedByDocker, web.GetString("managedBy"))
	assert.True(t, web.GetBool("enabled"))
	assert.True(t, web.GetBool("notify"))
	assert.Equal(t, 60, web.GetInt("interval"))
	assert.Equal(t, []string{"locations", "protocol", "target"}, web.GetStringSlice("managedFields"))

	pg := monitors["docker:db:pg"]
	require.NotNil(t, pg)
	assert.Equal(t, "", pg.GetString("system"), "a hub monitor")
	assert.Equal(t, []string{env.owner.Id}, pg.GetStringSlice("users"))
	assert.Equal(t, "10.0.0.5", pg.GetString("target"))

	problems := discoveryErrors(t, app, systemID)
	require.Len(t, problems, 1)
	assert.Equal(t, "bad", problems[0].Container)
	assert.Contains(t, problems[0].Error, "unknown type")

	// A user setting survives label updates it is not controlled by.
	web.Set("notify", false)
	web.Set("latencyThreshold", 500)
	require.NoError(t, app.Save(web))
	discovery.Monitors[0].Labels = map[string]string{"type": "http", "interval": "30"}
	discovery.Monitors[2].Labels = map[string]string{"type": "icmp"}
	require.NoError(t, env.hub.reconcileDockerDiscovery(systemID, discovery, start))
	web, err := app.FindRecordById("network_monitors", web.Id)
	require.NoError(t, err)
	assert.Equal(t, 30, web.GetInt("interval"))
	assert.False(t, web.GetBool("notify"))
	assert.Equal(t, 500.0, web.GetFloat("latencyThreshold"))
	assert.Contains(t, web.GetStringSlice("managedFields"), "interval")
	assert.Empty(t, discoveryErrors(t, app, systemID), "fixed labels clear the errors")
	assert.Len(t, managedMonitors(t, app, systemID), 3)

	// Traefik routers become monitors once enabled.
	env.system.Set("autoDiscoverTraefik", true)
	require.NoError(t, app.Save(env.system))
	require.NoError(t, env.hub.reconcileDockerDiscovery(systemID, discovery, start))
	traefik := managedMonitors(t, app, systemID)["docker:proxy:traefik.app.app.example.com"]
	require.NotNil(t, traefik)
	assert.Equal(t, "app.example.com", traefik.GetString("name"))

	// A vanished container disables its monitors, which are deleted after the grace period.
	discovery.Monitors = discovery.Monitors[1:]
	require.NoError(t, env.hub.reconcileDockerDiscovery(systemID, discovery, start))
	web, err = app.FindRecordById("network_monitors", web.Id)
	require.NoError(t, err)
	assert.False(t, web.GetBool("enabled"))
	assert.Equal(t, start, web.GetDateTime("managedMissingSince").Time())

	require.NoError(t, env.hub.reconcileDockerDiscovery(systemID, discovery, start.Add(discoveryMissingGrace-time.Minute)))
	_, err = app.FindRecordById("network_monitors", web.Id)
	require.NoError(t, err, "kept during the grace period")

	// A container that is back re-enables its monitor.
	back := &esystem.Discovery{Monitors: append([]esystem.DiscoveredMonitor{
		{Container: "web", Key: "default", Port: 8080, Labels: map[string]string{"type": "http"}},
	}, discovery.Monitors...)}
	require.NoError(t, env.hub.reconcileDockerDiscovery(systemID, back, start.Add(time.Hour)))
	web, err = app.FindRecordById("network_monitors", web.Id)
	require.NoError(t, err)
	assert.True(t, web.GetBool("enabled"))
	assert.True(t, web.GetDateTime("managedMissingSince").IsZero())

	require.NoError(t, env.hub.reconcileDockerDiscovery(systemID, discovery, start.Add(2*time.Hour)))
	require.NoError(t, env.hub.reconcileDockerDiscovery(systemID, discovery, start.Add(2*time.Hour+discoveryMissingGrace)))
	_, err = app.FindRecordById("network_monitors", web.Id)
	require.Error(t, err, "deleted after the grace period")

	unmanaged, err = app.FindRecordById("network_monitors", unmanaged.Id)
	require.NoError(t, err)
	assert.True(t, unmanaged.GetBool("enabled"))
	assert.Equal(t, "web", unmanaged.GetString("name"))
	assert.Empty(t, unmanaged.GetString("managedBy"))
}

func TestReconcileDockerDiscoveryOptIn(t *testing.T) {
	env := newDiscoveryTestEnv(t, true)
	env.system.Set("autoDiscover", false)
	require.NoError(t, env.hub.Save(env.system))
	discovery := &esystem.Discovery{Monitors: []esystem.DiscoveredMonitor{
		{Container: "web", Key: "default", Port: 8080, Labels: map[string]string{"type": "http"}},
	}}
	require.NoError(t, env.hub.reconcileDockerDiscovery(env.system.Id, discovery, time.Now()))
	env.hub.ReconcileDockerDiscovery(env.system, discovery)
	assert.Empty(t, managedMonitors(t, env.hub, env.system.Id))
	assert.Empty(t, env.hub.discovery.states, "nothing is scheduled")
}

func TestDockerDiscoveryRateLimit(t *testing.T) {
	d := newDockerDiscovery()
	now := time.Now()
	gen, ok := d.begin("s", 1, now)
	require.True(t, ok)
	_, ok = d.begin("s", 2, now)
	assert.False(t, ok, "one reconcile per system at a time")
	d.end("s", 1, gen, now, true)
	_, ok = d.begin("s", 1, now.Add(time.Minute))
	assert.False(t, ok, "unchanged payload")
	gen, ok = d.begin("s", 2, now.Add(time.Minute))
	require.True(t, ok, "changed payload")
	d.invalidate("s")
	d.end("s", 2, gen, now, true)
	gen, ok = d.begin("s", 2, now.Add(2*time.Minute))
	require.True(t, ok, "invalidated during the run")
	d.end("s", 2, gen, now.Add(2*time.Minute), true)
	_, ok = d.begin("s", 2, now.Add(2*time.Minute+discoveryRefresh))
	assert.True(t, ok, "refreshed periodically")
}

func TestManagedMonitorAPIProtection(t *testing.T) {
	env := newDiscoveryTestEnv(t, false)
	discovery := &esystem.Discovery{Monitors: []esystem.DiscoveredMonitor{
		{Container: "web", Key: "default", Port: 8080, Labels: map[string]string{"type": "http", "keyword": "ok", "name": "Web"}},
	}}
	require.NoError(t, env.hub.reconcileDockerDiscovery(env.system.Id, discovery, time.Now()))
	web := managedMonitors(t, env.hub, env.system.Id)["docker:web:default"]
	require.NotNil(t, web)

	response := env.updateMonitor(t, web.Id, env.owner, map[string]any{
		"name": "Renamed", "target": "http://evil.example", "notify": false,
		"http":      map[string]any{"keyword": "changed", "method": "HEAD"},
		"managedBy": "", "managedKey": "x",
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	web, err := env.hub.FindRecordById("network_monitors", web.Id)
	require.NoError(t, err)
	assert.Equal(t, "Web", web.GetString("name"))
	assert.Equal(t, "http://localhost:8080", web.GetString("target"))
	assert.False(t, web.GetBool("notify"), "fields without labels stay editable")
	var options map[string]any
	require.NoError(t, json.Unmarshal([]byte(web.GetString("http")), &options))
	assert.Equal(t, "ok", options["keyword"])
	assert.Equal(t, "HEAD", options["method"])
	assert.Equal(t, managedByDocker, web.GetString("managedBy"))
	assert.Equal(t, "docker:web:default", web.GetString("managedKey"))

	// Clients cannot write discovery errors.
	response = env.request(t, http.MethodPatch, "/api/collections/systems/records/"+env.system.Id, env.owner,
		map[string]any{"discoveryErrors": []map[string]string{{"error": "x"}}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Empty(t, discoveryErrors(t, env.hub, env.system.Id))

	// Deleting a managed monitor makes the next payload recreate it.
	env.hub.discovery.states[env.system.Id] = &discoveryState{hash: 5, at: time.Now()}
	response = env.request(t, http.MethodDelete, "/api/collections/network_monitors/records/"+web.Id, env.owner, nil)
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	assert.Zero(t, env.hub.discovery.states[env.system.Id].hash)
}
