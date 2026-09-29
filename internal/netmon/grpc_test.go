//go:build testing

package netmon

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveH2C runs handler on a cleartext HTTP/2 (prior knowledge) server and returns its port.
func serveH2C(t *testing.T, handler http.HandlerFunc) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	protocols.SetHTTP1(true)
	server := &http.Server{Handler: handler, Protocols: protocols}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Close() })
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}

// fakeHealth answers health checks: services maps names to serving status;
// unknown services get grpc-status 5.
func fakeHealth(t *testing.T, services map[string]uint64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != grpcHealthPath || r.Header.Get("Content-Type") != "application/grpc" || r.ProtoMajor != 2 {
			http.Error(w, "not grpc", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		service := ""
		if len(body) > 5 && body[5] == 0x0a {
			size, n := binary.Uvarint(body[6:])
			service = string(body[6+n : 6+n+int(size)])
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		status, ok := services[service]
		if !ok {
			w.Header().Set("Grpc-Status", "5")
			w.WriteHeader(http.StatusOK)
			return
		}
		message := []byte{}
		if status != 0 {
			message = append([]byte{0x08}, binary.AppendUvarint(nil, status)...)
		}
		frame := make([]byte, 5)
		binary.BigEndian.PutUint32(frame[1:], uint32(len(message)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append(frame, message...))
		w.Header().Set("Grpc-Status", "0")
	}
}

func grpcConfig(port uint16, service string) monitor.Config {
	config := monitor.Config{ID: "g", Protocol: monitor.ProtocolGRPC, Target: "127.0.0.1", Port: port, Timeout: 2}
	if service != "" {
		config.Check = &monitor.CheckOptions{Service: service}
	}
	return config
}

func TestProbeGRPC(t *testing.T) {
	port := serveH2C(t, fakeHealth(t, map[string]uint64{"": 1, "down": 2, "unknown": 0}))

	t.Run("serving", func(t *testing.T) {
		out := probeGRPC(context.Background(), grpcConfig(port, ""))
		require.NoError(t, out.Err)
		assert.GreaterOrEqual(t, out.ResponseUs, int64(0))
	})
	t.Run("not serving", func(t *testing.T) {
		out := probeGRPC(context.Background(), grpcConfig(port, "down"))
		assert.ErrorIs(t, out.Err, errGRPCNotServing)
	})
	t.Run("unknown status", func(t *testing.T) {
		out := probeGRPC(context.Background(), grpcConfig(port, "unknown"))
		assert.ErrorIs(t, out.Err, errGRPCNotServing)
	})
	t.Run("service not found", func(t *testing.T) {
		out := probeGRPC(context.Background(), grpcConfig(port, "missing"))
		require.Error(t, out.Err)
		assert.Equal(t, "grpc status 5", out.Err.Error())
	})
	t.Run("not a grpc server", func(t *testing.T) {
		plain := serveH2C(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hello")) })
		out := probeGRPC(context.Background(), grpcConfig(plain, ""))
		assert.ErrorIs(t, out.Err, errGRPCInvalidResponse)
	})
	t.Run("http error", func(t *testing.T) {
		plain := serveH2C(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
		out := probeGRPC(context.Background(), grpcConfig(plain, ""))
		require.Error(t, out.Err)
		assert.Equal(t, "unexpected status 404", out.Err.Error())
		assert.EqualValues(t, 404, out.StatusCode)
	})
	t.Run("timeout", func(t *testing.T) {
		slow := serveH2C(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(3 * time.Second) })
		config := grpcConfig(slow, "")
		config.Timeout = 1
		out := probeGRPC(context.Background(), config)
		assert.ErrorIs(t, out.Err, errProbeTimeout)
	})
	t.Run("connection refused", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		closed, _ := strconv.Atoi(listener.Addr().(*net.TCPAddr).AddrPort().String()[len("127.0.0.1:"):])
		listener.Close()
		out := probeGRPC(context.Background(), grpcConfig(uint16(closed), ""))
		assert.Error(t, out.Err)
	})
}

func TestParseGRPCHealthResponse(t *testing.T) {
	frame := func(message ...byte) []byte {
		out := make([]byte, 5)
		binary.BigEndian.PutUint32(out[1:], uint32(len(message)))
		return append(out, message...)
	}
	status, err := parseGRPCHealthResponse(frame(0x08, 0x01))
	require.NoError(t, err)
	assert.EqualValues(t, 1, status)
	// unknown fields are skipped
	status, err = parseGRPCHealthResponse(frame(0x12, 0x01, 'x', 0x08, 0x02))
	require.NoError(t, err)
	assert.EqualValues(t, 2, status)
	status, err = parseGRPCHealthResponse(frame())
	require.NoError(t, err)
	assert.Zero(t, status)
	_, err = parseGRPCHealthResponse([]byte{1, 0, 0, 0, 0})
	assert.ErrorIs(t, err, errGRPCCompressed)
	_, err = parseGRPCHealthResponse([]byte{0, 0, 0, 0, 9, 1})
	assert.ErrorIs(t, err, errGRPCInvalidResponse)
	assert.Equal(t, []byte{0, 0, 0, 0, 5, 0x0a, 3, 'a', 'b', 'c'}, grpcHealthRequest("abc"))
	assert.Equal(t, []byte{0, 0, 0, 0, 0}, grpcHealthRequest(""))
}
