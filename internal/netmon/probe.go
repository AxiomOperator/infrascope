package netmon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/entities/monitor"
)

const (
	networkMonitorUserAgent = "Beszel-Agent/" + beszel.Version + " (+https://beszel.dev)"
	// maxCheckErrLen bounds error text stored in check events.
	maxCheckErrLen = 200
)

// Outcome is the result of a single probe or externally reported check.
type Outcome struct {
	// ResponseUs is the response time in microseconds. It is ignored when Err is set.
	ResponseUs int64
	// StatusCode is the HTTP status of the evaluated response, if one was received.
	StatusCode uint16
	// Err is set when the check failed.
	Err error
	// Keyword reports whether the configured keyword was found in the response
	// body, before KeywordInvert applies. It is nil when no keyword was checked.
	Keyword *bool
}

// outcomeOf converts a response time and error into an outcome.
func outcomeOf(responseUs int64, err error) Outcome {
	if err == nil && responseUs < 0 {
		err = errors.New("probe failed")
	}
	if err != nil {
		return Outcome{ResponseUs: -1, Err: err}
	}
	return Outcome{ResponseUs: responseUs}
}

// checkErrString returns error text short enough to store in a check event.
func checkErrString(err error) string {
	text := err.Error()
	if len(text) <= maxCheckErrLen {
		return text
	}
	return strings.ToValidUTF8(text[:maxCheckErrLen], "")
}

// monitorProbe performs one check. Failures are recorded as loss by the task runner.
// Implementations must honor cancellation and bound their execution time.
type monitorProbe func(context.Context, monitor.Config) Outcome

func networkMonitorProbe(httpProbe *httpProber) monitorProbe {
	return func(ctx context.Context, config monitor.Config) Outcome {
		timeout := config.ProbeTimeout()
		switch config.Protocol {
		case "icmp":
			return outcomeOf(monitorICMP(ctx, config.Target, timeout))
		case "tcp":
			return outcomeOf(monitorTCP(ctx, config.Target, config.Port, timeout))
		case "http":
			return httpProbe.probe(ctx, config)
		case "dns":
			return outcomeOf(monitorDNS(ctx, config.Target, config.Server, timeout))
		default:
			return outcomeOf(-1, fmt.Errorf("unknown monitor protocol: %s", config.Protocol))
		}
	}
}

// limitProbe allows at most cap(sem) probes to run at once. Waiting for a slot
// does not count toward the probe timeout.
func limitProbe(probe monitorProbe, sem chan struct{}) monitorProbe {
	return func(ctx context.Context, config monitor.Config) Outcome {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return outcomeOf(-1, ctx.Err())
		}
		defer func() { <-sem }()
		return probe(ctx, config)
	}
}

// monitorTCP measures connection establishment time, including address fallback
// but excluding DNS resolution.
// Returns -1 and an error on failure.
func monitorTCP(ctx context.Context, target string, port uint16, timeout time.Duration) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Resolve DNS first, outside the timing window but within the probe deadline.
	ips, err := net.DefaultResolver.LookupHost(ctx, target)
	if err != nil {
		return -1, err
	}
	if len(ips) == 0 {
		return -1, errors.New("no addresses resolved for TCP monitor")
	}
	portString := fmt.Sprintf("%d", port)
	deadline, _ := ctx.Deadline()

	// Share the remaining probe budget across addresses so an unresponsive
	// first address cannot consume all the time available for alternatives.
	start := time.Now()
	for i, ip := range ips {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		dialer := net.Dialer{Timeout: time.Until(deadline) / time.Duration(len(ips)-i)}
		var conn net.Conn
		conn, err = dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, portString))
		if err != nil {
			continue
		}
		responseUs := time.Since(start).Microseconds()
		conn.Close()
		return responseUs, nil
	}
	return -1, err
}

// monitorDNS measures DNS resolution response time in microseconds. If server is
// non-empty, the lookup is sent to that DNS server (host or host:port, default
// port 53) instead of the system resolver. Returns -1 and an error on failure.
func monitorDNS(ctx context.Context, target, server string, timeout time.Duration) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resolver := net.DefaultResolver
	if server != "" {
		resolver = dnsResolverForServer(server)
	}

	start := time.Now()
	ips, err := resolver.LookupHost(ctx, target)
	if err != nil {
		return -1, err
	}
	if len(ips) == 0 {
		return -1, errors.New("no addresses resolved")
	}
	return time.Since(start).Microseconds(), nil
}

// dnsResolverForServer builds a resolver that sends lookups to the given DNS
// server address instead of the system resolver. server may be a bare host or
// host:port; when no port is given, the standard DNS port 53 is used.
func dnsResolverForServer(server string) *net.Resolver {
	address := server
	if _, _, err := net.SplitHostPort(server); err != nil {
		address = net.JoinHostPort(server, "53")
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, address)
		},
	}
}
