package store

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// idempotencyPollInterval is how often a caller that lost the reservation
// race re-checks for the winner's result. Deliberately short -- the
// winner's own critical section (fn plus one UPDATE) is expected to be a
// handful of Postgres round trips, not a long-running operation.
const idempotencyPollInterval = 25 * time.Millisecond

// Do reserves the (tool, personID, key) triple's mcp_idempotency row via
// an atomic INSERT ... ON CONFLICT DO NOTHING before running fn, rather
// than a SELECT-then-run-then-INSERT: a plain SELECT-then-INSERT lets N
// concurrent callers all observe "no row" and all run fn, breaking the
// "run exactly once" contract and surfacing the losing INSERTs' raw
// unique_violation to their callers instead of a replay. Only the caller
// whose INSERT actually creates the row (result_ref starts NULL) runs fn;
// every other concurrent caller's INSERT is a no-op, and it falls through
// to pollExisting below to wait for and then replay the winner's result.
//
// This does not hold any connection or lock across fn's execution -- each
// SQL statement here is an independent, quick round trip that returns its
// connection to the pool immediately. That matters because fn itself is
// arbitrary tool logic that very likely needs its own connections from
// this same pool: holding a connection (via a long-lived transaction or an
// advisory lock's dedicated connection) for the whole span of fn would let
// fn's own pool usage deadlock against the connection this call is
// holding, especially under a small pool. See #1600/#1611 for the
// TOCTOU race this method now closes, and the earlier
// transaction+pg_advisory_xact_lock attempt this replaced, which
// reintroduced exactly that deadlock (confirmed live via CI's
// Test Database Integration job timing out).
func (s idempotencyStore) Do(ctx context.Context, tool string, personID uuid.UUID, key, fingerprint string, fn func(context.Context) (uuid.UUID, error)) (uuid.UUID, bool, error) {
	for {
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO mcp_idempotency (tool_name, person_id, idempotency_key, request_fingerprint, result_ref)
			VALUES ($1, $2, $3, $4, NULL)
			ON CONFLICT (tool_name, person_id, idempotency_key) DO NOTHING
		`, tool, personID, key, fingerprint)
		if err != nil {
			return uuid.Nil, false, fmt.Errorf("reserve mcp_idempotency: %w", err)
		}

		if tag.RowsAffected() == 1 {
			return s.runAndFill(ctx, tool, personID, key, fn)
		}

		// Lost the reservation race -- someone else already owns this key.
		result, replay, done, err := s.pollExisting(ctx, tool, personID, key, fingerprint)
		if done {
			return result, replay, err
		}
		// The prior owner's fn failed and deleted its reservation before we
		// polled it -- loop around and race to become the new owner.
	}
}

// runAndFill runs fn having already won the reservation race, then fills
// in the reserved row's result_ref. If fn fails, the reservation is
// deleted rather than left as a permanently-unfillable NULL row, so a
// later retry with the same key can run fn again.
func (s idempotencyStore) runAndFill(ctx context.Context, tool string, personID uuid.UUID, key string, fn func(context.Context) (uuid.UUID, error)) (uuid.UUID, bool, error) {
	result, err := fn(ctx)
	if err != nil {
		if _, delErr := s.pool.Exec(ctx, `
			DELETE FROM mcp_idempotency
			WHERE tool_name = $1 AND person_id = $2 AND idempotency_key = $3 AND result_ref IS NULL
		`, tool, personID, key); delErr != nil {
			return uuid.Nil, false, fmt.Errorf("%w (also failed to release failed reservation: %v)", err, delErr)
		}
		return uuid.Nil, false, err
	}

	if _, err := s.pool.Exec(ctx, `
		UPDATE mcp_idempotency SET result_ref = $4
		WHERE tool_name = $1 AND person_id = $2 AND idempotency_key = $3
	`, tool, personID, key, result); err != nil {
		return uuid.Nil, false, fmt.Errorf("record mcp_idempotency result: %w", err)
	}

	return result, false, nil
}

// pollExisting waits for a losing caller's key to resolve: a differing
// fingerprint is an immediate conflict (done=true); a matching fingerprint
// with a filled-in result_ref is an immediate replay (done=true); a
// matching fingerprint with a still-NULL result_ref means the owner's fn
// is still running, so this sleeps idempotencyPollInterval and retries; a
// vanished row means the owner's fn failed and deleted its reservation
// (done=false, telling Do to loop around and try to become the new
// owner).
func (s idempotencyStore) pollExisting(ctx context.Context, tool string, personID uuid.UUID, key, fingerprint string) (uuid.UUID, bool, bool, error) {
	for {
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
				return uuid.Nil, false, true, ErrIdempotencyConflict
			}
			if resultRef != nil {
				return *resultRef, true, true, nil
			}
			// Still in flight -- wait and check again.
		case errors.Is(err, pgx.ErrNoRows):
			return uuid.Nil, false, false, nil
		default:
			return uuid.Nil, false, true, fmt.Errorf("poll mcp_idempotency: %w", err)
		}

		select {
		case <-ctx.Done():
			return uuid.Nil, false, true, ctx.Err()
		case <-time.After(idempotencyPollInterval):
		}
	}
}
