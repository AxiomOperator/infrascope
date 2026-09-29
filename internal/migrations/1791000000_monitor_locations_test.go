//go:build testing

package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitorLocationsMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	monitors := mustCollection(t, app, "network_monitors")
	for _, name := range monitorLocationFields {
		require.NotNil(t, monitors.Fields.GetByName(name), name)
	}
	relation, ok := monitors.Fields.GetByName("locationSystems").(*core.RelationField)
	require.True(t, ok)
	assert.Equal(t, 10, relation.MaxSelect)
	assert.False(t, relation.CascadeDelete)

	// Existing monitors get their location from their system.
	migration := findMigration(t, "_monitor_locations.go")
	require.NoError(t, migration.Down(app))
	monitors = mustCollection(t, app, "network_monitors")
	assert.Nil(t, monitors.Fields.GetByName("locations"))
	systems := mustCollection(t, app, "systems")
	system := core.NewRecord(systems)
	system.Load(map[string]any{"name": "a", "host": "localhost"})
	require.NoError(t, app.SaveNoValidate(system))
	agent := core.NewRecord(monitors)
	agent.Load(map[string]any{"system": system.Id, "target": "1.1.1.1", "protocol": "icmp"})
	require.NoError(t, app.SaveNoValidate(agent))
	hub := core.NewRecord(monitors)
	hub.Load(map[string]any{"target": "1.1.1.1", "protocol": "icmp"})
	require.NoError(t, app.SaveNoValidate(hub))

	require.NoError(t, migration.Up(app))
	agent, err = app.FindRecordById("network_monitors", agent.Id)
	require.NoError(t, err)
	assert.JSONEq(t, `["`+system.Id+`"]`, agent.GetString("locations"))
	assert.Equal(t, []string{system.Id}, agent.GetStringSlice("locationSystems"))
	assert.Equal(t, 1, agent.GetInt("quorum"))
	hub, err = app.FindRecordById("network_monitors", hub.Id)
	require.NoError(t, err)
	assert.JSONEq(t, `["hub"]`, hub.GetString("locations"))
	assert.Empty(t, hub.GetStringSlice("locationSystems"))
}
