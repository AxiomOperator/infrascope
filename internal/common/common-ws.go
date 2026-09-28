package common

import (
	"encoding/hex"

	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel/internal/entities/smart"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/entities/systemd"
)

type WebSocketAction = uint8

const (
	// Request system data from agent
	GetData WebSocketAction = iota
	// Check the fingerprint of the agent
	CheckFingerprint
	// Request container logs from agent
	GetContainerLogs
	// Request container info from agent
	GetContainerInfo
	// Request SMART data from agent
	GetSmartData
	// Request detailed systemd service info from agent
	GetSystemdInfo
	// Request ZFS detail data from agent
	GetZfsData
	// Sync network monitor configuration to agent
	SyncNetworkMonitors
	// Request the list of pending package updates from agent
	GetPackageUpdates
	// Add new actions here...
)

// HubRequest defines the structure for requests sent from hub to agent.
type HubRequest[T any] struct {
	Action WebSocketAction `cbor:"0,keyasint"`
	Data   T               `cbor:"1,keyasint,omitempty,omitzero"`
	Id     *uint32         `cbor:"2,keyasint,omitempty"`
}

// AgentResponse defines the structure for responses sent from agent to hub.
type AgentResponse struct {
	Id          *uint32                    `cbor:"0,keyasint,omitempty"`
	SystemData  *system.CombinedData       `cbor:"1,keyasint,omitempty,omitzero"` // Legacy (<= 0.17)
	Fingerprint *FingerprintResponse       `cbor:"2,keyasint,omitempty,omitzero"` // Legacy (<= 0.17)
	Error       string                     `cbor:"3,keyasint,omitempty,omitzero"`
	String      *string                    `cbor:"4,keyasint,omitempty,omitzero"` // Legacy (<= 0.17)
	SmartData   map[string]smart.SmartData `cbor:"5,keyasint,omitempty,omitzero"` // Legacy (<= 0.17)
	ServiceInfo systemd.ServiceDetails     `cbor:"6,keyasint,omitempty,omitzero"` // Legacy (<= 0.17)
	// Data is the generic response payload for new endpoints (0.18+)
	Data          cbor.RawMessage `cbor:"7,keyasint,omitempty,omitzero"`
	SmartComplete bool            `cbor:"8,keyasint,omitempty,omitzero"`
}

// HubAuthNonceHeader is the request header carrying the agent's per-connection
// nonce, which the hub includes in the signature that proves its identity.
const HubAuthNonceHeader = "X-Nonce"

// HubAuthNonceSize is the length of the hub auth nonce in bytes (hex encoded on the wire).
const HubAuthNonceSize = 32

// IsValidHubAuthNonce reports whether nonce is a hex-encoded HubAuthNonceSize-byte value.
func IsValidHubAuthNonce(nonce string) bool {
	if len(nonce) != HubAuthNonceSize*2 {
		return false
	}
	_, err := hex.DecodeString(nonce)
	return err == nil
}

// HubAuthChallenge returns the message the hub signs to prove its identity to
// the agent. With a nonce the signature is bound to a single connection and
// can't be replayed. Without one (older agents) it covers only the token.
func HubAuthChallenge(token, nonce string) []byte {
	if nonce == "" {
		return []byte(token)
	}
	return []byte("beszel-hub-auth-v2\x00" + token + "\x00" + nonce)
}

type FingerprintRequest struct {
	Signature   []byte `cbor:"0,keyasint"`
	NeedSysInfo bool   `cbor:"1,keyasint"` // For universal token system creation
}

type FingerprintResponse struct {
	Fingerprint string `cbor:"0,keyasint"`
	// Optional system info for universal token system creation
	Hostname string `cbor:"1,keyasint,omitzero"`
	Port     string `cbor:"2,keyasint,omitzero"`
	Name     string `cbor:"3,keyasint,omitzero"`
}

type DataRequestOptions struct {
	CacheTimeMs    uint16 `cbor:"0,keyasint"`
	IncludeDetails bool   `cbor:"1,keyasint"`
}

type ZfsDataRequest struct {
	Force bool `cbor:"0,keyasint,omitempty"`
}

type ContainerLogsRequest struct {
	ContainerID string `cbor:"0,keyasint"`
}

type ContainerInfoRequest struct {
	ContainerID string `cbor:"0,keyasint"`
}

type SystemdInfoRequest struct {
	ServiceName string `cbor:"0,keyasint"`
}
