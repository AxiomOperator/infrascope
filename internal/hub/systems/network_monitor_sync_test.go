//go:build testing

package systems

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blang/semver"
	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/monitor"
	esystem "github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/hub/ws"
	"github.com/lxzan/gws"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"
)

type monitorSyncClient struct {
	gws.BuiltinEventHandler
	requests chan common.HubRequest[monitor.SyncRequest]
	failSync atomic.Bool
}

func (c *monitorSyncClient) OnMessage(conn *gws.Conn, message *gws.Message) {
	defer message.Close()
	var req common.HubRequest[cbor.RawMessage]
	if err := cbor.Unmarshal(message.Bytes(), &req); err != nil {
		return
	}
	resp := common.AgentResponse{Id: req.Id}
	if req.Action == common.GetData {
		resp.SystemData = &esystem.CombinedData{}
	} else {
		var data monitor.SyncRequest
		if err := cbor.Unmarshal(req.Data, &data); err != nil {
			return
		}
		c.requests <- common.HubRequest[monitor.SyncRequest]{Id: req.Id, Action: req.Action, Data: data}
		if c.failSync.Load() {
			resp.Error = "test sync failure"
		} else {
			resp.Data, _ = cbor.Marshal(monitor.SyncResponse{})
		}
	}
	response, _ := cbor.Marshal(resp)
	_ = conn.WriteMessage(gws.OpcodeBinary, response)
}

// Avoid the production delayed disconnect notification; these tests explicitly
// remove each connection from the manager before reconnecting.
type monitorSyncServer struct{ ws.Handler }

func (*monitorSyncServer) OnClose(*gws.Conn, error) {}

func TestNetworkMonitorSyncSkipsOlderAgents(t *testing.T) {
	for _, version := range []string{"0.0.0", "0.18.0", "0.19.0"} {
		t.Run(version, func(t *testing.T) {
			// No transport: attempting to send any request would fail.
			sys := &System{agentVersion: semver.MustParse(version)}
			require.NoError(t, sys.SyncNetworkMonitors(nil))
			result, err := sys.UpsertNetworkMonitor(monitor.Config{ID: "test"}, true)
			require.NoError(t, err)
			require.Nil(t, result)
			require.NoError(t, sys.DeleteNetworkMonitor("test"))
		})
	}
}

func TestNetworkMonitorReconnectSync(t *testing.T) {
	for _, change := range []string{"delete", "disable", "retry"} {
		t.Run(change, func(t *testing.T) {
			sys, app := newTestSystemWithHub(t)
			record, err := app.FindRecordById("systems", sys.Id)
			require.NoError(t, err)
			// Suppress unrelated system-stat requests while exercising reconnects.
			record.Set("status", paused)
			require.NoError(t, app.SaveNoValidate(record))
			collection, err := app.FindCachedCollectionByNameOrId("network_monitors")
			require.NoError(t, err)
			probe := core.NewRecord(collection)
			probe.Load(map[string]any{
				"system": sys.Id, "target": "localhost", "protocol": "tcp",
				"port": 80, "interval": 60, "enabled": true,
			})
			require.NoError(t, app.SaveNoValidate(probe))

			sm := NewSystemManager(stubHub{App: app})
			t.Cleanup(func() {
				sm.cancel()
				_ = sm.RemoveSystem(sys.Id)
				sm.smartFetchMap.StopCleaner()
				sm.zfsFetchMap.StopCleaner()
			})
			version := semver.MustParse("0.20.0")
			connections := make(chan *ws.WsConn, 1)
			upgrader := gws.NewUpgrader(&monitorSyncServer{}, nil)
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
			client := &monitorSyncClient{requests: make(chan common.HubRequest[monitor.SyncRequest], 2)}
			connect := func() monitor.SyncRequest {
				t.Helper()
				conn, _, err := gws.NewClient(client, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http")})
				require.NoError(t, err)
				t.Cleanup(func() { _ = conn.NetConn().Close() })
				go conn.ReadLoop()
				select {
				case wsConn := <-connections:
					require.NoError(t, sm.AddWebSocketSystem(sys.Id, version, wsConn))
				case <-time.After(3 * time.Second):
					t.Fatal("websocket connection was not established")
				}
				select {
				case req := <-client.requests:
					require.Equal(t, common.SyncNetworkMonitors, req.Action)
					require.Equal(t, monitor.SyncActionReplace, req.Data.Action)
					return req.Data
				case <-time.After(3 * time.Second):
					t.Fatal("reconnected agent did not receive a monitor replacement")
					return monitor.SyncRequest{}
				}
			}

			client.failSync.Store(change == "retry")
			initial := connect()
			require.Len(t, initial.Configs, 1)
			require.Equal(t, probe.Id, initial.Configs[0].ID)
			if change == "retry" {
				system, err := sm.GetSystem(sys.Id)
				require.NoError(t, err)
				require.Eventually(t, system.monitorsNeedSync.Load, time.Second, time.Millisecond)
				// A second failed sync must not fail the stats fetch or clear pending state.
				_, err = system.fetchDataFromAgent(common.DataRequestOptions{})
				require.NoError(t, err)
				require.True(t, system.monitorsNeedSync.Load())
				require.Len(t, client.requests, 1)
				<-client.requests
				client.failSync.Store(false)
				_, err = system.fetchDataFromAgent(common.DataRequestOptions{})
				require.NoError(t, err)
				require.False(t, system.monitorsNeedSync.Load())
				require.Len(t, client.requests, 1)
				retry := <-client.requests
				require.Equal(t, monitor.SyncActionReplace, retry.Data.Action)
				require.Equal(t, initial.Configs, retry.Data.Configs)
				_, err = system.fetchDataFromAgent(common.DataRequestOptions{})
				require.NoError(t, err)
				require.Empty(t, client.requests, "successful sync must not repeat on every fetch")
				return
			}
			require.NoError(t, sm.RemoveSystem(sys.Id))
			if change == "delete" {
				require.NoError(t, app.Delete(probe))
			} else {
				probe.Set("enabled", false)
				require.NoError(t, app.SaveNoValidate(probe))
			}
			require.Empty(t, connect().Configs, "reconnect must clear the agent's previous probe")
		})
	}
}

func TestGetMonitorConfigsForSystemIncludesServer(t *testing.T) {
	sys, app := newTestSystemWithHub(t)
	collection, err := app.FindCachedCollectionByNameOrId("network_monitors")
	require.NoError(t, err)
	probe := core.NewRecord(collection)
	probe.Load(map[string]any{
		"system": sys.Id, "target": "example.com", "protocol": "dns",
		"server": "1.1.1.1", "interval": 60, "enabled": true,
	})
	require.NoError(t, app.SaveNoValidate(probe))

	configs, err := sys.manager.GetMonitorConfigsForSystem(sys.Id)
	require.NoError(t, err)
	require.Len(t, configs, 1)
	require.Equal(t, "1.1.1.1", configs[0].Server, "reconnect sync must keep the custom DNS server")
}

func TestGetMonitorConfigsForSystemFullConfig(t *testing.T) {
	sys, app := newTestSystemWithHub(t)
	collection, err := app.FindCachedCollectionByNameOrId("network_monitors")
	require.NoError(t, err)
	save := func(data map[string]any) *core.Record {
		record := core.NewRecord(collection)
		record.Load(data)
		require.NoError(t, app.SaveNoValidate(record))
		return record
	}
	httpMonitor := save(map[string]any{
		"system": sys.Id, "target": "https://example.com", "protocol": "http", "interval": 60, "enabled": true,
		"timeout": 20, "retryInterval": 15,
		"http":        map[string]any{"method": "POST", "acceptedCodes": []string{"200-299"}, "maxRedirects": -1, "keyword": "ok"},
		"httpSecrets": map[string]any{"headers": [][2]string{{"X-Token", "secret"}}, "body": "{}", "basicUser": "u", "basicPass": "p"},
	})
	// malformed options are skipped instead of probing with defaults
	save(map[string]any{
		"system": sys.Id, "target": "https://bad.example.com", "protocol": "http", "interval": 60, "enabled": true,
		"http": `{"maxRedirects":"many"}`,
	})
	// push monitors never run on agents
	save(map[string]any{"system": sys.Id, "protocol": "push", "interval": 60, "enabled": true})
	// hub monitors belong to no system
	save(map[string]any{"target": "https://hub.example.com", "protocol": "http", "interval": 60, "enabled": true})

	configs, err := sys.manager.GetMonitorConfigsForSystem(sys.Id)
	require.NoError(t, err)
	require.Equal(t, []monitor.Config{{
		ID: httpMonitor.Id, Target: "https://example.com", Protocol: "http", Interval: 60, Timeout: 20, RetryInterval: 15,
		HTTP: &monitor.HTTPOptions{
			Method: "POST", Headers: [][2]string{{"X-Token", "secret"}}, Body: "{}", AcceptedCodes: []string{"200-299"},
			MaxRedirects: -1, Keyword: "ok", BasicUser: "u", BasicPass: "p",
		},
	}}, configs)
}

func TestMonitorConfigFromRecordOmitsUnusedHTTPOptions(t *testing.T) {
	collection := core.NewBaseCollection("network_monitors")
	collection.Fields.Add(
		&core.TextField{Name: "protocol"}, &core.JSONField{Name: "http"}, &core.JSONField{Name: "httpSecrets"},
	)
	record := core.NewRecord(collection)
	record.Load(map[string]any{"protocol": "http", "http": map[string]any{"acceptedCodes": []string{}}, "httpSecrets": nil})
	config, err := MonitorConfigFromRecord(nil, record)
	require.NoError(t, err)
	require.Nil(t, config.HTTP, "default options must not be sent")

	record.Set("protocol", "tcp")
	record.Set("http", map[string]any{"keyword": "ok"})
	config, err = MonitorConfigFromRecord(nil, record)
	require.NoError(t, err)
	require.Nil(t, config.HTTP, "only http monitors have http options")
}

func TestGetMonitorConfigsForSystemQueryError(t *testing.T) {
	sys, app := newTestSystemWithHub(t)
	_, err := app.DB().NewQuery("DROP TABLE network_monitors").Execute()
	require.NoError(t, err)
	_, err = sys.manager.GetMonitorConfigsForSystem(sys.Id)
	require.Error(t, err, "a failed query must not be treated as an empty monitor set")
}

func TestSyncRequestForAgentStripsUnsupportedFields(t *testing.T) {
	full := monitor.Config{
		ID: "m1", Target: "https://example.com", Protocol: "http", Interval: 60,
		Timeout: 30, RetryInterval: 10, HTTP: &monitor.HTTPOptions{Method: "POST", Keyword: "ok"},
	}
	legacy := monitor.Config{ID: "m1", Target: "https://example.com", Protocol: "http", Interval: 60}
	for _, tc := range []struct {
		version string
		want    monitor.Config
	}{
		{"0.20.0", legacy},
		{"0.20.9", legacy},
		{"0.21.0", full},
		{"0.22.1", full},
	} {
		t.Run(tc.version, func(t *testing.T) {
			version := semver.MustParse(tc.version)
			upsert := syncRequestForAgent(monitor.SyncRequest{Action: monitor.SyncActionUpsert, Config: full, RunNow: true}, version)
			require.Equal(t, tc.want, upsert.Config)
			require.True(t, upsert.RunNow)

			configs := []monitor.Config{full, full}
			replace := syncRequestForAgent(monitor.SyncRequest{Action: monitor.SyncActionReplace, Configs: configs}, version)
			require.Equal(t, []monitor.Config{tc.want, tc.want}, replace.Configs)
			require.Equal(t, full, configs[0], "the caller's configs must not be modified")

			empty := syncRequestForAgent(monitor.SyncRequest{Action: monitor.SyncActionReplace}, version)
			require.Nil(t, empty.Configs)
		})
	}
}
