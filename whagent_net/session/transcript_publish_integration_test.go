//go:build integration

// This file exercises NFR2 (publish-after-commit, never before, and a
// publish failure must never roll back or fail the commit) end to end
// against a real Postgres, using a fake events.PublisherInterface in place
// of a real RabbitMQ connection -- see issue #2111's Testing section.
package session_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/events"
)

// fakePublisher is an events.PublisherInterface test double that records
// every published event and lets a test observe (onPublish, invoked
// synchronously before Publish returns) or force (err) publish behavior
// around the store's commit boundary.
type fakePublisher struct {
	mu        sync.Mutex
	published []events.Event
	err       error
	onPublish func(events.Event)
}

var _ events.PublisherInterface = (*fakePublisher)(nil)

func (f *fakePublisher) Publish(ctx context.Context, ev events.Event) error {
	if f.onPublish != nil {
		f.onPublish(ev)
	}
	f.mu.Lock()
	f.published = append(f.published, ev)
	f.mu.Unlock()
	return f.err
}

func (f *fakePublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.published)
}

func (f *fakePublisher) last() events.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.published[len(f.published)-1]
}

// TestTranscriptStore_Append_PublishesCommittedEventExactlyOnce proves a
// single Append call both commits the row and publishes it exactly once,
// with the published record equal to the committed one Append returns.
func TestTranscriptStore_Append_PublishesCommittedEventExactlyOnce(t *testing.T) {
	ctx := context.Background()
	pub := &fakePublisher{}
	s, _ := newStoreWithPublisher(t, pub)
	sess := createTestSession(t, ctx, s)

	ev, err := s.Transcript().Append(ctx, sess.SessionID, 0, "turn.started", json.RawMessage(`{"ok":true}`))
	require.NoError(t, err)

	require.Equal(t, 1, pub.count(), "Append must publish the committed event exactly once")
	published := pub.last()
	assert.Equal(t, ev.EventID, published.EventID)
	assert.Equal(t, ev.SessionID, published.SessionID)
	assert.Equal(t, ev.Seq, published.Seq)
	assert.Equal(t, ev.Turn, published.Turn)
	assert.Equal(t, ev.Type, published.Type)
	assert.JSONEq(t, string(ev.Payload), string(published.Payload))
	assert.True(t, ev.CommittedAt.Equal(published.CommittedAt), "published CommittedAt must match the committed row's")
}

// TestTranscriptStore_Append_PublishOnlyHappensAfterCommit is the
// roll-back-safety test (NFR2, issue #2111): it proves the row is already
// durably visible via an independent query against the store's own pool --
// outside the Append transaction, so it can only see committed data -- at
// the exact moment Publish is invoked.
//
// Red/green per issue #2111's Testing instruction: temporarily moving
// transcript.go's `s.pub.Publish(ctx, ev)` call to before `tx.Commit(ctx)`
// (i.e. publishing from inside the still-open transaction) turned this test
// red, failing with "committed row not visible when Publish was invoked" --
// Postgres' default READ COMMITTED isolation hides another session's
// uncommitted row, so the independent query correctly detects the ordering
// violation. Restoring publish to after Commit turned it green again.
func TestTranscriptStore_Append_PublishOnlyHappensAfterCommit(t *testing.T) {
	ctx := context.Background()
	pub := &fakePublisher{}
	s, db := newStoreWithPublisher(t, pub)
	sess := createTestSession(t, ctx, s)

	var visibleAtPublishTime bool
	pub.onPublish = func(ev events.Event) {
		var count int
		err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM transcript_event WHERE event_id = $1`, ev.EventID).Scan(&count)
		require.NoError(t, err)
		visibleAtPublishTime = count == 1
	}

	_, err := s.Transcript().Append(ctx, sess.SessionID, 0, "turn.started", json.RawMessage(`{}`))
	require.NoError(t, err)

	require.Equal(t, 1, pub.count(), "Publish must have been invoked")
	assert.True(t, visibleAtPublishTime, "committed row not visible when Publish was invoked -- publish must happen strictly after commit")
}

// TestTranscriptStore_Append_PublishFailureDoesNotRollBackOrFailAppend
// proves a publish failure is logged and swallowed (transcript.go), never
// surfaced as an Append error and never used to roll back the
// already-committed row (NFR2).
func TestTranscriptStore_Append_PublishFailureDoesNotRollBackOrFailAppend(t *testing.T) {
	ctx := context.Background()
	pub := &fakePublisher{err: assert.AnError}
	s, _ := newStoreWithPublisher(t, pub)
	sess := createTestSession(t, ctx, s)

	ev, err := s.Transcript().Append(ctx, sess.SessionID, 0, "turn.started", json.RawMessage(`{}`))
	require.NoError(t, err, "a publish failure must never fail Append")

	got, err := s.Transcript().Read(ctx, sess.SessionID, ev.Seq, 1)
	require.NoError(t, err)
	require.Len(t, got, 1, "the committed row must survive a publish failure, not be rolled back")
	assert.Equal(t, ev.EventID, got[0].EventID)
}
