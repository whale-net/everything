// person_oidc_identity (migration 020, issue #2116, FR12(b)): the
// (iss, sub) -> Person mapping the whagent-net authentication path
// resolves every verified whagent Claim's on-behalf-of subject against,
// auto-provisioning a Person the first time a given (iss, sub) pair is
// seen. Deliberately a SEPARATE store/table from person.go's
// google_subject / UpsertByGoogleSubject -- see migration 020's header for
// why the two identity keys must not be merged or re-keyed off each
// other.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PersonIdentityStore covers person_oidc_identity (migration 020).
type PersonIdentityStore interface {
	// FindOrCreateByIssSub resolves the (iss, sub) pair -- a verified
	// whagent Claim's SubjectIssuer/Subject, LB2's shape verbatim -- to a
	// Person, auto-provisioning an identity-key-only Person row (no
	// email/display name; FR10 forbids the claim from carrying either) the
	// first time this exact pair is seen. created reports whether a new
	// Person row was inserted. Keyed on the PAIR, never on sub alone: the
	// same sub under two different iss values must resolve to two
	// distinct persons (see migration 020's header).
	FindOrCreateByIssSub(ctx context.Context, iss, sub string) (Person, bool, error)
}

// personIdentityStore implements PersonIdentityStore against
// person_oidc_identity (migration 020).
type personIdentityStore struct{ pool *pgxpool.Pool }

var _ PersonIdentityStore = personIdentityStore{}

// personIdentityColumns is the person_oidc_identity -> person join's SELECT
// list, in Person scan order -- shared by the fast-path lookup and the
// post-race re-lookup below so the two can never drift on which columns
// (or COALESCE rules) they read.
const personIdentityColumns = `p.id, COALESCE(p.google_subject, ''), COALESCE(p.email, ''), COALESCE(p.display_name, ''), p.created_at`

// lookup resolves (iss, sub) to its already-linked Person, or pgx.ErrNoRows
// if this pair has never been seen.
func (s personIdentityStore) lookup(ctx context.Context, iss, sub string) (Person, error) {
	var p Person
	err := s.pool.QueryRow(ctx, `
		SELECT `+personIdentityColumns+`
		FROM person_oidc_identity i
		JOIN person p ON p.id = i.person_id
		WHERE i.iss = $1 AND i.sub = $2
	`, iss, sub).Scan(&p.ID, &p.GoogleSubject, &p.Email, &p.DisplayName, &p.CreatedAt)
	return p, err
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505), via errors.As against *pgconn.PgError rather
// than string-matching -- so it keeps working regardless of how pgx wraps
// the underlying error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// FindOrCreateByIssSub -- see the interface doc comment for the full
// contract.
//
// Unlike UpsertByGoogleSubject's single-statement ON CONFLICT idiom, this
// find-or-create spans two tables (person, person_oidc_identity are
// deliberately separate -- see migration 020's header), so no single
// statement's ON CONFLICT can cover it. Instead:
//
//  1. Fast path (no transaction): look the pair up directly. This is the
//     common case -- every call after the first for a given (iss, sub).
//  2. Not found: open a transaction, INSERT a fresh identity-key-only
//     Person (NULL google_subject/email/display_name -- see migration
//     020's up.sql for why google_subject had to become nullable for
//     this), then INSERT the (person_id, iss, sub) link row.
//  3. If step 2's link insert unique-violates person_oidc_identity_iss_sub,
//     a concurrent call for the exact same pair won the race and committed
//     first. Postgres's own unique-index locking (not app-level retry
//     logic) is what makes this detectable rather than silently
//     duplicating: the losing transaction's INSERT either blocks on the
//     winner's uncommitted index entry and then fails once the winner
//     commits, or fails immediately if the winner already had. Either way
//     this transaction rolls back in full -- including the speculative
//     Person row step 2 created, via the deferred Rollback below -- so
//     losing the race never leaves an orphaned, identity-less Person
//     behind. The pair is then re-looked-up to hand back the winner's
//     Person instead.
//
// This is what guarantees "first call for an unseen (iss, sub) creates
// exactly one Person row" holds even under concurrent first-sight calls.
func (s personIdentityStore) FindOrCreateByIssSub(ctx context.Context, iss, sub string) (Person, bool, error) {
	if iss == "" || sub == "" {
		return Person{}, false, errors.New("find or create person identity: iss and sub are both required")
	}

	if p, err := s.lookup(ctx, iss, sub); err == nil {
		return p, false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Person{}, false, fmt.Errorf("find or create person identity: lookup: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Person{}, false, fmt.Errorf("find or create person identity: begin tx: %w", err)
	}
	// The unique-violation branch below rolls tx back explicitly (see that
	// branch for why) before this defer ever fires; when it does, this
	// call is a no-op (tx.Rollback on an already-closed tx just returns
	// pgx.ErrTxClosed, which is fine to ignore). Otherwise this defer is
	// either the rollback that undoes a lost race, or a no-op once Commit
	// has succeeded.
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit -- or the explicit Rollback below -- has already run.

	var p Person
	if err := tx.QueryRow(ctx, `
		INSERT INTO person (google_subject, email, display_name)
		VALUES (NULL, NULL, NULL)
		RETURNING id, COALESCE(google_subject, ''), COALESCE(email, ''), COALESCE(display_name, ''), created_at
	`).Scan(&p.ID, &p.GoogleSubject, &p.Email, &p.DisplayName, &p.CreatedAt); err != nil {
		return Person{}, false, fmt.Errorf("find or create person identity: auto-provision person: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO person_oidc_identity (person_id, iss, sub) VALUES ($1, $2, $3)
	`, p.ID, iss, sub); err != nil {
		if isUniqueViolation(err) {
			// Lost the race for this exact (iss, sub) pair. Roll back
			// explicitly -- and *before* the re-lookup below -- rather than
			// relying on the deferred Rollback at function exit: the
			// re-lookup below acquires its own connection from s.pool, and
			// under a saturated pool (e.g. a concurrent-race test driving
			// more losers than the pool has connections) every loser
			// holding its own tx connection open while *also* blocking on a
			// second connection for the re-lookup is a self-deadlock --
			// every held connection is waiting on one more connection from
			// the very pool it's exhausting. Rolling back first returns
			// this connection to the pool before asking for another, so
			// the re-lookup can never deadlock against sibling losers.
			// Rollback here also still discards the Person row just
			// INSERTed, so it never becomes a second, orphaned row for
			// this pair; the deferred Rollback above becomes a no-op.
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
				return Person{}, false, fmt.Errorf("find or create person identity: rollback after concurrent create: %w", rollbackErr)
			}
			existing, lookupErr := s.lookup(ctx, iss, sub)
			if lookupErr != nil {
				return Person{}, false, fmt.Errorf("find or create person identity: resolve after concurrent create: %w", lookupErr)
			}
			return existing, false, nil
		}
		return Person{}, false, fmt.Errorf("find or create person identity: link identity: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Person{}, false, fmt.Errorf("find or create person identity: commit: %w", err)
	}

	return p, true, nil
}
