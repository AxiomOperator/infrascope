package webpush

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// MaxEndpointLength is the longest accepted push endpoint URL.
const MaxEndpointLength = 2048

var (
	// ErrInvalidEndpoint is returned for endpoints that are not public https URLs.
	ErrInvalidEndpoint = errors.New("push endpoint must be a public https URL")
	// errInternalDestination is returned by the dialer for internal addresses.
	errInternalDestination = errors.New("push endpoint resolves to an internal address")
)

// ValidateEndpoint checks that a push endpoint is an https URL whose host is
// not obviously internal. Any https host is allowed (browsers use their
// vendor's push service, which we can't enumerate), and addresses are checked
// again when connecting, so names that resolve to internal addresses are
// blocked as well.
func ValidateEndpoint(endpoint string) error {
	if len(endpoint) > MaxEndpointLength {
		return ErrInvalidEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Opaque != "" {
		return ErrInvalidEndpoint
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return ErrInvalidEndpoint
	}
	if addr, err := netip.ParseAddr(host); err == nil && !publicAddr(addr) {
		return ErrInvalidEndpoint
	}
	if !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		// single label names only resolve on internal networks
		return ErrInvalidEndpoint
	}
	return nil
}

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// publicAddr reports whether an address is a public unicast address.
func publicAddr(addr netip.Addr) bool {
	if addr.Zone() != "" {
		return false
	}
	addr = addr.Unmap()
	return addr.IsGlobalUnicast() && !addr.IsPrivate() && !addr.IsLoopback() &&
		!addr.IsLinkLocalUnicast() && !addr.IsMulticast() && !addr.IsUnspecified() &&
		!cgnatPrefix.Contains(addr)
}

// checkAddress is the dialer Control hook: it runs for every resolved address
// right before connecting.
func checkAddress(address string) error {
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil || !publicAddr(addrPort.Addr()) {
		return errInternalDestination
	}
	return nil
}

// newGuardedClient returns an HTTP client that refuses to connect to internal
// addresses and ignores proxy settings (a proxy would resolve the target itself
// and bypass the check).
func newGuardedClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error { return checkAddress(address) },
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConnsPerHost: 4,
		},
		// push services answer directly; never follow redirects elsewhere
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
