package hub

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/henrygd/beszel/internal/hub/webpush"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// maxPushSubscriptionsPerUser caps the devices of one user.
const maxPushSubscriptionsPerUser = 25

// serviceWorkerFile is the service worker script in the site dist directory.
const serviceWorkerFile = "sw.js"

// newWebPush creates the browser notification sender. Without it the hub
// still runs; browser notifications are then unavailable.
func newWebPush(h *Hub) *webpush.Service {
	service, err := webpush.New(h)
	if err != nil {
		h.Logger().Error("Browser notifications are unavailable", "err", err)
		return nil
	}
	return service
}

// registerWebPushRoutes registers the push subscription API of the current user.
func (h *Hub) registerWebPushRoutes(apiAuth *router.RouterGroup[*core.RequestEvent]) {
	apiAuth.GET("/push-subscriptions/vapid-key", h.getVapidKey)
	apiAuth.POST("/push-subscriptions", h.createPushSubscription)
	apiAuth.DELETE("/push-subscriptions/{id}", h.deletePushSubscription)
	apiAuth.POST("/push-subscriptions/{id}/test", h.testPushSubscription)
}

// requireUserAuth rejects superusers, which have no subscriptions.
func requireUserAuth(e *core.RequestEvent) error {
	if e.Auth == nil || e.Auth.Collection().Name != "users" {
		return e.ForbiddenError("Only users can use browser notifications.", nil)
	}
	return nil
}

func (h *Hub) getVapidKey(e *core.RequestEvent) error {
	if err := requireUserAuth(e); err != nil {
		return err
	}
	return e.JSON(http.StatusOK, map[string]any{
		"enabled":   h.webPush.Enabled(),
		"publicKey": h.webPush.PublicKey(),
	})
}

type pushSubscriptionRequest struct {
	// Subscription is the browser's PushSubscription.toJSON().
	Subscription struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	} `json:"subscription"`
	Name string `json:"name"`
}

// createPushSubscription saves a browser subscription of the current user. A
// subscription with the same endpoint (the same browser) is replaced, also
// when another user subscribed it before: the device now belongs to this user.
func (h *Hub) createPushSubscription(e *core.RequestEvent) error {
	if err := requireUserAuth(e); err != nil {
		return err
	}
	if !h.webPush.Enabled() {
		return e.Error(http.StatusServiceUnavailable, "Browser notifications are not available.", nil)
	}
	var body pushSubscriptionRequest
	if err := e.BindBody(&body); err != nil {
		return e.BadRequestError("Invalid request body.", nil)
	}
	sub := body.Subscription
	if err := webpush.ValidateEndpoint(sub.Endpoint); err != nil {
		return e.BadRequestError("The push endpoint must be a public https URL.", nil)
	}
	if err := webpush.ValidateKeys(sub.Keys.P256dh, sub.Keys.Auth); err != nil {
		return e.BadRequestError("Invalid subscription keys.", nil)
	}
	userID := e.Auth.Id
	record, err := e.App.FindFirstRecordByFilter(webpush.Collection, "endpoint = {:endpoint}", dbx.Params{"endpoint": sub.Endpoint})
	if err != nil {
		count, err := e.App.CountRecords(webpush.Collection, dbx.HashExp{"user": userID})
		if err != nil {
			return err
		}
		if count >= maxPushSubscriptionsPerUser {
			return e.BadRequestError("Too many devices. Remove a device first.", nil)
		}
		collection, err := e.App.FindCachedCollectionByNameOrId(webpush.Collection)
		if err != nil {
			return err
		}
		record = core.NewRecord(collection)
		record.Set("endpoint", sub.Endpoint)
	}
	record.Set("user", userID)
	record.Set("p256dh", sub.Keys.P256dh)
	record.Set("auth", sub.Keys.Auth)
	record.Set("name", truncateRunes(strings.TrimSpace(body.Name), 100))
	record.Set("userAgent", truncateRunes(e.Request.UserAgent(), 300))
	record.Set("failures", 0)
	if err := e.App.Save(record); err != nil {
		return e.BadRequestError("Failed to save the subscription.", err)
	}
	return e.JSON(http.StatusOK, pushSubscriptionJSON(record))
}

func pushSubscriptionJSON(record *core.Record) map[string]any {
	return map[string]any{
		"id":            record.Id,
		"endpoint":      record.GetString("endpoint"),
		"name":          record.GetString("name"),
		"userAgent":     record.GetString("userAgent"),
		"createdAt":     record.GetString("createdAt"),
		"lastSuccessAt": record.GetString("lastSuccessAt"),
		"failures":      record.GetInt("failures"),
	}
}

// findOwnPushSubscription returns the subscription of the current user, or
// nil after writing a 404.
func findOwnPushSubscription(e *core.RequestEvent) (*core.Record, error) {
	if err := requireUserAuth(e); err != nil {
		return nil, err
	}
	record, err := e.App.FindRecordById(webpush.Collection, e.Request.PathValue("id"))
	if err != nil || record.GetString("user") != e.Auth.Id {
		return nil, e.NotFoundError("Subscription not found.", nil)
	}
	return record, nil
}

func (h *Hub) deletePushSubscription(e *core.RequestEvent) error {
	record, err := findOwnPushSubscription(e)
	if err != nil {
		return err
	}
	if err := e.App.Delete(record); err != nil {
		return err
	}
	return e.NoContent(http.StatusNoContent)
}

func (h *Hub) testPushSubscription(e *core.RequestEvent) error {
	record, err := findOwnPushSubscription(e)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(e.Request.Context(), 30*time.Second)
	defer cancel()
	err = h.webPush.SendOne(ctx, e.Auth.Id, record.Id, webpush.Message{
		Title: "InfraScope",
		Body:  "Browser notifications are working on this device.",
		URL:   h.MakeLink("settings", "notifications"),
		Tag:   "infrascope-test",
	})
	switch {
	case err == nil:
		return e.JSON(http.StatusOK, map[string]bool{"sent": true})
	case errors.Is(err, webpush.ErrDisabled):
		return e.Error(http.StatusServiceUnavailable, "Browser notifications are not available.", nil)
	case errors.Is(err, webpush.ErrSubscriptionGone):
		return e.Error(http.StatusGone, "The browser subscription expired. Enable notifications again.", nil)
	default:
		h.Logger().Warn("Failed to send test browser notification", "err", err)
		return e.Error(http.StatusBadGateway, "The push service did not accept the notification.", nil)
	}
}

// isServiceWorkerPath reports whether a request is for the service worker,
// which must be served from the base path root so its scope covers the app.
// The base path prefix is optional as proxies may strip it.
func isServiceWorkerPath(urlPath, basePath string) bool {
	return urlPath == "/"+serviceWorkerFile || urlPath == basePath+serviceWorkerFile
}

// serveServiceWorker serves the service worker script uncached (browsers
// check it for updates) and allowed to control the whole app.
func serveServiceWorker(e *core.RequestEvent, fsys fs.FS, basePath string) error {
	script, err := fs.ReadFile(fsys, serviceWorkerFile)
	if err != nil {
		return e.NotFoundError("", nil)
	}
	header := e.Response.Header()
	header.Set("Content-Type", "text/javascript; charset=utf-8")
	header.Set("Cache-Control", "no-cache")
	header.Set("Service-Worker-Allowed", basePath)
	return e.Blob(http.StatusOK, "text/javascript; charset=utf-8", script)
}
