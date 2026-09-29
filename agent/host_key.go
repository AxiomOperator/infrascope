package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	gossh "golang.org/x/crypto/ssh"
)

// hostKeyFileName is the file in the agent data directory holding the SSH
// server's persistent ed25519 host key (OpenSSH private key format).
const hostKeyFileName = "ssh_host_ed25519_key"

// loadOrCreateHostSigner returns the agent's persistent ed25519 SSH host key.
//
// The hub pins agent host keys on first use, so the key must survive agent
// restarts. It is read from <dataDir>/ssh_host_ed25519_key, or generated and
// written there (mode 0600) if missing. When no data directory is available or
// the key cannot be loaded or persisted, an in-memory key is used and a warning
// is logged, since the hub's pin will then break on every restart.
func loadOrCreateHostSigner(dataDir string) (gossh.Signer, error) {
	if dataDir == "" {
		slog.Warn("No data directory; using ephemeral SSH host key. Hub host key pinning will reset on restart")
		return newEphemeralHostSigner()
	}
	path := filepath.Join(dataDir, hostKeyFileName)

	signer, err := readHostSigner(path)
	if err == nil {
		return signer, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		// Don't overwrite an unreadable or corrupt key file (it may be fixable,
		// e.g. a permissions problem); serve with an ephemeral key meanwhile.
		// A hub that pinned the old key will reject this one until repaired.
		slog.Error("Could not load SSH host key; using ephemeral key", "path", path, "err", err)
		return newEphemeralHostSigner()
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := writeHostKey(path, priv); err != nil {
		slog.Warn("Could not persist SSH host key; using ephemeral key. Hub host key pinning will reset on restart", "path", path, "err", err)
	} else {
		slog.Info("Generated SSH host key", "path", path)
	}
	return gossh.NewSignerFromKey(priv)
}

func newEphemeralHostSigner() (gossh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return gossh.NewSignerFromKey(priv)
}

// readHostSigner reads an ed25519 host key from path, tightening its
// permissions to 0600 if they are broader.
func readHostSigner(path string) (gossh.Signer, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			slog.Warn("SSH host key has insecure permissions", "path", path, "mode", perm, "err", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	signer, err := gossh.ParsePrivateKey(data)
	if err != nil {
		return nil, err
	}
	if signer.PublicKey().Type() != gossh.KeyAlgoED25519 {
		return nil, fmt.Errorf("unexpected key type %s", signer.PublicKey().Type())
	}
	return signer, nil
}

// writeHostKey atomically writes priv to path with mode 0600.
func writeHostKey(path string, priv ed25519.PrivateKey) error {
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+hostKeyFileName+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := pem.Encode(tmp, block); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
