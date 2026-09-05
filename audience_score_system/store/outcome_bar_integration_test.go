//go:build integration

// outcome_bar_integration_test.go covers OutcomeBarStore (migration 014,
// issue #1882): Upsert/GetByChannel round-tripping, NFR1's converge-on-one-
// row idempotency, per-Channel isolation, GetByChannel's pgx.ErrNoRows
// "never configured" contract, and Upsert's pre-database validation
// (ErrUnsupportedOutcomeBarMetric, ErrInvalidOutcomeBarThreshold) writing
// nothing. Same package/build tag/harness as
// video_script_integration_test.go -- newStore/setupChannel
// (store_integration_test.go) are reused directly.
package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// ── OutcomeBarStore.Upsert / GetByChannel round trip ────────────────────────

func TestOutcomeBarStore_Upsert_InsertsAndRoundTripsThroughGetByChannel(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	got, err := s.OutcomeBars().Upsert(ctx, store.SetOutcomeBarInput{
		ChannelID: ch.ID, MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1000,
		UpdatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	assert.NotEqual(t, ch.ID, got.ID, "the returned row's own id must not be the channel_id")
	assert.Equal(t, ch.ID, got.ChannelID)
	assert.Equal(t, store.OutcomeBarMetricViews, got.MetricName)
	assert.Equal(t, float64(1000), got.ThresholdValue)
	assert.Equal(t, creator.ID, got.UpdatedByPersonID)

	fetched, err := s.OutcomeBars().GetByChannel(ctx, ch.ID)
	require.NoError(t, err)
	assert.Equal(t, got.ID, fetched.ID)
	assert.Equal(t, got.ChannelID, fetched.ChannelID)
	assert.Equal(t, got.MetricName, fetched.MetricName)
	assert.Equal(t, got.ThresholdValue, fetched.ThresholdValue)
	assert.Equal(t, got.UpdatedByPersonID, fetched.UpdatedByPersonID)
}

// ── NFR1 convergence ─────────────────────────────────────────────────────────

func TestOutcomeBarStore_Upsert_IdenticalValuesTwice_ConvergesOnOneRow(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	first, err := s.OutcomeBars().Upsert(ctx, store.SetOutcomeBarInput{
		ChannelID: ch.ID, MetricName: store.OutcomeBarMetricViews, ThresholdValue: 5000,
		UpdatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	second, err := s.OutcomeBars().Upsert(ctx, store.SetOutcomeBarInput{
		ChannelID: ch.ID, MetricName: store.OutcomeBarMetricViews, ThresholdValue: 5000,
		UpdatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	assert.Equal(t, first.ID, second.ID, "two Upserts with an identical (channel_id, metric_name, threshold_value) triple must yield the same row id (NFR1)")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM outcome_bar WHERE channel_id = $1`, ch.ID).Scan(&count))
	assert.Equal(t, 1, count, "repeated identical Upserts must leave exactly one outcome_bar row for the channel (NFR1)")
}

func TestOutcomeBarStore_Upsert_ChangedThreshold_UpdatesInPlace(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	otherPerson, _, err := s.Persons().UpsertByGoogleSubject(ctx, "sub-outcome-bar-updater", "updater@example.com", "Updater")
	require.NoError(t, err)

	first, err := s.OutcomeBars().Upsert(ctx, store.SetOutcomeBarInput{
		ChannelID: ch.ID, MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1000,
		UpdatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	updated, err := s.OutcomeBars().Upsert(ctx, store.SetOutcomeBarInput{
		ChannelID: ch.ID, MetricName: store.OutcomeBarMetricViews, ThresholdValue: 2000,
		UpdatedByPersonID: otherPerson.ID,
	})
	require.NoError(t, err)

	assert.Equal(t, first.ID, updated.ID, "an Upsert with a changed threshold must update the same row, not insert a second one")
	assert.Equal(t, float64(2000), updated.ThresholdValue)
	assert.Equal(t, otherPerson.ID, updated.UpdatedByPersonID, "updated_by_person_id must reflect the second caller")
	assert.True(t, !updated.UpdatedAt.Before(first.UpdatedAt), "updated_at must advance (or at least not go backwards) on update")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM outcome_bar WHERE channel_id = $1`, ch.ID).Scan(&count))
	assert.Equal(t, 1, count, "an update must still leave exactly one row for the channel")
}

// ── Per-Channel isolation ────────────────────────────────────────────────────

func TestOutcomeBarStore_Upsert_TwoChannels_KeepIndependentRows(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch1, creator1 := setupChannel(t, ctx, s)
	ch2, creator2 := setupChannel(t, ctx, s)

	bar1, err := s.OutcomeBars().Upsert(ctx, store.SetOutcomeBarInput{
		ChannelID: ch1.ID, MetricName: store.OutcomeBarMetricViews, ThresholdValue: 100,
		UpdatedByPersonID: creator1.ID,
	})
	require.NoError(t, err)

	bar2, err := s.OutcomeBars().Upsert(ctx, store.SetOutcomeBarInput{
		ChannelID: ch2.ID, MetricName: store.OutcomeBarMetricViews, ThresholdValue: 200,
		UpdatedByPersonID: creator2.ID,
	})
	require.NoError(t, err)

	assert.NotEqual(t, bar1.ID, bar2.ID)

	got1, err := s.OutcomeBars().GetByChannel(ctx, ch1.ID)
	require.NoError(t, err)
	assert.Equal(t, float64(100), got1.ThresholdValue, "ch1's bar must not be affected by ch2's Upsert")

	got2, err := s.OutcomeBars().GetByChannel(ctx, ch2.ID)
	require.NoError(t, err)
	assert.Equal(t, float64(200), got2.ThresholdValue)
}

// ── GetByChannel "never configured" contract ────────────────────────────────

func TestOutcomeBarStore_GetByChannel_NeverConfigured_ReturnsErrNoRows(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, _ := setupChannel(t, ctx, s)

	_, err := s.OutcomeBars().GetByChannel(ctx, ch.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, pgx.ErrNoRows), "GetByChannel on a channel with no bar must return pgx.ErrNoRows, recognisable via errors.Is")
}

// ── Upsert pre-database validation ──────────────────────────────────────────

func TestOutcomeBarStore_Upsert_UnsupportedMetric_RejectedNothingWritten(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	_, err := s.OutcomeBars().Upsert(ctx, store.SetOutcomeBarInput{
		ChannelID: ch.ID, MetricName: "ctr", ThresholdValue: 100,
		UpdatedByPersonID: creator.ID,
	})
	assert.ErrorIs(t, err, store.ErrUnsupportedOutcomeBarMetric, "Upsert must reject any metric_name other than \"views\" (FR1)")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM outcome_bar WHERE channel_id = $1`, ch.ID).Scan(&count))
	assert.Equal(t, 0, count, "a rejected metric_name must not write any row")
}

func TestOutcomeBarStore_Upsert_NegativeThreshold_RejectedNothingWritten(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	_, err := s.OutcomeBars().Upsert(ctx, store.SetOutcomeBarInput{
		ChannelID: ch.ID, MetricName: store.OutcomeBarMetricViews, ThresholdValue: -1,
		UpdatedByPersonID: creator.ID,
	})
	assert.ErrorIs(t, err, store.ErrInvalidOutcomeBarThreshold, "Upsert must reject a negative threshold_value")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM outcome_bar WHERE channel_id = $1`, ch.ID).Scan(&count))
	assert.Equal(t, 0, count, "a rejected negative threshold must not write any row")
}
