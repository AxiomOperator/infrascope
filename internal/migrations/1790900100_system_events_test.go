//go:build testing

package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSystemEventsMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	events := mustCollection(t, app, "system_events")
	relation, ok := events.Fields.GetByName("system").(*core.RelationField)
	require.True(t, ok)
	assert.True(t, relation.CascadeDelete)
	assert.Equal(t, mustCollection(t, app, "systems").Id, relation.CollectionId)
	systems, ok := mustCollection(t, app, "status_pages").Fields.GetByName("systems").(*core.RelationField)
	require.True(t, ok)
	assert.Equal(t, 100, systems.MaxSelect)
	assert.False(t, systems.CascadeDelete)

	migration := findMigration(t, "_system_events.go")
	require.NoError(t, migration.Down(app))
	_, err = app.FindCollectionByNameOrId("system_events")
	assert.Error(t, err)
	assert.Nil(t, mustCollection(t, app, "status_pages").Fields.GetByName("systems"))
	require.NoError(t, migration.Up(app))
	assert.NotNil(t, mustCollection(t, app, "status_pages").Fields.GetByName("systems"))
}
