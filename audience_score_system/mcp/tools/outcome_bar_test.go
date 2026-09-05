package tools

// Pure-Go coverage for outcome_bar.go's ChannelScopeID() on both input
// types, get_outcome_bar's handler (both the FR2 "not configured" branch
// and the fully-populated render), set_outcome_bar's mutate rejecting
// an unauthenticated caller, and get_calibration_trend's handler
// (FR5/FR6/FR7, issue #1885) -- all driven against in-memory fakes for
// store.OutcomeBarStore and store.CalibrationStore, no Postgres or MCP
// transport needed, mirroring access_test.go's split.
//
// What this file deliberately does NOT cover: setOutcomeBarMutate's
// person.ID -> UpdatedByPersonID pass-through, and its mapping of
// store.ErrUnsupportedOutcomeBarMetric / store.ErrInvalidOutcomeBarThreshold
// to caller-facing messages. Both live behind server.PersonFromContext,
// whose backing context key (context.go's personContextKey) is
// package-private to mcp/server -- there is no way for this package's
// test file to place a *store.Person on ctx the way the real auth
// middleware does, only to prove its absence (the unauthenticated path
// below). access_test.go hits the identical wall for InviteCoCreatorInput/
// PromoteToCoCreatorInput/RemoveChannelPersonInput's mutate functions and
// draws the same line. Both are instead covered end-to-end, against a
// real Person resolved by the real auth middleware, in
// outcome_bar_integration_test.go (build tag "integration"):
// TestSetOutcomeBar_UpdatedByPersonID_TracksCaller and
// TestSetOutcomeBar_InvalidMetricAndThreshold_RejectedWithReadableMessage.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// ── ChannelScopeID ────────────────────────────────────────────────────────

func TestOutcomeBarInputs_ChannelScopeID(t *testing.T) {
	id := uuid.New()

	assert.Equal(t, id, SetOutcomeBarInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, SetOutcomeBarInput{ChannelID: "not-a-uuid"}.ChannelScopeID())

	assert.Equal(t, id, GetOutcomeBarInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, GetOutcomeBarInput{ChannelID: "not-a-uuid"}.ChannelScopeID())

	assert.Equal(t, id, GetCalibrationTrendInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, GetCalibrationTrendInput{ChannelID: "not-a-uuid"}.ChannelScopeID())
}

// ── fake store.OutcomeBarStore ───────────────────────────────────────────

// fakeOutcomeBarStore is a configurable, in-memory store.OutcomeBarStore
// stand-in scoped to what outcome_bar.go's handler/mutate/render functions
// need, following access_test.go's fakeAccessRoleStore style.
type fakeOutcomeBarStore struct {
	getBar store.OutcomeBar
	getErr error

	upsertResult store.OutcomeBar
	upsertErr    error
	upsertCalls  []store.SetOutcomeBarInput
}

var _ store.OutcomeBarStore = (*fakeOutcomeBarStore)(nil)

func (f *fakeOutcomeBarStore) Upsert(_ context.Context, in store.SetOutcomeBarInput) (store.OutcomeBar, error) {
	f.upsertCalls = append(f.upsertCalls, in)
	if f.upsertErr != nil {
		return store.OutcomeBar{}, f.upsertErr
	}
	return f.upsertResult, nil
}

func (f *fakeOutcomeBarStore) GetByChannel(_ context.Context, _ uuid.UUID) (store.OutcomeBar, error) {
	if f.getErr != nil {
		return store.OutcomeBar{}, f.getErr
	}
	return f.getBar, nil
}

// ── get_outcome_bar ──────────────────────────────────────────────────────

func TestGetOutcomeBar_NotConfigured_ReturnsConfiguredFalseNoThreshold(t *testing.T) {
	fake := &fakeOutcomeBarStore{getErr: pgx.ErrNoRows}
	h := getOutcomeBarHandler(fake)

	res, out, err := h(context.Background(), nil, GetOutcomeBarInput{ChannelID: uuid.New().String()})
	require.NoError(t, err, "FR2: not-configured is a successful response, never an error")
	assert.Nil(t, res)
	assert.Equal(t, OutcomeBarOutput{Configured: false}, out)
	assert.Nil(t, out.ThresholdValue, "must never be a defaulted threshold")
	assert.Empty(t, out.MetricName)
	assert.Empty(t, out.UpdatedAt)
	assert.Empty(t, out.UpdatedByPersonID)
}

func TestGetOutcomeBar_Configured_RendersEveryField(t *testing.T) {
	channelID := uuid.New()
	personID := uuid.New()
	updatedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fake := &fakeOutcomeBarStore{getBar: store.OutcomeBar{
		ID:                uuid.New(),
		ChannelID:         channelID,
		MetricName:        store.OutcomeBarMetricViews,
		ThresholdValue:    1000,
		UpdatedAt:         updatedAt,
		UpdatedByPersonID: personID,
	}}
	h := getOutcomeBarHandler(fake)

	_, out, err := h(context.Background(), nil, GetOutcomeBarInput{ChannelID: channelID.String()})
	require.NoError(t, err)
	assert.True(t, out.Configured)
	assert.Equal(t, store.OutcomeBarMetricViews, out.MetricName)
	require.NotNil(t, out.ThresholdValue)
	assert.Equal(t, 1000.0, *out.ThresholdValue)
	assert.Equal(t, updatedAt.Format(time.RFC3339), out.UpdatedAt)
	assert.Equal(t, personID.String(), out.UpdatedByPersonID)
}

func TestGetOutcomeBar_OtherStoreError_Propagated(t *testing.T) {
	fake := &fakeOutcomeBarStore{getErr: assert.AnError}
	h := getOutcomeBarHandler(fake)

	_, out, err := h(context.Background(), nil, GetOutcomeBarInput{ChannelID: uuid.New().String()})
	assert.Error(t, err, "only pgx.ErrNoRows collapses to configured:false -- any other error must surface")
	assert.Equal(t, OutcomeBarOutput{}, out)
}

// ── set_outcome_bar: unauthenticated rejection ───────────────────────────

// TestSetOutcomeBar_Mutate_RejectsUnauthenticatedCaller proves mutate never
// reaches bars.Upsert without a Person resolved on ctx -- the only half of
// NFR2's authentication requirement this package can drive without the
// real auth middleware (see file header). In production RegisterWrite
// guarantees a Person is always resolved before mutate ever runs.
func TestSetOutcomeBar_Mutate_RejectsUnauthenticatedCaller(t *testing.T) {
	fake := &fakeOutcomeBarStore{}
	mutate := setOutcomeBarMutate(fake)

	_, err := mutate(context.Background(), SetOutcomeBarInput{
		ChannelID:      uuid.New().String(),
		MetricName:     store.OutcomeBarMetricViews,
		ThresholdValue: 1000,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unauthenticated")
	assert.Empty(t, fake.upsertCalls, "an unauthenticated call must never reach the store")
}

func TestSetOutcomeBar_Mutate_RejectsUnparseableChannelID(t *testing.T) {
	fake := &fakeOutcomeBarStore{}
	mutate := setOutcomeBarMutate(fake)

	_, err := mutate(context.Background(), SetOutcomeBarInput{ChannelID: "not-a-uuid"})
	assert.Error(t, err)
	assert.Empty(t, fake.upsertCalls)
}

// ── fake store.CalibrationStore ──────────────────────────────────────────

// fakeCalibrationStore is a configurable, in-memory store.CalibrationStore
// stand-in scoped to what getCalibrationTrendHandler needs -- records every
// MonthlyTrend call's arguments so tests can assert on what the handler
// passed through (default-limit selection, FR6's "never called" branch)
// without duplicating any bucketing/rate logic itself.
type fakeCalibrationStore struct {
	rows      []store.CalibrationBucket
	truncated bool
	err       error

	calls []fakeCalibrationTrendCall
}

type fakeCalibrationTrendCall struct {
	channelID     uuid.UUID
	bar           store.OutcomeBar
	since, before *time.Time
	limit         int
}

var _ store.CalibrationStore = (*fakeCalibrationStore)(nil)

func (f *fakeCalibrationStore) MonthlyTrend(_ context.Context, channelID uuid.UUID, bar store.OutcomeBar, since, before *time.Time, limit int) ([]store.CalibrationBucket, bool, error) {
	f.calls = append(f.calls, fakeCalibrationTrendCall{channelID: channelID, bar: bar, since: since, before: before, limit: limit})
	if f.err != nil {
		return nil, false, f.err
	}
	return f.rows, f.truncated, nil
}

// ── get_calibration_trend ────────────────────────────────────────────────

// TestGetCalibrationTrend_NotConfigured_ReturnsNotConfiguredAndNeverQueriesCalibration
// is FR6's central assertion: on pgx.ErrNoRows from GetByChannel, the
// handler must short-circuit to the shared not-configured shape with an
// empty, non-nil Buckets slice, Truncated false, a nil error -- and,
// critically, must never call calibration.MonthlyTrend at all (there is no
// bar to classify against).
func TestGetCalibrationTrend_NotConfigured_ReturnsNotConfiguredAndNeverQueriesCalibration(t *testing.T) {
	bars := &fakeOutcomeBarStore{getErr: pgx.ErrNoRows}
	calibration := &fakeCalibrationStore{}
	h := getCalibrationTrendHandler(bars, calibration)

	res, out, err := h(context.Background(), nil, GetCalibrationTrendInput{ChannelID: uuid.New().String()})
	require.NoError(t, err, "FR6: not-configured is a successful response, never an error")
	assert.Nil(t, res)
	assert.Equal(t, notConfiguredOutcomeBar(), out.OutcomeBar)
	assert.NotNil(t, out.Buckets, "Buckets must be non-nil so a client never distinguishes null from []")
	assert.Empty(t, out.Buckets)
	assert.False(t, out.Truncated)
	assert.Empty(t, calibration.calls, "calibration.MonthlyTrend must never be called when no bar is configured")
}

// TestGetCalibrationTrend_DefaultLimit_ReachesStoreAsTwelve proves
// Limit: 0 (the zero value / omitted) is defaulted to
// defaultCalibrationTrendLimit before reaching the store, not passed
// through as a literal 0 (which store.CalibrationStore.MonthlyTrend
// treats as unbounded).
func TestGetCalibrationTrend_DefaultLimit_ReachesStoreAsTwelve(t *testing.T) {
	channelID := uuid.New()
	bar := store.OutcomeBar{MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1000}
	bars := &fakeOutcomeBarStore{getBar: bar}
	calibration := &fakeCalibrationStore{}
	h := getCalibrationTrendHandler(bars, calibration)

	_, _, err := h(context.Background(), nil, GetCalibrationTrendInput{ChannelID: channelID.String()})
	require.NoError(t, err)
	require.Len(t, calibration.calls, 1)
	assert.Equal(t, defaultCalibrationTrendLimit, calibration.calls[0].limit)
	assert.Equal(t, 12, calibration.calls[0].limit, "default is stated as twelve months of buckets")
	assert.Equal(t, channelID, calibration.calls[0].channelID)
	assert.Equal(t, bar, calibration.calls[0].bar, "must classify against the CURRENT bar just read (FR4)")
}

// TestGetCalibrationTrend_ExplicitLimit_ReachesStoreUnchanged proves a
// caller-supplied Limit passes straight through, uncapped and undefaulted.
func TestGetCalibrationTrend_ExplicitLimit_ReachesStoreUnchanged(t *testing.T) {
	bars := &fakeOutcomeBarStore{getBar: store.OutcomeBar{MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1000}}
	calibration := &fakeCalibrationStore{}
	h := getCalibrationTrendHandler(bars, calibration)

	_, _, err := h(context.Background(), nil, GetCalibrationTrendInput{ChannelID: uuid.New().String(), Limit: 3})
	require.NoError(t, err)
	require.Len(t, calibration.calls, 1)
	assert.Equal(t, 3, calibration.calls[0].limit)
}

// TestGetCalibrationTrend_SinceBefore_PassedThroughUnchanged proves
// Since/Before reach calibration.MonthlyTrend exactly as given, with no
// reinterpretation in the handler.
func TestGetCalibrationTrend_SinceBefore_PassedThroughUnchanged(t *testing.T) {
	bars := &fakeOutcomeBarStore{getBar: store.OutcomeBar{MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1000}}
	calibration := &fakeCalibrationStore{}
	h := getCalibrationTrendHandler(bars, calibration)

	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	_, _, err := h(context.Background(), nil, GetCalibrationTrendInput{ChannelID: uuid.New().String(), Since: &since, Before: &before})
	require.NoError(t, err)
	require.Len(t, calibration.calls, 1)
	require.NotNil(t, calibration.calls[0].since)
	require.NotNil(t, calibration.calls[0].before)
	assert.True(t, since.Equal(*calibration.calls[0].since))
	assert.True(t, before.Equal(*calibration.calls[0].before))
}

// TestGetCalibrationTrend_RendersRowsInStoreOrder_TruncatedPassedThrough
// proves the handler neither re-sorts the store's rows nor recomputes
// truncated -- both are rendered exactly as store.CalibrationStore
// returned them (FR5/FR7).
func TestGetCalibrationTrend_RendersRowsInStoreOrder_TruncatedPassedThrough(t *testing.T) {
	channelID := uuid.New()
	bar := store.OutcomeBar{MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1000, ChannelID: channelID}
	jan := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	bars := &fakeOutcomeBarStore{getBar: bar}
	calibration := &fakeCalibrationStore{
		rows: []store.CalibrationBucket{
			{BucketStart: jan, Candidates: 3, Calibrated: 2, Miscalibrated: 1, Rate: 2.0 / 3.0},
			{BucketStart: feb, Candidates: 2, Calibrated: 0, Miscalibrated: 2, Rate: 0},
		},
		truncated: true,
	}
	h := getCalibrationTrendHandler(bars, calibration)

	_, out, err := h(context.Background(), nil, GetCalibrationTrendInput{ChannelID: channelID.String()})
	require.NoError(t, err)
	assert.True(t, out.Truncated, "truncated must be passed through unchanged")
	require.Len(t, out.Buckets, 2)
	assert.Equal(t, jan.Format(time.RFC3339), out.Buckets[0].BucketStart, "must render in the store's returned order (chronological), never re-sorted")
	assert.Equal(t, feb.Format(time.RFC3339), out.Buckets[1].BucketStart)
	assert.Equal(t, 3, out.Buckets[0].Candidates)
	assert.Equal(t, 2, out.Buckets[0].Calibrated)
	assert.Equal(t, 1, out.Buckets[0].Miscalibrated)
	assert.InDelta(t, 2.0/3.0, out.Buckets[0].CalibrationRate, 1e-9)
	assert.True(t, out.OutcomeBar.Configured)
	assert.Equal(t, toOutcomeBarOutput(bar), out.OutcomeBar, "must echo the bar classified against")
}

// TestGetCalibrationTrend_OtherStoreError_Propagated proves only
// pgx.ErrNoRows collapses to the not-configured shape -- any other error
// from GetByChannel must surface.
func TestGetCalibrationTrend_OtherStoreError_Propagated(t *testing.T) {
	bars := &fakeOutcomeBarStore{getErr: assert.AnError}
	calibration := &fakeCalibrationStore{}
	h := getCalibrationTrendHandler(bars, calibration)

	_, out, err := h(context.Background(), nil, GetCalibrationTrendInput{ChannelID: uuid.New().String()})
	assert.Error(t, err, "only pgx.ErrNoRows collapses to not-configured -- any other error must surface")
	assert.Equal(t, GetCalibrationTrendOutput{}, out)
	assert.Empty(t, calibration.calls, "must not query calibration when the bar lookup itself failed")
}

// TestGetCalibrationTrend_CalibrationStoreError_Propagated proves an error
// from calibration.MonthlyTrend itself (once a bar IS configured) also
// surfaces rather than being swallowed.
func TestGetCalibrationTrend_CalibrationStoreError_Propagated(t *testing.T) {
	bars := &fakeOutcomeBarStore{getBar: store.OutcomeBar{MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1000}}
	calibration := &fakeCalibrationStore{err: assert.AnError}
	h := getCalibrationTrendHandler(bars, calibration)

	_, out, err := h(context.Background(), nil, GetCalibrationTrendInput{ChannelID: uuid.New().String()})
	assert.Error(t, err)
	assert.Equal(t, GetCalibrationTrendOutput{}, out)
}
