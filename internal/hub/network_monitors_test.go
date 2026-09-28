//go:build testing

package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/monitor"
	esystem "github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/hub/ws"
	"github.com/lxzan/gws"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// monitorTestEnv is a test hub with monitor hooks bound and a system owned by owner.
type monitorTestEnv struct {
	hub    *Hub
	owner  *core.Record
	system *core.Record
}

func newMonitorTestEnv(t *testing.T) *monitorTestEnv {
	t.Helper()
	hub, testApp, err := createTestHub(t)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupTestHub(hub, testApp) })
	bindNetworkMonitorsEvents(hub)
	owner := createMonitorTestUser(t, hub, "owner@example.com", "user")
	system, err := createTestRecord(hub, "systems", map[string]any{
		"name": "Paused", "host": "localhost", "port": "45876",
		"status": "paused", "users": []string{owner.Id},
	})
	require.NoError(t, err)
	return &monitorTestEnv{hub: hub, owner: owner, system: system}
}

func createMonitorTestUser(t *testing.T, app core.App, email, role string) *core.Record {
	t.Helper()
	user, err := createTestRecord(app, "users", map[string]any{"email": email, "password": "testtesttest", "role": role})
	require.NoError(t, err)
	return user
}

func authToken(t *testing.T, record *core.Record) string {
	t.Helper()
	token, err := record.NewAuthToken()
	require.NoError(t, err)
	return token
}

// request sends an API request as the given auth record (nil for a guest).
func (env *monitorTestEnv) request(t *testing.T, method, url string, auth *core.Record, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	router, err := apis.NewRouter(env.hub)
	require.NoError(t, err)
	handler, err := router.BuildMux()
	require.NoError(t, err)
	request := httptest.NewRequest(method, url, reader)
	request.Header.Set("Content-Type", "application/json")
	if auth != nil {
		request.Header.Set("Authorization", authToken(t, auth))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// createMonitor creates a monitor through the API as the owner and returns the stored record.
func (env *monitorTestEnv) createMonitor(t *testing.T, body map[string]any) *core.Record {
	t.Helper()
	response := env.request(t, http.MethodPost, "/api/collections/network_monitors/records", env.owner, body)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var created struct{ ID string }
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &created))
	record, err := env.hub.FindRecordById("network_monitors", created.ID)
	require.NoError(t, err)
	return record
}

func (env *monitorTestEnv) updateMonitor(t *testing.T, id string, auth *core.Record, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return env.request(t, http.MethodPatch, "/api/collections/network_monitors/records/"+id, auth, body)
}

func (env *monitorTestEnv) hubMonitor(body map[string]any) map[string]any {
	monitor := map[string]any{"users": []string{env.owner.Id}, "target": "https://example.com", "protocol": "http", "interval": 60, "enabled": true}
	for key, value := range body {
		monitor[key] = value
	}
	return monitor
}

func TestCreateNetworkMonitorsOnPausedSystem(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "single"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			env := newMonitorTestEnv(t)
			// Paused systems are not loaded into the manager at startup.
			_, err := env.hub.sm.GetSystem(env.system.Id)
			require.Error(t, err)

			payload := func(target string) map[string]any {
				return map[string]any{
					"system": env.system.Id, "target": target, "protocol": "icmp",
					"interval": 60, "enabled": true,
				}
			}
			url := "/api/collections/network_monitors/records"
			var body any = payload("1.1.1.1")
			count := 1
			if batch {
				body = map[string]any{"requests": []map[string]any{
					{"method": "POST", "url": url, "body": payload("1.1.1.1")},
					{"method": "POST", "url": url, "body": payload("8.8.8.8")},
				}}
				url = "/api/batch"
				count = 2
			}
			response := env.request(t, http.MethodPost, url, env.owner, body)
			assert.Equal(t, http.StatusOK, response.Code, response.Body.String())

			records, err := env.hub.FindAllRecords("network_monitors")
			require.NoError(t, err)
			require.Len(t, records, count)
			for _, record := range records {
				assert.Equal(t, env.system.Id, record.GetString("system"))
				assert.True(t, record.GetBool("enabled"))
				assert.Equal(t, "unknown", record.GetString("status"))
			}
		})
	}
}

func TestMonitorRequestValidation(t *testing.T) {
	env := newMonitorTestEnv(t)
	agentMonitor := func(body map[string]any) map[string]any {
		monitor := map[string]any{"system": env.system.Id, "target": "https://example.com", "protocol": "http", "interval": 60}
		for key, value := range body {
			monitor[key] = value
		}
		return monitor
	}
	for _, tc := range []struct {
		name    string
		body    map[string]any
		message string
	}{
		{"bad http method", agentMonitor(map[string]any{"http": map[string]any{"method": "FETCH"}}), "unsupported method"},
		{"bad accepted code", agentMonitor(map[string]any{"http": map[string]any{"acceptedCodes": []string{"20"}}}), "invalid accepted status code"},
		{"bad header", agentMonitor(map[string]any{"httpSecrets": map[string]any{"headers": [][2]string{{"Bad Header", "x"}}}}), "invalid header name"},
		{"malformed http options", agentMonitor(map[string]any{"http": map[string]any{"maxRedirects": "many"}}), "nvalid http options"},
		{"missing target", agentMonitor(map[string]any{"target": "", "protocol": "icmp"}), "Target is required"},
		{"push on a system", agentMonitor(map[string]any{"protocol": "push"}), "Push monitors cannot run on a system"},
		{"hub interval too short", env.hubMonitor(map[string]any{"interval": 5}), "at least 10 seconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := env.request(t, http.MethodPost, "/api/collections/network_monitors/records", env.owner, tc.body)
			assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), tc.message)
		})
	}

	t.Run("hub users without the requester", func(t *testing.T) {
		other := createMonitorTestUser(t, env.hub, "other@example.com", "user")
		record := env.createMonitor(t, env.hubMonitor(nil))
		// Superusers may set any users, but users must not be empty.
		response := env.updateMonitor(t, record.Id, env.owner, map[string]any{"users": []string{other.Id}})
		assert.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
		superuser := createTestSuperuser(t, env.hub)
		response = env.updateMonitor(t, record.Id, superuser, map[string]any{"users": []string{}})
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), "at least one user")
		response = env.updateMonitor(t, record.Id, superuser, map[string]any{"users": []string{other.Id}})
		assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
	})

	t.Run("hub interval override", func(t *testing.T) {
		t.Setenv("HUB_MONITOR_MIN_INTERVAL", "5")
		record := env.createMonitor(t, env.hubMonitor(map[string]any{"interval": 5}))
		assert.Equal(t, 5, record.GetInt("interval"))
	})
}

func createTestSuperuser(t *testing.T, app core.App) *core.Record {
	t.Helper()
	collection, err := app.FindCachedCollectionByNameOrId(core.CollectionNameSuperusers)
	require.NoError(t, err)
	superuser := core.NewRecord(collection)
	superuser.Set("email", "super@example.com")
	superuser.Set("password", "testtesttest")
	require.NoError(t, app.Save(superuser))
	return superuser
}

func TestMonitorNormalizesProtocolOptions(t *testing.T) {
	env := newMonitorTestEnv(t)
	record := env.createMonitor(t, map[string]any{
		"system": env.system.Id, "target": "https://example.com", "protocol": "http", "interval": 60,
		"port": 443, "server": "1.1.1.1", "users": []string{env.owner.Id},
		// headers belong in httpSecrets, so they are dropped from http
		"http":        map[string]any{"method": "POST", "keyword": "ok", "headers": [][2]string{{"X-Token", "leak"}}},
		"httpSecrets": map[string]any{"headers": [][2]string{{"X-Token", "secret"}}, "basicUser": "admin"},
	})
	assert.Zero(t, record.GetInt("port"))
	assert.Empty(t, record.GetString("server"))
	assert.Empty(t, record.GetStringSlice("users"), "agent monitors follow the system's users")
	assert.JSONEq(t, `{"method":"POST","keyword":"ok"}`, record.GetString("http"))
	assert.JSONEq(t, `{"headers":[["X-Token","secret"]],"basicUser":"admin"}`, record.GetString("httpSecrets"))

	response := env.updateMonitor(t, record.Id, env.owner, map[string]any{"protocol": "icmp"})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	record, err := env.hub.FindRecordById("network_monitors", record.Id)
	require.NoError(t, err)
	assert.Contains(t, []string{"", "null"}, record.GetString("http"))
	assert.Contains(t, []string{"", "null"}, record.GetString("httpSecrets"))
}

func TestPushMonitorToken(t *testing.T) {
	env := newMonitorTestEnv(t)
	record := env.createMonitor(t, env.hubMonitor(map[string]any{"protocol": "push", "pushToken": "client-token"}))
	token := record.GetString("pushToken")
	assert.Len(t, token, pushTokenLength)
	assert.NotEqual(t, "client-token", token)
	assert.Empty(t, record.GetString("target"), "push monitors have no target")

	response := env.updateMonitor(t, record.Id, env.owner, map[string]any{"pushToken": "client-token", "name": "Backups"})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	record, err := env.hub.FindRecordById("network_monitors", record.Id)
	require.NoError(t, err)
	assert.Equal(t, token, record.GetString("pushToken"), "updates keep the token")
	assert.Equal(t, "Backups", record.GetString("name"))

	response = env.updateMonitor(t, record.Id, env.owner, map[string]any{"protocol": "http", "target": "https://example.com"})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	record, err = env.hub.FindRecordById("network_monitors", record.Id)
	require.NoError(t, err)
	assert.Empty(t, record.GetString("pushToken"))

	record = env.createMonitor(t, env.hubMonitor(map[string]any{"pushToken": "client-token"}))
	assert.Empty(t, record.GetString("pushToken"), "only push monitors have a token")
}

func TestMonitorServerFieldsNotClientSettable(t *testing.T) {
	env := newMonitorTestEnv(t)
	record := env.createMonitor(t, env.hubMonitor(map[string]any{
		"status": "up", "lastError": "fake", "lastCheck": 123, "res": 5, "recent": []int{1}, "state": map[string]any{"x": 1},
	}))
	assert.Equal(t, "unknown", record.GetString("status"))
	assert.Empty(t, record.GetString("lastError"))
	assert.Zero(t, record.GetInt("lastCheck"))
	assert.Zero(t, record.GetFloat("res"))
	assert.Contains(t, []string{"", "null"}, record.GetString("recent"))
	assert.Contains(t, []string{"", "null"}, record.GetString("state"))

	record.Set("status", "down")
	record.Set("lastError", "timeout")
	require.NoError(t, env.hub.SaveNoValidate(record))
	response := env.updateMonitor(t, record.Id, env.owner, map[string]any{"status": "up", "lastError": "", "name": "Site"})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	record, err := env.hub.FindRecordById("network_monitors", record.Id)
	require.NoError(t, err)
	assert.Equal(t, "down", record.GetString("status"))
	assert.Equal(t, "timeout", record.GetString("lastError"))
	assert.Equal(t, "Site", record.GetString("name"))
}

func TestMonitorUpdateKeepsIDAndStats(t *testing.T) {
	env := newMonitorTestEnv(t)
	record := env.createMonitor(t, map[string]any{"system": env.system.Id, "target": "1.1.1.1", "protocol": "icmp", "interval": 60})
	_, err := createTestRecord(env.hub, "network_monitor_stats", map[string]any{
		"system": env.system.Id, "monitor": record.Id, "type": "1m", "created": time.Now().UnixMilli(),
	})
	require.NoError(t, err)

	response := env.updateMonitor(t, record.Id, env.owner, map[string]any{"target": "8.8.8.8", "protocol": "tcp", "port": 53})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	monitors, err := env.hub.FindAllRecords("network_monitors")
	require.NoError(t, err)
	require.Len(t, monitors, 1)
	assert.Equal(t, record.Id, monitors[0].Id)
	assert.Equal(t, "8.8.8.8", monitors[0].GetString("target"))
	stats, err := env.hub.CountRecords("network_monitor_stats")
	require.NoError(t, err)
	assert.EqualValues(t, 1, stats)
}

func TestMonitorSecretsVisibility(t *testing.T) {
	env := newMonitorTestEnv(t)
	readonly := createMonitorTestUser(t, env.hub, "readonly@example.com", "readonly")
	outsider := createMonitorTestUser(t, env.hub, "outsider@example.com", "user")
	env.system.Set("users", []string{env.owner.Id, readonly.Id})
	require.NoError(t, env.hub.Save(env.system))

	agentMonitor := env.createMonitor(t, map[string]any{
		"system": env.system.Id, "target": "https://example.com", "protocol": "http", "interval": 60,
		"httpSecrets": map[string]any{"basicPass": "hunter2"},
	})
	pushMonitor := env.createMonitor(t, env.hubMonitor(map[string]any{"protocol": "push", "users": []string{env.owner.Id, readonly.Id}}))
	token := pushMonitor.GetString("pushToken")

	get := func(t *testing.T, auth *core.Record, id string) string {
		t.Helper()
		response := env.request(t, http.MethodGet, "/api/collections/network_monitors/records/"+id, auth, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		return response.Body.String()
	}
	list := func(t *testing.T, auth *core.Record) string {
		t.Helper()
		response := env.request(t, http.MethodGet, "/api/collections/network_monitors/records", auth, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		return response.Body.String()
	}

	t.Run("owner", func(t *testing.T) {
		assert.Contains(t, get(t, env.owner, agentMonitor.Id), "hunter2")
		assert.Contains(t, get(t, env.owner, pushMonitor.Id), token)
		body := list(t, env.owner)
		assert.Contains(t, body, "hunter2")
		assert.Contains(t, body, token)
		assert.NotContains(t, body, `"state"`, "state is hidden")
	})
	t.Run("readonly member", func(t *testing.T) {
		body := get(t, readonly, agentMonitor.Id) + get(t, readonly, pushMonitor.Id) + list(t, readonly)
		assert.Contains(t, body, agentMonitor.Id)
		assert.NotContains(t, body, "hunter2")
		assert.NotContains(t, body, token)
		assert.NotContains(t, body, "httpSecrets")
	})
	t.Run("superuser", func(t *testing.T) {
		superuser := createTestSuperuser(t, env.hub)
		body := list(t, superuser)
		assert.Contains(t, body, "hunter2")
		assert.Contains(t, body, token)
	})
	t.Run("non-member with SHARE_ALL_SYSTEMS", func(t *testing.T) {
		t.Setenv("SHARE_ALL_SYSTEMS", "true")
		require.NoError(t, env.hub.SetCollectionAuthSettings())
		t.Cleanup(func() {
			t.Setenv("SHARE_ALL_SYSTEMS", "")
			_ = env.hub.SetCollectionAuthSettings()
		})
		body := list(t, outsider)
		assert.Contains(t, body, agentMonitor.Id)
		assert.Contains(t, body, pushMonitor.Id)
		assert.NotContains(t, body, "hunter2")
		assert.NotContains(t, body, token)
	})
}

// fakeMonitorAgent answers hub requests and records monitor sync requests.
type fakeMonitorAgent struct {
	gws.BuiltinEventHandler
	requests chan monitor.SyncRequest
}

func (a *fakeMonitorAgent) OnMessage(conn *gws.Conn, message *gws.Message) {
	defer message.Close()
	var req common.HubRequest[cbor.RawMessage]
	if err := cbor.Unmarshal(message.Bytes(), &req); err != nil {
		return
	}
	resp := common.AgentResponse{Id: req.Id}
	if req.Action == common.SyncNetworkMonitors {
		var data monitor.SyncRequest
		if err := cbor.Unmarshal(req.Data, &data); err == nil {
			a.requests <- data
		}
		resp.Data, _ = cbor.Marshal(monitor.SyncResponse{})
	} else {
		resp.SystemData = &esystem.CombinedData{}
	}
	response, _ := cbor.Marshal(resp)
	_ = conn.WriteMessage(gws.OpcodeBinary, response)
}

// fakeAgentServer ignores disconnects, which tests handle by removing the system.
type fakeAgentServer struct{ ws.Handler }

func (*fakeAgentServer) OnClose(*gws.Conn, error) {}

// connectFakeMonitorAgent connects a fake agent for systemID and consumes its initial monitor sync.
func connectFakeMonitorAgent(t *testing.T, hub *Hub, systemID string) chan monitor.SyncRequest {
	t.Helper()
	version := beszel.MinVersionMonitorChecks
	connections := make(chan *ws.WsConn, 1)
	upgrader := gws.NewUpgrader(&fakeAgentServer{}, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		wsConn := ws.NewWsConnection(conn, version)
		conn.Session().Store("wsConn", wsConn)
		connections <- wsConn
		conn.ReadLoop()
	}))
	t.Cleanup(server.Close)
	agent := &fakeMonitorAgent{requests: make(chan monitor.SyncRequest, 16)}
	conn, _, err := gws.NewClient(agent, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.NetConn().Close() })
	go conn.ReadLoop()
	select {
	case wsConn := <-connections:
		require.NoError(t, hub.sm.AddWebSocketSystem(systemID, version, wsConn))
	case <-time.After(3 * time.Second):
		t.Fatal("websocket connection was not established")
	}
	t.Cleanup(func() { _ = hub.sm.RemoveSystem(systemID) })
	require.Equal(t, monitor.SyncActionReplace, nextSyncRequest(t, agent.requests).Action)
	return agent.requests
}

func nextSyncRequest(t *testing.T, requests chan monitor.SyncRequest) monitor.SyncRequest {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not receive a monitor sync request")
		return monitor.SyncRequest{}
	}
}

func TestMonitorRunnerChangeSyncsAgents(t *testing.T) {
	env := newMonitorTestEnv(t)
	other, err := createTestRecord(env.hub, "systems", map[string]any{
		"name": "Other", "host": "localhost", "port": "45877", "status": "paused", "users": []string{env.owner.Id},
	})
	require.NoError(t, err)
	agentA := connectFakeMonitorAgent(t, env.hub, env.system.Id)
	agentB := connectFakeMonitorAgent(t, env.hub, other.Id)

	record := env.createMonitor(t, map[string]any{
		"system": env.system.Id, "target": "https://example.com", "protocol": "http", "interval": 60,
		"timeout": 20, "http": map[string]any{"keyword": "ok"}, "httpSecrets": map[string]any{"basicPass": "pw"},
	})

	// agent to agent
	response := env.updateMonitor(t, record.Id, env.owner, map[string]any{"system": other.Id, "enabled": true})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	req := nextSyncRequest(t, agentA)
	assert.Equal(t, monitor.SyncActionDelete, req.Action)
	assert.Equal(t, record.Id, req.Config.ID)
	req = nextSyncRequest(t, agentB)
	assert.Equal(t, monitor.SyncActionUpsert, req.Action)
	assert.True(t, req.RunNow, "a monitor new to the agent runs immediately")
	assert.Equal(t, monitor.Config{
		ID: record.Id, Target: "https://example.com", Protocol: "http", Interval: 60, Timeout: 20,
		HTTP: &monitor.HTTPOptions{Keyword: "ok", BasicPass: "pw"},
	}, req.Config)

	// agent to hub
	response = env.updateMonitor(t, record.Id, env.owner, map[string]any{"system": "", "users": []string{env.owner.Id}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	req = nextSyncRequest(t, agentB)
	assert.Equal(t, monitor.SyncActionDelete, req.Action)
	assert.Equal(t, record.Id, req.Config.ID)

	// hub to agent
	response = env.updateMonitor(t, record.Id, env.owner, map[string]any{"system": env.system.Id})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	req = nextSyncRequest(t, agentA)
	assert.Equal(t, monitor.SyncActionUpsert, req.Action)
	assert.Equal(t, record.Id, req.Config.ID)

	// same agent, disabled
	response = env.updateMonitor(t, record.Id, env.owner, map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	req = nextSyncRequest(t, agentA)
	assert.Equal(t, monitor.SyncActionDelete, req.Action)

	updated, err := env.hub.FindRecordById("network_monitors", record.Id)
	require.NoError(t, err)
	assert.Empty(t, updated.GetStringSlice("users"))
	assert.Empty(t, agentA)
	assert.Empty(t, agentB)
}
