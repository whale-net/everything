// video_script.go covers `video_script` (migration 010, milestone
// video-script-model, issues #1823/#1824) -- M2.1's replacement for
// schedule_entry as the record of a proposed video: Propose/Greenlight/
// Deny/Archive's full propose/greenlit/denied/archived lifecycle
// (FR36-FR40), plus the publish-freeze predicate Archive consults (FR39,
// modelled on scheduleStore's isPublished, store/schedule.go). Every
// other task in this milestone depends on this file.
//
// VideoScriptStore performs NO authorization itself -- store.CanWrite
// (propose) and store.CanApprove (greenlight/deny/archive) are applied by
// callers, the MCP registry and web handlers (NFR13/NFR5). See authz.go,
// the only place role questions are answered.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrVideoScriptPublished is returned by Archive, with no state change,
// once the freeze predicate holds for the target video_script: a live
// (auto/confirmed) video_schedule_match whose synced_video.published_at
// is non-null (FR39). Checked atomically, inside the same transaction as
// the attempted state change, so a racing sync write can never slip an
// archive through between the check and the UPDATE.
var ErrVideoScriptPublished = errors.New("video_script is frozen: its matched video has already been published")

// ErrVideoScriptTransition is returned by Greenlight/Deny/Archive, with no
// state change, for any transition outside the exhaustive set FR40
// allows: proposed->greenlit, proposed->denied, greenlit->archived (only
// when the freeze does not hold). Everything else -- including
// greenlit->proposed, anything out of denied/archived, and
// greenlit->archived under an active freeze -- returns this error instead
// of ErrVideoScriptPublished, except the freeze case itself, which
// returns ErrVideoScriptPublished specifically so callers can render the
// freeze distinctly from an ordinary invalid transition.
var ErrVideoScriptTransition = errors.New("video_script: invalid state transition")

// ProposeVideoScriptInput is the input to VideoScriptStore.Propose. IdeaID
// is NOT part of this struct -- Propose derives it from VerdictID
// (viability_verdict.idea_id) so it can never disagree with VerdictID
// (LB3).
type ProposeVideoScriptInput struct {
	ChannelID         uuid.UUID
	VerdictID         uuid.UUID // must reference a VerdictViable row.
	StrategyID        uuid.UUID // must be on ChannelID.
	Title             string
	ScriptText        string
	TargetPublishDate *time.Time
	CreatedByPersonID uuid.UUID
	IdempotencyKey    string
}

// VideoScriptStore covers `video_script` (migration 010, FR36-FR40). Every
// row's VerdictID is the FK to the specific viability_verdict *version*
// that judged the Idea viable (LB3), and StrategyID is always non-nil
// (unlike schedule_entry's verdict-only grounding) -- FR36 requires
// proposing a video_script under a Strategy.
type VideoScriptStore interface {
	// Propose inserts a proposed video_script in one transaction (FR36,
	// NFR12/13). Returns ErrVerdictNotViable without writing anything if
	// in.VerdictID does not exist or its verdict is not VerdictViable --
	// mirrors ScheduleStore.SaveDraft's gate (store/schedule.go) exactly.
	// Also rejects (with an error, no write) if in.VerdictID's idea is not
	// on in.ChannelID, or in.StrategyID is not on in.ChannelID. IdeaID is
	// always derived from in.VerdictID, never accepted from the caller.
	Propose(ctx context.Context, in ProposeVideoScriptInput) (VideoScript, error)

	// Greenlight transitions scriptID from proposed to greenlit, stamping
	// decided_by_person_id/decided_at (FR37, requires CanApprove). Returns
	// ErrVideoScriptTransition, with no state change, unless scriptID is
	// currently proposed.
	Greenlight(ctx context.Context, scriptID, byPersonID uuid.UUID) error

	// Deny transitions scriptID from proposed to denied (terminal),
	// stamping decided_by_person_id/decided_at (FR38, requires
	// CanApprove). Returns ErrVideoScriptTransition, with no state change,
	// unless scriptID is currently proposed.
	Deny(ctx context.Context, scriptID, byPersonID uuid.UUID) error

	// Archive transitions scriptID from greenlit to archived (FR39,
	// requires CanApprove). In one transaction: returns
	// ErrVideoScriptPublished, with no state change, if the freeze
	// predicate holds (a live auto/confirmed video_schedule_match to a
	// published synced_video; a pending match does not freeze). Returns
	// ErrVideoScriptTransition, with no state change, unless scriptID is
	// currently greenlit.
	Archive(ctx context.Context, scriptID, byPersonID uuid.UUID) error

	// GetByID returns the VideoScript for id, or pgx.ErrNoRows if none
	// exists.
	GetByID(ctx context.Context, id uuid.UUID) (VideoScript, error)

	// ListByChannel returns every VideoScript for channelID.
	ListByChannel(ctx context.Context, channelID uuid.UUID) ([]VideoScript, error)

	// ListDetailByChannel returns every VideoScriptDetail for channelID,
	// joining the bound verdict version/value, idea title, and strategy
	// title, and computing Published per row -- modelled directly on
	// ScheduleStore.ListDetailByChannel (store/schedule.go).
	ListDetailByChannel(ctx context.Context, channelID uuid.UUID) ([]VideoScriptDetail, error)

	// IsPublished reports whether scriptID has a live (auto or confirmed)
	// video_schedule_match row whose synced_video.published_at is
	// non-null -- FR39's freeze predicate (issue #1824), the pool-level
	// wrapper `web`'s view-rendering code calls directly, mirroring
	// ScheduleStore.IsPublished (store/schedule.go). Archive consults the
	// same predicate atomically, inside its own transaction, rather than
	// calling this method.
	IsPublished(ctx context.Context, scriptID uuid.UUID) (bool, error)
}

// videoScriptStore implements VideoScriptStore against `video_script`
// (migration 010). Every method returns ErrVideoScriptNotImplemented
// until Implementation wires in the real SQL -- same scaffold/feat split
// other store methods in this package have followed (e.g. AccessStore,
// store/access.go).
type videoScriptStore struct{ pool *pgxpool.Pool }

var _ VideoScriptStore = videoScriptStore{}

// ErrVideoScriptNotImplemented is returned by every VideoScriptStore
// method until Implementation wires in the real queries.
var ErrVideoScriptNotImplemented = errors.New("store: video_script lifecycle not implemented")

func (s videoScriptStore) Propose(ctx context.Context, in ProposeVideoScriptInput) (VideoScript, error) {
	return VideoScript{}, ErrVideoScriptNotImplemented
}

func (s videoScriptStore) Greenlight(ctx context.Context, scriptID, byPersonID uuid.UUID) error {
	return ErrVideoScriptNotImplemented
}

func (s videoScriptStore) Deny(ctx context.Context, scriptID, byPersonID uuid.UUID) error {
	return ErrVideoScriptNotImplemented
}

func (s videoScriptStore) Archive(ctx context.Context, scriptID, byPersonID uuid.UUID) error {
	return ErrVideoScriptNotImplemented
}

func (s videoScriptStore) GetByID(ctx context.Context, id uuid.UUID) (VideoScript, error) {
	return VideoScript{}, ErrVideoScriptNotImplemented
}

func (s videoScriptStore) ListByChannel(ctx context.Context, channelID uuid.UUID) ([]VideoScript, error) {
	return nil, ErrVideoScriptNotImplemented
}

func (s videoScriptStore) ListDetailByChannel(ctx context.Context, channelID uuid.UUID) ([]VideoScriptDetail, error) {
	return nil, ErrVideoScriptNotImplemented
}

func (s videoScriptStore) IsPublished(ctx context.Context, scriptID uuid.UUID) (bool, error) {
	return false, ErrVideoScriptNotImplemented
}
