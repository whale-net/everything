//go:build integration

package session_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTranscriptStore_Append_AssignsStrictlyIncreasingSeqPerSession proves
// Append allocates seq 1, 2, 3, ... in order for repeated appends to the
// same session.
func TestTranscriptStore_Append_AssignsStrictlyIncreasingSeqPerSession(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	sess := createTestSession(t, ctx, s)

	for i := 1; i <= 5; i++ {
		ev, err := s.Transcript().Append(ctx, sess.SessionID, 0, "test.event", json.RawMessage(`{}`))
		require.NoError(t, err)
		assert.Equal(t, int64(i), ev.Seq, "seq must be strictly increasing starting at 1")
		assert.Equal(t, sess.SessionID, ev.SessionID)
		assert.False(t, ev.CommittedAt.IsZero())
	}
}

// TestTranscriptStore_Append_IndependentSequencesAcrossSessions proves two
// sessions' seq counters never interfere with each other.
func TestTranscriptStore_Append_IndependentSequencesAcrossSessions(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	sessA := createTestSession(t, ctx, s)
	sessB := createTestSession(t, ctx, s)

	evA1, err := s.Transcript().Append(ctx, sessA.SessionID, 0, "a.event", json.RawMessage(`{}`))
	require.NoError(t, err)
	evB1, err := s.Transcript().Append(ctx, sessB.SessionID, 0, "b.event", json.RawMessage(`{}`))
	require.NoError(t, err)
	evA2, err := s.Transcript().Append(ctx, sessA.SessionID, 0, "a.event", json.RawMessage(`{}`))
	require.NoError(t, err)

	assert.Equal(t, int64(1), evA1.Seq)
	assert.Equal(t, int64(1), evB1.Seq, "session B's seq must start at 1 independent of session A's progress")
	assert.Equal(t, int64(2), evA2.Seq)
}

// TestTranscriptStore_Append_ConcurrentAppends_NoDuplicateSeq proves
// concurrent Appends to the SAME session never produce a duplicate seq --
// the advisory-lock allocation (backstopped by the UNIQUE (session_id, seq)
// constraint) actually serializes them. Red/green per issue #2109's
// Testing instruction: commenting out transcript.go's
// pg_advisory_xact_lock call turns this test red with exactly the expected
// failure --
// `duplicate key value violates unique constraint "transcript_event_session_id_seq_key"`
// -- confirming the lock (not the UNIQUE constraint alone) is what
// prevents the race under concurrent load; restoring the lock turns it
// green again.
func TestTranscriptStore_Append_ConcurrentAppends_NoDuplicateSeq(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	sess := createTestSession(t, ctx, s)

	const n = 20
	var wg sync.WaitGroup
	seqCh := make(chan int64, n)
	errCh := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ev, err := s.Transcript().Append(ctx, sess.SessionID, 0, "concurrent.event", json.RawMessage(`{}`))
			if err != nil {
				errCh <- err
				return
			}
			seqCh <- ev.Seq
		}()
	}
	wg.Wait()
	close(seqCh)
	close(errCh)

	for err := range errCh {
		require.NoError(t, err, "no concurrent Append should fail")
	}

	seen := make(map[int64]bool, n)
	for seq := range seqCh {
		require.False(t, seen[seq], "duplicate seq %d allocated across concurrent appends", seq)
		seen[seq] = true
	}
	assert.Len(t, seen, n, "every concurrent append must have received a distinct seq")
}

// TestTranscriptStore_Read_OrderedBySeqAndSupportsResumeFromSeq proves Read
// returns events in seq order and that fromSeq is an inclusive resume
// point, not an offset.
func TestTranscriptStore_Read_OrderedBySeqAndSupportsResumeFromSeq(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	sess := createTestSession(t, ctx, s)

	var appended []int64
	for i := 0; i < 5; i++ {
		ev, err := s.Transcript().Append(ctx, sess.SessionID, 0, "test.event", json.RawMessage(`{}`))
		require.NoError(t, err)
		appended = append(appended, ev.Seq)
	}

	all, err := s.Transcript().Read(ctx, sess.SessionID, 1, 100)
	require.NoError(t, err)
	require.Len(t, all, 5)
	for i, ev := range all {
		assert.Equal(t, appended[i], ev.Seq, "events must be returned in seq order")
	}

	resumed, err := s.Transcript().Read(ctx, sess.SessionID, 3, 100)
	require.NoError(t, err)
	require.Len(t, resumed, 3, "fromSeq must be inclusive: seq 3, 4, 5")
	assert.Equal(t, int64(3), resumed[0].Seq)
	assert.Equal(t, int64(5), resumed[2].Seq)

	limited, err := s.Transcript().Read(ctx, sess.SessionID, 1, 2)
	require.NoError(t, err)
	require.Len(t, limited, 2, "limit must be honored")
	assert.Equal(t, int64(1), limited[0].Seq)
	assert.Equal(t, int64(2), limited[1].Seq)
}
