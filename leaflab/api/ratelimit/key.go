package ratelimit

import (
	"fmt"
)

// Key is an opaque rate limiter key identifying a principal and optional session.
// It is not an IdP subject string and must not be typed as one.
// Keys are constructed via ForPrincipal and ForSession methods.
type Key struct {
	principal string
	session   string
}

// ForPrincipal creates a Key for a principal (human or non-human) without a session.
// The principal is an opaque identifier, not an IdP subject.
func ForPrincipal(principal string) Key {
	return Key{
		principal: principal,
		session:   "",
	}
}

// ForSession creates a Key for a principal with a specific session.
// The principal is an opaque identifier, not an IdP subject.
func ForSession(principal, session string) Key {
	return Key{
		principal: principal,
		session:   session,
	}
}

// String returns a stable string representation of the key for internal hashing.
// This must not be exposed as a public API; it is for internal use only.
func (k Key) string() string {
	if k.session != "" {
		return fmt.Sprintf("session:%s:%s", k.principal, k.session)
	}
	return fmt.Sprintf("principal:%s", k.principal)
}

// Principal returns the principal component of the key.
// It is exposed for logging and telemetry purposes, not for key construction.
func (k Key) Principal() string {
	return k.principal
}

// HasSession returns true if this key includes a session component.
func (k Key) HasSession() bool {
	return k.session != ""
}
