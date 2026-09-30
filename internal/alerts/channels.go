package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/henrygd/beszel/internal/hub/monitorsecrets"
	"github.com/nicholas-fedor/shoutrrr"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/mailer"
)

// Notification channels
//
// A user's notification_channels records are the destinations of their
// alerts: email addresses, a Shoutrrr URL, or their browsers (Web Push,
// through the sender set with SetBrowserSender). Every alert has a severity.
// An alert with explicit channels (alerts.channels, network_monitors.channels)
// goes to those of the recipient's channels that are enabled; otherwise, or
// when none of them belongs to the recipient, it goes to the recipient's
// enabled default channels whose minSeverity is at most the alert's
// severity. Recoveries and reminders have the severity of the alert they
// follow, so they reach the same channels.
//
// Users without any channel (e.g. created after the migration and not yet
// configured) keep the legacy delivery to user_settings emails and webhooks.

// Severity ranks alerts for routing.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Channel types.
const (
	ChannelEmail    = "email"
	ChannelShoutrrr = "shoutrrr"
	ChannelBrowser  = "browser"
)

// rank orders severities; unknown values rank as warning.
func (s Severity) rank() int {
	switch s {
	case SeverityInfo:
		return 0
	case SeverityCritical:
		return 2
	}
	return 1
}

// ParseSeverity returns s as a Severity, and whether it is a valid one.
func ParseSeverity(s string) (Severity, bool) {
	switch Severity(s) {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return Severity(s), true
	}
	return "", false
}

// orDefault returns s when valid, else def.
func (s Severity) orDefault(def Severity) Severity {
	if parsed, ok := ParseSeverity(string(s)); ok {
		return parsed
	}
	return def
}

// defaultSeverity is the severity of an alert type (alerts / alerts_history
// name) without an override. value is the history value (days left for
// certificate alerts).
func defaultSeverity(name string, value float64) Severity {
	switch name {
	case "Status", alertNameMonitorDown, containerAlertName, alertNameSystemdFailed:
		return SeverityCritical
	case alertNameMonitorCert:
		return certSeverity(int(value))
	}
	return SeverityWarning
}

// certSeverity is critical for certificates expiring within three days.
func certSeverity(daysLeft int) Severity {
	if daysLeft <= 3 {
		return SeverityCritical
	}
	return SeverityWarning
}

// AlertPushMessage is a browser (Web Push) notification.
type AlertPushMessage struct {
	Title, Body, URL, Tag string
	Urgent                bool
}

// BrowserSender delivers a push notification to a user's subscribed browsers.
type BrowserSender func(ctx context.Context, userID string, msg AlertPushMessage) error

// SetBrowserSender sets the sender of browser channels; nil disables them.
func (am *AlertManager) SetBrowserSender(sender BrowserSender) {
	am.browserSender = sender
}

// channel is a resolved notification destination.
type channel struct {
	ID          string
	Name        string
	Type        string
	Enabled     bool
	IsDefault   bool
	MinSeverity Severity
	Addresses   []string
	URL         string
	Template    NotificationTemplate
}

// channelConfig is notification_channels.config.
type channelConfig struct {
	Addresses []string `json:"addresses,omitempty"`
	URL       string   `json:"url,omitempty"`
}

// channelSecretsBox opens sealed channel URLs.
func channelSecretsBox(app core.App) (*monitorsecrets.Box, error) {
	return monitorsecrets.ForDataDirWithInfo(app.DataDir(), monitorsecrets.ChannelsInfo)
}

// ChannelURL returns the plaintext Shoutrrr URL of a stored config value,
// opening it when sealed.
func ChannelURL(app core.App, value string) (string, error) {
	if !strings.HasPrefix(value, monitorsecrets.Prefix) {
		return value, nil
	}
	box, err := channelSecretsBox(app)
	if err != nil {
		return "", err
	}
	plaintext, err := box.Open(value)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// channelFromRecord decodes a notification_channels record.
func channelFromRecord(app core.App, record *core.Record) (channel, error) {
	c := channel{
		ID:          record.Id,
		Name:        record.GetString("name"),
		Type:        record.GetString("type"),
		Enabled:     record.GetBool("enabled"),
		IsDefault:   record.GetBool("isDefault"),
		MinSeverity: Severity(record.GetString("minSeverity")).orDefault(SeverityInfo),
	}
	var config channelConfig
	_ = record.UnmarshalJSONField("config", &config)
	_ = record.UnmarshalJSONField("template", &c.Template)
	c.Addresses = config.Addresses
	if c.Type == ChannelShoutrrr {
		url, err := ChannelURL(app, config.URL)
		if err != nil {
			return c, fmt.Errorf("open channel URL: %w", err)
		}
		c.URL = url
	}
	return c, nil
}

// userNotificationPrefs are the notification fields of user_settings.settings.
type userNotificationPrefs struct {
	Emails                   []string             `json:"emails"`
	Webhooks                 []string             `json:"webhooks"`
	Templates                NotificationTemplate `json:"templates"`
	CriticalBypassQuietHours bool                 `json:"criticalBypassQuietHours"`
}

// loadUserPrefs reads a user's notification settings. A user without
// settings has none.
func (am *AlertManager) loadUserPrefs(userID string) (userNotificationPrefs, bool, error) {
	var prefs userNotificationPrefs
	record, err := am.hub.FindFirstRecordByFilter("user_settings", "user={:user}", dbx.Params{"user": userID})
	if err != nil {
		return prefs, false, err
	}
	if err := record.UnmarshalJSONField("settings", &prefs); err != nil {
		am.hub.Logger().Error("Failed to unmarshal user settings", "err", err)
	}
	return prefs, true, nil
}

// userChannels returns a user's channels. Without any channel record, the
// legacy settings emails and webhooks are returned as default channels.
func (am *AlertManager) userChannels(userID string, prefs userNotificationPrefs) ([]channel, error) {
	records, err := am.hub.FindAllRecords("notification_channels", dbx.HashExp{"user": userID})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return legacyChannels(prefs), nil
	}
	channels := make([]channel, 0, len(records))
	for _, record := range records {
		c, err := channelFromRecord(am.hub, record)
		if err != nil {
			am.hub.Logger().Error("Failed to load notification channel", "channel", record.Id, "err", err)
			continue
		}
		channels = append(channels, c)
	}
	return channels, nil
}

// legacyChannels are the destinations of user_settings emails and webhooks.
func legacyChannels(prefs userNotificationPrefs) []channel {
	var channels []channel
	for i, webhook := range prefs.Webhooks {
		channels = append(channels, channel{
			ID: fmt.Sprintf("legacy-webhook-%d", i), Name: "Webhook", Type: ChannelShoutrrr,
			Enabled: true, IsDefault: true, MinSeverity: SeverityInfo, URL: webhook,
		})
	}
	if len(prefs.Emails) > 0 {
		channels = append(channels, channel{
			ID: "legacy-email", Name: "Email", Type: ChannelEmail,
			Enabled: true, IsDefault: true, MinSeverity: SeverityInfo, Addresses: prefs.Emails,
		})
	}
	return channels
}

// routeChannels selects the channels an alert goes to: the enabled explicit
// channels when any of them belongs to the user, else the enabled default
// channels whose threshold the severity meets.
func routeChannels(channels []channel, explicit []string, severity Severity) []channel {
	var selected []channel
	if len(explicit) > 0 {
		matched := false
		for _, c := range channels {
			if slices.Contains(explicit, c.ID) {
				matched = true
				if c.Enabled {
					selected = append(selected, c)
				}
			}
		}
		if matched {
			return selected
		}
	}
	for _, c := range channels {
		if c.Enabled && c.IsDefault && c.MinSeverity.rank() <= severity.rank() {
			selected = append(selected, c)
		}
	}
	return selected
}

// deliverAlert sends an alert to the user's channels without checking quiet
// hours.
func (am *AlertManager) deliverAlert(data AlertMessageData) error {
	data.Severity = data.Severity.orDefault(SeverityWarning)
	now := time.Now()
	data.AckLink = am.ackLink(data.HistoryID, data.UserID, now)
	prefs, _, err := am.loadUserPrefs(data.UserID)
	if err != nil {
		return err
	}
	channels, err := am.userChannels(data.UserID, prefs)
	if err != nil {
		return err
	}
	targets := routeChannels(channels, data.Channels, data.Severity)
	return am.sendToChannels(data, targets, prefs.Templates, now)
}

// sendToChannels renders and delivers data to each channel. Shoutrrr and
// browser failures are logged; an email failure is returned.
func (am *AlertManager) sendToChannels(data AlertMessageData, targets []channel, userTemplate NotificationTemplate, now time.Time) error {
	send := sendPublicNotification
	if slices.ContainsFunc(targets, func(c channel) bool { return c.Type == ChannelShoutrrr }) {
		// Read the owner's current role at delivery time, including for URLs
		// saved before an admin was demoted. Never fall back on lookup failure.
		owner, err := am.hub.FindRecordById("users", data.UserID)
		if err != nil {
			return fmt.Errorf("load notification owner: %w", err)
		}
		if owner.GetString("role") == "admin" {
			send = shoutrrr.Send
		}
	}
	var emailErr error
	for _, c := range targets {
		title, body, custom := am.renderForChannel(data, c, userTemplate, now)
		switch c.Type {
		case ChannelShoutrrr:
			link := data.Link
			if custom {
				link = ""
			}
			if err := am.sendShoutrrrAlert(c.URL, title, body, link, data.LinkText, send); err != nil {
				am.hub.Logger().Error("Failed to send shoutrrr alert", "channel", c.Name, "err", err)
			}
		case ChannelEmail:
			if err := am.sendEmailAlert(c.Addresses, title, body, data.Link, custom); err != nil {
				am.hub.Logger().Error("Failed to send email alert", "channel", c.Name, "err", err)
				emailErr = err
			}
		case ChannelBrowser:
			if err := am.sendBrowserAlert(data, title, body); errors.Is(err, ErrNoBrowserDevices) {
				am.hub.Logger().Debug("No browser devices for alert", "channel", c.Name, "user", data.UserID)
			} else if err != nil {
				am.hub.Logger().Error("Failed to send browser alert", "channel", c.Name, "err", err)
			}
		}
	}
	return emailErr
}

// renderForChannel renders the title and body of data for c: the channel's
// template, else the user's, else the built-in text. custom reports whether
// a body template was used (its output is sent as is, without the link and
// acknowledgement lines the built-in body gets).
func (am *AlertManager) renderForChannel(data AlertMessageData, c channel, userTemplate NotificationTemplate, now time.Time) (title, body string, custom bool) {
	builtinBody := data.Message
	if data.AckLink != "" {
		builtinBody += "\n\nAcknowledge: " + data.AckLink
	}
	tmpl := userTemplate
	if !c.Template.IsZero() {
		tmpl = c.Template
	}
	if tmpl.IsZero() {
		return data.Title, builtinBody, false
	}
	vars := data.templateData(now)
	vars.Message = data.Message
	title, body, err := RenderTemplate(tmpl, vars)
	if err != nil {
		am.logTemplateErrorOnce(tmpl, err)
		return data.Title, builtinBody, false
	}
	if strings.TrimSpace(tmpl.Body) == "" {
		return title, builtinBody, false
	}
	return title, body, true
}

// templateData returns the template variables of an alert.
func (data AlertMessageData) templateData(now time.Time) TemplateData {
	status := data.Status
	if status == "" {
		status = "triggered"
	}
	return TemplateData{
		Title:    data.Title,
		Message:  data.Message,
		Severity: string(data.Severity),
		Status:   status,
		Name:     data.Name,
		Value:    data.Value,
		Link:     data.Link,
		AckLink:  data.AckLink,
		Time:     formatAlertTime(now),
	}
}

// sendEmailAlert emails an alert to addresses.
func (am *AlertManager) sendEmailAlert(emails []string, title, body, link string, custom bool) error {
	addresses := make([]mail.Address, 0, len(emails))
	for _, email := range emails {
		if email = strings.TrimSpace(email); email != "" {
			addresses = append(addresses, mail.Address{Address: email})
		}
	}
	if len(addresses) == 0 {
		return nil
	}
	text := body
	if !custom && link != "" {
		text += "\n\n" + link
	}
	message := mailer.Message{
		To:      addresses,
		Subject: title,
		Text:    text,
		From: mail.Address{
			Address: am.hub.Settings().Meta.SenderAddress,
			Name:    am.hub.Settings().Meta.SenderName,
		},
	}
	if err := am.hub.NewMailClient().Send(&message); err != nil {
		return err
	}
	am.hub.Logger().Info("Sent email alert", "to", message.To, "subj", message.Subject)
	return nil
}

var errBrowserUnavailable = errors.New("browser notifications are not available")

// ErrNoBrowserDevices is returned by a BrowserSender when the user has no
// subscribed devices. Alerts skip it quietly; a channel test reports it.
var ErrNoBrowserDevices = errors.New("no devices are subscribed to browser notifications; enable them on a device first")

// sendBrowserAlert pushes an alert to the user's browsers.
func (am *AlertManager) sendBrowserAlert(data AlertMessageData, title, body string) error {
	if am.browserSender == nil {
		return errBrowserUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Repeats of one alert replace each other on the device.
	target := data.MonitorID
	if target == "" {
		target = data.SystemID
	}
	tag := strings.Trim(data.AlertType+":"+target, ":")
	return am.browserSender(ctx, data.UserID, AlertPushMessage{
		Title:  title,
		Body:   body,
		URL:    data.Link,
		Tag:    tag,
		Urgent: data.Severity == SeverityCritical,
	})
}

// sendTestToChannel sends a test notification to one channel.
func (am *AlertManager) sendTestToChannel(userID string, record *core.Record, isAdmin bool) error {
	c, err := channelFromRecord(am.hub, record)
	if err != nil {
		return err
	}
	prefs, _, _ := am.loadUserPrefs(userID)
	data := AlertMessageData{
		UserID:   userID,
		Title:    "Test Alert",
		Message:  "This is a test notification from InfraScope.",
		Link:     am.hub.Settings().Meta.AppURL,
		LinkText: "View InfraScope",
		Severity: SeverityInfo,
		Name:     c.Name,
		Status:   "test",
	}
	now := time.Now()
	title, body, custom := am.renderForChannel(data, c, prefs.Templates, now)
	switch c.Type {
	case ChannelShoutrrr:
		send := sendPublicNotification
		if isAdmin {
			send = shoutrrr.Send
		}
		link := data.Link
		if custom {
			link = ""
		}
		return am.sendShoutrrrAlert(c.URL, title, body, link, data.LinkText, send)
	case ChannelEmail:
		if len(c.Addresses) == 0 {
			return errors.New("the channel has no email addresses")
		}
		return am.sendEmailAlert(c.Addresses, title, body, data.Link, custom)
	case ChannelBrowser:
		return am.sendBrowserAlert(data, title, body)
	}
	return fmt.Errorf("unknown channel type %q", c.Type)
}

// channelIDsOwnedBy returns the ids among ids that are channels of user.
func channelIDsOwnedBy(app core.App, user string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	values := make([]any, len(ids))
	for i, id := range ids {
		values[i] = id
	}
	var owned []string
	err := app.DB().Select("id").From("notification_channels").
		Where(dbx.And(dbx.HashExp{"user": user}, dbx.In("id", values...))).Column(&owned)
	return owned, err
}

// decodeTemplateJSON decodes a template JSON value; empty values are zero.
func decodeTemplateJSON(raw string) (NotificationTemplate, error) {
	var t NotificationTemplate
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return t, nil
	}
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return t, errors.New("the template must be an object with title and body")
	}
	return t, nil
}

// alertStatusLabel is the template status of a triggered or resolved alert.
func alertStatusLabel(triggered bool) string {
	if triggered {
		return "triggered"
	}
	return "resolved"
}

// smartAlertSeverity is critical for failed disks.
func smartAlertSeverity(state string) Severity {
	if state == "FAILED" {
		return SeverityCritical
	}
	return SeverityWarning
}

// zfsAlertSeverity is critical for failed or unavailable pools.
func zfsAlertSeverity(poolSeverity int) Severity {
	if poolSeverity >= 3 {
		return SeverityCritical
	}
	return SeverityWarning
}
