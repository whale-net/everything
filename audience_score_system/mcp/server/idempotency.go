// Idempotency middleware. Every write tool's input type may carry an
// optional idempotency_key string by implementing IdempotencyKeyed;
// RegisterWrite (registry.go) runs that tool's mutation under
// store.Idempotency.Do's (tool, personID, key) guard (NFR2/LB4) whenever a
// non-empty key is present -- computed here from RunIdempotent and
// computeFingerprint, the only sanctioned entry points. A tool with no key
// (or whose input doesn't implement IdempotencyKeyed) must instead be safe
// via natural-key upsert; this task's Implementation section requires
// every write tool to state which mechanism it uses.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/audience_score_system/store"
)

// IdempotencyKeyed is implemented by a write tool's input type when its
// schema includes the optional idempotency_key argument (this task's
// Implementation notes: every write tool's schema does). RegisterWrite
// type-asserts each call's decoded input against this interface; a
// nonempty key routes the call's mutation through the (tool, personID,
// key) idempotency guard, an empty key (or an input type that doesn't
// implement this interface) runs the mutation directly, relying on it
// being safe via natural-key upsert.
type IdempotencyKeyed interface {
	// IdempotencyKey returns the caller-supplied idempotency key for this
	// call, or "" if none was supplied.
	IdempotencyKey() string
}

// RunIdempotent runs fn under idempotency's (tool, personID, key) guard --
// a thin pass-through to store.Idempotency.Do (already real, migration
// 002/#1569). Called from RegisterWrite (registry.go) for every write
// tool call that carries a nonempty idempotency_key.
func RunIdempotent(ctx context.Context, idempotency store.Idempotency, tool string, personID uuid.UUID, key, fingerprint string, fn func(context.Context) (uuid.UUID, error)) (uuid.UUID, bool, error) {
	if idempotency == nil {
		return uuid.Nil, false, errors.New("idempotency store not configured")
	}
	return idempotency.Do(ctx, tool, personID, key, fingerprint, fn)
}

// computeFingerprint returns request_fingerprint (NFR2): a stable hash of
// tool plus in's JSON encoding. encoding/json marshals a Go struct's
// fields in declaration order (unlike a map), so this is stable across
// repeated calls carrying the same concrete input type and values --
// exactly what distinguishes a genuine replay (same fingerprint) from a
// same-key-different-args conflict (store.ErrIdempotencyConflict).
func computeFingerprint(tool string, in any) (string, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("marshal request for fingerprint: %w", err)
	}
	sum := sha256.Sum256(append([]byte(tool+"\x00"), body...))
	return hex.EncodeToString(sum[:]), nil
}
