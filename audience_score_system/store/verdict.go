package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AppendVerdictInput is the input to VerdictStore.Append.
type AppendVerdictInput struct {
	IdeaID         uuid.UUID
	Verdict        VerdictValue
	Reasoning      string
	AuthorPersonID uuid.UUID
	IdempotencyKey string
	// CitedResearchNoteIDs populates verdict_citation (FR11) for this
	// version, in the same transaction as the insert.
	CitedResearchNoteIDs []uuid.UUID
}

// ErrVerdictNotViable is returned by ScheduleStore.SaveDraft when the
// referenced Verdict's Verdict field is not VerdictViable (FR16).
var ErrVerdictNotViable = errors.New("referenced verdict is not viable")

// VerdictStore covers `viability_verdict` and `verdict_citation`
// (migration 002, FR11-FR13). viability_verdict is an append-only version
// log (FR12) -- no method here, or anywhere else in this package, may
// UPDATE an existing viability_verdict row.
type VerdictStore interface {
	// Append allocates version = max+1 for in.IdeaID in one transaction
	// and inserts a new viability_verdict row plus its verdict_citation
	// rows. Never UPDATEs an existing row (FR12).
	Append(ctx context.Context, in AppendVerdictInput) (Verdict, error)

	// Current returns the highest-version Verdict for ideaID (i.e.
	// v_current_verdict's row for that idea), or an error if ideaID has no
	// verdict yet.
	Current(ctx context.Context, ideaID uuid.UUID) (Verdict, error)

	// History returns every Verdict for ideaID, ordered by version
	// ascending (FR13).
	History(ctx context.Context, ideaID uuid.UUID) ([]Verdict, error)
}

// verdictStore implements VerdictStore against `viability_verdict` and
// `verdict_citation` (migration 002).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1569's "Implementation" scope).
type verdictStore struct{ pool *pgxpool.Pool }

var _ VerdictStore = verdictStore{}

func (s verdictStore) Append(ctx context.Context, in AppendVerdictInput) (Verdict, error) {
	return Verdict{}, errors.New("not implemented")
}

func (s verdictStore) Current(ctx context.Context, ideaID uuid.UUID) (Verdict, error) {
	return Verdict{}, errors.New("not implemented")
}

func (s verdictStore) History(ctx context.Context, ideaID uuid.UUID) ([]Verdict, error) {
	return nil, errors.New("not implemented")
}
