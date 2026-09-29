package systems

import (
	"errors"
	"fmt"
	"net"

	"github.com/pocketbase/dbx"
	"golang.org/x/crypto/ssh"
)

// hostKeyField is the hidden systems field that pins the SHA256 fingerprint of
// the agent's SSH host key (trust on first use). It is cleared when the
// system's host or port changes, or through the reset-host-key API.
const hostKeyField = "hostKey"

// downReasonField is the systems field holding why the system last went down
// (e.g. a host key mismatch). It is cleared when the system comes up.
const downReasonField = "downReason"

const maxDownReasonLength = 500

// ErrHostKeyMismatch is returned when an agent presents a host key other than
// the one pinned for the system.
var ErrHostKeyMismatch = errors.New("SSH host key mismatch")

// rejectHostKey is the shared config's callback; per-system configs replace it.
func rejectHostKey(string, net.Addr, ssh.PublicKey) error {
	return errors.New("SSH host key verification is not configured")
}

// sshClientConfig returns the hub SSH config with this system's host key check.
func (sys *System) sshClientConfig() (*ssh.ClientConfig, error) {
	if sys.manager == nil {
		return nil, errNoSSHConfig
	}
	base, err := sys.manager.getSSHConfig()
	if err != nil {
		return nil, err
	}
	config := *base
	config.HostKeyCallback = sys.verifyHostKey
	return &config, nil
}

// verifyHostKey implements trust on first use for the agent's SSH host key.
//
// With a pinned fingerprint, any other key (of any type) is rejected. Without
// one, the key is pinned, except for RSA keys: agents without a persistent
// host key present a fresh RSA key on every start (the SSH library's
// default), so pinning those would take the system down at the next agent
// restart. Such agents stay unpinned until they present a persistent key.
func (sys *System) verifyHostKey(_ string, _ net.Addr, key ssh.PublicKey) error {
	fingerprint := ssh.FingerprintSHA256(key)
	app := sys.manager.hub
	var row struct {
		HostKey string `db:"hostKey"`
	}
	err := app.DB().NewQuery("SELECT COALESCE(hostKey, '') AS hostKey FROM systems WHERE id={:id}").
		Bind(dbx.Params{"id": sys.Id}).One(&row)
	if err != nil {
		return fmt.Errorf("failed to read pinned host key: %w", err)
	}
	if row.HostKey != "" {
		if row.HostKey != fingerprint {
			return fmt.Errorf("%w: the agent presented %s but %s is pinned. If the agent was reinstalled or its host key changed, reset the system's host key", ErrHostKeyMismatch, fingerprint, row.HostKey)
		}
		return nil
	}
	if key.Type() == ssh.KeyAlgoRSA {
		return nil
	}
	// Pin only while the field is still empty, so concurrent first connections
	// can't overwrite each other's pin.
	result, err := app.DB().NewQuery("UPDATE systems SET hostKey={:key} WHERE id={:id} AND COALESCE(hostKey, '') = ''").
		Bind(dbx.Params{"id": sys.Id, "key": fingerprint}).Execute()
	if err != nil {
		return fmt.Errorf("failed to pin host key: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		// Another connection pinned a key first; verify against it.
		return sys.verifyHostKey("", nil, key)
	}
	app.Logger().Info("Pinned agent SSH host key", "system", sys.Id, "fingerprint", fingerprint)
	return nil
}
