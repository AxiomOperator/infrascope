// Package uptime derives the up/down status of network monitors from their
// individual check results, records status segments in monitor_events, keeps
// the server-managed status fields of network_monitors current and reports
// confirmed status changes to a notifier.
//
// The package depends only on core.App and the monitor entities, so the hub,
// the system updaters and the hub monitor runner can all feed it.
package uptime

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"time"
)

// Monitor status values stored in network_monitors.status. monitor_events
// segments use the same values except pending, which never opens a segment.
const (
	StatusUp          = "up"
	StatusDown        = "down"
	StatusPending     = "pending"
	StatusMaintenance = "maintenance"
	StatusPaused      = "paused"
	StatusUnknown     = "unknown"
)

// Recent check states stored in the second element of a RecentCheck.
const (
	RecentDown    = 0 // failed check while the monitor is confirmed down
	RecentUp      = 1 // successful check
	RecentPending = 2 // failed check that has not (yet) confirmed the monitor down
)

const (
	// recentSize is the number of checks kept in network_monitors.recent.
	recentSize = 60
	// uptimeRefresh is the shortest interval between uptime recomputations of a monitor.
	uptimeRefresh = 5 * time.Minute
	// agentFetchInterval is how often the hub collects agent results, which
	// delays delivery of agent checks by up to this much.
	agentFetchInterval = time.Minute
	// maxSuppressedByLength matches the size of the suppressedBy fields.
	maxSuppressedByLength = 1000
	// maxErrorLength matches the size of the lastError and monitor_events.error fields.
	maxErrorLength = 300
	// legacyFailureError is the error of checks synthesised for agents that do
	// not report individual checks.
	legacyFailureError = "probe failed (agent does not report errors)"
)

// Transition is a confirmed up/down status change. Transitions of monitors
// without the notify option are reported too (for automatic incidents), with
// Notify false; they must not be notified.
type Transition struct {
	// Notify is the monitor's notify option.
	Notify    bool
	MonitorID string
	// SystemID is empty for hub monitors.
	SystemID string
	// Name is the monitor name, or its target when it has no name.
	Name   string
	Target string
	// Status is the new confirmed status, "up" or "down".
	Status string
	// Prev is the previously notified status, "up", "down" or "" for a new monitor.
	Prev string
	// At is when the status changed (the confirming check, or the end of a
	// maintenance window that hid the change).
	At time.Time
	// Err and StatusCode are from the first failed check of the down period.
	Err        string
	StatusCode uint16
	// DownSince is the first failed check of the latest down period. For an
	// "up" transition it is when the recovered outage started.
	DownSince time.Time
}

// RecentCheck is one entry of network_monitors.recent. It is stored as the
// JSON array [unixSeconds, state, responseMs]:
//
//   - unixSeconds: check completion time in Unix seconds
//   - state: RecentDown (0), RecentUp (1) or RecentPending (2)
//   - responseMs: response time in milliseconds (two decimals), or -1 when the check failed
//
// The UI reads it as `recent: [number, number, number][]`, oldest first.
type RecentCheck struct {
	At         int64
	State      int
	ResponseMs float64
}

// MarshalJSON encodes the check as [unixSeconds, state, responseMs].
func (c RecentCheck) MarshalJSON() ([]byte, error) {
	return json.Marshal([3]float64{float64(c.At), float64(c.State), c.ResponseMs})
}

// UnmarshalJSON decodes a [unixSeconds, state, responseMs] array.
func (c *RecentCheck) UnmarshalJSON(data []byte) error {
	var values [3]float64
	if err := json.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("recent check: %w", err)
	}
	c.At = int64(values[0])
	c.State = int(values[1])
	c.ResponseMs = values[2]
	return nil
}

// Uptime is stored in network_monitors.uptime as {"d1": pct, "d7": pct, "d30": pct}.
// A percentage is null when the window has no up or down time, since
// maintenance, unknown and paused time do not count.
type Uptime struct {
	D1  *float64 `json:"d1"`
	D7  *float64 `json:"d7"`
	D30 *float64 `json:"d30"`
}

// Segment is a monitor_events row: a period with one displayed status.
type Segment struct {
	Status string `db:"status"`
	// Start and End are Unix milliseconds; End is 0 while the segment is open.
	Start int64 `db:"start"`
	End   int64 `db:"end"`
}

// uptimeWindows are the uptime windows, ordered as the fields of Uptime.
var uptimeWindows = [3]time.Duration{24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour}

// UptimeFromSegments computes uptime percentages at now. Open segments
// extend to now; up and down time form the denominator.
func UptimeFromSegments(segments []Segment, now time.Time) Uptime {
	nowMs := now.UnixMilli()
	var result [3]*float64
	for i, window := range uptimeWindows {
		from := nowMs - window.Milliseconds()
		var upMs, downMs int64
		for _, segment := range segments {
			end := segment.End
			if end == 0 || end > nowMs {
				end = nowMs
			}
			overlap := end - max(segment.Start, from)
			if overlap <= 0 {
				continue
			}
			switch segment.Status {
			case StatusUp:
				upMs += overlap
			case StatusDown:
				downMs += overlap
			}
		}
		if total := upMs + downMs; total > 0 {
			pct := math.Round(float64(upMs)/float64(total)*100_000) / 1000
			result[i] = &pct
		}
	}
	return Uptime{D1: result[0], D7: result[1], D30: result[2]}
}

// persistedState is stored in the hidden network_monitors.state JSON field.
//
//	{"v":1,"failStreak":2,"pendingSince":1700000000000,"pendingError":"timeout",
//	 "pendingStatusCode":503,"confirmed":"up","notified":"up","downSince":0,
//	 "maintenance":false}
type persistedState struct {
	Version int `json:"v"`
	// FailStreak counts consecutive failed checks.
	FailStreak int `json:"failStreak,omitempty"`
	// PendingSince, PendingError and PendingStatusCode describe the first
	// failed check of the current streak (Unix milliseconds).
	PendingSince      int64  `json:"pendingSince,omitempty"`
	PendingError      string `json:"pendingError,omitempty"`
	PendingStatusCode uint16 `json:"pendingStatusCode,omitempty"`
	// Confirmed is the latest confirmed status, "up", "down" or "" before the first.
	Confirmed string `json:"confirmed,omitempty"`
	// Notified is the confirmed status last reported (or silently accepted).
	Notified string `json:"notified,omitempty"`
	// DownSince is the first failed check of the latest down period (Unix milliseconds).
	DownSince int64 `json:"downSince,omitempty"`
	// Maintenance is whether the monitor was in a maintenance window at the last evaluation.
	Maintenance bool `json:"maintenance,omitempty"`
	// Locations holds the state of each location of a multi-location monitor.
	// Single-location monitors keep their location's state in the fields above.
	Locations map[string]locPersisted `json:"locations,omitempty"`
}

// equal reports whether two states are the same.
func (p persistedState) equal(o persistedState) bool {
	a, b := p.Locations, o.Locations
	p.Locations, o.Locations = nil, nil
	return reflect.DeepEqual(p, o) && maps.Equal(a, b)
}

// locPersisted is the persisted state of one location (see persistedState).
type locPersisted struct {
	FailStreak        int    `json:"failStreak,omitempty"`
	PendingSince      int64  `json:"pendingSince,omitempty"`
	PendingError      string `json:"pendingError,omitempty"`
	PendingStatusCode uint16 `json:"pendingStatusCode,omitempty"`
	Confirmed         string `json:"confirmed,omitempty"`
	DownSince         int64  `json:"downSince,omitempty"`
}

const stateVersion = 1

func truncateError(err string) string {
	return truncateText(err, maxErrorLength)
}

// truncateText shortens text to at most limit bytes without splitting a rune.
func truncateText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && text[cut]&0xC0 == 0x80 {
		cut--
	}
	return text[:cut]
}
