// Package monitorsecrets encrypts monitor secrets (the network_monitors
// httpSecrets field) at rest.
//
// The key is derived with HKDF-SHA256 from the seed of the hub's ed25519
// private key (id_ed25519 in the data dir) and used with AES-256-GCM. A sealed
// value is the string "enc:v1:" followed by the standard base64 encoding of
// nonce||ciphertext, stored as a JSON string in the JSON field.
package monitorsecrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

const (
	// Prefix starts every sealed value.
	Prefix = "enc:v1:"
	// KeyFileName is the hub's private key file in the data dir.
	KeyFileName = "id_ed25519"
	hkdfInfo    = "infrascope/monitor-secrets/v1"
)

// Box seals and opens monitor secrets.
type Box struct {
	aead cipher.AEAD
}

// NewBox returns a Box whose key is derived from secret.
func NewBox(secret []byte) (*Box, error) {
	if len(secret) == 0 {
		return nil, errors.New("empty key material")
	}
	key, err := hkdf.Key(sha256.New, secret, nil, hkdfInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// LoadKeyFile returns a Box keyed by the ed25519 private key in the OpenSSH
// PEM file at path. It does not create the file.
func LoadKeyFile(path string) (*Box, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read monitor secrets key: %w", err)
	}
	raw, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse monitor secrets key: %w", err)
	}
	var seed []byte
	switch key := raw.(type) {
	case ed25519.PrivateKey:
		seed = key.Seed()
	case *ed25519.PrivateKey:
		seed = key.Seed()
	default:
		return nil, fmt.Errorf("monitor secrets key: unsupported key type %T", raw)
	}
	return NewBox(seed)
}

// boxes caches loaded boxes by key file path.
var boxes sync.Map

// ForDataDir returns the Box of the key file in dataDir. Successful loads
// are cached; a missing key file is an error.
func ForDataDir(dataDir string) (*Box, error) {
	path := filepath.Join(dataDir, KeyFileName)
	if box, ok := boxes.Load(path); ok {
		return box.(*Box), nil
	}
	box, err := LoadKeyFile(path)
	if err != nil {
		return nil, err
	}
	actual, _ := boxes.LoadOrStore(path, box)
	return actual.(*Box), nil
}

// Seal encrypts plaintext and returns the sealed string.
func (b *Box) Seal(plaintext []byte) (string, error) {
	nonce := make([]byte, b.aead.NonceSize(), b.aead.NonceSize()+len(plaintext)+b.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, plaintext, nil)
	return Prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a sealed string.
func (b *Box) Open(sealed string) ([]byte, error) {
	encoded, ok := strings.CutPrefix(sealed, Prefix)
	if !ok {
		return nil, errors.New("not a sealed value")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode sealed value: %w", err)
	}
	size := b.aead.NonceSize()
	if len(data) < size+b.aead.Overhead() {
		return nil, errors.New("sealed value is too short")
	}
	plaintext, err := b.aead.Open(nil, data[:size], data[size:], nil)
	if err != nil {
		return nil, errors.New("decrypt sealed value: authentication failed")
	}
	return plaintext, nil
}

// IsSealedJSON reports whether raw, the stored JSON of a field, is a sealed value.
func IsSealedJSON(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), `"`+Prefix)
}

// SealJSON seals the JSON document raw and returns it as a JSON string value.
func (b *Box) SealJSON(raw string) (string, error) {
	sealed, err := b.Seal([]byte(raw))
	if err != nil {
		return "", err
	}
	quoted, err := json.Marshal(sealed)
	return string(quoted), err
}

// OpenJSON returns the JSON document sealed in raw, a JSON string value made by SealJSON.
func (b *Box) OpenJSON(raw string) (string, error) {
	var sealed string
	if err := json.Unmarshal([]byte(raw), &sealed); err != nil {
		return "", fmt.Errorf("invalid sealed value: %w", err)
	}
	plaintext, err := b.Open(sealed)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
