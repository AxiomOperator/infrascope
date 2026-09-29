package records

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// Delete old records
func (rm *RecordManager) DeleteOldRecords() {
	// Pocketbase cron does not handle errors, log them here.
	rm.app.RunInTransaction(func(txApp core.App) error {
		err := deleteOldSystemStats(txApp)
		if err != nil {
			slog.Error("Error deleting old system stats", "err", err)
		}
		err = deleteOldContainerRecords(txApp)
		if err != nil {
			slog.Error("Error deleting old container records", "err", err)
		}
		err = deleteOldSystemdServiceRecords(txApp)
		if err != nil {
			slog.Error("Error deleting old systemd service records", "err", err)
		}
		err = deleteOldAlertsHistory(txApp, 200, 250)
		if err != nil {
			slog.Error("Error deleting old alerts history", "err", err)
		}
		err = deleteOldMonitorEvents(txApp, monitorEventsRetention, 5000, 5000)
		if err != nil {
			slog.Error("Error deleting old monitor events", "err", err)
		}
		err = deleteOldSystemEvents(txApp, systemEventsRetention, 5000, 5000)
		if err != nil {
			slog.Error("Error deleting old system events", "err", err)
		}
		err = deleteOldQuietHours(txApp)
		if err != nil {
			slog.Error("Error deleting old quiet hours", "err", err)
		}
		return nil
	})
}

// Delete old alerts history records
func deleteOldAlertsHistory(app core.App, countToKeep, countBeforeDeletion int) error {
	db := app.DB()
	var users []struct {
		Id string `db:"user"`
	}
	err := db.NewQuery("SELECT user, COUNT(*) as count FROM alerts_history GROUP BY user HAVING count > {:countBeforeDeletion}").Bind(dbx.Params{"countBeforeDeletion": countBeforeDeletion}).All(&users)
	if err != nil {
		return err
	}
	for _, user := range users {
		_, err = db.NewQuery("DELETE FROM alerts_history WHERE user = {:user} AND id NOT IN (SELECT id FROM alerts_history WHERE user = {:user} ORDER BY created DESC LIMIT {:countToKeep})").Bind(dbx.Params{"user": user.Id, "countToKeep": countToKeep}).Execute()
		if err != nil {
			return err
		}
	}
	return nil
}

// monitorEventsRetention is how long closed monitor status segments are kept.
const monitorEventsRetention = 90 * 24 * time.Hour

// systemEventsRetention is how long closed system status segments are kept.
const systemEventsRetention = 90 * 24 * time.Hour

// Deletes closed monitor_events segments that ended before the retention
// period, and keeps at most countToKeep of the newest segments per monitor
// once a monitor has more than countBeforeDeletion. Open segments (end = 0)
// always cover the present and are never deleted by age.
func deleteOldMonitorEvents(app core.App, retention time.Duration, countToKeep, countBeforeDeletion int) error {
	return deleteOldSegments(app, "monitor_events", "monitor", retention, countToKeep, countBeforeDeletion)
}

// Deletes old system_events segments like deleteOldMonitorEvents, per system.
func deleteOldSystemEvents(app core.App, retention time.Duration, countToKeep, countBeforeDeletion int) error {
	return deleteOldSegments(app, "system_events", "system", retention, countToKeep, countBeforeDeletion)
}

// deleteOldSegments deletes closed segments of table that ended before the
// retention period and caps the segments per owner (the owner column).
// table and owner are constants of the callers.
func deleteOldSegments(app core.App, table, owner string, retention time.Duration, countToKeep, countBeforeDeletion int) error {
	db := app.DB()
	cutoff := time.Now().UTC().Add(-retention).UnixMilli()
	if _, err := db.NewQuery("DELETE FROM " + table + " WHERE end != 0 AND end < {:cutoff}").Bind(dbx.Params{"cutoff": cutoff}).Execute(); err != nil {
		return err
	}
	var owners []struct {
		Id string `db:"owner"`
	}
	err := db.NewQuery("SELECT " + owner + " AS owner, COUNT(*) as count FROM " + table + " GROUP BY " + owner + " HAVING count > {:countBeforeDeletion}").Bind(dbx.Params{"countBeforeDeletion": countBeforeDeletion}).All(&owners)
	if err != nil {
		return err
	}
	for _, o := range owners {
		_, err = db.NewQuery("DELETE FROM " + table + " WHERE " + owner + " = {:owner} AND id NOT IN (SELECT id FROM " + table + " WHERE " + owner + " = {:owner} ORDER BY start DESC LIMIT {:countToKeep})").Bind(dbx.Params{"owner": o.Id, "countToKeep": countToKeep}).Execute()
		if err != nil {
			return err
		}
	}
	return nil
}

// Deletes system_stats records older than what is displayed in the UI
func deleteOldSystemStats(app core.App) error {
	// Collections to process
	collections := [3]string{"system_stats", "container_stats", "network_monitor_stats"}

	// Record types and their retention periods
	type RecordDeletionData struct {
		recordType string
		retention  time.Duration
	}
	recordData := []RecordDeletionData{
		{recordType: "1m", retention: time.Hour},             // 1 hour
		{recordType: "10m", retention: 12 * time.Hour},       // 12 hours
		{recordType: "20m", retention: 24 * time.Hour},       // 1 day
		{recordType: "120m", retention: 7 * 24 * time.Hour},  // 7 days
		{recordType: "480m", retention: 30 * 24 * time.Hour}, // 30 days
	}

	now := time.Now().UTC()
	db := app.DB()

	for _, collection := range collections {
		query := db.Delete(collection, dbx.NewExp("type={:type} AND created<{:created}"))
		for _, rd := range recordData {
			if _, err := query.Bind(dbx.Params{
				"type":    rd.recordType,
				"created": getCreatedTimeField(collection, now.Add(-rd.retention)),
			}).Execute(); err != nil {
				return fmt.Errorf("failed to delete from %s: %v", collection, err)
			}
		}
	}
	return nil
}

// Deletes systemd service records that haven't been updated in the last 20 minutes
func deleteOldSystemdServiceRecords(app core.App) error {
	now := time.Now().UTC()
	twentyMinutesAgo := now.Add(-20 * time.Minute)

	// Delete systemd service records where updated < twentyMinutesAgo
	_, err := app.DB().NewQuery("DELETE FROM systemd_services WHERE updated < {:updated}").Bind(dbx.Params{"updated": twentyMinutesAgo.UnixMilli()}).Execute()
	if err != nil {
		return fmt.Errorf("failed to delete old systemd service records: %v", err)
	}

	return nil
}

// Deletes container records that haven't been updated in the last 10 minutes
func deleteOldContainerRecords(app core.App) error {
	now := time.Now().UTC()
	tenMinutesAgo := now.Add(-10 * time.Minute)

	// Delete container records where updated < tenMinutesAgo
	_, err := app.DB().NewQuery("DELETE FROM containers WHERE updated < {:updated}").Bind(dbx.Params{"updated": tenMinutesAgo.UnixMilli()}).Execute()
	if err != nil {
		return fmt.Errorf("failed to delete old container records: %v", err)
	}

	return nil
}

// Deletes old quiet hours records where end date has passed
func deleteOldQuietHours(app core.App) error {
	now := time.Now().UTC()
	_, err := app.DB().NewQuery("DELETE FROM quiet_hours WHERE type = 'one-time' AND end < {:now}").Bind(dbx.Params{"now": now}).Execute()
	if err != nil {
		return err
	}

	return nil
}
