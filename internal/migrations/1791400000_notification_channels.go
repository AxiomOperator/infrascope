package migrations

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/henrygd/beszel/internal/hub/monitorsecrets"
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

const notificationChannelsCollectionId = "notification_channels_001"

// severityValues are the alert severities, from lowest to highest.
var severityValues = []string{"info", "warning", "critical"}

// Adds named notification channels with severity routing:
//
//   - notification_channels: a user's destinations (email addresses, a
//     Shoutrrr URL, or the user's browsers via Web Push). minSeverity is the
//     lowest severity a default channel receives; isDefault channels receive
//     alerts without explicit channels. Shoutrrr URLs are stored sealed with
//     the hub key (see monitorsecrets.ChannelsInfo); the hub seals values
//     stored as plaintext on startup.
//   - alerts.severity, alerts.channels, network_monitors.severity,
//     network_monitors.channels: optional per-alert severity override and
//     explicit channels (empty = default routing).
//   - alerts_history.severity: the severity an alert was sent with.
//
// Existing user_settings.emails and webhooks are copied into default
// channels with the lowest threshold, so delivery is unchanged. The settings
// keys are kept (they are only read for users without channels).
func init() {
	m.Register(func(app core.App) error {
		channels := core.NewBaseCollection("notification_channels", notificationChannelsCollectionId)
		owner := `@request.auth.id != "" && user = @request.auth.id`
		write := owner + ` && @request.auth.role != "readonly"`
		channels.ListRule = types.Pointer(owner)
		channels.ViewRule = types.Pointer(owner)
		channels.CreateRule = types.Pointer(write)
		channels.UpdateRule = types.Pointer(write + ` && (@request.body.user:changed = false || @request.body.user = @request.auth.id)`)
		channels.DeleteRule = types.Pointer(write)
		channels.Fields.Add(
			&core.RelationField{Id: "nc_user", Name: "user", CollectionId: "_pb_users_auth_", MaxSelect: 1, Required: true, CascadeDelete: true},
			&core.TextField{Id: "nc_name", Name: "name", Required: true, Max: 100},
			&core.SelectField{Id: "nc_type", Name: "type", Required: true, MaxSelect: 1, Values: []string{"email", "shoutrrr", "browser"}},
			&core.JSONField{Id: "nc_config", Name: "config", MaxSize: 20000},
			&core.BoolField{Id: "nc_enabled", Name: "enabled"},
			&core.SelectField{Id: "nc_min_severity", Name: "minSeverity", MaxSelect: 1, Values: severityValues},
			&core.BoolField{Id: "nc_is_default", Name: "isDefault"},
			&core.JSONField{Id: "nc_template", Name: "template", MaxSize: 10000},
			&core.AutodateField{Id: "nc_created", Name: "created", OnCreate: true},
			&core.AutodateField{Id: "nc_updated", Name: "updated", OnCreate: true, OnUpdate: true},
		)
		channels.AddIndex("idx_nc_user", false, "user", "")
		if err := app.Save(channels); err != nil {
			return err
		}

		for _, name := range []string{"alerts", "network_monitors"} {
			collection, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			collection.Fields.Add(
				&core.SelectField{Id: name + "_severity", Name: "severity", MaxSelect: 1, Values: severityValues},
				&core.RelationField{Id: name + "_channels", Name: "channels", CollectionId: channels.Id, MaxSelect: 50},
			)
			if err := app.Save(collection); err != nil {
				return err
			}
		}
		history, err := app.FindCollectionByNameOrId("alerts_history")
		if err != nil {
			return err
		}
		history.Fields.Add(&core.SelectField{Id: "ah_severity", Name: "severity", MaxSelect: 1, Values: severityValues})
		if err := app.Save(history); err != nil {
			return err
		}
		return migrateLegacyNotificationSettings(app, channels)
	}, func(app core.App) error {
		for _, name := range []string{"alerts", "network_monitors"} {
			collection, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			collection.Fields.RemoveByName("severity")
			collection.Fields.RemoveByName("channels")
			if err := app.Save(collection); err != nil {
				return err
			}
		}
		history, err := app.FindCollectionByNameOrId("alerts_history")
		if err != nil {
			return err
		}
		history.Fields.RemoveByName("severity")
		if err := app.Save(history); err != nil {
			return err
		}
		if channels, err := app.FindCollectionByNameOrId(notificationChannelsCollectionId); err == nil {
			return app.Delete(channels)
		}
		return nil
	})
}

// migrateLegacyNotificationSettings creates default channels from the
// emails and webhooks of each user's settings.
func migrateLegacyNotificationSettings(app core.App, channels *core.Collection) error {
	var rows []struct {
		User     string `db:"user"`
		Settings string `db:"settings"`
	}
	if err := app.DB().Select("user", "settings").From("user_settings").All(&rows); err != nil {
		return err
	}
	// Seal URLs now when the hub key exists; otherwise the hub seals them on startup.
	box, _ := monitorsecrets.ForDataDirWithInfo(app.DataDir(), monitorsecrets.ChannelsInfo)
	for _, row := range rows {
		var settings struct {
			Emails   []string `json:"emails"`
			Webhooks []string `json:"webhooks"`
		}
		if row.User == "" || json.Unmarshal([]byte(row.Settings), &settings) != nil {
			continue
		}
		save := func(name, kind string, config map[string]any) error {
			record := core.NewRecord(channels)
			record.Load(map[string]any{
				"user": row.User, "name": name, "type": kind, "config": config,
				"enabled": true, "minSeverity": "info", "isDefault": true,
			})
			return app.SaveNoValidate(record)
		}
		var addresses []string
		for _, email := range settings.Emails {
			if email = strings.TrimSpace(email); email != "" {
				addresses = append(addresses, email)
			}
		}
		if len(addresses) > 0 {
			if err := save("Email", "email", map[string]any{"addresses": addresses}); err != nil {
				return err
			}
		}
		used := map[string]int{}
		for _, webhook := range settings.Webhooks {
			if webhook = strings.TrimSpace(webhook); webhook == "" {
				continue
			}
			name := legacyWebhookName(webhook)
			used[name]++
			if used[name] > 1 {
				name = fmt.Sprintf("%s %d", name, used[name])
			}
			value := webhook
			if box != nil {
				if sealed, err := box.Seal([]byte(webhook)); err == nil {
					value = sealed
				}
			}
			if err := save(name, "shoutrrr", map[string]any{"url": value}); err != nil {
				return err
			}
		}
	}
	return nil
}

// legacyWebhookServiceNames names common Shoutrrr services by URL scheme.
var legacyWebhookServiceNames = map[string]string{
	"bark": "Bark", "discord": "Discord", "generic": "Webhook", "gotify": "Gotify",
	"googlechat": "Google Chat", "ifttt": "IFTTT", "join": "Join", "lark": "Lark",
	"matrix": "Matrix", "mattermost": "Mattermost", "ntfy": "ntfy", "opsgenie": "Opsgenie",
	"pushbullet": "Pushbullet", "pushover": "Pushover", "rocketchat": "Rocket.Chat",
	"signal": "Signal", "slack": "Slack", "smtp": "SMTP", "teams": "Teams",
	"telegram": "Telegram", "wecom": "WeCom", "zulip": "Zulip",
}

// legacyWebhookName is the channel name of a migrated Shoutrrr URL.
func legacyWebhookName(raw string) string {
	scheme := raw
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
		scheme = parsed.Scheme
	} else if before, _, ok := strings.Cut(raw, "://"); ok {
		scheme = before
	}
	scheme = strings.ToLower(scheme)
	scheme, _, _ = strings.Cut(scheme, "+")
	if name, ok := legacyWebhookServiceNames[scheme]; ok {
		return name
	}
	return "Webhook"
}
