//go:build testing

package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/systems"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func kumaFixture(t *testing.T) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/uptime_kuma_backup.json")
	require.NoError(t, err)
	return data
}

func (env *monitorTestEnv) kumaImport(t *testing.T, handler http.Handler, auth *core.Record, body any) (*httptest.ResponseRecorder, kumaImportResponse) {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/api/beszel/import/uptime-kuma", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	if auth != nil {
		request.Header.Set("Authorization", authToken(t, auth))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result kumaImportResponse
	if response.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	}
	return response, result
}

func notesByName(notes []kumaImportNote, reason bool) map[string][]string {
	out := map[string][]string{}
	for _, note := range notes {
		text := note.Change
		if reason {
			text = note.Reason
		}
		out[note.Name] = append(out[note.Name], text)
	}
	return out
}

func TestKumaImportDryRun(t *testing.T) {
	env := newMonitorTestEnv(t)
	handler := env.apiHandler(t)
	response, result := env.kumaImport(t, handler, env.owner, map[string]any{"backup": kumaFixture(t), "dryRun": true})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	assert.Equal(t, 8, result.Created)
	require.Len(t, result.Monitors, 8)
	assert.Equal(t, kumaImportedMonitor{Name: "Website", Protocol: "http", Target: "https://example.com"}, result.Monitors[0])
	assert.Equal(t, kumaImportedMonitor{Name: "SSH", Protocol: "tcp", Target: "10.0.0.5"}, result.Monitors[3])
	assert.Equal(t, kumaImportedMonitor{Name: "Backup job", Protocol: "push", Target: ""}, result.Monitors[6])
	assert.Equal(t, map[string][]string{
		"Network":       {"groups are not imported"},
		"Postgres":      {"unsupported type postgres"},
		"Container":     {"unsupported type docker"},
		"Inverted":      {"upside down mode is not supported"},
		"NTLM intranet": {"unsupported authentication method ntlm"},
		"Bad method":    {`http options: unsupported method "TRACE"`},
	}, notesByName(result.Skipped, true))
	adjusted := notesByName(result.Adjusted, false)
	assert.Equal(t, []string{"retries lowered from 15 to 10"}, adjusted["Health keyword"])
	assert.Equal(t, []string{"timeout lowered from 96s to 60s", "max redirects lowered from 50 to 20"}, adjusted["API status"])
	assert.Equal(t, []string{"DNS record type MX is not supported; the host name is resolved instead"}, adjusted["DNS"])
	assert.Equal(t, []string{"push URL is now /api/beszel/push/<token> (same token)"}, adjusted["Backup job"])
	assert.Equal(t, []string{"new push token generated; update the push URL"}, adjusted["Cron heartbeat"])

	count, err := env.hub.CountRecords("network_monitors")
	require.NoError(t, err)
	assert.Zero(t, count, "a dry run creates nothing")

	// Arrays are never null.
	response, _ = env.kumaImport(t, handler, env.owner, map[string]any{"backup": map[string]any{"monitorList": []any{}}, "dryRun": true})
	require.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"created":0,"skipped":[],"adjusted":[],"monitors":[]}`, response.Body.String())
}

func TestKumaImportCreatesHubMonitors(t *testing.T) {
	t.Setenv("HUB_MONITOR_MIN_INTERVAL", "30")
	env := newMonitorTestEnv(t)
	handler := env.apiHandler(t)
	// The backup may also be sent as the text of the file.
	response, result := env.kumaImport(t, handler, env.owner, map[string]any{"backup": string(kumaFixture(t))})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, 8, result.Created)
	assert.Contains(t, notesByName(result.Adjusted, false)["Health keyword"], "interval raised from 20s to 30s")

	find := func(name string) *core.Record {
		t.Helper()
		record, err := env.hub.FindFirstRecordByFilter("network_monitors", "name = {:name}", dbx.Params{"name": name})
		require.NoError(t, err, name)
		assert.Equal(t, []string{env.owner.Id}, record.GetStringSlice("users"))
		assert.Empty(t, record.GetString("system"))
		return record
	}
	website := find("Website")
	assert.True(t, website.GetBool("enabled"))
	assert.True(t, website.GetBool("notify"))
	assert.Equal(t, 2, website.GetInt("retries"))
	assert.Equal(t, 30, website.GetInt("retryInterval"))
	assert.Equal(t, 48, website.GetInt("timeout"))
	assert.Equal(t, kumaCertExpiryDays, website.GetInt("certExpiryDays"))
	assert.NotContains(t, website.GetString("httpSecrets"), "hunter2", "imported secrets are encrypted")
	config, err := systems.MonitorConfigFromRecord(env.hub, website)
	require.NoError(t, err)
	assert.Equal(t, &monitor.HTTPOptions{
		Headers:      [][2]string{{"Accept", "application/json"}, {"X-Api-Key", "kuma-secret-key"}},
		MaxRedirects: 10, BasicUser: "admin", BasicPass: "hunter2",
	}, config.HTTP)

	keyword := find("Health keyword")
	assert.False(t, keyword.GetBool("notify"))
	config, err = systems.MonitorConfigFromRecord(env.hub, keyword)
	require.NoError(t, err)
	assert.Equal(t, &monitor.HTTPOptions{
		Method: "POST", Body: `{"ping":true}`, AcceptedCodes: []string{"200-299", "301"}, MaxRedirects: -1,
		IgnoreTLS: true, Keyword: "DOWN", KeywordInvert: true,
	}, config.HTTP)
	assert.Equal(t, 10, keyword.GetInt("retries"))

	api := find("API status")
	config, err = systems.MonitorConfigFromRecord(env.hub, api)
	require.NoError(t, err)
	assert.Equal(t, "data.status", config.HTTP.JSONPath)
	assert.Equal(t, "ok", config.HTTP.JSONExpected)
	assert.Equal(t, 60, api.GetInt("timeout"))

	ssh := find("SSH")
	assert.Equal(t, "tcp", ssh.GetString("protocol"))
	assert.Equal(t, 22, ssh.GetInt("port"))
	gateway := find("Gateway")
	assert.Equal(t, "icmp", gateway.GetString("protocol"))
	assert.False(t, gateway.GetBool("enabled"), "paused Kuma monitors are imported disabled")
	dns := find("DNS")
	assert.Equal(t, "dns", dns.GetString("protocol"))
	assert.Equal(t, "9.9.9.9:5353", dns.GetString("server"))
	assert.Equal(t, "Ab3dEf6hIj9kLm2nOp5qRs8tUv1wXy4Z", find("Backup job").GetString("pushToken"))
	cron := find("Cron heartbeat").GetString("pushToken")
	assert.Len(t, cron, pushTokenLength)
	assert.NotEqual(t, "short-token", cron)

	// Importing again skips the monitors that now exist.
	response, result = env.kumaImport(t, handler, env.owner, map[string]any{"backup": kumaFixture(t)})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Zero(t, result.Created)
	assert.Len(t, result.Skipped, 14)
	count, err := env.hub.CountRecords("network_monitors")
	require.NoError(t, err)
	assert.EqualValues(t, 8, count)
}

func TestKumaImportIntoSystem(t *testing.T) {
	env := newMonitorTestEnv(t)
	handler := env.apiHandler(t)
	readonly := createMonitorTestUser(t, env.hub, "readonly@example.com", "readonly")
	outsider := createMonitorTestUser(t, env.hub, "outsider@example.com", "user")
	env.system.Set("users", []string{env.owner.Id, readonly.Id})
	require.NoError(t, env.hub.Save(env.system))
	body := map[string]any{"backup": kumaFixture(t), "system": env.system.Id}

	response, _ := env.kumaImport(t, handler, nil, body)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	response, _ = env.kumaImport(t, handler, readonly, body)
	assert.Equal(t, http.StatusForbidden, response.Code)
	response, _ = env.kumaImport(t, handler, outsider, body)
	assert.Equal(t, http.StatusNotFound, response.Code)

	response, result := env.kumaImport(t, handler, env.owner, body)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, 6, result.Created)
	skipped := notesByName(result.Skipped, true)
	assert.Equal(t, []string{"push monitors can only run on the hub"}, skipped["Backup job"])
	assert.Equal(t, []string{"retries lowered from 15 to 10"}, notesByName(result.Adjusted, false)["Health keyword"],
		"agent monitors have no hub minimum interval")
	monitors, err := env.hub.FindAllRecords("network_monitors", dbx.HashExp{"system": env.system.Id})
	require.NoError(t, err)
	assert.Len(t, monitors, 6)
	for _, record := range monitors {
		assert.Empty(t, record.GetStringSlice("users"))
	}

	superuser := createTestSuperuser(t, env.hub)
	response, _ = env.kumaImport(t, handler, superuser, map[string]any{"backup": kumaFixture(t)})
	assert.Equal(t, http.StatusBadRequest, response.Code, "superusers must import into a system")
}

func TestKumaImportRejectsInvalidBackups(t *testing.T) {
	env := newMonitorTestEnv(t)
	handler := env.apiHandler(t)
	for name, backup := range map[string]any{
		"not an object": "hello",
		"no monitors":   map[string]any{"version": "1.23.0"},
	} {
		response, _ := env.kumaImport(t, handler, env.owner, map[string]any{"backup": backup})
		assert.Equal(t, http.StatusBadRequest, response.Code, name)
	}
	monitors := make([]map[string]any, kumaImportMaxMonitors+1)
	for i := range monitors {
		monitors[i] = map[string]any{"name": fmt.Sprint(i), "type": "ping", "hostname": "10.0.0.1", "interval": 60}
	}
	response, _ := env.kumaImport(t, handler, env.owner, map[string]any{"backup": map[string]any{"monitorList": monitors}})
	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Contains(t, response.Body.String(), "at most")

	request := httptest.NewRequest(http.MethodPost, "/api/beszel/import/uptime-kuma",
		strings.NewReader(`{"backup":"`+strings.Repeat("x", kumaImportMaxBody)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", authToken(t, env.owner))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
}
