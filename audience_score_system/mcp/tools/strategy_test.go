package tools

// Pure-Go, table-driven coverage for strategy.go's pure functions:
// parseCadence (the vocabulary save_strategy's cadence accepts),
// advanceCadence (weekly/biweekly/monthly interval math), and
// rollToWeekday (generate_schedule_plan's preferred-weekday roll-forward).
// No Postgres or MCP transport needed here -- that end-to-end coverage
// (save_strategy/get_strategy/list_strategies/generate_schedule_plan
// against a real database and a real in-process MCP client, including the
// FR16-style viable-verdict gate) is strategy_integration_test.go's job
// (build tag "integration"), mirroring schedule_draft_test.go's split.

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// ── parseCadence ─────────────────────────────────────────────────────────

func TestParseCadence_AcceptsAllThreeCaseInsensitivelyTrimmed(t *testing.T) {
	cases := []struct {
		raw  string
		want store.Cadence
	}{
		{"weekly", store.CadenceWeekly},
		{"Weekly", store.CadenceWeekly},
		{"  WEEKLY  ", store.CadenceWeekly},
		{"biweekly", store.CadenceBiweekly},
		{"monthly", store.CadenceMonthly},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseCadence(tc.raw)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseCadence_RejectsAnythingElse(t *testing.T) {
	for _, raw := range []string{"", "daily", "yearly", "week"} {
		t.Run(raw, func(t *testing.T) {
			got, err := parseCadence(raw)
			assert.Error(t, err)
			assert.Empty(t, got)
		})
	}
}

// ── advanceCadence ───────────────────────────────────────────────────────

var anAnchor = time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC) // a Wednesday.

func TestAdvanceCadence(t *testing.T) {
	assert.Equal(t, anAnchor.AddDate(0, 0, 7), advanceCadence(anAnchor, store.CadenceWeekly))
	assert.Equal(t, anAnchor.AddDate(0, 0, 14), advanceCadence(anAnchor, store.CadenceBiweekly))
	assert.Equal(t, anAnchor.AddDate(0, 1, 0), advanceCadence(anAnchor, store.CadenceMonthly))
}

func TestAdvanceCadence_UnrecognizedValueFallsBackToWeekly(t *testing.T) {
	assert.Equal(t, anAnchor.AddDate(0, 0, 7), advanceCadence(anAnchor, store.Cadence("bogus")))
}

// ── rollToWeekday ────────────────────────────────────────────────────────

func TestRollToWeekday_AlreadyOnTargetDay_Unchanged(t *testing.T) {
	assert.True(t, anAnchor.Weekday() == time.Wednesday)
	got := rollToWeekday(anAnchor, "Wednesday")
	assert.Equal(t, anAnchor, got)
}

func TestRollToWeekday_AdvancesForwardToNextOccurrence(t *testing.T) {
	got := rollToWeekday(anAnchor, "Friday") // Wednesday -> Friday, +2 days.
	assert.Equal(t, anAnchor.AddDate(0, 0, 2), got)
	assert.Equal(t, time.Friday, got.UTC().Weekday())
}

func TestRollToWeekday_WrapsAroundTheWeek(t *testing.T) {
	got := rollToWeekday(anAnchor, "Monday") // Wednesday -> next Monday, +5 days.
	assert.Equal(t, anAnchor.AddDate(0, 0, 5), got)
	assert.Equal(t, time.Monday, got.UTC().Weekday())
}

func TestRollToWeekday_NeverRollsBackward(t *testing.T) {
	for d := time.Sunday; d <= time.Saturday; d++ {
		got := rollToWeekday(anAnchor, d.String())
		assert.False(t, got.Before(anAnchor), "rollToWeekday must never move earlier than its input")
	}
}

// ── ChannelScopeID / IdempotencyKey sanity for every input type ─────────

func TestStrategyInputs_ChannelScopeID(t *testing.T) {
	id := uuid.New()

	assert.Equal(t, id, SaveStrategyInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, SaveStrategyInput{ChannelID: "not-a-uuid"}.ChannelScopeID())

	assert.Equal(t, id, GetStrategyInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, id, ListStrategiesInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, id, GenerateSchedulePlanInput{ChannelID: id.String()}.ChannelScopeID())
}

func TestSaveStrategyInput_IdempotencyKey(t *testing.T) {
	in := SaveStrategyInput{IdempotencyKeyArg: "abc"}
	assert.Equal(t, "abc", in.IdempotencyKey())
}

// ── pacingTracker ────────────────────────────────────────────────────────

func TestPacingTracker_NilPolicy_AlwaysFits(t *testing.T) {
	tr := newPacingTracker(nil, nil, nil)
	assert.True(t, tr.fits(anAnchor))
	tr.commit(anAnchor)
	assert.True(t, tr.fits(anAnchor), "commit must be a no-op with no policy set")
}

func TestPacingTracker_FitsUnderTarget(t *testing.T) {
	policy := &store.PacingPolicy{TargetUploadsPerWeek: 2}
	tr := newPacingTracker(policy, nil, nil)
	assert.True(t, tr.fits(anAnchor), "0 existing + 1 new = 1, within target 2")
}

func TestPacingTracker_BaselineFromPersistedEntries_CountsTowardTarget(t *testing.T) {
	policy := &store.PacingPolicy{TargetUploadsPerWeek: 1}
	entries := []store.ScheduleEntry{{ProposedPublishAt: anAnchor}}
	tr := newPacingTracker(policy, entries, nil)
	assert.False(t, tr.fits(anAnchor.Add(2*time.Hour)), "1 persisted + 1 new = 2, exceeds target 1")
	assert.True(t, tr.fits(anAnchor.AddDate(0, 0, 7)), "a different calendar week is unaffected")
}

func TestPacingTracker_BaselineFromSyncedVideos_CountsTowardTarget(t *testing.T) {
	policy := &store.PacingPolicy{TargetUploadsPerWeek: 1}
	publishedAt := anAnchor
	synced := []store.SyncedVideo{{PublishedAt: &publishedAt}}
	tr := newPacingTracker(policy, nil, synced)
	assert.False(t, tr.fits(anAnchor.Add(time.Hour)))
}

func TestPacingTracker_Commit_AccumulatesWithinACall(t *testing.T) {
	policy := &store.PacingPolicy{TargetUploadsPerWeek: 2}
	tr := newPacingTracker(policy, nil, nil)

	assert.True(t, tr.fits(anAnchor), "1st entry: 0+1=1, fits target 2")
	tr.commit(anAnchor)
	assert.True(t, tr.fits(anAnchor.Add(time.Hour)), "2nd entry same week: 1+1=2, fits target 2")
	tr.commit(anAnchor.Add(time.Hour))
	assert.False(t, tr.fits(anAnchor.Add(2*time.Hour)), "3rd entry same week: 2+1=3, exceeds target 2")
}

func TestPacingTracker_FractionalTarget_UnderOneNeverFits(t *testing.T) {
	// A target below 1/week means no week can ever hold even a single
	// entry without exceeding it -- generate_schedule_plan's bounded
	// maxPacingPushWeeks loop (not pacingTracker itself) is what keeps
	// this from pushing forever.
	policy := &store.PacingPolicy{TargetUploadsPerWeek: 0.5}
	tr := newPacingTracker(policy, nil, nil)
	assert.False(t, tr.fits(anAnchor))
}
