//go:build testing

package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenameAppName(t *testing.T) {
	app, err := tests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	settings := app.Settings()
	settings.Meta.AppName = "Beszel"
	settings.Meta.SenderName = "Beszel"
	require.NoError(t, app.Save(settings))
	require.NoError(t, renameAppName(app, "Beszel", "InfraScope"))
	assert.Equal(t, "InfraScope", app.Settings().Meta.AppName)
	assert.Equal(t, "InfraScope", app.Settings().Meta.SenderName)

	// custom names are kept
	settings = app.Settings()
	settings.Meta.AppName = "Homelab"
	settings.Meta.SenderName = "Ops"
	require.NoError(t, app.Save(settings))
	require.NoError(t, renameAppName(app, "Beszel", "InfraScope"))
	assert.Equal(t, "Homelab", app.Settings().Meta.AppName)
	assert.Equal(t, "Ops", app.Settings().Meta.SenderName)
}
