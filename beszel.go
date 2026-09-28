// Package beszel provides core application constants and version information
// which are used throughout the application.
package beszel

import "github.com/blang/semver"

const (
	// Version is the current version of the application.
	Version = "0.20.0"
	// AppName is the name of the application.
	AppName = "beszel"
)

// MinVersionCbor is the minimum supported version for CBOR compatibility.
var MinVersionCbor = semver.MustParse("0.12.0")

// MinVersionAgentResponse is the minimum supported version for AgentResponse compatibility.
var MinVersionAgentResponse = semver.MustParse("0.13.0")

// MinVersionZfsData is the minimum agent version that supports ZFS detail requests.
var MinVersionZfsData = semver.MustParse("0.18.9")

// MinVersionNetworkMonitors is the minimum agent version that supports network monitor sync.
var MinVersionNetworkMonitors = semver.MustParse("0.20.0")

// MinVersionMonitorChecks is the minimum agent version that supports monitor
// timeouts, HTTP options, retry intervals and per-check events. Older agents
// receive configs without these fields (see monitor.Config.Legacy).
var MinVersionMonitorChecks = semver.MustParse("0.21.0")
