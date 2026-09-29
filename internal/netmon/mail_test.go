//go:build testing

package netmon

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testTLSConfig returns a server TLS config with a self-signed certificate.
func testTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Test CA"},
		Issuer:       pkix.Name{CommonName: "Test CA"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

// serveTCP runs handle for each connection to a local listener and returns its port.
func serveTCP(t *testing.T, handle func(net.Conn)) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				handle(conn)
			}()
		}
	}()
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}

// fakeSMTP serves an SMTP dialog; tlsConfig enables STARTTLS.
func fakeSMTP(greeting string, tlsConfig *tls.Config) func(net.Conn) {
	return func(conn net.Conn) {
		reader := bufio.NewReader(conn)
		_, _ = conn.Write([]byte(greeting))
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"):
				ext := ""
				if tlsConfig != nil {
					ext = "250-STARTTLS\r\n"
				}
				_, _ = conn.Write([]byte("250-mail.test greets you\r\n" + ext + "250 SIZE 1000\r\n"))
			case cmd == "STARTTLS":
				_, _ = conn.Write([]byte("220 go ahead\r\n"))
				tlsConn := tls.Server(conn, tlsConfig)
				if tlsConn.Handshake() != nil {
					return
				}
				conn, reader, tlsConfig = tlsConn, bufio.NewReader(tlsConn), nil
			case cmd == "QUIT":
				_, _ = conn.Write([]byte("221 bye\r\n"))
				return
			default:
				_, _ = conn.Write([]byte("500 unknown\r\n"))
			}
		}
	}
}

func mailConfig(protocol string, port uint16, check *monitor.CheckOptions) monitor.Config {
	return monitor.Config{ID: "m", Protocol: protocol, Target: "127.0.0.1", Port: port, Timeout: 2, Check: check}
}

func TestProbeSMTP(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		port := serveTCP(t, fakeSMTP("220-mail.test ESMTP\r\n220 ready\r\n", nil))
		out := probeSMTP(context.Background(), mailConfig(monitor.ProtocolSMTP, port, nil))
		require.NoError(t, out.Err)
		assert.GreaterOrEqual(t, out.ResponseUs, int64(0))
		assert.Nil(t, out.Cert)
	})
	t.Run("starttls", func(t *testing.T) {
		port := serveTCP(t, fakeSMTP("220 ready\r\n", testTLSConfig(t)))
		out := probeSMTP(context.Background(), mailConfig(monitor.ProtocolSMTP, port, &monitor.CheckOptions{StartTLS: true, IgnoreTLS: true}))
		require.NoError(t, out.Err)
		require.NotNil(t, out.Cert)
		assert.Equal(t, "Test CA", out.Cert.Issuer)
	})
	t.Run("starttls verifies certificate", func(t *testing.T) {
		port := serveTCP(t, fakeSMTP("220 ready\r\n", testTLSConfig(t)))
		out := probeSMTP(context.Background(), mailConfig(monitor.ProtocolSMTP, port, &monitor.CheckOptions{StartTLS: true}))
		require.Error(t, out.Err)
		assert.Contains(t, out.Err.Error(), "certificate")
	})
	t.Run("starttls unsupported", func(t *testing.T) {
		port := serveTCP(t, fakeSMTP("220 ready\r\n", nil))
		out := probeSMTP(context.Background(), mailConfig(monitor.ProtocolSMTP, port, &monitor.CheckOptions{StartTLS: true}))
		assert.ErrorIs(t, out.Err, errStartTLSUnsupported)
	})
	t.Run("implicit tls", func(t *testing.T) {
		tlsConfig := testTLSConfig(t)
		port := serveTCP(t, func(conn net.Conn) {
			fakeSMTP("220 ready\r\n", nil)(tls.Server(conn, tlsConfig))
		})
		out := probeSMTP(context.Background(), mailConfig(monitor.ProtocolSMTP, port, &monitor.CheckOptions{TLS: true, IgnoreTLS: true}))
		require.NoError(t, out.Err)
		assert.NotNil(t, out.Cert)
	})
	t.Run("rejected", func(t *testing.T) {
		port := serveTCP(t, fakeSMTP("554 no service\r\n", nil))
		out := probeSMTP(context.Background(), mailConfig(monitor.ProtocolSMTP, port, nil))
		assert.ErrorIs(t, out.Err, errMailRejected)
		assert.EqualValues(t, -1, out.ResponseUs)
	})
	t.Run("wrong service", func(t *testing.T) {
		port := serveTCP(t, func(conn net.Conn) { _, _ = conn.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n")) })
		out := probeSMTP(context.Background(), mailConfig(monitor.ProtocolSMTP, port, nil))
		assert.Error(t, out.Err)
	})
	t.Run("timeout", func(t *testing.T) {
		port := serveTCP(t, func(conn net.Conn) { time.Sleep(3 * time.Second) })
		config := mailConfig(monitor.ProtocolSMTP, port, nil)
		config.Timeout = 1
		out := probeSMTP(context.Background(), config)
		assert.ErrorIs(t, out.Err, errProbeTimeout)
	})
}

// fakeIMAP serves an IMAP dialog; tlsConfig enables STARTTLS.
func fakeIMAP(greeting string, tlsConfig *tls.Config) func(net.Conn) {
	return func(conn net.Conn) {
		reader := bufio.NewReader(conn)
		_, _ = conn.Write([]byte(greeting))
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			tag, cmd, _ := strings.Cut(strings.TrimSpace(line), " ")
			switch strings.ToUpper(cmd) {
			case "STARTTLS":
				if tlsConfig == nil {
					_, _ = conn.Write([]byte(tag + " BAD unsupported\r\n"))
					continue
				}
				_, _ = conn.Write([]byte(tag + " OK begin TLS\r\n"))
				tlsConn := tls.Server(conn, tlsConfig)
				if tlsConn.Handshake() != nil {
					return
				}
				conn, reader, tlsConfig = tlsConn, bufio.NewReader(tlsConn), nil
			case "LOGOUT":
				_, _ = conn.Write([]byte("* BYE logging out\r\n" + tag + " OK LOGOUT completed\r\n"))
				return
			default:
				_, _ = conn.Write([]byte(tag + " BAD unknown\r\n"))
			}
		}
	}
}

func TestProbeIMAP(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		port := serveTCP(t, fakeIMAP("* OK [CAPABILITY IMAP4rev1 STARTTLS] ready\r\n", nil))
		out := probeIMAP(context.Background(), mailConfig(monitor.ProtocolIMAP, port, nil))
		require.NoError(t, out.Err)
	})
	t.Run("preauth", func(t *testing.T) {
		port := serveTCP(t, fakeIMAP("* PREAUTH ready\r\n", nil))
		out := probeIMAP(context.Background(), mailConfig(monitor.ProtocolIMAP, port, nil))
		require.NoError(t, out.Err)
	})
	t.Run("bye", func(t *testing.T) {
		port := serveTCP(t, fakeIMAP("* BYE too many connections\r\n", nil))
		out := probeIMAP(context.Background(), mailConfig(monitor.ProtocolIMAP, port, nil))
		assert.ErrorIs(t, out.Err, errMailRejected)
	})
	t.Run("starttls", func(t *testing.T) {
		port := serveTCP(t, fakeIMAP("* OK ready\r\n", testTLSConfig(t)))
		out := probeIMAP(context.Background(), mailConfig(monitor.ProtocolIMAP, port, &monitor.CheckOptions{StartTLS: true, IgnoreTLS: true}))
		require.NoError(t, out.Err)
		assert.NotNil(t, out.Cert)
	})
	t.Run("starttls refused", func(t *testing.T) {
		port := serveTCP(t, fakeIMAP("* OK ready\r\n", nil))
		out := probeIMAP(context.Background(), mailConfig(monitor.ProtocolIMAP, port, &monitor.CheckOptions{StartTLS: true}))
		assert.ErrorIs(t, out.Err, errStartTLSFailed)
	})
	t.Run("wrong service", func(t *testing.T) {
		port := serveTCP(t, fakeSMTP("220 ready\r\n", nil))
		out := probeIMAP(context.Background(), mailConfig(monitor.ProtocolIMAP, port, nil))
		assert.ErrorIs(t, out.Err, errMailUnexpectedGreeting)
	})
	t.Run("timeout", func(t *testing.T) {
		port := serveTCP(t, func(conn net.Conn) { time.Sleep(3 * time.Second) })
		config := mailConfig(monitor.ProtocolIMAP, port, nil)
		config.Timeout = 1
		out := probeIMAP(context.Background(), config)
		assert.ErrorIs(t, out.Err, errProbeTimeout)
	})
}
