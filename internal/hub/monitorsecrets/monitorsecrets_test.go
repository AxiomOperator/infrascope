package monitorsecrets

import (
	"crypto/ed25519"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func writeKey(t *testing.T, dir string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(key, "")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, KeyFileName), pem.EncodeToMemory(block), 0o600))
}

func TestSealOpenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeKey(t, dir)
	box, err := ForDataDir(dir)
	require.NoError(t, err)
	cached, err := ForDataDir(dir)
	require.NoError(t, err)
	assert.Same(t, box, cached)

	raw := `{"basicPass":"hunter2"}`
	sealed, err := box.SealJSON(raw)
	require.NoError(t, err)
	assert.True(t, IsSealedJSON(sealed))
	assert.False(t, IsSealedJSON(raw))
	assert.NotContains(t, sealed, "hunter2")
	again, err := box.SealJSON(raw)
	require.NoError(t, err)
	assert.NotEqual(t, sealed, again, "each seal uses a fresh nonce")

	opened, err := box.OpenJSON(sealed)
	require.NoError(t, err)
	assert.Equal(t, raw, opened)

	// The same key file derives the same key.
	reloaded, err := LoadKeyFile(filepath.Join(dir, KeyFileName))
	require.NoError(t, err)
	opened, err = reloaded.OpenJSON(sealed)
	require.NoError(t, err)
	assert.Equal(t, raw, opened)
}

func TestOpenRejectsTamperingAndOtherKeys(t *testing.T) {
	box, err := NewBox([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	other, err := NewBox([]byte("fedcba9876543210fedcba9876543210"))
	require.NoError(t, err)
	sealed, err := box.Seal([]byte("secret"))
	require.NoError(t, err)

	_, err = other.Open(sealed)
	assert.Error(t, err)
	tampered := sealed[:len(sealed)-2] + strings.Repeat("A", 2)
	if tampered == sealed {
		tampered = sealed[:len(sealed)-2] + "BB"
	}
	_, err = box.Open(tampered)
	assert.Error(t, err)
	_, err = box.Open("plain")
	assert.Error(t, err)
	_, err = box.Open(Prefix + "AAAA")
	assert.Error(t, err)
}

func TestForDataDirMissingKey(t *testing.T) {
	_, err := ForDataDir(t.TempDir())
	assert.Error(t, err)
}
