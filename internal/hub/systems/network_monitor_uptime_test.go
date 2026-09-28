//go:build testing

package systems

import (
	"testing"
	"time"

	"github.com/blang/semver"
	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateRecordsFeedsUptime checks that default-interval monitor results
// update monitor status once the records are committed.
func TestCreateRecordsFeedsUptime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		result  func(now int64) monitor.Result
		status  string
		err     string
	}{
		{"checks", "0.21.0", func(now int64) monitor.Result {
			return monitor.Result{LastProbeAt: now, TotalCount: 2, Checks: []monitor.CheckEvent{
				{At: now - 1000, ResponseUs: -1, Err: "connection refused"},
				{At: now, ResponseUs: -1},
			}}
		}, "down", "connection refused"},
		{"checks recover", "0.21.0", func(now int64) monitor.Result {
			return monitor.Result{Checks: []monitor.CheckEvent{{At: now - 1000, ResponseUs: -1, Err: "x"}, {At: now, ResponseUs: 900}}}
		}, "up", ""},
		{"no new checks", "0.21.0", func(now int64) monitor.Result {
			return monitor.Result{LastProbeAt: now, TotalCount: 2}
		}, "unknown", ""},
		{"legacy failure", "0.20.0", func(now int64) monitor.Result {
			return monitor.Result{LastProbeAt: now, TotalCount: 2, PacketLoss: 100}
		}, "down", "probe failed (agent does not report errors)"},
		{"legacy success", "0.20.0", func(now int64) monitor.Result {
			return monitor.Result{LastProbeAt: now, TotalCount: 2, SuccessCount: 1, AvgResponse: 1500}
		}, "up", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys, app := newTestSystemWithHub(t)
			engine := uptime.New(app)
			sys.manager.hub = stubHub{App: app, uptime: engine}
			sys.agentVersion = semver.MustParse(tc.version)

			col, err := app.FindCachedCollectionByNameOrId("network_monitors")
			require.NoError(t, err)
			record := core.NewRecord(col)
			record.Load(map[string]any{"system": sys.Id, "target": "example.com", "protocol": "icmp", "interval": 60, "enabled": true, "status": "unknown"})
			require.NoError(t, app.SaveNoValidate(record))
			require.NoError(t, engine.Load())

			now := time.Now().UnixMilli()
			_, err = sys.createRecords(&system.CombinedData{Monitors: map[string]monitor.Result{record.Id: tc.result(now)}})
			require.NoError(t, err)
			engine.Flush()

			stored, err := app.FindRecordById("network_monitors", record.Id)
			require.NoError(t, err)
			assert.Equal(t, tc.status, stored.GetString("status"))
			assert.Equal(t, tc.err, stored.GetString("lastError"))
		})
	}
}
