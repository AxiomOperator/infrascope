//go:build testing

package systems

import (
	"sync"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusRecordingHub records the status alerts the manager requests.
type statusRecordingHub struct {
	stubHub
	mu       sync.Mutex
	statuses []string
}

func (h *statusRecordingHub) HandleStatusAlerts(status string, _ *core.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.statuses = append(h.statuses, status)
	return nil
}

func (h *statusRecordingHub) recorded() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.statuses...)
}

func newStatusAlertTest(t *testing.T) (*SystemManager, *statusRecordingHub, func(status string)) {
	t.Helper()
	sys, app := newTestSystemWithHub(t)
	hub := &statusRecordingHub{stubHub: stubHub{App: app}}
	sm := NewSystemManager(hub)
	t.Cleanup(func() {
		sm.cancel()
		_ = sm.RemoveSystem(sys.Id)
		sm.smartFetchMap.StopCleaner()
		sm.zfsFetchMap.StopCleaner()
	})
	record, err := app.FindRecordById("systems", sys.Id)
	require.NoError(t, err)
	transition := func(status string) {
		t.Helper()
		record.Set("status", status)
		require.NoError(t, app.SaveNoValidate(record))
		e := &core.RecordEvent{App: app}
		e.Record = record
		require.NoError(t, sm.onRecordAfterUpdateSuccess(e))
	}
	return sm, hub, transition
}

func TestStatusAlertUpPendingDown(t *testing.T) {
	_, hub, transition := newStatusAlertTest(t)
	transition(pending) // new system
	transition(up)
	require.Equal(t, []string{up}, hub.recorded())

	// An edit or resume moves the system through pending; a failure to
	// reconnect must still alert since it was confirmed up before.
	transition(pending)
	transition(down)
	assert.Equal(t, []string{up, down}, hub.recorded())

	// Recovery after the real down still sends up.
	transition(pending)
	transition(up)
	assert.Equal(t, []string{up, down, up}, hub.recorded())
}

func TestStatusAlertDownPendingDownAlertsOnce(t *testing.T) {
	_, hub, transition := newStatusAlertTest(t)
	transition(pending)
	transition(up)
	transition(down)
	transition(pending)
	transition(down)
	assert.Equal(t, []string{up, down}, hub.recorded(), "a system that was already down must not alert again")
}

func TestStatusAlertNeverUpDoesNotAlertDown(t *testing.T) {
	_, hub, transition := newStatusAlertTest(t)
	transition(pending)
	transition(down)
	assert.Empty(t, hub.recorded(), "a new system that never connected has no confirmed up state")
}

// Concurrent adds of one system (e.g. a record hook racing an agent connect)
// must leave exactly one running updater: every replaced system is cancelled.
func TestAddRecordConcurrentReplaceIsAtomic(t *testing.T) {
	sys, app := newTestSystemWithHub(t)
	sm := NewSystemManager(stubHub{App: app})
	t.Cleanup(func() {
		sm.cancel()
		sm.smartFetchMap.StopCleaner()
		sm.zfsFetchMap.StopCleaner()
	})
	record, err := app.FindRecordById("systems", sys.Id)
	require.NoError(t, err)
	record.Set("status", pending)

	const n = 32
	added := make([]*System, n)
	var wg sync.WaitGroup
	for i := range n {
		added[i] = sm.NewSystem(sys.Id)
		wg.Go(func() {
			assert.NoError(t, sm.AddRecord(record, added[i]))
		})
	}
	wg.Wait()

	current, err := sm.GetSystem(sys.Id)
	require.NoError(t, err)
	running := 0
	for _, s := range added {
		if s.ctx.Err() == nil {
			running++
			assert.Same(t, current, s, "only the managed system may keep running")
		}
	}
	assert.Equal(t, 1, running)

	// A replaced system's updater must not remove its replacement.
	for _, s := range added {
		if s != current {
			sm.removeInstance(s)
		}
	}
	assert.True(t, sm.systems.Has(sys.Id))
	require.NoError(t, sm.RemoveSystem(sys.Id))
	assert.Error(t, current.ctx.Err())
}
