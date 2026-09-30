//go:build testing

package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerDiscoveryMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	systems := mustCollection(t, app, "systems")
	for _, name := range dockerDiscoverySystemFields {
		require.NotNil(t, systems.Fields.GetByName(name), name)
	}
	monitors := mustCollection(t, app, "network_monitors")
	for _, name := range dockerDiscoveryMonitorFields {
		require.NotNil(t, monitors.Fields.GetByName(name), name)
	}
	relation, ok := monitors.Fields.GetByName("managedSystem").(*core.RelationField)
	require.True(t, ok)
	assert.True(t, relation.CascadeDelete)
	require.NotNil(t, monitors.GetIndex("idx_nm_managed_key"))

	// The key is unique per system.
	system := core.NewRecord(systems)
	system.Load(map[string]any{"name": "a", "host": "localhost"})
	require.NoError(t, app.SaveNoValidate(system))
	for i, wantErr := range []bool{false, true} {
		record := core.NewRecord(monitors)
		record.Load(map[string]any{"target": "1.1.1.1", "protocol": "icmp", "managedSystem": system.Id, "managedKey": "docker:web:default"})
		err := app.SaveNoValidate(record)
		assert.Equal(t, wantErr, err != nil, i)
	}
	// Unmanaged monitors have no key and are not constrained.
	for range 2 {
		record := core.NewRecord(monitors)
		record.Load(map[string]any{"target": "1.1.1.1", "protocol": "icmp"})
		require.NoError(t, app.SaveNoValidate(record))
	}

	migration := findMigration(t, "_docker_discovery.go")
	require.NoError(t, migration.Down(app))
	assert.Nil(t, mustCollection(t, app, "systems").Fields.GetByName("autoDiscover"))
	assert.Nil(t, mustCollection(t, app, "network_monitors").Fields.GetByName("managedKey"))
	require.NoError(t, migration.Up(app))
}
