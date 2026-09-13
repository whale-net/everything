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

	"github.com/google/uuid"
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

	// LinkToExistingPerson links (iss, sub) to an EXISTING personID -- the
	// link-to-existing path FR6 uses instead of FindOrCreateByIssSub's
	// auto-provisioning branch.
	//
	// FR8: if the pair already has a row pointing at a DIFFERENT Person, it
	// returns ErrLinkedToOtherPerson and writes nothing -- no merge, no
	// reassignment, no overwrite.
	// FR9: if the pair already points at personID, it returns
	// LinkAlreadyOwned with no error and no duplicate row.
	//
	// No person row is ever created by this method -- it links to a
	// Person that already exists (the signed-in Google-session Person). A
	// personID that doesn't exist must fail on the foreign key, not
	// silently create anything.
	LinkToExistingPerson(ctx context.Context, iss, sub string, personID uuid.UUID) (LinkOutcome, error)
}

// LinkOutcome reports which of LinkToExistingPerson's two non-error
// outcomes occurred (FR6/FR9). See ErrLinkedToOtherPerson for the
// conflict case (FR8), which is an error, not a LinkOutcome value.
type LinkOutcome int

const (
	// LinkCreated (FR6): a new person_oidc_identity row was written,
	// linking (iss, sub) to the given personID.
	LinkCreated LinkOutcome = iota
	// LinkAlreadyOwned (FR9): this (iss, sub) pair already points at this
	// same personID -- a no-op, not an error, and no duplicate row.
	LinkAlreadyOwned
)

// ErrLinkedToOtherPerson (FR8) is returned by LinkToExistingPerson when
// (iss, sub) already has a person_oidc_identity row pointing at a
// DIFFERENT Person than the one requested. Never merge, reassign, or
// overwrite in this case -- fail safe instead.
var ErrLinkedToOtherPerson = errors.New("person identity: (iss, sub) is already linked to a different person")

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

// LinkToExistingPerson -- see the interface doc comment for the full
// contract (FR6-FR9, NFR4).
//
// Unlike FindOrCreateByIssSub, there is no fast-path lookup and no Person
// row to speculatively create: this method only ever writes the
// person_oidc_identity link row itself, inside a single transaction
// (NFR4), and lets the person_id foreign key reject an unknown personID
// rather than ever creating one.
//
//  1. INSERT the (person_id, iss, sub) link row.
//  2. If that unique-violates person_oidc_identity_iss_sub (FR8/FR9), a
//     row for this exact (iss, sub) pair already exists -- either written
//     by a previous call to this method, or auto-provisioned by
//     FindOrCreateByIssSub. Roll back first (same reasoning as
//     FindOrCreateByIssSub's own race branch: return this connection to
//     the pool before asking for another one for the re-lookup, so
//     concurrent losers can't self-deadlock a saturated pool), then
//     re-read the row to decide FR9 (same person_id: LinkAlreadyOwned, no
//     error) from FR8 (different person_id: ErrLinkedToOtherPerson, and
//     nothing is merged, reassigned, or overwritten).
//  3. Any other insert error (notably an unknown personID failing the
//     person_id foreign key) is returned as-is; the deferred Rollback
//     below discards the failed transaction, leaving no partial row.
func (s personIdentityStore) LinkToExistingPerson(ctx context.Context, iss, sub string, personID uuid.UUID) (LinkOutcome, error) {
	if iss == "" || sub == "" {
		return 0, errors.New("link to existing person: iss and sub are both required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("link to existing person: begin tx: %w", err)
	}
	// See FindOrCreateByIssSub's identical defer for why this is safe as a
	// no-op once either Commit or the explicit Rollback below has run.
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit -- or the explicit Rollback below -- has already run.

	if _, err := tx.Exec(ctx, `
		INSERT INTO person_oidc_identity (person_id, iss, sub) VALUES ($1, $2, $3)
	`, personID, iss, sub); err != nil {
		if isUniqueViolation(err) {
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
				return 0, fmt.Errorf("link to existing person: rollback after conflict: %w", rollbackErr)
			}
			existing, lookupErr := s.lookup(ctx, iss, sub)
			if lookupErr != nil {
				return 0, fmt.Errorf("link to existing person: resolve after conflict: %w", lookupErr)
			}
			if existing.ID == personID {
				return LinkAlreadyOwned, nil
			}
			return 0, ErrLinkedToOtherPerson
		}
		return 0, fmt.Errorf("link to existing person: insert link: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("link to existing person: commit: %w", err)
	}

	return LinkCreated, nil
}
