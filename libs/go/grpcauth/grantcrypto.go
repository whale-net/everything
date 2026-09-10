package grpcauth

import "errors"

// GrantKeySize is the required length, in bytes, of the symmetric key used to
// protect delegated-grant token material. AES-256-GCM demands exactly 32 bytes
// (NFR2).
const GrantKeySize = 32

// ErrInvalidGrantKey is returned when a supplied key is not exactly
// GrantKeySize bytes. It reports the expected size only — never any part of
// the supplied key (NFR1).
var ErrInvalidGrantKey = errors.New("grpcauth: grant encryption key must be 32 bytes")

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
	// TODO(implementation): crypto/aes + crypto/cipher GCM, crypto/rand nonce.
	return nil, errors.New("grpcauth: Encrypt not implemented")
}

// Decrypt opens a nonce || ciphertext blob produced by Encrypt under key.
//
// It fails if the key is the wrong size, if the blob is shorter than a nonce,
// or if GCM authentication fails (wrong key or tampered ciphertext). Returned
// errors never embed plaintext or key bytes (NFR1).
func Decrypt(key []byte, ciphertext []byte) ([]byte, error) {
	// TODO(implementation): validate key, split nonce, GCM Open.
	return nil, errors.New("grpcauth: Decrypt not implemented")
}
