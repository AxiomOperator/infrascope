package system

// Discovery lists the monitors an agent found in the labels of its running
// containers. The agent only sends it when the hub asks for it
// (common.DataRequestOptions.Discovery) and Docker could be queried, so a nil
// Discovery means "unknown" and an empty one "no labeled containers".
type Discovery struct {
	Monitors []DiscoveredMonitor `cbor:"0,keyasint,omitempty" json:"m,omitempty"`
}

// DiscoveredMonitor is one monitor declared by container labels.
//
// For infrascope.monitor.* labels, Key is the monitor id within the
// container ("default" for labels without an id) and Labels holds the label
// fields without the prefix (type, name, target, port, ...). For Traefik
// routers, Traefik is true, Key is "traefik.<router>.<host>" and Labels holds
// the derived type and target.
type DiscoveredMonitor struct {
	Container string            `cbor:"0,keyasint" json:"c"`
	Key       string            `cbor:"1,keyasint" json:"k"`
	Labels    map[string]string `cbor:"2,keyasint,omitempty" json:"l,omitempty"`
	// Port is the host port to probe: the port label translated through the
	// container's published ports, or the lowest published TCP port when the
	// label is omitted. 0 when unknown.
	Port    uint16 `cbor:"3,keyasint,omitempty" json:"p,omitempty"`
	Traefik bool   `cbor:"4,keyasint,omitempty" json:"t,omitempty"`
}
