package netmon

import (
	"context"
	"errors"
	"fmt"
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
	// Cert is the leaf certificate of a TLS connection the probe made, which
	// replaces the monitor's certificate info. It is nil without TLS.
	Cert *monitor.CertInfo
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

// ProbeFunc performs one check of a monitor. It must honor cancellation and
// bound its execution time by config.ProbeTimeout().
type ProbeFunc func(ctx context.Context, config monitor.Config) Outcome

func networkMonitorProbe(httpProbe *httpProber, extra map[string]ProbeFunc) monitorProbe {
	return func(ctx context.Context, config monitor.Config) Outcome {
		if probe, ok := extra[config.Protocol]; ok {
			return probe(ctx, config)
		}
		timeout := config.ProbeTimeout()
		switch config.Protocol {
		case monitor.ProtocolICMP:
			return outcomeOf(monitorICMP(ctx, config.Target, timeout))
		case monitor.ProtocolTCP:
			if config.Check == nil {
				return outcomeOf(monitorTCP(ctx, config.Target, config.Port, timeout))
			}
			return probeTCP(ctx, config)
		case monitor.ProtocolHTTP:
			return httpProbe.probe(ctx, config)
		case monitor.ProtocolDNS:
			return probeDNS(ctx, config)
		case monitor.ProtocolSSH:
			return probeSSH(ctx, config)
		case monitor.ProtocolPostgres:
			return probePostgres(ctx, config)
		case monitor.ProtocolMySQL:
			return probeMySQL(ctx, config)
		case monitor.ProtocolRedis:
			return probeRedis(ctx, config)
		case monitor.ProtocolSMTP:
			return probeSMTP(ctx, config)
		case monitor.ProtocolIMAP:
			return probeIMAP(ctx, config)
		case monitor.ProtocolGRPC:
			return probeGRPC(ctx, config)
		case monitor.ProtocolMinecraft:
			return probeMinecraft(ctx, config)
		case monitor.ProtocolA2S:
			return probeA2S(ctx, config)
		case monitor.ProtocolDocker:
			return outcomeOf(-1, errors.New("docker checks require an agent with docker access"))
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
	conn, start, err := dialTCP(ctx, target, port)
	if err != nil {
		return -1, err
	}
	responseUs := time.Since(start).Microseconds()
	conn.Close()
	return responseUs, nil
}
