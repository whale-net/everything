package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Sealer encrypts and authenticates small JSON payloads (AES-256-GCM) into
// URL-safe strings, so OAuth state, client registrations and authorization
// codes can be carried by the client instead of stored server-side — which
// keeps an authorization server correct across replicas with no database.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer derives an AES-256 key from secret (any non-empty string).
func NewSealer(secret string) (*Sealer, error) {
	if secret == "" {
		return nil, errors.New("auth: Sealer secret is required")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("auth: sealer cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("auth: sealer gcm: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal encodes v. purpose is bound to the ciphertext, so a value sealed for
// one use (e.g. "client") cannot be opened as another (e.g. "code").
func (s *Sealer) Seal(purpose string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, []byte(purpose))), nil
}

// Open decodes a value produced by Seal with the same purpose into v.
func (s *Sealer) Open(purpose, sealed string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return errors.New("auth: invalid sealed value")
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(purpose))
	if err != nil {
		return errors.New("auth: invalid sealed value")
	}
	return json.Unmarshal(plain, v)
}
