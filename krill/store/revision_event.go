// This file (issue #2542, krill M2, FR2-FR4, NFR1) is RevisionEventStore
// -- `revision_event`'s (migration 008) accessor.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RevisionEventStore covers `revision_event` (migration 008) -- the
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
	// constraints migration 008 declares, so a caller gets a named error
	// rather than a raw Postgres constraint violation.
	Append(ctx context.Context, e NewRevisionEvent) (RevisionEvent, error)

	// ListBySession returns every revision_event row for sessionID,
	// ordered by seq_no ascending.
	ListBySession(ctx context.Context, sessionID uuid.UUID) ([]RevisionEvent, error)

	// ListOpenQuestions returns sessionID's currently open questions --
	// FR6's derived, last-event-wins view over open_questions_delta. See
	// open_questions.go for the implementation and why it is one SQL
	// window query, never a folded read of ListBySession's full log.
	ListOpenQuestions(ctx context.Context, sessionID uuid.UUID) ([]OpenQuestion, error)
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

// ErrInvalidRevisionEvent is returned by RevisionEventStore.Append when e
// fails FR3's VerifiedAgainst rule, FR4's SignoffStatus rule, or FR2's
// entity_deltas.change enum, checked in Go ahead of the INSERT -- a caller
// gets this named error instead of a raw Postgres CHECK-constraint
// violation for the same condition.
var ErrInvalidRevisionEvent = errors.New("krill/store: invalid revision event")

// validateNewRevisionEvent enforces, in Go, the same conditional-presence
// rules migration 008's DDL CHECK constraints declare (FR3, FR4), plus
// FR2's entity_deltas.change enum, which has no DDL-level CHECK (JSONB
// contents aren't constrained by the schema) and so is enforced here only.
func validateNewRevisionEvent(e NewRevisionEvent) error {
	requiresVerifiedAgainst := e.EventType == EventTypeDraft || e.EventType == EventTypeReconciliation
	switch {
	case requiresVerifiedAgainst && (e.VerifiedAgainst == nil || *e.VerifiedAgainst == ""):
		return fmt.Errorf("%w: verified_against is required for event_type %q", ErrInvalidRevisionEvent, e.EventType)
	case !requiresVerifiedAgainst && e.VerifiedAgainst != nil:
		return fmt.Errorf("%w: verified_against must be nil for event_type %q", ErrInvalidRevisionEvent, e.EventType)
	}

	requiresSignoffStatus := e.EventType == EventTypeSignoff
	switch {
	case requiresSignoffStatus && e.SignoffStatus == nil:
		return fmt.Errorf("%w: signoff_status is required for event_type %q", ErrInvalidRevisionEvent, e.EventType)
	case !requiresSignoffStatus && e.SignoffStatus != nil:
		return fmt.Errorf("%w: signoff_status must be nil for event_type %q", ErrInvalidRevisionEvent, e.EventType)
	}
	if e.SignoffStatus != nil && *e.SignoffStatus != SignoffStatusApproved && *e.SignoffStatus != SignoffStatusChangesRequested {
		return fmt.Errorf("%w: signoff_status %q is not a recognized value", ErrInvalidRevisionEvent, *e.SignoffStatus)
	}

	for _, d := range e.EntityDeltas {
		if d.Change != EntityDeltaChangeCreated && d.Change != EntityDeltaChangeUpdated {
			return fmt.Errorf("%w: entity_deltas change %q is not created|updated", ErrInvalidRevisionEvent, d.Change)
		}
	}

	// FR6's identity shape: every opened/resolved entry must actually name
	// a question. The stateful half of FR6's validation -- a resolved id
	// that was never opened anywhere in this session -- needs a query
	// against the session's history and so cannot run here; see Append's
	// call to validateResolvedQuestionsOpened (open_questions.go).
	for _, oq := range e.OpenQuestionsDelta.Opened {
		if oq.QuestionID == "" {
			return fmt.Errorf("%w: open_questions_delta.opened entry has an empty question_id", ErrInvalidRevisionEvent)
		}
	}
	for _, r := range e.OpenQuestionsDelta.Resolved {
		if r == "" {
			return fmt.Errorf("%w: open_questions_delta.resolved entry has an empty question_id", ErrInvalidRevisionEvent)
		}
	}
	return nil
}

func scanRevisionEvent(row pgx.Row) (RevisionEvent, error) {
	var ev RevisionEvent
	var actingKind, onBehalfOfKind, eventType string
	var entityDeltas, openQuestionsDelta json.RawMessage
	var signoffStatus *string
	if err := row.Scan(
		&ev.ID, &ev.ScopeID, &ev.SessionID, &ev.SeqNo,
		&ev.Acting.Iss, &ev.Acting.Sub, &actingKind,
		&ev.OnBehalfOf.Iss, &ev.OnBehalfOf.Sub, &onBehalfOfKind,
		&eventType, &entityDeltas, &openQuestionsDelta, &ev.VerifiedAgainst, &signoffStatus,
		&ev.CreatedAt,
	); err != nil {
		return RevisionEvent{}, err
	}
	ev.Acting.Kind = SubjectKind(actingKind)
	ev.OnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	ev.EventType = EventType(eventType)
	if err := json.Unmarshal(entityDeltas, &ev.EntityDeltas); err != nil {
		return RevisionEvent{}, fmt.Errorf("unmarshal entity_deltas: %w", err)
	}
	if err := json.Unmarshal(openQuestionsDelta, &ev.OpenQuestionsDelta); err != nil {
		return RevisionEvent{}, fmt.Errorf("unmarshal open_questions_delta: %w", err)
	}
	if signoffStatus != nil {
		st := SignoffStatus(*signoffStatus)
		ev.SignoffStatus = &st
	}
	return ev, nil
}

func (s revisionEventStore) Append(ctx context.Context, e NewRevisionEvent) (RevisionEvent, error) {
	if err := validateNewRevisionEvent(e); err != nil {
		return RevisionEvent{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RevisionEvent{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	ev, err := appendRevisionEventTx(ctx, tx, e)
	if err != nil {
		return RevisionEvent{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return RevisionEvent{}, fmt.Errorf("commit: %w", err)
	}
	return ev, nil
}

// appendRevisionEventTx is Append's per-transaction body, factored out so
// mediated.go's ProposeEntities (issue #2546, NFR2) can append the one
// revision_event describing a mediated write's entity creates inside the
// SAME transaction as those creates, rather than opening a second
// transaction the way calling the exported Append method would. Append
// itself is just this function wrapped in its own Begin/Commit. e is
// assumed already validated (validateNewRevisionEvent) by the caller.
//
// tx must already hold (or be about to take) no conflicting lock on
// e.SessionID's design_session row -- this function takes the same
// `SELECT ... FOR UPDATE` lock Append's doc comment describes, so two
// concurrent appends to the same session (whether via Append or via
// ProposeEntities) still cannot race the same seq_no.
func appendRevisionEventTx(ctx context.Context, tx pgx.Tx, e NewRevisionEvent) (RevisionEvent, error) {
	// A nil Opened/Resolved slice marshals to JSON `null` -- a JSONB
	// scalar, not the empty array open_questions.go's ListOpenQuestions
	// (and validateResolvedQuestionsOpened below) unnest with
	// jsonb_array_elements. Default both to empty slices here, inside the
	// shared transactional body rather than only in Append's wrapper, so
	// every row this store ever writes carries `[]`, never a JSON null,
	// for either field -- including mediated.go's ProposeEntities, which
	// calls this function directly and never goes through Append.
	if e.OpenQuestionsDelta.Opened == nil {
		e.OpenQuestionsDelta.Opened = []OpenQuestionOpened{}
	}
	if e.OpenQuestionsDelta.Resolved == nil {
		e.OpenQuestionsDelta.Resolved = []string{}
	}

	entityDeltas, err := json.Marshal(e.EntityDeltas)
	if err != nil {
		return RevisionEvent{}, fmt.Errorf("marshal entity_deltas: %w", err)
	}
	openQuestionsDelta, err := json.Marshal(e.OpenQuestionsDelta)
	if err != nil {
		return RevisionEvent{}, fmt.Errorf("marshal open_questions_delta: %w", err)
	}

	// Lock the owning design_session row for the duration of this
	// transaction so two concurrent Appends to the same session cannot
	// both compute the same seq_no (see Append's doc comment) -- never a
	// plain MAX(seq_no)+1 without this lock, which would race the
	// `UNIQUE (session_id, seq_no)` index.
	var locked uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM design_session WHERE id = $1 FOR UPDATE`, e.SessionID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return RevisionEvent{}, errParentNotFound("design_session", e.SessionID)
	}
	if err != nil {
		return RevisionEvent{}, fmt.Errorf("lock design_session for append: %w", err)
	}

	// FR6's "resolved names an unopened question is a 400, not a silent
	// no-op" rule -- checked here, inside the same transaction as the
	// row lock above, against the session's full opened-question history
	// (open_questions.go), never against ListOpenQuestions' currently-open
	// subset (re-resolving an already-resolved question must stay a no-op,
	// per Testing case 4).
	if err := validateResolvedQuestionsOpened(ctx, tx, e.SessionID, e.OpenQuestionsDelta); err != nil {
		return RevisionEvent{}, err
	}

	var signoffStatus *string
	if e.SignoffStatus != nil {
		v := string(*e.SignoffStatus)
		signoffStatus = &v
	}

	ev, err := scanRevisionEvent(tx.QueryRow(ctx, `
		INSERT INTO revision_event (
			scope_id, session_id, seq_no,
			acting_iss, acting_sub, acting_kind,
			on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind,
			event_type, entity_deltas, open_questions_delta, verified_against, signoff_status
		) VALUES (
			$1, $2,
			COALESCE((SELECT MAX(seq_no) FROM revision_event WHERE session_id = $2), 0) + 1,
			$3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
		)
		RETURNING `+revisionEventColumns,
		e.ScopeID, e.SessionID,
		e.Acting.Iss, e.Acting.Sub, string(e.Acting.Kind),
		e.OnBehalfOf.Iss, e.OnBehalfOf.Sub, string(e.OnBehalfOf.Kind),
		string(e.EventType), entityDeltas, openQuestionsDelta, e.VerifiedAgainst, signoffStatus,
	))
	if err != nil {
		return RevisionEvent{}, fmt.Errorf("insert revision_event: %w", err)
	}
	return ev, nil
}

func (s revisionEventStore) ListBySession(ctx context.Context, sessionID uuid.UUID) ([]RevisionEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+revisionEventColumns+`
		FROM revision_event
		WHERE session_id = $1
		ORDER BY seq_no ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list revision_event by session: %w", err)
	}
	defer rows.Close()

	var events []RevisionEvent
	for rows.Next() {
		ev, err := scanRevisionEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan revision_event: %w", err)
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}
