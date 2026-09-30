package alerts

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strings"

	"github.com/henrygd/beszel/internal/hub/monitorsecrets"
	"github.com/pocketbase/pocketbase/core"
)

const (
	maxChannelAddresses = 50
	maxChannelURLLen    = 4096
)

// bindChannelEvents validates channel, template and routing input, and
// records the severity of new alert history rows.
func (am *AlertManager) bindChannelEvents() {
	am.hub.OnRecordCreateRequest("notification_channels").BindFunc(validateChannelRequest)
	am.hub.OnRecordUpdateRequest("notification_channels").BindFunc(validateChannelRequest)
	am.hub.OnRecordCreateRequest("user_settings").BindFunc(validateNotificationSettings)
	am.hub.OnRecordUpdateRequest("user_settings").BindFunc(validateNotificationSettings)
	am.hub.OnRecordCreateRequest("alerts").BindFunc(validateAlertChannels)
	am.hub.OnRecordUpdateRequest("alerts").BindFunc(validateAlertChannels)
	am.hub.OnRecordCreateRequest("network_monitors").BindFunc(validateMonitorChannels)
	am.hub.OnRecordUpdateRequest("network_monitors").BindFunc(validateMonitorChannels)
	am.hub.OnRecordCreate("alerts_history").BindFunc(am.setHistorySeverity)
}

// validateChannelRequest validates and normalizes a submitted channel.
func validateChannelRequest(e *core.RecordRequestEvent) error {
	if err := prepareChannel(e.Record); err != nil {
		return e.BadRequestError(err.Error(), nil)
	}
	return e.Next()
}

// prepareChannel validates a channel record and normalizes its config,
// minimum severity and template.
func prepareChannel(record *core.Record) error {
	record.Set("name", strings.TrimSpace(record.GetString("name")))
	if record.GetString("name") == "" {
		return errors.New("Name is required.")
	}
	if _, ok := ParseSeverity(record.GetString("minSeverity")); !ok {
		record.Set("minSeverity", string(SeverityInfo))
	}
	var config channelConfig
	if raw := record.GetString("config"); raw != "" && raw != "null" {
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			return errors.New("Invalid channel configuration.")
		}
	}
	switch record.GetString("type") {
	case ChannelEmail:
		var addresses []string
		for _, address := range config.Addresses {
			address = strings.TrimSpace(address)
			if address == "" || slices.Contains(addresses, address) {
				continue
			}
			parsed, err := mail.ParseAddress(address)
			if err != nil || parsed.Address != address {
				return fmt.Errorf("Invalid email address: %s", address)
			}
			addresses = append(addresses, address)
		}
		if len(addresses) == 0 {
			return errors.New("Enter at least one email address.")
		}
		if len(addresses) > maxChannelAddresses {
			return fmt.Errorf("A channel can have at most %d email addresses.", maxChannelAddresses)
		}
		record.Set("config", map[string]any{"addresses": addresses})
	case ChannelShoutrrr:
		value := strings.TrimSpace(config.URL)
		if strings.HasPrefix(value, monitorsecrets.Prefix) {
			// Sealed values are only accepted unchanged (clients submit plaintext).
			original := record.Original()
			var stored channelConfig
			if original != nil && !record.IsNew() {
				_ = original.UnmarshalJSONField("config", &stored)
			}
			if record.IsNew() || stored.URL != value {
				return errors.New("Invalid notification URL.")
			}
		} else {
			if value == "" || len(value) > maxChannelURLLen {
				return errors.New("Enter a valid notification URL.")
			}
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme == "" {
				return errors.New("Enter a valid notification URL.")
			}
		}
		record.Set("config", map[string]any{"url": value})
	case ChannelBrowser:
		record.Set("config", map[string]any{})
	default:
		return errors.New("Invalid channel type.")
	}
	template, err := decodeTemplateJSON(record.GetString("template"))
	if err != nil {
		return err
	}
	if template.IsZero() {
		record.Set("template", nil)
		return nil
	}
	if err := ValidateTemplate(template); err != nil {
		return err
	}
	record.Set("template", template)
	return nil
}

// validateNotificationSettings rejects user settings with invalid templates.
func validateNotificationSettings(e *core.RecordRequestEvent) error {
	var settings struct {
		Templates      json.RawMessage `json:"templates"`
		CriticalBypass json.RawMessage `json:"criticalBypassQuietHours"`
	}
	if err := e.Record.UnmarshalJSONField("settings", &settings); err != nil {
		return e.Next()
	}
	if len(settings.CriticalBypass) > 0 && string(settings.CriticalBypass) != "null" {
		var bypass bool
		if json.Unmarshal(settings.CriticalBypass, &bypass) != nil {
			return e.BadRequestError("criticalBypassQuietHours must be a boolean.", nil)
		}
	}
	template, err := decodeTemplateJSON(string(settings.Templates))
	if err != nil {
		return e.BadRequestError(err.Error(), nil)
	}
	if err := ValidateTemplate(template); err != nil {
		return e.BadRequestError(err.Error(), nil)
	}
	return e.Next()
}

// validateAlertChannels requires an alert's channels to be its user's.
func validateAlertChannels(e *core.RecordRequestEvent) error {
	ids := e.Record.GetStringSlice("channels")
	if len(ids) > 0 {
		owned, err := channelIDsOwnedBy(e.App, e.Record.GetString("user"), ids)
		if err != nil {
			return err
		}
		if len(owned) != len(ids) {
			return e.BadRequestError("Invalid notification channels.", nil)
		}
	}
	return e.Next()
}

// validateMonitorChannels requires channels added to a monitor to be the
// requester's. Monitors are shared, so channels of other users are kept.
func validateMonitorChannels(e *core.RecordRequestEvent) error {
	ids := e.Record.GetStringSlice("channels")
	if len(ids) > 0 && !e.HasSuperuserAuth() {
		var existing []string
		if !e.Record.IsNew() {
			existing = e.Record.Original().GetStringSlice("channels")
		}
		var added []string
		for _, id := range ids {
			if !slices.Contains(existing, id) {
				added = append(added, id)
			}
		}
		if len(added) > 0 {
			if e.Auth == nil {
				return e.BadRequestError("Invalid notification channels.", nil)
			}
			owned, err := channelIDsOwnedBy(e.App, e.Auth.Id, added)
			if err != nil {
				return err
			}
			if len(owned) != len(added) {
				return e.BadRequestError("Invalid notification channels.", nil)
			}
		}
	}
	return e.Next()
}

// PreviewTemplate renders a template with sample data
// (POST /api/beszel/notification-templates/preview).
func PreviewTemplate(e *core.RequestEvent) error {
	var body NotificationTemplate
	if err := e.BindBody(&body); err != nil {
		return e.BadRequestError("Invalid template.", err)
	}
	title, message, err := RenderTemplate(body, sampleTemplateData())
	if err != nil {
		return e.BadRequestError(err.Error(), nil)
	}
	if strings.TrimSpace(body.Body) == "" {
		data := sampleTemplateData()
		message = data.Message + "\n\nAcknowledge: " + data.AckLink
	}
	return e.JSON(http.StatusOK, map[string]string{"title": title, "body": message})
}

// TestChannel sends a test notification to one of the requester's channels
// (POST /api/beszel/notification-channels/{id}/test).
func (am *AlertManager) TestChannel(e *core.RequestEvent) error {
	record, err := e.App.FindRecordById("notification_channels", e.Request.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (e.Auth == nil || record.GetString("user") != e.Auth.Id)) {
		return e.NotFoundError("", nil)
	}
	if err != nil {
		return err
	}
	isAdmin := e.Auth.GetString("role") == "admin"
	err = am.sendTestToChannel(e.Auth.Id, record, isAdmin)
	if errors.Is(err, errInternalDestination) || errors.Is(err, errUnrestrictedService) {
		return e.ForbiddenError(err.Error(), nil)
	}
	if err != nil {
		return e.JSON(http.StatusOK, map[string]string{"err": err.Error()})
	}
	return e.JSON(http.StatusOK, map[string]bool{"err": false})
}
