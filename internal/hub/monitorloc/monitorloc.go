// Package monitorloc reads the locations of network monitors: the runners
// that check a monitor, either the hub ("hub") or an agent system (its id).
//
// A network_monitors record lists its locations in the "locations" JSON
// array. Records stored before multi-location checks (or saved without the
// hooks) have none; their single location is derived from "system": the
// hub when it is empty, the system otherwise. "system" holds the primary
// location, the first agent location ("" when the hub is the only one), and
// "locationSystems" mirrors the agent locations as a relation for API rules.
package monitorloc

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

const (
	// Hub is the location of monitors checked by the hub itself.
	Hub = "hub"
	// Max is the largest number of locations of a monitor.
	Max = 10
)

// FromSystemID returns the location of a runner: the hub for "", else the system.
func FromSystemID(systemID string) string {
	if systemID == "" {
		return Hub
	}
	return systemID
}

// SystemID returns the system id of a location, "" for the hub.
func SystemID(location string) string {
	if location == Hub {
		return ""
	}
	return location
}

// Stored returns the stored locations of a record, trimmed and deduplicated
// in order. It is empty for records without locations.
func Stored(record *core.Record) []string {
	var raw []string
	value := record.Get("locations")
	switch v := value.(type) {
	case []string:
		raw = v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				raw = append(raw, s)
			}
		}
	default:
		text := record.GetString("locations")
		if text != "" && text != "null" {
			_ = json.Unmarshal([]byte(text), &raw)
		}
	}
	return Normalize(raw)
}

// Normalize trims and deduplicates locations, keeping their order.
func Normalize(raw []string) []string {
	locations := make([]string, 0, len(raw))
	for _, location := range raw {
		location = strings.TrimSpace(location)
		if location == "" || slices.Contains(locations, location) {
			continue
		}
		locations = append(locations, location)
	}
	return locations
}

// Of returns the locations of a record: its stored locations, or the single
// location derived from its system.
func Of(record *core.Record) []string {
	if locations := Stored(record); len(locations) > 0 {
		return locations
	}
	return []string{FromSystemID(record.GetString("system"))}
}

// Has reports whether location is one of the record's locations.
func Has(record *core.Record, location string) bool {
	return slices.Contains(Of(record), location)
}

// Primary returns the primary location's system id: the first agent
// location, or "" when the hub is the only location.
func Primary(locations []string) string {
	for _, location := range locations {
		if location != Hub {
			return location
		}
	}
	return ""
}

// Systems returns the agent locations (system ids), in order.
func Systems(locations []string) []string {
	systems := make([]string, 0, len(locations))
	for _, location := range locations {
		if location != Hub {
			systems = append(systems, location)
		}
	}
	return systems
}

// DefaultQuorum is the default quorum of n locations: a majority.
func DefaultQuorum(n int) int {
	if n <= 1 {
		return 1
	}
	return n/2 + 1
}

// QuorumFor returns the effective quorum of a stored quorum value for n
// locations: the value when it is between 1 and n, the default otherwise.
func QuorumFor(quorum, n int) int {
	if quorum < 1 || quorum > max(n, 1) {
		return DefaultQuorum(n)
	}
	return quorum
}

// Quorum returns the effective quorum of a record: how many locations must
// confirm a monitor down.
func Quorum(record *core.Record) int {
	return QuorumFor(record.GetInt("quorum"), len(Of(record)))
}

// Resolve returns the locations a record should have: its stored locations,
// or the location derived from its system when it has none. When the record
// is not new, its system was changed and its locations were not, the monitor
// was moved by a writer that only knows "system", so its locations follow
// the new system.
func Resolve(record *core.Record) []string {
	locations := Stored(record)
	if original := record.Original(); !record.IsNew() && original != nil && len(locations) > 0 &&
		record.GetString("system") != original.GetString("system") &&
		slices.Equal(locations, Stored(original)) {
		locations = nil
	}
	if len(locations) == 0 {
		locations = []string{FromSystemID(record.GetString("system"))}
	}
	return locations
}

// Set stores locations and the fields derived from them: system (the
// primary location), locationSystems and quorum (defaulted when out of range).
func Set(record *core.Record, locations []string) {
	record.Set("locations", locations)
	record.Set("system", Primary(locations))
	record.Set("locationSystems", Systems(locations))
	record.Set("quorum", QuorumFor(record.GetInt("quorum"), len(locations)))
}

// Normalized sets the location fields of a record from Resolve.
func Normalized(record *core.Record) {
	Set(record, Resolve(record))
}
