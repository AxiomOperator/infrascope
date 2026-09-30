package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// KeyFileName is the VAPID private key file in the hub data directory.
//
// The key is generated once and stored rather than derived from the hub's
// SSH key: rotating one must not silently invalidate the other, and every
// browser subscription is bound to this public key (a new key means every
// device has to subscribe again).
const KeyFileName = "vapid_private.pem"

// jwtLifetime is the validity of VAPID tokens (RFC 8292 allows up to 24h).
const jwtLifetime = 12 * time.Hour

// loadOrCreateKey reads the VAPID P-256 private key at path, generating and
// storing a new one (mode 0600) when the file does not exist.
func loadOrCreateKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return parseKey(data)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// O_EXCL: if another process created the key meanwhile, use its key
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		if data, err = os.ReadFile(path); err != nil {
			return nil, err
		}
		return parseKey(data)
	}
	if err != nil {
		return nil, err
	}
	if err := pem.Encode(file, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		file.Close()
		os.Remove(path)
		return nil, err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	return key, nil
}

func parseKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid VAPID key file")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if parsed, err = x509.ParseECPrivateKey(block.Bytes); err != nil {
			return nil, fmt.Errorf("invalid VAPID key: %w", err)
		}
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("VAPID key is not a P-256 key")
	}
	return key, nil
}

// publicKeyBytes returns the uncompressed point (65 octets) of the key.
func publicKeyBytes(key *ecdsa.PrivateKey) ([]byte, error) {
	public, err := key.PublicKey.ECDH()
	if err != nil {
		return nil, err
	}
	return public.Bytes(), nil
}

// audience returns the origin of a push endpoint, the "aud" claim of VAPID.
func audience(endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid push endpoint")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// vapidToken returns a VAPID JWT (RFC 8292, ES256) for the audience.
func vapidToken(key *ecdsa.PrivateKey, aud, subject string, now time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub,omitempty"`
	}{aud, now.Add(jwtLifetime).Unix(), subject})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	// JWS ES256 signatures are R || S, each 32 octets big-endian
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// authorization returns the Authorization header value for a push endpoint:
// "vapid t=<jwt>, k=<public key>".
func authorization(key *ecdsa.PrivateKey, publicKey []byte, endpoint, subject string, now time.Time) (string, error) {
	aud, err := audience(endpoint)
	if err != nil {
		return "", err
	}
	token, err := vapidToken(key, aud, subject, now)
	if err != nil {
		return "", err
	}
	return "vapid t=" + token + ", k=" + base64.RawURLEncoding.EncodeToString(publicKey), nil
}
