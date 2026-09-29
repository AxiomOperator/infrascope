package netmon

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
)

// dialTCP resolves host and connects to the first reachable address. DNS
// resolution happens before start, so the returned start time excludes it.
// The remaining time until the context deadline is shared across addresses,
// so an unresponsive first address cannot consume all the time available for
// alternatives. ctx must have a deadline.
func dialTCP(ctx context.Context, host string, port uint16) (net.Conn, time.Time, error) {
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(ips) == 0 {
		return nil, time.Time{}, errors.New("no addresses resolved")
	}
	portString := strconv.Itoa(int(port))
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(monitor.DefaultProbeTimeout)
	}
	start := time.Now()
	for i, ip := range ips {
		if err := ctx.Err(); err != nil {
			return nil, start, err
		}
		dialer := net.Dialer{Timeout: time.Until(deadline) / time.Duration(len(ips)-i)}
		var conn net.Conn
		conn, err = dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, portString))
		if err == nil {
			return conn, start, nil
		}
	}
	return nil, start, err
}

// tlsClientConfig returns the client TLS config of a check against host.
func tlsClientConfig(host string, ignoreTLS bool) *tls.Config {
	return &tls.Config{ServerName: host, InsecureSkipVerify: ignoreTLS}
}

// certInfoOf returns the leaf certificate details of a TLS connection.
func certInfoOf(state tls.ConnectionState) *monitor.CertInfo {
	if len(state.PeerCertificates) == 0 {
		return nil
	}
	leaf := state.PeerCertificates[0]
	return &monitor.CertInfo{Expires: leaf.NotAfter.UnixMilli(), Issuer: leaf.Issuer.CommonName}
}

// connSession is a connection of a check, which may be upgraded to TLS.
type connSession struct {
	ctx       context.Context
	conn      net.Conn
	host      string
	ignoreTLS bool
	// reader buffers conn; it is replaced when the connection is upgraded.
	reader *bufio.Reader
	// cert is set once a TLS handshake completed.
	cert *monitor.CertInfo
}

// upgradeTLS performs a client TLS handshake on the connection. Any data
// buffered from the plain connection is discarded.
func (s *connSession) upgradeTLS() error {
	tlsConn := tls.Client(s.conn, tlsClientConfig(s.host, s.ignoreTLS))
	if err := tlsConn.HandshakeContext(s.ctx); err != nil {
		return err
	}
	s.conn = tlsConn
	s.reader = bufio.NewReader(tlsConn)
	s.cert = certInfoOf(tlsConn.ConnectionState())
	return nil
}

// write sends data on the current connection.
func (s *connSession) write(data []byte) error {
	_, err := s.conn.Write(data)
	return err
}

// probeConn connects to the target, optionally with implicit TLS, and runs
// check on the connection within the probe timeout. The response time covers
// connecting until check returns, excluding DNS resolution. Certificate info
// of a completed TLS handshake is reported even when the check fails.
func probeConn(ctx context.Context, config monitor.Config, implicitTLS bool, check func(*connSession) error) Outcome {
	ctx, cancel := context.WithTimeout(ctx, config.ProbeTimeout())
	defer cancel()
	conn, start, err := dialTCP(ctx, config.Target, config.Port)
	if err != nil {
		return outcomeOf(-1, err)
	}
	defer conn.Close()
	// Closing the socket interrupts pending reads and writes on cancellation.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	session := &connSession{ctx: ctx, conn: conn, host: config.Target, reader: bufio.NewReader(conn)}
	if config.Check != nil {
		session.ignoreTLS = config.Check.IgnoreTLS
	}
	if implicitTLS {
		err = session.upgradeTLS()
	}
	if err == nil {
		err = check(session)
	}
	out := Outcome{ResponseUs: time.Since(start).Microseconds(), Cert: session.cert}
	if err != nil {
		// The context closes the connection at the deadline, so reads may fail
		// with a closed connection error instead of a timeout.
		if isTimeout(err) || errors.Is(err, net.ErrClosed) && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errProbeTimeout
		}
		return out.fail(err)
	}
	return out
}

// isTimeout reports whether err is a deadline error of a connection or context.
func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// errProbeTimeout reports a check that did not complete within the probe timeout.
var errProbeTimeout = errors.New("timeout")
