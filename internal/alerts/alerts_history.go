package alerts

import (
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// On triggered alert record delete, set matching alert history record to resolved
func resolveHistoryOnAlertDelete(e *core.RecordEvent) error {
	// Monitor alert history belongs to monitors, not alerts records.
	if isMonitorAlertName(e.Record.GetString("name")) {
		return e.Next()
	}
	if e.Record.GetString("name") == alertNameNetworkMonitorLoss {
		if err := resolveNetworkMonitorHistory(e.App, e.Record.Id); err != nil {
			return err
		}
		return e.Next()
	}
	if !e.Record.GetBool("triggered") {
		return e.Next()
	}
	_ = resolveAlertHistoryRecord(e.App, e.Record.Id)
	return e.Next()
}

// On alert record update, update alert history record
func updateHistoryOnAlertUpdate(e *core.RecordEvent) error {
	// Network monitor incidents have separate history entries per monitor, and
	// monitor alert history belongs to monitors, not alerts records.
	if name := e.Record.GetString("name"); name == alertNameNetworkMonitorLoss || isMonitorAlertName(name) {
		return e.Next()
	}
	original := e.Record.Original()
	new := e.Record

	originalTriggered := original.GetBool("triggered")
	newTriggered := new.GetBool("triggered")

	// no need to update alert history if triggered state has not changed
	if originalTriggered == newTriggered {
		return e.Next()
	}

	// if new state is triggered, create new alert history record
	if newTriggered {
		_, _ = createAlertHistoryRecord(e.App, new)
		return e.Next()
	}

	// if new state is not triggered, check for matching alert history record and set it to resolved
	_ = resolveAlertHistoryRecord(e.App, new.Id)
	return e.Next()
}

// resolveAlertHistoryRecord sets the resolved field to the current time
func resolveAlertHistoryRecord(app core.App, alertRecordID string) error {
	alertHistoryRecord, err := app.FindFirstRecordByFilter("alerts_history",
		"alert_id={:alert_id} && resolved=null && name!={:down} && name!={:cert}",
		dbx.Params{"alert_id": alertRecordID, "down": alertNameMonitorDown, "cert": alertNameMonitorCert})
	if err != nil || alertHistoryRecord == nil {
		return err
	}
	alertHistoryRecord.Set("resolved", time.Now().UTC())
	err = app.Save(alertHistoryRecord)
	if err != nil {
		app.Logger().Error("Failed to resolve alert history", "err", err)
	}
	return err
}

// createAlertHistoryRecord creates a new alert history record
func createAlertHistoryRecord(app core.App, alertRecord *core.Record) (alertHistoryRecord *core.Record, err error) {
	alertHistoryCollection, err := app.FindCachedCollectionByNameOrId("alerts_history")
	if err != nil {
		return nil, err
	}
	alertHistoryRecord = core.NewRecord(alertHistoryCollection)
	alertHistoryRecord.Set("alert_id", alertRecord.Id)
	alertHistoryRecord.Set("user", alertRecord.GetString("user"))
	alertHistoryRecord.Set("system", alertRecord.GetString("system"))
	alertHistoryRecord.Set("name", alertRecord.GetString("name"))
	alertHistoryRecord.Set("value", alertRecord.GetFloat("value"))
	err = app.Save(alertHistoryRecord)
	if err != nil {
		app.Logger().Error("Failed to save alert history", "err", err)
	}
	return alertHistoryRecord, err
}
