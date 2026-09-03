package sync

// Pure-Go, table-driven coverage for Match (matching.go, issue #1581,
// FR22/FR23): Jaccard title similarity + linear publish-date-proximity
// scoring, combined via titleWeight/dateWeight into a single confidence in
// [0,1], including the boundary case exactly at MatchConfidenceThreshold
// (0.8) and which side of it a caller (SyncOutcomes, outcomes.go) would
// land on. No Postgres or YouTube client needed -- this file is in package
// sync (not sync_test) specifically so it can also exercise titleSimilarity
// and dateProximity directly, the two unexported building blocks Match
// composes.

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Match: no candidates ────────────────────────────────────────────────────

func TestMatch_NoCandidates_ReturnsNotOkZeroConfidence(t *testing.T) {
	video := SyncedVideo{Title: "Anything", PublishedAt: time.Now()}

	best, confidence, ok := Match(video, nil)
	assert.False(t, ok, "no plausible candidate at all must report ok == false, never a fabricated best guess")
	assert.Equal(t, 0.0, confidence)
	assert.Equal(t, ScheduleEntry{}, best)
}

// ── Match: exact title, same day -> high confidence, above threshold ───────

func TestMatch_ExactTitleSameDay_AboveThreshold(t *testing.T) {
	now := time.Now()
	entryID := uuid.New()
	video := SyncedVideo{Title: "My Great Video Title", PublishedAt: now}
	candidates := []ScheduleEntry{{ID: entryID, Title: "My Great Video Title", ProposedPublishAt: now}}

	best, confidence, ok := Match(video, candidates)
	require.True(t, ok)
	assert.Equal(t, entryID, best.ID)
	assert.InDelta(t, 1.0, confidence, 1e-9, "identical title + identical instant must score at (or effectively at) the maximum")
	assert.GreaterOrEqual(t, confidence, MatchConfidenceThreshold, "must land on the auto-link side of the threshold")
}

// ── Match: near-identical title (case/punctuation) + same day -> above threshold ──

func TestMatch_NearIdenticalTitleCasePunctuationDifferences_SameDay_AboveThreshold(t *testing.T) {
	now := time.Now()
	entryID := uuid.New()
	video := SyncedVideo{Title: "my great video title!!", PublishedAt: now}
	candidates := []ScheduleEntry{{ID: entryID, Title: "My Great Video Title", ProposedPublishAt: now}}

	best, confidence, ok := Match(video, candidates)
	require.True(t, ok)
	assert.Equal(t, entryID, best.ID)
	assert.GreaterOrEqual(t, confidence, MatchConfidenceThreshold, "case/punctuation differences alone must not prevent an auto-link")
}

// ── Match: same title, 3 weeks off -> below threshold ───────────────────────

func TestMatch_SameTitleThreeWeeksOff_BelowThreshold(t *testing.T) {
	now := time.Now()
	entryID := uuid.New()
	video := SyncedVideo{Title: "My Great Video Title", PublishedAt: now}
	candidates := []ScheduleEntry{{ID: entryID, Title: "My Great Video Title", ProposedPublishAt: now.Add(21 * 24 * time.Hour)}}

	_, confidence, ok := Match(video, candidates)
	require.True(t, ok, "a candidate still exists, just a poor one -- ok is only false with zero candidates")
	assert.Less(t, confidence, MatchConfidenceThreshold, "a 3-week publish-date gap must fall outside dateProximityWindow and pull the score below threshold even with a perfect title match")
}

// ── Match: different title, same day -> below threshold ────────────────────

func TestMatch_DifferentTitleSameDay_BelowThreshold(t *testing.T) {
	now := time.Now()
	entryID := uuid.New()
	video := SyncedVideo{Title: "Completely Unrelated Content", PublishedAt: now}
	candidates := []ScheduleEntry{{ID: entryID, Title: "My Great Video Title", ProposedPublishAt: now}}

	_, confidence, ok := Match(video, candidates)
	require.True(t, ok)
	assert.Less(t, confidence, MatchConfidenceThreshold, "zero title overlap must fall below threshold even on the exact same day")
}

// ── Match: picks the highest-scoring candidate among several ───────────────

func TestMatch_MultipleCandidates_PicksHighestScoring(t *testing.T) {
	now := time.Now()
	video := SyncedVideo{Title: "My Great Video Title", PublishedAt: now}

	poor := ScheduleEntry{ID: uuid.New(), Title: "Totally Different", ProposedPublishAt: now.Add(30 * 24 * time.Hour)}
	best := ScheduleEntry{ID: uuid.New(), Title: "My Great Video Title", ProposedPublishAt: now}
	mediocre := ScheduleEntry{ID: uuid.New(), Title: "My Great Video", ProposedPublishAt: now.Add(10 * 24 * time.Hour)}

	got, confidence, ok := Match(video, []ScheduleEntry{poor, mediocre, best})
	require.True(t, ok)
	assert.Equal(t, best.ID, got.ID, "must pick the best-scoring candidate, not merely the first or last in the slice")
	assert.GreaterOrEqual(t, confidence, MatchConfidenceThreshold)
}

// ── Match: boundary case exactly at the threshold ───────────────────────────
//
// titleWeight (0.7) and dateWeight (0.3) are fixed at 0.7/0.3 in
// matching.go. Choosing title similarity t = 5/7 (5 shared words out of a
// 7-word union) and date proximity d = 1.0 (published at the exact instant
// proposed) gives score = 0.7*(5/7) + 0.3*1.0, which is exactly
// float64(0.8) -- the same bit pattern as MatchConfidenceThreshold -- so
// this is a real boundary case, not an approximation.

func exactlyAtThresholdCandidate(publishedAt time.Time) (SyncedVideo, ScheduleEntry) {
	// 5 shared normalized words.
	video := SyncedVideo{Title: "Alpha Bravo Charlie Delta Echo", PublishedAt: publishedAt}
	// Same 5 words plus 2 more the video's title doesn't have -> union 7,
	// intersection 5 -> Jaccard 5/7.
	entry := ScheduleEntry{ID: uuid.New(), Title: "Alpha Bravo Charlie Delta Echo Foxtrot Golf", ProposedPublishAt: publishedAt}
	return video, entry
}

func TestMatch_BoundaryExactlyAtThreshold_ScoresExactlyPointEightAndFallsOnTheAutoSide(t *testing.T) {
	now := time.Now()
	video, entry := exactlyAtThresholdCandidate(now)

	best, confidence, ok := Match(video, []ScheduleEntry{entry})
	require.True(t, ok)
	assert.Equal(t, entry.ID, best.ID)
	require.Equal(t, 0.8, confidence, "5/7 title similarity + exact-instant date proximity must compose to exactly float64(0.8)")

	// This is the exact value SyncOutcomes compares with >= (outcomes.go) --
	// prove it lands on the auto side, not pending.
	assert.True(t, confidence >= MatchConfidenceThreshold, "a confidence exactly equal to the threshold must auto-link (>=, not >)")
}

// TestMatch_JustBelowThreshold_FallsOnThePendingSide perturbs the boundary
// case's date by a hair (any nonzero gap makes the date-proximity term
// strictly less than 1.0), pushing the composed score strictly below 0.8 --
// the "pending" side of the exact same boundary the test above lands
// on the "auto" side of.
func TestMatch_JustBelowThreshold_FallsOnThePendingSide(t *testing.T) {
	now := time.Now()
	video, entry := exactlyAtThresholdCandidate(now)
	entry.ProposedPublishAt = now.Add(time.Nanosecond)

	_, confidence, ok := Match(video, []ScheduleEntry{entry})
	require.True(t, ok)
	assert.Less(t, confidence, MatchConfidenceThreshold, "even a 1ns publish-date gap must drop the score below the exact-instant boundary case")
	assert.False(t, confidence >= MatchConfidenceThreshold)
}

// ── titleSimilarity: Jaccard index on normalized word sets ─────────────────

func TestTitleSimilarity_JaccardCases(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want float64
	}{
		{"identical", "Hello World", "Hello World", 1.0},
		{"case_and_punctuation_ignored", "Hello, World!!", "hello world", 1.0},
		{"stopwords_ignored", "The Cat and the Hat", "Cat Hat", 1.0},
		{"no_overlap", "Alpha Bravo", "Charlie Delta", 0.0},
		{"partial_overlap_one_third", "Alpha Bravo", "Alpha Charlie", 1.0 / 3.0},
		{"both_empty_after_normalization", "the a an", "and of in", 1.0},
		{"one_side_empty_after_normalization", "the a an", "Alpha Bravo", 0.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := titleSimilarity(tc.a, tc.b)
			assert.InDelta(t, tc.want, got, 1e-9)
			// titleSimilarity must be symmetric.
			assert.InDelta(t, tc.want, titleSimilarity(tc.b, tc.a), 1e-9)
		})
	}
}

// ── dateProximity: linear decay to 0 at dateProximityWindow, symmetric ─────

func TestDateProximity_LinearDecayAndWindowBounds(t *testing.T) {
	base := time.Now()

	assert.Equal(t, 1.0, dateProximity(base, base), "same instant must score 1.0 exactly")

	half := dateProximity(base, base.Add(dateProximityWindow/2))
	assert.InDelta(t, 0.5, half, 1e-9, "half the window must score 0.5")

	assert.Equal(t, 0.0, dateProximity(base, base.Add(dateProximityWindow)), "exactly at the window boundary must score 0")
	assert.Equal(t, 0.0, dateProximity(base, base.Add(2*dateProximityWindow)), "well beyond the window must also score 0, not negative")

	// Symmetric regardless of which side of "before/after" the gap falls on.
	assert.Equal(t, dateProximity(base, base.Add(time.Hour)), dateProximity(base.Add(time.Hour), base))
}
