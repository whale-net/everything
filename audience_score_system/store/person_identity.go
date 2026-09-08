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

	"github.com/jackc/pgx/v5/pgxpool"
)

// errPersonIdentityNotImplemented is returned by FindOrCreateByIssSub until
// the Implementation phase of issue #2116 lands the real
// ON CONFLICT (iss, sub) idiom. Scaffold exists to settle this store's
// public shape -- the interface method and migration 020's schema -- not
// the method body.
var errPersonIdentityNotImplemented = errors.New("store: PersonIdentityStore.FindOrCreateByIssSub not implemented yet (scaffold phase, see issue #2116)")

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

// FindOrCreateByIssSub -- see the interface doc comment for the full
// contract. Implementation-phase note: lands the same
// ON CONFLICT (iss, sub) DO UPDATE ... RETURNING ..., (xmax = 0) atomic
// find-or-create idiom person.go's UpsertByGoogleSubject already uses,
// joined back to `person` to hand back the full Person row (COALESCE'd
// email/display_name, exactly like GetByID) -- not stubbed further here
// since Scaffold's job is settling this method's signature and the
// migration 020 schema it reads/writes, not this body (issue #2116
// Scaffold vs. Implementation split).
func (s personIdentityStore) FindOrCreateByIssSub(ctx context.Context, iss, sub string) (Person, bool, error) {
	return Person{}, false, errPersonIdentityNotImplemented
}
