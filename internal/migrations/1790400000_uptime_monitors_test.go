//go:build testing

package migrations

import (
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func uptimeMigration(t *testing.T) *core.Migration {
	t.Helper()
	for _, migration := range core.AppMigrations.Items() {
		if strings.HasSuffix(migration.File, "_uptime_monitors.go") {
			return migration
		}
	}
	t.Fatal("uptime monitors migration is not registered")
	return nil
}

func assertUptimeSchema(t *testing.T, app core.App) {
	t.Helper()
	monitors, err := app.FindCollectionByNameOrId("network_monitors")
	require.NoError(t, err)
	for _, name := range networkMonitorUptimeFields {
		assert.NotNil(t, monitors.Fields.GetByName(name), name)
	}
	assert.False(t, monitors.Fields.GetByName("system").(*core.RelationField).Required)
	assert.False(t, monitors.Fields.GetByName("target").(*core.TextField).Required)
	assert.Contains(t, monitors.Fields.GetByName("protocol").(*core.SelectField).Values, "push")
	assert.True(t, monitors.Fields.GetByName("state").GetHidden())
	assert.NotNil(t, monitors.GetIndex("idx_nm_push_token"))

	stats, err := app.FindCollectionByNameOrId("network_monitor_stats")
	require.NoError(t, err)
	assert.False(t, stats.Fields.GetByName("system").(*core.RelationField).Required)
	history, err := app.FindCollectionByNameOrId("alerts_history")
	require.NoError(t, err)
	assert.False(t, history.Fields.GetByName("system").(*core.RelationField).Required)
	assert.NotNil(t, history.Fields.GetByName("monitor"))

	for _, name := range []string{"monitor_events", "status_pages", "monitor_maintenance"} {
		_, err := app.FindCollectionByNameOrId(name)
		assert.NoError(t, err, name)
	}
}

func TestUptimeMonitorsMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()
	assertUptimeSchema(t, app)

	// Incidents reference status pages, so they are rolled back first.
	require.NoError(t, findMigration(t, "_incidents.go").Down(app))
	migration := uptimeMigration(t)
	require.NoError(t, migration.Down(app))
	monitors, err := app.FindCollectionByNameOrId("network_monitors")
	require.NoError(t, err)
	assert.Nil(t, monitors.Fields.GetByName("users"))
	assert.True(t, monitors.Fields.GetByName("system").(*core.RelationField).Required)
	assert.NotContains(t, monitors.Fields.GetByName("protocol").(*core.SelectField).Values, "push")
	for _, name := range []string{"monitor_events", "status_pages", "monitor_maintenance"} {
		_, err := app.FindCollectionByNameOrId(name)
		assert.Error(t, err, name)
	}

	// Existing monitors start with an unknown status.
	user := core.NewRecord(mustCollection(t, app, "users"))
	user.Set("email", "user@example.com")
	user.Set("password", "testtesttest")
	require.NoError(t, app.Save(user))
	system := core.NewRecord(mustCollection(t, app, "systems"))
	system.Load(map[string]any{"name": "s", "host": "localhost", "port": "45876", "users": []string{user.Id}})
	require.NoError(t, app.Save(system))
	monitor := core.NewRecord(monitors)
	monitor.Load(map[string]any{"system": system.Id, "target": "1.1.1.1", "protocol": "icmp", "interval": 60})
	require.NoError(t, app.Save(monitor))

	require.NoError(t, migration.Up(app))
	assertUptimeSchema(t, app)
	monitor, err = app.FindRecordById("network_monitors", monitor.Id)
	require.NoError(t, err)
	assert.Equal(t, "unknown", monitor.GetString("status"))
}

func mustCollection(t *testing.T, app core.App, name string) *core.Collection {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId(name)
	require.NoError(t, err)
	return collection
}
