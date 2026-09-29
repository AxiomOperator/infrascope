//go:build testing

package migrations

import (
	"testing"

	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSystemHostKeyMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	systems := mustCollection(t, app, "systems")
	hostKey := systems.Fields.GetByName("hostKey")
	require.NotNil(t, hostKey)
	assert.True(t, hostKey.GetHidden(), "the pinned host key is not exposed to clients")
	downReason := systems.Fields.GetByName("downReason")
	require.NotNil(t, downReason)
	assert.False(t, downReason.GetHidden())

	migration := findMigration(t, "_system_host_key.go")
	require.NoError(t, migration.Down(app))
	systems = mustCollection(t, app, "systems")
	assert.Nil(t, systems.Fields.GetByName("hostKey"))
	assert.Nil(t, systems.Fields.GetByName("downReason"))
	require.NoError(t, migration.Up(app))
	assert.NotNil(t, mustCollection(t, app, "systems").Fields.GetByName("hostKey"))
}
