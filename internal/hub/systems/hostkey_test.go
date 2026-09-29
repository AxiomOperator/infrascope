//go:build testing

package systems

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"testing"
	"time"

	"github.com/blang/semver"
	"github.com/henrygd/beszel/internal/hub/expirymap"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func newHostSigner(t *testing.T, rsaKey bool) ssh.Signer {
	t.Helper()
	var key any
	var err error
	if rsaKey {
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	} else {
		_, key, err = ed25519.GenerateKey(rand.Reader)
	}
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(key)
	require.NoError(t, err)
	return signer
}

// startHostKeyServer serves SSH handshakes with the given host key and returns
// its host and port.
func startHostKeyServer(t *testing.T, hostKey ssh.Signer) (string, string) {
	t.Helper()
	config := &ssh.ServerConfig{NoClientAuth: true, ServerVersion: "SSH-2.0-beszel_0.20.0"}
	config.AddHostKey(hostKey)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				server, channels, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					_ = conn.Close()
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(reqs)
				for channel := range channels {
					_ = channel.Reject(ssh.Prohibited, "test")
				}
			}()
		}
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	return host, port
}

func pinnedHostKey(t *testing.T, app core.App, systemID string) string {
	t.Helper()
	record, err := app.FindRecordById("systems", systemID)
	require.NoError(t, err)
	return record.GetString(hostKeyField)
}

func TestSSHHostKeyTrustOnFirstUse(t *testing.T) {
	sys, app := newTestSystemWithHub(t)
	sys.manager.sshConfig = &ssh.ClientConfig{User: "test", HostKeyCallback: rejectHostKey, Timeout: time.Second}
	sys.manager.zfsFetchMap = expirymap.New[zfsFetchState](time.Hour)
	t.Cleanup(sys.manager.zfsFetchMap.StopCleaner)
	t.Cleanup(sys.closeSSHConnection)

	agentKey := newHostSigner(t, false)
	sys.Host, sys.Port = startHostKeyServer(t, agentKey)

	// First connection pins the key.
	require.NoError(t, sys.createSSHClient())
	fingerprint := ssh.FingerprintSHA256(agentKey.PublicKey())
	assert.Equal(t, fingerprint, pinnedHostKey(t, app, sys.Id))
	sys.closeSSHConnection()

	// The same key keeps working.
	require.NoError(t, sys.createSSHClient())
	sys.closeSSHConnection()

	// A different key, of any type, is rejected with a clear error.
	for _, rsaKey := range []bool{false, true} {
		sys.Host, sys.Port = startHostKeyServer(t, newHostSigner(t, rsaKey))
		err := sys.createSSHClient()
		require.ErrorIs(t, err, ErrHostKeyMismatch)
		assert.Contains(t, err.Error(), "reset the system's host key")
		assert.Equal(t, fingerprint, pinnedHostKey(t, app, sys.Id), "a mismatch must not replace the pin")
	}

	// The mismatch is surfaced as the system's down reason.
	sys.ctx, sys.cancel = t.Context(), func() {}
	sys.Status = up
	require.NoError(t, sys.setDown(sys.createSSHClient()))
	record, err := app.FindRecordById("systems", sys.Id)
	require.NoError(t, err)
	assert.Equal(t, down, record.GetString("status"))
	assert.Contains(t, record.GetString(downReasonField), ErrHostKeyMismatch.Error())
}

// Agents without a persistent host key present a new RSA key on every start,
// so such keys are accepted without pinning until a pin exists.
func TestSSHHostKeyEphemeralRSANotPinned(t *testing.T) {
	sys, app := newTestSystemWithHub(t)
	sys.manager.sshConfig = &ssh.ClientConfig{User: "test", HostKeyCallback: rejectHostKey, Timeout: time.Second}
	sys.manager.zfsFetchMap = expirymap.New[zfsFetchState](time.Hour)
	t.Cleanup(sys.manager.zfsFetchMap.StopCleaner)
	t.Cleanup(sys.closeSSHConnection)

	for range 2 {
		sys.Host, sys.Port = startHostKeyServer(t, newHostSigner(t, true))
		require.NoError(t, sys.createSSHClient())
		sys.closeSSHConnection()
		assert.Empty(t, pinnedHostKey(t, app, sys.Id))
	}
}

func TestHostKeyClearedOnAddressChange(t *testing.T) {
	sys, app := newTestSystemWithHub(t)
	sm := &SystemManager{hub: sys.manager.hub}
	_, err := app.DB().NewQuery("UPDATE systems SET hostKey='SHA256:pinned' WHERE id={:id}").
		Bind(map[string]any{"id": sys.Id}).Execute()
	require.NoError(t, err)

	update := func(field, value string) *core.Record {
		t.Helper()
		record, err := app.FindRecordById("systems", sys.Id)
		require.NoError(t, err)
		record.Set(field, value)
		e := &core.RecordEvent{App: app}
		e.Record = record
		require.NoError(t, sm.onRecordUpdate(e))
		return record
	}

	assert.Equal(t, "SHA256:pinned", update("name", "renamed").GetString(hostKeyField), "unrelated edits keep the pin")
	assert.Empty(t, update("host", "10.0.0.2").GetString(hostKeyField))
	assert.Empty(t, update("port", "2222").GetString(hostKeyField))
}

// Without a hub key the SSH fallback must fail cleanly instead of dialing with
// a nil signer (which panics).
func TestSSHFallbackWithoutSigner(t *testing.T) {
	sys, _ := newTestSystemWithHub(t)
	sys.Host, sys.Port = "127.0.0.1", "1"
	_, err := sys.ensureSSHTransport()
	require.ErrorIs(t, err, errNoSSHConfig)
	require.ErrorIs(t, sys.createSSHClient(), errNoSSHConfig)
	sys.agentVersion = semver.MustParse("0.20.0")
	require.ErrorIs(t, sys.SyncNetworkMonitors(nil), errNoSSHConfig)
}
