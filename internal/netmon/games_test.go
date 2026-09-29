//go:build testing

package netmon

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMinecraft reads the handshake and status request and replies with payload as the status string.
func fakeMinecraft(payload string) func(net.Conn) {
	return func(conn net.Conn) {
		reader := bufio.NewReader(conn)
		for range 2 {
			length, err := readVarInt(reader)
			if err != nil {
				return
			}
			if _, err := io.CopyN(io.Discard, reader, int64(length)); err != nil {
				return
			}
		}
		body := appendVarInt(nil, 0)
		body = appendVarInt(body, int32(len(payload)))
		body = append(body, payload...)
		_, _ = conn.Write(minecraftPacket(body))
	}
}

func TestProbeMinecraft(t *testing.T) {
	config := func(port uint16) monitor.Config {
		return monitor.Config{ID: "mc", Protocol: monitor.ProtocolMinecraft, Target: "127.0.0.1", Port: port, Timeout: 2}
	}
	t.Run("valid", func(t *testing.T) {
		port := serveTCP(t, fakeMinecraft(`{"version":{"name":"1.21","protocol":767},"players":{"online":3,"max":20},"description":"hi"}`))
		out := probeMinecraft(context.Background(), config(port))
		require.NoError(t, out.Err)
	})
	t.Run("json without version", func(t *testing.T) {
		port := serveTCP(t, fakeMinecraft(`{"players":{"online":3}}`))
		out := probeMinecraft(context.Background(), config(port))
		assert.ErrorIs(t, out.Err, errMinecraftInvalid)
	})
	t.Run("garbage", func(t *testing.T) {
		port := serveTCP(t, func(conn net.Conn) {
			// read the request first, so closing does not reset the connection
			_, _ = conn.Read(make([]byte, 512))
			_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.CloseWrite()
			}
			_, _ = io.Copy(io.Discard, conn)
		})
		out := probeMinecraft(context.Background(), config(port))
		assert.ErrorIs(t, out.Err, errMinecraftInvalid)
	})
	t.Run("timeout", func(t *testing.T) {
		port := serveTCP(t, func(conn net.Conn) { time.Sleep(3 * time.Second) })
		c := config(port)
		c.Timeout = 1
		out := probeMinecraft(context.Background(), c)
		assert.ErrorIs(t, out.Err, errProbeTimeout)
	})
}

func TestVarInt(t *testing.T) {
	for _, value := range []int32{0, 1, 127, 128, 25565, -1, 1 << 30} {
		encoded := appendVarInt(nil, value)
		decoded, err := readVarInt(bytes.NewReader(encoded))
		require.NoError(t, err)
		assert.Equal(t, value, decoded)
	}
	assert.Equal(t, []byte{0xff, 0xff, 0xff, 0xff, 0x0f}, appendVarInt(nil, -1))
}

// serveUDP answers each datagram with the replies returned by respond.
func serveUDP(t *testing.T, respond func(request []byte) [][]byte) uint16 {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			for _, reply := range respond(append([]byte(nil), buf[:n]...)) {
				_, _ = conn.WriteTo(reply, addr)
			}
		}
	}()
	return uint16(conn.LocalAddr().(*net.UDPAddr).Port)
}

var a2sInfoReply = append([]byte{0xff, 0xff, 0xff, 0xff, 'I', 17}, "My Server\x00de_dust2\x00csgo\x00Counter-Strike\x00\x00\x00\x10\x00"...)

func TestProbeA2S(t *testing.T) {
	config := func(port uint16) monitor.Config {
		return monitor.Config{ID: "a2s", Protocol: monitor.ProtocolA2S, Target: "127.0.0.1", Port: port, Timeout: 2}
	}
	t.Run("challenge", func(t *testing.T) {
		challenge := []byte{1, 2, 3, 4}
		var sawChallenge atomic.Bool
		port := serveUDP(t, func(request []byte) [][]byte {
			if !bytes.HasPrefix(request, a2sInfoQuery) {
				return nil
			}
			if bytes.Equal(request[len(a2sInfoQuery):], challenge) {
				sawChallenge.Store(true)
				return [][]byte{a2sInfoReply}
			}
			return [][]byte{append([]byte{0xff, 0xff, 0xff, 0xff, 'A'}, challenge...)}
		})
		out := probeA2S(context.Background(), config(port))
		require.NoError(t, out.Err)
		assert.True(t, sawChallenge.Load())
	})
	t.Run("direct info", func(t *testing.T) {
		port := serveUDP(t, func([]byte) [][]byte { return [][]byte{a2sInfoReply} })
		out := probeA2S(context.Background(), config(port))
		require.NoError(t, out.Err)
	})
	t.Run("endless challenges", func(t *testing.T) {
		port := serveUDP(t, func([]byte) [][]byte { return [][]byte{{0xff, 0xff, 0xff, 0xff, 'A', 9, 9, 9, 9}} })
		out := probeA2S(context.Background(), config(port))
		assert.ErrorIs(t, out.Err, errA2SInvalid)
	})
	t.Run("truncated info", func(t *testing.T) {
		port := serveUDP(t, func([]byte) [][]byte { return [][]byte{a2sInfoReply[:12]} })
		out := probeA2S(context.Background(), config(port))
		assert.ErrorIs(t, out.Err, errA2SInvalid)
	})
	t.Run("split", func(t *testing.T) {
		port := serveUDP(t, func([]byte) [][]byte { return [][]byte{{0xfe, 0xff, 0xff, 0xff, 1, 2, 3}} })
		out := probeA2S(context.Background(), config(port))
		assert.ErrorIs(t, out.Err, errA2SSplit)
	})
	t.Run("garbage", func(t *testing.T) {
		port := serveUDP(t, func([]byte) [][]byte { return [][]byte{[]byte("hello")} })
		out := probeA2S(context.Background(), config(port))
		assert.ErrorIs(t, out.Err, errA2SInvalid)
	})
	t.Run("no reply", func(t *testing.T) {
		port := serveUDP(t, func([]byte) [][]byte { return nil })
		c := config(port)
		c.Timeout = 1
		out := probeA2S(context.Background(), c)
		assert.ErrorIs(t, out.Err, errProbeTimeout)
	})
}
