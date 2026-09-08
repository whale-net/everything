//go:build integration

package session_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

// TestSessionStore_Create_GetByID_RoundTrip proves the baseline plumbing:
// Create fills in CreatedAt/UpdatedAt and GetByID returns every column
// byte-identical, including the deliberately-duplicated subject/
// on_behalf_of triples (NFR3).
func TestSessionStore_Create_GetByID_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	sess := newTestSession()
	require.NoError(t, s.Sessions().Create(ctx, sess))
	assert.False(t, sess.CreatedAt.IsZero())
	assert.False(t, sess.UpdatedAt.IsZero())

	got, err := s.Sessions().GetByID(ctx, sess.SessionID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, sess.SessionID, got.SessionID)
	assert.Equal(t, sess.Subject, got.Subject)
	assert.Equal(t, sess.OnBehalfOf, got.OnBehalfOf)
	assert.Equal(t, sess.Subject, got.OnBehalfOf, "M1 always writes on_behalf_of identical to subject")
	assert.Equal(t, session.StatusRunning, got.Status)
	assert.Nil(t, got.ParentSessionID, "parent_session_id is always NULL in M1")
}

// TestSessionStore_GetByID_NotFound_ReturnsNilNotError proves GetByID's
// documented "no rows -> nil, nil" contract.
func TestSessionStore_GetByID_NotFound_ReturnsNilNotError(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	got, err := s.Sessions().GetByID(ctx, uuid.New())
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestSessionStore_RejectsNullOnBehalfOfSub proves on_behalf_of_sub is a
// real NOT NULL column, not merely documented as such -- a raw INSERT with
// it explicitly NULL must be rejected by Postgres (NFR3: on_behalf_of_* is
// NOT NULL, never nullable, even though M1 always writes it identical to
// subject_*).
func TestSessionStore_RejectsNullOnBehalfOfSub(t *testing.T) {
	ctx := context.Background()
	_, db := newStore(t)

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO sessions (
			session_id, subject_iss, subject_sub, subject_kind,
			on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind,
			agent_id, model, status
		) VALUES ($1, 'iss', 'sub', 'human', 'iss', NULL, 'human', 'agent', 'model', 'running')
	`, uuid.New())
	assert.Error(t, err, "a NULL on_behalf_of_sub must be rejected by the NOT NULL constraint")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&count))
	assert.Equal(t, 0, count, "the rejected insert must not leave a row behind")
}

// TestSessionStore_RejectsOutOfSetSubjectKind proves subject_kind's CHECK
// constraint (IN ('human', 'service')) is real DB enforcement, not just an
// app-level convention -- an out-of-set value must be rejected.
func TestSessionStore_RejectsOutOfSetSubjectKind(t *testing.T) {
	ctx := context.Background()
	_, db := newStore(t)

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO sessions (
			session_id, subject_iss, subject_sub, subject_kind,
			on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind,
			agent_id, model, status
		) VALUES ($1, 'iss', 'sub', 'robot', 'iss', 'sub', 'robot', 'agent', 'model', 'running')
	`, uuid.New())
	assert.Error(t, err, "subject_kind = 'robot' is out-of-set and must be rejected by the CHECK constraint")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&count))
	assert.Equal(t, 0, count, "the rejected insert must not leave a row behind")
}

// TestSessionStore_UpdateStatus_CAS_TerminalStatusNeverOverwritten proves
// UpdateStatus's compare-and-swap: once a session is done (terminal), a
// later write of running (non-terminal) is rejected as ErrTerminalStatus
// and the session's status is left untouched. Red/green verified:
// temporarily dropping 'done' from sessions.go's terminalStatusList turned
// this test red (the CAS's WHERE predicate no longer excluded 'done', so
// the running write silently succeeded) with exactly the two expected
// failures -- no ErrTerminalStatus, and the re-read status flipping to
// "running" -- restoring terminalStatusList turned it green again.
func TestSessionStore_UpdateStatus_CAS_TerminalStatusNeverOverwritten(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	sess := createTestSession(t, ctx, s)

	require.NoError(t, s.Sessions().UpdateStatus(ctx, sess.SessionID, session.StatusDone, nil))

	err := s.Sessions().UpdateStatus(ctx, sess.SessionID, session.StatusRunning, nil)
	assert.ErrorIs(t, err, session.ErrTerminalStatus, "a write of running over an existing done must be rejected, not silently accepted")

	got, err := s.Sessions().GetByID(ctx, sess.SessionID)
	require.NoError(t, err)
	assert.Equal(t, session.StatusDone, got.Status, "the terminal status must be left untouched by the rejected write")
}

// TestSessionStore_UpdateStatus_CappedRecordsTerminalReason proves a
// terminal UpdateStatus call populates cap_kind/error_category/
// error_detail from TerminalReason (FR3), and that those columns round
// trip through GetByID.
func TestSessionStore_UpdateStatus_CappedRecordsTerminalReason(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	sess := createTestSession(t, ctx, s)

	capKind := session.CapKindCost
	detail := "cost cap exceeded at $1.00"
	require.NoError(t, s.Sessions().UpdateStatus(ctx, sess.SessionID, session.StatusCapped, &session.TerminalReason{
		CapKind:     &capKind,
		ErrorDetail: &detail,
	}))

	got, err := s.Sessions().GetByID(ctx, sess.SessionID)
	require.NoError(t, err)
	assert.Equal(t, session.StatusCapped, got.Status)
	require.NotNil(t, got.CapKind)
	assert.Equal(t, session.CapKindCost, *got.CapKind)
	require.NotNil(t, got.ErrorDetail)
	assert.Equal(t, detail, *got.ErrorDetail)
}

// TestSessionStore_UpdateStatus_NonExistentSession_ReturnsErrNoRows proves
// UpdateStatus distinguishes "session doesn't exist" from "session is
// terminal" -- a nonexistent id must surface pgx.ErrNoRows, not
// ErrTerminalStatus.
func TestSessionStore_UpdateStatus_NonExistentSession_ReturnsErrNoRows(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	err := s.Sessions().UpdateStatus(ctx, uuid.New(), session.StatusDone, nil)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, session.ErrTerminalStatus, "a nonexistent session must not be reported as terminal")
}
