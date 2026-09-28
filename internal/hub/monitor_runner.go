package hub

import "github.com/pocketbase/pocketbase/core"

// hubMonitorRunner runs monitors that belong to the hub rather than an agent
// (network_monitors records without a system, including push monitors).
type hubMonitorRunner interface {
	// Sync starts, reconfigures or, for a disabled record, stops the record's monitor.
	Sync(record *core.Record)
	// Remove stops the monitor with the given id, if it runs.
	Remove(id string)
}

// noopHubMonitorRunner is the runner used until the hub probes monitors itself.
// Hub monitor records can be created and edited, but are not probed.
type noopHubMonitorRunner struct{}

func (noopHubMonitorRunner) Sync(*core.Record) {}

func (noopHubMonitorRunner) Remove(string) {}
