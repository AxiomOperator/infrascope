//go:build testing

package webpush

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/henrygd/beszel/internal/migrations"
	"github.com/pocketbase/pocketbase/core"
	pbtests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func b64(t *testing.T, value string) []byte {
	t.Helper()
	data, err := decodeBase64(strings.ReplaceAll(value, " ", ""))
	require.NoError(t, err)
	return data
}

// TestEncryptRFC8291 checks the encryption against RFC 8291 Section 5 and
// Appendix A.
func TestEncryptRFC8291(t *testing.T) {
	plaintext := b64(t, "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24")
	assert.Equal(t, "When I grow up, I want to be a watermelon", string(plaintext))
	asPrivate, err := ecdh.P256().NewPrivateKey(b64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	require.NoError(t, err)
	assert.Equal(t, b64(t, "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"), asPrivate.PublicKey().Bytes())
	uaPublic, authSecret, err := parseKeys(
		"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		"BTBZMqHH6r4Tts7J_aSIgg",
	)
	require.NoError(t, err)
	salt := b64(t, "DGv6ra1nlYgDCS1FRnbzlw")

	out, err := encrypt(plaintext, uaPublic, authSecret, asPrivate, salt)
	require.NoError(t, err)
	expected := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	assert.Equal(t, expected, base64.RawURLEncoding.EncodeToString(out))
	// Appendix A: 86 octet header, then the ciphertext
	assert.Equal(t, b64(t, "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z 9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml mlMoZIIgDll6e3vCYLocInmYWAmS6Tlz AC8wEqKK6PBru3jl7A8"), out[:headerSize])
	assert.Equal(t, b64(t, "8pfeW0KbunFT06SuDKoJH9Ql87S1QUrd irN6GcG7sFz1y1sqLgVi1VhjVkHsUoEs bI_0LpXMuGvnzQ"), out[headerSize:])

	// The user agent decrypts it.
	uaPrivate, err := ecdh.P256().NewPrivateKey(b64(t, "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	require.NoError(t, err)
	assert.Equal(t, plaintext, decryptForTest(t, out, uaPrivate, authSecret))
}

func TestEncryptRandomized(t *testing.T) {
	uaPrivate, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	a, err := encrypt([]byte("hello"), uaPrivate.PublicKey(), secret, nil, nil)
	require.NoError(t, err)
	b, err := encrypt([]byte("hello"), uaPrivate.PublicKey(), secret, nil, nil)
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "fresh salt and key per message")
	assert.Equal(t, []byte("hello"), decryptForTest(t, a, uaPrivate, secret))
	_, err = encrypt(make([]byte, recordSize), uaPrivate.PublicKey(), secret, nil, nil)
	assert.Error(t, err)
}

func TestParseKeys(t *testing.T) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	raw := key.PublicKey().Bytes()
	auth := make([]byte, 16)
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding} {
		_, _, err := parseKeys(enc.EncodeToString(raw), enc.EncodeToString(auth))
		assert.NoError(t, err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	bad := append([]byte{}, raw...)
	bad[40] ^= 0xff // not on the curve
	for _, keys := range [][2]string{
		{enc(raw[:33]), enc(auth)},
		{enc(bad), enc(auth)},
		{enc(raw), enc(auth[:8])},
		{"!!", enc(auth)},
	} {
		_, _, err := parseKeys(keys[0], keys[1])
		assert.ErrorIs(t, err, errInvalidKeys, keys)
	}
}

// decryptForTest decrypts an aes128gcm push message as a user agent would.
func decryptForTest(t *testing.T, message []byte, uaPrivate *ecdh.PrivateKey, authSecret []byte) []byte {
	t.Helper()
	require.Greater(t, len(message), headerSize)
	salt := message[:16]
	assert.Equal(t, uint32(recordSize), binary.BigEndian.Uint32(message[16:20]))
	require.Equal(t, byte(65), message[20])
	asPublic, err := ecdh.P256().NewPublicKey(message[21:86])
	require.NoError(t, err)
	secret, err := uaPrivate.ECDH(asPublic)
	require.NoError(t, err)
	info := "WebPush: info\x00" + string(uaPrivate.PublicKey().Bytes()) + string(asPublic.Bytes())
	ikm, err := hkdf.Key(sha256.New, secret, authSecret, info, 32)
	require.NoError(t, err)
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	require.NoError(t, err)
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	require.NoError(t, err)
	block, err := aes.NewCipher(cek)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	plain, err := gcm.Open(nil, nonce, message[headerSize:], nil)
	require.NoError(t, err)
	require.Equal(t, byte(0x02), plain[len(plain)-1], "last record delimiter")
	return plain[:len(plain)-1]
}

// verifyVAPID parses and verifies an Authorization header and returns the claims.
func verifyVAPID(t *testing.T, header string) map[string]any {
	t.Helper()
	rest, ok := strings.CutPrefix(header, "vapid t=")
	require.True(t, ok, header)
	token, k, ok := strings.Cut(rest, ", k=")
	require.True(t, ok, header)
	rawKey, err := base64.RawURLEncoding.DecodeString(k)
	require.NoError(t, err)
	x, y := elliptic.Unmarshal(elliptic.P256(), rawKey) //nolint:staticcheck // test only
	require.NotNil(t, x)
	publicKey := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	assert.JSONEq(t, `{"typ":"JWT","alg":"ES256"}`, string(headerJSON))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	require.Len(t, signature, 64)
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])
	require.True(t, ecdsa.Verify(publicKey, digest[:], r, s), "valid ES256 signature")
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(claimsJSON, &claims))
	return claims
}

func newTestApp(t *testing.T) *pbtests.TestApp {
	t.Helper()
	app, err := pbtests.NewTestApp(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)
	return app
}

func TestVAPIDKeyPersistsAndSigns(t *testing.T) {
	app := newTestApp(t)
	first, err := New(app)
	require.NoError(t, err)
	assert.True(t, first.Enabled())
	info, err := os.Stat(filepath.Join(app.DataDir(), KeyFileName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	second, err := New(app)
	require.NoError(t, err)
	assert.Equal(t, first.PublicKey(), second.PublicKey(), "same key after restart")
	raw, err := base64.RawURLEncoding.DecodeString(first.PublicKey())
	require.NoError(t, err)
	assert.Len(t, raw, 65)

	now := time.Unix(1_800_000_000, 0)
	header, err := authorization(first.key, first.publicKey, "https://push.example.com:8443/send/abc?x=1", "mailto:ops@example.com", now)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(header, ", k="+first.PublicKey()))
	claims := verifyVAPID(t, header)
	assert.Equal(t, "https://push.example.com:8443", claims["aud"])
	assert.Equal(t, "mailto:ops@example.com", claims["sub"])
	assert.EqualValues(t, now.Add(12*time.Hour).Unix(), claims["exp"])

	// a corrupt key file is an error, not silently replaced
	require.NoError(t, os.WriteFile(filepath.Join(app.DataDir(), KeyFileName), []byte("junk"), 0o600))
	_, err = New(app)
	assert.Error(t, err)
	var disabled *Service
	assert.False(t, disabled.Enabled())
	_, err = disabled.Send(context.Background(), "u", Message{})
	assert.ErrorIs(t, err, ErrDisabled)
}

func TestDefaultSubject(t *testing.T) {
	app := newTestApp(t)
	s, err := New(app)
	require.NoError(t, err)
	settings := app.Settings()
	settings.Meta.SenderAddress = ""
	settings.Meta.AppURL = "http://localhost:8090"
	assert.Equal(t, "mailto:admin@localhost", s.subject())
	settings.Meta.AppURL = "https://hub.example.com"
	assert.Equal(t, "https://hub.example.com", s.subject())
	settings.Meta.SenderAddress = "alerts@example.com"
	assert.Equal(t, "mailto:alerts@example.com", s.subject())
}

func TestBuildPayload(t *testing.T) {
	data, err := buildPayload(Message{Title: "Down", Body: "api is down", URL: "/system/x", Tag: "t", Urgent: true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"title":"Down","body":"api is down","url":"/system/x","tag":"t","urgent":true}`, string(data))

	for _, body := range []string{strings.Repeat("a", 10000), strings.Repeat("é<\"", 3000)} {
		data, err = buildPayload(Message{Title: "T", Body: body})
		require.NoError(t, err)
		assert.LessOrEqual(t, len(data), MaxPayloadSize)
		var decoded payload
		require.NoError(t, json.Unmarshal(data, &decoded))
		assert.True(t, strings.HasSuffix(decoded.Body, "…"))
		assert.Greater(t, len(decoded.Body), 1000)
	}
}

func TestValidateEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/abc",
		"https://web.push.apple.com/abc",
		"https://8.8.8.8/push",
	} {
		assert.NoError(t, ValidateEndpoint(endpoint), endpoint)
	}
	for _, endpoint := range []string{
		"http://fcm.googleapis.com/fcm/send/abc",
		"https://localhost/push",
		"https://127.0.0.1/push",
		"https://10.0.0.5/push",
		"https://192.168.1.1/push",
		"https://169.254.169.254/latest",
		"https://[::1]/push",
		"https://[fe80::1]/push",
		"https://[::ffff:127.0.0.1]/push",
		"https://100.64.0.1/push",
		"https://0.0.0.0/push",
		"https://router.local/push",
		"https://metadata.internal/push",
		"https://intranet/push",
		"https://user:pass@push.example.com/x",
		"https://push.example.com/" + strings.Repeat("a", MaxEndpointLength),
		"javascript:alert(1)",
		"",
	} {
		assert.ErrorIs(t, ValidateEndpoint(endpoint), ErrInvalidEndpoint, endpoint)
	}
	assert.Error(t, checkAddress("127.0.0.1:443"))
	assert.Error(t, checkAddress("[::1]:443"))
	assert.Error(t, checkAddress("10.1.2.3:443"))
	assert.NoError(t, checkAddress("1.1.1.1:443"))
}

// testDevice is a browser subscription with its private keys.
type testDevice struct {
	key    *ecdh.PrivateKey
	secret []byte
	record *core.Record
}

func addDevice(t *testing.T, app core.App, userID, endpoint string) *testDevice {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	collection, err := app.FindCollectionByNameOrId(Collection)
	require.NoError(t, err)
	record := core.NewRecord(collection)
	record.Set("user", userID)
	record.Set("endpoint", endpoint)
	record.Set("p256dh", base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()))
	record.Set("auth", base64.RawURLEncoding.EncodeToString(secret))
	require.NoError(t, app.Save(record))
	return &testDevice{key: key, secret: secret, record: record}
}

func addUser(t *testing.T, app core.App, email string) *core.Record {
	t.Helper()
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	user := core.NewRecord(users)
	user.Set("email", email)
	user.Set("password", "testtesttest")
	require.NoError(t, app.Save(user))
	return user
}

type pushRequest struct {
	path   string
	header http.Header
	body   []byte
}

// pushServer is a fake push service answering with the status for each path.
type pushServer struct {
	*httptest.Server
	mu       sync.Mutex
	statuses map[string]int
	requests []pushRequest
}

func newPushServer(t *testing.T) *pushServer {
	t.Helper()
	ps := &pushServer{statuses: map[string]int{}}
	ps.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ps.mu.Lock()
		defer ps.mu.Unlock()
		ps.requests = append(ps.requests, pushRequest{path: r.URL.Path, header: r.Header.Clone(), body: body})
		status, ok := ps.statuses[r.URL.Path]
		if !ok {
			status = http.StatusCreated
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(ps.Close)
	return ps
}

func (ps *pushServer) byPath(path string) []pushRequest {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var out []pushRequest
	for _, request := range ps.requests {
		if request.path == path {
			out = append(out, request)
		}
	}
	return out
}

func TestSendFlow(t *testing.T) {
	app := newTestApp(t)
	server := newPushServer(t)
	server.statuses["/gone"] = http.StatusGone
	server.statuses["/missing"] = http.StatusNotFound
	server.statuses["/busy"] = http.StatusTooManyRequests
	server.statuses["/broken"] = http.StatusInternalServerError
	s, err := New(app, WithHTTPClient(server.Client()), WithSubject(func() string { return "mailto:ops@example.com" }), WithTTL(600))
	require.NoError(t, err)

	user := addUser(t, app, "user@example.com")
	other := addUser(t, app, "other@example.com")
	ok := addDevice(t, app, user.Id, server.URL+"/ok")
	gone := addDevice(t, app, user.Id, server.URL+"/gone")
	missing := addDevice(t, app, user.Id, server.URL+"/missing")
	busy := addDevice(t, app, user.Id, server.URL+"/busy")
	broken := addDevice(t, app, user.Id, server.URL+"/broken")
	foreign := addDevice(t, app, other.Id, server.URL+"/other")

	msg := Message{Title: "api is down", Body: "HTTP 503", URL: "/monitors", Tag: "monitor-1", Urgent: true}
	sent, err := s.Send(context.Background(), user.Id, msg)
	require.NoError(t, err)
	assert.Equal(t, 1, sent)
	assert.Empty(t, server.byPath("/other"), "only the user's devices")

	requests := server.byPath("/ok")
	require.Len(t, requests, 1)
	header := requests[0].header
	assert.Equal(t, "aes128gcm", header.Get("Content-Encoding"))
	assert.Equal(t, "application/octet-stream", header.Get("Content-Type"))
	assert.Equal(t, "600", header.Get("TTL"))
	assert.Equal(t, "high", header.Get("Urgency"))
	claims := verifyVAPID(t, header.Get("Authorization"))
	assert.Equal(t, server.URL, claims["aud"])
	assert.Equal(t, "mailto:ops@example.com", claims["sub"])
	var got payload
	require.NoError(t, json.Unmarshal(decryptForTest(t, requests[0].body, ok.key, ok.secret), &got))
	assert.Equal(t, payload{Title: "api is down", Body: "HTTP 503", URL: "/monitors", Tag: "monitor-1", Urgent: true}, got)

	reload := func(d *testDevice) *core.Record {
		record, err := app.FindRecordById(Collection, d.record.Id)
		if err != nil {
			return nil
		}
		return record
	}
	okRecord := reload(ok)
	require.NotNil(t, okRecord)
	assert.False(t, okRecord.GetDateTime("lastSuccessAt").IsZero())
	assert.Nil(t, reload(gone), "410 deletes the subscription")
	assert.Nil(t, reload(missing), "404 deletes the subscription")
	assert.Equal(t, 1, reload(busy).GetInt("failures"))
	assert.Equal(t, 1, reload(broken).GetInt("failures"))
	assert.NotNil(t, reload(foreign))

	// normal urgency
	_, err = s.Send(context.Background(), user.Id, Message{Title: "back up"})
	require.NoError(t, err)
	requests = server.byPath("/ok")
	assert.Equal(t, "normal", requests[len(requests)-1].header.Get("Urgency"))
	assert.Equal(t, 2, reload(broken).GetInt("failures"))

	// a success resets the count
	server.mu.Lock()
	delete(server.statuses, "/busy")
	server.mu.Unlock()
	require.NoError(t, s.SendOne(context.Background(), user.Id, busy.record.Id, Message{Title: "x"}))
	assert.Zero(t, reload(busy).GetInt("failures"))

	// consecutive failures delete the subscription
	for range MaxFailures - 2 {
		assert.Error(t, s.SendOne(context.Background(), user.Id, broken.record.Id, Message{Title: "x"}))
	}
	assert.Nil(t, reload(broken), "deleted after MaxFailures consecutive failures")

	// only the owner can target a subscription
	assert.ErrorIs(t, s.SendOne(context.Background(), user.Id, foreign.record.Id, Message{Title: "x"}), ErrNotFound)

	// all failed: an error
	server.mu.Lock()
	server.statuses["/ok"] = http.StatusServiceUnavailable
	server.statuses["/busy"] = http.StatusServiceUnavailable
	server.mu.Unlock()
	sent, err = s.Send(context.Background(), user.Id, Message{Title: "x"})
	assert.Zero(t, sent)
	assert.ErrorContains(t, err, "503")

	// no devices
	app.Delete(reload(ok))
	app.Delete(reload(busy))
	_, err = s.Send(context.Background(), user.Id, Message{Title: "x"})
	assert.ErrorIs(t, err, ErrNoSubscriptions)
}

func TestSendCanceled(t *testing.T) {
	app := newTestApp(t)
	server := newPushServer(t)
	s, err := New(app, WithHTTPClient(server.Client()))
	require.NoError(t, err)
	user := addUser(t, app, "user@example.com")
	device := addDevice(t, app, user.Id, server.URL+"/ok")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sent, err := s.Send(ctx, user.Id, Message{Title: "x"})
	assert.Zero(t, sent)
	assert.True(t, errors.Is(err, context.Canceled), err)
	record, err := app.FindRecordById(Collection, device.record.Id)
	require.NoError(t, err)
	assert.Zero(t, record.GetInt("failures"), "cancellation is not a failure")
}

func TestSendBlocksInternalEndpoints(t *testing.T) {
	app := newTestApp(t)
	server := newPushServer(t)
	// the default client refuses loopback addresses at connect time
	s, err := New(app)
	require.NoError(t, err)
	user := addUser(t, app, "user@example.com")
	device := addDevice(t, app, user.Id, server.URL+"/ok")
	sent, err := s.Send(context.Background(), user.Id, Message{Title: "x"})
	assert.Zero(t, sent)
	assert.ErrorIs(t, err, errInternalDestination)
	assert.Empty(t, server.byPath("/ok"))
	record, err := app.FindRecordById(Collection, device.record.Id)
	require.NoError(t, err)
	assert.Equal(t, 1, record.GetInt("failures"))

	// plain http endpoints are never used
	addDevice(t, app, user.Id, "http://push.example.com/x")
	require.NoError(t, app.Delete(record))
	_, err = s.Send(context.Background(), user.Id, Message{Title: "x"})
	assert.ErrorIs(t, err, ErrInvalidEndpoint)
}
