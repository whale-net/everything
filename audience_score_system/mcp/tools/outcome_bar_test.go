package tools

// Pure-Go coverage for outcome_bar.go's ChannelScopeID() on both input
// types, get_outcome_bar's handler (both the FR2 "not configured" branch
// and the fully-populated render), and set_outcome_bar's mutate rejecting
// an unauthenticated caller -- all driven against an in-memory fake
// store.OutcomeBarStore, no Postgres or MCP transport needed, mirroring
// access_test.go's split.
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
