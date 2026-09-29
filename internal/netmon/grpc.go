package netmon

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
)

const (
	grpcHealthPath = "/grpc.health.v1.Health/Check"
	// maxGRPCMessage bounds the health response read.
	maxGRPCMessage = 1 << 20
	// grpcServing is HealthCheckResponse.ServingStatus SERVING.
	grpcServing = 1
)

// Fixed failure messages of grpc checks.
var (
	errGRPCNotServing      = errors.New("not serving")
	errGRPCInvalidResponse = errors.New("invalid grpc response")
	errGRPCCompressed      = errors.New("compressed grpc response")
)

type grpcTransportKey struct {
	tls, ignoreTLS bool
}

// grpcTransports caches one HTTP/2 transport per TLS mode, so connections are
// reused rather than leaked across probes.
var grpcTransports sync.Map // grpcTransportKey -> *http.Transport

func grpcTransport(key grpcTransportKey) *http.Transport {
	if transport, ok := grpcTransports.Load(key); ok {
		return transport.(*http.Transport)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			host, portText, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			port, err := strconv.ParseUint(portText, 10, 16)
			if err != nil {
				return nil, err
			}
			conn, _, err := dialTCP(ctx, host, uint16(port))
			return conn, err
		},
		ForceAttemptHTTP2: true,
		IdleConnTimeout:   90 * time.Second,
		MaxIdleConns:      100,
	}
	protocols := new(http.Protocols)
	if key.tls {
		protocols.SetHTTP2(true)
		transport.TLSClientConfig = tlsClientConfig("", key.ignoreTLS)
	} else {
		protocols.SetUnencryptedHTTP2(true)
	}
	transport.Protocols = protocols
	actual, _ := grpcTransports.LoadOrStore(key, transport)
	return actual.(*http.Transport)
}

// grpcHealthRequest encodes a length-prefixed HealthCheckRequest message.
func grpcHealthRequest(service string) []byte {
	var message []byte
	if service != "" {
		message = append(message, 0x0a) // field 1, wire type 2
		message = binary.AppendUvarint(message, uint64(len(service)))
		message = append(message, service...)
	}
	frame := make([]byte, 5, 5+len(message))
	binary.BigEndian.PutUint32(frame[1:], uint32(len(message)))
	return append(frame, message...)
}

// probeGRPC calls the standard gRPC health check over HTTP/2, with TLS or
// cleartext (h2c prior knowledge), and requires the SERVING status.
func probeGRPC(ctx context.Context, config monitor.Config) Outcome {
	ctx, cancel := context.WithTimeout(ctx, config.ProbeTimeout())
	defer cancel()
	check := config.Check
	if check == nil {
		check = &monitor.CheckOptions{}
	}
	scheme := "http"
	if check.TLS {
		scheme = "https"
	}
	url := scheme + "://" + net.JoinHostPort(config.Target, strconv.Itoa(int(config.Port))) + grpcHealthPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(grpcHealthRequest(check.Service)))
	if err != nil {
		return outcomeOf(-1, err)
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	req.Header.Set("User-Agent", networkMonitorUserAgent)

	start := time.Now()
	resp, err := grpcTransport(grpcTransportKey{tls: check.TLS, ignoreTLS: check.IgnoreTLS}).RoundTrip(req)
	if err != nil {
		return outcomeOf(-1, grpcTimeoutErr(ctx, err))
	}
	defer resp.Body.Close()
	out := Outcome{StatusCode: uint16(resp.StatusCode)}
	if resp.TLS != nil {
		out.Cert = certInfoOf(*resp.TLS)
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.CopyN(io.Discard, resp.Body, maxHTTPBodyDrain)
		return out.fail(fmt.Errorf("unexpected status %d", resp.StatusCode))
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/grpc") {
		_, _ = io.CopyN(io.Discard, resp.Body, maxHTTPBodyDrain)
		return out.fail(errGRPCInvalidResponse)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGRPCMessage+5))
	if err != nil {
		return out.fail(grpcTimeoutErr(ctx, err))
	}
	// Trailers are complete once the body is read; trailers-only responses
	// carry the status in the headers.
	status := resp.Trailer.Get("Grpc-Status")
	if status == "" {
		status = resp.Header.Get("Grpc-Status")
	}
	if status != "" && status != "0" {
		code, err := strconv.Atoi(status)
		if err != nil {
			return out.fail(errGRPCInvalidResponse)
		}
		return out.fail(fmt.Errorf("grpc status %d", code))
	}
	serving, err := parseGRPCHealthResponse(body)
	if err != nil {
		return out.fail(err)
	}
	if serving != grpcServing {
		return out.fail(errGRPCNotServing)
	}
	out.ResponseUs = time.Since(start).Microseconds()
	return out
}

func grpcTimeoutErr(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errProbeTimeout
	}
	return err
}

// parseGRPCHealthResponse returns the status of a length-prefixed
// HealthCheckResponse; a missing status field is UNKNOWN (0).
func parseGRPCHealthResponse(body []byte) (uint64, error) {
	if len(body) < 5 {
		return 0, errGRPCInvalidResponse
	}
	if body[0] != 0 {
		return 0, errGRPCCompressed
	}
	length := binary.BigEndian.Uint32(body[1:5])
	if length > maxGRPCMessage || int(length) > len(body)-5 {
		return 0, errGRPCInvalidResponse
	}
	message := body[5 : 5+length]
	var status uint64
	for len(message) > 0 {
		tag, n := binary.Uvarint(message)
		if n <= 0 {
			return 0, errGRPCInvalidResponse
		}
		message = message[n:]
		field, wireType := tag>>3, tag&7
		switch wireType {
		case 0:
			value, n := binary.Uvarint(message)
			if n <= 0 {
				return 0, errGRPCInvalidResponse
			}
			message = message[n:]
			if field == 1 {
				status = value
			}
		case 1:
			if len(message) < 8 {
				return 0, errGRPCInvalidResponse
			}
			message = message[8:]
		case 2:
			size, n := binary.Uvarint(message)
			if n <= 0 || size > uint64(len(message)-n) {
				return 0, errGRPCInvalidResponse
			}
			message = message[n+int(size):]
		case 5:
			if len(message) < 4 {
				return 0, errGRPCInvalidResponse
			}
			message = message[4:]
		default:
			return 0, errGRPCInvalidResponse
		}
	}
	return status, nil
}
