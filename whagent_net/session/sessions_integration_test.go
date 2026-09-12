//go:build integration

package session_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
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

// createTestSessionAt is createTestSession plus a raw SQL patch pinning
// created_at to a caller-chosen deterministic value (Create itself always
// writes NOW() -- List's ordering/pagination tests need exact control over
// created_at, including deliberately identical values to exercise the
// session_id tie-breaker). mutate, if non-nil, is applied to the fixture
// before Create.
func createTestSessionAt(t *testing.T, ctx context.Context, s *session.Store, db *dbtest.Postgres, createdAt time.Time, mutate func(*session.Session)) *session.Session {
	t.Helper()
	sess := newTestSession()
	if mutate != nil {
		mutate(sess)
	}
	require.NoError(t, s.Sessions().Create(ctx, sess))
	_, err := db.Pool.Exec(ctx, `UPDATE sessions SET created_at = $1 WHERE session_id = $2`, createdAt, sess.SessionID)
	require.NoError(t, err)
	sess.CreatedAt = createdAt
	return sess
}

// TestSessionStore_List_FiltersByAgentID proves agent_id is applied in SQL
// (FR3/C15): only the matching session is returned.
func TestSessionStore_List_FiltersByAgentID(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	base := time.Now().UTC().Truncate(time.Microsecond)

	want := createTestSessionAt(t, ctx, s, db, base, func(sess *session.Session) { sess.AgentID = "agent-a" })
	createTestSessionAt(t, ctx, s, db, base.Add(-time.Second), func(sess *session.Session) { sess.AgentID = "agent-b" })

	agentID := "agent-a"
	got, _, err := s.Sessions().List(ctx, session.SessionFilter{AgentID: &agentID}, session.SessionPage{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, want.SessionID, got[0].SessionID)
}

// TestSessionStore_List_FiltersByState proves the state filter matches the
// stored status column, not some derived value.
func TestSessionStore_List_FiltersByState(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	base := time.Now().UTC().Truncate(time.Microsecond)

	running := createTestSessionAt(t, ctx, s, db, base, nil)
	done := createTestSessionAt(t, ctx, s, db, base.Add(-time.Second), nil)
	require.NoError(t, s.Sessions().UpdateStatus(ctx, done.SessionID, session.StatusDone, nil))

	state := session.StatusDone
	got, _, err := s.Sessions().List(ctx, session.SessionFilter{State: &state}, session.SessionPage{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, done.SessionID, got[0].SessionID)
	assert.NotEqual(t, running.SessionID, got[0].SessionID)
}

// TestSessionStore_List_FiltersByStartedByKind proves started_by_kind
// filters on subject_kind (LB2/NFR3) -- a service-started session must not
// leak into a human-only (or vice versa) filtered list.
func TestSessionStore_List_FiltersByStartedByKind(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	base := time.Now().UTC().Truncate(time.Microsecond)

	human := createTestSessionAt(t, ctx, s, db, base, func(sess *session.Session) {
		sess.Subject.Kind = session.SubjectKindHuman
		sess.OnBehalfOf.Kind = session.SubjectKindHuman
	})
	service := createTestSessionAt(t, ctx, s, db, base.Add(-time.Second), func(sess *session.Session) {
		sess.Subject.Kind = session.SubjectKindService
		sess.OnBehalfOf.Kind = session.SubjectKindService
	})

	kind := session.SubjectKindService
	got, _, err := s.Sessions().List(ctx, session.SessionFilter{StartedByKind: &kind}, session.SessionPage{})
	require.NoError(t, err)
	require.Len(t, got, 1, "started_by_kind=service must return only the service-subject row")
	assert.Equal(t, service.SessionID, got[0].SessionID)
	assert.NotEqual(t, human.SessionID, got[0].SessionID)
}

// TestSessionStore_List_StartTimeRange_BoundariesAreInclusiveExclusive
// proves StartedAfter is an inclusive lower bound and StartedBefore an
// exclusive upper bound on created_at, exactly as SessionFilter's doc
// comment states.
func TestSessionStore_List_StartTimeRange_BoundariesAreInclusiveExclusive(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	base := time.Now().UTC().Truncate(time.Microsecond)

	before := createTestSessionAt(t, ctx, s, db, base.Add(-time.Hour), nil)
	at := createTestSessionAt(t, ctx, s, db, base, nil)
	after := createTestSessionAt(t, ctx, s, db, base.Add(time.Hour), nil)

	t.Run("started_after is inclusive", func(t *testing.T) {
		got, _, err := s.Sessions().List(ctx, session.SessionFilter{StartedAfter: &base}, session.SessionPage{})
		require.NoError(t, err)
		ids := sessionIDs(got)
		assert.Contains(t, ids, at.SessionID, "the boundary row itself must be included")
		assert.Contains(t, ids, after.SessionID)
		assert.NotContains(t, ids, before.SessionID)
	})

	t.Run("started_before is exclusive", func(t *testing.T) {
		got, _, err := s.Sessions().List(ctx, session.SessionFilter{StartedBefore: &base}, session.SessionPage{})
		require.NoError(t, err)
		ids := sessionIDs(got)
		assert.Contains(t, ids, before.SessionID)
		assert.NotContains(t, ids, at.SessionID, "the boundary row itself must be excluded")
		assert.NotContains(t, ids, after.SessionID)
	})
}

// TestSessionStore_List_CombinesFiltersWithAND proves multiple set filters
// narrow the result jointly, not independently (a row matching only one of
// two set filters must not appear).
func TestSessionStore_List_CombinesFiltersWithAND(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	base := time.Now().UTC().Truncate(time.Microsecond)

	want := createTestSessionAt(t, ctx, s, db, base, func(sess *session.Session) { sess.AgentID = "agent-a" })
	createTestSessionAt(t, ctx, s, db, base.Add(-time.Second), func(sess *session.Session) { sess.AgentID = "agent-a" })
	require.NoError(t, s.Sessions().UpdateStatus(ctx, want.SessionID, session.StatusDone, nil))

	agentID := "agent-a"
	state := session.StatusDone
	got, _, err := s.Sessions().List(ctx, session.SessionFilter{AgentID: &agentID, State: &state}, session.SessionPage{})
	require.NoError(t, err)
	require.Len(t, got, 1, "only the row matching BOTH filters must be returned")
	assert.Equal(t, want.SessionID, got[0].SessionID)
}

// TestSessionStore_List_MultipleSubjectsAllAppear proves List has no
// implicit by-subject scope -- sessions started by distinct subjects all
// appear in an unfiltered call (the caller-side visibility rule, #2237,
// applies in api/handlers, not here).
func TestSessionStore_List_MultipleSubjectsAllAppear(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	base := time.Now().UTC().Truncate(time.Microsecond)

	a := createTestSessionAt(t, ctx, s, db, base, func(sess *session.Session) { sess.Subject.Sub = "subject-a" })
	b := createTestSessionAt(t, ctx, s, db, base.Add(-time.Second), func(sess *session.Session) { sess.Subject.Sub = "subject-b" })
	c := createTestSessionAt(t, ctx, s, db, base.Add(-2*time.Second), func(sess *session.Session) { sess.Subject.Sub = "subject-c" })

	got, _, err := s.Sessions().List(ctx, session.SessionFilter{}, session.SessionPage{})
	require.NoError(t, err)
	ids := sessionIDs(got)
	assert.Contains(t, ids, a.SessionID)
	assert.Contains(t, ids, b.SessionID)
	assert.Contains(t, ids, c.SessionID)
}

// TestSessionStore_List_Pagination_WalksForwardAndBackNoDuplicateNoSkip
// seeds a set larger than one page and proves next/prev pagination covers
// every row exactly once, forward and back, including a run of rows that
// share the exact same created_at -- proving the session_id tie-breaker
// (not created_at alone) determines order there.
func TestSessionStore_List_Pagination_WalksForwardAndBackNoDuplicateNoSkip(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	base := time.Now().UTC().Truncate(time.Microsecond)

	const total = 5
	// The three newest rows share created_at exactly, straddling a page
	// boundary at pageSize=2 below -- with no session_id tie-breaker, a
	// keyset predicate of "created_at < cursor" alone would skip the
	// remaining tied row(s) entirely (ties are excluded by strict "<"),
	// not merely reorder them, which is what actually makes this fixture
	// catch a missing tie-breaker (see the issue's red/green instruction).
	const tiedAtSameInstant = 3
	var seeded []*session.Session
	for i := 0; i < total; i++ {
		createdAt := base
		if i >= tiedAtSameInstant {
			createdAt = base.Add(-time.Duration(i) * time.Second)
		}
		seeded = append(seeded, createTestSessionAt(t, ctx, s, db, createdAt, nil))
	}

	// Canonical order per List's contract: created_at DESC, session_id DESC.
	sortSessionsCanonical(seeded)
	want := sessionIDs(seeded)

	const pageSize = 2

	// Walk forward to the end, collecting every id in order and remembering
	// the exact PageInfo the LAST page returned -- its PrevPageToken is the
	// backward walk's starting cursor below.
	var forward []uuid.UUID
	var lastInfo session.PageInfo
	var nextToken string
	for page := 0; page < total+1; page++ { // +1 guards against an infinite loop if pagination regresses
		got, info, err := s.Sessions().List(ctx, session.SessionFilter{}, session.SessionPage{PageSize: pageSize, PageToken: nextToken})
		require.NoError(t, err)
		if len(got) == 0 {
			break
		}
		forward = append(forward, sessionIDs(got)...)
		lastInfo = info
		if info.NextPageToken == "" {
			break
		}
		nextToken = info.NextPageToken
	}
	require.Equal(t, want, forward, "forward walk must cover every row exactly once, in canonical order, with no gap or duplicate")
	require.Empty(t, lastInfo.NextPageToken, "the forward walk above must have reached the true last page")

	// Walk backward from the last page's own PrevPageToken, back to the
	// start, prepending each earlier page in front of the pages already
	// collected -- starting from the last page's own ids (the tail of
	// `forward`, already known to be correct from the assertion above).
	backward := append([]uuid.UUID{}, forward[len(forward)-pageSizeOf(forward, want, pageSize):]...)
	prevToken := lastInfo.PrevPageToken
	for page := 0; page < total+1; page++ {
		if prevToken == "" {
			break
		}
		got, info, err := s.Sessions().List(ctx, session.SessionFilter{}, session.SessionPage{PageSize: pageSize, PageToken: prevToken})
		require.NoError(t, err)
		require.NotEmpty(t, got, "a non-empty PrevPageToken must always resolve to a non-empty page")
		backward = append(sessionIDs(got), backward...)
		prevToken = info.PrevPageToken
	}
	require.Equal(t, want, backward, "backward walk must reconstruct the exact same canonical order, with no gap or duplicate")
}

// pageSizeOf returns however many rows made up the final page of a forward
// walk over total items with the given pageSize (a short final page when
// total isn't a multiple of pageSize).
func pageSizeOf(forward, want []uuid.UUID, pageSize int) int {
	n := len(want) % pageSize
	if n == 0 {
		n = pageSize
	}
	if n > len(forward) {
		n = len(forward)
	}
	return n
}

// sortSessionsCanonical sorts sessions in place into List's documented
// (created_at DESC, session_id DESC) order.
func sortSessionsCanonical(sessions []*session.Session) {
	for i := 1; i < len(sessions); i++ {
		for j := i; j > 0; j-- {
			a, b := sessions[j-1], sessions[j]
			if a.CreatedAt.Before(b.CreatedAt) || (a.CreatedAt.Equal(b.CreatedAt) && strings.Compare(a.SessionID.String(), b.SessionID.String()) < 0) {
				sessions[j-1], sessions[j] = sessions[j], sessions[j-1]
			}
		}
	}
}

// sessionIDs extracts SessionID from each row, for assert.Contains checks
// that don't care about order.
func sessionIDs(sessions []*session.Session) []uuid.UUID {
	ids := make([]uuid.UUID, len(sessions))
	for i, sess := range sessions {
		ids[i] = sess.SessionID
	}
	return ids
}

// TestSessionStore_List_InvalidPageToken_ReturnsErrInvalidPageToken proves
// a tampered/malformed page_token is reported as ErrInvalidPageToken (which
// the handler maps to codes.InvalidArgument) -- never a panic, never a
// silent full-list fallback.
func TestSessionStore_List_InvalidPageToken_ReturnsErrInvalidPageToken(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	_, _, err := s.Sessions().List(ctx, session.SessionFilter{}, session.SessionPage{PageToken: "not-a-real-token"})
	assert.ErrorIs(t, err, session.ErrInvalidPageToken)
}

// TestSessionStore_List_UsesIndexNotSequentialScan proves migration 002's
// idx_sessions_created_at_id index (not a sequential scan) backs List's
// default ORDER BY created_at DESC, session_id DESC / LIMIT query on a
// seeded table (Validation section).
func TestSessionStore_List_UsesIndexNotSequentialScan(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	base := time.Now().UTC()
	for i := 0; i < 200; i++ {
		createTestSessionAt(t, ctx, s, db, base.Add(-time.Duration(i)*time.Second), nil)
	}

	rows, err := db.Pool.Query(ctx, `EXPLAIN SELECT session_id FROM sessions ORDER BY created_at DESC, session_id DESC LIMIT 10`)
	require.NoError(t, err)
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	require.NoError(t, rows.Err())

	planText := plan.String()
	assert.Contains(t, planText, "idx_sessions_created_at_id", "the list query must use the new index, plan was:\n%s", planText)
	assert.NotContains(t, planText, "Seq Scan on sessions", "the list query must not fall back to a sequential scan, plan was:\n%s", planText)
}
