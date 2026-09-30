//go:build testing

package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIncidentsMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	incidents := mustCollection(t, app, "incidents")
	for name, target := range map[string]string{
		"monitors": "network_monitors", "systems": "systems", "statusPages": "status_pages", "user": "users",
	} {
		relation, ok := incidents.Fields.GetByName(name).(*core.RelationField)
		require.True(t, ok, name)
		assert.Equal(t, mustCollection(t, app, target).Id, relation.CollectionId, name)
		assert.Equal(t, name == "user", relation.CascadeDelete, name)
	}
	status, ok := incidents.Fields.GetByName("status").(*core.SelectField)
	require.True(t, ok)
	assert.Equal(t, []string{"investigating", "identified", "monitoring", "resolved"}, status.Values)
	impact, ok := incidents.Fields.GetByName("impact").(*core.SelectField)
	require.True(t, ok)
	assert.Equal(t, []string{"none", "minor", "major", "critical"}, impact.Values)
	assert.True(t, incidents.Fields.GetByName("autoKey").GetHidden())

	updates := mustCollection(t, app, "incident_updates")
	incident, ok := updates.Fields.GetByName("incident").(*core.RelationField)
	require.True(t, ok)
	assert.True(t, incident.CascadeDelete)
	assert.Equal(t, incidents.Id, incident.CollectionId)
	message, ok := updates.Fields.GetByName("message").(*core.TextField)
	require.True(t, ok)
	assert.Equal(t, 5000, message.Max)
	assert.NotNil(t, mustCollection(t, app, "status_pages").Fields.GetByName("autoIncidents"))

	migration := findMigration(t, "_incidents.go")
	require.NoError(t, migration.Down(app))
	for _, name := range []string{"incidents", "incident_updates"} {
		_, err = app.FindCollectionByNameOrId(name)
		assert.Error(t, err, name)
	}
	assert.Nil(t, mustCollection(t, app, "status_pages").Fields.GetByName("autoIncidents"))
	require.NoError(t, migration.Up(app))
	assert.NotNil(t, mustCollection(t, app, "incident_updates").Fields.GetByName("message"))
}
