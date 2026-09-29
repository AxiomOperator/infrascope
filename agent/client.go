package agent

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/agent/utils"
	"github.com/henrygd/beszel/internal/common"

	"github.com/fxamacker/cbor/v2"
	"github.com/lxzan/gws"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

const (
	// Keep the connection alive long enough for a slow collection cycle to
	// finish before the hub considers the agent disconnected.
	wsDeadline = 120 * time.Second
	// hubNonceAuthFileName marks that a hub has signed a per-connection nonce.
	// From then on the agent refuses the replayable token-only signature.
	hubNonceAuthFileName = "hub-nonce-auth"
	// maxConcurrentSlowRequests bounds how many slow requests (see slowActions)
	// run at once off the WebSocket read loop.
	maxConcurrentSlowRequests = 4
	// slowRequestTimeout bounds a slow request, including the time spent
	// waiting for a free slot. It stays below wsDeadline.
	slowRequestTimeout = 90 * time.Second
)

// slowActions are hub requests that can take a long time (smartctl, D-Bus,
// Docker API, package managers). They run in their own goroutine so the
// WebSocket read loop keeps processing pings and GetData while they execute;
// otherwise a slow handler can exceed wsDeadline and drop the connection.
// Responses are routed by request id, so they may arrive out of order.
var slowActions = map[common.WebSocketAction]bool{
	common.GetContainerLogs:  true,
	common.GetContainerInfo:  true,
	common.GetSmartData:      true,
	common.GetSystemdInfo:    true,
	common.GetZfsData:        true,
	common.GetPackageUpdates: true,
}

// errNoHubURL is returned when HUB_URL is unset. This is not a failure
// condition: an agent configured with only a public key runs in SSH-only mode,
// where the hub dials the agent and no outbound WebSocket client is expected.
var errNoHubURL = errors.New("HUB_URL environment variable not set")

type caCertFileError struct {
	err error
}

func (e *caCertFileError) Error() string {
	return e.err.Error()
}

func (e *caCertFileError) Unwrap() error {
	return e.err
}

// WebSocketClient manages the WebSocket connection between the agent and hub.
// It handles authentication, message routing, and connection lifecycle management.
type WebSocketClient struct {
	gws.BuiltinEventHandler
	options            *gws.ClientOption                   // WebSocket client configuration options
	agent              *Agent                              // Reference to the parent agent
	Conn               *gws.Conn                           // Active WebSocket connection (guarded by mu)
	hubURL             *url.URL                            // Parsed hub URL for connection
	token              string                              // Authentication token for hub registration
	fingerprint        string                              // System fingerprint for identification
	hubRequest         *common.HubRequest[cbor.RawMessage] // Reusable request structure for message parsing
	lastConnectAttempt time.Time                           // Timestamp of last connection attempt (guarded by mu)
	hubVerified        atomic.Bool                         // Whether the hub on the current connection has been cryptographically verified
	nonce              string                              // Random per-connection value the hub must sign (guarded by mu)
	nonceAuthSeen      atomic.Bool                         // Whether a hub has signed a nonce, so token-only signatures are refused
	tlsConfig          *tls.Config                         // Optional TLS configuration with custom CA certificates

	// mu guards Conn, lastConnectAttempt and nonce, which are read from the
	// read loop, handler goroutines and the connection manager.
	mu sync.Mutex
	// connectMu serializes Connect so two attempts never race to replace Conn.
	connectMu sync.Mutex

	slowSlotsOnce sync.Once
	slowSlots     chan struct{} // semaphore bounding concurrent slow requests
}

// newWebSocketClient creates a new WebSocket client for the given agent.
// It reads configuration from environment variables and validates the hub URL.
func newWebSocketClient(agent *Agent) (client *WebSocketClient, err error) {
	hubURLStr, exists := utils.GetEnv("HUB_URL")
	if !exists {
		return nil, errNoHubURL
	}

	client = &WebSocketClient{}

	client.hubURL, err = url.Parse(hubURLStr)
	if err != nil || client.hubURL.Host == "" {
		return nil, fmt.Errorf("invalid HUB_URL %q: must include scheme and host (e.g. http://hub.example.com:8090)", hubURLStr)
	}
	// get registration token
	client.token, err = getToken()
	if err != nil {
		return nil, err
	}
	client.tlsConfig, err = getTLSConfig()
	if err != nil {
		return nil, err
	}

	client.agent = agent
	client.hubRequest = &common.HubRequest[cbor.RawMessage]{}
	client.fingerprint = agent.getFingerprint()

	return client, nil
}

// getToken returns the token for the WebSocket client.
// It first checks the TOKEN environment variable, then the TOKEN_FILE environment variable.
// If neither is set, it returns an error.
func getToken() (string, error) {
	// get token from env var
	token, _ := utils.GetEnv("TOKEN")
	if token != "" {
		return token, nil
	}
	// get token from file
	tokenFile, _ := utils.GetEnv("TOKEN_FILE")
	if tokenFile == "" {
		return "", errors.New("must set TOKEN or TOKEN_FILE")
	}
	tokenBytes, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", err
	}
	return parseTokenFile(string(tokenBytes), tokenFile)
}

// parseTokenFile reads a single token from TOKEN_FILE.
// Blank lines and comments are ignored. Multiple tokens are rejected because
// the agent supports only one outbound hub connection.
func parseTokenFile(contents, path string) (string, error) {
	var token string
	for line := range strings.Lines(contents) {
		line = strings.TrimSpace(line)
		if len(line) == 0 || strings.HasPrefix(line, "#") {
			continue
		}
		if token != "" {
			return "", fmt.Errorf("%s must contain a single token", path)
		}
		token = line
	}
	// An empty file keeps returning an empty token, as before: the caller decides
	// what to do about it.
	return token, nil
}

// getTLSConfig returns a TLS configuration containing the system certificate
// pool plus any certificates configured through CA_CERT_FILE. A nil config lets
// gws use Go's default TLS configuration and system roots.
func getTLSConfig() (*tls.Config, error) {
	caCertFile, _ := utils.GetEnv("CA_CERT_FILE")
	if caCertFile == "" {
		return nil, nil
	}

	caCertPEM, err := os.ReadFile(caCertFile)
	if err != nil {
		return nil, &caCertFileError{fmt.Errorf("read CA_CERT_FILE %q: %w", caCertFile, err)}
	}

	rootCAs, err := x509.SystemCertPool()
	if err != nil {
		return nil, &caCertFileError{fmt.Errorf("load system CA certificate pool: %w", err)}
	}
	if !rootCAs.AppendCertsFromPEM(caCertPEM) {
		return nil, &caCertFileError{fmt.Errorf("CA_CERT_FILE %q does not contain any valid PEM certificates", caCertFile)}
	}

	return &tls.Config{RootCAs: rootCAs}, nil
}

// getOptions returns the WebSocket client options, creating them if necessary.
// It configures the connection URL, TLS settings, and authentication headers.
func (client *WebSocketClient) getOptions() *gws.ClientOption {
	if client.options != nil {
		return client.options
	}

	// update the hub url to use websocket scheme and api path
	if client.hubURL.Scheme == "https" {
		client.hubURL.Scheme = "wss"
	} else {
		client.hubURL.Scheme = "ws"
	}
	client.hubURL.Path = path.Join(client.hubURL.Path, "api/beszel/agent-connect")

	// make sure BESZEL_AGENT_ALL_PROXY works (GWS only checks ALL_PROXY)
	if val := os.Getenv("BESZEL_AGENT_ALL_PROXY"); val != "" {
		os.Setenv("ALL_PROXY", val)
	}

	client.options = &gws.ClientOption{
		Addr:      client.hubURL.String(),
		TlsConfig: client.tlsConfig,
		RequestHeader: http.Header{
			"User-Agent": []string{getUserAgent()},
			"X-Token":    []string{client.token},
			"X-Beszel":   []string{beszel.Version},
		},
		NewDialer: func() (gws.Dialer, error) {
			return proxy.FromEnvironment(), nil
		},
	}
	return client.options
}

// Connect establishes a WebSocket connection to the hub.
// It closes any existing connection before attempting to reconnect.
func (client *WebSocketClient) Connect() (err error) {
	client.connectMu.Lock()
	defer client.connectMu.Unlock()

	client.mu.Lock()
	client.lastConnectAttempt = time.Now()
	client.mu.Unlock()

	// make sure previous connection is closed
	client.Close()

	// every connection must prove the hub's identity again
	client.hubVerified.Store(false)
	nonceBytes := make([]byte, common.HubAuthNonceSize)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	nonce := hex.EncodeToString(nonceBytes)
	client.mu.Lock()
	client.nonce = nonce
	client.mu.Unlock()
	options := client.getOptions()
	options.RequestHeader.Set(common.HubAuthNonceHeader, nonce)

	conn, _, err := gws.NewClient(client, options)
	if err != nil {
		return err
	}
	client.mu.Lock()
	client.Conn = conn
	client.mu.Unlock()

	go conn.ReadLoop()

	return nil
}

// lastAttempt returns the time of the last connection attempt.
func (client *WebSocketClient) lastAttempt() time.Time {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.lastConnectAttempt
}

// currentConn returns the active connection, or nil.
func (client *WebSocketClient) currentConn() *gws.Conn {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.Conn
}

// currentNonce returns the nonce of the current connection attempt.
func (client *WebSocketClient) currentNonce() string {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.nonce
}

// OnOpen handles WebSocket connection establishment.
// It sets a deadline for the connection to prevent hanging.
func (client *WebSocketClient) OnOpen(conn *gws.Conn) {
	conn.SetDeadline(time.Now().Add(wsDeadline))
}

// OnClose handles WebSocket connection closure.
// It logs the closure reason and notifies the connection manager.
func (client *WebSocketClient) OnClose(conn *gws.Conn, err error) {
	if err != nil {
		slog.Warn("Connection closed", "err", strings.TrimPrefix(err.Error(), "gws: "))
	}
	client.agent.connectionManager.eventChan <- WebSocketDisconnect
}

// OnMessage handles incoming WebSocket messages from the hub.
// It decodes CBOR messages and routes them to appropriate handlers. Slow
// requests with a request id are handed off to a goroutine (see slowActions)
// so they never block the read loop.
func (client *WebSocketClient) OnMessage(conn *gws.Conn, message *gws.Message) {
	defer message.Close()
	conn.SetDeadline(time.Now().Add(wsDeadline))

	if message.Opcode != gws.OpcodeBinary {
		return
	}

	// Data is decoded into a RawMessage copy, so the request stays valid after
	// the message buffer is released.
	var HubRequest common.HubRequest[cbor.RawMessage]

	err := cbor.Unmarshal(message.Data.Bytes(), &HubRequest)
	if err != nil {
		slog.Error("Error parsing message", "err", err)
		return
	}

	r := &wsResponder{client: client, conn: conn}
	if client.dispatchSlowRequest(&HubRequest, r) {
		return
	}

	if err := client.handleHubRequestWith(context.Background(), &HubRequest, HubRequest.Id, r.sendResponse); err != nil {
		slog.Error("Error handling message", "err", err)
		r.sendError(HubRequest.Id, err)
	}
}

// responder sends responses for requests read from one connection. Binding to
// the connection keeps a late response from a slow request off a newer
// connection.
type responder interface {
	sendResponse(data any, requestID *uint32) error
	sendError(requestID *uint32, err error)
}

type wsResponder struct {
	client *WebSocketClient
	conn   *gws.Conn
}

func (r *wsResponder) sendResponse(data any, requestID *uint32) error {
	if requestID != nil {
		return r.client.sendMessageOn(r.conn, newAgentResponse(data, requestID))
	}
	// Legacy format - send data directly
	return r.client.sendMessageOn(r.conn, data)
}

// sendError reports a failed request to the hub so it does not wait for its
// timeout. Legacy requests without an id get no error response.
func (r *wsResponder) sendError(requestID *uint32, err error) {
	if requestID == nil || err == nil {
		return
	}
	_ = r.client.sendMessageOn(r.conn, common.AgentResponse{Id: requestID, Error: err.Error()})
}

// dispatchSlowRequest runs slow requests in a goroutine, bounded by
// maxConcurrentSlowRequests. It reports whether the request was dispatched.
// Requests without an id (legacy hubs) are matched to responses by order, so
// they are always handled inline.
func (client *WebSocketClient) dispatchSlowRequest(req *common.HubRequest[cbor.RawMessage], r responder) bool {
	if req.Id == nil || !slowActions[req.Action] {
		return false
	}
	go client.handleSlowRequest(req, r)
	return true
}

func (client *WebSocketClient) slowRequestSlots() chan struct{} {
	client.slowSlotsOnce.Do(func() {
		client.slowSlots = make(chan struct{}, maxConcurrentSlowRequests)
	})
	return client.slowSlots
}

// handleSlowRequest runs one slow request with a timeout once a slot is free.
func (client *WebSocketClient) handleSlowRequest(req *common.HubRequest[cbor.RawMessage], r responder) {
	ctx, cancel := context.WithTimeout(context.Background(), slowRequestTimeout)
	defer cancel()

	slots := client.slowRequestSlots()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		r.sendError(req.Id, errors.New("agent busy: too many concurrent requests"))
		return
	}

	if err := client.handleHubRequestWith(ctx, req, req.Id, r.sendResponse); err != nil {
		slog.Error("Error handling message", "action", req.Action, "err", err)
		r.sendError(req.Id, err)
	}
}

// OnPing handles WebSocket ping frames.
// It responds with a pong and updates the connection deadline.
func (client *WebSocketClient) OnPing(conn *gws.Conn, message []byte) {
	conn.SetDeadline(time.Now().Add(wsDeadline))
	conn.WritePong(message)
}

// handleAuthChallenge verifies the authenticity of the hub and returns the system's fingerprint.
func (client *WebSocketClient) handleAuthChallenge(msg *common.HubRequest[cbor.RawMessage], requestID *uint32) (err error) {
	var authRequest common.FingerprintRequest
	if err := cbor.Unmarshal(msg.Data, &authRequest); err != nil {
		return err
	}

	if err := client.verifySignature(authRequest.Signature); err != nil {
		client.hubVerified.Store(false)
		return err
	}

	client.hubVerified.Store(true)
	client.agent.connectionManager.eventChan <- WebSocketConnect

	response := &common.FingerprintResponse{
		Fingerprint: client.fingerprint,
	}

	if authRequest.NeedSysInfo {
		response.Name, _ = utils.GetEnv("SYSTEM_NAME")
		response.Hostname = client.agent.systemDetails.Hostname
		serverAddr := client.agent.connectionManager.serverOptions.Addr
		_, response.Port, _ = net.SplitHostPort(serverAddr)
	}

	return client.sendResponse(response, requestID)
}

// verifySignature verifies the hub's signature using the public keys. It expects
// a signature over the token and this connection's nonce. Older hubs sign only
// the token, which is accepted with a warning until a hub has signed a nonce.
func (client *WebSocketClient) verifySignature(signature []byte) error {
	if nonce := client.currentNonce(); nonce != "" && client.verifyChallenge(common.HubAuthChallenge(client.token, nonce), signature) {
		client.markNonceAuthSeen()
		return nil
	}
	if !client.verifyChallenge(common.HubAuthChallenge(client.token, ""), signature) {
		return errors.New("invalid signature - check KEY value")
	}
	if client.hasSeenNonceAuth() {
		return fmt.Errorf("hub signature is not bound to this connection and may be replayed; if the hub was downgraded, delete %s", filepath.Join(client.agent.dataDir, hubNonceAuthFileName))
	}
	slog.Warn("Hub signature is not bound to this connection; update the hub to prevent replayed handshakes")
	return nil
}

// verifyChallenge reports whether signature is a valid signature of challenge by any of the agent's keys.
func (client *WebSocketClient) verifyChallenge(challenge, signature []byte) bool {
	for _, pubKey := range client.agent.keys {
		sig := ssh.Signature{
			Format: pubKey.Type(),
			Blob:   signature,
		}
		if pubKey.Verify(challenge, &sig) == nil {
			return true
		}
	}
	return false
}

// hasSeenNonceAuth reports whether a hub has previously signed a nonce, in this process or a past one.
func (client *WebSocketClient) hasSeenNonceAuth() bool {
	if client.nonceAuthSeen.Load() {
		return true
	}
	if client.agent.dataDir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(client.agent.dataDir, hubNonceAuthFileName)); err == nil {
		client.nonceAuthSeen.Store(true)
		return true
	}
	return false
}

// markNonceAuthSeen records that the hub signs nonces, so later token-only signatures are refused.
func (client *WebSocketClient) markNonceAuthSeen() {
	if client.nonceAuthSeen.Swap(true) || client.agent.dataDir == "" {
		return
	}
	markerPath := filepath.Join(client.agent.dataDir, hubNonceAuthFileName)
	if err := os.WriteFile(markerPath, nil, 0o600); err != nil {
		slog.Warn("Failed to save hub nonce auth marker", "path", markerPath, "err", err)
	}
}

// Close closes the WebSocket connection gracefully.
// This method is safe to call multiple times.
func (client *WebSocketClient) Close() {
	if conn := client.currentConn(); conn != nil {
		_ = conn.WriteClose(1000, nil)
	}
}

// handleHubRequest routes the request to the appropriate handler using the handler registry.
func (client *WebSocketClient) handleHubRequest(msg *common.HubRequest[cbor.RawMessage], requestID *uint32) error {
	return client.handleHubRequestWith(context.Background(), msg, requestID, client.sendResponse)
}

// handleHubRequestWith routes the request using the given context and response sender.
func (client *WebSocketClient) handleHubRequestWith(ctx context.Context, msg *common.HubRequest[cbor.RawMessage], requestID *uint32, send func(data any, requestID *uint32) error) error {
	hctx := &HandlerContext{
		Ctx:          ctx,
		Client:       client,
		Agent:        client.agent,
		Request:      msg,
		RequestID:    requestID,
		HubVerified:  client.hubVerified.Load(),
		SendResponse: send,
	}
	return client.agent.handlerRegistry.Handle(hctx)
}

// sendMessage encodes the given data to CBOR and sends it as a binary message over the WebSocket connection to the hub.
func (client *WebSocketClient) sendMessage(data any) error {
	return client.sendMessageOn(client.currentConn(), data)
}

// sendMessageOn encodes data to CBOR and writes it to conn. gws serializes
// concurrent writes on a connection.
func (client *WebSocketClient) sendMessageOn(conn *gws.Conn, data any) error {
	if conn == nil {
		return errors.New("not connected")
	}
	bytes, err := cbor.Marshal(data)
	if err != nil {
		return err
	}
	err = conn.WriteMessage(gws.OpcodeBinary, bytes)
	if err != nil {
		// If writing fails (e.g., broken pipe due to network issues),
		// close the connection to trigger reconnection logic (#1263)
		_ = conn.WriteClose(1000, nil)
	}
	return err
}

// sendResponse sends a response with optional request ID.
// For ID-based requests, we must populate legacy typed fields for backward
// compatibility with older hubs (<= 0.17) that don't read the generic Data field.
func (client *WebSocketClient) sendResponse(data any, requestID *uint32) error {
	if requestID != nil {
		response := newAgentResponse(data, requestID)
		return client.sendMessage(response)
	}
	// Legacy format - send data directly
	return client.sendMessage(data)
}

// getUserAgent returns one of two User-Agent strings based on current time.
// This is used to avoid being blocked by Cloudflare or other anti-bot measures.
func getUserAgent() string {
	const (
		uaBase    = "Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
		uaWindows = "Windows NT 11.0; Win64; x64"
		uaMac     = "Macintosh; Intel Mac OS X 14_0_0"
	)
	if time.Now().UnixNano()%2 == 0 {
		return fmt.Sprintf(uaBase, uaWindows)
	}
	return fmt.Sprintf(uaBase, uaMac)
}
