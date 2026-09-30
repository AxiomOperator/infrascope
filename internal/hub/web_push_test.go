//go:build testing

package hub

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/henrygd/beszel/internal/hub/webpush"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pushKeys returns valid p256dh and auth keys of a browser subscription.
func pushKeys(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(secret)
}

func pushSubscriptionBody(t *testing.T, endpoint, name string) string {
	t.Helper()
	p256dh, auth := pushKeys(t)
	body, err := json.Marshal(map[string]any{
		"subscription": map[string]any{"endpoint": endpoint, "keys": map[string]string{"p256dh": p256dh, "auth": auth}},
		"name":         name,
	})
	require.NoError(t, err)
	return string(body)
}

func TestPushSubscriptionAPI(t *testing.T) {
	env := newStatusPageTestEnv(t)
	do := func(method, url string, auth *core.Record, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, url, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0")
		if auth != nil {
			request.Header.Set("Authorization", authToken(t, auth))
		}
		response := httptest.NewRecorder()
		env.handler.ServeHTTP(response, request)
		return response
	}

	// VAPID key: users only
	assert.Equal(t, http.StatusUnauthorized, do(http.MethodGet, "/api/beszel/push-subscriptions/vapid-key", nil, "").Code)
	superuser := env.create(t, core.CollectionNameSuperusers, map[string]any{"email": "admin@example.com", "password": "testtesttest"})
	assert.Equal(t, http.StatusForbidden, do(http.MethodGet, "/api/beszel/push-subscriptions/vapid-key", superuser, "").Code)
	response := do(http.MethodGet, "/api/beszel/push-subscriptions/vapid-key", env.owner, "")
	require.Equal(t, http.StatusOK, response.Code)
	var key struct {
		Enabled   bool   `json:"enabled"`
		PublicKey string `json:"publicKey"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &key))
	assert.True(t, key.Enabled)
	assert.Equal(t, env.hub.WebPush().PublicKey(), key.PublicKey)
	assert.Len(t, key.PublicKey, 87)

	// endpoints must be public https URLs; keys must be valid
	for _, endpoint := range []string{"http://push.example.com/x", "https://127.0.0.1/x", "https://10.0.0.1/x", "https://localhost/x", "https://[::1]/x"} {
		response := do(http.MethodPost, "/api/beszel/push-subscriptions", env.owner, pushSubscriptionBody(t, endpoint, "x"))
		assert.Equal(t, http.StatusBadRequest, response.Code, endpoint)
	}
	badKeys := `{"subscription":{"endpoint":"https://fcm.googleapis.com/fcm/send/a","keys":{"p256dh":"AAAA","auth":"AAAA"}}}`
	assert.Equal(t, http.StatusBadRequest, do(http.MethodPost, "/api/beszel/push-subscriptions", env.owner, badKeys).Code)
	assert.Equal(t, http.StatusUnauthorized, do(http.MethodPost, "/api/beszel/push-subscriptions", nil, pushSubscriptionBody(t, "https://fcm.googleapis.com/fcm/send/a", "x")).Code)

	endpoint := "https://fcm.googleapis.com/fcm/send/abc"
	response = do(http.MethodPost, "/api/beszel/push-subscriptions", env.owner, pushSubscriptionBody(t, endpoint, "  Laptop  "))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &created))
	assert.Equal(t, "Laptop", created["name"])
	assert.NotContains(t, created, "p256dh")
	record, err := env.hub.FindRecordById(webpush.Collection, created["id"].(string))
	require.NoError(t, err)
	assert.Equal(t, env.owner.Id, record.GetString("user"))
	assert.Contains(t, record.GetString("userAgent"), "Firefox")

	// owners list their own subscriptions; keys are hidden
	list := do(http.MethodGet, "/api/collections/push_subscriptions/records", env.owner, "")
	require.Equal(t, http.StatusOK, list.Code)
	assert.Contains(t, list.Body.String(), record.Id)
	assert.NotContains(t, list.Body.String(), record.GetString("auth"))
	list = do(http.MethodGet, "/api/collections/push_subscriptions/records", env.other, "")
	require.Equal(t, http.StatusOK, list.Code)
	assert.NotContains(t, list.Body.String(), record.Id)
	// no direct creation through the records API
	assert.NotEqual(t, http.StatusOK, do(http.MethodPost, "/api/collections/push_subscriptions/records", env.owner, `{"user":"`+env.owner.Id+`","endpoint":"https://x.example.com/1","p256dh":"a","auth":"b"}`).Code)

	// other users can neither test nor delete it
	assert.Equal(t, http.StatusNotFound, do(http.MethodDelete, "/api/beszel/push-subscriptions/"+record.Id, env.other, "").Code)
	assert.Equal(t, http.StatusNotFound, do(http.MethodPost, "/api/beszel/push-subscriptions/"+record.Id+"/test", env.other, "").Code)

	// the same browser subscribing for another user moves to that user
	response = do(http.MethodPost, "/api/beszel/push-subscriptions", env.other, pushSubscriptionBody(t, endpoint, "Shared"))
	require.Equal(t, http.StatusOK, response.Code)
	total, err := env.hub.CountRecords(webpush.Collection)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	record, err = env.hub.FindRecordById(webpush.Collection, record.Id)
	require.NoError(t, err)
	assert.Equal(t, env.other.Id, record.GetString("user"))

	assert.Equal(t, http.StatusNoContent, do(http.MethodDelete, "/api/beszel/push-subscriptions/"+record.Id, env.other, "").Code)
	_, err = env.hub.FindRecordById(webpush.Collection, record.Id)
	assert.Error(t, err)

	// test notifications go through the push service
	var status atomic.Int32
	status.Store(http.StatusCreated)
	var received atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	defer server.Close()
	env.hub.webPush, err = webpush.New(env.hub, webpush.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	p256dh, auth := pushKeys(t)
	device := env.create(t, webpush.Collection, map[string]any{"user": env.owner.Id, "endpoint": server.URL + "/d", "p256dh": p256dh, "auth": auth})
	response = do(http.MethodPost, "/api/beszel/push-subscriptions/"+device.Id+"/test", env.owner, "")
	assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.EqualValues(t, 1, received.Load())
	status.Store(http.StatusInternalServerError)
	assert.Equal(t, http.StatusBadGateway, do(http.MethodPost, "/api/beszel/push-subscriptions/"+device.Id+"/test", env.owner, "").Code)
	status.Store(http.StatusGone)
	assert.Equal(t, http.StatusGone, do(http.MethodPost, "/api/beszel/push-subscriptions/"+device.Id+"/test", env.owner, "").Code)
	_, err = env.hub.FindRecordById(webpush.Collection, device.Id)
	assert.Error(t, err, "expired subscriptions are deleted")

	// Send reaches all of the user's devices
	status.Store(http.StatusCreated)
	env.create(t, webpush.Collection, map[string]any{"user": env.owner.Id, "endpoint": server.URL + "/e", "p256dh": p256dh, "auth": auth})
	sent, err := env.hub.WebPush().Send(t.Context(), env.owner.Id, webpush.Message{Title: "t"})
	require.NoError(t, err)
	assert.Equal(t, 1, sent)

	// without a sender, browser notifications are unavailable
	env.hub.webPush = nil
	response = do(http.MethodGet, "/api/beszel/push-subscriptions/vapid-key", env.owner, "")
	assert.Contains(t, response.Body.String(), `"enabled":false`)
	assert.Equal(t, http.StatusServiceUnavailable, do(http.MethodPost, "/api/beszel/push-subscriptions", env.owner, pushSubscriptionBody(t, endpoint, "x")).Code)
}

func TestServiceWorkerRoute(t *testing.T) {
	assert.True(t, isServiceWorkerPath("/sw.js", "/"))
	assert.True(t, isServiceWorkerPath("/hub/sw.js", "/hub/"))
	assert.True(t, isServiceWorkerPath("/sw.js", "/hub/"), "proxy stripped the base path")
	assert.False(t, isServiceWorkerPath("/static/sw.js", "/"))
	assert.False(t, isServiceWorkerPath("/other/sw.js", "/hub/"))

	hub, testApp, err := createTestHub(t)
	require.NoError(t, err)
	defer cleanupTestHub(hub, testApp)
	fsys := fstest.MapFS{"sw.js": {Data: []byte("self.addEventListener('push', () => {})")}}
	request := httptest.NewRequest(http.MethodGet, "/hub/sw.js", nil)
	response := httptest.NewRecorder()
	event := &core.RequestEvent{App: hub}
	event.Response, event.Request = response, request
	require.NoError(t, serveServiceWorker(event, fsys, "/hub/"))
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "/hub/", response.Header().Get("Service-Worker-Allowed"))
	assert.Equal(t, "no-cache", response.Header().Get("Cache-Control"))
	assert.Contains(t, response.Header().Get("Content-Type"), "javascript")
	assert.Contains(t, response.Body.String(), "push")
}
