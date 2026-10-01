// AmendNote (FR e18afe39): amending a note appends a new task_note row whose
// supersedes_note_id names the old row (029_note_supersession). The old row
// is retained; the new row keeps its kind, target and lifecycle status.
package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
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

// AmendNote appends the superseding note row and returns it.
// Scaffold stub; implemented in the Implementation lane.
func (s taskStore) AmendNote(ctx context.Context, params AmendNoteParams) (Note, error) {
	return Note{}, errors.New("krill/store: AmendNote not implemented")
}
