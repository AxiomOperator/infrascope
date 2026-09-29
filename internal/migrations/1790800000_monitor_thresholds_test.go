//go:build testing

package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitorThresholdsMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	monitors := mustCollection(t, app, "network_monitors")
	for _, name := range []string{"lossThreshold", "latencyThreshold"} {
		field := monitors.Fields.GetByName(name)
		require.NotNil(t, field, name)
		assert.False(t, field.GetHidden(), name)
	}
	state := monitors.Fields.GetByName("alertState")
	require.NotNil(t, state)
	assert.True(t, state.GetHidden(), "alert state is server-managed and hidden")
	assert.EqualValues(t, 1<<20, monitors.Fields.GetByName("httpSecrets").(*core.JSONField).MaxSize)

	migration := findMigration(t, "_monitor_thresholds.go")
	require.NoError(t, migration.Down(app))
	monitors = mustCollection(t, app, "network_monitors")
	assert.Nil(t, monitors.Fields.GetByName("lossThreshold"))
	assert.Nil(t, monitors.Fields.GetByName("alertState"))
	require.NoError(t, migration.Up(app))
	assert.NotNil(t, mustCollection(t, app, "network_monitors").Fields.GetByName("latencyThreshold"))
}
