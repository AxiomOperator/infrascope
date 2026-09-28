package agent

import "github.com/henrygd/beszel/internal/netmon"

// MonitorManager manages network monitor configurations and task lifetimes.
type MonitorManager = netmon.Manager

func newMonitorManager() *MonitorManager {
	return netmon.NewManager(defaultDataCacheTimeMs)
}
