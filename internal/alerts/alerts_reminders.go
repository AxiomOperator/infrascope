package alerts

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// Reminders for unacknowledged alerts
//
// Users opt in with reminderMinutes (reminderMinMinutes to reminderMaxMinutes,
// 0 or missing for off) in user_settings.settings. Every minute the hub
// resends open, unacknowledged alert history rows of those users whose last
// notification (the row's creation, or its last reminder) is at least that
// many minutes old, as "Reminder: <title> (still active for 45m)". A row gets
// at most maxAlertReminders reminders. Rows silenced by quiet hours, of
// monitors in maintenance or behind a down parent, and system status alerts
// behind a down parent are skipped without counting a reminder.
// Acknowledging or resolving a row ends its reminders.
const (
	reminderMinMinutes = 5
	reminderMaxMinutes = 1440
	maxAlertReminders  = 24
)

// reminderSettings are the reminder fields of user_settings.settings.
type reminderSettings struct {
	ReminderMinutes *float64 `json:"reminderMinutes"`
}

// validReminderMinutes reports whether minutes is an allowed reminder interval.
func validReminderMinutes(minutes float64) bool {
	return minutes == 0 || (minutes == float64(int(minutes)) && minutes >= reminderMinMinutes && minutes <= reminderMaxMinutes)
}

// validateReminderSettings rejects user settings with an invalid reminderMinutes.
func validateReminderSettings(e *core.RecordRequestEvent) error {
	var settings reminderSettings
	if err := e.Record.UnmarshalJSONField("settings", &settings); err == nil && settings.ReminderMinutes != nil &&
		!validReminderMinutes(*settings.ReminderMinutes) {
		return e.BadRequestError(fmt.Sprintf("The reminder interval must be 0 (off) or between %d and %d minutes.", reminderMinMinutes, reminderMaxMinutes), nil)
	}
	return e.Next()
}

// SendAlertReminders sends the due reminders of unacknowledged alerts. It
// runs every minute; errors are logged.
func (am *AlertManager) SendAlertReminders() {
	if err := am.sendAlertReminders(time.Now()); err != nil {
		am.hub.Logger().Error("Failed to send alert reminders", "err", err)
	}
}

// reminderUsers returns the reminder interval of each user with reminders on.
func reminderUsers(app core.App) (map[string]time.Duration, error) {
	var rows []struct {
		User     string `db:"user"`
		Settings string `db:"settings"`
	}
	err := app.DB().Select("user", "settings").From("user_settings").
		Where(dbx.NewExp("settings LIKE {:pattern}", dbx.Params{"pattern": "%reminderMinutes%"})).All(&rows)
	if err != nil {
		return nil, err
	}
	users := make(map[string]time.Duration, len(rows))
	for _, row := range rows {
		var settings reminderSettings
		if json.Unmarshal([]byte(row.Settings), &settings) != nil || settings.ReminderMinutes == nil {
			continue
		}
		minutes := *settings.ReminderMinutes
		if minutes == 0 || !validReminderMinutes(minutes) {
			continue
		}
		users[row.User] = time.Duration(minutes) * time.Minute
	}
	return users, nil
}

// reminderDue reports whether an open history row is due a reminder at now.
func reminderDue(record *core.Record, interval time.Duration, now time.Time) bool {
	if !record.GetDateTime("resolved").IsZero() || !record.GetDateTime("acknowledgedAt").IsZero() {
		return false
	}
	if record.GetInt("reminderCount") >= maxAlertReminders {
		return false
	}
	last := record.GetDateTime("remindedAt").Time()
	if last.IsZero() {
		last = record.GetDateTime("created").Time()
	}
	return !now.Before(last.Add(interval))
}

func (am *AlertManager) sendAlertReminders(now time.Time) error {
	users, err := reminderUsers(am.hub)
	if err != nil || len(users) == 0 {
		return err
	}
	for user, interval := range users {
		records, err := am.hub.FindAllRecords("alerts_history", dbx.HashExp{"user": user, "resolved": "", "acknowledgedAt": ""},
			dbx.NewExp("reminderCount < {:max}", dbx.Params{"max": maxAlertReminders}))
		if err != nil {
			return err
		}
		for _, record := range records {
			if !reminderDue(record, interval, now) || am.reminderSuppressed(record, now) {
				continue
			}
			if err := am.sendAlertReminder(record.Id, interval, now); err != nil {
				am.hub.Logger().Error("Failed to send alert reminder", "alert", record.Id, "err", err)
			}
		}
	}
	return nil
}

// reminderSuppressed reports whether a row's reminder is held: by quiet
// hours, a monitor maintenance window, or a down parent monitor.
func (am *AlertManager) reminderSuppressed(record *core.Record, now time.Time) bool {
	if monitorID := record.GetString("monitor"); monitorID != "" {
		if am.inMaintenance != nil && am.inMaintenance(monitorID, now) {
			return true
		}
		if am.monitorSuppressed != nil && am.monitorSuppressed(monitorID) {
			return true
		}
	} else if systemID := record.GetString("system"); systemID != "" && record.GetString("name") == "Status" {
		if am.systemSuppressed != nil && am.systemSuppressed(systemID) {
			return true
		}
	}
	severity, _ := am.historyRouting(record)
	return am.silencedAt(record.GetString("user"), record.GetString("system"), severity, now)
}

var errReminderNotDue = errors.New("reminder not due")

// sendAlertReminder records a reminder of a history row, if it is still
// due, and then sends it.
func (am *AlertManager) sendAlertReminder(id string, interval time.Duration, now time.Time) error {
	var record *core.Record
	err := am.hub.RunInTransaction(func(tx core.App) error {
		var err error
		record, err = tx.FindRecordById("alerts_history", id)
		if err != nil {
			return err
		}
		// The row may have been acknowledged or resolved meanwhile.
		if !reminderDue(record, interval, now) {
			return errReminderNotDue
		}
		record.Set("remindedAt", now.UTC())
		record.Set("reminderCount", record.GetInt("reminderCount")+1)
		return tx.Save(record)
	})
	if errors.Is(err, errReminderNotDue) {
		return nil
	}
	if err != nil {
		return err
	}
	return am.deliverAlert(am.reminderMessage(record, now))
}

// reminderMessage builds the reminder notification of a history row.
func (am *AlertManager) reminderMessage(record *core.Record, now time.Time) AlertMessageData {
	name := record.GetString("name")
	systemID := record.GetString("system")
	monitorName := record.GetString("monitor_name")
	var systemName string
	if systemID != "" {
		if system, err := am.hub.FindRecordById("systems", systemID); err == nil {
			systemName = system.GetString("name")
		}
	}
	since := record.GetDateTime("created").Time()
	title := reminderTitle(name, systemName, monitorName)
	state := "active"
	if name == alertNameMonitorDown || name == "Status" {
		state = "down"
	}
	duration := formatOutage(now.Sub(since))
	count := record.GetInt("reminderCount")
	message := fmt.Sprintf("%s has been %s since %s (%s) and is not acknowledged.\nReminder %d of %d.",
		title, state, formatAlertTime(since), duration, count, maxAlertReminders)

	link, linkText := am.hub.MakeLink("monitors"), "View monitors"
	if record.GetString("monitor") == "" && systemID != "" {
		link, linkText = am.hub.MakeLink("system", systemID), "View "+systemName
	}
	severity, channels := am.historyRouting(record)
	targetName := monitorName
	if record.GetString("monitor") == "" && systemName != "" {
		targetName = systemName
	}
	return AlertMessageData{
		UserID:    record.GetString("user"),
		SystemID:  systemID,
		Title:     fmt.Sprintf("Reminder: %s (still %s for %s)", title, state, duration),
		Message:   message,
		Link:      link,
		LinkText:  linkText,
		HistoryID: record.Id,
		Severity:  severity,
		Channels:  channels,
		Name:      targetName,
		Value:     strconv.FormatFloat(record.GetFloat("value"), 'f', -1, 64),
		Status:    "reminder",
		MonitorID: record.GetString("monitor"),
		AlertType: record.GetString("name"),
	}
}

// reminderTitle describes an open alert history row.
func reminderTitle(name, systemName, monitorName string) string {
	switch name {
	case alertNameMonitorDown:
		return fmt.Sprintf("%s is down", monitorName)
	case alertNameMonitorCert:
		return fmt.Sprintf("Certificate for %s expires soon", monitorName)
	case alertNameMonitorLoss:
		return fmt.Sprintf("%s: packet loss above threshold", monitorName)
	case alertNameMonitorLatency:
		return fmt.Sprintf("%s: response time above threshold", monitorName)
	case alertNameNetworkMonitorLoss:
		return fmt.Sprintf("Network monitor loss on %s: %s", systemName, monitorName)
	case "Status":
		return fmt.Sprintf("Connection to %s is down", systemName)
	}
	label := name
	if after, ok := strings.CutPrefix(name, "LoadAvg"); ok {
		label = after + "m load"
	}
	if systemName == "" {
		return label + " alert"
	}
	return fmt.Sprintf("%s %s alert", systemName, label)
}

// historyRouting returns the severity and explicit channels of an alert
// history row, so reminders reach the channels of the original alert: the
// row's recorded severity, else the severity of its alert (the alerts
// record, or the monitor for monitor alerts).
func (am *AlertManager) historyRouting(record *core.Record) (Severity, []string) {
	severity, channels := am.alertRouting(record)
	if recorded, ok := ParseSeverity(record.GetString("severity")); ok {
		severity = recorded
	}
	return severity, channels
}

// alertRouting derives the severity and explicit channels of a history row
// from its alert configuration.
func (am *AlertManager) alertRouting(record *core.Record) (Severity, []string) {
	name := record.GetString("name")
	value := record.GetFloat("value")
	def := defaultSeverity(name, value)
	var source *core.Record
	if monitorID := record.GetString("monitor"); monitorID != "" {
		source, _ = am.hub.FindRecordById("network_monitors", monitorID)
	} else if alertID := record.GetString("alert_id"); alertID != "" {
		source, _ = am.hub.FindRecordById("alerts", alertID)
	}
	if source == nil {
		return def, nil
	}
	return Severity(source.GetString("severity")).orDefault(def), source.GetStringSlice("channels")
}

// setHistorySeverity records the severity of new alert history rows that
// do not set one.
func (am *AlertManager) setHistorySeverity(e *core.RecordEvent) error {
	if _, ok := ParseSeverity(e.Record.GetString("severity")); !ok {
		severity, _ := am.alertRouting(e.Record)
		e.Record.Set("severity", string(severity))
	}
	return e.Next()
}
