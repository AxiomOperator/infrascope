//go:build testing

package systemevents_test

import (
	"sync"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/hub/systemevents"
	_ "github.com/henrygd/beszel/internal/migrations"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

var base = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func newApp(t *testing.T) *pbtests.TestApp {
	t.Helper()
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)
	return app
}

func newSystem(t *testing.T, app core.App, status string) *core.Record {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("systems")
	require.NoError(t, err)
	record := core.NewRecord(collection)
	record.Load(map[string]any{"name": "s", "host": "localhost", "port": "45876", "status": status})
	require.NoError(t, app.SaveNoValidate(record))
	return record
}

func rows(t *testing.T, app core.App, systemID string) []systemevents.Segment {
	t.Helper()
	var segments []systemevents.Segment
	require.NoError(t, app.DB().Select("id", "system", "status", "start", "end").From("system_events").
		Where(dbx.HashExp{"system": systemID}).OrderBy("start", "end DESC").All(&segments))
	return segments
}

func insert(t *testing.T, app core.App, systemID, status string, start, end int64) {
	t.Helper()
	_, err := app.DB().Insert("system_events", dbx.Params{
		"id": core.GenerateDefaultRandomId(), "system": systemID, "status": status, "start": start, "end": end,
	}).Execute()
	require.NoError(t, err)
}

type seg struct {
	Status     string
	Start, End int64
}

func simplify(segments []systemevents.Segment) []seg {
	result := make([]seg, len(segments))
	for i, s := range segments {
		result[i] = seg{s.Status, s.Start, s.End}
	}
	return result
}

func TestRecorderTransitions(t *testing.T) {
	app := newApp(t)
	c := &clock{t: base}
	recorder := systemevents.New()
	recorder.SetNow(c.now)
	recorder.Bind(app)

	ms := func(minutes int) int64 { return base.Add(time.Duration(minutes) * time.Minute).UnixMilli() }
	system := newSystem(t, app, "pending")
	assert.Equal(t, []seg{{"pending", ms(0), 0}}, simplify(rows(t, app, system.Id)))

	c.set(base.Add(time.Minute))
	system.Set("status", "up")
	require.NoError(t, app.SaveNoValidate(system))
	// Saves without a status change do not add rows.
	c.set(base.Add(2 * time.Minute))
	system.Set("name", "renamed")
	require.NoError(t, app.SaveNoValidate(system))
	require.NoError(t, app.SaveNoValidate(system))

	c.set(base.Add(5 * time.Minute))
	system.Set("status", "down")
	require.NoError(t, app.SaveNoValidate(system))
	c.set(base.Add(7 * time.Minute))
	system.Set("status", "paused")
	require.NoError(t, app.SaveNoValidate(system))
	// Unknown values are ignored.
	c.set(base.Add(8 * time.Minute))
	system.Set("status", "")
	require.NoError(t, app.SaveNoValidate(system))

	assert.Equal(t, []seg{
		{"pending", ms(0), ms(1)},
		{"up", ms(1), ms(5)},
		{"down", ms(5), ms(7)},
		{"paused", ms(7), 0},
	}, simplify(rows(t, app, system.Id)))

	// A new recorder (empty cache) continues the open row instead of duplicating it.
	fresh := systemevents.New()
	fresh.SetNow(c.now)
	system.Set("status", "paused")
	require.NoError(t, fresh.Observe(app, system.Id, "paused"))
	assert.Len(t, rows(t, app, system.Id), 4)

	// Deleting the system deletes its rows.
	require.NoError(t, app.Delete(system))
	assert.Empty(t, rows(t, app, system.Id))
}

func TestRecorderReconcile(t *testing.T) {
	app := newApp(t)
	// Systems are created without a recorder, so they have no rows.
	kept := newSystem(t, app, "up")
	changed := newSystem(t, app, "down")
	missing := newSystem(t, app, "paused")
	duplicated := newSystem(t, app, "up")

	ms := func(minutes int) int64 { return base.Add(time.Duration(minutes) * time.Minute).UnixMilli() }
	insert(t, app, kept.Id, "up", ms(0), 0)
	insert(t, app, changed.Id, "up", ms(0), 0)
	insert(t, app, duplicated.Id, "down", ms(0), 0)
	insert(t, app, duplicated.Id, "up", ms(10), 0)

	now := time.Now().UTC().Truncate(time.Millisecond)
	recorder := systemevents.New()
	recorder.SetNow(func() time.Time { return now })
	require.NoError(t, recorder.Reconcile(app))

	assert.Equal(t, []seg{{"up", ms(0), 0}}, simplify(rows(t, app, kept.Id)))
	assert.Equal(t, []seg{{"paused", now.UnixMilli(), 0}}, simplify(rows(t, app, missing.Id)))
	assert.Equal(t, []seg{{"down", ms(0), ms(10)}, {"up", ms(10), 0}}, simplify(rows(t, app, duplicated.Id)))
	// The changed status is dated to the last update of the system record.
	changedAt := changed.GetDateTime("updated").Time().UnixMilli()
	assert.Equal(t, []seg{{"up", ms(0), changedAt}, {"down", changedAt, 0}}, simplify(rows(t, app, changed.Id)))

	// Reconciling again changes nothing.
	before := map[string][]systemevents.Segment{}
	for _, id := range []string{kept.Id, changed.Id, missing.Id, duplicated.Id} {
		before[id] = rows(t, app, id)
	}
	require.NoError(t, systemevents.New().Reconcile(app))
	for id, segments := range before {
		assert.Equal(t, segments, rows(t, app, id))
	}
}

func TestRecorderConcurrentObserve(t *testing.T) {
	app := newApp(t)
	recorder := systemevents.New()
	system := newSystem(t, app, "up")
	var wg sync.WaitGroup
	for i := range 20 {
		status := "up"
		if i%2 == 1 {
			status = "down"
		}
		wg.Go(func() { assert.NoError(t, recorder.Observe(app, system.Id, status)) })
	}
	wg.Wait()
	var open int
	for _, s := range rows(t, app, system.Id) {
		if s.End == 0 {
			open++
		}
	}
	assert.Equal(t, 1, open)
}

func TestLoad(t *testing.T) {
	app := newApp(t)
	a := newSystem(t, app, "up")
	b := newSystem(t, app, "up")
	insert(t, app, a.Id, "up", 0, 100)
	insert(t, app, a.Id, "down", 100, 200)
	insert(t, app, a.Id, "up", 200, 0)
	insert(t, app, b.Id, "up", 50, 0)
	segments, err := systemevents.Load(app, []string{a.Id}, 150, 1000)
	require.NoError(t, err)
	assert.Equal(t, []seg{{"down", 100, 200}, {"up", 200, 0}}, simplify(segments))
	segments, err = systemevents.Load(app, nil, 0, 1000)
	require.NoError(t, err)
	assert.Empty(t, segments)
}
