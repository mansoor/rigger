// Package crypto provides authenticated symmetric encryption for secrets stored
// at rest (Phase 7: remote-host SSH private keys). The key is derived from the
// server's JWT secret via HKDF-SHA256, so no separate key material is needed —
// rotating JWT_SECRET invalidates stored ciphertexts (they must be re-entered).
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// hkdfInfo domain-separates this key from any other future use of the JWT secret.
const hkdfInfo = "rigger/phase7/host-ssh-key/v1"

// DeriveKey derives a stable 32-byte AES-256 key from the JWT secret using
// HKDF-SHA256. The same secret always yields the same key.
func DeriveKey(jwtSecret []byte) ([]byte, error) {
	if len(jwtSecret) == 0 {
		return nil, errors.New("crypto: empty JWT secret")
	}
	r := hkdf.New(sha256.New, jwtSecret, nil, []byte(hkdfInfo))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("crypto: derive key: %w", err)
	}
	return key, nil
}

// Encrypt seals plaintext with AES-256-GCM and returns base64(nonce || ciphertext).
func Encrypt(key, plaintext []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. It fails (non-nil error) on a wrong key or tampered
// ciphertext.
func Decrypt(key []byte, encoded string) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("crypto: base64: %w", err)
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("crypto: ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: decrypt: %w", err)
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("crypto: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
