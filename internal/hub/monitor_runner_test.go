//go:build testing

package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/henrygd/beszel/internal/netmon"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startTestRunner replaces the hub's runner with a started hubRunner.
func (env *monitorTestEnv) startTestRunner(t *testing.T) *hubRunner {
	t.Helper()
	runner := newHubRunner(env.hub, env.hub.uptime)
	env.hub.hubMonitors = runner
	require.NoError(t, runner.start())
	t.Cleanup(runner.Stop)
	return runner
}

// apiHandler returns a handler serving the PocketBase and custom API routes.
func (env *monitorTestEnv) apiHandler(t *testing.T) http.Handler {
	t.Helper()
	router, err := apis.NewRouter(env.hub)
	require.NoError(t, err)
	require.NoError(t, env.hub.registerApiRoutes(&core.ServeEvent{App: env.hub, Router: router}))
	handler, err := router.BuildMux()
	require.NoError(t, err)
	return handler
}

// monitorRecord reloads a monitor record.
func (env *monitorTestEnv) monitorRecord(t *testing.T, id string) *core.Record {
	t.Helper()
	record, err := env.hub.FindRecordById("network_monitors", id)
	require.NoError(t, err)
	return record
}

func recentChecks(t *testing.T, record *core.Record) []uptime.RecentCheck {
	t.Helper()
	var recent []uptime.RecentCheck
	require.NoError(t, record.UnmarshalJSONField("recent", &recent))
	return recent
}

func TestHubRunnerProbesHTTPMonitor(t *testing.T) {
	t.Setenv("HUB_MONITOR_MIN_INTERVAL", "1")
	env := newMonitorTestEnv(t)
	runner := env.startTestRunner(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// A long interval staggers the first scheduled probe by at least 30s, so
	// a quick status comes from the immediate run on create.
	record := env.createMonitor(t, env.hubMonitor(map[string]any{
		"target": server.URL, "interval": 60, "retries": 1, "timeout": 1,
	}))
	require.Eventually(t, func() bool {
		runner.applyChecks()
		return env.hub.Uptime().Status(record.Id) == uptime.StatusUp
	}, 5*time.Second, 20*time.Millisecond)

	// Collected stats are stored for the hub (no system).
	require.NoError(t, runner.collect())
	stats, err := env.hub.FindAllRecords("network_monitor_stats", dbx.HashExp{"monitor": record.Id})
	require.NoError(t, err)
	require.Len(t, stats, 1)
	assert.Empty(t, stats[0].GetString("system"))
	assert.Equal(t, "1m", stats[0].GetString("type"))
	assert.EqualValues(t, 1, stats[0].GetInt("success_count"))
	assert.NotEmpty(t, env.monitorRecord(t, record.Id).GetString("updated"))
	// Unchanged results do not add rows.
	require.NoError(t, runner.collect())
	count, err := env.hub.CountRecords("network_monitor_stats", dbx.HashExp{"monitor": record.Id})
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)

	// Checks reach the engine through the callback only; collecting does not apply them again.
	env.hub.Uptime().Flush()
	before := len(recentChecks(t, env.monitorRecord(t, record.Id)))
	assert.Equal(t, 1, before)

	// A short interval probes the stopped server and confirms down after the retry.
	response := env.updateMonitor(t, record.Id, env.owner, map[string]any{"interval": 1})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	server.Close()
	require.Eventually(t, func() bool {
		runner.applyChecks()
		return env.hub.Uptime().Status(record.Id) == uptime.StatusDown
	}, 10*time.Second, 50*time.Millisecond)
	stored := env.monitorRecord(t, record.Id)
	assert.Equal(t, uptime.StatusDown, stored.GetString("status"))

	env.hub.Uptime().Flush()
	recent := recentChecks(t, env.monitorRecord(t, record.Id))
	require.NoError(t, runner.collect())
	env.hub.Uptime().Flush()
	assert.Len(t, recentChecks(t, env.monitorRecord(t, record.Id)), len(recent), "collect must not replay checks")
	count, err = env.hub.CountRecords("network_monitor_stats", dbx.HashExp{"monitor": record.Id})
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)

	// Disabling stops the monitor; its results are no longer collected.
	response = env.updateMonitor(t, record.Id, env.owner, map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Empty(t, runner.mgr.GetResults(hubMonitorResultsMs))
	assert.False(t, runner.running[record.Id])
}

func TestHubRunnerLoadsMonitorsOnStart(t *testing.T) {
	env := newMonitorTestEnv(t)
	enabled := env.createMonitor(t, env.hubMonitor(map[string]any{"protocol": "push"}))
	env.createMonitor(t, env.hubMonitor(map[string]any{"protocol": "push", "enabled": false}))
	env.createMonitor(t, map[string]any{"system": env.system.Id, "target": "1.1.1.1", "protocol": "icmp", "interval": 60, "enabled": true})

	// The hub's runner is inert until started.
	runner, ok := env.hub.hubMonitors.(*hubRunner)
	require.True(t, ok, "hub monitors are enabled by default")
	assert.Empty(t, runner.running)

	require.NoError(t, runner.start())
	t.Cleanup(runner.Stop)
	assert.Equal(t, map[string]bool{enabled.Id: true}, runner.running)
	assert.Equal(t, map[string]string{enabled.GetString("pushToken"): enabled.Id}, runner.tokens)
}

func TestHubRunnerDisabledByEnv(t *testing.T) {
	t.Setenv("HUB_MONITORS", "false")
	env := newMonitorTestEnv(t)
	assert.IsType(t, noopHubMonitorRunner{}, env.hub.hubMonitors)
	require.NoError(t, env.hub.startHubMonitors())
	record := env.createMonitor(t, env.hubMonitor(map[string]any{"protocol": "push"}))

	response := pushRequest(t, env.apiHandler(t), http.MethodGet, record.GetString("pushToken"), "", "")
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func TestAgentSyncExcludesHubMonitors(t *testing.T) {
	env := newMonitorTestEnv(t)
	agent := env.createMonitor(t, map[string]any{"system": env.system.Id, "target": "1.1.1.1", "protocol": "icmp", "interval": 60, "enabled": true})
	env.createMonitor(t, env.hubMonitor(nil))
	env.createMonitor(t, env.hubMonitor(map[string]any{"protocol": "push"}))

	configs, err := env.hub.sm.GetMonitorConfigsForSystem(env.system.Id)
	require.NoError(t, err)
	require.Len(t, configs, 1)
	assert.Equal(t, agent.Id, configs[0].ID)
}

// pushRequest sends a push from ip ("" for the default test address).
func pushRequest(t *testing.T, handler http.Handler, method, token, query, ip string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/beszel/push/" + token
	if query != "" {
		url += "?" + query
	}
	request := httptest.NewRequest(method, url, nil)
	if ip != "" {
		request.RemoteAddr = ip + ":1234"
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// resetPushLimit allows the next push of a monitor without waiting a second.
func (r *hubRunner) resetPushLimit(id string) {
	r.limits.monitors.Remove(id)
}

func TestPushMonitorChecks(t *testing.T) {
	env := newMonitorTestEnv(t)
	runner := env.startTestRunner(t)
	handler := env.apiHandler(t)
	record := env.createMonitor(t, env.hubMonitor(map[string]any{"protocol": "push", "retries": 1}))
	token := record.GetString("pushToken")

	response := pushRequest(t, handler, http.MethodGet, token, "ping=12.5", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.JSONEq(t, `{"ok":true}`, response.Body.String())
	runner.applyChecks()
	assert.Equal(t, uptime.StatusUp, env.hub.Uptime().Status(record.Id))
	env.hub.Uptime().Flush()
	recent := recentChecks(t, env.monitorRecord(t, record.Id))
	require.Len(t, recent, 1)
	assert.Equal(t, 12.5, recent[0].ResponseMs)

	// One push per second is accepted; excess pushes are not checks.
	response = pushRequest(t, handler, http.MethodGet, token, "status=down", "")
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	runner.applyChecks()
	assert.Equal(t, uptime.StatusUp, env.hub.Uptime().Status(record.Id))

	time.Sleep(2 * time.Millisecond) // distinct check times
	runner.resetPushLimit(record.Id)
	response = pushRequest(t, handler, http.MethodPost, token, "status=down&msg=backup+failed", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	runner.applyChecks()
	assert.Equal(t, uptime.StatusPending, env.hub.Uptime().Status(record.Id))
	env.hub.Uptime().Flush()
	assert.Equal(t, "backup failed", env.monitorRecord(t, record.Id).GetString("lastError"))

	time.Sleep(2 * time.Millisecond) // distinct check times
	runner.resetPushLimit(record.Id)
	response = pushRequest(t, handler, http.MethodGet, token, "status=down", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	runner.applyChecks()
	assert.Equal(t, uptime.StatusDown, env.hub.Uptime().Status(record.Id))
	env.hub.Uptime().Flush()
	assert.Equal(t, pushDownError, env.monitorRecord(t, record.Id).GetString("lastError"))

	// Pushes are collected as stats like probes.
	require.NoError(t, runner.collect())
	stats, err := env.hub.FindAllRecords("network_monitor_stats", dbx.HashExp{"monitor": record.Id})
	require.NoError(t, err)
	require.Len(t, stats, 1)
	assert.EqualValues(t, 3, stats[0].GetInt("total_count"))
	assert.EqualValues(t, 1, stats[0].GetInt("success_count"))

	// Unknown tokens and disabled monitors are not found.
	response = pushRequest(t, handler, http.MethodGet, "unknown", "", "")
	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.JSONEq(t, `{"ok":false,"msg":"monitor not found"}`, response.Body.String())
	updated := env.updateMonitor(t, record.Id, env.owner, map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
	runner.resetPushLimit(record.Id)
	response = pushRequest(t, handler, http.MethodGet, token, "", "")
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func TestPushUnknownTokenLimitPerIP(t *testing.T) {
	env := newMonitorTestEnv(t)
	env.startTestRunner(t)
	handler := env.apiHandler(t)
	record := env.createMonitor(t, env.hubMonitor(map[string]any{"protocol": "push"}))

	for i := range pushUnknownLimit {
		response := pushRequest(t, handler, http.MethodGet, "guess"+strconv.Itoa(i), "", "203.0.113.5")
		require.Equal(t, http.StatusNotFound, response.Code, i)
	}
	response := pushRequest(t, handler, http.MethodGet, "guess", "", "203.0.113.5")
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	// A blocked client cannot probe valid tokens either.
	response = pushRequest(t, handler, http.MethodGet, record.GetString("pushToken"), "", "203.0.113.5")
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	// Other clients are not affected.
	response = pushRequest(t, handler, http.MethodGet, record.GetString("pushToken"), "", "203.0.113.6")
	assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
}

func TestPushDeadline(t *testing.T) {
	env := newMonitorTestEnv(t)
	runner := env.startTestRunner(t)
	record := env.createMonitor(t, env.hubMonitor(map[string]any{"protocol": "push", "interval": 60, "retries": 2}))
	start := time.Now()
	status := func() string {
		runner.applyChecks()
		return env.hub.Uptime().Status(record.Id)
	}
	// Deadlines are the interval plus max(10s, interval/10).
	check := func(after time.Duration) {
		time.Sleep(2 * time.Millisecond) // distinct check times
		runner.checkPushDeadlines(start.Add(after))
	}

	check(69 * time.Second)
	assert.Equal(t, uptime.StatusUnknown, status(), "within the grace period")
	check(71 * time.Second)
	assert.Equal(t, uptime.StatusPending, status())
	env.hub.Uptime().Flush()
	assert.Equal(t, noPushError, env.monitorRecord(t, record.Id).GetString("lastError"))
	// One failure per missed interval.
	check(100 * time.Second)
	check(130 * time.Second)
	assert.Equal(t, uptime.StatusPending, status())
	env.hub.Uptime().Flush()
	assert.Len(t, recentChecks(t, env.monitorRecord(t, record.Id)), 1)
	check(132 * time.Second)
	assert.Equal(t, uptime.StatusPending, status())
	check(193 * time.Second)
	assert.Equal(t, uptime.StatusDown, status(), "down after retries+1 missed intervals")

	// A push restarts the deadline.
	time.Sleep(2 * time.Millisecond) // distinct check times
	require.NoError(t, runner.recordPush(record.Id, netmon.Outcome{ResponseUs: 1000}))
	assert.Equal(t, uptime.StatusUp, status())
	check(time.Since(start) + 69*time.Second)
	assert.Equal(t, uptime.StatusUp, status())
}

func TestPushDeadlineGraceAfterRestart(t *testing.T) {
	env := newMonitorTestEnv(t)
	record := env.createMonitor(t, env.hubMonitor(map[string]any{"protocol": "push", "interval": 60}))
	// The last push was long ago, before the hub restarted.
	record.Set("lastCheck", time.Now().Add(-time.Hour).UnixMilli())
	require.NoError(t, env.hub.SaveNoValidate(record))

	runner := env.startTestRunner(t)
	start := time.Now()
	runner.checkPushDeadlines(start.Add(time.Second))
	runner.checkPushDeadlines(start.Add(69 * time.Second))
	runner.applyChecks()
	assert.Equal(t, uptime.StatusUnknown, env.hub.Uptime().Status(record.Id), "one full interval of grace after start")
	runner.checkPushDeadlines(start.Add(71 * time.Second))
	runner.applyChecks()
	assert.Equal(t, uptime.StatusDown, env.hub.Uptime().Status(record.Id), "no retries: down after one missed interval")
}

func TestRegeneratePushToken(t *testing.T) {
	env := newMonitorTestEnv(t)
	runner := env.startTestRunner(t)
	handler := env.apiHandler(t)
	record := env.createMonitor(t, env.hubMonitor(map[string]any{"protocol": "push"}))
	oldToken := record.GetString("pushToken")
	httpMonitor := env.createMonitor(t, env.hubMonitor(nil))
	readonly := createMonitorTestUser(t, env.hub, "readonly@example.com", "readonly")
	other := createMonitorTestUser(t, env.hub, "other@example.com", "user")
	// The readonly user can view the monitor.
	record.Set("users", []string{env.owner.Id, readonly.Id})
	require.NoError(t, env.hub.Save(record))

	regenerate := func(id string, auth *core.Record) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/beszel/monitors/"+id+"/push-token", nil)
		if auth != nil {
			request.Header.Set("Authorization", authToken(t, auth))
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	assert.Equal(t, http.StatusUnauthorized, regenerate(record.Id, nil).Code)
	assert.Equal(t, http.StatusForbidden, regenerate(record.Id, readonly).Code)
	assert.Equal(t, http.StatusNotFound, regenerate(record.Id, other).Code)
	assert.Equal(t, http.StatusNotFound, regenerate("missing", env.owner).Code)
	assert.Equal(t, http.StatusBadRequest, regenerate(httpMonitor.Id, env.owner).Code)
	assert.Equal(t, oldToken, env.monitorRecord(t, record.Id).GetString("pushToken"))

	response := regenerate(record.Id, env.owner)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body struct{ PushToken string }
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Len(t, body.PushToken, pushTokenLength)
	assert.NotEqual(t, oldToken, body.PushToken)
	assert.Equal(t, body.PushToken, env.monitorRecord(t, record.Id).GetString("pushToken"))

	assert.Equal(t, http.StatusNotFound, pushRequest(t, handler, http.MethodGet, oldToken, "", "").Code)
	assert.Equal(t, http.StatusOK, pushRequest(t, handler, http.MethodGet, body.PushToken, "", "").Code)

	superuser := createTestSuperuser(t, env.hub)
	response = regenerate(record.Id, superuser)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Len(t, runner.tokens, 1)
}

func TestHubRunnerEvaluatesThresholdAlerts(t *testing.T) {
	t.Setenv("HUB_MONITOR_MIN_INTERVAL", "1")
	env := newMonitorTestEnv(t)
	runner := newHubRunner(env.hub, env.hub.uptime)
	var evaluations atomic.Int32
	runner.onSaved = func(results map[string]monitor.Result) {
		evaluations.Add(1)
		env.hub.HandleMonitorResults("", results)
	}
	env.hub.hubMonitors = runner
	require.NoError(t, runner.start())
	t.Cleanup(runner.Stop)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	record := env.createMonitor(t, env.hubMonitor(map[string]any{
		"target": server.URL, "interval": 1, "timeout": 1, "lossThreshold": 50,
	}))
	require.Eventually(t, func() bool {
		require.NoError(t, runner.collect())
		rows, err := env.hub.FindAllRecords("alerts_history", dbx.HashExp{"alert_id": record.Id, "name": "MonitorLoss"})
		require.NoError(t, err)
		return len(rows) == 1 && rows[0].GetString("user") == env.owner.Id && rows[0].GetFloat("value") == 100
	}, 10*time.Second, 200*time.Millisecond)
	assert.Positive(t, evaluations.Load())
}

func TestThresholdAlertsUseMaintenanceWindows(t *testing.T) {
	env := newMonitorTestEnv(t)
	record := env.createMonitor(t, env.hubMonitor(map[string]any{"lossThreshold": 10}))
	now := time.Now().UTC()
	window, err := createTestRecord(env.hub, "monitor_maintenance", map[string]any{
		"user": env.owner.Id, "title": "Upgrade", "type": "one-time",
		"start": now.Add(-time.Hour), "end": now.Add(time.Hour), "monitors": []string{record.Id},
	})
	require.NoError(t, err)
	_, err = env.hub.DB().Update("network_monitors", dbx.Params{"loss1h": 50}, dbx.HashExp{"id": record.Id}).Execute()
	require.NoError(t, err)
	results := map[string]monitor.Result{record.Id: {LastProbeAt: time.Now().UnixMilli(), SampleCount: 60, PacketLoss1h: 50}}
	history := func() int {
		count, err := env.hub.CountRecords("alerts_history", dbx.HashExp{"alert_id": record.Id, "name": "MonitorLoss"})
		require.NoError(t, err)
		return int(count)
	}

	env.hub.HandleMonitorResults("", results)
	assert.Zero(t, history(), "held during the hub's maintenance window")
	require.NoError(t, env.hub.Delete(window))
	env.hub.HandleMonitorResults("", results)
	assert.Equal(t, 1, history(), "opened once the window is gone")
}
