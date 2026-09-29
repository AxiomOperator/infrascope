//go:build testing

package hub

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/henrygd/beszel/internal/hub/monitorsecrets"
	"github.com/henrygd/beszel/internal/hub/systems"
	"github.com/pocketbase/dbx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawMonitorColumn reads a network_monitors column as stored.
func rawMonitorColumn(t *testing.T, env *monitorTestEnv, id, column string) string {
	t.Helper()
	var value string
	require.NoError(t, env.hub.DB().NewQuery("SELECT COALESCE("+column+", '') FROM network_monitors WHERE id = {:id}").
		Bind(dbx.Params{"id": id}).Row(&value))
	return value
}

func TestMonitorSecretsEncryptedAtRest(t *testing.T) {
	env := newMonitorTestEnv(t)
	record := env.createMonitor(t, env.hubMonitor(map[string]any{
		"httpSecrets": map[string]any{"basicPass": "hunter2", "headers": [][2]string{{"X-Token", "tok-123"}}},
	}))
	raw := rawMonitorColumn(t, env, record.Id, "httpSecrets")
	assert.True(t, strings.HasPrefix(raw, `"`+monitorsecrets.Prefix), raw)
	assert.NotContains(t, raw, "hunter2")
	assert.NotContains(t, raw, "tok-123")

	// Owners receive plaintext objects.
	response := env.request(t, http.MethodGet, "/api/collections/network_monitors/records/"+record.Id, env.owner, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body struct {
		HTTPSecrets systems.MonitorHTTPSecrets `json:"httpSecrets"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, "hunter2", body.HTTPSecrets.BasicPass)

	// Updates without secrets keep them; the UI echoing plaintext re-seals them.
	response = env.updateMonitor(t, record.Id, env.owner, map[string]any{"name": "Renamed"})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "hunter2")
	config, err := systems.MonitorConfigFromRecord(env.hub, env.monitorRecord(t, record.Id))
	require.NoError(t, err)
	assert.Equal(t, "hunter2", config.HTTP.BasicPass)
	response = env.updateMonitor(t, record.Id, env.owner, map[string]any{"httpSecrets": map[string]any{"basicPass": "changed"}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	raw = rawMonitorColumn(t, env, record.Id, "httpSecrets")
	assert.NotContains(t, raw, "changed")
	config, err = systems.MonitorConfigFromRecord(env.hub, env.monitorRecord(t, record.Id))
	require.NoError(t, err)
	assert.Equal(t, "changed", config.HTTP.BasicPass)

	// The stored sealed value is accepted unchanged, another one is not.
	response = env.updateMonitor(t, record.Id, env.owner, map[string]any{"httpSecrets": json.RawMessage(raw)})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	other := env.createMonitor(t, env.hubMonitor(map[string]any{"httpSecrets": map[string]any{"basicPass": "x"}}))
	response = env.updateMonitor(t, other.Id, env.owner, map[string]any{"httpSecrets": json.RawMessage(raw)})
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
}

func TestSealStoredMonitorSecrets(t *testing.T) {
	env := newMonitorTestEnv(t)
	record := env.createMonitor(t, env.hubMonitor(nil))
	// A row stored before encryption.
	plaintext := `{"basicUser":"u","basicPass":"legacy-pass"}`
	_, err := env.hub.DB().Update("network_monitors", dbx.Params{"httpSecrets": plaintext}, dbx.HashExp{"id": record.Id}).Execute()
	require.NoError(t, err)
	config, err := systems.MonitorConfigFromRecord(env.hub, env.monitorRecord(t, record.Id))
	require.NoError(t, err)
	assert.Equal(t, "legacy-pass", config.HTTP.BasicPass, "plaintext rows stay readable")

	require.NoError(t, env.hub.sealStoredMonitorSecrets())
	sealed := rawMonitorColumn(t, env, record.Id, "httpSecrets")
	assert.True(t, monitorsecrets.IsSealedJSON(sealed))
	assert.NotContains(t, sealed, "legacy-pass")
	config, err = systems.MonitorConfigFromRecord(env.hub, env.monitorRecord(t, record.Id))
	require.NoError(t, err)
	assert.Equal(t, "legacy-pass", config.HTTP.BasicPass)

	// Idempotent.
	require.NoError(t, env.hub.sealStoredMonitorSecrets())
	assert.Equal(t, sealed, rawMonitorColumn(t, env, record.Id, "httpSecrets"))
}

func TestMonitorSecretsFailClosedWithoutKey(t *testing.T) {
	env := newMonitorTestEnv(t)
	// An unreadable key file.
	require.NoError(t, os.Mkdir(filepath.Join(env.hub.DataDir(), monitorsecrets.KeyFileName), 0o700))
	response := env.request(t, http.MethodPost, "/api/collections/network_monitors/records", env.owner,
		env.hubMonitor(map[string]any{"httpSecrets": map[string]any{"basicPass": "hunter2"}}))
	assert.NotEqual(t, http.StatusOK, response.Code)
	count, err := env.hub.CountRecords("network_monitors")
	require.NoError(t, err)
	assert.Zero(t, count, "plaintext secrets are not stored")
	// Monitors without secrets need no key.
	env.createMonitor(t, env.hubMonitor(nil))
}

func TestMonitorThresholdFields(t *testing.T) {
	env := newMonitorTestEnv(t)
	for _, body := range []map[string]any{{"lossThreshold": 100}, {"lossThreshold": -1}, {"latencyThreshold": -5}} {
		response := env.request(t, http.MethodPost, "/api/collections/network_monitors/records", env.owner, env.hubMonitor(body))
		assert.Equal(t, http.StatusBadRequest, response.Code, body)
	}
	record := env.createMonitor(t, env.hubMonitor(map[string]any{
		"lossThreshold": 5.5, "latencyThreshold": 250, "alertState": map[string]any{"loss": true},
	}))
	assert.Equal(t, 5.5, record.GetFloat("lossThreshold"))
	assert.Equal(t, 250.0, record.GetFloat("latencyThreshold"))
	assert.Empty(t, rawMonitorColumn(t, env, record.Id, "alertState"), "clients cannot set alert state")

	// The alert manager's direct writes survive saves of records loaded earlier.
	stale := env.monitorRecord(t, record.Id)
	_, err := env.hub.DB().Update("network_monitors", dbx.Params{"alertState": `{"latency":true}`, "certState": `{"notified":1}`},
		dbx.HashExp{"id": record.Id}).Execute()
	require.NoError(t, err)
	stale.Set("lastError", "timeout")
	require.NoError(t, env.hub.SaveNoValidate(stale))
	assert.JSONEq(t, `{"latency":true}`, rawMonitorColumn(t, env, record.Id, "alertState"))
	assert.JSONEq(t, `{"notified":1}`, rawMonitorColumn(t, env, record.Id, "certState"))
	response := env.updateMonitor(t, record.Id, env.owner, map[string]any{"alertState": nil, "latencyThreshold": 300})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.NotContains(t, response.Body.String(), "alertState")
	assert.JSONEq(t, `{"latency":true}`, rawMonitorColumn(t, env, record.Id, "alertState"))
}
