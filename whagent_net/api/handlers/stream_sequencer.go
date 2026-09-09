package handlers

import "github.com/whale-net/everything/whagent_net/events"

// eventSequencer is StreamEvents' NFR4/LB1 merge stage (issue #2239): it
// takes events.Event values arriving in whatever order the backfill
// Read/live-tail handoff produces -- including exact re-deliveries of an
// event the publisher re-sent after a retry (events.Publisher's own doc
// comment: "a retry can then re-publish a duplicate, but can never publish
// an event that was never durably committed") -- and releases them to the
// caller strictly in `seq` order, holding a later seq in a small reorder
// window until the gap in front of it closes, and never releasing the same
// seq twice, however many times it was fed.
//
// A committed event's seq is allocated exactly once at commit time
// (session/transcript.go's insertEventTx) and never reassigned, so a given
// seq always carries the same event_id on every delivery -- deduplicating
// on seq therefore gives the identical guarantee NFR4 describes as
// "deduplicating on event_id": the two are the same check for this stream,
// since Seq is what lets it be done without a growing set of every
// event_id ever seen.
//
// Not safe for concurrent use -- StreamEvents drives one eventSequencer
// per stream, from the single goroutine that both backfills and tails it.
type eventSequencer struct {
	// next is the smallest seq not yet released. Starts at the stream's
	// requested from_seq (issue body: "Inclusive... a client reconnecting
	// after a drop passes the last-seen seq + 1 here").
	next int64
	// pending buffers each not-yet-released seq's event, keyed by seq.
	// Deliberately a []events.Event, not a bare events.Event, so `seen`
	// below is load-bearing rather than redundant: without it, two Feed
	// calls for the same seq while it is still waiting on an earlier gap
	// (a re-published duplicate arriving before the gap closes) would
	// both append here and both release -- the same seq emitted twice --
	// the instant the gap in front of it closes.
	pending map[int64][]events.Event
	// seen is the dedup set (NFR4/LB1): every seq Feed has ever accepted
	// into pending, whether already released or still buffered there.
	// Checked -- and only checked -- before a seq is admitted to pending
	// at all, so a duplicate is dropped at the door rather than relying
	// on anything downstream to collapse it.
	seen map[int64]struct{}
}

// newEventSequencer returns an eventSequencer that will next release
// startSeq -- except startSeq < 1 is clamped up to 1: seq is 1-indexed
// (session/transcript.go's insertEventTx: `COALESCE(MAX(seq), 0) + 1`,
// same as ReadTranscriptRequest.from_seq's own "0 (the default) reads
// from the beginning" semantics), so seq 0 never exists and never
// arrives. Left unclamped, a from_seq of 0 (StreamEventsRequest's
// documented default) would wait forever on a gap at seq 0 that can
// never close, holding every real event 1, 2, 3, ... in the reorder
// window and releasing none of them.
func newEventSequencer(startSeq int64) *eventSequencer {
	if startSeq < 1 {
		startSeq = 1
	}
	return &eventSequencer{
		next:    startSeq,
		pending: make(map[int64][]events.Event),
		seen:    make(map[int64]struct{}),
	}
}

// Feed records ev's arrival and returns however many events are now safe
// to emit, in strict seq order (zero, one, or -- when ev closes a gap that
// unblocks a run of already-buffered arrivals -- more than one).
//
// Red/green (issue #2239's Testing section): commenting out the dedup-set
// guard below (the `if _, dup := m.seen[ev.Seq]; dup` check) makes
// TestEventSequencer_DuplicateRepublishNeverEmittedTwice fail, asserting a
// second copy of a re-published event once the reorder window's gap in
// front of it closes; restoring the guard turns it green again.
func (m *eventSequencer) Feed(ev events.Event) []events.Event {
	if _, dup := m.seen[ev.Seq]; dup {
		// A duplicate re-publish of a seq already admitted -- whether
		// already released (ev.Seq < m.next) or still sitting in the
		// reorder window waiting on an earlier gap. Drop it.
		return nil
	}
	m.seen[ev.Seq] = struct{}{}

	if ev.Seq < m.next {
		// Already released before this (first-seen) admission somehow
		// arrived this late -- nothing left to buffer or drain.
		// Unreachable in practice (seen would already hold ev.Seq from
		// the Feed call that released it), kept only so a duplicate that
		// slips past `seen` some other way can never be buffered behind
		// m.next and stall the drain loop below.
		return nil
	}
	m.pending[ev.Seq] = append(m.pending[ev.Seq], ev)

	var ready []events.Event
	for {
		evs, ok := m.pending[m.next]
		if !ok {
			break
		}
		ready = append(ready, evs...)
		delete(m.pending, m.next)
		m.next++
	}
	return ready
}
