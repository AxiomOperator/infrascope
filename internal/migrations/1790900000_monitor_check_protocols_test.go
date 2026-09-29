//go:build testing

package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitorCheckProtocolsMigration(t *testing.T) {
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	defer app.Cleanup()

	monitors := mustCollection(t, app, "network_monitors")
	protocol := monitors.Fields.GetByName("protocol").(*core.SelectField)
	for _, value := range append([]string{"icmp", "tcp", "http", "dns", "push"}, monitorCheckProtocols...) {
		assert.Contains(t, protocol.Values, value)
	}
	check := monitors.Fields.GetByName("check")
	require.NotNil(t, check)
	assert.False(t, check.GetHidden())

	migration := findMigration(t, "_monitor_check_protocols.go")
	require.NoError(t, migration.Down(app))
	monitors = mustCollection(t, app, "network_monitors")
	assert.Nil(t, monitors.Fields.GetByName("check"))
	assert.NotContains(t, monitors.Fields.GetByName("protocol").(*core.SelectField).Values, "redis")
	assert.Contains(t, monitors.Fields.GetByName("protocol").(*core.SelectField).Values, "push")
	require.NoError(t, migration.Up(app))
	assert.NotNil(t, mustCollection(t, app, "network_monitors").Fields.GetByName("check"))
}
