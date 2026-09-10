package grpcauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// GrantKeySize is the required length, in bytes, of the symmetric key used to
// protect delegated-grant token material. AES-256-GCM demands exactly 32 bytes
// (NFR2).
const GrantKeySize = 32

// ErrInvalidGrantKey is returned when a supplied key is not exactly
// GrantKeySize bytes. It reports the expected size only — never any part of
// the supplied key (NFR1).
var ErrInvalidGrantKey = errors.New("grpcauth: grant encryption key must be 32 bytes")

// errDecryptFailed is returned for any failure while opening a ciphertext:
// truncated input or GCM authentication failure. It deliberately carries no
// detail about which case occurred, so it never risks leaking plaintext or
// key material (NFR1).
var errDecryptFailed = errors.New("grpcauth: decrypt failed")

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != GrantKeySize {
		return nil, ErrInvalidGrantKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		// aes.NewCipher only fails on bad key length, already checked above,
		// but guard anyway without embedding key bytes in the error.
		return nil, fmt.Errorf("grpcauth: %w", ErrInvalidGrantKey)
	}
	return cipher.NewGCM(block)
}

// Encrypt seals plaintext with AES-256-GCM under key, returning
// nonce || ciphertext. A fresh random nonce is generated per call, so
// encrypting the same plaintext twice yields different outputs.
//
// key must be exactly GrantKeySize bytes; the caller derives it from its own
// configuration (the same shape as audience_score_system's
// ASS_TOKEN_ENCRYPTION_KEY-derived key). This package never reads an
// environment variable itself.
//
// Returned errors never embed plaintext or key bytes (NFR1).
func Encrypt(key []byte, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("grpcauth: generate nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt opens a nonce || ciphertext blob produced by Encrypt under key.
//
// It fails if the key is the wrong size, if the blob is shorter than a nonce,
// or if GCM authentication fails (wrong key or tampered ciphertext). Returned
// errors never embed plaintext or key bytes (NFR1).
func Decrypt(key []byte, ciphertext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errDecryptFailed
	}
	nonce, sealed := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, errDecryptFailed
	}
	return plaintext, nil
}
