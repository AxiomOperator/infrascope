package alerts

import (
	"slices"
	"sync"

	"github.com/henrygd/beszel/internal/hub/monitorloc"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// locationMonitors returns the enabled monitors with a system as one of
// their locations.
func locationMonitors(app core.App, systemID string) ([]*core.Record, error) {
	records, err := app.FindAllRecords("network_monitors", dbx.HashExp{"enabled": true})
	if err != nil {
		return nil, err
	}
	location := monitorloc.FromSystemID(systemID)
	return slices.DeleteFunc(records, func(record *core.Record) bool {
		return !monitorloc.Has(record, location)
	}), nil
}

// networkMonitorCache keeps just the enabled monitor IDs and probe intervals
// needed for the alert fast path. Names and targets are read only on transitions.
// Returned maps are immutable; configuration changes invalidate the whole entry.
type networkMonitorCache struct {
	app     core.App
	mu      sync.RWMutex
	systems map[string]map[string]int
}

func newNetworkMonitorCache(app core.App) *networkMonitorCache {
	c := &networkMonitorCache{app: app, systems: make(map[string]map[string]int)}
	invalidate := func(e *core.RecordEvent) error {
		c.invalidateLocations(e.Record)
		return e.Next()
	}
	app.OnRecordAfterCreateSuccess("network_monitors").BindFunc(invalidate)
	app.OnRecordAfterDeleteSuccess("network_monitors").BindFunc(invalidate)
	app.OnRecordAfterUpdateSuccess("network_monitors").BindFunc(func(e *core.RecordEvent) error {
		old := e.Record.Original()
		// Realtime metric saves also invoke this hook. They must not evict config.
		if old.GetString("system") != e.Record.GetString("system") ||
			!slices.Equal(monitorloc.Of(old), monitorloc.Of(e.Record)) ||
			old.GetBool("enabled") != e.Record.GetBool("enabled") ||
			old.GetInt("interval") != e.Record.GetInt("interval") {
			c.invalidateLocations(old)
			c.invalidateLocations(e.Record)
		}
		return e.Next()
	})
	app.OnRecordAfterDeleteSuccess("systems").BindFunc(func(e *core.RecordEvent) error {
		c.invalidate(e.Record.Id)
		return e.Next()
	})
	return c
}

// invalidateLocations drops the entries of every location system of a monitor.
func (c *networkMonitorCache) invalidateLocations(record *core.Record) {
	for _, location := range monitorloc.Of(record) {
		c.invalidate(monitorloc.SystemID(location))
	}
}

func (c *networkMonitorCache) invalidate(systemID string) {
	c.mu.Lock()
	delete(c.systems, systemID)
	c.mu.Unlock()
}

func (c *networkMonitorCache) get(systemID string) (map[string]int, error) {
	c.mu.RLock()
	monitors, ok := c.systems[systemID]
	c.mu.RUnlock()
	if ok {
		return monitors, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if monitors, ok := c.systems[systemID]; ok {
		return monitors, nil
	}
	// Keep the lock through the load so a concurrent config change cannot be
	// invalidated first and then overwritten by the older query result.
	records, err := locationMonitors(c.app, systemID)
	if err != nil {
		return nil, err
	}
	monitors = make(map[string]int, len(records))
	for _, record := range records {
		monitors[record.Id] = record.GetInt("interval")
	}
	c.systems[systemID] = monitors
	return monitors, nil
}
