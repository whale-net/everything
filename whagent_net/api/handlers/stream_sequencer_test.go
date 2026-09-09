package handlers

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/events"
)

// ev builds a minimal events.Event carrying only what eventSequencer looks
// at (Seq), with a seq-derived stable EventID so two ev(2) calls in the
// same test -- simulating a re-published duplicate -- produce the
// identical event_id a real duplicate delivery would (session/
// transcript.go's insertEventTx: a given seq always carries the same
// event_id, never reassigned).
func ev(seq int64) events.Event {
	return events.Event{
		EventID: uuid.MustParse("00000000-0000-0000-0000-000000000000"),
		Seq:     seq,
	}
}

func seqsOf(evs []events.Event) []int64 {
	out := make([]int64, len(evs))
	for i, e := range evs {
		out[i] = e.Seq
	}
	return out
}

// TestEventSequencer_InOrderNoDuplicates proves the trivial case: fed
// exactly in order, every Feed call releases its own event immediately.
func TestEventSequencer_InOrderNoDuplicates(t *testing.T) {
	seq := newEventSequencer(1)

	assert.Equal(t, []int64{1}, seqsOf(seq.Feed(ev(1))))
	assert.Equal(t, []int64{2}, seqsOf(seq.Feed(ev(2))))
	assert.Equal(t, []int64{3}, seqsOf(seq.Feed(ev(3))))
}

// TestEventSequencer_DuplicateOfAlreadyReleasedSeqDropped is issue #2239's
// Testing-section case "1,2,2,3": a re-published duplicate of an
// already-released seq must be dropped, and the sequence emitted overall
// must be exactly 1,2,3 -- never 1,2,2,3.
func TestEventSequencer_DuplicateOfAlreadyReleasedSeqDropped(t *testing.T) {
	seq := newEventSequencer(1)

	var got []int64
	got = append(got, seqsOf(seq.Feed(ev(1)))...)
	got = append(got, seqsOf(seq.Feed(ev(2)))...)
	got = append(got, seqsOf(seq.Feed(ev(2)))...) // duplicate re-publish, already released
	got = append(got, seqsOf(seq.Feed(ev(3)))...)

	require.Equal(t, []int64{1, 2, 3}, got, "a duplicate re-publish of an already-released seq must never be emitted again")
}

// TestEventSequencer_DuplicateRepublishNeverEmittedTwice is issue #2239's
// Testing-section "duplicate-republish" case in its sharpest form: a
// duplicate of seq 3 arrives while 3 is still buffered in the reorder
// window (waiting on the gap at seq 2), and must be dropped -- not queued
// alongside the first arrival -- so that closing the gap releases 3
// exactly once, not twice.
//
// Red/green (issue body): commenting out eventSequencer.Feed's dedup-set
// guard (stream_sequencer.go's `if _, dup := m.seen[ev.Seq]; dup` check)
// turns this test red -- it then observes seq 3 released twice, as
// [1, 2, 3, 3], the instant the gap at seq 2 closes. Restoring the guard
// turns it green again.
func TestEventSequencer_DuplicateRepublishNeverEmittedTwice(t *testing.T) {
	seq := newEventSequencer(1)

	var got []int64
	got = append(got, seqsOf(seq.Feed(ev(1)))...)
	got = append(got, seqsOf(seq.Feed(ev(3)))...) // buffered: gap at seq 2
	got = append(got, seqsOf(seq.Feed(ev(3)))...) // duplicate, still buffered
	got = append(got, seqsOf(seq.Feed(ev(2)))...) // closes the gap

	require.Equal(t, []int64{1, 2, 3}, got, "seq 3 must be released exactly once, even though it was fed twice before the gap closed")
}

// TestEventSequencer_OutOfOrderHoldsForReorderWindow is issue #2239's
// Testing-section case "1,3,2": seq 3 arrives before seq 2 and must be
// held (not emitted) until seq 2 closes the gap, at which point both 2 and
// 3 release together, in order.
func TestEventSequencer_OutOfOrderHoldsForReorderWindow(t *testing.T) {
	seq := newEventSequencer(1)

	assert.Equal(t, []int64{1}, seqsOf(seq.Feed(ev(1))))
	assert.Empty(t, seqsOf(seq.Feed(ev(3))), "seq 3 must be held until the gap at seq 2 closes")
	assert.Equal(t, []int64{2, 3}, seqsOf(seq.Feed(ev(2))), "closing the gap at seq 2 must release 2 and the already-buffered 3 together, in order")
}

// TestEventSequencer_StartsAtRequestedFromSeq proves a stream resuming
// mid-session (from_seq > 1, FR5's reconnect case) releases starting at
// exactly that seq, not from 1.
func TestEventSequencer_StartsAtRequestedFromSeq(t *testing.T) {
	seq := newEventSequencer(42)

	assert.Empty(t, seqsOf(seq.Feed(ev(1))), "a seq before the requested from_seq must never be released")
	assert.Equal(t, []int64{42}, seqsOf(seq.Feed(ev(42))))
}

// TestEventSequencer_NeverEmitsSameSeqTwice is a broader property check
// across a longer, heavily-duplicated, shuffled arrival order -- issue
// #2239's Testing-section requirement "never emits a seq twice" applied
// beyond the two named minimal cases above.
func TestEventSequencer_NeverEmitsSameSeqTwice(t *testing.T) {
	seq := newEventSequencer(1)
	arrivals := []int64{1, 1, 3, 2, 2, 5, 4, 4, 3, 5, 6, 6, 6}

	var got []int64
	for _, s := range arrivals {
		got = append(got, seqsOf(seq.Feed(ev(s)))...)
	}

	require.Equal(t, []int64{1, 2, 3, 4, 5, 6}, got)
}
