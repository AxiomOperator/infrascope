package alerts

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/hub/monitorloc"
	"github.com/henrygd/beszel/internal/hub/uptime"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// Uptime monitor alerts
//
// Monitor alerts are not configured through `alerts` records. Their history
// rows (alerts_history) have alert_id set to the monitor ID, monitor set to
// the monitor, system set for agent monitors and empty for hub monitors, and
// are created per recipient:
//
//   - MonitorDown: opened when the uptime engine reports a monitor down
//     (the monitor's notify option), resolved when it reports it up again.
//   - MonitorCert: opened when a monitored certificate expires within the
//     monitor's certExpiryDays, with value = whole days left (negative once
//     expired). Resolved when a certificate outside the window replaces it,
//     or certExpiryDays is cleared. Certificate alerts are opted into with
//     certExpiryDays > 0 alone, independently of notify.
//   - MonitorLoss and MonitorLatency: the monitor's own one-hour loss and
//     latency thresholds (see alerts_monitor_thresholds.go).
//
// Recipients are the users of the systems a monitor runs on and, when the
// hub is one of its locations (hub and push monitors), the monitor's users. Delivery uses SendAlert, so
// the user's destinations and quiet hours (global, plus the system's for agent
// monitors) apply. Notifications are sent after the history is committed.
const (
	alertNameMonitorDown = "MonitorDown"
	alertNameMonitorCert = "MonitorCert"
)

// isMonitorAlertName reports whether name is a monitor alert, whose history
// rows are not tied to an `alerts` record.
func isMonitorAlertName(name string) bool {
	switch name {
	case alertNameMonitorDown, alertNameMonitorCert, alertNameMonitorLoss, alertNameMonitorLatency:
		return true
	}
	return false
}

// monitorCertState is stored in the hidden network_monitors.certState field.
type monitorCertState struct {
	// Notified is the certificate expiry (Unix milliseconds) last notified.
	Notified int64 `json:"notified,omitempty"`
}

// monitorTarget holds what monitor alerts need to know about a monitor.
type monitorTarget struct {
	id, name, systemID, systemName string
	users                          []string
	// severity overrides the default severity of the monitor's alerts;
	// channels are its explicit channels.
	severity Severity
	channels []string
}

// message returns the alert message of the target to user.
func (t monitorTarget) message(user, alertType string, severity Severity, triggered bool) AlertMessageData {
	return AlertMessageData{
		UserID: user, SystemID: t.systemID, MonitorID: t.id,
		Severity: t.severity.orDefault(severity), Channels: t.channels,
		Name: t.name, Status: alertStatusLabel(triggered), AlertType: alertType,
	}
}

// loadMonitorTarget reads a monitor's display name and recipients. The
// system of a target is its primary location; the label names it only for
// monitors with a single location.
func loadMonitorTarget(app core.App, record *core.Record) (monitorTarget, error) {
	target := monitorTarget{
		id: record.Id, name: record.GetString("name"), systemID: record.GetString("system"),
		severity: Severity(record.GetString("severity")), channels: record.GetStringSlice("channels"),
	}
	if target.name == "" {
		target.name = record.GetString("target")
	}
	locations := monitorloc.Of(record)
	addUsers := func(users []string) {
		for _, user := range users {
			if !slices.Contains(target.users, user) {
				target.users = append(target.users, user)
			}
		}
	}
	for _, location := range locations {
		if location == monitorloc.Hub {
			addUsers(record.GetStringSlice("users"))
			continue
		}
		system, err := app.FindRecordById("systems", location)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return target, err
		}
		if len(locations) == 1 {
			target.systemName = system.GetString("name")
		}
		addUsers(system.GetStringSlice("users"))
	}
	return target, nil
}

// label is the monitor name, with its system for agent monitors.
func (t monitorTarget) label() string {
	if t.systemName == "" {
		return t.name
	}
	return fmt.Sprintf("%s on %s", t.name, t.systemName)
}

// HandleMonitorTransitions notifies confirmed monitor status changes from the
// uptime engine and records them in alerts_history. Errors are logged.
func (am *AlertManager) HandleMonitorTransitions(transitions []uptime.Transition) {
	for _, transition := range transitions {
		if err := am.handleMonitorTransition(transition); err != nil {
			am.hub.Logger().Error("Failed to handle monitor status change", "monitor", transition.MonitorID, "status", transition.Status, "err", err)
		}
	}
}

func (am *AlertManager) handleMonitorTransition(transition uptime.Transition) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	var messages []AlertMessageData
	err = am.hub.RunInTransaction(func(tx core.App) error {
		messages = nil
		now := time.Now().UTC()
		if transition.Status == uptime.StatusUp {
			if err := resolveMonitorHistory(tx, transition.MonitorID, alertNameMonitorDown, now); err != nil {
				return err
			}
		}
		record, err := tx.FindRecordById("network_monitors", transition.MonitorID)
		if errors.Is(err, sql.ErrNoRows) {
			// Deleted monitors are not notified.
			return nil
		}
		if err != nil {
			return err
		}
		target, err := loadMonitorTarget(tx, record)
		if err != nil {
			return err
		}
		if transition.Name != "" {
			target.name = transition.Name
		}
		switch transition.Status {
		case uptime.StatusDown:
			open, err := openMonitorHistoryUsers(tx, transition.MonitorID, alertNameMonitorDown)
			if err != nil {
				return err
			}
			for _, user := range target.users {
				// An open incident was already notified to this user.
				if open[user] {
					continue
				}
				historyID, err := createMonitorHistory(tx, user, target, alertNameMonitorDown, 0)
				if err != nil {
					return err
				}
				message := am.monitorDownMessage(user, target, transition)
				message.HistoryID = historyID
				messages = append(messages, message)
			}
		case uptime.StatusUp:
			for _, user := range target.users {
				messages = append(messages, am.monitorUpMessage(user, target, transition))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	am.sendMonitorMessages(messages)
	return nil
}

func (am *AlertManager) monitorDownMessage(user string, target monitorTarget, transition uptime.Transition) AlertMessageData {
	since := transition.DownSince
	if since.IsZero() {
		since = transition.At
	}
	var body strings.Builder
	fmt.Fprintf(&body, "%s is down", target.label())
	if transition.Target != "" && transition.Target != target.name {
		fmt.Fprintf(&body, " (%s)", transition.Target)
	}
	fmt.Fprintf(&body, " since %s.", formatAlertTime(since))
	if transition.Err != "" {
		fmt.Fprintf(&body, "\nError: %s", transition.Err)
	}
	if transition.StatusCode != 0 {
		fmt.Fprintf(&body, "\nStatus code: %d", transition.StatusCode)
	}
	message := target.message(user, alertNameMonitorDown, SeverityCritical, true)
	message.Title = fmt.Sprintf("%s is down", target.name)
	message.Message = body.String()
	message.Link, message.LinkText = am.hub.MakeLink("monitors"), "View monitors"
	message.Value = "down"
	return message
}

func (am *AlertManager) monitorUpMessage(user string, target monitorTarget, transition uptime.Transition) AlertMessageData {
	message := fmt.Sprintf("%s is up.", target.label())
	if !transition.DownSince.IsZero() && transition.At.After(transition.DownSince) {
		message = fmt.Sprintf("%s is back up after %s (down since %s).", target.label(),
			formatOutage(transition.At.Sub(transition.DownSince)), formatAlertTime(transition.DownSince))
	}
	data := target.message(user, alertNameMonitorDown, SeverityCritical, false)
	data.Title = fmt.Sprintf("%s is up", target.name)
	data.Message = message
	data.Link, data.LinkText = am.hub.MakeLink("monitors"), "View monitors"
	data.Value = "up"
	return data
}

func (am *AlertManager) sendMonitorMessages(messages []AlertMessageData) {
	for _, message := range messages {
		if err := am.SendAlert(message); err != nil {
			am.hub.Logger().Error("Failed to send monitor alert", "user", message.UserID, "title", message.Title, "err", err)
		}
	}
}

// formatOutage formats an outage duration, e.g. "1h 5m" or "42s".
func formatOutage(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return d.String()
	}
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	return strings.Join(parts, " ")
}

func formatAlertTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

// openMonitorHistoryUsers returns the users with an open history row of the given monitor alert.
func openMonitorHistoryUsers(app core.App, monitorID, name string) (map[string]bool, error) {
	records, err := app.FindAllRecords("alerts_history", dbx.HashExp{"alert_id": monitorID, "name": name, "resolved": ""})
	if err != nil {
		return nil, err
	}
	users := make(map[string]bool, len(records))
	for _, record := range records {
		users[record.GetString("user")] = true
	}
	return users, nil
}

// createMonitorHistory opens a monitor alert for a user and returns the history row id.
func createMonitorHistory(app core.App, user string, target monitorTarget, name string, value float64) (string, error) {
	collection, err := app.FindCachedCollectionByNameOrId("alerts_history")
	if err != nil {
		return "", err
	}
	history := core.NewRecord(collection)
	history.Load(map[string]any{
		"alert_id": target.id, "user": user, "system": target.systemID, "monitor": target.id,
		"name": name, "monitor_name": target.name, "value": value,
	})
	if err := app.Save(history); err != nil {
		return "", err
	}
	return history.Id, nil
}

// resolveMonitorHistory resolves the open history rows of a monitor alert.
func resolveMonitorHistory(app core.App, monitorID, name string, now time.Time) error {
	records, err := app.FindAllRecords("alerts_history", dbx.HashExp{"alert_id": monitorID, "name": name, "resolved": ""})
	if err != nil {
		return err
	}
	for _, record := range records {
		record.Set("resolved", now.UTC())
		if err := app.Save(record); err != nil {
			return err
		}
	}
	return nil
}

// resolveDeletedMonitorHistory resolves the open monitor alerts of a deleted monitor.
func resolveDeletedMonitorHistory(e *core.RecordEvent) error {
	if err := e.Next(); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, name := range []string{alertNameMonitorDown, alertNameMonitorCert, alertNameMonitorLoss, alertNameMonitorLatency} {
		if err := resolveMonitorHistory(e.App, e.Record.Id, name, now); err != nil {
			e.App.Logger().Error("Failed to resolve monitor alerts", "monitor", e.Record.Id, "err", err)
		}
	}
	return nil
}

// CheckMonitorCerts notifies monitored certificates that expire within their
// monitor's certExpiryDays, once per certificate, and resolves the alerts of
// replaced certificates. It runs hourly.
func (am *AlertManager) CheckMonitorCerts() {
	if err := am.checkMonitorCerts(time.Now()); err != nil {
		am.hub.Logger().Error("Failed to check monitor certificates", "err", err)
	}
}

func (am *AlertManager) checkMonitorCerts(now time.Time) error {
	// Monitors with certificate alerts enabled, or with alerts left open.
	open, err := am.hub.FindAllRecords("alerts_history", dbx.HashExp{"name": alertNameMonitorCert, "resolved": ""})
	if err != nil {
		return err
	}
	ids := make([]any, 0, len(open))
	for _, record := range open {
		ids = append(ids, record.GetString("alert_id"))
	}
	condition := dbx.NewExp("certExpiryDays > 0")
	if len(ids) > 0 {
		condition = dbx.Or(condition, dbx.In("id", ids...))
	}
	var monitorIDs []string
	if err := am.hub.DB().Select("id").From("network_monitors").Where(condition).Column(&monitorIDs); err != nil {
		return err
	}
	for _, id := range monitorIDs {
		if err := am.checkMonitorCert(id, now); err != nil {
			am.hub.Logger().Error("Failed to check monitor certificate", "monitor", id, "err", err)
		}
	}
	return nil
}

// certDaysLeft returns the whole days until expires, negative once expired.
func certDaysLeft(expires, now time.Time) int {
	return int(math.Floor(expires.Sub(now).Hours() / 24))
}

func (am *AlertManager) checkMonitorCert(monitorID string, now time.Time) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	var messages []AlertMessageData
	err = am.hub.RunInTransaction(func(tx core.App) error {
		messages = nil
		record, err := tx.FindRecordById("network_monitors", monitorID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var cert monitor.CertInfo
		_ = record.UnmarshalJSONField("certInfo", &cert)
		var state monitorCertState
		_ = record.UnmarshalJSONField("certState", &state)
		days := record.GetInt("certExpiryDays")
		expires := time.UnixMilli(cert.Expires)
		within := record.GetBool("enabled") && days > 0 && cert.Expires > 0 &&
			expires.Sub(now) < time.Duration(days)*24*time.Hour
		if !within {
			// A renewed certificate, or certificate alerts turned off.
			return resolveMonitorHistory(tx, monitorID, alertNameMonitorCert, now)
		}
		if state.Notified == cert.Expires {
			return nil
		}
		// Alerts of a previous certificate are superseded by this one.
		if err := resolveMonitorHistory(tx, monitorID, alertNameMonitorCert, now); err != nil {
			return err
		}
		target, err := loadMonitorTarget(tx, record)
		if err != nil {
			return err
		}
		daysLeft := certDaysLeft(expires, now)
		for _, user := range target.users {
			historyID, err := createMonitorHistory(tx, user, target, alertNameMonitorCert, float64(daysLeft))
			if err != nil {
				return err
			}
			message := am.monitorCertMessage(user, target, cert, daysLeft, now)
			message.HistoryID = historyID
			messages = append(messages, message)
		}
		state.Notified = cert.Expires
		encoded, err := json.Marshal(state)
		if err != nil {
			return err
		}
		// Update only certState, without hooks, so concurrent status writes are kept.
		_, err = tx.DB().Update("network_monitors", dbx.Params{"certState": string(encoded)}, dbx.HashExp{"id": monitorID}).Execute()
		return err
	})
	if err != nil {
		return err
	}
	am.sendMonitorMessages(messages)
	return nil
}

func (am *AlertManager) monitorCertMessage(user string, target monitorTarget, cert monitor.CertInfo, daysLeft int, now time.Time) AlertMessageData {
	expires := time.UnixMilli(cert.Expires).UTC()
	expired := !expires.After(now)
	var title string
	switch {
	case expired:
		title = fmt.Sprintf("Certificate for %s has expired", target.name)
	case daysLeft < 1:
		title = fmt.Sprintf("Certificate for %s expires in less than a day", target.name)
	case daysLeft == 1:
		title = fmt.Sprintf("Certificate for %s expires in 1 day", target.name)
	default:
		title = fmt.Sprintf("Certificate for %s expires in %d days", target.name, daysLeft)
	}
	message := fmt.Sprintf("The certificate of %s expires on %s.", target.label(), formatAlertTime(expires))
	if expired {
		message = fmt.Sprintf("The certificate of %s expired on %s.", target.label(), formatAlertTime(expires))
	}
	if cert.Issuer != "" {
		message += fmt.Sprintf("\nIssuer: %s", cert.Issuer)
	}
	data := target.message(user, alertNameMonitorCert, certSeverity(daysLeft), true)
	data.Title, data.Message = title, message
	data.Link, data.LinkText = am.hub.MakeLink("monitors"), "View monitors"
	data.Value = strconv.Itoa(daysLeft)
	return data
}
