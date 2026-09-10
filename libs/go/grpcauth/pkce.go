package grpcauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// randomURLSafeBytes is the byte length of every random token this file
// generates (PKCE code_verifier, CSRF state) before base64url encoding: 32
// bytes -> 43 base64url characters, comfortably inside RFC 7636's 43-128
// character requirement for a code_verifier and plenty of entropy for a CSRF
// state (NFR5).
const randomURLSafeBytes = 32

// randomURLSafeToken returns a crypto/rand-sourced, base64url (no padding)
// encoded random token. Shared by generatePKCEVerifier and the per-flow CSRF
// state generator in delegatedgrant_authcode.go -- both just need
// unpredictable, URL-safe bytes.
func randomURLSafeToken() (string, error) {
	buf := make([]byte, randomURLSafeBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("grpcauth: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// generatePKCEVerifier returns a new random RFC 7636 PKCE code_verifier.
func generatePKCEVerifier() (string, error) {
	verifier, err := randomURLSafeToken()
	if err != nil {
		return "", fmt.Errorf("grpcauth: generate PKCE verifier: %w", err)
	}
	return verifier, nil
}

// pkceChallengeS256 derives the RFC 7636 S256 code_challenge from verifier:
// base64url(sha256(verifier)) with no padding.
//
// PKCE math note: this and generatePKCEVerifier duplicate the derivation
// also present (as the *verify* side) in libs/go/mcpauth/pkce.go, which
// implements PKCE for mcpauth's own role as an authorization server.
// Depending on mcpauth from here would pull an authorization-server package
// into grpcauth's dependency graph for ~10 lines of math; core grpcauth must
// not gain an mcpauth dependency, so the derivation is copied rather than
// shared.
func pkceChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
