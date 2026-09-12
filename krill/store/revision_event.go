// This file (issue #2542, krill M2, FR2-FR4, NFR1) is RevisionEventStore
// -- `revision_event`'s (migration 006) accessor. Method bodies are
// scaffold stubs; the Implementation phase of issue #2542 fills them in
// against this same interface.
package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RevisionEventStore covers `revision_event` (migration 006) -- the
// append-only round log FR2 defines. NFR1 is a hard acceptance criterion,
// not a nicety: there is no Update, no Delete, and no Amend method
// anywhere on this interface, and none may be added later without
// widening NFR1 itself.
type RevisionEventStore interface {
	// Append allocates the next seq_no for e.SessionID (monotonic,
	// per-session) and inserts the new revision_event row.
	//
	// Append allocates seq_no inside a transaction that first takes a
	// `SELECT ... FOR UPDATE` lock on the owning design_session row (the
	// same pattern amend.go already uses to serialize concurrent amends
	// of one entity), then computes `COALESCE(MAX(seq_no), 0) + 1` for
	// e.SessionID -- never a plain MAX(seq_no)+1 without that lock, which
	// would race the `UNIQUE (session_id, seq_no)` index under concurrent
	// appends.
	//
	// Append sources ScopeID and both identity triples from e itself; it
	// never infers OnBehalfOf from Acting, matching session.go's
	// InitSession contract ("the store layer never infers this -- every
	// caller passes both explicitly").
	//
	// Append validates FR3's VerifiedAgainst and FR4's SignoffStatus
	// conditional-presence rules in Go, in addition to the DDL CHECK
	// constraints migration 006 declares, so a caller gets a named error
	// rather than a raw Postgres constraint violation.
	Append(ctx context.Context, e NewRevisionEvent) (RevisionEvent, error)

	// ListBySession returns every revision_event row for sessionID,
	// ordered by seq_no ascending.
	ListBySession(ctx context.Context, sessionID uuid.UUID) ([]RevisionEvent, error)
}

// revisionEventStore is the pgx-backed RevisionEventStore implementation.
type revisionEventStore struct{ pool *pgxpool.Pool }

var _ RevisionEventStore = revisionEventStore{}

// revisionEventColumns is the scan/select column list shared by every
// method below.
const revisionEventColumns = `id, scope_id, session_id, seq_no, ` +
	`acting_iss, acting_sub, acting_kind, ` +
	`on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind, ` +
	`event_type, entity_deltas, open_questions_delta, verified_against, signoff_status, created_at`

func (s revisionEventStore) Append(ctx context.Context, e NewRevisionEvent) (RevisionEvent, error) {
	return RevisionEvent{}, fmt.Errorf("store: RevisionEventStore.Append not implemented -- see issue #2542's Implementation phase")
}

func (s revisionEventStore) ListBySession(ctx context.Context, sessionID uuid.UUID) ([]RevisionEvent, error) {
	return nil, fmt.Errorf("store: RevisionEventStore.ListBySession not implemented -- see issue #2542's Implementation phase")
}
