package netmon

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/md5"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// svcListen serves each accepted connection with handle and returns the
// listener's port on 127.0.0.1.
func svcListen(t *testing.T, handle func(net.Conn)) uint16 {
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

// svcSilent accepts connections and never sends anything.
func svcSilent(conn net.Conn) { _, _ = io.Copy(io.Discard, conn) }

// svcTLSConfig returns a server TLS config with a self-signed certificate
// expiring at notAfter.
func svcTLSConfig(t *testing.T, notAfter time.Time) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "svc-test"},
		Issuer:       pkix.Name{CommonName: "svc-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

func svcConfig(protocol string, port uint16, check *monitor.CheckOptions) monitor.Config {
	return monitor.Config{ID: "m1", Protocol: protocol, Target: "127.0.0.1", Port: port, Timeout: 2, Check: check}
}

func requireSuccess(t *testing.T, out Outcome) {
	t.Helper()
	require.NoError(t, out.Err)
	assert.GreaterOrEqual(t, out.ResponseUs, int64(0))
}

func requireFailure(t *testing.T, out Outcome, message string) {
	t.Helper()
	require.Error(t, out.Err)
	assert.Equal(t, message, out.Err.Error())
	assert.EqualValues(t, -1, out.ResponseUs)
}

func TestProbeTCPBannerAndTLS(t *testing.T) {
	greeter := svcListen(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("220 ftp.example ready\r\n"))
		_, _ = io.Copy(io.Discard, conn)
	})
	requireSuccess(t, probeTCP(t.Context(), svcConfig("tcp", greeter, &monitor.CheckOptions{Banner: "220 ftp"})))
	requireFailure(t, probeTCP(t.Context(), svcConfig("tcp", greeter, &monitor.CheckOptions{Banner: "SSH-"})), "banner not found")

	silent := svcListen(t, svcSilent)
	config := svcConfig("tcp", silent, &monitor.CheckOptions{Banner: "x"})
	config.Timeout = 1
	requireFailure(t, probeTCP(t.Context(), config), "timeout")

	expires := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	tlsConfig := svcTLSConfig(t, expires)
	secure := svcListen(t, func(conn net.Conn) {
		tlsConn := tls.Server(conn, tlsConfig)
		if tlsConn.Handshake() == nil {
			_, _ = tlsConn.Write([]byte("hello\n"))
		}
	})
	out := probeTCP(t.Context(), svcConfig("tcp", secure, &monitor.CheckOptions{TLS: true, IgnoreTLS: true, Banner: "hello"}))
	requireSuccess(t, out)
	require.NotNil(t, out.Cert)
	assert.Equal(t, expires.UnixMilli(), out.Cert.Expires)
	assert.Equal(t, "svc-test", out.Cert.Issuer)

	out = probeTCP(t.Context(), svcConfig("tcp", secure, &monitor.CheckOptions{TLS: true}))
	require.Error(t, out.Err, "self-signed certificates fail verification")
	assert.Contains(t, out.Err.Error(), "certificate")
}

func TestProbeSSH(t *testing.T) {
	server := func(lines ...string) uint16 {
		return svcListen(t, func(conn net.Conn) {
			for _, line := range lines {
				_, _ = conn.Write([]byte(line + "\r\n"))
			}
			_, _ = io.Copy(io.Discard, conn)
		})
	}
	requireSuccess(t, probeSSH(t.Context(), svcConfig("ssh", server("SSH-2.0-OpenSSH_9.6"), nil)))
	requireSuccess(t, probeSSH(t.Context(), svcConfig("ssh", server("welcome", "SSH-2.0-OpenSSH_9.6"), nil)))
	requireSuccess(t, probeSSH(t.Context(), svcConfig("ssh", server("SSH-2.0-OpenSSH_9.6"), &monitor.CheckOptions{Banner: "OpenSSH"})))
	requireFailure(t, probeSSH(t.Context(), svcConfig("ssh", server("SSH-2.0-dropbear"), &monitor.CheckOptions{Banner: "OpenSSH"})), "banner not found")
	requireFailure(t, probeSSH(t.Context(), svcConfig("ssh", server("SSH-1.5-old"), nil)), "unsupported ssh protocol version")

	http := svcListen(t, func(conn net.Conn) { _, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n")) })
	requireFailure(t, probeSSH(t.Context(), svcConfig("ssh", http, nil)), "not an ssh server")

	config := svcConfig("ssh", svcListen(t, svcSilent), nil)
	config.Timeout = 1
	requireFailure(t, probeSSH(t.Context(), config), "timeout")
}

// ---------- PostgreSQL ----------

type fakePostgres struct {
	t        *testing.T
	auth     string // "trust", "md5", "scram", "cleartext"
	password string
	sslReply byte // 0 rejects SSL requests with 'N'
	tls      *tls.Config
	// failCode, if set, is sent as an error instead of an authentication request.
	failCode string
}

func (f fakePostgres) serve(conn net.Conn) {
	reader := bufio.NewReader(conn)
	readStartup := func() ([]byte, bool) {
		var length uint32
		if binary.Read(reader, binary.BigEndian, &length) != nil || length < 8 || length > 10000 {
			return nil, false
		}
		body := make([]byte, length-4)
		if _, err := io.ReadFull(reader, body); err != nil {
			return nil, false
		}
		return body, true
	}
	body, ok := readStartup()
	if !ok {
		return
	}
	if binary.BigEndian.Uint32(body) == postgresSSLRequest {
		if f.sslReply == 0 {
			_, _ = conn.Write([]byte{'N'})
			return
		}
		_, _ = conn.Write([]byte{f.sslReply})
		tlsConn := tls.Server(conn, f.tls)
		if tlsConn.Handshake() != nil {
			return
		}
		conn, reader = tlsConn, bufio.NewReader(tlsConn)
		if body, ok = readStartup(); !ok {
			return
		}
	}
	params := strings.Split(string(body[4:]), "\x00")
	user := ""
	for i := 0; i+1 < len(params); i += 2 {
		if params[i] == "user" {
			user = params[i+1]
		}
	}
	send := func(kind byte, payload []byte) {
		message := []byte{kind}
		message = binary.BigEndian.AppendUint32(message, uint32(4+len(payload)))
		_, _ = conn.Write(append(message, payload...))
	}
	sendAuth := func(code uint32, extra []byte) {
		send('R', append(binary.BigEndian.AppendUint32(nil, code), extra...))
	}
	sendError := func(code string) {
		send('E', []byte("SFATAL\x00C"+code+"\x00Mdenied\x00\x00"))
	}
	readPassword := func() []byte {
		var header [5]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil || header[0] != 'p' {
			return nil
		}
		payload := make([]byte, binary.BigEndian.Uint32(header[1:])-4)
		_, _ = io.ReadFull(reader, payload)
		return payload
	}
	if f.failCode != "" {
		sendError(f.failCode)
		return
	}
	switch f.auth {
	case "trust":
		sendAuth(0, nil)
	case "cleartext":
		sendAuth(3, nil)
		if string(readPassword()) != f.password+"\x00" {
			sendError("28P01")
			return
		}
		sendAuth(0, nil)
	case "md5":
		salt := []byte{1, 2, 3, 4}
		sendAuth(5, salt)
		inner := md5.Sum([]byte(f.password + user))
		outer := md5.Sum(append([]byte(hex.EncodeToString(inner[:])), salt...))
		if string(readPassword()) != "md5"+hex.EncodeToString(outer[:])+"\x00" {
			sendError("28P01")
			return
		}
		sendAuth(0, nil)
	case "scram":
		sendAuth(10, []byte("SCRAM-SHA-256\x00\x00"))
		initial := readPassword()
		mechanism, rest, _ := strings.Cut(string(initial), "\x00")
		if mechanism != "SCRAM-SHA-256" || len(rest) < 4 {
			sendError("08P01")
			return
		}
		clientFirst := rest[4:]
		clientBare := strings.TrimPrefix(clientFirst, "n,,")
		clientNonce := strings.TrimPrefix(clientBare, "n=,r=")
		salt := []byte("saltsalt")
		serverFirst := "r=" + clientNonce + "server,s=" + base64.StdEncoding.EncodeToString(salt) + ",i=4096"
		sendAuth(11, []byte(serverFirst))
		clientFinal := string(readPassword())
		withoutProof, proof, _ := strings.Cut(clientFinal, ",p=")
		salted, _ := pbkdf2.Key(sha256.New, f.password, salt, 4096, 32)
		authMessage := clientBare + "," + serverFirst + "," + withoutProof
		clientKey := hmacSHA256(salted, "Client Key")
		storedKey := sha256.Sum256(clientKey)
		signature := hmacSHA256(storedKey[:], authMessage)
		want := make([]byte, len(clientKey))
		for i := range clientKey {
			want[i] = clientKey[i] ^ signature[i]
		}
		got, _ := base64.StdEncoding.DecodeString(proof)
		if !hmac.Equal(got, want) {
			sendError("28P01")
			return
		}
		serverSignature := hmacSHA256(hmacSHA256(salted, "Server Key"), authMessage)
		sendAuth(12, []byte("v="+base64.StdEncoding.EncodeToString(serverSignature)))
		sendAuth(0, nil)
	}
	_, _ = io.Copy(io.Discard, reader)
}

func TestProbePostgres(t *testing.T) {
	serve := func(f fakePostgres) uint16 {
		f.t = t
		return svcListen(t, f.serve)
	}
	for _, auth := range []string{"trust", "cleartext", "md5", "scram"} {
		t.Run(auth, func(t *testing.T) {
			port := serve(fakePostgres{auth: auth, password: "s3cret"})
			// Without a password any authentication request succeeds.
			requireSuccess(t, probePostgres(t.Context(), svcConfig("postgres", port, nil)))
			requireSuccess(t, probePostgres(t.Context(), svcConfig("postgres", port, &monitor.CheckOptions{Username: "app", Password: "s3cret"})))
			if auth != "trust" {
				requireFailure(t, probePostgres(t.Context(), svcConfig("postgres", port, &monitor.CheckOptions{Username: "app", Password: "wrong"})), "authentication failed")
			}
		})
	}

	t.Run("error response proves postgres", func(t *testing.T) {
		port := serve(fakePostgres{failCode: "28000"})
		requireSuccess(t, probePostgres(t.Context(), svcConfig("postgres", port, nil)))
		requireFailure(t, probePostgres(t.Context(), svcConfig("postgres", port, &monitor.CheckOptions{Password: "x"})), "authentication failed")
		busy := serve(fakePostgres{failCode: "53300"})
		requireFailure(t, probePostgres(t.Context(), svcConfig("postgres", busy, &monitor.CheckOptions{Password: "x"})), "postgres error 53300")
	})

	t.Run("tls", func(t *testing.T) {
		port := serve(fakePostgres{auth: "trust", sslReply: 'S', tls: svcTLSConfig(t, time.Now().Add(time.Hour))})
		out := probePostgres(t.Context(), svcConfig("postgres", port, &monitor.CheckOptions{TLS: true, IgnoreTLS: true}))
		requireSuccess(t, out)
		assert.NotNil(t, out.Cert)
		noSSL := serve(fakePostgres{auth: "trust"})
		requireFailure(t, probePostgres(t.Context(), svcConfig("postgres", noSSL, &monitor.CheckOptions{TLS: true})), "server does not support TLS")
	})

	t.Run("wrong service", func(t *testing.T) {
		http := svcListen(t, func(conn net.Conn) { _, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n")) })
		requireFailure(t, probePostgres(t.Context(), svcConfig("postgres", http, nil)), "not a postgresql server")
		closed := svcListen(t, func(net.Conn) {})
		requireFailure(t, probePostgres(t.Context(), svcConfig("postgres", closed, nil)), "not a postgresql server")
	})

	t.Run("timeout", func(t *testing.T) {
		config := svcConfig("postgres", svcListen(t, svcSilent), nil)
		config.Timeout = 1
		requireFailure(t, probePostgres(t.Context(), config), "timeout")
	})
}

// ---------- MySQL ----------

func mysqlPacket(payload []byte) []byte {
	return append([]byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), 0}, payload...)
}

func TestProbeMySQL(t *testing.T) {
	serve := func(packet []byte) uint16 {
		return svcListen(t, func(conn net.Conn) {
			_, _ = conn.Write(packet)
			_, _ = io.Copy(io.Discard, conn)
		})
	}
	handshake := append([]byte{0x0a}, "8.0.36\x00"...)
	handshake = append(handshake, 1, 0, 0, 0)                            // connection id
	handshake = append(handshake, []byte("abcdefgh")...)                 // auth plugin data part 1
	handshake = append(handshake, 0, 0xff, 0xf7, 0x21, 0x02, 0x00, 0xff) // filler and more
	requireSuccess(t, probeMySQL(t.Context(), svcConfig("mysql", serve(mysqlPacket(handshake)), nil)))

	tooMany := append([]byte{0xff, 0x10, 0x04}, "Too many connections"...)
	requireFailure(t, probeMySQL(t.Context(), svcConfig("mysql", serve(mysqlPacket(tooMany)), nil)), "server error 1040")
	requireFailure(t, probeMySQL(t.Context(), svcConfig("mysql", serve([]byte("SSH-2.0-OpenSSH\r\n")), nil)), "not a mysql server")
	requireFailure(t, probeMySQL(t.Context(), svcConfig("mysql", serve(mysqlPacket(handshake[:5])), nil)), "not a mysql server")

	config := svcConfig("mysql", svcListen(t, svcSilent), nil)
	config.Timeout = 1
	requireFailure(t, probeMySQL(t.Context(), config), "timeout")
}

// ---------- Redis ----------

// fakeRedis answers AUTH and PING like a Redis server requiring password
// (and user, when set). Without a password it answers PING directly.
func fakeRedis(user, password string) func(net.Conn) {
	return func(conn net.Conn) {
		reader := bufio.NewReader(conn)
		authed := password == ""
		for {
			args, err := readRESPArray(reader)
			if err != nil {
				return
			}
			switch strings.ToUpper(args[0]) {
			case "AUTH":
				gotUser, gotPass := "default", args[len(args)-1]
				if len(args) == 3 {
					gotUser = args[1]
				}
				wantUser := user
				if wantUser == "" {
					wantUser = "default"
				}
				if gotUser == wantUser && gotPass == password {
					authed = true
					_, _ = conn.Write([]byte("+OK\r\n"))
				} else {
					_, _ = conn.Write([]byte("-WRONGPASS invalid username-password pair\r\n"))
				}
			case "PING":
				if !authed {
					_, _ = conn.Write([]byte("-NOAUTH Authentication required.\r\n"))
					continue
				}
				_, _ = conn.Write([]byte("+PONG\r\n"))
			}
		}
	}
}

func readRESPArray(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	var count int
	if line[0] != '*' {
		return nil, io.ErrUnexpectedEOF
	}
	for _, c := range strings.TrimSpace(line[1:]) {
		count = count*10 + int(c-'0')
	}
	args := make([]string, 0, count)
	for range count {
		if _, err := reader.ReadString('\n'); err != nil {
			return nil, err
		}
		arg, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		args = append(args, strings.TrimSuffix(arg, "\r\n"))
	}
	return args, nil
}

func TestProbeRedis(t *testing.T) {
	open := svcListen(t, fakeRedis("", ""))
	requireSuccess(t, probeRedis(t.Context(), svcConfig("redis", open, nil)))

	protected := svcListen(t, fakeRedis("", "pa ss"))
	requireFailure(t, probeRedis(t.Context(), svcConfig("redis", protected, nil)), "authentication required")
	requireSuccess(t, probeRedis(t.Context(), svcConfig("redis", protected, &monitor.CheckOptions{Password: "pa ss"})))
	requireFailure(t, probeRedis(t.Context(), svcConfig("redis", protected, &monitor.CheckOptions{Password: "nope"})), "authentication failed")

	acl := svcListen(t, fakeRedis("monitor", "pw"))
	requireSuccess(t, probeRedis(t.Context(), svcConfig("redis", acl, &monitor.CheckOptions{Username: "monitor", Password: "pw"})))

	tlsConfig := svcTLSConfig(t, time.Now().Add(time.Hour))
	secure := svcListen(t, func(conn net.Conn) {
		tlsConn := tls.Server(conn, tlsConfig)
		if tlsConn.Handshake() == nil {
			fakeRedis("", "")(tlsConn)
		}
	})
	out := probeRedis(t.Context(), svcConfig("redis", secure, &monitor.CheckOptions{TLS: true, IgnoreTLS: true}))
	requireSuccess(t, out)
	assert.NotNil(t, out.Cert)

	http := svcListen(t, func(conn net.Conn) {
		_, _ = bufio.NewReader(conn).ReadString('\n')
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
	})
	requireFailure(t, probeRedis(t.Context(), svcConfig("redis", http, nil)), "not a redis server")

	config := svcConfig("redis", svcListen(t, svcSilent), nil)
	config.Timeout = 1
	requireFailure(t, probeRedis(t.Context(), config), "timeout")
}

func TestRedisCommandEncoding(t *testing.T) {
	assert.Equal(t, "*2\r\n$4\r\nAUTH\r\n$5\r\na\r\nbc\r\n", string(redisCommand("AUTH", "a\r\nbc")))
}

// TestProbeCertReachesResults checks that certificate info seen by a probe is
// reported once in the default interval results and with immediate results.
func TestProbeCertReachesResults(t *testing.T) {
	cert := &monitor.CertInfo{Expires: 1234, Issuer: "ca"}
	pm := newManagerWithProbe(func(context.Context, monitor.Config) Outcome {
		return Outcome{ResponseUs: 5, Cert: cert}
	}, testDefaultIntervalMs)
	defer pm.Stop()
	config := monitor.Config{ID: "m1", Protocol: "tcp", Target: "127.0.0.1", Port: 1, Interval: 3600}
	result, err := pm.UpsertMonitor(config, true)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, cert, result.Cert)

	results := pm.GetResults(testDefaultIntervalMs)
	assert.Equal(t, cert, results["m1"].Cert, "new certificate info is reported once")
	results = pm.GetResults(testDefaultIntervalMs)
	assert.Nil(t, results["m1"].Cert)
}
