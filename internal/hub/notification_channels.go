package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/henrygd/beszel/internal/alerts"
	"github.com/henrygd/beszel/internal/hub/monitorsecrets"
	"github.com/henrygd/beszel/internal/hub/webpush"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// bindNotificationChannels seals the Shoutrrr URLs of notification channels
// at rest, shows them in plaintext to their owner only, and connects browser
// channels to Web Push.
func (h *Hub) bindNotificationChannels() {
	h.AlertManager.SetBrowserSender(h.sendBrowserNotification)

	seal := func(e *core.RecordEvent) error {
		if err := h.sealChannelURL(e.Record); err != nil {
			return err
		}
		return e.Next()
	}
	h.OnRecordCreate("notification_channels").BindFunc(seal)
	h.OnRecordUpdate("notification_channels").BindFunc(seal)

	// Enrich runs for API responses, realtime events and expanded relations.
	h.OnRecordEnrich("notification_channels").BindFunc(func(e *core.RecordEnrichEvent) error {
		raw := channelURLValue(e.Record)
		if raw == "" {
			return e.Next()
		}
		owner := e.RequestInfo != nil && e.RequestInfo.Auth != nil &&
			e.RequestInfo.Auth.Collection().Name == "users" && e.RequestInfo.Auth.Id == e.Record.GetString("user")
		url := ""
		if owner {
			plaintext, err := alerts.ChannelURL(e.App, raw)
			if err != nil {
				e.App.Logger().Error("Failed to decrypt notification channel URL", "channel", e.Record.Id, "err", err)
			} else {
				url = plaintext
			}
		}
		e.Record.Set("config", map[string]any{"url": url})
		return e.Next()
	})

	h.OnServe().BindFunc(func(e *core.ServeEvent) error {
		if err := h.sealStoredChannelURLs(); err != nil {
			h.Logger().Error("Failed to encrypt stored notification channel URLs", "err", err)
		}
		return e.Next()
	})
}

// sendBrowserNotification delivers a browser channel through Web Push.
func (h *Hub) sendBrowserNotification(ctx context.Context, userID string, msg alerts.AlertPushMessage) error {
	if h.webPush == nil || !h.webPush.Enabled() {
		return fmt.Errorf("browser notifications are not available")
	}
	_, err := h.webPush.Send(ctx, userID, webpush.Message{
		Title: msg.Title, Body: msg.Body, URL: msg.URL, Tag: msg.Tag, Urgent: msg.Urgent,
	})
	if errors.Is(err, webpush.ErrNoSubscriptions) {
		return alerts.ErrNoBrowserDevices
	}
	return err
}

// channelURLValue returns the stored Shoutrrr URL of a channel record.
func channelURLValue(record *core.Record) string {
	if record.GetString("type") != alerts.ChannelShoutrrr {
		return ""
	}
	var config struct {
		URL string `json:"url"`
	}
	_ = record.UnmarshalJSONField("config", &config)
	return config.URL
}

// channelSecretsBox returns the box sealing channel URLs, creating the hub
// key when missing.
func (h *Hub) channelSecretsBox() (*monitorsecrets.Box, error) {
	if _, err := h.GetSSHKey(""); err != nil {
		return nil, err
	}
	return monitorsecrets.ForDataDirWithInfo(h.DataDir(), monitorsecrets.ChannelsInfo)
}

// sealChannelURL seals a channel's plaintext URL before it is written. It
// fails closed: a new plaintext URL is rejected without a key, while a
// plaintext value already stored may be kept.
func (h *Hub) sealChannelURL(record *core.Record) error {
	raw := channelURLValue(record)
	if raw == "" || strings.HasPrefix(raw, monitorsecrets.Prefix) {
		return nil
	}
	box, err := h.channelSecretsBox()
	if err == nil {
		var sealed string
		if sealed, err = box.Seal([]byte(raw)); err == nil {
			record.Set("config", map[string]any{"url": sealed})
			return nil
		}
	}
	h.Logger().Error("Failed to encrypt notification channel URL", "channel", record.Id, "err", err)
	if !record.IsNew() && raw == channelURLValue(record.Original()) {
		return nil
	}
	return fmt.Errorf("notification channel URL cannot be stored securely: %w", err)
}

// sealStoredChannelURLs seals plaintext channel URLs stored before the hub
// key existed (e.g. by the migration). It skips rows changed concurrently.
func (h *Hub) sealStoredChannelURLs() error {
	var rows []struct {
		ID     string `db:"id"`
		Config string `db:"config"`
	}
	err := h.DB().Select("id", "config").From("notification_channels").
		Where(dbx.HashExp{"type": alerts.ChannelShoutrrr}).
		AndWhere(dbx.NewExp("config NOT LIKE {:prefix}", dbx.Params{"prefix": `%"` + monitorsecrets.Prefix + "%"})).
		All(&rows)
	if err != nil || len(rows) == 0 {
		return err
	}
	box, err := h.channelSecretsBox()
	if err != nil {
		return err
	}
	collection, err := h.FindCachedCollectionByNameOrId("notification_channels")
	if err != nil {
		return err
	}
	count := 0
	for _, row := range rows {
		record := core.NewRecord(collection)
		record.Load(map[string]any{"type": alerts.ChannelShoutrrr, "config": row.Config})
		raw := channelURLValue(record)
		if raw == "" || strings.HasPrefix(raw, monitorsecrets.Prefix) {
			continue
		}
		sealed, err := box.Seal([]byte(raw))
		if err != nil {
			return err
		}
		record.Set("config", map[string]any{"url": sealed})
		if _, err := h.DB().Update("notification_channels", dbx.Params{"config": record.GetString("config")},
			dbx.HashExp{"id": row.ID, "config": row.Config}).Execute(); err != nil {
			return err
		}
		count++
	}
	if count > 0 {
		h.Logger().Info("Encrypted stored notification channel URLs", "count", count)
	}
	return nil
}
