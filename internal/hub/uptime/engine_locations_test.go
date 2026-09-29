//go:build testing

package uptime

import (
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addSystem stores another system for location tests.
func (env *testEnv) addSystem(name string) string {
	env.t.Helper()
	systems, err := env.app.FindCachedCollectionByNameOrId("systems")
	require.NoError(env.t, err)
	system := core.NewRecord(systems)
	system.Set("name", name)
	system.Set("host", "localhost")
	system.Set("status", "up")
	require.NoError(env.t, env.app.SaveNoValidate(system))
	return system.Id
}

// checkAt reports one check of a location ("hub" or a system id) at the current time.
func (env *testEnv) checkAt(id, location string, ok bool, err string) {
	at := env.clock().UnixMilli()
	event := monitor.CheckEvent{At: at, ResponseUs: 20_000}
	if !ok {
		event = monitor.CheckEvent{At: at, ResponseUs: -1, Err: err, StatusCode: 503}
	}
	if location == "hub" {
		env.engine.Observe(id, []monitor.CheckEvent{event})
		return
	}
	env.engine.ObserveResults(location, map[string]monitor.Result{id: {Checks: []monitor.CheckEvent{event}}}, false)
}

// locationEnv has a monitor checked from the hub and two systems, a and b.
type locationEnv struct {
	*testEnv
	a, b string
	id   string
}

func newLocationEnv(t *testing.T, quorum, retries int) *locationEnv {
	env := newTestEnv(t)
	a, b := env.addSystem("alpha"), env.addSystem("beta")
	record := env.createMonitor(map[string]any{
		"system": a, "locations": []string{"hub", a, b}, "quorum": quorum, "retries": retries,
		"users": []string{}, "name": "Web",
	})
	return &locationEnv{testEnv: env, a: a, b: b, id: record.Id}
}

// round reports one check per location, a minute after the previous round.
// Locations missing from results do not report.
func (env *locationEnv) round(results map[string]bool) {
	env.advance(time.Minute)
	for _, location := range []string{"hub", env.a, env.b} {
		if ok, reports := results[location]; reports {
			env.checkAt(env.id, location, ok, "timeout from "+location)
		}
	}
}

func (env *locationEnv) locationStatus() map[string]LocationStatus {
	env.t.Helper()
	env.engine.Flush()
	var statuses map[string]LocationStatus
	require.NoError(env.t, env.record(env.id).UnmarshalJSONField("locationStatus", &statuses))
	return statuses
}

func TestLocationQuorum(t *testing.T) {
	type step struct {
		// results by location: "h", "a", "b" for the hub and the systems;
		// + ok, - failed, ? unknown (system down), omitted: no report.
		results string
		status  string
		notice  []string
	}
	for _, tc := range []struct {
		name    string
		quorum  int
		retries int
		steps   []step
	}{
		{"2 of 3 down is down", 2, 0, []step{
			{"h+ a+ b+", "up", nil},
			{"h+ a- b+", "pending", nil},
			{"h+ a- b-", "down", []string{"down"}},
			{"h+ a- b-", "down", nil},
			{"h+ a+ b-", "pending", []string{"up"}},
			{"h+ a+ b+", "up", nil},
		}},
		{"1 of 3 below quorum stays pending", 2, 0, []step{
			{"h+ a+ b+", "up", nil},
			{"h- a+ b+", "pending", nil},
			{"h- a+ b+", "pending", nil},
			{"h+ a+ b+", "up", nil},
		}},
		{"quorum 1 is down from any location", 1, 0, []step{
			{"h+ a+ b+", "up", nil},
			{"h+ a+ b-", "down", []string{"down"}},
			{"h+ a+ b+", "up", []string{"up"}},
		}},
		{"retries per location", 2, 1, []step{
			{"h+ a+ b+", "up", nil},
			{"h- a- b+", "pending", nil},
			{"h- a- b+", "down", []string{"down"}},
		}},
		{"unknown locations are excluded", 2, 0, []step{
			{"h+ a+ b+", "up", nil},
			{"h+ a- b?", "pending", nil},
			{"h- a- b?", "down", []string{"down"}},
			{"h+ a+ b+", "up", []string{"up"}},
		}},
		{"known below quorum is unknown", 2, 0, []step{
			{"h+ a+ b+", "up", nil},
			{"h- a? b?", "unknown", nil},
			{"h- a? b?", "unknown", nil},
			{"h- a- b+", "down", []string{"down"}},
		}},
		{"new monitor waits for quorum", 3, 0, []step{
			{"h+", "unknown", nil},
			{"h+ a+", "unknown", nil},
			{"h+ a+ b+", "up", nil},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newLocationEnv(t, tc.quorum, tc.retries)
			locations := map[byte]string{'h': "hub", 'a': env.a, 'b': env.b}
			for i, s := range tc.steps {
				results := map[string]bool{}
				for j := 0; j+1 < len(s.results); j += 3 {
					location := locations[s.results[j]]
					switch s.results[j+1] {
					case '+':
						results[location] = true
					case '-':
						results[location] = false
					case '?':
						env.engine.SystemStatusChanged(location, "down")
					}
				}
				env.round(results)
				notice := s.notice
				if notice == nil {
					notice = []string{}
				}
				assert.Equal(t, s.status, env.engine.Status(env.id), "step %d engine status", i)
				assert.Equal(t, s.status, env.record(env.id).GetString("status"), "step %d record status", i)
				assert.Equal(t, notice, env.takeNotices(), "step %d notices", i)
			}
		})
	}
}

func TestLocationErrors(t *testing.T) {
	env := newLocationEnv(t, 2, 0)
	env.round(map[string]bool{"hub": true, env.a: true, env.b: true})
	env.round(map[string]bool{"hub": true, env.a: false, env.b: true})
	st := env.engine.monitors[env.id]
	assert.Equal(t, "down from 1 of 3 locations: alpha: timeout from "+env.a, st.lastError)

	statuses := env.locationStatus()
	assert.Equal(t, "up", statuses["hub"].Status)
	assert.EqualValues(t, 20_000, statuses["hub"].Res)
	assert.Equal(t, "down", statuses[env.a].Status)
	assert.Equal(t, "timeout from "+env.a, statuses[env.a].LastError)
	assert.EqualValues(t, 503, statuses[env.a].LastStatusCode)
	assert.EqualValues(t, -1, statuses[env.a].Res)
	assert.Equal(t, "down from 1 of 3 locations: alpha: timeout from "+env.a, env.record(env.id).GetString("lastError"))

	downAt := env.advance(time.Minute).UnixMilli()
	env.checkAt(env.id, "hub", false, "refused")
	transitions := env.takeTransitions()
	require.Len(t, transitions, 1)
	assert.Equal(t, "down", transitions[0].Status)
	assert.Equal(t, env.a, transitions[0].SystemID, "the primary location")
	assert.Equal(t, "down from 2 of 3 locations: Hub: refused; alpha: timeout from "+env.a, transitions[0].Err)
	// The outage started with the first location's failure.
	assert.Equal(t, downAt-60_000, transitions[0].DownSince.UnixMilli())
	segments := env.segments(env.id)
	last := segments[len(segments)-1]
	assert.Equal(t, "down", last.Status)
	assert.Equal(t, downAt-60_000, last.Start)
	assert.Equal(t, transitions[0].Err, last.Error)

	// Every location's checks are recorded in recent.
	require.Len(t, st.recent, 7)
	assert.Equal(t, RecentPending, st.recent[4].State, "a failure while not down")
	assert.Equal(t, RecentDown, st.recent[6].State)
}

func TestLocationStateSurvivesRestart(t *testing.T) {
	env := newLocationEnv(t, 2, 1)
	env.round(map[string]bool{"hub": true, env.a: true, env.b: true})
	env.round(map[string]bool{"hub": false, env.a: false, env.b: true})
	assert.Equal(t, "pending", env.engine.Status(env.id))

	env.engine = env.newEngine()
	require.NoError(t, env.engine.Load())
	assert.Equal(t, "pending", env.engine.Status(env.id))
	// The streaks were kept: one more failure from each confirms down.
	env.round(map[string]bool{"hub": false, env.a: false, env.b: true})
	assert.Equal(t, "down", env.engine.Status(env.id))
	assert.Equal(t, []string{"down"}, env.takeNotices())
}

func TestLocationChanges(t *testing.T) {
	env := newTestEnv(t)
	a := env.addSystem("alpha")
	record := env.createMonitor(map[string]any{"system": "", "locations": []string{"hub"}, "users": []string{}})
	env.checkAt(record.Id, "hub", true, "")
	assert.Equal(t, "up", env.engine.Status(record.Id))

	// An added location is unknown until it reports; with the default
	// quorum of 2, so is the monitor.
	env.update(record.Id, map[string]any{"system": a, "locations": []string{"hub", a}, "quorum": 0})
	assert.Equal(t, "unknown", env.engine.Status(record.Id))
	env.advance(time.Minute)
	env.checkAt(record.Id, a, true, "")
	env.checkAt(record.Id, "hub", true, "")
	assert.Equal(t, "up", env.engine.Status(record.Id))

	// Results of a removed location are ignored.
	env.update(record.Id, map[string]any{"system": a, "locations": []string{a}, "quorum": 0})
	env.advance(time.Minute)
	env.checkAt(record.Id, a, true, "")
	env.engine.ObserveResults("", map[string]monitor.Result{record.Id: {Checks: []monitor.CheckEvent{{At: env.clock().UnixMilli() + 1, ResponseUs: -1, Err: "x"}}}}, false)
	assert.Equal(t, "up", env.engine.Status(record.Id))
	assert.Empty(t, env.takeNotices())

	// Stale locations become unknown.
	env.update(record.Id, map[string]any{"system": a, "locations": []string{"hub", a}, "quorum": 1})
	env.advance(time.Minute)
	env.checkAt(record.Id, a, true, "")
	env.checkAt(record.Id, "hub", false, "down")
	assert.Equal(t, "down", env.engine.Status(record.Id))
	env.takeNotices()
	for range 6 {
		env.advance(time.Minute)
		env.checkAt(record.Id, a, true, "")
		env.engine.Tick(env.clock())
	}
	assert.Equal(t, "up", env.engine.Status(record.Id), "the stale hub location no longer counts")
	env.engine.Flush()
	var statuses map[string]LocationStatus
	require.NoError(t, env.record(record.Id).UnmarshalJSONField("locationStatus", &statuses))
	assert.Equal(t, "unknown", statuses["hub"].Status)
	assert.Equal(t, "up", statuses[a].Status)
}
