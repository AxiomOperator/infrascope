//go:build testing

package uptime

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	_ "github.com/henrygd/beszel/internal/migrations"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testEnv struct {
	t      *testing.T
	app    *pbtests.TestApp
	engine *Engine
	system string

	mu          sync.Mutex
	now         time.Time
	notices     []Transition
	maintenance map[string]bool
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	t.Parallel()
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)
	systems, err := app.FindCachedCollectionByNameOrId("systems")
	require.NoError(t, err)
	system := core.NewRecord(systems)
	system.Set("name", "test")
	system.Set("host", "localhost")
	system.Set("status", "up")
	require.NoError(t, app.SaveNoValidate(system))
	env := &testEnv{t: t, app: app, system: system.Id, now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), maintenance: map[string]bool{}}
	env.engine = env.newEngine()
	return env
}

// newEngine creates an engine on the env's clock, notices and maintenance windows.
func (env *testEnv) newEngine() *Engine {
	return New(env.app,
		WithNow(env.clock),
		WithNotifier(func(transitions []Transition) {
			env.mu.Lock()
			defer env.mu.Unlock()
			env.notices = append(env.notices, transitions...)
		}),
		WithMaintenanceCheck(func(id string, _ time.Time) bool {
			env.mu.Lock()
			defer env.mu.Unlock()
			return env.maintenance[id]
		}),
	)
}

func (env *testEnv) clock() time.Time {
	env.mu.Lock()
	defer env.mu.Unlock()
	return env.now
}

func (env *testEnv) advance(d time.Duration) time.Time {
	env.mu.Lock()
	defer env.mu.Unlock()
	env.now = env.now.Add(d)
	return env.now
}

func (env *testEnv) setMaintenance(id string, on bool) {
	env.mu.Lock()
	env.maintenance[id] = on
	env.mu.Unlock()
}

// takeNotices returns and clears the notified statuses.
func (env *testEnv) takeNotices() []string {
	env.mu.Lock()
	defer env.mu.Unlock()
	statuses := []string{}
	for _, n := range env.notices {
		statuses = append(statuses, n.Status)
	}
	env.notices = nil
	return statuses
}

func (env *testEnv) takeTransitions() []Transition {
	env.mu.Lock()
	defer env.mu.Unlock()
	notices := env.notices
	env.notices = nil
	return notices
}

// createMonitor stores an enabled agent monitor and registers it with the engine.
func (env *testEnv) createMonitor(fields map[string]any) *core.Record {
	env.t.Helper()
	collection, err := env.app.FindCachedCollectionByNameOrId("network_monitors")
	require.NoError(env.t, err)
	record := core.NewRecord(collection)
	record.Load(map[string]any{
		"system": env.system, "target": "example.com", "protocol": "icmp", "interval": 60,
		"enabled": true, "notify": true, "status": StatusUnknown,
	})
	record.Load(fields)
	require.NoError(env.t, env.app.SaveNoValidate(record))
	env.engine.Upsert(record)
	return record
}

func (env *testEnv) update(id string, fields map[string]any) {
	env.t.Helper()
	record := env.record(id)
	record.Load(fields)
	require.NoError(env.t, env.app.SaveNoValidate(record))
	env.engine.Upsert(record)
}

func (env *testEnv) record(id string) *core.Record {
	env.t.Helper()
	record, err := env.app.FindRecordById("network_monitors", id)
	require.NoError(env.t, err)
	return record
}

// check reports one check a minute after the previous step.
func (env *testEnv) check(id string, ok bool, err string) int64 {
	at := env.advance(time.Minute).UnixMilli()
	event := monitor.CheckEvent{At: at, ResponseUs: 12_345}
	if !ok {
		event = monitor.CheckEvent{At: at, ResponseUs: -1, Err: err, StatusCode: 503}
	}
	env.engine.Observe(id, []monitor.CheckEvent{event})
	return at
}

type testSegment struct {
	Status     string `db:"status"`
	Start      int64  `db:"start"`
	End        int64  `db:"end"`
	Error      string `db:"error"`
	StatusCode int    `db:"statusCode"`
}

func (env *testEnv) segments(id string) []testSegment {
	env.t.Helper()
	var segments []testSegment
	require.NoError(env.t, env.app.DB().Select("status", "start", "end", "error", "statusCode").From("monitor_events").
		Where(dbx.HashExp{"monitor": id}).OrderBy("start ASC", "rowid ASC").All(&segments))
	return segments
}

func TestStateMachine(t *testing.T) {
	type step struct {
		action string // ok, fail, unknown, maint+, maint-, notify, disable, enable
		status string
		notice []string
	}
	for _, tc := range []struct {
		name    string
		retries int
		notify  bool
		steps   []step
	}{
		{"retries 0", 0, true, []step{
			{"ok", "up", nil}, {"fail", "down", []string{"down"}}, {"fail", "down", nil}, {"ok", "up", []string{"up"}},
		}},
		{"retries 2", 2, true, []step{
			{"ok", "up", nil}, {"fail", "pending", nil}, {"fail", "pending", nil}, {"fail", "down", []string{"down"}},
			{"fail", "down", nil}, {"ok", "up", []string{"up"}},
		}},
		{"flapping never confirms down", 1, true, []step{
			{"ok", "up", nil}, {"fail", "pending", nil}, {"ok", "up", nil}, {"fail", "pending", nil}, {"ok", "up", nil},
		}},
		{"down notifies once", 0, true, []step{
			{"ok", "up", nil}, {"fail", "down", []string{"down"}}, {"fail", "down", nil}, {"fail", "down", nil},
		}},
		{"unknown then up does not notify", 0, true, []step{
			{"ok", "up", nil}, {"unknown", "unknown", nil}, {"ok", "up", nil},
		}},
		{"unknown then down notifies", 0, true, []step{
			{"ok", "up", nil}, {"unknown", "unknown", nil}, {"fail", "down", []string{"down"}},
		}},
		{"unknown resets the failure streak", 1, true, []step{
			{"ok", "up", nil}, {"fail", "pending", nil}, {"unknown", "unknown", nil}, {"fail", "pending", nil}, {"fail", "down", []string{"down"}},
		}},
		{"notify off updates notified silently", 0, false, []step{
			{"ok", "up", nil}, {"fail", "down", nil}, {"notify", "down", nil}, {"fail", "down", nil}, {"ok", "up", []string{"up"}},
		}},
		{"new monitor first down notifies", 0, true, []step{
			{"fail", "down", []string{"down"}}, {"ok", "up", []string{"up"}},
		}},
		{"new monitor pending", 1, true, []step{
			{"fail", "pending", nil}, {"ok", "up", nil},
		}},
		{"maintenance suppresses, still down after exit notifies once", 0, true, []step{
			{"ok", "up", nil}, {"maint+", "maintenance", nil}, {"fail", "maintenance", nil}, {"fail", "maintenance", nil},
			{"maint-", "down", []string{"down"}}, {"fail", "down", nil},
		}},
		{"maintenance hides a recovered outage", 0, true, []step{
			{"ok", "up", nil}, {"maint+", "maintenance", nil}, {"fail", "maintenance", nil}, {"ok", "maintenance", nil},
			{"maint-", "up", nil},
		}},
		{"paused while disabled", 0, true, []step{
			{"ok", "up", nil}, {"disable", "paused", nil}, {"enable", "unknown", nil}, {"ok", "up", nil},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			record := env.createMonitor(map[string]any{"retries": tc.retries, "notify": tc.notify})
			id := record.Id
			for i, s := range tc.steps {
				switch s.action {
				case "ok":
					env.check(id, true, "")
				case "fail":
					env.check(id, false, "timeout")
				case "unknown":
					env.engine.MarkUnknown([]string{id})
				case "maint+", "maint-":
					env.setMaintenance(id, s.action == "maint+")
					env.engine.Tick(env.advance(time.Second))
				case "notify":
					env.update(id, map[string]any{"notify": true})
				case "disable", "enable":
					env.update(id, map[string]any{"enabled": s.action == "enable"})
				}
				notice := s.notice
				if notice == nil {
					notice = []string{}
				}
				assert.Equal(t, s.status, env.engine.Status(id), "step %d (%s) engine status", i, s.action)
				assert.Equal(t, s.status, env.record(id).GetString("status"), "step %d (%s) record status", i, s.action)
				assert.Equal(t, notice, env.takeNotices(), "step %d (%s) notices", i, s.action)
			}
		})
	}
}

func TestTransitionDetails(t *testing.T) {
	env := newTestEnv(t)
	web := env.createMonitor(map[string]any{"retries": 1, "name": "Web"})
	env.check(web.Id, true, "")
	firstFailure := env.check(web.Id, false, "refused")
	env.check(web.Id, false, "refused")
	env.takeNotices()

	// Omitted errors are restored, and the transition reports the first failure.
	record := env.createMonitor(map[string]any{"retries": 1})
	env.check(record.Id, true, "")
	failAt := env.advance(time.Minute).UnixMilli()
	result := monitor.Result{Checks: []monitor.CheckEvent{
		{At: failAt - 1000, ResponseUs: -1, Err: "timeout", StatusCode: 504},
		{At: failAt, ResponseUs: -1, StatusCode: 504},
	}}
	env.engine.ObserveResults(env.system, map[string]monitor.Result{record.Id: result}, false)
	notices := env.takeTransitions()
	require.Len(t, notices, 1)
	n := notices[0]
	assert.Equal(t, record.Id, n.MonitorID)
	assert.Equal(t, env.system, n.SystemID)
	assert.Equal(t, "example.com", n.Name)
	assert.Equal(t, "example.com", n.Target)
	assert.Equal(t, "down", n.Status)
	assert.Equal(t, "up", n.Prev)
	assert.Equal(t, "timeout", n.Err)
	assert.EqualValues(t, 504, n.StatusCode)
	assert.Equal(t, failAt-1000, n.DownSince.UnixMilli())
	assert.Equal(t, failAt, n.At.UnixMilli())
	env.engine.Flush()
	assert.Equal(t, "timeout", env.record(record.Id).GetString("lastError"))

	upAt := env.check(web.Id, true, "")
	notices = env.takeTransitions()
	require.Len(t, notices, 1)
	assert.Equal(t, "Web", notices[0].Name)
	assert.Equal(t, "up", notices[0].Status)
	assert.Equal(t, "down", notices[0].Prev)
	assert.Equal(t, firstFailure, notices[0].DownSince.UnixMilli())
	assert.Equal(t, upAt, notices[0].At.UnixMilli())
}

func TestSegments(t *testing.T) {
	env := newTestEnv(t)
	record := env.createMonitor(map[string]any{"retries": 2})
	id := record.Id
	created := env.clock().UnixMilli()
	t0 := env.check(id, true, "")
	t1 := env.check(id, false, "first error")
	env.check(id, false, "second error")
	env.check(id, false, "third error")
	segments := env.segments(id)
	require.Len(t, segments, 3)
	assert.Equal(t, testSegment{Status: "unknown", Start: created, End: t0}, segments[0])
	assert.Equal(t, testSegment{Status: "up", Start: t0, End: t1}, segments[1])
	// The down segment starts at the first failure and stores its error.
	assert.Equal(t, testSegment{Status: "down", Start: t1, Error: "first error", StatusCode: 503}, segments[2])

	t4 := env.check(id, true, "")
	env.engine.MarkUnknown([]string{id})
	t5 := env.clock().UnixMilli()
	segments = env.segments(id)
	require.Len(t, segments, 5)
	assert.Equal(t, t4, segments[2].End)
	assert.Equal(t, testSegment{Status: "up", Start: t4, End: t5}, segments[3])
	assert.Equal(t, testSegment{Status: "unknown", Start: t5}, segments[4])

	var state persistedState
	require.NoError(t, env.record(id).UnmarshalJSONField("state", &state))
	assert.Equal(t, persistedState{Version: 1, Confirmed: "up", Notified: "up", DownSince: t1}, state)
}

func TestRecentRing(t *testing.T) {
	env := newTestEnv(t)
	record := env.createMonitor(map[string]any{"retries": 1})
	var last int64
	for i := range 70 {
		last = env.check(record.Id, i%3 != 2, "err")
	}
	// A fourth failure in a row: first pending, then down.
	env.check(record.Id, false, "err")
	downAt := env.check(record.Id, false, "err")
	env.engine.Flush()

	stored := env.record(record.Id)
	var recent [][3]float64
	require.NoError(t, json.Unmarshal([]byte(stored.GetString("recent")), &recent))
	require.Len(t, recent, 60)
	assert.Equal(t, [3]float64{float64(downAt / 1000), RecentDown, -1}, recent[59])
	assert.Equal(t, [3]float64{float64((downAt - 60_000) / 1000), RecentPending, -1}, recent[58])
	assert.Equal(t, [3]float64{float64(last / 1000), RecentUp, 12.34}, recent[57])
	assert.Equal(t, downAt, int64(stored.GetFloat("lastCheck")))
	assert.Equal(t, "err", stored.GetString("lastError"))
	assert.Equal(t, 503, stored.GetInt("lastStatusCode"))

	// Round trip through RecentCheck.
	var checks []RecentCheck
	require.NoError(t, json.Unmarshal([]byte(stored.GetString("recent")), &checks))
	assert.Equal(t, RecentCheck{At: last / 1000, State: RecentUp, ResponseMs: 12.34}, checks[57])
	raw, err := json.Marshal(checks[57:58])
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("[[%d,1,12.34]]", last/1000), string(raw))
}

func pct(v float64) *float64 { return &v }

func TestUptimeFromSegments(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	segments := []Segment{
		{Status: "down", Start: at(40 * 24 * time.Hour), End: at(35 * 24 * time.Hour)}, // outside every window
		{Status: "up", Start: at(48 * time.Hour), End: at(12 * time.Hour)},             // clipped for d1
		{Status: "down", Start: at(12 * time.Hour), End: at(6 * time.Hour)},
		{Status: "maintenance", Start: at(6 * time.Hour), End: at(3 * time.Hour)},
		{Status: "unknown", Start: at(3 * time.Hour), End: at(2 * time.Hour)},
		{Status: "up", Start: at(2 * time.Hour)}, // open
	}
	got := UptimeFromSegments(segments, now)
	assert.Equal(t, Uptime{D1: pct(70), D7: pct(86.364), D30: pct(86.364)}, got)

	raw, err := json.Marshal(UptimeFromSegments([]Segment{{Status: "maintenance", Start: at(time.Hour)}}, now))
	require.NoError(t, err)
	assert.JSONEq(t, `{"d1":null,"d7":null,"d30":null}`, string(raw))

	// Older down time only counts in the longer windows.
	got = UptimeFromSegments([]Segment{
		{Status: "down", Start: at(10 * 24 * time.Hour), End: at(9 * 24 * time.Hour)},
		{Status: "up", Start: at(9 * 24 * time.Hour)},
	}, now)
	assert.Equal(t, Uptime{D1: pct(100), D7: pct(100), D30: pct(90)}, got)
}

func TestFlushWritesUptime(t *testing.T) {
	env := newTestEnv(t)
	record := env.createMonitor(nil)
	env.check(record.Id, true, "")
	env.advance(time.Hour)
	env.check(record.Id, false, "x")
	env.advance(59 * time.Minute)
	env.engine.Flush()
	var uptime Uptime
	require.NoError(t, env.record(record.Id).UnmarshalJSONField("uptime", &uptime))
	require.NotNil(t, uptime.D1)
	// Up for 61 minutes (from the first check), then down for 59.
	assert.InDelta(t, 61.0/120*100, *uptime.D1, 0.001)

	// Refreshed at most every five minutes.
	env.check(record.Id, true, "")
	env.engine.Flush()
	var again Uptime
	require.NoError(t, env.record(record.Id).UnmarshalJSONField("uptime", &again))
	assert.Equal(t, uptime, again)
	env.advance(5 * time.Minute)
	env.engine.Flush()
	require.NoError(t, env.record(record.Id).UnmarshalJSONField("uptime", &again))
	assert.NotEqual(t, uptime, again)
}

func TestRestartRestoresState(t *testing.T) {
	env := newTestEnv(t)
	record := env.createMonitor(map[string]any{"retries": 1})
	id := record.Id
	env.check(id, true, "")
	env.check(id, false, "x")
	env.check(id, false, "x")
	env.check(id, false, "x")
	assert.Equal(t, []string{"down"}, env.takeNotices())
	segmentCount := len(env.segments(id))

	env.engine = env.newEngine()
	require.NoError(t, env.engine.Load())
	assert.Equal(t, "down", env.engine.Status(id))
	assert.Empty(t, env.takeNotices())
	assert.Len(t, env.segments(id), segmentCount)

	env.check(id, false, "x")
	assert.Empty(t, env.takeNotices())
	env.check(id, true, "")
	assert.Equal(t, []string{"up"}, env.takeNotices())
	segments := env.segments(id)
	require.Len(t, segments, segmentCount+1)
	assert.Equal(t, "down", segments[segmentCount-1].Status)
	assert.NotZero(t, segments[segmentCount-1].End)

	// A restart mid-streak keeps the streak.
	env.check(id, false, "x")
	env.engine = env.newEngine()
	require.NoError(t, env.engine.Load())
	assert.Equal(t, "pending", env.engine.Status(id))
	env.check(id, false, "x")
	assert.Equal(t, []string{"down"}, env.takeNotices())
}

func TestLoadRepairsDisabledMonitor(t *testing.T) {
	env := newTestEnv(t)
	record := env.createMonitor(nil)
	env.check(record.Id, true, "")
	// Disabled without the engine seeing the update.
	_, err := env.app.DB().NewQuery("UPDATE network_monitors SET enabled = 0 WHERE id = {:id}").Bind(dbx.Params{"id": record.Id}).Execute()
	require.NoError(t, err)
	env.engine = env.newEngine()
	require.NoError(t, env.engine.Load())
	assert.Equal(t, "paused", env.record(record.Id).GetString("status"))
	segments := env.segments(record.Id)
	assert.Equal(t, "paused", segments[len(segments)-1].Status)
	assert.Empty(t, env.takeNotices())
}

func TestSystemStatusChanged(t *testing.T) {
	env := newTestEnv(t)
	a := env.createMonitor(nil)
	b := env.createMonitor(nil)
	env.check(a.Id, true, "")
	env.check(b.Id, true, "")

	env.engine.SystemStatusChanged(env.system, "down")
	for _, id := range []string{a.Id, b.Id} {
		assert.Equal(t, "unknown", env.record(id).GetString("status"))
		segments := env.segments(id)
		assert.Equal(t, "unknown", segments[len(segments)-1].Status)
	}
	env.engine.SystemStatusChanged(env.system, "paused")
	assert.Equal(t, "paused", env.record(a.Id).GetString("status"))
	env.engine.SystemStatusChanged(env.system, "up")
	assert.Equal(t, "unknown", env.record(a.Id).GetString("status"))
	env.check(a.Id, true, "")
	assert.Equal(t, "up", env.record(a.Id).GetString("status"))
	assert.Empty(t, env.takeNotices())

	// Other systems are unaffected.
	env.engine.SystemStatusChanged("other", "down")
	assert.Equal(t, "up", env.engine.Status(a.Id))
}

func TestStaleMonitorsBecomeUnknown(t *testing.T) {
	env := newTestEnv(t)
	record := env.createMonitor(map[string]any{"interval": 60})
	push := env.createMonitor(map[string]any{"system": "", "protocol": "push", "target": ""})
	env.check(record.Id, true, "")
	env.engine.Tick(env.advance(3 * time.Minute))
	assert.Equal(t, "up", env.engine.Status(record.Id))
	env.engine.Tick(env.advance(2 * time.Minute))
	assert.Equal(t, "unknown", env.engine.Status(record.Id))
	assert.Equal(t, "unknown", env.record(record.Id).GetString("status"))
	assert.Equal(t, "unknown", env.engine.Status(push.Id))
	assert.Empty(t, env.takeNotices())
}

func TestObserveResults(t *testing.T) {
	env := newTestEnv(t)
	record := env.createMonitor(nil)
	at := env.advance(time.Minute).UnixMilli()

	// Newer agents without checks report nothing new.
	env.engine.ObserveResults(env.system, map[string]monitor.Result{record.Id: {LastProbeAt: at, TotalCount: 1}}, false)
	assert.Equal(t, "unknown", env.engine.Status(record.Id))

	// Results of another system are ignored.
	checks := monitor.Result{Checks: []monitor.CheckEvent{{At: at, ResponseUs: 100}}}
	env.engine.ObserveResults("other", map[string]monitor.Result{record.Id: checks}, false)
	assert.Equal(t, "unknown", env.engine.Status(record.Id))

	// Legacy agents: one check synthesised per probe window.
	failed := monitor.Result{LastProbeAt: at, TotalCount: 3}
	env.engine.ObserveResults(env.system, map[string]monitor.Result{record.Id: failed}, true)
	assert.Equal(t, "down", env.engine.Status(record.Id))
	assert.Equal(t, legacyFailureError, env.engine.monitors[record.Id].lastError)
	assert.Equal(t, 1, env.engine.monitors[record.Id].p.FailStreak)
	// The same probe window is not counted twice.
	env.engine.ObserveResults(env.system, map[string]monitor.Result{record.Id: failed}, true)
	assert.Equal(t, 1, env.engine.monitors[record.Id].p.FailStreak)
	assert.Equal(t, []string{"down"}, env.takeNotices())

	env.advance(time.Minute)
	ok := monitor.Result{LastProbeAt: at + 60_000, TotalCount: 3, SuccessCount: 2, AvgResponse: 2500}
	env.engine.ObserveResults(env.system, map[string]monitor.Result{record.Id: ok}, true)
	assert.Equal(t, "up", env.engine.Status(record.Id))
	st := env.engine.monitors[record.Id]
	assert.Equal(t, RecentCheck{At: (at + 60_000) / 1000, State: RecentUp, ResponseMs: 2.5}, st.recent[len(st.recent)-1])
}

func TestChecksFromResult(t *testing.T) {
	assert.Nil(t, ChecksFromResult(monitor.Result{TotalCount: 1}, false))
	assert.Nil(t, ChecksFromResult(monitor.Result{LastProbeAt: 5}, true))
	assert.Equal(t, []monitor.CheckEvent{{At: 5, ResponseUs: -1, Err: legacyFailureError}},
		ChecksFromResult(monitor.Result{LastProbeAt: 5, TotalCount: 2}, true))
	assert.Equal(t, []monitor.CheckEvent{{At: 5, ResponseUs: 150}},
		ChecksFromResult(monitor.Result{LastProbeAt: 5, TotalCount: 2, SuccessCount: 2, ResponseSum: 300}, true))
	original := []monitor.CheckEvent{{At: 1, ResponseUs: -1, Err: "a"}, {At: 2, ResponseUs: -1}}
	got := ChecksFromResult(monitor.Result{Checks: original, TotalCount: 5}, true)
	assert.Equal(t, "a", got[1].Err)
	assert.Empty(t, original[1].Err, "input is not modified")
}

func TestRemovedMonitorIsIgnored(t *testing.T) {
	env := newTestEnv(t)
	record := env.createMonitor(nil)
	require.NoError(t, env.app.Delete(record))
	env.engine.Remove(record.Id)
	env.check(record.Id, false, "x")
	assert.Empty(t, env.segments(record.Id))
	assert.Empty(t, env.takeNotices())
}

func TestConcurrentObserve(t *testing.T) {
	env := newTestEnv(t)
	var ids []string
	for range 4 {
		ids = append(ids, env.createMonitor(nil).Id)
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			base := env.clock().UnixMilli()
			for i := range 20 {
				env.engine.Observe(id, []monitor.CheckEvent{{At: base + int64(i+1)*1000, ResponseUs: int64(i%2*2 - 1)}})
			}
		})
	}
	wg.Go(func() {
		for range 10 {
			env.engine.Tick(env.clock())
			env.engine.Flush()
			env.engine.MarkUnknown(ids[:1])
		}
	})
	wg.Wait()
	for _, id := range ids {
		open := 0
		for _, segment := range env.segments(id) {
			if segment.End == 0 {
				open++
			}
		}
		assert.Equal(t, 1, open, "one open segment")
		assert.Equal(t, env.engine.Status(id), env.record(id).GetString("status"))
	}
}
