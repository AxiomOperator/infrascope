package monitor

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// Monitor protocols. ProtocolPush is declared with the Config type.
const (
	ProtocolICMP      = "icmp"
	ProtocolTCP       = "tcp"
	ProtocolHTTP      = "http"
	ProtocolDNS       = "dns"
	ProtocolSSH       = "ssh"
	ProtocolPostgres  = "postgres"
	ProtocolMySQL     = "mysql"
	ProtocolRedis     = "redis"
	ProtocolSMTP      = "smtp"
	ProtocolIMAP      = "imap"
	ProtocolGRPC      = "grpc"
	ProtocolMinecraft = "minecraft"
	ProtocolA2S       = "a2s"
	// ProtocolDocker checks a container on the agent's Docker or Podman host.
	// It only runs on agents.
	ProtocolDocker = "docker"
)

// Protocols lists every monitor protocol.
var Protocols = []string{
	ProtocolICMP, ProtocolTCP, ProtocolHTTP, ProtocolDNS, ProtocolPush,
	ProtocolSSH, ProtocolPostgres, ProtocolMySQL, ProtocolRedis, ProtocolSMTP, ProtocolIMAP,
	ProtocolGRPC, ProtocolMinecraft, ProtocolA2S, ProtocolDocker,
}

// checkProtocols were added with CheckOptions. Agents older than
// beszel.MinVersionMonitorChecks cannot run them.
var checkProtocols = []string{
	ProtocolSSH, ProtocolPostgres, ProtocolMySQL, ProtocolRedis, ProtocolSMTP, ProtocolIMAP,
	ProtocolGRPC, ProtocolMinecraft, ProtocolA2S, ProtocolDocker,
}

// IsCheckProtocol reports whether protocol requires an agent version of at
// least beszel.MinVersionMonitorChecks.
func IsCheckProtocol(protocol string) bool {
	return slices.Contains(checkProtocols, protocol)
}

// IsAgentOnlyProtocol reports whether monitors of protocol only run on agents.
func IsAgentOnlyProtocol(protocol string) bool {
	return protocol == ProtocolDocker
}

// UsesPort reports whether monitors of protocol connect to Config.Port.
func UsesPort(protocol string) bool {
	switch protocol {
	case ProtocolTCP, ProtocolSSH, ProtocolPostgres, ProtocolMySQL, ProtocolRedis, ProtocolSMTP,
		ProtocolIMAP, ProtocolGRPC, ProtocolMinecraft, ProtocolA2S:
		return true
	}
	return false
}

// DefaultPort returns the well-known port of protocol, which depends on the
// TLS options for smtp and imap. It is 0 when the protocol has no default
// (tcp and grpc require a port).
func DefaultPort(protocol string, check *CheckOptions) uint16 {
	var tls, startTLS bool
	if check != nil {
		tls, startTLS = check.TLS, check.StartTLS
	}
	switch protocol {
	case ProtocolSSH:
		return 22
	case ProtocolPostgres:
		return 5432
	case ProtocolMySQL:
		return 3306
	case ProtocolRedis:
		return 6379
	case ProtocolSMTP:
		switch {
		case tls:
			return 465
		case startTLS:
			return 587
		}
		return 25
	case ProtocolIMAP:
		if tls {
			return 993
		}
		return 143
	case ProtocolMinecraft:
		return 25565
	case ProtocolA2S:
		return 27015
	}
	return 0
}

// Limits for check options, enforced by Validate.
const (
	MaxCheckTextLen = 500
	// MaxBannerLen bounds an expected banner; probes read at most 1 KiB of it.
	MaxBannerLen = 256
)

// DNS record types a dns monitor can query. Empty resolves A and AAAA records.
var DNSRecordTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV"}

// DNS match modes. MatchContains, the default, succeeds when a record
// contains the expected value; MatchEquals when a record equals it.
const (
	MatchContains = "contains"
	MatchEquals   = "equals"
)

// CheckOptions configures protocol-specific checks. Each protocol uses only
// some fields (see ForProtocol); the others must be empty.
//
//   - dns: RecordType, Expected, MatchMode
//   - tcp: Banner, TLS, IgnoreTLS
//   - ssh: Banner
//   - postgres: TLS, IgnoreTLS, Username, Password
//   - redis: TLS, IgnoreTLS, Username, Password
//   - smtp, imap: TLS, StartTLS, IgnoreTLS
//   - grpc: TLS, IgnoreTLS, Service
//
// Username and Password are stored encrypted by the hub.
type CheckOptions struct {
	// RecordType is the DNS record type to query; empty resolves A and AAAA.
	RecordType string `cbor:"0,keyasint,omitempty"`
	// Expected is a value one of the DNS records must match, per MatchMode.
	Expected  string `cbor:"1,keyasint,omitempty"`
	MatchMode string `cbor:"2,keyasint,omitempty"`
	// Banner must appear in the first bytes the server sends. For ssh it
	// replaces the default check of the SSH-2.0 identification.
	Banner string `cbor:"3,keyasint,omitempty"`
	// TLS connects with implicit TLS. For postgres it requests SSL first.
	TLS bool `cbor:"4,keyasint,omitempty"`
	// StartTLS upgrades smtp and imap connections with STARTTLS.
	StartTLS bool `cbor:"5,keyasint,omitempty"`
	// IgnoreTLS skips certificate verification.
	IgnoreTLS bool `cbor:"6,keyasint,omitempty"`
	// Username defaults to "postgres" for postgres; for redis it selects an ACL user.
	Username string `cbor:"7,keyasint,omitempty"`
	// Password authenticates postgres and redis checks when set.
	Password string `cbor:"8,keyasint,omitempty"`
	// Service is the gRPC health service name; empty checks the whole server.
	Service string `cbor:"9,keyasint,omitempty"`
}

// ForProtocol returns a copy with only the fields protocol uses, or nil when
// none is set. IgnoreTLS is kept only with TLS or StartTLS, and MatchMode
// only with Expected.
func (o *CheckOptions) ForProtocol(protocol string) *CheckOptions {
	if o == nil {
		return nil
	}
	var out CheckOptions
	switch protocol {
	case ProtocolDNS:
		out.RecordType = strings.ToUpper(strings.TrimSpace(o.RecordType))
		out.Expected = o.Expected
		if o.Expected != "" {
			out.MatchMode = o.MatchMode
			if out.MatchMode == MatchContains {
				out.MatchMode = ""
			}
		}
	case ProtocolTCP:
		out.Banner, out.TLS = o.Banner, o.TLS
	case ProtocolSSH:
		out.Banner = o.Banner
	case ProtocolPostgres, ProtocolRedis:
		out.TLS, out.Username, out.Password = o.TLS, o.Username, o.Password
	case ProtocolSMTP, ProtocolIMAP:
		out.TLS, out.StartTLS = o.TLS, o.StartTLS
	case ProtocolGRPC:
		out.TLS, out.Service = o.TLS, o.Service
	}
	if out.TLS || out.StartTLS {
		out.IgnoreTLS = o.IgnoreTLS
	}
	if reflect.ValueOf(out).IsZero() {
		return nil
	}
	return &out
}

// UsesTLS reports whether a check with these options connects with TLS.
func (o *CheckOptions) UsesTLS() bool {
	return o != nil && (o.TLS || o.StartTLS)
}

// Validate reports whether the options are well formed for protocol.
func (o *CheckOptions) Validate(protocol string) error {
	if o == nil {
		return nil
	}
	if !reflect.DeepEqual(o.ForProtocol(protocol), o) {
		return fmt.Errorf("unsupported check options for the %s protocol", protocol)
	}
	if o.RecordType != "" && !slices.Contains(DNSRecordTypes, o.RecordType) {
		return fmt.Errorf("unsupported record type %q", o.RecordType)
	}
	if o.MatchMode != "" && o.MatchMode != MatchContains && o.MatchMode != MatchEquals {
		return fmt.Errorf("unsupported match mode %q", o.MatchMode)
	}
	if o.TLS && o.StartTLS {
		return errors.New("TLS and STARTTLS cannot be combined")
	}
	if len(o.Banner) > MaxBannerLen {
		return fmt.Errorf("banner must be at most %d characters", MaxBannerLen)
	}
	for name, value := range map[string]string{
		"expected value": o.Expected,
		"banner":         o.Banner,
		"username":       o.Username,
		"password":       o.Password,
		"service":        o.Service,
	} {
		if len(value) > MaxCheckTextLen {
			return fmt.Errorf("%s must be at most %d characters", name, MaxCheckTextLen)
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%s contains invalid characters", name)
		}
	}
	return nil
}

// containerRefPattern matches Docker container names and IDs.
var containerRefPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)

// ValidContainerRef reports whether ref is a valid container name or ID.
func ValidContainerRef(ref string) bool {
	return containerRefPattern.MatchString(ref)
}

// validateTarget checks the target format of protocols with a plain host or
// container target.
func (c Config) validateTarget() error {
	switch {
	case c.Protocol == ProtocolDocker:
		if !ValidContainerRef(c.Target) {
			return errors.New("target must be a container name or ID")
		}
	case UsesPort(c.Protocol), c.Protocol == ProtocolDNS:
		if strings.Contains(c.Target, "://") || strings.ContainsAny(c.Target, "/ \t") {
			return errors.New("target must be a host name or IP address")
		}
	}
	return nil
}
