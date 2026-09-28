package alerts

import "time"

// Window types of quiet_hours and monitor_maintenance records.
const (
	WindowOneTime = "one-time"
	WindowDaily   = "daily"
)

// WindowActive reports whether a quiet hours or maintenance window is active at now.
//
// One-time windows are active from start (inclusive) to end (exclusive).
//
// Daily windows use only the time of day. The UI stores them as the chosen
// local times on the creation date, converted to UTC, and shows them as that
// UTC time of day shifted by the stored date's offset. The hub does not know
// the user's time zone, so it compares UTC times of day, which matches the UI
// while the user's offset equals the one at creation. A window whose end is
// before its start crosses midnight; equal times never match.
func WindowActive(windowType string, start, end, now time.Time) bool {
	if windowType != WindowDaily {
		return !now.Before(start) && now.Before(end)
	}
	startMinutes := minuteOfDay(start)
	endMinutes := minuteOfDay(end)
	nowMinutes := minuteOfDay(now)
	if endMinutes < startMinutes {
		return nowMinutes >= startMinutes || nowMinutes < endMinutes
	}
	return nowMinutes >= startMinutes && nowMinutes < endMinutes
}

// minuteOfDay returns the UTC minutes since midnight of t.
func minuteOfDay(t time.Time) int {
	hour, minute, _ := t.UTC().Clock()
	return hour*60 + minute
}
