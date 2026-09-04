package tools

// Pure-Go, table-driven coverage for schedule_draft.go's pure functions:
// parseWeekdayName (the vocabulary set_pacing_policy's preferred_days
// accepts), weekBounds (the Monday-Sunday UTC week FR18's cadence check
// reasons about), and computeScheduleFlags (FR18's non-blocking
// cadence_exceeded/off_preferred_day/collision derivation). No Postgres or
// MCP transport needed here -- that end-to-end coverage (save_
// schedule_draft/set_pacing_policy/get_drafting_context/
// list_schedule_entries against a real database and a real in-process MCP
// client, including the LB3 verdict-version-binding and FR18
// freshly-derived-on-replay assertions) is schedule_draft_integration_
// test.go's job (build tag "integration"), per issue #1579's Testing
// section.

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// ── parseWeekdayName ─────────────────────────────────────────────────────

func TestParseWeekdayName_AcceptsAllSevenCaseInsensitivelyTrimmed(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"Monday", "Monday"},
		{"monday", "Monday"},
		{"MONDAY", "Monday"},
		{"  Tuesday  ", "Tuesday"},
		{"wednesday", "Wednesday"},
		{"thursday", "Thursday"},
		{"friday", "Friday"},
		{"saturday", "Saturday"},
		{"sunday", "Sunday"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseWeekdayName(tc.raw)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseWeekdayName_RejectsAnythingElse(t *testing.T) {
	for _, raw := range []string{"", "Mon", "Someday", "monday1"} {
		t.Run(raw, func(t *testing.T) {
			got, err := parseWeekdayName(raw)
			assert.Error(t, err, "must reject %q rather than silently storing a value time.Weekday().String() would never match", raw)
			assert.Empty(t, got)
		})
	}
}

// ── weekBounds ───────────────────────────────────────────────────────────

// aMonday is an arbitrary but fixed anchor -- confirmed a Monday by
// construction below, never by a hardcoded calendar date that could rot.
var aMonday = mustMonday(time.Date(2031, 3, 5, 0, 0, 0, 0, time.UTC))

func mustMonday(anchor time.Time) time.Time {
	daysSinceMonday := (int(anchor.Weekday()) + 6) % 7
	return anchor.AddDate(0, 0, -daysSinceMonday)
}

func TestWeekBounds_MondayMidnightToNextMondayMidnightUTC(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
	}{
		{"exactly Monday 00:00 UTC", aMonday},
		{"Monday noon", aMonday.Add(12 * time.Hour)},
		{"Wednesday", aMonday.AddDate(0, 0, 2).Add(9 * time.Hour)},
		{"Sunday 23:59:59", aMonday.AddDate(0, 0, 6).Add(23*time.Hour + 59*time.Minute + 59*time.Second)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := weekBounds(tc.in)
			assert.Equal(t, aMonday, start, "week start must always be this Monday 00:00 UTC")
			assert.Equal(t, aMonday.AddDate(0, 0, 7), end, "week end must be the following Monday 00:00 UTC (exclusive)")
			assert.True(t, time.Monday == start.Weekday())
			assert.True(t, !tc.in.Before(start) && tc.in.Before(end), "input must fall within its own computed [start, end)")
		})
	}
}

func TestWeekBounds_NonUTCInputConvertedFirst(t *testing.T) {
	loc := time.FixedZone("UTC-8", -8*60*60)
	// aMonday+2h UTC, expressed in UTC-8, is still the previous calendar
	// day there -- weekBounds must convert to UTC before computing the
	// week, not reason in the input's own zone.
	inLocal := aMonday.Add(2 * time.Hour).In(loc)
	start, end := weekBounds(inLocal)
	assert.Equal(t, aMonday, start)
	assert.Equal(t, aMonday.AddDate(0, 0, 7), end)
}

func TestWeekBounds_DifferentWeeksProduceDifferentBounds(t *testing.T) {
	start1, _ := weekBounds(aMonday)
	start2, _ := weekBounds(aMonday.AddDate(0, 0, 7))
	assert.NotEqual(t, start1, start2)
	assert.Equal(t, start1.AddDate(0, 0, 7), start2)
}

// ── computeScheduleFlags ─────────────────────────────────────────────────

func flagTypes(flags []ScheduleFlag) []string {
	out := make([]string, len(flags))
	for i, f := range flags {
		out[i] = f.Type
	}
	return out
}

func TestComputeScheduleFlags_NoPolicyNoOtherEntries_NoFlags(t *testing.T) {
	entry := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: aMonday.Add(9 * time.Hour)}
	result := computeScheduleFlags(entry, nil, nil, nil)
	assert.False(t, result.cadenceExceeded)
	assert.False(t, result.offPreferredDay)
	assert.False(t, result.collision)
	assert.Empty(t, result.flags)
}

func TestComputeScheduleFlags_CadenceExceeded_OnlyWhenPolicySetAndCountExceedsTarget(t *testing.T) {
	entry := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: aMonday.Add(9 * time.Hour)}                   // Monday
	other1 := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: aMonday.AddDate(0, 0, 2).Add(9 * time.Hour)} // Wednesday, same week
	other2 := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: aMonday.AddDate(0, 0, 4).Add(9 * time.Hour)} // Friday, same week

	t.Run("target=2, entry+2 others=3 total -> exceeded", func(t *testing.T) {
		policy := &store.PacingPolicy{TargetUploadsPerWeek: 2}
		result := computeScheduleFlags(entry, policy, []store.ScheduleEntry{other1, other2}, nil)
		assert.True(t, result.cadenceExceeded)
		assert.Contains(t, flagTypes(result.flags), "cadence_exceeded")
	})

	t.Run("target=3, entry+2 others=3 total -> not exceeded (at target, not over)", func(t *testing.T) {
		policy := &store.PacingPolicy{TargetUploadsPerWeek: 3}
		result := computeScheduleFlags(entry, policy, []store.ScheduleEntry{other1, other2}, nil)
		assert.False(t, result.cadenceExceeded)
	})

	t.Run("no policy -> never flagged regardless of count", func(t *testing.T) {
		result := computeScheduleFlags(entry, nil, []store.ScheduleEntry{other1, other2}, nil)
		assert.False(t, result.cadenceExceeded)
	})

	t.Run("entries outside the entry's own week are not counted", func(t *testing.T) {
		nextWeek := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: aMonday.AddDate(0, 0, 9)}
		policy := &store.PacingPolicy{TargetUploadsPerWeek: 1}
		result := computeScheduleFlags(entry, policy, []store.ScheduleEntry{nextWeek}, nil)
		assert.False(t, result.cadenceExceeded, "an entry in a different week must not count toward this week's cadence")
	})

	t.Run("entry itself (same ID in otherEntries) is not double-counted", func(t *testing.T) {
		policy := &store.PacingPolicy{TargetUploadsPerWeek: 1}
		result := computeScheduleFlags(entry, policy, []store.ScheduleEntry{entry}, nil)
		assert.False(t, result.cadenceExceeded, "the entry's own row appearing in otherEntries must not be counted twice")
	})

	t.Run("synced videos in the same week count toward cadence", func(t *testing.T) {
		policy := &store.PacingPolicy{TargetUploadsPerWeek: 1}
		syncedAt := aMonday.AddDate(0, 0, 3).Add(9 * time.Hour)
		synced := []store.SyncedVideo{{YouTubeVideoID: "v1", PublishedAt: &syncedAt}}
		result := computeScheduleFlags(entry, policy, nil, synced)
		assert.True(t, result.cadenceExceeded, "entry(1) + synced video(1) = 2 > target 1")
	})
}

func TestComputeScheduleFlags_OffPreferredDay(t *testing.T) {
	monday := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: aMonday.Add(9 * time.Hour)}
	tuesday := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: aMonday.AddDate(0, 0, 1).Add(9 * time.Hour)}

	t.Run("weekday not in preferred_days -> flagged", func(t *testing.T) {
		policy := &store.PacingPolicy{TargetUploadsPerWeek: 100, PreferredDays: []string{"Monday"}}
		result := computeScheduleFlags(tuesday, policy, nil, nil)
		assert.True(t, result.offPreferredDay)
		assert.Contains(t, flagTypes(result.flags), "off_preferred_day")
	})

	t.Run("weekday in preferred_days -> not flagged", func(t *testing.T) {
		policy := &store.PacingPolicy{TargetUploadsPerWeek: 100, PreferredDays: []string{"Monday"}}
		result := computeScheduleFlags(monday, policy, nil, nil)
		assert.False(t, result.offPreferredDay)
	})

	t.Run("empty preferred_days -> never flagged", func(t *testing.T) {
		policy := &store.PacingPolicy{TargetUploadsPerWeek: 100, PreferredDays: nil}
		result := computeScheduleFlags(tuesday, policy, nil, nil)
		assert.False(t, result.offPreferredDay, "empty preferred_days must mean no day preference, never a flag")
	})

	t.Run("no policy -> never flagged", func(t *testing.T) {
		result := computeScheduleFlags(tuesday, nil, nil, nil)
		assert.False(t, result.offPreferredDay)
	})
}

func TestComputeScheduleFlags_Collision_IndependentOfPolicy(t *testing.T) {
	entry := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: aMonday.Add(9 * time.Hour)}

	t.Run("other schedule_entry 2h away -> collision", func(t *testing.T) {
		near := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: entry.ProposedPublishAt.Add(2 * time.Hour)}
		result := computeScheduleFlags(entry, nil, []store.ScheduleEntry{near}, nil)
		assert.True(t, result.collision, "no policy is set, but collision is never policy-gated")
		assert.Contains(t, flagTypes(result.flags), "collision")
	})

	t.Run("other schedule_entry exactly at the 12h boundary -> still collision (inclusive)", func(t *testing.T) {
		boundary := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: entry.ProposedPublishAt.Add(collisionWindow)}
		result := computeScheduleFlags(entry, nil, []store.ScheduleEntry{boundary}, nil)
		assert.True(t, result.collision)
	})

	t.Run("other schedule_entry just past the window -> no collision", func(t *testing.T) {
		far := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: entry.ProposedPublishAt.Add(collisionWindow + time.Second)}
		result := computeScheduleFlags(entry, nil, []store.ScheduleEntry{far}, nil)
		assert.False(t, result.collision)
	})

	t.Run("synced scheduled draft (PublishAt) 2h away -> collision", func(t *testing.T) {
		publishAt := entry.ProposedPublishAt.Add(-2 * time.Hour)
		synced := []store.SyncedVideo{{YouTubeVideoID: "sched-1", IsScheduledDraft: true, PublishAt: &publishAt}}
		result := computeScheduleFlags(entry, nil, nil, synced)
		assert.True(t, result.collision, "a synced scheduled/private draft within the window must collide")
	})

	t.Run("synced published video (PublishedAt) 2h away -> collision", func(t *testing.T) {
		publishedAt := entry.ProposedPublishAt.Add(2 * time.Hour)
		synced := []store.SyncedVideo{{YouTubeVideoID: "pub-1", PublishedAt: &publishedAt}}
		result := computeScheduleFlags(entry, nil, nil, synced)
		assert.True(t, result.collision)
	})

	t.Run("entry itself (same ID) never collides with itself", func(t *testing.T) {
		result := computeScheduleFlags(entry, nil, []store.ScheduleEntry{entry}, nil)
		assert.False(t, result.collision)
	})

	t.Run("nothing nearby -> no collision", func(t *testing.T) {
		far := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: entry.ProposedPublishAt.AddDate(0, 0, 3)}
		result := computeScheduleFlags(entry, nil, []store.ScheduleEntry{far}, nil)
		assert.False(t, result.collision)
	})
}

func TestComputeScheduleFlags_AllThreeCanFireTogether_EachWithItsOwnFlagEntry(t *testing.T) {
	entry := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: aMonday.AddDate(0, 0, 1).Add(9 * time.Hour)} // Tuesday
	nearCollision := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: entry.ProposedPublishAt.Add(1 * time.Hour)}
	sameWeek := store.ScheduleEntry{ID: uuid.New(), ProposedPublishAt: aMonday.AddDate(0, 0, 3).Add(9 * time.Hour)}
	policy := &store.PacingPolicy{TargetUploadsPerWeek: 1, PreferredDays: []string{"Monday"}}

	result := computeScheduleFlags(entry, policy, []store.ScheduleEntry{nearCollision, sameWeek}, nil)
	assert.True(t, result.cadenceExceeded)
	assert.True(t, result.offPreferredDay)
	assert.True(t, result.collision)
	assert.ElementsMatch(t, []string{"cadence_exceeded", "off_preferred_day", "collision"}, flagTypes(result.flags),
		"every true boolean must have exactly one matching ScheduleFlag entry, and vice versa")
}

// ── ChannelScopeID sanity for every input type in this file ────────────

func TestScheduleDraftInputs_ChannelScopeID(t *testing.T) {
	id := uuid.New()

	assert.Equal(t, id, SetPacingPolicyInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, SetPacingPolicyInput{ChannelID: "not-a-uuid"}.ChannelScopeID())

	assert.Equal(t, id, GetPacingPolicyInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, id, GetDraftingContextInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, id, SaveScheduleDraftInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, id, ListScheduleEntriesInput{ChannelID: id.String()}.ChannelScopeID())
}

func TestSetPacingPolicyInput_IdempotencyKey(t *testing.T) {
	in := SetPacingPolicyInput{IdempotencyKeyArg: "abc"}
	assert.Equal(t, "abc", in.IdempotencyKey())
}

func TestSaveScheduleDraftInput_IdempotencyKey(t *testing.T) {
	in := SaveScheduleDraftInput{IdempotencyKeyArg: "xyz"}
	assert.Equal(t, "xyz", in.IdempotencyKey())
}

// weekdayNames' vocabulary must exactly match the set of names
// time.Weekday().String() ever produces -- otherwise a stored
// preferred_days value could never match a real proposed_publish_at's
// weekday in computeScheduleFlags's off_preferred_day check.
func TestWeekdayNames_MatchesTimeWeekdayStringOutputExactly(t *testing.T) {
	for d := time.Sunday; d <= time.Saturday; d++ {
		canon, err := parseWeekdayName(d.String())
		require.NoError(t, err)
		assert.Equal(t, d.String(), canon)
	}
	assert.Len(t, weekdayNames, 7)
}

func TestParseWeekdayName_EmptyStringAfterTrimRejected(t *testing.T) {
	_, err := parseWeekdayName(strings.Repeat(" ", 3))
	assert.Error(t, err)
}

// list_schedule_entries' since/before/limit pagination (issue #1812) moved
// into ScheduleStore.ListByChannel's real SQL (issue #1808/#1812's
// follow-up: filtering belongs against Postgres, not re-implemented over
// an unbounded Go-side fetch) -- see
// TestListScheduleEntries_LimitTruncatedSincePageForward_BeforeNarrows in
// schedule_draft_integration_test.go ("integration" gotag, requires
// Docker) for that coverage against the real embedded schema.
