//go:build testing

package records

import (
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// DeleteOldSystemStats exposes deleteOldSystemStats for testing
func DeleteOldSystemStats(app core.App) error {
	return deleteOldSystemStats(app)
}

// DeleteOldAlertsHistory exposes deleteOldAlertsHistory for testing
func DeleteOldAlertsHistory(app core.App, countToKeep, countBeforeDeletion int) error {
	return deleteOldAlertsHistory(app, countToKeep, countBeforeDeletion)
}

// TwoDecimals exposes twoDecimals for testing
func TwoDecimals(value float64) float64 {
	return twoDecimals(value)
}

// DeleteOldMonitorEvents exposes deleteOldMonitorEvents for testing
func DeleteOldMonitorEvents(app core.App, retention time.Duration, countToKeep, countBeforeDeletion int) error {
	return deleteOldMonitorEvents(app, retention, countToKeep, countBeforeDeletion)
}

// DeleteOldSystemEvents exposes deleteOldSystemEvents for testing
func DeleteOldSystemEvents(app core.App, retention time.Duration, countToKeep, countBeforeDeletion int) error {
	return deleteOldSystemEvents(app, retention, countToKeep, countBeforeDeletion)
}
