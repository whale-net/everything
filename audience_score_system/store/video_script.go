// video_script.go covers `video_script` (migration 010, milestone
// video-script-model, issues #1823/#1824) -- M2.1's replacement for the
// now-dropped schedule_entry table (migration 013, issue #1835) as the
// record of a proposed video: Propose/Greenlight/Deny/Archive's full
// propose/greenlit/denied/archived lifecycle (FR36-FR40), plus the
// publish-freeze predicate Archive consults (FR39). Every other task in
// this milestone depends on this file.
//
// VideoScriptStore performs NO authorization itself -- store.CanWrite
// (propose) and store.CanApprove (greenlight/deny/archive) are applied by
// callers, the MCP registry and web handlers (NFR13/NFR5). See authz.go,
// the only place role questions are answered.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	// in.VerdictID does not exist or its verdict is not VerdictViable.
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
	// title, and computing Published per row.
	ListDetailByChannel(ctx context.Context, channelID uuid.UUID) ([]VideoScriptDetail, error)

	// IsPublished reports whether scriptID has a live (auto or confirmed)
	// video_schedule_match row whose synced_video.published_at is
	// non-null -- FR39's freeze predicate (issue #1824), the pool-level
	// wrapper `web`'s view-rendering code calls directly. Archive consults
	// the same predicate atomically, inside its own transaction, rather
	// than calling this method.
	IsPublished(ctx context.Context, scriptID uuid.UUID) (bool, error)
}

// videoScriptStore implements VideoScriptStore against `video_script`
// (migration 010).
type videoScriptStore struct{ pool *pgxpool.Pool }

var _ VideoScriptStore = videoScriptStore{}

const videoScriptColumns = `id, channel_id, idea_id, verdict_id, strategy_id, title, script_text, status, target_publish_date, decided_by_person_id, decided_at, created_by_person_id, created_at, updated_at, COALESCE(idempotency_key, '')`

// videoScriptColumnsAliased is videoScriptColumns qualified with the vs.
// alias ListDetailByChannel's multi-table join needs.
const videoScriptColumnsAliased = `vs.id, vs.channel_id, vs.idea_id, vs.verdict_id, vs.strategy_id, vs.title, vs.script_text, vs.status, vs.target_publish_date, vs.decided_by_person_id, vs.decided_at, vs.created_by_person_id, vs.created_at, vs.updated_at, COALESCE(vs.idempotency_key, '')`

func scanVideoScript(row pgx.Row) (VideoScript, error) {
	var s VideoScript
	err := row.Scan(&s.ID, &s.ChannelID, &s.IdeaID, &s.VerdictID, &s.StrategyID, &s.Title, &s.ScriptText, &s.Status, &s.TargetPublishDate, &s.DecidedByPersonID, &s.DecidedAt, &s.CreatedByPersonID, &s.CreatedAt, &s.UpdatedAt, &s.IdempotencyKey)
	return s, err
}

// Propose honours IdempotencyKey (a replayed (channel, author, key) triple
// returns the original row unchanged) per NFR12/LB4. Otherwise, inside the
// same transaction, it looks up in.VerdictID's verdict, idea, and idea's
// channel -- rejecting with ErrVerdictNotViable unless the verdict exists
// and is VerdictViable, and with a plain error if the verdict's idea is
// not on in.ChannelID (mirroring StrategyStore.Save's identical check) --
// then confirms in.StrategyID is
// on in.ChannelID (ErrStrategyNotFound otherwise, reused rather than
// inventing a second "not found on this channel" spelling). idea_id is
// always taken from the verdict row, never in.IdeaID (there is no such
// field -- LB3).
func (s videoScriptStore) Propose(ctx context.Context, in ProposeVideoScriptInput) (VideoScript, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return VideoScript{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if in.IdempotencyKey != "" {
		existing, err := scanVideoScript(tx.QueryRow(ctx, `
			SELECT `+videoScriptColumns+`
			FROM video_script
			WHERE channel_id = $1 AND created_by_person_id = $2 AND idempotency_key = $3
		`, in.ChannelID, in.CreatedByPersonID, in.IdempotencyKey))
		if err == nil {
			if err := tx.Commit(ctx); err != nil {
				return VideoScript{}, fmt.Errorf("commit: %w", err)
			}
			return existing, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return VideoScript{}, fmt.Errorf("lookup video_script by idempotency key: %w", err)
		}
	}

	var verdict VerdictValue
	var ideaID, ideaChannelID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT vv.verdict, vv.idea_id, i.channel_id
		FROM viability_verdict vv
		JOIN idea i ON i.id = vv.idea_id
		WHERE vv.id = $1
	`, in.VerdictID).Scan(&verdict, &ideaID, &ideaChannelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return VideoScript{}, fmt.Errorf("verdict %s: %w", in.VerdictID, ErrVerdictNotViable)
		}
		return VideoScript{}, fmt.Errorf("lookup verdict for video script: %w", err)
	}
	if verdict != VerdictViable {
		return VideoScript{}, ErrVerdictNotViable
	}
	if ideaChannelID != in.ChannelID {
		return VideoScript{}, fmt.Errorf("verdict %s does not belong to channel %s", in.VerdictID, in.ChannelID)
	}

	var strategyExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM strategy WHERE id = $1 AND channel_id = $2)
	`, in.StrategyID, in.ChannelID).Scan(&strategyExists); err != nil {
		return VideoScript{}, fmt.Errorf("check strategy %s on channel %s: %w", in.StrategyID, in.ChannelID, err)
	}
	if !strategyExists {
		return VideoScript{}, ErrStrategyNotFound
	}

	script, err := scanVideoScript(tx.QueryRow(ctx, `
		INSERT INTO video_script (channel_id, idea_id, verdict_id, strategy_id, title, script_text, status, target_publish_date, created_by_person_id, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, 'proposed', $7, $8, NULLIF($9, ''))
		RETURNING `+videoScriptColumns,
		in.ChannelID, ideaID, in.VerdictID, in.StrategyID, in.Title, in.ScriptText, in.TargetPublishDate, in.CreatedByPersonID, in.IdempotencyKey))
	if err != nil {
		return VideoScript{}, fmt.Errorf("insert video_script: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return VideoScript{}, fmt.Errorf("commit: %w", err)
	}
	return script, nil
}

func (s videoScriptStore) Greenlight(ctx context.Context, scriptID, byPersonID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE video_script
		SET status = 'greenlit', decided_by_person_id = $1, decided_at = NOW(), updated_at = NOW()
		WHERE id = $2 AND status = 'proposed'
	`, byPersonID, scriptID)
	if err != nil {
		return fmt.Errorf("greenlight video_script: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("video_script %s not proposed: %w", scriptID, ErrVideoScriptTransition)
	}
	return nil
}

func (s videoScriptStore) Deny(ctx context.Context, scriptID, byPersonID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE video_script
		SET status = 'denied', decided_by_person_id = $1, decided_at = NOW(), updated_at = NOW()
		WHERE id = $2 AND status = 'proposed'
	`, byPersonID, scriptID)
	if err != nil {
		return fmt.Errorf("deny video_script: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("video_script %s not proposed: %w", scriptID, ErrVideoScriptTransition)
	}
	return nil
}

// Archive checks the freeze predicate and performs the status change in
// the same transaction (FR39) -- see isVideoScriptPublished's doc comment
// for exactly what the freeze predicate is.
func (s videoScriptStore) Archive(ctx context.Context, scriptID, byPersonID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	published, err := isVideoScriptPublished(ctx, tx, scriptID)
	if err != nil {
		return err
	}
	if published {
		return ErrVideoScriptPublished
	}

	tag, err := tx.Exec(ctx, `
		UPDATE video_script
		SET status = 'archived', decided_by_person_id = $1, decided_at = NOW(), updated_at = NOW()
		WHERE id = $2 AND status = 'greenlit'
	`, byPersonID, scriptID)
	if err != nil {
		return fmt.Errorf("archive video_script: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("video_script %s not greenlit: %w", scriptID, ErrVideoScriptTransition)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s videoScriptStore) GetByID(ctx context.Context, id uuid.UUID) (VideoScript, error) {
	script, err := scanVideoScript(s.pool.QueryRow(ctx, `SELECT `+videoScriptColumns+` FROM video_script WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return VideoScript{}, pgx.ErrNoRows
		}
		return VideoScript{}, fmt.Errorf("get video_script by id: %w", err)
	}
	return script, nil
}

func (s videoScriptStore) ListByChannel(ctx context.Context, channelID uuid.UUID) ([]VideoScript, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+videoScriptColumns+`
		FROM video_script
		WHERE channel_id = $1
		ORDER BY created_at
	`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list video_script by channel: %w", err)
	}
	defer rows.Close()

	var scripts []VideoScript
	for rows.Next() {
		script, err := scanVideoScript(rows)
		if err != nil {
			return nil, fmt.Errorf("scan video_script: %w", err)
		}
		scripts = append(scripts, script)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list video_script by channel: %w", err)
	}
	return scripts, nil
}

// dbQueryRower is the subset of *pgxpool.Pool and pgx.Tx
// isVideoScriptPublished needs -- lets Archive run the same freeze check
// against its own transaction (atomically with the mutation) that
// VideoScriptStore.IsPublished runs directly against the pool.
type dbQueryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// isVideoScriptPublished is IsPublished's shared body, factored out so
// Archive can run it inside its own transaction (q == the tx) while
// VideoScriptStore.IsPublished runs it directly against the pool (q ==
// s.pool) -- exactly one query defines "recorded as published" for
// video_script (FR39). A pending match does not freeze -- only a live
// (auto/confirmed) match to a synced_video with a non-null published_at
// does.
func isVideoScriptPublished(ctx context.Context, q dbQueryRower, scriptID uuid.UUID) (bool, error) {
	var published bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM video_schedule_match vsm
			JOIN synced_video sv ON sv.id = vsm.synced_video_id
			WHERE vsm.video_script_id = $1
			  AND vsm.state IN ('auto', 'confirmed')
			  AND sv.published_at IS NOT NULL
		)
	`, scriptID).Scan(&published)
	if err != nil {
		return false, fmt.Errorf("check video_script %s published: %w", scriptID, err)
	}
	return published, nil
}

func (s videoScriptStore) IsPublished(ctx context.Context, scriptID uuid.UUID) (bool, error) {
	return isVideoScriptPublished(ctx, s.pool, scriptID)
}

// ListDetailByChannel joins video_script with its bound idea (title),
// viability_verdict (version/value), and strategy (title), plus -- via a
// LATERAL subquery (guarding against row multiplication: nothing in the
// schema prevents more than one live match row per video_script_id, and a
// plain join would silently duplicate the video_script row for each one)
// -- whether it has a live, published match. Not channel-scoped by the
// row's own channel_id
// alone in the sense of leaking another Channel's rows: the WHERE clause
// below is exactly vs.channel_id = $1, so a second Channel's scripts never
// appear.
func (s videoScriptStore) ListDetailByChannel(ctx context.Context, channelID uuid.UUID) ([]VideoScriptDetail, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			`+videoScriptColumnsAliased+`,
			vv.version,
			vv.verdict,
			i.title,
			st.title,
			pub.published
		FROM video_script vs
		JOIN idea i ON i.id = vs.idea_id
		JOIN viability_verdict vv ON vv.id = vs.verdict_id
		JOIN strategy st ON st.id = vs.strategy_id
		LEFT JOIN LATERAL (
			SELECT TRUE AS published
			FROM video_schedule_match vsm
			JOIN synced_video sv ON sv.id = vsm.synced_video_id
			WHERE vsm.video_script_id = vs.id
			  AND vsm.state IN ('auto', 'confirmed')
			  AND sv.published_at IS NOT NULL
			ORDER BY (vsm.state = 'confirmed') DESC, vsm.created_at DESC
			LIMIT 1
		) pub ON TRUE
		WHERE vs.channel_id = $1
		ORDER BY vs.created_at
	`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list video_script details by channel: %w", err)
	}
	defer rows.Close()

	var details []VideoScriptDetail
	for rows.Next() {
		var d VideoScriptDetail
		var published *bool
		err := rows.Scan(
			&d.Script.ID, &d.Script.ChannelID, &d.Script.IdeaID, &d.Script.VerdictID, &d.Script.StrategyID, &d.Script.Title, &d.Script.ScriptText, &d.Script.Status, &d.Script.TargetPublishDate,
			&d.Script.DecidedByPersonID, &d.Script.DecidedAt, &d.Script.CreatedByPersonID, &d.Script.CreatedAt, &d.Script.UpdatedAt, &d.Script.IdempotencyKey,
			&d.VerdictVersion, &d.Verdict, &d.IdeaTitle, &d.StrategyTitle,
			&published,
		)
		if err != nil {
			return nil, fmt.Errorf("scan video_script detail: %w", err)
		}
		d.Published = published != nil && *published
		details = append(details, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list video_script details by channel: %w", err)
	}
	return details, nil
}
