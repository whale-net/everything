package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SaveDraftInput is the input to ScheduleStore.SaveDraft.
type SaveDraftInput struct {
	ChannelID         uuid.UUID
	IdeaID            uuid.UUID
	VerdictID         uuid.UUID // must reference a VerdictViable row (FR16).
	ProposedPublishAt time.Time
	CreatedByPersonID uuid.UUID
	IdempotencyKey    string
}

// ScheduleStore covers `schedule_entry` (migration 002, FR16-FR20). Every
// row's VerdictID is the FK to the specific Verdict version that judged
// the Idea viable (LB3) -- SaveDraft rejects any verdict that is not
// VerdictViable.
type ScheduleStore interface {
	// SaveDraft inserts a draft schedule_entry. Returns ErrVerdictNotViable
	// without writing anything if in.VerdictID's verdict is not
	// VerdictViable (FR16).
	SaveDraft(ctx context.Context, in SaveDraftInput) (ScheduleEntry, error)

	// Approve transitions entryID from draft to committed, stamping
	// approved_by_person_id/approved_at (FR19, requires CanApprove).
	Approve(ctx context.Context, entryID, byPersonID uuid.UUID) error

	// Unapprove reverses Approve, transitioning entryID back to draft and
	// clearing approved_by_person_id/approved_at (FR20).
	Unapprove(ctx context.Context, entryID, byPersonID uuid.UUID) error

	// Update changes a draft entry's proposed_publish_at (FR18's pacing
	// adjustments).
	Update(ctx context.Context, entryID uuid.UUID, proposedPublishAt time.Time) error

	// ListByChannel returns every ScheduleEntry for channelID.
	ListByChannel(ctx context.Context, channelID uuid.UUID) ([]ScheduleEntry, error)
}

// scheduleStore implements ScheduleStore against `schedule_entry`
// (migration 002).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1569's "Implementation" scope).
type scheduleStore struct{ pool *pgxpool.Pool }

var _ ScheduleStore = scheduleStore{}

func (s scheduleStore) SaveDraft(ctx context.Context, in SaveDraftInput) (ScheduleEntry, error) {
	return ScheduleEntry{}, errors.New("not implemented")
}

func (s scheduleStore) Approve(ctx context.Context, entryID, byPersonID uuid.UUID) error {
	return errors.New("not implemented")
}

func (s scheduleStore) Unapprove(ctx context.Context, entryID, byPersonID uuid.UUID) error {
	return errors.New("not implemented")
}

func (s scheduleStore) Update(ctx context.Context, entryID uuid.UUID, proposedPublishAt time.Time) error {
	return errors.New("not implemented")
}

func (s scheduleStore) ListByChannel(ctx context.Context, channelID uuid.UUID) ([]ScheduleEntry, error) {
	return nil, errors.New("not implemented")
}
