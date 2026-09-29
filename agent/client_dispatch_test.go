//go:build testing

package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel/internal/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedResponse struct {
	id   uint32
	data any
	err  string
}

// fakeResponder records responses instead of writing to a WebSocket.
type fakeResponder struct {
	mu        sync.Mutex
	responses []recordedResponse
	notify    chan struct{}
}

func newFakeResponder() *fakeResponder {
	return &fakeResponder{notify: make(chan struct{}, 64)}
}

func (r *fakeResponder) record(resp recordedResponse) {
	r.mu.Lock()
	r.responses = append(r.responses, resp)
	r.mu.Unlock()
	r.notify <- struct{}{}
}

func (r *fakeResponder) sendResponse(data any, requestID *uint32) error {
	r.record(recordedResponse{id: *requestID, data: data})
	return nil
}

func (r *fakeResponder) sendError(requestID *uint32, err error) {
	r.record(recordedResponse{id: *requestID, err: err.Error()})
}

func (r *fakeResponder) snapshot() []recordedResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedResponse(nil), r.responses...)
}

func (r *fakeResponder) waitFor(t *testing.T, n int) []recordedResponse {
	t.Helper()
	require.Eventually(t, func() bool { return len(r.snapshot()) >= n }, 5*time.Second, time.Millisecond)
	return r.snapshot()
}

func newDispatchTestClient(handler RequestHandler) *WebSocketClient {
	registry := &HandlerRegistry{handlers: map[common.WebSocketAction]RequestHandler{
		common.GetSmartData:   handler,
		common.GetSystemdInfo: handler,
	}}
	client := &WebSocketClient{agent: &Agent{handlerRegistry: registry}}
	client.hubVerified.Store(true)
	return client
}

func slowRequest(id uint32, action common.WebSocketAction, data string) *common.HubRequest[cbor.RawMessage] {
	raw, _ := cbor.Marshal(data)
	return &common.HubRequest[cbor.RawMessage]{Id: &id, Action: action, Data: raw}
}

type funcHandler func(hctx *HandlerContext) error

func (f funcHandler) Handle(hctx *HandlerContext) error { return f(hctx) }

func TestSlowRequestsDoNotBlockAndRespondOutOfOrder(t *testing.T) {
	release := make(chan struct{})
	handler := funcHandler(func(hctx *HandlerContext) error {
		var name string
		_ = cbor.Unmarshal(hctx.Request.Data, &name)
		if name == "slow" {
			select {
			case <-release:
			case <-hctx.Ctx.Done():
				return hctx.Ctx.Err()
			}
		}
		if _, ok := hctx.Ctx.Deadline(); !ok {
			return errors.New("slow request context has no deadline")
		}
		return hctx.SendResponse(name, hctx.RequestID)
	})
	client := newDispatchTestClient(handler)
	r := newFakeResponder()

	start := time.Now()
	require.True(t, client.dispatchSlowRequest(slowRequest(1, common.GetSmartData, "slow"), r))
	require.True(t, client.dispatchSlowRequest(slowRequest(2, common.GetSystemdInfo, "fast"), r))
	assert.Less(t, time.Since(start), time.Second, "dispatch must not wait for the handler")

	responses := r.waitFor(t, 1)
	assert.Equal(t, recordedResponse{id: 2, data: "fast"}, responses[0], "later request answers first")

	close(release)
	responses = r.waitFor(t, 2)
	assert.Equal(t, recordedResponse{id: 1, data: "slow"}, responses[1])
}

func TestSlowRequestErrorsAreReportedToHub(t *testing.T) {
	client := newDispatchTestClient(funcHandler(func(hctx *HandlerContext) error {
		return errContainerExcluded
	}))
	r := newFakeResponder()
	require.True(t, client.dispatchSlowRequest(slowRequest(7, common.GetSmartData, ""), r))
	responses := r.waitFor(t, 1)
	assert.Equal(t, recordedResponse{id: 7, err: errContainerExcluded.Error()}, responses[0])
}

func TestSlowRequestsAreBounded(t *testing.T) {
	release := make(chan struct{})
	var running, maxRunning atomic.Int32
	client := newDispatchTestClient(funcHandler(func(hctx *HandlerContext) error {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		<-release
		return hctx.SendResponse("ok", hctx.RequestID)
	}))
	r := newFakeResponder()

	total := maxConcurrentSlowRequests + 3
	for i := range total {
		require.True(t, client.dispatchSlowRequest(slowRequest(uint32(i+1), common.GetSmartData, ""), r))
	}
	require.Eventually(t, func() bool { return running.Load() == maxConcurrentSlowRequests }, 5*time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	assert.EqualValues(t, maxConcurrentSlowRequests, maxRunning.Load())

	close(release)
	responses := r.waitFor(t, total)
	assert.Len(t, responses, total)
	assert.EqualValues(t, maxConcurrentSlowRequests, maxRunning.Load())
}

func TestFastAndLegacyRequestsAreHandledInline(t *testing.T) {
	client := newDispatchTestClient(funcHandler(func(hctx *HandlerContext) error { return nil }))
	r := newFakeResponder()

	// GetData stays on the read loop's fast path.
	assert.False(t, client.dispatchSlowRequest(slowRequest(1, common.GetData, ""), r))
	// Legacy requests without an id are matched by order, so they stay inline.
	legacy := slowRequest(2, common.GetSmartData, "")
	legacy.Id = nil
	assert.False(t, client.dispatchSlowRequest(legacy, r))
}

func TestHandlerContextDefaultsToBackground(t *testing.T) {
	hctx := &HandlerContext{}
	assert.Equal(t, context.Background(), hctx.context())
}
