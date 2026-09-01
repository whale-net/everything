package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PacingStore covers `pacing_policy` (migration 002, FR17). Natural key =
// Channel (channel_id UNIQUE, migration 002), so Upsert converges on
// repeated calls with identical values (NFR2 by construction).
type PacingStore interface {
	// Upsert finds or creates the PacingPolicy row for channelID, writing
	// p's fields either way.
	Upsert(ctx context.Context, channelID uuid.UUID, p PacingPolicy) (PacingPolicy, error)

	// Get returns the PacingPolicy for channelID, and false if none has
	// been set yet.
	Get(ctx context.Context, channelID uuid.UUID) (PacingPolicy, bool, error)
}

// pacingStore implements PacingStore against `pacing_policy` (migration
// 002).
type pacingStore struct{ pool *pgxpool.Pool }

var _ PacingStore = pacingStore{}

const pacingPolicyColumns = `id, channel_id, target_uploads_per_week, preferred_days, updated_at, updated_by_person_id`

func scanPacingPolicy(row pgx.Row) (PacingPolicy, error) {
	var p PacingPolicy
	err := row.Scan(&p.ID, &p.ChannelID, &p.TargetUploadsPerWeek, &p.PreferredDays, &p.UpdatedAt, &p.UpdatedByPersonID)
	return p, err
}

// Upsert relies on ON CONFLICT (channel_id) DO UPDATE -- the same
// find-or-create-in-one-round-trip idiom as PersonStore.
// UpsertByGoogleSubject -- so repeated calls with identical values leave
// exactly one row with identical content (NFR2).
func (s pacingStore) Upsert(ctx context.Context, channelID uuid.UUID, p PacingPolicy) (PacingPolicy, error) {
	days := p.PreferredDays
	if days == nil {
		days = []string{}
	}
	out, err := scanPacingPolicy(s.pool.QueryRow(ctx, `
		INSERT INTO pacing_policy (channel_id, target_uploads_per_week, preferred_days, updated_by_person_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (channel_id) DO UPDATE
			SET target_uploads_per_week = EXCLUDED.target_uploads_per_week,
				preferred_days = EXCLUDED.preferred_days,
				updated_at = NOW(),
				updated_by_person_id = EXCLUDED.updated_by_person_id
		RETURNING `+pacingPolicyColumns,
		channelID, p.TargetUploadsPerWeek, days, p.UpdatedByPersonID))
	if err != nil {
		return PacingPolicy{}, fmt.Errorf("upsert pacing_policy: %w", err)
	}
	return out, nil
}

func (s pacingStore) Get(ctx context.Context, channelID uuid.UUID) (PacingPolicy, bool, error) {
	p, err := scanPacingPolicy(s.pool.QueryRow(ctx, `SELECT `+pacingPolicyColumns+` FROM pacing_policy WHERE channel_id = $1`, channelID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PacingPolicy{}, false, nil
		}
		return PacingPolicy{}, false, fmt.Errorf("get pacing_policy: %w", err)
	}
	return p, true, nil
}
