//go:build testing

package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlertAcknowledgementsMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	history := mustCollection(t, app, "alerts_history")
	for _, name := range alertAckFields {
		assert.NotNil(t, history.Fields.GetByName(name), name)
	}
	assert.Nil(t, history.UpdateRule, "clients must not update history rows")
	by, ok := history.Fields.GetByName("acknowledgedBy").(*core.RelationField)
	require.True(t, ok)
	assert.Equal(t, "_pb_users_auth_", by.CollectionId)
	assert.Equal(t, 1000, history.Fields.GetByName("ackNote").(*core.TextField).Max)

	notes := mustCollection(t, app, "alert_notes")
	alert, ok := notes.Fields.GetByName("alert").(*core.RelationField)
	require.True(t, ok)
	assert.Equal(t, history.Id, alert.CollectionId)
	assert.True(t, alert.CascadeDelete)
	require.NotNil(t, notes.ListRule)
	assert.Contains(t, *notes.ListRule, "alert.user = @request.auth.id")
	require.NotNil(t, notes.CreateRule)
	assert.Contains(t, *notes.CreateRule, `@request.auth.role != "readonly"`)
	assert.Nil(t, notes.UpdateRule)

	migration := findMigration(t, "_alert_acknowledgements.go")
	require.NoError(t, migration.Down(app))
	history = mustCollection(t, app, "alerts_history")
	for _, name := range alertAckFields {
		assert.Nil(t, history.Fields.GetByName(name), name)
	}
	_, err = app.FindCollectionByNameOrId("alert_notes")
	assert.Error(t, err)
	require.NoError(t, migration.Up(app))
	assert.NotNil(t, mustCollection(t, app, "alerts_history").Fields.GetByName("acknowledgedAt"))
	mustCollection(t, app, "alert_notes")
}
