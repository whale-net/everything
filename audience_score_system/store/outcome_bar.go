// outcome_bar.go covers `outcome_bar` (migration 014, issue #1882) -- the
// per-Channel outcome bar's storage half (C14 / FR1 / FR2 / NFR1): a
// single current-state config row per Channel naming which metric to
// classify against and the threshold that separates "calibrated" from
// "miss" (FR4 always classifies against the row a Channel *currently*
// holds -- no history, see migration 014's SQL comment / AGENTS.md §
// SCD2).
//
// OutcomeBarStore performs NO authorization itself -- store.CanWrite/
// store.CanRead are applied by callers (NFR5), same as every other store
// in this package. See authz.go, the only place role questions are
// answered.
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

// OutcomeBarMetricViews is the only metric_name M3 accepts (FR1). A later
// milestone can add another accepted value here with no schema migration
// -- outcome_bar.metric_name deliberately carries no CHECK constraint
// (migration 014's SQL comment).
const OutcomeBarMetricViews = "views"

// ErrUnsupportedOutcomeBarMetric is returned by OutcomeBarStore.Upsert,
// with nothing written, when SetOutcomeBarInput.MetricName is not
// OutcomeBarMetricViews (FR1).
var ErrUnsupportedOutcomeBarMetric = errors.New("outcome bar: unsupported metric_name")

// ErrInvalidOutcomeBarThreshold is returned by OutcomeBarStore.Upsert,
// with nothing written, when SetOutcomeBarInput.ThresholdValue is
// negative -- a planner-added guard, not an FR: outcome_bar.
// threshold_value is NUMERIC and happily accepts a negative, which would
// silently classify every candidate as calibrated, never the caller's
// intent.
var ErrInvalidOutcomeBarThreshold = errors.New("outcome bar: threshold_value must not be negative")

// OutcomeBar is one row of `outcome_bar` (migration 014).
type OutcomeBar struct {
	ID                uuid.UUID
	ChannelID         uuid.UUID
	MetricName        string
	ThresholdValue    float64
	UpdatedAt         time.Time
	UpdatedByPersonID uuid.UUID
}

// SetOutcomeBarInput is the input to OutcomeBarStore.Upsert.
type SetOutcomeBarInput struct {
	ChannelID         uuid.UUID
	MetricName        string
	ThresholdValue    float64
	UpdatedByPersonID uuid.UUID
}

// OutcomeBarStore covers `outcome_bar` (migration 014, C14 / FR1 / FR2 /
// NFR1).
type OutcomeBarStore interface {
	// Upsert converges on the single row for in.ChannelID (NFR1):
	// repeated calls with identical values leave exactly one row and
	// return the same id. Validates in.MetricName ==
	// OutcomeBarMetricViews (else ErrUnsupportedOutcomeBarMetric) and
	// in.ThresholdValue >= 0 (else ErrInvalidOutcomeBarThreshold) before
	// touching the database -- either rejection writes nothing.
	Upsert(ctx context.Context, in SetOutcomeBarInput) (OutcomeBar, error)

	// GetByChannel returns pgx.ErrNoRows when no bar has ever been set
	// for channelID -- the MCP layer (its own task) maps this to FR2's
	// explicit "not configured" result; this package does not invent a
	// sentinel ErrOutcomeBarNotConfigured.
	GetByChannel(ctx context.Context, channelID uuid.UUID) (OutcomeBar, error)
}

// outcomeBarStore implements OutcomeBarStore against `outcome_bar`
// (migration 014).
type outcomeBarStore struct{ pool *pgxpool.Pool }

var _ OutcomeBarStore = outcomeBarStore{}

// Upsert validates in before touching the database -- either rejection
// (ErrUnsupportedOutcomeBarMetric, ErrInvalidOutcomeBarThreshold) writes
// nothing -- then relies on the ON CONFLICT (channel_id) DO UPDATE idiom
// (store/person.go's UpsertByGoogleSubject, store/sync.go's UpsertVideos)
// to converge on the single row for in.ChannelID in one round trip
// (NFR1).
func (s outcomeBarStore) Upsert(ctx context.Context, in SetOutcomeBarInput) (OutcomeBar, error) {
	if in.MetricName != OutcomeBarMetricViews {
		return OutcomeBar{}, ErrUnsupportedOutcomeBarMetric
	}
	if in.ThresholdValue < 0 {
		return OutcomeBar{}, ErrInvalidOutcomeBarThreshold
	}

	var b OutcomeBar
	err := s.pool.QueryRow(ctx, `
		INSERT INTO outcome_bar (channel_id, metric_name, threshold_value, updated_by_person_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (channel_id) DO UPDATE
			SET metric_name = EXCLUDED.metric_name,
			    threshold_value = EXCLUDED.threshold_value,
			    updated_at = NOW(),
			    updated_by_person_id = EXCLUDED.updated_by_person_id
		RETURNING id, channel_id, metric_name, threshold_value, updated_at, updated_by_person_id
	`, in.ChannelID, in.MetricName, in.ThresholdValue, in.UpdatedByPersonID).Scan(
		&b.ID, &b.ChannelID, &b.MetricName, &b.ThresholdValue, &b.UpdatedAt, &b.UpdatedByPersonID,
	)
	if err != nil {
		return OutcomeBar{}, fmt.Errorf("upsert outcome_bar: %w", err)
	}
	return b, nil
}

// GetByChannel returns pgx.ErrNoRows unwrapped-and-recognisable (same
// contract as PersonStore.GetByID / StrategyStore.GetByID) when no bar
// has ever been set for channelID.
func (s outcomeBarStore) GetByChannel(ctx context.Context, channelID uuid.UUID) (OutcomeBar, error) {
	var b OutcomeBar
	err := s.pool.QueryRow(ctx, `
		SELECT id, channel_id, metric_name, threshold_value, updated_at, updated_by_person_id
		FROM outcome_bar
		WHERE channel_id = $1
	`, channelID).Scan(
		&b.ID, &b.ChannelID, &b.MetricName, &b.ThresholdValue, &b.UpdatedAt, &b.UpdatedByPersonID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OutcomeBar{}, pgx.ErrNoRows
		}
		return OutcomeBar{}, fmt.Errorf("get outcome_bar by channel: %w", err)
	}
	return b, nil
}
