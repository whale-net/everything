//go:build integration

// See //whagent_net/session:session_integration_test's BUILD comment for
// why this file only builds under the "integration" tag (real Postgres
// via dbtest, requires Docker) and is excluded from `bazel test //...`.
package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
	"github.com/whale-net/everything/whagent_net/session"
)

// newTestStore provisions an isolated, migrated Postgres database (see
// //whagent_net/session:session_integration_test's identical newDB
// helper) and returns a ready *session.Store plus the underlying
// dbtest.Postgres for direct SQL assertions.
func newTestStore(t *testing.T) (*session.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	return session.New(db.Pool, nil), db
}

// newTestSessionRow inserts a minimal valid sessions row, matching
// //whagent_net/session:session_integration_test's newTestSession
// fixture shape.
func newTestSessionRow(t *testing.T, ctx context.Context, s *session.Store) *session.Session {
	t.Helper()
	subject := session.Subject{
		Iss:  "https://issuer.example.com",
		Sub:  "user-" + uuid.NewString(),
		Kind: session.SubjectKindHuman,
	}
	sess := &session.Session{
		SessionID:  uuid.New(),
		Subject:    subject,
		OnBehalfOf: subject,
		AgentID:    "test-agent",
		Model:      "test-model",
		Status:     session.StatusRunning,
	}
	require.NoError(t, s.Sessions().Create(ctx, sess))
	return sess
}

// TestActivities_BuildContext_PersistsEventIDListNotAssembledContext
// proves issue #2114's Testing phase: "Context build persists the exact
// ordered event-ID list into turn_context and never persists the
// assembled context itself." Two parts: (1) turn_context's stored
// event_ids exactly matches BuildContext's returned EventIDs, in the same
// order; (2) turn_context has no column that could hold an assembled
// message body -- a structural guarantee, not just an incidental one,
// checked directly against information_schema so a future migration
// widening the table would fail this test rather than silently
// regressing the contract.
func TestActivities_BuildContext_PersistsEventIDListNotAssembledContext(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	a := &Activities{Store: store}
	result, err := a.BuildContext(ctx, BuildContextInput{
		SessionID: sess.SessionID,
		Turn:      1,
		Input:     "hello from the user",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.EventIDs, "BuildContext must select at least the turn's own new user-input event")

	var storedIDs []uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT event_ids FROM turn_context WHERE session_id = $1 AND turn = $2
	`, sess.SessionID, 1).Scan(&storedIDs))
	assert.Equal(t, result.EventIDs, storedIDs, "turn_context must store the exact ordered event-ID list BuildContext returned")

	var columns []string
	rows, err := db.Pool.Query(ctx, `
		SELECT column_name FROM information_schema.columns WHERE table_name = 'turn_context'
	`)
	require.NoError(t, err)
	for rows.Next() {
		var col string
		require.NoError(t, rows.Scan(&col))
		columns = append(columns, col)
	}
	rows.Close()
	assert.ElementsMatch(t, []string{"session_id", "turn", "event_ids"}, columns,
		"turn_context must have no column capable of holding an assembled context body -- only the event-ID list")
}

// TestActivities_BuildContext_Retried_PersistsSameEventIDList proves
// BuildContext's retry-safety (activities.go's doc comment): a retried
// call for the same (session, turn, input) does not duplicate the
// user-input transcript event and recomputes the identical event-ID
// list.
func TestActivities_BuildContext_Retried_PersistsSameEventIDList(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	a := &Activities{Store: store}
	in := BuildContextInput{SessionID: sess.SessionID, Turn: 1, Input: "hello"}

	first, err := a.BuildContext(ctx, in)
	require.NoError(t, err)
	second, err := a.BuildContext(ctx, in)
	require.NoError(t, err)

	assert.Equal(t, first.EventIDs, second.EventIDs, "a retried BuildContext call must recompute the same event-ID list")

	var eventCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM transcript_event WHERE session_id = $1 AND turn = $2
	`, sess.SessionID, 1).Scan(&eventCount))
	assert.Equal(t, 1, eventCount, "a retried BuildContext call must not duplicate the user-input transcript event")
}

// appendToolUnlockEvent commits one tool_unlock transcript event (issue
// #2668) directly via AppendIfAbsent, the way DispatchTool commits
// tool_call/tool_result and the way a future search_tools dispatch step
// (issue #2671) will commit tool_unlock -- exercised directly here since
// nothing writes this event type yet.
func appendToolUnlockEvent(t *testing.T, ctx context.Context, store *session.Store, sessionID uuid.UUID, turn, callIndex int, names []string, query string) {
	t.Helper()
	payload, err := marshalToolUnlockPayload(names, query)
	require.NoError(t, err)
	_, err = store.Transcript().AppendIfAbsent(ctx, sessionID, turn, toolUnlockEventType(callIndex), payload)
	require.NoError(t, err)
}

// appendFillerEvent commits an ordinary user_message transcript event
// under a fresh turn -- used to pad a session's transcript past
// maxContextEvents without touching any tool_unlock bookkeeping.
func appendFillerEvent(t *testing.T, ctx context.Context, store *session.Store, sessionID uuid.UUID, turn int) {
	t.Helper()
	payload, err := marshalMessagePayload(llm.Message{Role: llm.RoleUser, Content: "filler"})
	require.NoError(t, err)
	_, err = store.Transcript().AppendIfAbsent(ctx, sessionID, turn, events.EventTypeUserMessage, payload)
	require.NoError(t, err)
}

// TestActivities_UnlockedTools_NoUnlockEvents_ReturnsEmptySlice proves
// UnlockedTools returns an empty (not error, not nil-panicking) result for
// a session whose transcript has never committed a tool_unlock event.
func TestActivities_UnlockedTools_NoUnlockEvents_ReturnsEmptySlice(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	a := &Activities{Store: store}
	result, err := a.UnlockedTools(ctx, UnlockedToolsInput{SessionID: sess.SessionID})
	require.NoError(t, err)
	assert.Empty(t, result.ToolNames)
}

// TestActivities_UnlockedTools_PreservesFirstUnlockOrderAcrossCalls proves
// FR6's ordering contract: unlock events committed as [a, b] then [c]
// yield [a, b, c].
func TestActivities_UnlockedTools_PreservesFirstUnlockOrderAcrossCalls(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	appendToolUnlockEvent(t, ctx, store, sess.SessionID, 1, 0, []string{"a", "b"}, "first query")
	appendToolUnlockEvent(t, ctx, store, sess.SessionID, 2, 0, []string{"c"}, "second query")

	a := &Activities{Store: store}
	result, err := a.UnlockedTools(ctx, UnlockedToolsInput{SessionID: sess.SessionID})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, result.ToolNames)
}

// TestActivities_UnlockedTools_DedupesKeepingFirstPosition proves the
// other half of FR6's ordering contract: unlock events committed as
// [a, b] then [b, c] yield [a, b, c] -- b's re-unlock on the second turn
// is a no-op for ordering purposes, it stays at index 1.
func TestActivities_UnlockedTools_DedupesKeepingFirstPosition(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	appendToolUnlockEvent(t, ctx, store, sess.SessionID, 1, 0, []string{"a", "b"}, "first query")
	appendToolUnlockEvent(t, ctx, store, sess.SessionID, 2, 0, []string{"b", "c"}, "second query")

	a := &Activities{Store: store}
	result, err := a.UnlockedTools(ctx, UnlockedToolsInput{SessionID: sess.SessionID})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, result.ToolNames)
	require.Len(t, result.ToolNames, 3)
	assert.Equal(t, "b", result.ToolNames[1], "b must stay at its first-unlock position")
}

// TestActivities_UnlockedTools_Deterministic proves repeated calls over
// the same fixture return an identical slice every time -- guards against
// a map-backed implementation, whose iteration order Go deliberately
// randomizes across process runs (and can vary within one run across
// enough map sizes/inserts).
func TestActivities_UnlockedTools_Deterministic(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	appendToolUnlockEvent(t, ctx, store, sess.SessionID, 1, 0, []string{"a", "b", "c", "d", "e"}, "first query")
	appendToolUnlockEvent(t, ctx, store, sess.SessionID, 2, 0, []string{"e", "d", "c", "b", "a", "f"}, "second query")

	a := &Activities{Store: store}
	first, err := a.UnlockedTools(ctx, UnlockedToolsInput{SessionID: sess.SessionID})
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		again, err := a.UnlockedTools(ctx, UnlockedToolsInput{SessionID: sess.SessionID})
		require.NoError(t, err)
		assert.Equal(t, first.ToolNames, again.ToolNames, "UnlockedTools must return an identical slice on every call over the same fixture")
	}
}

// TestActivities_UnlockedTools_MultipleUnlockEventsWithinOneTurn proves
// two search_tools calls within a single turn -- at different call
// indices, so their tool_unlock events don't collide under
// AppendIfAbsent's (session_id, turn, type) idempotency key -- are both
// picked up.
func TestActivities_UnlockedTools_MultipleUnlockEventsWithinOneTurn(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	appendToolUnlockEvent(t, ctx, store, sess.SessionID, 1, 0, []string{"a"}, "first query")
	appendToolUnlockEvent(t, ctx, store, sess.SessionID, 1, 1, []string{"b"}, "second query")

	a := &Activities{Store: store}
	result, err := a.UnlockedTools(ctx, UnlockedToolsInput{SessionID: sess.SessionID})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, result.ToolNames, "both tool_unlock events within the same turn must be picked up")
}

// TestActivities_UnlockedTools_SurvivesBeyondMaxContextEvents proves FR6's
// "for the life of the session" durability: UnlockedTools reads the WHOLE
// transcript (readWholeTranscript), not the context-budgeted projection
// BuildContext selects, so an unlock stays effective even once its
// tool_unlock event ages out of every turn's maxContextEvents window.
func TestActivities_UnlockedTools_SurvivesBeyondMaxContextEvents(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	appendToolUnlockEvent(t, ctx, store, sess.SessionID, 1, 0, []string{"early_unlocked_tool"}, "first query")
	for turn := 2; turn <= maxContextEvents+10; turn++ {
		appendFillerEvent(t, ctx, store, sess.SessionID, turn)
	}

	a := &Activities{Store: store}
	result, err := a.UnlockedTools(ctx, UnlockedToolsInput{SessionID: sess.SessionID})
	require.NoError(t, err)
	assert.Contains(t, result.ToolNames, "early_unlocked_tool", "an unlock must remain effective even once its tool_unlock event ages out of maxContextEvents")
}

// bigToolDefsForBuildContextTest builds a toolDefs slice whose combined
// toolDefsCharge (budget.go) is large enough to matter, used below to
// prove bulk mode ignores it entirely while search mode is bounded by it.
func bigToolDefsForBuildContextTest() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		{Name: "search_tools", Description: strings.Repeat("d", 2000)},
		{Name: "tool_a", Description: strings.Repeat("d", 2000)},
		{Name: "tool_b", Description: strings.Repeat("d", 2000)},
	}
}

// TestActivities_BuildContext_BulkMode_IgnoresToolsAndMatchesLegacyTruncation
// proves FR10's (issue #2673) "a bulk-mode session's accounting is
// untouched" for the >maxContextEvents truncation case: BuildContext with
// a bulk (zero-valued ToolLoadingMode) definition produces byte-identical
// EventIDs whether or not Tools is set, and still truncates to exactly
// maxContextEvents -- today's flat placeholder, not fitToBudget.
func TestActivities_BuildContext_BulkMode_IgnoresToolsAndMatchesLegacyTruncation(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	for turn := 1; turn <= maxContextEvents+10; turn++ {
		appendFillerEvent(t, ctx, store, sess.SessionID, turn)
	}

	a := &Activities{Store: store}
	turn := maxContextEvents + 11

	withoutTools, err := a.BuildContext(ctx, BuildContextInput{
		SessionID: sess.SessionID,
		Turn:      turn,
		Input:     "hello",
	})
	require.NoError(t, err)

	// A retried call for the SAME (session, turn) is idempotent
	// (BuildContext's doc comment), so calling it again with a large Tools
	// slice attached isolates whether bulk mode's truncation depends on
	// Tools at all -- it must not.
	withTools, err := a.BuildContext(ctx, BuildContextInput{
		SessionID:  sess.SessionID,
		Turn:       turn,
		Input:      "hello",
		Definition: session.AgentDefinition{ToolLoadingMode: session.ToolLoadingModeBulk},
		Tools:      bigToolDefsForBuildContextTest(),
	})
	require.NoError(t, err)

	assert.Equal(t, withoutTools.EventIDs, withTools.EventIDs, "bulk mode must ignore Tools entirely -- byte-identical output with or without it, for the same transcript")
	assert.Len(t, withTools.EventIDs, maxContextEvents, "bulk mode must still truncate to exactly maxContextEvents, unchanged by FR10")
}

// TestActivities_BuildContext_SearchMode_BoundedByBudgetNotMaxContextEvents
// proves the other half of FR10 (issue #2673): a search-mode session's
// context is bounded by searchModeContextBudget (budget.go's fitToBudget),
// not by maxContextEvents. The transcript here has more than
// maxContextEvents small filler events, all of which fit comfortably
// under the char budget -- a case where the two bounds disagree, so this
// test would fail (asserting len <= maxContextEvents) if the old flat
// truncation were still applied to a search-mode session.
func TestActivities_BuildContext_SearchMode_BoundedByBudgetNotMaxContextEvents(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	fillerCount := maxContextEvents + 50
	for turn := 1; turn <= fillerCount; turn++ {
		appendFillerEvent(t, ctx, store, sess.SessionID, turn)
	}

	a := &Activities{Store: store}
	result, err := a.BuildContext(ctx, BuildContextInput{
		SessionID:  sess.SessionID,
		Turn:       fillerCount + 1,
		Input:      "hello",
		Definition: session.AgentDefinition{ToolLoadingMode: session.ToolLoadingModeSearch},
	})
	require.NoError(t, err)

	assert.Greater(t, len(result.EventIDs), maxContextEvents, "a search-mode session must not be truncated to maxContextEvents when the char budget still has room -- it must be bounded by the budget, not the event count")
	assert.Len(t, result.EventIDs, fillerCount+1, "every filler event plus this turn's own new user-input event must fit comfortably under the char budget")
}
