package ghupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// releasePublicKey is the base64-encoded ed25519 public key that release
// archives are signed with. It is embedded at build time:
//
//	-ldflags "-X github.com/henrygd/beszel/internal/ghupdate.releasePublicKey=<base64 key>"
//
// Builds without it refuse to self-update (fail closed).
var releasePublicKey string

// SignatureSuffix is appended to a release archive's name to name the asset
// holding its signature (e.g. beszel_linux_amd64.tar.gz.sig). The asset holds
// the base64-encoded ed25519 signature of the archive's bytes.
const SignatureSuffix = ".sig"

// ErrUnsignedBuild is returned by Update when the executable has no release
// signing key embedded, so downloaded releases can't be verified.
var ErrUnsignedBuild = errors.New("self-update is unavailable: this build has no release signing key embedded, and InfraScope only installs releases whose signature it can verify. Download the release manually, or use an official release build")

// ErrInvalidSignature is returned when a release archive's signature is
// missing or does not match the embedded public key.
var ErrInvalidSignature = errors.New("release signature verification failed")

// ParsePublicKey decodes a base64-encoded ed25519 public key.
func ParsePublicKey(encoded string) (ed25519.PublicKey, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, ErrUnsignedBuild
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid embedded release public key")
	}
	return ed25519.PublicKey(key), nil
}

// ParsePrivateKey decodes a base64-encoded ed25519 private key: either the
// 32-byte seed or the 64-byte private key.
func ParsePrivateKey(encoded string) (ed25519.PrivateKey, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("invalid signing key encoding: %w", err)
	}
	switch len(key) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(key), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(key), nil
	}
	return nil, fmt.Errorf("invalid signing key length %d", len(key))
}

// SignArchive returns the base64-encoded signature of an archive's bytes, as
// stored in its signature asset.
func SignArchive(key ed25519.PrivateKey, archive []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, archive)) + "\n"
}

// verifyArchiveSignature checks the signature asset contents against the
// archive at path.
func verifyArchiveSignature(publicKey ed25519.PublicKey, path string, signature []byte) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: malformed signature", ErrInvalidSignature)
	}
	archive, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read release for signature verification: %w", err)
	}
	if !ed25519.Verify(publicKey, archive, sig) {
		return fmt.Errorf("%w: the archive was not signed by the InfraScope release key", ErrInvalidSignature)
	}
	return nil
}
