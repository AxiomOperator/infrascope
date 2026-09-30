//go:build testing

package hub

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/henrygd/beszel/internal/alerts"
	"github.com/henrygd/beszel/internal/hub/monitorsecrets"
	"github.com/pocketbase/dbx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSealStoredChannelURLs(t *testing.T) {
	env := newMonitorTestEnv(t)
	record, err := createTestRecord(env.hub, "notification_channels", map[string]any{
		"user": env.owner.Id, "name": "hook", "type": "shoutrrr", "enabled": true,
		"config": map[string]any{"url": "generic+https://example.com/x"},
	})
	require.NoError(t, err)
	// A row stored before the key existed (e.g. by the migration).
	plaintext := `{"url":"generic+https://example.com/legacy"}`
	_, err = env.hub.DB().Update("notification_channels", dbx.Params{"config": plaintext}, dbx.HashExp{"id": record.Id}).Execute()
	require.NoError(t, err)

	require.NoError(t, env.hub.sealStoredChannelURLs())
	var raw string
	require.NoError(t, env.hub.DB().NewQuery("SELECT config FROM notification_channels WHERE id={:id}").Bind(dbx.Params{"id": record.Id}).Row(&raw))
	assert.Contains(t, raw, monitorsecrets.Prefix)
	assert.NotContains(t, raw, "legacy")
	stored, err := env.hub.FindRecordById("notification_channels", record.Id)
	require.NoError(t, err)
	url, err := alerts.ChannelURL(env.hub, channelURLValue(stored))
	require.NoError(t, err)
	assert.Equal(t, "generic+https://example.com/legacy", url)
	// Idempotent.
	require.NoError(t, env.hub.sealStoredChannelURLs())
}

func TestMonitorChannelsValidation(t *testing.T) {
	env := newMonitorTestEnv(t)
	stranger := createMonitorTestUser(t, env.hub, "stranger@example.com", "user")
	channel := func(user string) string {
		record, err := createTestRecord(env.hub, "notification_channels", map[string]any{
			"user": user, "name": "c", "type": "browser", "enabled": true,
		})
		require.NoError(t, err)
		return record.Id
	}
	mine, theirs := channel(env.owner.Id), channel(stranger.Id)

	code, body := env.postMonitor(t, env.owner, env.hubMonitor(map[string]any{"channels": []string{theirs}}))
	assert.Equal(t, http.StatusBadRequest, code, body)
	assert.Contains(t, body, "Invalid notification channels")

	monitor := env.createMonitor(t, env.hubMonitor(map[string]any{"channels": []string{mine}, "severity": "warning"}))
	assert.Equal(t, []string{mine}, monitor.GetStringSlice("channels"))
	assert.Equal(t, "warning", monitor.GetString("severity"))

	// Channels of other users already on a shared monitor are kept on update.
	monitor.Set("channels", []string{mine, theirs})
	require.NoError(t, env.hub.SaveNoValidate(monitor))
	response := env.updateMonitor(t, monitor.Id, env.owner, map[string]any{"name": "renamed", "channels": []string{mine, theirs}})
	assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
}

func TestBrowserSender(t *testing.T) {
	env := newMonitorTestEnv(t)
	hub := env.hub
	if hub.webPush.Enabled() {
		// A user without subscribed browsers gets ErrNoBrowserDevices, which alerts skip quietly.
		err := hub.sendBrowserNotification(context.Background(), env.owner.Id, alerts.AlertPushMessage{Title: "x"})
		require.ErrorIs(t, err, alerts.ErrNoBrowserDevices)
	}
	saved := hub.webPush
	hub.webPush = nil
	defer func() { hub.webPush = saved }()
	err := hub.sendBrowserNotification(context.Background(), env.owner.Id, alerts.AlertPushMessage{Title: "x"})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "not available"))
}
