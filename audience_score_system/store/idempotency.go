package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
type idempotencyStore struct{ pool *pgxpool.Pool }

var _ Idempotency = idempotencyStore{}

// Do looks up the (tool, personID, key) triple's mcp_idempotency row
// first: a matching fingerprint replays result_ref without running fn
// again; a differing fingerprint returns ErrIdempotencyConflict without
// running fn; no row runs fn and records its result under a new row keyed
// by the PRIMARY KEY (tool_name, person_id, idempotency_key) -- the same
// constraint that makes a genuinely concurrent double-insert for the same
// key fail loudly rather than silently duplicating state.
func (s idempotencyStore) Do(ctx context.Context, tool string, personID uuid.UUID, key, fingerprint string, fn func(context.Context) (uuid.UUID, error)) (uuid.UUID, bool, error) {
	var existingFingerprint string
	var resultRef *uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT request_fingerprint, result_ref
		FROM mcp_idempotency
		WHERE tool_name = $1 AND person_id = $2 AND idempotency_key = $3
	`, tool, personID, key).Scan(&existingFingerprint, &resultRef)
	switch {
	case err == nil:
		if existingFingerprint != fingerprint {
			return uuid.Nil, false, ErrIdempotencyConflict
		}
		if resultRef == nil {
			return uuid.Nil, true, nil
		}
		return *resultRef, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		// No prior record -- fall through to the first-run path below.
	default:
		return uuid.Nil, false, fmt.Errorf("lookup mcp_idempotency: %w", err)
	}

	result, err := fn(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO mcp_idempotency (tool_name, person_id, idempotency_key, request_fingerprint, result_ref)
		VALUES ($1, $2, $3, $4, $5)
	`, tool, personID, key, fingerprint, result); err != nil {
		return uuid.Nil, false, fmt.Errorf("record mcp_idempotency: %w", err)
	}

	return result, false, nil
}
