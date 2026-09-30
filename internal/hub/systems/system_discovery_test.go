//go:build testing

package systems

import (
	"testing"

	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discoveryHub records the discovery payloads passed to the reconciler.
type discoveryHub struct {
	stubHub
	calls *[]*system.Discovery
}

func (h discoveryHub) ReconcileDockerDiscovery(_ *core.Record, discovery *system.Discovery) {
	*h.calls = append(*h.calls, discovery)
}

func TestCreateRecordsDockerDiscovery(t *testing.T) {
	sys, app := newTestSystemWithHub(t)
	var calls []*system.Discovery
	sys.manager.hub = discoveryHub{stubHub: stubHub{App: app}, calls: &calls}
	payload := &system.Discovery{Monitors: []system.DiscoveredMonitor{{Container: "web", Key: "default"}}}

	// Off: nothing is reconciled and the next request does not ask for discovery.
	_, err := sys.createRecords(&system.CombinedData{Discovery: payload})
	require.NoError(t, err)
	assert.Empty(t, calls)
	assert.False(t, sys.autoDiscover.Load())

	record, err := app.FindRecordById("systems", sys.Id)
	require.NoError(t, err)
	record.Set("autoDiscover", true)
	require.NoError(t, app.SaveNoValidate(record))

	// Responses without discovery (older agents, Docker unavailable) are not reconciled.
	_, err = sys.createRecords(&system.CombinedData{})
	require.NoError(t, err)
	assert.Empty(t, calls)
	assert.True(t, sys.autoDiscover.Load())

	_, err = sys.createRecords(&system.CombinedData{Discovery: payload})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Same(t, payload, calls[0])
}
