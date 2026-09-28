//go:build testing

package netmon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitorChecksDrainOnlyOnDefaultInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var failure error
		pm := newManagerWithProbe(func(context.Context, monitor.Config) Outcome {
			if failure != nil {
				return Outcome{ResponseUs: -1, StatusCode: 503, Err: failure}
			}
			return Outcome{ResponseUs: 42, StatusCode: 200}
		}, testDefaultIntervalMs)
		defer pm.Stop()
		_, err := pm.UpsertMonitor(monitor.Config{ID: "m", Interval: 3600}, false)
		require.NoError(t, err)
		task := pm.monitors["m"]

		start := time.Now().UnixMilli()
		task.runProbe(pm.probe)
		time.Sleep(time.Second)
		failure = errors.New("unexpected status 503")
		task.runProbe(pm.probe)
		task.runProbe(pm.probe)
		failure = errors.New("other")
		task.runProbe(pm.probe)

		realtime := pm.GetResults(1000)["m"]
		assert.Empty(t, realtime.Checks, "realtime results must not consume checks")

		result := pm.GetResults(testDefaultIntervalMs)["m"]
		assert.Zero(t, result.Dropped)
		assert.Equal(t, []monitor.CheckEvent{
			{At: start, ResponseUs: 42, StatusCode: 200},
			{At: start + 1000, ResponseUs: -1, StatusCode: 503, Err: "unexpected status 503"},
			{At: start + 1000, ResponseUs: -1, StatusCode: 503}, // repeated error omitted
			{At: start + 1000, ResponseUs: -1, StatusCode: 503, Err: "other"},
		}, result.Checks)
		assert.Empty(t, pm.GetResults(testDefaultIntervalMs)["m"].Checks, "checks are reported once")
	})
}

func TestMonitorChecksCapAndDropped(t *testing.T) {
	task := newMonitorTask(monitor.Config{ID: "m"})
	defer task.cancel()
	fail := legacyProbe(func(context.Context, monitor.Config) (int64, error) { return 0, errors.New("down") })
	for range maxUnsentChecks + 5 {
		task.runProbe(fail)
	}
	checks, dropped := task.takeUnsentChecks()
	require.Len(t, checks, maxUnsentChecks)
	assert.Equal(t, uint32(5), dropped)
	assert.Equal(t, "down", checks[0].Err, "the first event of a batch always has its error")
	assert.Empty(t, checks[1].Err)
	checks, dropped = task.takeUnsentChecks()
	assert.Empty(t, checks)
	assert.Zero(t, dropped)
}

func TestMonitorRunNowReportsItsCheckOnce(t *testing.T) {
	var calls atomic.Int32
	pm := newManagerWithProbe(func(context.Context, monitor.Config) Outcome {
		calls.Add(1)
		return Outcome{ResponseUs: 7, StatusCode: 204}
	}, testDefaultIntervalMs)
	defer pm.Stop()
	result, err := pm.UpsertMonitor(monitor.Config{ID: "m", Interval: 3600}, true)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Checks, 1)
	assert.Equal(t, int64(7), result.Checks[0].ResponseUs)
	assert.Equal(t, uint16(204), result.Checks[0].StatusCode)
	assert.Empty(t, pm.GetResults(testDefaultIntervalMs)["m"].Checks, "an immediate check is not reported again")

	// Scheduled checks are still queued.
	pm.monitors["m"].runProbe(pm.probe)
	assert.Len(t, pm.GetResults(testDefaultIntervalMs)["m"].Checks, 1)
}

func TestMonitorChecksSurviveConfigChange(t *testing.T) {
	pm := newManagerWithProbe(legacyProbe(func(context.Context, monitor.Config) (int64, error) { return 1, nil }), testDefaultIntervalMs)
	defer pm.Stop()
	config := monitor.Config{ID: "m", Interval: 3600}
	_, err := pm.UpsertMonitor(config, false)
	require.NoError(t, err)
	pm.monitors["m"].runProbe(pm.probe)
	config.Interval = 1800
	_, err = pm.UpsertMonitor(config, false)
	require.NoError(t, err)
	assert.Len(t, pm.GetResults(testDefaultIntervalMs)["m"].Checks, 1)
}

func TestMonitorScheduleRetryInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var failing atomic.Bool
		var calls atomic.Int32
		go runMonitorSchedule(ctx, 10*time.Second, 2*time.Second, 0, func() bool {
			calls.Add(1)
			return failing.Load()
		})
		synctest.Wait()
		assert.Equal(t, 1, int(calls.Load()))
		time.Sleep(10 * time.Second)
		synctest.Wait()
		assert.Equal(t, 2, int(calls.Load()))

		// A failure switches to the retry interval.
		failing.Store(true)
		time.Sleep(10 * time.Second)
		synctest.Wait()
		assert.Equal(t, 3, int(calls.Load()))
		time.Sleep(2 * time.Second)
		synctest.Wait()
		assert.Equal(t, 4, int(calls.Load()))
		time.Sleep(2 * time.Second)
		synctest.Wait()
		assert.Equal(t, 5, int(calls.Load()))

		// Success returns to the regular interval.
		failing.Store(false)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		assert.Equal(t, 6, int(calls.Load()))
		time.Sleep(2 * time.Second)
		synctest.Wait()
		assert.Equal(t, 6, int(calls.Load()))
		time.Sleep(8 * time.Second)
		synctest.Wait()
		assert.Equal(t, 7, int(calls.Load()))
	})
}

func TestMonitorRetryIntervalFromConfig(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		pm := newManagerWithProbe(func(context.Context, monitor.Config) Outcome {
			calls.Add(1)
			return outcomeOf(-1, errors.New("down"))
		}, testDefaultIntervalMs)
		defer pm.Stop()
		pm.SyncMonitors([]monitor.Config{{ID: "m", Interval: 60, RetryInterval: 5}})
		// The first check runs after a random stagger; retries then follow every 5s.
		for range 60 {
			if calls.Load() > 0 {
				break
			}
			time.Sleep(time.Second)
			synctest.Wait()
		}
		require.Equal(t, int32(1), calls.Load())
		time.Sleep(20 * time.Second)
		synctest.Wait()
		assert.Equal(t, int32(5), calls.Load())
	})
}

func TestLimitProbeConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var running, peak atomic.Int32
		release := make(chan struct{})
		probe := limitProbe(func(ctx context.Context, _ monitor.Config) Outcome {
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-release
			running.Add(-1)
			return Outcome{ResponseUs: 1}
		}, make(chan struct{}, 2))
		var wg sync.WaitGroup
		for range 5 {
			wg.Go(func() { probe(t.Context(), monitor.Config{}) })
		}
		synctest.Wait()
		assert.Equal(t, int32(2), running.Load())
		close(release)
		wg.Wait()
		assert.Equal(t, int32(2), peak.Load())

		// Waiting for a slot honors cancellation.
		sem := make(chan struct{}, 1)
		sem <- struct{}{}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		out := limitProbe(func(context.Context, monitor.Config) Outcome { return Outcome{} }, sem)(ctx, monitor.Config{})
		assert.ErrorIs(t, out.Err, context.Canceled)
	})
}

func TestManagerWithConcurrencyLimitsProbes(t *testing.T) {
	var running, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}))
	defer server.Close()
	pm := NewManager(testDefaultIntervalMs, WithConcurrency(1))
	defer pm.Stop()
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c", "d"} {
		wg.Go(func() {
			result, err := pm.UpsertMonitor(monitor.Config{ID: id, Protocol: "http", Target: server.URL, Interval: 3600}, true)
			assert.NoError(t, err)
			assert.NotNil(t, result)
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), peak.Load())
}

func TestManagerOnCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	type call struct {
		id    string
		event monitor.CheckEvent
	}
	var mu sync.Mutex
	var calls []call
	pm := NewManager(testDefaultIntervalMs, WithOnCheck(func(id string, event monitor.CheckEvent) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call{id, event})
	}))
	defer pm.Stop()

	result, err := pm.UpsertMonitor(monitor.Config{ID: "http", Protocol: "http", Target: server.URL, Interval: 3600}, true)
	require.NoError(t, err)
	require.NotNil(t, result)
	pm.monitors["http"].runProbe(pm.probe)
	_, err = pm.UpsertMonitor(monitor.Config{ID: "push", Protocol: monitor.ProtocolPush, Interval: 60}, false)
	require.NoError(t, err)
	require.NoError(t, pm.RecordExternal("push", Outcome{ResponseUs: 5}))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, calls, 3)
	assert.Equal(t, "http", calls[0].id)
	assert.Equal(t, result.Checks[0], calls[0].event, "immediate checks are reported to the callback")
	assert.Equal(t, monitor.CheckEvent{At: calls[0].event.At, ResponseUs: -1, StatusCode: 503, Err: "unexpected status 503"}, calls[0].event)
	assert.Equal(t, "http", calls[1].id)
	assert.Equal(t, "push", calls[2].id)
	assert.Equal(t, int64(5), calls[2].event.ResponseUs)
}

func TestPushMonitorRecordsExternalOutcomes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		pm := newManagerWithProbe(func(context.Context, monitor.Config) Outcome {
			calls.Add(1)
			return Outcome{ResponseUs: 1}
		}, testDefaultIntervalMs)
		defer pm.Stop()
		config := monitor.Config{ID: "push", Protocol: monitor.ProtocolPush, Interval: 1}
		pm.SyncMonitors([]monitor.Config{config})
		result, err := pm.UpsertMonitor(config, true)
		require.NoError(t, err)
		assert.Nil(t, result, "push monitors without history have no result")

		time.Sleep(time.Hour)
		synctest.Wait()
		assert.Zero(t, calls.Load(), "push monitors are never probed")
		assert.Empty(t, pm.GetResults(testDefaultIntervalMs))

		require.NoError(t, pm.RecordExternal("push", Outcome{ResponseUs: 30}))
		require.NoError(t, pm.RecordExternal("push", Outcome{ResponseUs: -1, Err: errors.New("missed")}))
		assert.Error(t, pm.RecordExternal("unknown", Outcome{}))

		result, err = pm.UpsertMonitor(config, true)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Empty(t, result.Checks)
		assert.Zero(t, calls.Load())

		results := pm.GetResults(testDefaultIntervalMs)
		require.Contains(t, results, "push")
		assert.Equal(t, 50.0, results["push"].PacketLoss)
		assert.Equal(t, int64(30), results["push"].AvgResponse)
		assert.Equal(t, int64(2), results["push"].SampleCount)
		require.Len(t, results["push"].Checks, 2)
		assert.Equal(t, int64(30), results["push"].Checks[0].ResponseUs)
		assert.Equal(t, monitor.CheckEvent{At: results["push"].Checks[1].At, ResponseUs: -1, Err: "missed"}, results["push"].Checks[1])
	})
}

func TestRecordExternalOnCanceledTask(t *testing.T) {
	task := newMonitorTask(monitor.Config{ID: "m"})
	task.cancel()
	assert.False(t, task.recordExternal(Outcome{ResponseUs: 1}))
	assert.Empty(t, task.history.samples)
}
