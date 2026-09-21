// This file (issue #2874, root plan #2851, FR11, C25) is M5's note-
// lifecycle transition verb: TransitionNoteLifecycle appends one
// `task_note_lifecycle_event` row (016_escalation_axis.up.sql) and mirrors
// its Status onto the parent `task_note.current_status` column, in one
// transaction -- the authoritative, append-only history behind that
// denormalized current value (NFR2), the same "current value on the
// parent, history on a sibling append-only table" shape task.current_lane/
// task_lease_event already established (015_work_axis.up.sql).
//
// Open to any persona (FR11 says "any persona", not only an Agent) -- no
// claim, ownership, or persona check here at all, mirroring RecordNote's
// own "any Agent, claimant or not" posture (task_note.go); the only gate is
// NFR6's session requirement, enforced by the caller's HTTP/MCP layer, same
// as every other TaskStore write.
//
// TransitionNoteLifecycle never touches task_note.body/kind/task_id/
// entity_kind/entity_id -- see Note's own doc comment (task_note.go) for
// why those stay permanently immutable even after this method exists.
//
// A transition to the note's own current status is accepted, not rejected:
// it is recorded as an ordinary event like any other, so the append-only
// history (NFR2) never special-cases a would-be no-op by silently dropping
// it -- a caller can always tell "was this status re-affirmed at time T"
// from the event history, which a silent no-op would erase.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// NoteLifecycleEvent is one row of `task_note_lifecycle_event`
// (016_escalation_axis.up.sql) -- append-only, one row per status
// transition (FR11): never overwritten or deleted, never resolved by a
// later event referencing it (NFR2) -- the note's CurrentStatus (task_note.
// go) is simply mirrored to whatever this row's Status most recently was.
type NoteLifecycleEvent struct {
	ID      uuid.UUID
	ScopeID uuid.UUID
	NoteID  uuid.UUID
	Status  NoteLifecycleStatus

	// CreatedByActing/CreatedByOnBehalfOf are always populated (NFR3,
	// LB4) -- TransitionNoteLifecycle is the only write path onto this
	// table, and it is only ever called by a caller with an active
	// session (NFR6), enforced above this store layer.
	CreatedByActing     Subject
	CreatedByOnBehalfOf Subject
	CreatedAt           time.Time
}

// TransitionNoteLifecycleParams is TransitionNoteLifecycle's input (FR11):
// the target NoteID, the destination Status (one of NoteLifecycleStatus's
// fixed enumeration, task_escalation.go), and the LB4 subject pair.
type TransitionNoteLifecycleParams struct {
	ScopeID uuid.UUID
	NoteID  uuid.UUID
	Status  NoteLifecycleStatus

	Acting     Subject
	OnBehalfOf Subject
}

const noteLifecycleEventColumns = `id, scope_id, note_id, status, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at`

func scanNoteLifecycleEvent(row pgx.Row) (NoteLifecycleEvent, error) {
	var e NoteLifecycleEvent
	var actingKind, onBehalfOfKind string
	err := row.Scan(
		&e.ID, &e.ScopeID, &e.NoteID, &e.Status,
		&e.CreatedByActing.Iss, &e.CreatedByActing.Sub, &actingKind,
		&e.CreatedByOnBehalfOf.Iss, &e.CreatedByOnBehalfOf.Sub, &onBehalfOfKind,
		&e.CreatedAt,
	)
	if err != nil {
		return NoteLifecycleEvent{}, err
	}
	e.CreatedByActing.Kind = SubjectKind(actingKind)
	e.CreatedByOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	return e, nil
}

// TransitionNoteLifecycle appends one task_note_lifecycle_event row and
// mirrors its Status onto task_note.current_status, in one transaction
// (FR11). Rejects params.Status outside NoteLifecycleStatus's fixed
// enumeration with ErrUnknownNoteLifecycleStatus (task_escalation.go),
// checked in Go ahead of either write, and rejects an unknown or
// cross-scope params.NoteID with ErrNotFound -- both leave nothing written.
func (s taskStore) TransitionNoteLifecycle(ctx context.Context, params TransitionNoteLifecycleParams) (NoteLifecycleEvent, error) {
	if err := ValidateNoteLifecycleStatus(params.Status); err != nil {
		return NoteLifecycleEvent{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return NoteLifecycleEvent{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// task_note is plain (not SCD2, LB3 -- task_note.go's own doc
	// comment), so plainRowExists is the right existence check, mirroring
	// RecordNote's own use of it for a task target.
	exists, err := plainRowExists(ctx, tx, "task_note", params.NoteID, params.ScopeID)
	if err != nil {
		return NoteLifecycleEvent{}, err
	}
	if !exists {
		return NoteLifecycleEvent{}, errParentNotFound("task_note", params.NoteID)
	}

	event, err := scanNoteLifecycleEvent(tx.QueryRow(ctx, `
		INSERT INTO task_note_lifecycle_event (
			scope_id, note_id, status,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+noteLifecycleEventColumns,
		params.ScopeID, params.NoteID, string(params.Status),
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind)))
	if err != nil {
		return NoteLifecycleEvent{}, fmt.Errorf("insert task_note_lifecycle_event: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE task_note SET current_status = $1 WHERE id = $2 AND scope_id = $3
	`, string(params.Status), params.NoteID, params.ScopeID); err != nil {
		return NoteLifecycleEvent{}, fmt.Errorf("update task_note current_status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return NoteLifecycleEvent{}, fmt.Errorf("commit: %w", err)
	}
	return event, nil
}
