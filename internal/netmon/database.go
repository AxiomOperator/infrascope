package netmon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"syscall"

	"github.com/henrygd/beszel/internal/entities/monitor"
)

// Fixed failure messages of database checks. Server messages are not
// included, since they may reveal configuration details.
var (
	errNotPostgres      = errors.New("not a postgresql server")
	errPostgresNoTLS    = errors.New("server does not support TLS")
	errAuthFailed       = errors.New("authentication failed")
	errAuthUnsupported  = errors.New("unsupported authentication method")
	errNotMySQL         = errors.New("not a mysql server")
	errMySQLVersion     = errors.New("unsupported mysql protocol version")
	errNotRedis         = errors.New("not a redis server")
	errRedisAuthNeeded  = errors.New("authentication required")
	errRedisUnexpected  = errors.New("unexpected reply")
	errRedisLoading     = errors.New("server is loading")
	errScramServerProof = errors.New("invalid server signature")
)

const (
	// maxDatabaseMessage bounds a single protocol message read from a server.
	maxDatabaseMessage = 64 << 10
	// maxScramIterations bounds the key derivation a server can request.
	maxScramIterations = 1 << 20

	defaultPostgresUser = "postgres"
	postgresSSLRequest  = 80877103
	postgresProtocol3   = 196608
)

// ---------- PostgreSQL ----------

// probePostgres checks that the server speaks PostgreSQL: it sends a startup
// message and accepts an authentication request or an error response with
// a SQLSTATE code. With a password configured, it also authenticates
// (cleartext, MD5 or SCRAM-SHA-256) and succeeds once login is accepted.
// With TLS, it requests SSL before the startup message.
func probePostgres(ctx context.Context, config monitor.Config) Outcome {
	check := config.Check
	if check == nil {
		check = &monitor.CheckOptions{}
	}
	user := check.Username
	if user == "" {
		user = defaultPostgresUser
	}
	return probeConn(ctx, config, false, func(s *connSession) error {
		if check.TLS {
			var request [8]byte
			binary.BigEndian.PutUint32(request[0:], 8)
			binary.BigEndian.PutUint32(request[4:], postgresSSLRequest)
			if err := s.write(request[:]); err != nil {
				return err
			}
			reply, err := s.reader.ReadByte()
			if err != nil {
				return notProtocol(err, errNotPostgres)
			}
			switch reply {
			case 'S':
				if s.reader.Buffered() > 0 {
					// Data sent before the handshake could be injected by a peer.
					return errNotPostgres
				}
				if err := s.upgradeTLS(); err != nil {
					return err
				}
			case 'N':
				return errPostgresNoTLS
			default:
				return errNotPostgres
			}
		}
		if err := s.write(postgresStartup(user)); err != nil {
			return err
		}
		auth := postgresAuth{user: user, password: check.Password}
		for {
			kind, body, err := readPostgresMessage(s)
			if err != nil {
				return err
			}
			switch kind {
			case 'R':
				done, err := auth.handle(s, body)
				if done || err != nil {
					return err
				}
			case 'E':
				code, ok := postgresErrorCode(body)
				if !ok {
					return errNotPostgres
				}
				// Without a password, any error with a SQLSTATE shows the
				// server speaks PostgreSQL.
				if check.Password == "" {
					return nil
				}
				if auth.started || strings.HasPrefix(code, "28") {
					return errAuthFailed
				}
				return fmt.Errorf("postgres error %s", code)
			case 'N':
				// Notices may precede the authentication request.
			default:
				return errNotPostgres
			}
		}
	})
}

// notProtocol maps a closed connection to a protocol mismatch error.
func notProtocol(err, mismatch error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
		return mismatch
	}
	return err
}

// postgresStartup encodes a protocol 3.0 startup message.
func postgresStartup(user string) []byte {
	var params bytes.Buffer
	for _, value := range []string{"user", user, "application_name", "infrascope"} {
		params.WriteString(value)
		params.WriteByte(0)
	}
	params.WriteByte(0)
	message := make([]byte, 8, 8+params.Len())
	binary.BigEndian.PutUint32(message[0:], uint32(8+params.Len()))
	binary.BigEndian.PutUint32(message[4:], postgresProtocol3)
	return append(message, params.Bytes()...)
}

// readPostgresMessage reads a typed backend message.
func readPostgresMessage(s *connSession) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(s.reader, header[:]); err != nil {
		return 0, nil, notProtocol(err, errNotPostgres)
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length < 4 || length > maxDatabaseMessage {
		return 0, nil, errNotPostgres
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(s.reader, body); err != nil {
		return 0, nil, notProtocol(err, errNotPostgres)
	}
	return header[0], body, nil
}

// writePostgresMessage sends a typed frontend message.
func writePostgresMessage(s *connSession, kind byte, body []byte) error {
	message := make([]byte, 5, 5+len(body))
	message[0] = kind
	binary.BigEndian.PutUint32(message[1:], uint32(4+len(body)))
	return s.write(append(message, body...))
}

// postgresErrorCode returns the SQLSTATE of an ErrorResponse body.
func postgresErrorCode(body []byte) (string, bool) {
	for len(body) > 0 && body[0] != 0 {
		field := body[0]
		end := bytes.IndexByte(body[1:], 0)
		if end < 0 {
			return "", false
		}
		value := string(body[1 : 1+end])
		if field == 'C' {
			return value, validSQLState(value)
		}
		body = body[end+2:]
	}
	return "", false
}

func validSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// postgresAuth runs the authentication exchange of a postgres check.
type postgresAuth struct {
	user, password string
	// started is set once credentials were sent.
	started bool
	scram   *scramClient
}

// handle processes an authentication message. It reports done when the
// check succeeded.
func (a *postgresAuth) handle(s *connSession, body []byte) (done bool, err error) {
	if len(body) < 4 {
		return false, errNotPostgres
	}
	code := binary.BigEndian.Uint32(body)
	body = body[4:]
	if code == 0 {
		return true, nil // AuthenticationOk
	}
	if a.password == "" {
		// The server asks for credentials, which shows it speaks PostgreSQL.
		switch code {
		case 2, 3, 5, 7, 8, 9, 10:
			return true, nil
		}
		return false, errNotPostgres
	}
	switch code {
	case 3: // cleartext
		a.started = true
		return false, writePostgresMessage(s, 'p', append([]byte(a.password), 0))
	case 5: // MD5
		if len(body) < 4 {
			return false, errNotPostgres
		}
		a.started = true
		inner := md5.Sum([]byte(a.password + a.user))
		outer := md5.Sum(append([]byte(hex.EncodeToString(inner[:])), body[:4]...))
		return false, writePostgresMessage(s, 'p', append([]byte("md5"+hex.EncodeToString(outer[:])), 0))
	case 10: // SASL
		if !bytes.Contains(body, []byte("SCRAM-SHA-256\x00")) {
			return false, errAuthUnsupported
		}
		a.started = true
		a.scram = newScramClient(a.password)
		first := a.scram.clientFirst()
		message := append([]byte("SCRAM-SHA-256\x00"), binary.BigEndian.AppendUint32(nil, uint32(len(first)))...)
		return false, writePostgresMessage(s, 'p', append(message, first...))
	case 11: // SASLContinue
		if a.scram == nil {
			return false, errNotPostgres
		}
		final, err := a.scram.clientFinal(string(body))
		if err != nil {
			return false, err
		}
		return false, writePostgresMessage(s, 'p', []byte(final))
	case 12: // SASLFinal
		if a.scram == nil {
			return false, errNotPostgres
		}
		return false, a.scram.verifyServerFinal(string(body))
	}
	return false, errAuthUnsupported
}

// scramClient implements the client side of SCRAM-SHA-256 (RFC 7677)
// without channel binding, as PostgreSQL uses it.
type scramClient struct {
	password    string
	nonce       string
	clientBare  string
	authMessage string
	salted      []byte
}

func newScramClient(password string) *scramClient {
	nonce := make([]byte, 18)
	_, _ = rand.Read(nonce)
	return &scramClient{password: password, nonce: base64.StdEncoding.EncodeToString(nonce)}
}

func (c *scramClient) clientFirst() string {
	// PostgreSQL takes the user name from the startup message.
	c.clientBare = "n=,r=" + c.nonce
	return "n,," + c.clientBare
}

func (c *scramClient) clientFinal(serverFirst string) (string, error) {
	var nonce, salt string
	iterations := 0
	for attr := range strings.SplitSeq(serverFirst, ",") {
		key, value, ok := strings.Cut(attr, "=")
		if !ok {
			continue
		}
		switch key {
		case "r":
			nonce = value
		case "s":
			salt = value
		case "i":
			iterations, _ = strconv.Atoi(value)
		}
	}
	saltBytes, err := base64.StdEncoding.DecodeString(salt)
	if err != nil || len(saltBytes) == 0 || !strings.HasPrefix(nonce, c.nonce) || len(nonce) == len(c.nonce) ||
		iterations < 1 || iterations > maxScramIterations {
		return "", errNotPostgres
	}
	c.salted, err = pbkdf2.Key(sha256.New, c.password, saltBytes, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	withoutProof := "c=biws,r=" + nonce
	c.authMessage = c.clientBare + "," + serverFirst + "," + withoutProof
	clientKey := hmacSHA256(c.salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	signature := hmacSHA256(storedKey[:], c.authMessage)
	proof := make([]byte, len(clientKey))
	for i := range clientKey {
		proof[i] = clientKey[i] ^ signature[i]
	}
	return withoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof), nil
}

func (c *scramClient) verifyServerFinal(serverFinal string) error {
	if c.salted == nil {
		return errNotPostgres
	}
	value, ok := strings.CutPrefix(serverFinal, "v=")
	if !ok {
		return errAuthFailed
	}
	got, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return errScramServerProof
	}
	want := hmacSHA256(hmacSHA256(c.salted, "Server Key"), c.authMessage)
	if !hmac.Equal(got, want) {
		return errScramServerProof
	}
	return nil
}

func hmacSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

// ---------- MySQL ----------

// probeMySQL reads the server's initial handshake packet and succeeds for a
// valid protocol 10 handshake. An error packet (e.g. host not allowed, too
// many connections) fails with its error code.
func probeMySQL(ctx context.Context, config monitor.Config) Outcome {
	return probeConn(ctx, config, false, func(s *connSession) error {
		var header [4]byte
		if _, err := io.ReadFull(s.reader, header[:]); err != nil {
			return notProtocol(err, errNotMySQL)
		}
		length := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
		if length == 0 || length > maxDatabaseMessage || header[3] != 0 {
			return errNotMySQL
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(s.reader, payload); err != nil {
			return notProtocol(err, errNotMySQL)
		}
		return parseMySQLHandshake(payload)
	})
}

// parseMySQLHandshake validates the payload of an initial handshake packet.
func parseMySQLHandshake(payload []byte) error {
	switch payload[0] {
	case 0x0a:
	case 0xff:
		if len(payload) < 3 {
			return errNotMySQL
		}
		return fmt.Errorf("server error %d", binary.LittleEndian.Uint16(payload[1:]))
	case 0x09:
		return errMySQLVersion
	default:
		return errNotMySQL
	}
	rest := payload[1:]
	end := bytes.IndexByte(rest, 0)
	if end <= 0 || end > 100 {
		return errNotMySQL
	}
	// connection id (4), auth plugin data part 1 (8) and a filler byte (0)
	rest = rest[end+1:]
	if len(rest) < 13 || rest[12] != 0 {
		return errNotMySQL
	}
	return nil
}

// ---------- Redis ----------

// probeRedis sends PING, authenticating first when a password is set, and
// expects PONG.
func probeRedis(ctx context.Context, config monitor.Config) Outcome {
	check := config.Check
	if check == nil {
		check = &monitor.CheckOptions{}
	}
	return probeConn(ctx, config, check.TLS, func(s *connSession) error {
		if check.Password != "" {
			args := []string{"AUTH", check.Password}
			if check.Username != "" {
				args = []string{"AUTH", check.Username, check.Password}
			}
			if err := s.write(redisCommand(args...)); err != nil {
				return err
			}
			reply, err := readRedisReply(s)
			if err != nil {
				return err
			}
			if reply != "+OK" {
				if strings.HasPrefix(reply, "-") {
					return errAuthFailed
				}
				return errNotRedis
			}
		}
		if err := s.write(redisCommand("PING")); err != nil {
			return err
		}
		reply, err := readRedisReply(s)
		if err != nil {
			return err
		}
		switch {
		case reply == "+PONG":
			return nil
		case strings.HasPrefix(reply, "-NOAUTH"):
			return errRedisAuthNeeded
		case strings.HasPrefix(reply, "-LOADING"):
			return errRedisLoading
		case strings.HasPrefix(reply, "-"):
			return errRedisUnexpected
		}
		return errNotRedis
	})
}

// redisCommand encodes a command as a RESP array of bulk strings.
func redisCommand(args ...string) []byte {
	var b bytes.Buffer
	b.WriteString("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, arg := range args {
		b.WriteString("$" + strconv.Itoa(len(arg)) + "\r\n" + arg + "\r\n")
	}
	return b.Bytes()
}

// readRedisReply reads a single-line reply.
func readRedisReply(s *connSession) (string, error) {
	line, err := readLine(s, 512)
	if err != nil {
		if errors.Is(err, errLineTooLong) {
			return "", errNotRedis
		}
		return "", notProtocol(err, errNotRedis)
	}
	return line, nil
}
