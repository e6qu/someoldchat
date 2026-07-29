package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const version = "v1"

var ErrInvalidCiphertext = errors.New("secret ciphertext is invalid")

// ParseKeyHex decodes the operator-facing AES-256 key without reflecting any
// part of a malformed secret into an error or process log.
func ParseKeyHex(encoded string) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(key) != 32 {
		return nil, errors.New("application credential key must contain exactly 32 bytes of hex")
	}
	return key, nil
}

// Seal encrypts one application credential with AES-256-GCM. associatedData
// binds the ciphertext to its app and purpose, so copying a signing-secret
// ciphertext to another app or credential column makes it undecryptable.
func Seal(key []byte, associatedData, plaintext string) (string, error) {
	if len(key) != 32 {
		return "", errors.New("application credential key must be 32 bytes")
	}
	if strings.TrimSpace(associatedData) == "" || plaintext == "" {
		return "", errors.New("application credential and associated data are required")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, nonce, []byte(plaintext), []byte(associatedData))
	payload := append(nonce, sealed...)
	return version + "." + base64.RawURLEncoding.EncodeToString(payload), nil
}

func Open(key []byte, associatedData, encoded string) (string, error) {
	if len(key) != 32 {
		return "", errors.New("application credential key must be 32 bytes")
	}
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 || parts[0] != version || strings.TrimSpace(associatedData) == "" {
		return "", ErrInvalidCiphertext
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCiphertext, err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(payload) < aead.NonceSize()+aead.Overhead() {
		return "", ErrInvalidCiphertext
	}
	plaintext, err := aead.Open(nil, payload[:aead.NonceSize()], payload[aead.NonceSize():], []byte(associatedData))
	if err != nil {
		return "", fmt.Errorf("%w: authentication failed", ErrInvalidCiphertext)
	}
	return string(plaintext), nil
}
