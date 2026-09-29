//go:build testing

package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/container"
	"github.com/henrygd/beszel/internal/entities/system"

	"github.com/blang/semver"
	"github.com/fxamacker/cbor/v2"
	"github.com/gliderlabs/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// freeAddr reserves a free local port for network and returns its address.
func freeAddr(t *testing.T, network, host string) string {
	t.Helper()
	ln, err := net.Listen(network, net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("%s not available: %v", network, err)
	}
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// startTestServer starts agent's SSH server in the background, waits until it
// is listening and stops it when the test ends.
func startTestServer(t *testing.T, agent *Agent, opts ServerOptions) {
	t.Helper()
	errChan := make(chan error, 1)
	go func() { errChan <- agent.StartServer(opts) }()
	t.Cleanup(func() {
		_ = agent.StopServer()
		select {
		case <-errChan:
		case <-time.After(5 * time.Second):
			t.Error("SSH server did not stop")
		}
	})
	// a.server is set only after the listener is open.
	require.Eventually(t, func() bool {
		select {
		case err := <-errChan:
			errChan <- err
			return true
		default:
		}
		agent.serverMu.Lock()
		defer agent.serverMu.Unlock()
		return agent.server != nil
	}, 5*time.Second, 5*time.Millisecond)
	select {
	case err := <-errChan:
		t.Fatalf("StartServer returned early: %v", err)
	default:
	}
}

func dialTestServer(opts ServerOptions, signer gossh.Signer, hostKeyCallback gossh.HostKeyCallback) (*gossh.Client, error) {
	if hostKeyCallback == nil {
		hostKeyCallback = gossh.InsecureIgnoreHostKey()
	}
	network := "tcp"
	if opts.Network == "unix" {
		network = "unix"
	}
	return gossh.Dial(network, opts.Addr, &gossh.ClientConfig{
		User:            "a",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         4 * time.Second,
	})
}

func TestStartServer(t *testing.T) {
	// Generate a test key pair
	pubKey, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer, err := gossh.NewSignerFromKey(privKey)
	require.NoError(t, err)
	sshPubKey, err := gossh.NewPublicKey(pubKey)
	require.NoError(t, err)

	// Generate a different key pair for bad key test
	badPubKey, badPrivKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	badSigner, err := gossh.NewSignerFromKey(badPrivKey)
	require.NoError(t, err)
	sshBadPubKey, err := gossh.NewPublicKey(badPubKey)
	require.NoError(t, err)

	tests := []struct {
		name        string
		network     string
		host        string // for tcp: host to bind; port is chosen freely
		clientKey   gossh.Signer
		serverKey   gossh.PublicKey
		wantErr     bool
		errContains string
	}{
		{name: "tcp port only", network: "tcp", host: ""},
		{name: "tcp with ipv4", network: "tcp4", host: "127.0.0.1"},
		{name: "tcp with ipv6", network: "tcp6", host: "::1"},
		{name: "unix socket", network: "unix"},
		{name: "bad key should fail", network: "tcp", host: "127.0.0.1", clientKey: badSigner, serverKey: sshBadPubKey, wantErr: true, errContains: "ssh: handshake failed"},
		{name: "wrong client key should fail", network: "tcp", host: "127.0.0.1", clientKey: badSigner, wantErr: true, errContains: "ssh: handshake failed"},
		{name: "good key still good", network: "tcp", host: "127.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := ServerOptions{Network: tt.network, Keys: []gossh.PublicKey{sshPubKey}}
			if tt.serverKey != nil {
				// server trusts a key the client doesn't hold
				opts.Keys = []gossh.PublicKey{tt.serverKey}
				tt.clientKey = signer
			}
			if tt.network == "unix" {
				opts.Addr = filepath.Join(t.TempDir(), "beszel-test.sock")
				// a stale socket file should be removed
				f, err := os.Create(opts.Addr)
				require.NoError(t, err)
				require.NoError(t, f.Close())
			} else {
				opts.Addr = freeAddr(t, tt.network, tt.host)
				if tt.host == "" {
					_, port, _ := net.SplitHostPort(opts.Addr)
					opts.Addr = ":" + port
				}
			}
			clientKey := tt.clientKey
			if clientKey == nil {
				clientKey = signer
			}

			agent := createTestAgent(t)
			startTestServer(t, agent, opts)

			client, err := dialTestServer(opts, clientKey, nil)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}
			require.NoError(t, err)
			require.NotNil(t, client)
			client.Close()
		})
	}
}

func TestStartServerUnixSocketPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket permissions not applicable on windows")
	}
	signer, sshPubKey := newTestClientKey(t)
	opts := ServerOptions{
		Network: "unix",
		Addr:    filepath.Join(t.TempDir(), "agent.sock"),
		Keys:    []gossh.PublicKey{sshPubKey},
	}
	agent := createTestAgent(t)
	startTestServer(t, agent, opts)

	info, err := os.Stat(opts.Addr)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSocket, info.Mode().Type())
	assert.Equal(t, os.FileMode(0o666), info.Mode().Perm())

	client, err := dialTestServer(opts, signer, nil)
	require.NoError(t, err)
	client.Close()
}

func newTestClientKey(t *testing.T) (gossh.Signer, gossh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer, err := gossh.NewSignerFromKey(priv)
	require.NoError(t, err)
	return signer, signer.PublicKey()
}

func TestStartServerPersistentHostKey(t *testing.T) {
	signer, sshPubKey := newTestClientKey(t)
	dataDir := t.TempDir()
	keyPath := filepath.Join(dataDir, hostKeyFileName)

	// dial starts a fresh agent on dataDir and returns the host key it presents.
	dial := func() gossh.PublicKey {
		opts := ServerOptions{
			Network: "tcp",
			Addr:    freeAddr(t, "tcp", "127.0.0.1"),
			Keys:    []gossh.PublicKey{sshPubKey},
		}
		agent, err := NewAgent(dataDir)
		require.NoError(t, err)
		startTestServer(t, agent, opts)

		var hostKey gossh.PublicKey
		client, err := dialTestServer(opts, signer, func(_ string, _ net.Addr, key gossh.PublicKey) error {
			hostKey = key
			return nil
		})
		require.NoError(t, err)
		client.Close()
		require.NoError(t, agent.StopServer())
		return hostKey
	}

	first := dial()
	require.NotNil(t, first)
	assert.Equal(t, gossh.KeyAlgoED25519, first.Type(), "handshake should present ed25519 host key")

	info, err := os.Stat(keyPath)
	require.NoError(t, err, "host key should be persisted")
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	persisted, err := readHostSigner(keyPath)
	require.NoError(t, err)
	assert.Equal(t, first.Marshal(), persisted.PublicKey().Marshal())

	// restart with the same data dir: same host key
	second := dial()
	assert.Equal(t, first.Marshal(), second.Marshal(), "host key should be reused across restarts")
}

func TestLoadOrCreateHostSigner(t *testing.T) {
	t.Run("no data dir uses ephemeral key", func(t *testing.T) {
		a, err := loadOrCreateHostSigner("")
		require.NoError(t, err)
		b, err := loadOrCreateHostSigner("")
		require.NoError(t, err)
		assert.Equal(t, gossh.KeyAlgoED25519, a.PublicKey().Type())
		assert.NotEqual(t, a.PublicKey().Marshal(), b.PublicKey().Marshal())
	})

	t.Run("creates then reuses key", func(t *testing.T) {
		dir := t.TempDir()
		a, err := loadOrCreateHostSigner(dir)
		require.NoError(t, err)
		b, err := loadOrCreateHostSigner(dir)
		require.NoError(t, err)
		assert.Equal(t, a.PublicKey().Marshal(), b.PublicKey().Marshal())
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Len(t, entries, 1, "no temp files should be left behind")
	})

	t.Run("tightens loose permissions", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("permissions not applicable on windows")
		}
		dir := t.TempDir()
		_, err := loadOrCreateHostSigner(dir)
		require.NoError(t, err)
		path := filepath.Join(dir, hostKeyFileName)
		require.NoError(t, os.Chmod(path, 0o644))
		_, err = loadOrCreateHostSigner(dir)
		require.NoError(t, err)
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	})

	t.Run("corrupt key is not overwritten", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, hostKeyFileName)
		require.NoError(t, os.WriteFile(path, []byte("garbage"), 0o600))
		s, err := loadOrCreateHostSigner(dir)
		require.NoError(t, err)
		assert.Equal(t, gossh.KeyAlgoED25519, s.PublicKey().Type())
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "garbage", string(data))
	})
}

func TestStartServerNoGlobalHandler(t *testing.T) {
	// Two agents serving concurrently must each dispatch to their own handler.
	for range 2 {
		signer, pub := newTestClientKey(t)
		agent := createTestAgent(t)
		opts := ServerOptions{Network: "tcp", Addr: freeAddr(t, "tcp", "127.0.0.1"), Keys: []gossh.PublicKey{pub}}
		startTestServer(t, agent, opts)
		agent.serverMu.Lock()
		assert.NotNil(t, agent.server.Handler)
		agent.serverMu.Unlock()
		client, err := dialTestServer(opts, signer, nil)
		require.NoError(t, err)
		client.Close()
	}
}

func TestStartServerDisableSSH(t *testing.T) {
	t.Setenv("BESZEL_AGENT_DISABLE_SSH", "true")

	agent, err := NewAgent("")
	require.NoError(t, err)

	opts := ServerOptions{
		Network: "tcp",
		Addr:    ":45990",
	}

	err = agent.StartServer(opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SSH disabled")
}

func TestStopServerDoesNotBlockWhenEventQueueFull(t *testing.T) {
	agent := createTestAgent(t)
	agent.server = &ssh.Server{}
	agent.connectionManager.eventChan = make(chan ConnectionEvent, 1)
	agent.connectionManager.eventChan <- WebSocketConnect

	done := make(chan error, 1)
	go func() {
		done <- agent.StopServer()
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("StopServer blocked on the connection event queue")
	}

	assert.Nil(t, agent.server)
	assert.Equal(t, WebSocketConnect, <-agent.connectionManager.eventChan)
}

/////////////////////////////////////////////////////////////////
//////////////////// ParseKeys Tests ////////////////////////////
/////////////////////////////////////////////////////////////////

// Helper function to generate a temporary file with content
func createTempFile(content string) (string, error) {
	tmpFile, err := os.CreateTemp("", "ssh_keys_*.txt")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer tmpFile.Close()

	if _, err := tmpFile.WriteString(content); err != nil {
		return "", fmt.Errorf("failed to write to temp file: %w", err)
	}

	return tmpFile.Name(), nil
}

// Test case 1: String with a single SSH key
func TestParseSingleKeyFromString(t *testing.T) {
	input := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKCBM91kukN7hbvFKtbpEeo2JXjCcNxXcdBH7V7ADMBo"
	keys, err := ParseKeys(input)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("Expected 1 key, got %d keys", len(keys))
	}
	if keys[0].Type() != "ssh-ed25519" {
		t.Fatalf("Expected key type 'ssh-ed25519', got '%s'", keys[0].Type())
	}
}

// Test case 2: String with multiple SSH keys
func TestParseMultipleKeysFromString(t *testing.T) {
	input := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKCBM91kukN7hbvFKtbpEeo2JXjCcNxXcdBH7V7ADMBo\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJDMtAOQfxDlCxe+A5lVbUY/DHxK1LAF2Z3AV0FYv36D \n #comment\n ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJDMtAOQfxDlCxe+A5lVbUY/DHxK1LAF2Z3AV0FYv36D"
	keys, err := ParseKeys(input)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("Expected 3 keys, got %d keys", len(keys))
	}
	if keys[0].Type() != "ssh-ed25519" || keys[1].Type() != "ssh-ed25519" || keys[2].Type() != "ssh-ed25519" {
		t.Fatalf("Unexpected key types: %s, %s, %s", keys[0].Type(), keys[1].Type(), keys[2].Type())
	}
}

// Test case 3: File with a single SSH key
func TestParseSingleKeyFromFile(t *testing.T) {
	content := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKCBM91kukN7hbvFKtbpEeo2JXjCcNxXcdBH7V7ADMBo"
	filePath, err := createTempFile(content)
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(filePath) // Clean up the file after the test

	// Read the file content
	fileContent, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read temp file: %v", err)
	}

	// Parse the keys
	keys, err := ParseKeys(string(fileContent))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("Expected 1 key, got %d keys", len(keys))
	}
	if keys[0].Type() != "ssh-ed25519" {
		t.Fatalf("Expected key type 'ssh-ed25519', got '%s'", keys[0].Type())
	}
}

// Test case 4: File with multiple SSH keys
func TestParseMultipleKeysFromFile(t *testing.T) {
	content := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKCBM91kukN7hbvFKtbpEeo2JXjCcNxXcdBH7V7ADMBo\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJDMtAOQfxDlCxe+A5lVbUY/DHxK1LAF2Z3AV0FYv36D \n #comment\n ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJDMtAOQfxDlCxe+A5lVbUY/DHxK1LAF2Z3AV0FYv36D"
	filePath, err := createTempFile(content)
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	// defer os.Remove(filePath) // Clean up the file after the test

	// Read the file content
	fileContent, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read temp file: %v", err)
	}

	// Parse the keys
	keys, err := ParseKeys(string(fileContent))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("Expected 3 keys, got %d keys", len(keys))
	}
	if keys[0].Type() != "ssh-ed25519" || keys[1].Type() != "ssh-ed25519" || keys[2].Type() != "ssh-ed25519" {
		t.Fatalf("Unexpected key types: %s, %s, %s", keys[0].Type(), keys[1].Type(), keys[2].Type())
	}
}

// Test case 5: Invalid SSH key input
func TestParseInvalidKey(t *testing.T) {
	input := "invalid-key-data"
	_, err := ParseKeys(input)
	if err == nil {
		t.Fatalf("Expected an error for invalid key, got nil")
	}
	expectedErrMsg := "failed to parse key"
	if !strings.Contains(err.Error(), expectedErrMsg) {
		t.Fatalf("Expected error message to contain '%s', got: %v", expectedErrMsg, err)
	}
}

/////////////////////////////////////////////////////////////////
//////////////////// Hub Version Tests //////////////////////////
/////////////////////////////////////////////////////////////////

func TestExtractHubVersion(t *testing.T) {
	tests := []struct {
		name            string
		clientVersion   string
		expectedVersion string
		expectError     bool
	}{
		{
			name:            "valid beszel client version with underscore",
			clientVersion:   "SSH-2.0-beszel_0.11.1",
			expectedVersion: "0.11.1",
			expectError:     false,
		},
		{
			name:            "valid beszel client version with beta",
			clientVersion:   "SSH-2.0-beszel_1.0.0-beta",
			expectedVersion: "1.0.0-beta",
			expectError:     false,
		},
		{
			name:            "valid beszel client version with rc",
			clientVersion:   "SSH-2.0-beszel_0.12.0-rc1",
			expectedVersion: "0.12.0-rc1",
			expectError:     false,
		},
		{
			name:            "different SSH client",
			clientVersion:   "SSH-2.0-OpenSSH_8.0",
			expectedVersion: "8.0",
			expectError:     true,
		},
		{
			name:          "malformed version string without underscore",
			clientVersion: "SSH-2.0-beszel",
			expectError:   true,
		},
		{
			name:          "empty version string",
			clientVersion: "",
			expectError:   true,
		},
		{
			name:            "version string with underscore but no version",
			clientVersion:   "beszel_",
			expectedVersion: "",
			expectError:     true,
		},
		{
			name:            "version with patch and build metadata",
			clientVersion:   "SSH-2.0-beszel_1.2.3+build.123",
			expectedVersion: "1.2.3+build.123",
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := extractHubVersion(tt.clientVersion)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedVersion, result.String())
		})
	}
}

/////////////////////////////////////////////////////////////////
/////////////// Hub Version Detection Tests ////////////////////
/////////////////////////////////////////////////////////////////

func TestGetHubVersion(t *testing.T) {
	agent, err := NewAgent("")
	require.NoError(t, err)

	// Mock SSH context that implements the ssh.Context interface
	mockCtx := &mockSSHContext{
		sessionID:     "test-session-123",
		clientVersion: "SSH-2.0-beszel_0.12.0",
	}

	// Test first call - should extract version
	version := agent.getHubVersion(mockCtx)
	assert.Equal(t, "0.12.0", version.String())

	// Test that version reflects the current client version (no stale caching)
	mockCtx.clientVersion = "SSH-2.0-beszel_0.11.0"
	version = agent.getHubVersion(mockCtx)
	assert.Equal(t, "0.11.0", version.String())

	// Test with invalid version string (non-beszel client)
	mockCtx.clientVersion = "SSH-2.0-OpenSSH_8.0"
	version = agent.getHubVersion(mockCtx)
	assert.Equal(t, "0.0.0", version.String()) // Should be empty version for non-beszel clients

	// Test with no client version
	mockCtx.clientVersion = ""
	version = agent.getHubVersion(mockCtx)
	assert.True(t, version.EQ(semver.Version{})) // Should be empty version
}

// mockSSHContext implements ssh.Context for testing
type mockSSHContext struct {
	context.Context
	sync.Mutex
	sessionID     string
	clientVersion string
}

func (m *mockSSHContext) SessionID() string {
	return m.sessionID
}

func (m *mockSSHContext) ClientVersion() string {
	return m.clientVersion
}

func (m *mockSSHContext) ServerVersion() string {
	return "SSH-2.0-beszel_test"
}

func (m *mockSSHContext) Value(key interface{}) interface{} {
	if key == ssh.ContextKeyClientVersion {
		return m.clientVersion
	}
	return nil
}

func (m *mockSSHContext) User() string                    { return "test-user" }
func (m *mockSSHContext) RemoteAddr() net.Addr            { return nil }
func (m *mockSSHContext) LocalAddr() net.Addr             { return nil }
func (m *mockSSHContext) Permissions() *ssh.Permissions   { return nil }
func (m *mockSSHContext) SetValue(key, value interface{}) {}

/////////////////////////////////////////////////////////////////
/////////////// CBOR vs JSON Encoding Tests ////////////////////
/////////////////////////////////////////////////////////////////

// TestWriteToSessionEncoding tests that writeToSession actually encodes data in the correct format
func TestWriteToSessionEncoding(t *testing.T) {
	tests := []struct {
		name             string
		hubVersion       string
		expectedUsesCbor bool
	}{
		{
			name:             "old hub version should use JSON",
			hubVersion:       "0.11.1",
			expectedUsesCbor: false,
		},
		{
			name:             "non-beta release should use CBOR",
			hubVersion:       "0.12.0",
			expectedUsesCbor: true,
		},
		{
			name:             "even newer hub version should use CBOR",
			hubVersion:       "0.16.4",
			expectedUsesCbor: true,
		},
		{
			name:             "beta version below release threshold should use JSON",
			hubVersion:       "0.12.0-beta0",
			expectedUsesCbor: false,
		},
		// {
		// 	name:             "matching beta version should use CBOR",
		// 	hubVersion:       "0.12.0-beta2",
		// 	expectedUsesCbor: true,
		// },
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, err := NewAgent("")
			require.NoError(t, err)

			// Parse the test version
			version, err := semver.Parse(tt.hubVersion)
			require.NoError(t, err)

			// Create test data to encode
			testData := createTestCombinedData()

			var buf strings.Builder
			err = agent.writeToSession(&buf, testData, version)
			require.NoError(t, err)

			encodedData := buf.String()
			require.NotEmpty(t, encodedData)

			// Verify the encoding format by attempting to decode
			if tt.expectedUsesCbor {
				var decodedCbor system.CombinedData
				err = cbor.Unmarshal([]byte(encodedData), &decodedCbor)
				assert.NoError(t, err, "Should be valid CBOR data")

				var decodedJson system.CombinedData
				err = json.Unmarshal([]byte(encodedData), &decodedJson)
				assert.Error(t, err, "Should not be valid JSON data")

				assert.Equal(t, testData.Details.Hostname, decodedCbor.Details.Hostname)
				assert.Equal(t, testData.Stats.Cpu, decodedCbor.Stats.Cpu)
			} else {
				// Should be JSON - try to decode as JSON
				var decodedJson system.CombinedData
				err = json.Unmarshal([]byte(encodedData), &decodedJson)
				assert.NoError(t, err, "Should be valid JSON data")

				var decodedCbor system.CombinedData
				err = cbor.Unmarshal([]byte(encodedData), &decodedCbor)
				assert.Error(t, err, "Should not be valid CBOR data")

				// Verify the decoded JSON data matches our test data
				assert.Equal(t, testData.Details.Hostname, decodedJson.Details.Hostname)
				assert.Equal(t, testData.Stats.Cpu, decodedJson.Stats.Cpu)

				// Verify it looks like JSON (starts with '{' and contains readable field names)
				assert.True(t, strings.HasPrefix(encodedData, "{"), "JSON should start with '{'")
				assert.Contains(t, encodedData, `"info"`, "JSON should contain readable field names")
				assert.Contains(t, encodedData, `"stats"`, "JSON should contain readable field names")
			}
		})
	}
}

// Helper function to create test data for encoding tests
func createTestCombinedData() *system.CombinedData {
	return &system.CombinedData{
		Stats: system.Stats{
			Cpu:       25.5,
			Mem:       8589934592, // 8GB
			MemUsed:   4294967296, // 4GB
			MemPct:    50.0,
			DiskTotal: 1099511627776, // 1TB
			DiskUsed:  549755813888,  // 512GB
			DiskPct:   50.0,
		},
		Details: &system.Details{
			Hostname: "test-host",
		},
		Info: system.Info{
			Uptime:       3600,
			AgentVersion: "0.12.0",
		},
		Containers: []*container.Stats{
			{
				Name: "test-container",
				Cpu:  10.5,
				Mem:  1073741824, // 1GB
			},
		},
	}
}

// TestGetHubVersionConcurrent guards against a regression of the
// "concurrent map writes" panic previously caused by a shared, unsynchronized
// hubVersions cache (see https://github.com/henrygd/beszel/issues/2128).
// getHubVersion no longer shares mutable state between sessions, so calling
// it concurrently from many goroutines must be safe under `go test -race`.
func TestGetHubVersionConcurrent(t *testing.T) {
	agent, err := NewAgent("")
	require.NoError(t, err)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			ctx := &mockSSHContext{
				sessionID:     fmt.Sprintf("session-%d", i),
				clientVersion: "SSH-2.0-beszel_0.12.0",
			}
			version := agent.getHubVersion(ctx)
			assert.Equal(t, "0.12.0", version.String())
		}(i)
	}
	wg.Wait()
}
