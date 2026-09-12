package grpcauth

import (
	"bytes"
	"strings"
	"testing"
)

func testKey(b byte) []byte {
	key := make([]byte, GrantKeySize)
	for i := range key {
		key[i] = b
	}
	return key
}

// TestEncryptDecryptRoundTrip proves Decrypt(Encrypt(x)) == x for the basic
// happy path (NFR2).
func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := testKey(0x01)
	plaintext := []byte("refresh-token-material")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	got, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round trip mismatch: got %q, want %q", got, plaintext)
	}
}

// TestEncryptNonceRandomness proves two encryptions of identical plaintext
// under the same key produce different ciphertexts, i.e. the nonce is
// actually random per call and not reused (NFR2).
func TestEncryptNonceRandomness(t *testing.T) {
	key := testKey(0x02)
	plaintext := []byte("same plaintext every time")

	c1, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt #1: %v", err)
	}
	c2, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt #2: %v", err)
	}
	if bytes.Equal(c1, c2) {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext; nonce is not random")
	}
}

// TestDecryptWrongKeyFails proves a ciphertext sealed under one key cannot be
// opened with a different key.
func TestDecryptWrongKeyFails(t *testing.T) {
	key := testKey(0x03)
	wrongKey := testKey(0x04)
	plaintext := []byte("secret")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if _, err := Decrypt(wrongKey, ciphertext); err == nil {
		t.Fatal("Decrypt succeeded with the wrong key")
	}
}

// TestDecryptTruncatedFails proves a truncated ciphertext is rejected rather
// than silently returning garbage or panicking.
func TestDecryptTruncatedFails(t *testing.T) {
	key := testKey(0x05)
	plaintext := []byte("secret material")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Truncate to shorter than a nonce.
	if _, err := Decrypt(key, ciphertext[:4]); err == nil {
		t.Fatal("Decrypt succeeded on input shorter than a nonce")
	}

	// Truncate mid-ciphertext (longer than nonce, but incomplete).
	truncated := ciphertext[:len(ciphertext)-4]
	if _, err := Decrypt(key, truncated); err == nil {
		t.Fatal("Decrypt succeeded on truncated ciphertext")
	}
}

// TestDecryptTamperedFails proves GCM authentication catches a flipped byte
// in the ciphertext body.
func TestDecryptTamperedFails(t *testing.T) {
	key := testKey(0x06)
	plaintext := []byte("secret material that is long enough to tamper with safely")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	// Flip a byte well past the nonce, inside the sealed body.
	idx := len(tampered) - 1
	tampered[idx] ^= 0xFF

	if _, err := Decrypt(key, tampered); err == nil {
		t.Fatal("Decrypt succeeded on tampered ciphertext; GCM auth should have failed")
	}
}

// TestEncryptRejectsBadKeySize and TestDecryptRejectsBadKeySize prove both
// paths reject any key that is not exactly GrantKeySize bytes.
func TestEncryptRejectsBadKeySize(t *testing.T) {
	for _, size := range []int{0, 1, 16, 24, 31, 33, 64} {
		if _, err := Encrypt(make([]byte, size), []byte("x")); err == nil {
			t.Errorf("Encrypt accepted a %d-byte key", size)
		}
	}
}

func TestDecryptRejectsBadKeySize(t *testing.T) {
	for _, size := range []int{0, 1, 16, 24, 31, 33, 64} {
		if _, err := Decrypt(make([]byte, size), []byte("irrelevant-ciphertext")); err == nil {
			t.Errorf("Decrypt accepted a %d-byte key", size)
		}
	}
}

// TestDecryptFailureDoesNotLeakSecrets proves NFR1: whatever error a failed
// decrypt returns, its message never contains the plaintext or the key bytes.
func TestDecryptFailureDoesNotLeakSecrets(t *testing.T) {
	key := testKey(0x07)
	plaintext := []byte("do-not-leak-this-plaintext")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[len(tampered)-1] ^= 0xFF

	_, decErr := Decrypt(key, tampered)
	if decErr == nil {
		t.Fatal("expected Decrypt to fail on tampered ciphertext")
	}
	msg := decErr.Error()
	if strings.Contains(msg, string(plaintext)) {
		t.Fatalf("decrypt error leaks plaintext: %q", msg)
	}
	if strings.Contains(msg, string(key)) {
		t.Fatalf("decrypt error leaks key bytes: %q", msg)
	}

	// Also check the wrong-key-size error path.
	_, sizeErr := Decrypt([]byte("too-short"), ciphertext)
	if sizeErr == nil {
		t.Fatal("expected Decrypt to fail on a bad key size")
	}
	sizeMsg := sizeErr.Error()
	if strings.Contains(sizeMsg, "too-short") {
		t.Fatalf("bad-key-size error leaks key bytes: %q", sizeMsg)
	}
}
