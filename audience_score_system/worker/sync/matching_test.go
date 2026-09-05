package sync

// Pure-Go, table-driven coverage for Match (matching.go, issue #1581,
// FR22/FR23, re-anchored onto video_script by FR43/#1829): Jaccard title
// similarity + linear publish-date-proximity scoring, combined via
// titleWeight/dateWeight into a single confidence in [0,1], including the
// boundary case exactly at MatchConfidenceThreshold (0.8) and which side of
// it a caller (SyncOutcomes, outcomes.go) would land on, PLUS #1829's
// undated-candidate cap (a nil TargetPublishDate must never renormalize the
// 0.7/0.3 weights to title-only). No Postgres or YouTube client needed --
// this file is in package sync (not sync_test) specifically so it can also
// exercise titleSimilarity and dateProximity directly, the two unexported
// building blocks Match composes.

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptrTime(t time.Time) *time.Time { return &t }

// ── Match: no candidates ────────────────────────────────────────────────────

func TestMatch_NoCandidates_ReturnsNotOkZeroConfidence(t *testing.T) {
	video := SyncedVideo{Title: "Anything", PublishedAt: time.Now()}

	best, confidence, ok := Match(video, nil)
	assert.False(t, ok, "no plausible candidate at all must report ok == false, never a fabricated best guess")
	assert.Equal(t, 0.0, confidence)
	assert.Equal(t, VideoScript{}, best)
}

// ── Match: exact title, same day -> high confidence, above threshold ───────

func TestMatch_ExactTitleSameDay_AboveThreshold(t *testing.T) {
	now := time.Now()
	scriptID := uuid.New()
	video := SyncedVideo{Title: "My Great Video Title", PublishedAt: now}
	candidates := []VideoScript{{ID: scriptID, Title: "My Great Video Title", TargetPublishDate: ptrTime(now)}}

	best, confidence, ok := Match(video, candidates)
	require.True(t, ok)
	assert.Equal(t, scriptID, best.ID)
	assert.InDelta(t, 1.0, confidence, 1e-9, "identical title + identical instant must score at (or effectively at) the maximum")
	assert.GreaterOrEqual(t, confidence, MatchConfidenceThreshold, "must land on the auto-link side of the threshold")
}

// ── Match: near-identical title (case/punctuation) + same day -> above threshold ──

func TestMatch_NearIdenticalTitleCasePunctuationDifferences_SameDay_AboveThreshold(t *testing.T) {
	now := time.Now()
	scriptID := uuid.New()
	video := SyncedVideo{Title: "my great video title!!", PublishedAt: now}
	candidates := []VideoScript{{ID: scriptID, Title: "My Great Video Title", TargetPublishDate: ptrTime(now)}}

	best, confidence, ok := Match(video, candidates)
	require.True(t, ok)
	assert.Equal(t, scriptID, best.ID)
	assert.GreaterOrEqual(t, confidence, MatchConfidenceThreshold, "case/punctuation differences alone must not prevent an auto-link")
}

// ── Match: same title, 3 weeks off -> below threshold ───────────────────────

func TestMatch_SameTitleThreeWeeksOff_BelowThreshold(t *testing.T) {
	now := time.Now()
	scriptID := uuid.New()
	video := SyncedVideo{Title: "My Great Video Title", PublishedAt: now}
	candidates := []VideoScript{{ID: scriptID, Title: "My Great Video Title", TargetPublishDate: ptrTime(now.Add(21 * 24 * time.Hour))}}

	_, confidence, ok := Match(video, candidates)
	require.True(t, ok, "a candidate still exists, just a poor one -- ok is only false with zero candidates")
	assert.Less(t, confidence, MatchConfidenceThreshold, "a 3-week publish-date gap must fall outside dateProximityWindow and pull the score below threshold even with a perfect title match")
}

// ── Match: different title, same day -> below threshold ────────────────────

func TestMatch_DifferentTitleSameDay_BelowThreshold(t *testing.T) {
	now := time.Now()
	scriptID := uuid.New()
	video := SyncedVideo{Title: "Completely Unrelated Content", PublishedAt: now}
	candidates := []VideoScript{{ID: scriptID, Title: "My Great Video Title", TargetPublishDate: ptrTime(now)}}

	_, confidence, ok := Match(video, candidates)
	require.True(t, ok)
	assert.Less(t, confidence, MatchConfidenceThreshold, "zero title overlap must fall below threshold even on the exact same day")
}

// ── Match: picks the highest-scoring candidate among several ───────────────

func TestMatch_MultipleCandidates_PicksHighestScoring(t *testing.T) {
	now := time.Now()
	video := SyncedVideo{Title: "My Great Video Title", PublishedAt: now}

	poor := VideoScript{ID: uuid.New(), Title: "Totally Different", TargetPublishDate: ptrTime(now.Add(30 * 24 * time.Hour))}
	best := VideoScript{ID: uuid.New(), Title: "My Great Video Title", TargetPublishDate: ptrTime(now)}
	mediocre := VideoScript{ID: uuid.New(), Title: "My Great Video", TargetPublishDate: ptrTime(now.Add(10 * 24 * time.Hour))}

	got, confidence, ok := Match(video, []VideoScript{poor, mediocre, best})
	require.True(t, ok)
	assert.Equal(t, best.ID, got.ID, "must pick the best-scoring candidate, not merely the first or last in the slice")
	assert.GreaterOrEqual(t, confidence, MatchConfidenceThreshold)
}

// ── Match: boundary case exactly at the threshold ───────────────────────────
//
// titleWeight (0.7) and dateWeight (0.3) are fixed at 0.7/0.3 in
// matching.go. Choosing title similarity t = 5/7 (5 shared words out of a
// 7-word union) and date proximity d = 1.0 (published at the exact instant
// targeted) gives score = 0.7*(5/7) + 0.3*1.0, which is exactly
// float64(0.8) -- the same bit pattern as MatchConfidenceThreshold -- so
// this is a real boundary case, not an approximation.

func exactlyAtThresholdCandidate(publishedAt time.Time) (SyncedVideo, VideoScript) {
	// 5 shared normalized words.
	video := SyncedVideo{Title: "Alpha Bravo Charlie Delta Echo", PublishedAt: publishedAt}
	// Same 5 words plus 2 more the video's title doesn't have -> union 7,
	// intersection 5 -> Jaccard 5/7.
	entry := VideoScript{ID: uuid.New(), Title: "Alpha Bravo Charlie Delta Echo Foxtrot Golf", TargetPublishDate: ptrTime(publishedAt)}
	return video, entry
}

func TestMatch_BoundaryExactlyAtThreshold_ScoresExactlyPointEightAndFallsOnTheAutoSide(t *testing.T) {
	now := time.Now()
	video, entry := exactlyAtThresholdCandidate(now)

	best, confidence, ok := Match(video, []VideoScript{entry})
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
	entry.TargetPublishDate = ptrTime(now.Add(time.Nanosecond))

	_, confidence, ok := Match(video, []VideoScript{entry})
	require.True(t, ok)
	assert.Less(t, confidence, MatchConfidenceThreshold, "even a 1ns publish-date gap must drop the score below the exact-instant boundary case")
	assert.False(t, confidence >= MatchConfidenceThreshold)
}

// ── Match: undated candidates (FR43's no-renormalization invariant) ────────
//
// entry.TargetPublishDate == nil must score dateProximity as exactly 0,
// NEVER renormalized to titleWeight+dateWeight -- see score's doc comment
// (matching.go) for why this is load-bearing, not incidental. These are the
// regression tests for that invariant: they must fail if someone
// renormalizes score to let an undated candidate's title match alone clear
// MatchConfidenceThreshold.

func TestMatch_UndatedCandidate_PerfectTitle_ScoresExactlyTitleWeight_BelowThreshold(t *testing.T) {
	now := time.Now()
	scriptID := uuid.New()
	video := SyncedVideo{Title: "My Great Video Title", PublishedAt: now}
	candidates := []VideoScript{{ID: scriptID, Title: "My Great Video Title", TargetPublishDate: nil}}

	best, confidence, ok := Match(video, candidates)
	require.True(t, ok)
	assert.Equal(t, scriptID, best.ID)
	assert.Equal(t, titleWeight, confidence, "an undated candidate's score must be capped at exactly titleWeight (dateProximity pinned to 0), never renormalized to 1.0*titleSim")
	// Assert against the constant, not the literal 0.8: this is the
	// regression test for the no-renormalization rule (FR43) -- it must
	// fail if titleWeight/dateWeight or MatchConfidenceThreshold are ever
	// retuned such that a perfect-title undated candidate could clear the
	// threshold.
	assert.Less(t, confidence, MatchConfidenceThreshold, "even a perfect title match on an undated candidate must never auto-link -- FR44 frames manual resolution as the primary path for one")
}

func TestMatch_UndatedCandidate_ZeroTitleMatch_ScoresZero(t *testing.T) {
	now := time.Now()
	scriptID := uuid.New()
	video := SyncedVideo{Title: "Alpha Bravo", PublishedAt: now}
	candidates := []VideoScript{{ID: scriptID, Title: "Charlie Delta", TargetPublishDate: nil}}

	_, confidence, ok := Match(video, candidates)
	require.True(t, ok)
	assert.Equal(t, 0.0, confidence, "zero title overlap on an undated candidate must score exactly 0")
}

func TestMatch_DatedCandidate_ExactTargetDatePerfectTitle_ScoresExactlyOne(t *testing.T) {
	now := time.Now()
	scriptID := uuid.New()
	video := SyncedVideo{Title: "My Great Video Title", PublishedAt: now}
	candidates := []VideoScript{{ID: scriptID, Title: "My Great Video Title", TargetPublishDate: ptrTime(now)}}

	best, confidence, ok := Match(video, candidates)
	require.True(t, ok)
	assert.Equal(t, scriptID, best.ID)
	assert.InDelta(t, 1.0, confidence, 1e-9, "perfect title + exact target-date match must score at (or effectively at) the maximum")
}

// ── Match: undated vs. dated candidates compete correctly ──────────────────

// TestMatch_DatedNearMiss_BeatsUndatedPerfectTitle_WhenCombinedScoreIsHigher
// covers the case where a dated candidate's weaker title match, combined
// with an exact date match, still outscores an undated candidate's perfect
// title match (0.7*0.6 + 0.3*1.0 = 0.72 > titleWeight's 0.7 cap).
func TestMatch_DatedNearMiss_BeatsUndatedPerfectTitle_WhenCombinedScoreIsHigher(t *testing.T) {
	now := time.Now()
	video := SyncedVideo{Title: "Alpha Bravo Charlie Delta", PublishedAt: now}

	undatedPerfect := VideoScript{ID: uuid.New(), Title: "Alpha Bravo Charlie Delta", TargetPublishDate: nil}
	// Shared words: alpha, bravo, charlie (3) out of union {alpha, bravo,
	// charlie, delta, echo} (5) -> t = 3/5 = 0.6. Published at the exact
	// target date -> d = 1.0. score = 0.7*0.6 + 0.3*1.0 = 0.72.
	datedNearMiss := VideoScript{ID: uuid.New(), Title: "Alpha Bravo Charlie Echo", TargetPublishDate: ptrTime(now)}

	best, confidence, ok := Match(video, []VideoScript{undatedPerfect, datedNearMiss})
	require.True(t, ok)
	assert.Equal(t, datedNearMiss.ID, best.ID, "a dated near-match's higher combined score must beat an undated candidate's title-only-capped score")
	assert.InDelta(t, 0.72, confidence, 1e-9)
}

// TestMatch_DatedWeakMatch_LosesToUndatedPerfectTitle_WhenCombinedScoreIsLower
// covers the converse: a dated candidate with a weak title match and only a
// middling date proximity must NOT beat an undated candidate's perfect
// title match, because its combined score is lower.
func TestMatch_DatedWeakMatch_LosesToUndatedPerfectTitle_WhenCombinedScoreIsLower(t *testing.T) {
	now := time.Now()
	video := SyncedVideo{Title: "Alpha Bravo Charlie Delta", PublishedAt: now}

	undatedPerfect := VideoScript{ID: uuid.New(), Title: "Alpha Bravo Charlie Delta", TargetPublishDate: nil}
	// No shared words -> t = 0. Half the date-proximity window away -> d =
	// 0.5. score = 0.7*0 + 0.3*0.5 = 0.15, well below undatedPerfect's 0.7.
	datedWeak := VideoScript{ID: uuid.New(), Title: "Zulu Yankee Xray Whiskey", TargetPublishDate: ptrTime(now.Add(dateProximityWindow / 2))}

	best, confidence, ok := Match(video, []VideoScript{undatedPerfect, datedWeak})
	require.True(t, ok)
	assert.Equal(t, undatedPerfect.ID, best.ID, "a dated candidate's lower combined score must not beat an undated candidate's title-only-capped score")
	assert.Equal(t, titleWeight, confidence)
}

// TestMatch_TiedScores_ResolveDeterministically_FirstCandidateWins covers a
// genuine tie between a dated candidate whose target date falls outside
// dateProximityWindow entirely (dateProximity pinned to exactly 0, the
// same value scoring gives a nil TargetPublishDate) and an undated
// candidate with the same title similarity -- both compute
// titleWeight*1.0 via the identical expression in score, so their scores
// are bit-for-bit equal. Match's "s > bestScore" comparison means the
// first candidate encountered in the slice wins ties, never the last --
// this test proves that ordering is deterministic in both directions.
func TestMatch_TiedScores_ResolveDeterministically_FirstCandidateWins(t *testing.T) {
	now := time.Now()
	video := SyncedVideo{Title: "My Great Video Title", PublishedAt: now}

	datedButWayOutsideWindow := VideoScript{ID: uuid.New(), Title: "My Great Video Title", TargetPublishDate: ptrTime(now.Add(dateProximityWindow * 10))}
	undated := VideoScript{ID: uuid.New(), Title: "My Great Video Title", TargetPublishDate: nil}

	best1, confidence1, ok := Match(video, []VideoScript{datedButWayOutsideWindow, undated})
	require.True(t, ok)
	assert.Equal(t, datedButWayOutsideWindow.ID, best1.ID, "on a tie, the first candidate in the slice must win")

	best2, confidence2, ok := Match(video, []VideoScript{undated, datedButWayOutsideWindow})
	require.True(t, ok)
	assert.Equal(t, undated.ID, best2.ID, "reversing the slice order must reverse which tied candidate wins -- proves first-wins, not id-order or some other hidden tiebreak")

	assert.Equal(t, confidence1, confidence2, "both ties must compose to the exact same score")
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
