package agent

import (
	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/netmon"
)

// MonitorManager manages network monitor configurations and task lifetimes.
type MonitorManager = netmon.Manager

// newMonitorManager returns the agent's monitor manager. Docker monitors
// check containers with dm, which is nil when Docker is not configured.
func newMonitorManager(dm *dockerManager) *MonitorManager {
	return netmon.NewManager(defaultDataCacheTimeMs, netmon.WithProbe(monitor.ProtocolDocker, dockerMonitorProbe(dm)))
}
