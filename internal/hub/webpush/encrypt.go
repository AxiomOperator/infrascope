package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
)

// recordSize is the aes128gcm record size advertised in the header. Push
// messages fit in one record, so this only bounds the plaintext.
const recordSize = 4096

// headerSize is the size of the aes128gcm header with a P-256 key id:
// salt (16) || rs (4) || idlen (1) || keyid (65).
const headerSize = 16 + 4 + 1 + 65

var errInvalidKeys = errors.New("invalid subscription keys")

// decodeBase64 decodes base64url or standard base64, padded or not, as
// browsers and libraries serialize subscription keys either way.
func decodeBase64(value string) ([]byte, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "=")
	value = strings.NewReplacer("+", "-", "/", "_").Replace(value)
	return base64.RawURLEncoding.DecodeString(value)
}

// parseKeys decodes and validates the p256dh (uncompressed P-256 point) and
// auth (16 octet secret) keys of a subscription.
func parseKeys(p256dh, auth string) (*ecdh.PublicKey, []byte, error) {
	rawKey, err := decodeBase64(p256dh)
	if err != nil || len(rawKey) != 65 || rawKey[0] != 0x04 {
		return nil, nil, errInvalidKeys
	}
	publicKey, err := ecdh.P256().NewPublicKey(rawKey)
	if err != nil {
		return nil, nil, errInvalidKeys
	}
	secret, err := decodeBase64(auth)
	if err != nil || len(secret) != 16 {
		return nil, nil, errInvalidKeys
	}
	return publicKey, secret, nil
}

// encrypt encrypts a push message for a user agent per RFC 8291 using the
// aes128gcm content coding of RFC 8188, as a single record. asPrivate is the
// application server's ephemeral ECDH key and salt a random 16 octet salt;
// both are generated when nil (tests pass fixed values).
func encrypt(plaintext []byte, uaPublic *ecdh.PublicKey, authSecret []byte, asPrivate *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext)+1+16 > recordSize {
		return nil, errors.New("push message too large")
	}
	var err error
	if asPrivate == nil {
		if asPrivate, err = ecdh.P256().GenerateKey(rand.Reader); err != nil {
			return nil, err
		}
	}
	if salt == nil {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
	}
	ecdhSecret, err := asPrivate.ECDH(uaPublic)
	if err != nil {
		return nil, err
	}
	asPublic := asPrivate.PublicKey().Bytes()
	// key_info = "WebPush: info" || 0x00 || ua_public || as_public
	keyInfo := "WebPush: info\x00" + string(uaPublic.Bytes()) + string(asPublic)
	ikm, err := hkdf.Key(sha256.New, ecdhSecret, authSecret, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, headerSize+len(plaintext)+1+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	// the last (and only) record ends with the 0x02 padding delimiter
	record := append(append(make([]byte, 0, len(plaintext)+1), plaintext...), 0x02)
	return gcm.Seal(out, nonce, record, nil), nil
}

// ValidateKeys checks the p256dh and auth keys of a browser subscription
// (base64url, as in PushSubscription.toJSON()).
func ValidateKeys(p256dh, auth string) error {
	_, _, err := parseKeys(p256dh, auth)
	return err
}
