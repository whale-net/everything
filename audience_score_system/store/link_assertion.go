// link_assertion_consumption (migration 021, issue #2597, FR3/NFR1): the
// Postgres-backed, single-use ledger that makes an FR2 link assertion
// usable exactly once. Keyed on the assertion's `jti`, following the same
// pattern mcp_auth_code / libs/go/mcpauth's auth-code consumption already
// establishes -- not a new bespoke mechanism, and not an in-memory set.
// See migration 021's header for why this is a SEPARATE table from
// mcp_auth_code rather than a reuse of it.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LinkAssertionStore covers link_assertion_consumption (migration 021).
type LinkAssertionStore interface {
	// Consume records jti as used, returning ErrAssertionAlreadyConsumed
	// if it has been consumed before. Detection is via the primary-key
	// unique violation on INSERT, reusing the same SQLSTATE 23505 check
	// store/person_identity.go's isUniqueViolation already performs --
	// never a read-then-write.
	Consume(ctx context.Context, jti string, expiresAt time.Time) error

	// ReapExpired deletes consumption rows whose expires_at has passed,
	// returning the number of rows deleted. Exposed but not wired to any
	// scheduler by this task -- see this file's package doc comment.
	ReapExpired(ctx context.Context, now time.Time) (int64, error)

	// IsConsumed reports whether jti has already been recorded as consumed
	// -- a plain SELECT, never a write. This is issue #2600's GET
	// /link/whagent read-only replay check (FR3 step 3): it lets the
	// handler reject an already-used assertion before the Operator ever
	// sees the confirmation page, without performing the consuming write
	// itself -- that stays exclusively POST /link/whagent/confirm's job
	// via Consume, so an Operator who opens the confirmation page and
	// abandons it can still retry within the assertion's TTL.
	//
	// This is a convenience check only, never the single-use guarantee's
	// enforcement mechanism -- that guarantee is Consume's INSERT/
	// unique-violation path alone (see Consume's doc comment). A race
	// between this read and a concurrent Consume is harmless: the loser of
	// that race still gets Consume's authoritative
	// ErrAssertionAlreadyConsumed rejection.
	IsConsumed(ctx context.Context, jti string) (bool, error)
}

// ErrAssertionAlreadyConsumed is returned by LinkAssertionStore.Consume
// when jti has already been recorded as consumed -- the replay-rejection
// half of FR3/NFR1.
var ErrAssertionAlreadyConsumed = errors.New("store: link assertion already consumed")

// linkAssertionStore implements LinkAssertionStore against
// link_assertion_consumption (migration 021).
type linkAssertionStore struct{ pool *pgxpool.Pool }

var _ LinkAssertionStore = linkAssertionStore{}

// Consume -- see the interface doc comment for the full contract. The
// PRIMARY KEY on jti does the actual single-use enforcement: a second
// INSERT for the same jti unique-violates (SQLSTATE 23505, detected via
// isUniqueViolation from person_identity.go) rather than racing a
// SELECT-then-INSERT check, so this is safe under concurrent callers
// redeeming the same assertion.
func (s linkAssertionStore) Consume(ctx context.Context, jti string, expiresAt time.Time) error {
	if jti == "" {
		return errors.New("store: consume link assertion: jti is required")
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO link_assertion_consumption (jti, expires_at) VALUES ($1, $2)
	`, jti, expiresAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAssertionAlreadyConsumed
		}
		return fmt.Errorf("store: consume link assertion: %w", err)
	}
	return nil
}

// IsConsumed -- see the interface doc comment for the full contract. A
// plain SELECT EXISTS, deliberately never an INSERT -- see the interface
// doc comment for why this must stay read-only.
func (s linkAssertionStore) IsConsumed(ctx context.Context, jti string) (bool, error) {
	if jti == "" {
		return false, errors.New("store: is consumed link assertion: jti is required")
	}

	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM link_assertion_consumption WHERE jti = $1)
	`, jti).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: is consumed link assertion: %w", err)
	}
	return exists, nil
}

// ReapExpired -- see the interface doc comment for the full contract.
func (s linkAssertionStore) ReapExpired(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM link_assertion_consumption WHERE expires_at <= $1
	`, now)
	if err != nil {
		return 0, fmt.Errorf("store: reap expired link assertions: %w", err)
	}
	return tag.RowsAffected(), nil
}
