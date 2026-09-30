//go:build testing

package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitorDependenciesMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	monitors := mustCollection(t, app, "network_monitors")
	for _, name := range []string{"network_monitors", "systems"} {
		collection := mustCollection(t, app, name)
		relation, ok := collection.Fields.GetByName("dependsOn").(*core.RelationField)
		require.True(t, ok, name)
		assert.Equal(t, monitors.Id, relation.CollectionId)
		assert.Equal(t, 5, relation.MaxSelect)
		assert.False(t, relation.CascadeDelete)
		assert.NotNil(t, collection.Fields.GetByName("suppressedBy"), name)
	}

	migration := findMigration(t, "_monitor_dependencies.go")
	require.NoError(t, migration.Down(app))
	for _, name := range []string{"network_monitors", "systems"} {
		collection := mustCollection(t, app, name)
		for _, field := range dependencyFields {
			assert.Nil(t, collection.Fields.GetByName(field), name+"."+field)
		}
	}
	require.NoError(t, migration.Up(app))
	assert.NotNil(t, mustCollection(t, app, "systems").Fields.GetByName("dependsOn"))
}
