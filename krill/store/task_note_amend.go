// AmendNote (FR e18afe39): amending a note appends a new task_note row whose
// supersedes_note_id names the old row (030_note_supersession). The old row
// is retained; the new row keeps its kind, target and lifecycle status.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNoteAlreadySuperseded rejects amending a note that already has a
// superseding row; callers must amend the chain's head.
var ErrNoteAlreadySuperseded = errors.New("krill/store: note already superseded")

// AmendNoteParams is AmendNote's input.
type AmendNoteParams struct {
	ScopeID    uuid.UUID
	NoteID     uuid.UUID
	Body       string
	Acting     Subject
	OnBehalfOf Subject
}

// AmendNote appends the superseding note row and returns it. The old row is
// locked for the check so two concurrent amends of one head serialize; the
// second sees ErrNoteAlreadySuperseded.
func (s taskStore) AmendNote(ctx context.Context, params AmendNoteParams) (Note, error) {
	if params.Body == "" {
		return Note{}, ErrEmptyNoteBody
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Note{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	old, err := scanNote(tx.QueryRow(ctx, `
		SELECT `+noteColumns+` FROM task_note WHERE id = $1 AND scope_id = $2 FOR UPDATE
	`, params.NoteID, params.ScopeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Note{}, errParentNotFound("task_note", params.NoteID)
	}
	if err != nil {
		return Note{}, fmt.Errorf("get task_note for amend: %w", err)
	}

	var superseded bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM task_note WHERE supersedes_note_id = $1)
	`, params.NoteID).Scan(&superseded); err != nil {
		return Note{}, fmt.Errorf("check task_note superseded: %w", err)
	}
	if superseded {
		return Note{}, ErrNoteAlreadySuperseded
	}

	var entityKind *string
	if old.EntityKind != nil {
		ek := string(*old.EntityKind)
		entityKind = &ek
	}
	amended, err := scanNote(tx.QueryRow(ctx, `
		INSERT INTO task_note (
			scope_id, task_id, entity_kind, entity_id, kind, body, current_status, supersedes_note_id,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING `+noteColumns,
		old.ScopeID, old.TaskID, entityKind, old.EntityID, string(old.Kind), params.Body,
		string(old.CurrentStatus), old.ID,
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind)))
	if err != nil {
		return Note{}, fmt.Errorf("insert amended task_note: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Note{}, fmt.Errorf("commit: %w", err)
	}
	return amended, nil
}
