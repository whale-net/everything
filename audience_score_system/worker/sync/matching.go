package sync

import (
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// MatchConfidenceThreshold is the score (in [0,1], see Match) at or above
// which SyncOutcomes (outcomes.go) auto-links a published video to a
// video_script (video_schedule_match.state = 'auto', FR22, re-anchored onto
// video_script by FR43/#1829) rather than queuing it for human resolution
// (state = 'pending', FR23, never auto-linked below this value -- including
// "no plausible candidate at all", which Match reports as confidence 0).
//
// 0.8 is the starting value: title similarity (titleWeight below) is
// weighted more heavily than publish-date proximity because a video_script's
// target_publish_date is optional (FR36) and, even when set, a Channel's
// pacing policy can legitimately slip a video's actual publish date by days
// without the video being a different upload, whereas two differently-titled
// videos landing on the same day are far more likely to be a real ambiguity
// (e.g. two ideas both slipping into the same week) than a false negative --
// so the combined score only clears 0.8 when the title match is strong (see
// titleSimilarity) AND the dates are close (see dateProximity), which is
// exactly the "confident enough to auto-link without a human in the loop"
// bar FR22 requires. See this file's Testing coverage (matching_test.go,
// issues #1581/#1829) for the boundary cases this value was tuned against,
// including the undated-script cap score's doc comment describes.
//
// This is the ONLY place the threshold value lives -- SyncOutcomes reads
// this constant, never a hardcoded literal, so retuning it is a one-line
// change with no other call site to touch.
const MatchConfidenceThreshold = 0.8

// titleWeight/dateWeight split Match's combined confidence score between
// title similarity and publish-date proximity (must sum to 1). See
// MatchConfidenceThreshold's doc comment for the rationale behind
// weighting title more heavily.
const (
	titleWeight = 0.7
	dateWeight  = 0.3
)

// dateProximityWindow bounds dateProximity: a video published this far (or
// further) from a candidate's TargetPublishDate contributes zero to that
// candidate's date score, however close the title match is -- 14 days
// covers a Channel's pacing policy slipping a video by up to two weeks
// (FR18) while still treating a many-weeks-off match as date-implausible.
const dateProximityWindow = 14 * 24 * time.Hour

// SyncedVideo is the subset of a synced_video row (migration 002) Match
// scores against each candidate VideoScript -- deliberately decoupled from
// store.SyncedVideo (which carries fields, like YouTubeVideoID, this pure
// scoring logic never needs) so this file stays a dependency-free,
// table-testable unit with no store/DB import.
type SyncedVideo struct {
	// Title is the video's YouTube title.
	Title string
	// PublishedAt is when the video actually went live on YouTube.
	PublishedAt time.Time
}

// VideoScript is one `greenlit` video_script candidate Match scores a
// SyncedVideo against -- store.MatchCandidate's shape (video_script_id +
// its own title + target_publish_date, #1829's re-anchor of FR22/FR23 onto
// FR43), renamed/reshaped here purely so this file has no store import;
// outcomes.go converts between the two. TargetPublishDate is nilable (FR36
// makes the target date optional) -- see score's doc comment for how a nil
// value is scored.
type VideoScript struct {
	ID                uuid.UUID
	Title             string
	TargetPublishDate *time.Time
}

// Match scores video against each of candidates (title similarity +
// publish-date proximity, see titleSimilarity/dateProximity) and returns
// the highest-scoring one as best, with its score as confidence in [0,1].
// ok is false only when candidates is empty -- there is no "best" to
// report, and confidence is 0 in that case. A non-empty candidates always
// yields ok == true, even when confidence is far below
// MatchConfidenceThreshold: it is the caller's job (SyncOutcomes) to
// compare confidence against the threshold and decide auto vs. pending,
// never this function's.
func Match(video SyncedVideo, candidates []VideoScript) (best VideoScript, confidence float64, ok bool) {
	if len(candidates) == 0 {
		return VideoScript{}, 0, false
	}

	bestScore := -1.0
	var bestEntry VideoScript
	for _, c := range candidates {
		s := score(video, c)
		if s > bestScore {
			bestScore = s
			bestEntry = c
		}
	}
	return bestEntry, bestScore, true
}

// score combines titleSimilarity and dateProximity per titleWeight/
// dateWeight into a single confidence value in [0,1].
//
// entry.TargetPublishDate == nil scores dateProximity as exactly 0 --
// deliberately NOT renormalized to titleWeight+dateWeight (i.e. never
// score = 1.0*titleSim just because there is no date to compare against).
// This is FR43's load-bearing invariant, not an incidental default: with
// dateProximity pinned at 0, an undated candidate's score is capped at
// titleWeight*titleSim <= 0.7 < MatchConfidenceThreshold (0.8) by
// construction, whatever the title match -- so an undated video_script can
// never auto-link, only ever land in 'pending' for a human to resolve
// (FR44 frames manual resolution as the primary path for an undated
// script). Renormalizing would let a perfect title match alone clear 0.8
// and silently auto-link a script with no target date at all.
func score(video SyncedVideo, entry VideoScript) float64 {
	t := titleSimilarity(video.Title, entry.Title)
	d := 0.0
	if entry.TargetPublishDate != nil {
		d = dateProximity(video.PublishedAt, *entry.TargetPublishDate)
	}
	return titleWeight*t + dateWeight*d
}

// stopWords are excluded from titleSimilarity's word-overlap comparison --
// common enough (in an English creator-title context) that their presence
// or absence says nothing about whether two titles refer to the same
// video, and including them would inflate the similarity of two otherwise
// unrelated titles that happen to share "the"/"a"/"and".
var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "at": true, "for": true,
	"in": true, "is": true, "of": true, "on": true, "the": true,
	"to": true, "with": true,
}

// normalizeTitleWords lowercases s, replaces every rune that is not a
// letter/digit/space with a space (so punctuation never glues two words
// together or splits one apart inconsistently), collapses whitespace, and
// drops stopWords -- the "case, punctuation, stop-words" normalization
// this task's Implementation section calls for.
func normalizeTitleWords(s string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}

	fields := strings.Fields(b.String())
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if !stopWords[f] {
			out = append(out, f)
		}
	}
	return out
}

// titleSimilarity returns the Jaccard index (intersection over union) of
// a's and b's normalized word sets (normalizeTitleWords) -- 1.0 for
// identical normalized word sets (including "same words, different
// case/punctuation" per this task's spec), 0.0 for no shared words, and
// proportionally in between. Two titles that both normalize to nothing
// (e.g. both empty, or both entirely punctuation/stopwords) are treated as
// a perfect match (1.0): there is no word-level information to
// distinguish them, so absence of a title should not be scored as a
// mismatch against another absent title. A title with words compared
// against an empty one scores 0.0.
func titleSimilarity(a, b string) float64 {
	wa := normalizeTitleWords(a)
	wb := normalizeTitleWords(b)
	if len(wa) == 0 && len(wb) == 0 {
		return 1
	}
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}

	setA := make(map[string]bool, len(wa))
	for _, w := range wa {
		setA[w] = true
	}
	setB := make(map[string]bool, len(wb))
	for _, w := range wb {
		setB[w] = true
	}

	intersection := 0
	for w := range setA {
		if setB[w] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// dateProximity returns 1.0 for a and b on the exact same instant, decaying
// linearly to 0.0 at dateProximityWindow's separation (in either
// direction) and beyond.
func dateProximity(a, b time.Time) float64 {
	diff := a.Sub(b)
	if diff < 0 {
		diff = -diff
	}
	if diff >= dateProximityWindow {
		return 0
	}
	return 1 - float64(diff)/float64(dateProximityWindow)
}
