package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrIdempotencyConflict is returned by Idempotency.Do when the same
// (tool, person, key) triple arrives with a request_fingerprint that
// differs from the one recorded on the first call -- distinguishable from
// a plain replay so callers can reject the request rather than silently
// reusing a stale result.
var ErrIdempotencyConflict = errors.New("idempotency key reused with a different request fingerprint")

// Idempotency is the NFR2/LB4 helper every MCP write-back tool reuses,
// backed entirely by `mcp_idempotency` (migration 002) -- no in-memory
// caches anywhere in this codebase substitute for it.
type Idempotency interface {
	// Do runs fn under the (tool, personID, key) idempotency guard:
	//   - first call: runs fn, records its result under result_ref, and
	//     returns (result, false, nil).
	//   - replay (same tool/person/key, same fingerprint): does NOT run
	//     fn again; returns the previously recorded result_ref and
	//     (result, true, nil).
	//   - conflict (same tool/person/key, different fingerprint): does NOT
	//     run fn; returns ErrIdempotencyConflict.
	// Holds no state outside Postgres (LB4).
	Do(ctx context.Context, tool string, personID uuid.UUID, key, fingerprint string, fn func(context.Context) (uuid.UUID, error)) (uuid.UUID, bool, error)
}

// idempotencyStore implements Idempotency against `mcp_idempotency`
// (migration 002).
//
// Scaffold only -- Do is a stub. Full implementation lands in the
// Implementation phase (issue #1569's "Implementation" scope).
type idempotencyStore struct{ pool *pgxpool.Pool }

var _ Idempotency = idempotencyStore{}

func (s idempotencyStore) Do(ctx context.Context, tool string, personID uuid.UUID, key, fingerprint string, fn func(context.Context) (uuid.UUID, error)) (uuid.UUID, bool, error) {
	return uuid.Nil, false, errors.New("not implemented")
}
