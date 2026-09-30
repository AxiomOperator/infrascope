//go:build testing

package hub

import (
	"net/http"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (env *monitorTestEnv) agentMonitor(name string, fields map[string]any) map[string]any {
	body := map[string]any{"system": env.system.Id, "name": name, "target": "1.1.1.1", "protocol": "icmp", "interval": 60, "enabled": true, "notify": true}
	for key, value := range fields {
		body[key] = value
	}
	return body
}

func TestMonitorDependencyValidation(t *testing.T) {
	env := newMonitorTestEnv(t)
	stranger := createMonitorTestUser(t, env.hub, "stranger@example.com", "user")
	foreign := env.addSystem(t, "Foreign", stranger.Id)
	foreignMonitor, err := createTestRecord(env.hub, "network_monitors", map[string]any{
		"system": foreign.Id, "target": "1.1.1.1", "protocol": "icmp", "interval": 60,
	})
	require.NoError(t, err)

	a := env.createMonitor(t, env.agentMonitor("A", map[string]any{"suppressedBy": "client"}))
	assert.Empty(t, a.GetString("suppressedBy"), "suppressedBy is server-managed")
	b := env.createMonitor(t, env.agentMonitor("B", map[string]any{"dependsOn": []string{a.Id}}))
	c := env.createMonitor(t, env.agentMonitor("C", map[string]any{"dependsOn": []string{b.Id}}))
	assert.Equal(t, []string{b.Id}, c.GetStringSlice("dependsOn"))

	var many []string
	for i := range 6 {
		many = append(many, env.createMonitor(t, env.agentMonitor("M"+string(rune('0'+i)), nil)).Id)
	}

	for _, tc := range []struct {
		name string
		id   string
		deps []string
		want string
	}{
		{"self", a.Id, []string{a.Id}, "cannot depend on itself"},
		{"direct cycle", a.Id, []string{b.Id}, "cycle"},
		{"indirect cycle", a.Id, []string{c.Id}, "cycle"},
		{"too many", a.Id, many, "at most 5"},
		{"inaccessible", a.Id, []string{foreignMonitor.Id}, "access to all selected dependencies"},
		{"unknown", a.Id, []string{"doesnotexist123"}, "Unknown dependency"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := env.updateMonitor(t, tc.id, env.owner, map[string]any{"dependsOn": tc.deps})
			assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), tc.want)
		})
	}

	// Superusers still cannot create cycles.
	record, err := env.hub.FindRecordById("network_monitors", a.Id)
	require.NoError(t, err)
	record.Set("dependsOn", []string{c.Id})
	err = prepareMonitor(env.hub, record, record.Original(), nil, true)
	assert.ErrorContains(t, err, "cycle")

	// Chains deeper than the limit are rejected.
	prev := a.Id
	var chain []string
	for i := range maxDependencyDepth + 1 {
		chain = append(chain, env.createMonitor(t, env.agentMonitor("D"+string(rune('a'+i)), map[string]any{"dependsOn": []string{prev}})).Id)
		prev = chain[len(chain)-1]
	}
	last := env.createMonitor(t, env.agentMonitor("Last", nil))
	response := env.updateMonitor(t, last.Id, env.owner, map[string]any{"dependsOn": []string{prev}})
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "deep")
}

func TestSystemDependencies(t *testing.T) {
	env := newMonitorTestEnv(t)
	bindDependencyEvents(env.hub)
	stranger := createMonitorTestUser(t, env.hub, "stranger@example.com", "user")
	foreign := env.addSystem(t, "Foreign", stranger.Id)
	foreignMonitor, err := createTestRecord(env.hub, "network_monitors", map[string]any{
		"system": foreign.Id, "target": "1.1.1.1", "protocol": "icmp", "interval": 60,
	})
	require.NoError(t, err)
	router := env.createMonitor(t, env.agentMonitor("Router", nil))
	host := env.addSystem(t, "Host", env.owner.Id)

	update := func(auth *core.Record, body map[string]any) (int, string) {
		response := env.request(t, http.MethodPatch, "/api/collections/systems/records/"+host.Id, auth, body)
		return response.Code, response.Body.String()
	}
	code, body := update(env.owner, map[string]any{"dependsOn": []string{foreignMonitor.Id}})
	assert.Equal(t, http.StatusBadRequest, code, body)
	assert.Contains(t, body, "access to all selected dependencies")
	code, body = update(env.owner, map[string]any{"dependsOn": []string{router.Id}, "suppressedBy": "client"})
	require.Equal(t, http.StatusOK, code, body)
	host, err = env.hub.FindRecordById("systems", host.Id)
	require.NoError(t, err)
	assert.Equal(t, []string{router.Id}, host.GetStringSlice("dependsOn"))
	assert.Empty(t, host.GetString("suppressedBy"))

	suppressedBy := func() string {
		t.Helper()
		require.True(t, env.hub.dependencies.wait(5*time.Second))
		record, err := env.hub.FindRecordById("systems", host.Id)
		require.NoError(t, err)
		return record.GetString("suppressedBy")
	}
	checks := time.Now().UnixMilli() - 60_000
	check := func(ok bool) {
		checks++
		event := monitor.CheckEvent{At: checks, ResponseUs: 1000}
		if !ok {
			event = monitor.CheckEvent{At: checks, ResponseUs: -1, Err: "timeout"}
		}
		env.hub.uptime.Observe(router.Id, []monitor.CheckEvent{event})
	}

	check(true)
	assert.Empty(t, suppressedBy())
	check(false)
	assert.Equal(t, "Router", suppressedBy())
	assert.True(t, env.hub.systemSuppressed(host.Id))
	check(true)
	assert.Empty(t, suppressedBy())
	assert.False(t, env.hub.systemSuppressed(host.Id))
}
