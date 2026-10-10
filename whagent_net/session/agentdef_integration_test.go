//go:build integration

package session_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/whagent_net/session"
)

func strPtr(s string) *string { return &s }

func newTestAgentDefinition(agentID string) *session.AgentDefinition {
	return &session.AgentDefinition{
		AgentID:  agentID,
		Scope:    strPtr("test-scope"),
		Model:    strPtr("test-model"),
		ToolSet:  []session.ToolServerRef{{ServerURL: "https://mcp.example.com/research"}},
		MaxTurns: 100,
	}
}

// supersedeAgentDefinition closes agentID's open row and inserts def as its
// replacement, standing in for the UpdateAgent write path in fixtures.
func supersedeAgentDefinition(t *testing.T, ctx context.Context, s *session.Store, db *dbtest.Postgres, def *session.AgentDefinition) {
	t.Helper()
	_, err := db.Pool.Exec(ctx, `UPDATE agent_definition SET valid_to = NOW() WHERE agent_id = $1 AND valid_to IS NULL`, def.AgentID)
	require.NoError(t, err)
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def))
}

// upsertAgent inserts a fresh definition for agentID and returns it.
func upsertAgent(t *testing.T, ctx context.Context, s *session.Store, agentID string) *session.AgentDefinition {
	t.Helper()
	def := newTestAgentDefinition(agentID)
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def))
	return def
}

// assignNewAgent upserts a fresh definition for agentID and pins sess to it.
func assignNewAgent(t *testing.T, ctx context.Context, s *session.Store, sess *session.Session, agentID string) *session.AgentDefinition {
	t.Helper()
	def := upsertAgent(t, ctx, s, agentID)
	require.NoError(t, s.AgentDefinitions().AssignToSession(ctx, sess.SessionID, def.ID))
	return def
}

// TestAgentDefinitionStore_GetCurrent_GetByID_RoundTrip proves GetCurrent
// returns only the open row, GetByID returns superseded rows too, and a
// second Upsert for an agent with an open row is rejected rather than
// replacing it in place.
func TestAgentDefinitionStore_GetCurrent_GetByID_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	def1 := newTestAgentDefinition("research-agent")
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def1))
	assert.False(t, def1.CreatedAt.IsZero())
	assert.False(t, def1.ValidFrom.IsZero())

	assert.Error(t, s.AgentDefinitions().Upsert(ctx, newTestAgentDefinition("research-agent")),
		"Upsert must not replace an open row in place")

	def2 := newTestAgentDefinition("research-agent")
	def2.Model = strPtr("test-model-v2")
	supersedeAgentDefinition(t, ctx, s, db, def2)
	assert.NotEqual(t, def1.ID, def2.ID)

	current, err := s.AgentDefinitions().GetCurrent(ctx, "research-agent")
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Equal(t, def2.ID, current.ID)
	assert.Nil(t, current.ValidTo)
	require.NotNil(t, current.Model)
	assert.Equal(t, "test-model-v2", *current.Model)

	old, err := s.AgentDefinitions().GetByID(ctx, def1.ID)
	require.NoError(t, err)
	require.NotNil(t, old)
	require.NotNil(t, old.ValidTo, "superseded row must be closed")
	require.NotNil(t, old.Model)
	assert.Equal(t, "test-model", *old.Model)

	missing, err := s.AgentDefinitions().GetByID(ctx, uuid.New())
	require.NoError(t, err)
	assert.Nil(t, missing)
}

// TestAgentDefinitionStore_Scope_RoundTripsThroughGetByID proves an
// AgentDefinition written with a Scope (issue #2424 FR1) round-trips
// through GetByID unchanged, and that CurrentAssignment -> GetByID
// (the exact path worker/activities.go's ResolveAgentDefinition walks to
// find the grant key's input, FR4) resolves a non-empty Scope.
func TestAgentDefinitionStore_Scope_RoundTripsThroughGetByID(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	sess := createTestSession(t, ctx, s)

	def := newTestAgentDefinition("scope-agent")
	def.Scope = strPtr("audience_score_system")
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def))

	got, err := s.AgentDefinitions().GetCurrent(ctx, "scope-agent")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Scope)
	assert.Equal(t, "audience_score_system", *got.Scope)

	require.NoError(t, s.AgentDefinitions().AssignToSession(ctx, sess.SessionID, def.ID))
	assignment, err := s.AgentDefinitions().CurrentAssignment(ctx, sess.SessionID)
	require.NoError(t, err)
	require.NotNil(t, assignment)

	resolved, err := s.AgentDefinitions().GetByID(ctx, assignment.AgentDefinitionID)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.NotNil(t, resolved.Scope, "CurrentAssignment -> GetByID must resolve a non-nil Scope")
	assert.Equal(t, "audience_score_system", *resolved.Scope)
}

// TestAgentDefinitionStore_NilScope_RoundTripsThroughGetByID proves an
// AgentDefinition written with no Scope at all round-trips as nil, not as
// an empty string or an error -- the "no delegated-grant scoping" case
// this field's nullability exists for.
func TestAgentDefinitionStore_NilScope_RoundTripsThroughGetByID(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	def := newTestAgentDefinition("scopeless-agent")
	def.Scope = nil
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def))

	got, err := s.AgentDefinitions().GetCurrent(ctx, "scopeless-agent")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.Scope)
}

// TestAgentDefinitionStore_SystemPrompt_RoundTripsThroughGetByID proves
// an AgentDefinition written with a SystemPrompt (migration 015)
// round-trips through GetByID unchanged, and that leaving it unset
// round-trips as nil rather than an empty string or an error -- the "no
// system prompt configured" case this field's nullability exists for.
func TestAgentDefinitionStore_SystemPrompt_RoundTripsThroughGetByID(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	def := newTestAgentDefinition("system-prompt-agent")
	def.SystemPrompt = strPtr("You are a helpful research assistant.")
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def))

	got, err := s.AgentDefinitions().GetCurrent(ctx, "system-prompt-agent")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.SystemPrompt)
	assert.Equal(t, "You are a helpful research assistant.", *got.SystemPrompt)

	unset := newTestAgentDefinition("system-prompt-unset-agent")
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, unset))

	gotUnset, err := s.AgentDefinitions().GetCurrent(ctx, "system-prompt-unset-agent")
	require.NoError(t, err)
	require.NotNil(t, gotUnset)
	assert.Nil(t, gotUnset.SystemPrompt)
}

// TestAgentDefinitionStore_ListScopes_DistinctSortedExcludingNull proves
// ListScopes returns every distinct non-null Scope, sorted alphabetically,
// deduplicated across agent_id/version, with no null-scope row surfaced.
func TestAgentDefinitionStore_ListScopes_DistinctSortedExcludingNull(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	manman := newTestAgentDefinition("agent-manman")
	manman.Scope = strPtr("manmanv2")
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, manman))

	ass1 := newTestAgentDefinition("agent-ass")
	ass1.Scope = strPtr("audience_score_system")
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, ass1))

	ass2 := newTestAgentDefinition("agent-ass-2")
	ass2.Scope = strPtr("audience_score_system")
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, ass2))

	scopeless := newTestAgentDefinition("agent-scopeless")
	scopeless.Scope = nil
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, scopeless))

	scopes, err := s.AgentDefinitions().ListScopes(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"audience_score_system", "manmanv2"}, scopes)
}

// TestAgentDefinitionStore_GetCurrent_UnknownAgent_ReturnsNilNotError proves
// GetCurrent's documented "no rows -> nil, nil" contract.
func TestAgentDefinitionStore_GetCurrent_UnknownAgent_ReturnsNilNotError(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	got, err := s.AgentDefinitions().GetCurrent(ctx, "does-not-exist")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestAgentDefinitionStore_AssignToSession_TwiceClosesFirstOpensOne proves
// the SCD2 close-and-open pair: assigning twice leaves exactly one row with
// valid_to IS NULL, and the first row's valid_to is set.
func TestAgentDefinitionStore_AssignToSession_TwiceClosesFirstOpensOne(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)

	defA := upsertAgent(t, ctx, s, "agent-a")
	defB := upsertAgent(t, ctx, s, "agent-b")

	require.NoError(t, s.AgentDefinitions().AssignToSession(ctx, sess.SessionID, defA.ID))
	first, err := s.AgentDefinitions().CurrentAssignment(ctx, sess.SessionID)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, "agent-a", first.AgentID)
	assert.Equal(t, defA.ID, first.AgentDefinitionID)
	assert.Nil(t, first.ValidTo)

	require.NoError(t, s.AgentDefinitions().AssignToSession(ctx, sess.SessionID, defB.ID))

	current, err := s.AgentDefinitions().CurrentAssignment(ctx, sess.SessionID)
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Equal(t, "agent-b", current.AgentID)
	assert.Equal(t, defB.ID, current.AgentDefinitionID)
	assert.Nil(t, current.ValidTo)

	// Exactly one open row must exist for the session.
	var openCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM session_agent WHERE session_id = $1 AND valid_to IS NULL
	`, sess.SessionID).Scan(&openCount))
	assert.Equal(t, 1, openCount, "exactly one open session_agent row must survive a second AssignToSession")

	// The first (agent-a) row must now be closed.
	var closedCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM session_agent WHERE session_id = $1 AND agent_id = 'agent-a' AND valid_to IS NOT NULL
	`, sess.SessionID).Scan(&closedCount))
	assert.Equal(t, 1, closedCount, "the first assignment (agent-a) must be closed (valid_to set), not deleted")
}

// TestAgentDefinitionStore_ToolLoadingMode_UnsetRoundTripsAsBulk proves an
// AgentDefinition written with ToolLoadingMode left at its zero value
// round-trips as ToolLoadingModeBulk through both GetCurrent and GetByID
// (FR1: the zero value means bulk, by construction, not by caller
// convention alone).
func TestAgentDefinitionStore_ToolLoadingMode_UnsetRoundTripsAsBulk(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	def := newTestAgentDefinition("bulk-default-agent")
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def))
	assert.Equal(t, session.ToolLoadingModeBulk, def.ToolLoadingMode, "Upsert must normalize the zero value on def itself")

	latest, err := s.AgentDefinitions().GetCurrent(ctx, "bulk-default-agent")
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, session.ToolLoadingModeBulk, latest.ToolLoadingMode)

	version, err := s.AgentDefinitions().GetCurrent(ctx, "bulk-default-agent")
	require.NoError(t, err)
	require.NotNil(t, version)
	assert.Equal(t, session.ToolLoadingModeBulk, version.ToolLoadingMode)
}

// TestAgentDefinitionStore_ToolLoadingMode_SearchRoundTrips proves an
// AgentDefinition written with ToolLoadingModeSearch round-trips as
// "search", not silently coerced to bulk.
func TestAgentDefinitionStore_ToolLoadingMode_SearchRoundTrips(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	def := newTestAgentDefinition("search-agent")
	def.ToolLoadingMode = session.ToolLoadingModeSearch
	require.NoError(t, s.AgentDefinitions().Upsert(ctx, def))

	latest, err := s.AgentDefinitions().GetCurrent(ctx, "search-agent")
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, session.ToolLoadingModeSearch, latest.ToolLoadingMode)

	version, err := s.AgentDefinitions().GetCurrent(ctx, "search-agent")
	require.NoError(t, err)
	require.NotNil(t, version)
	assert.Equal(t, session.ToolLoadingModeSearch, version.ToolLoadingMode)
}

// TestAgentDefinitionStore_ToolLoadingMode_SupersedeTakesEffect proves a
// superseding row's tool_loading_mode is what GetCurrent returns, in both
// directions, while the superseded row keeps its own mode.
func TestAgentDefinitionStore_ToolLoadingMode_SupersedeTakesEffect(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	first := upsertAgent(t, ctx, s, "flip-agent")

	toSearch := newTestAgentDefinition("flip-agent")
	toSearch.ToolLoadingMode = session.ToolLoadingModeSearch
	supersedeAgentDefinition(t, ctx, s, db, toSearch)

	afterSearch, err := s.AgentDefinitions().GetCurrent(ctx, "flip-agent")
	require.NoError(t, err)
	require.NotNil(t, afterSearch)
	assert.Equal(t, session.ToolLoadingModeSearch, afterSearch.ToolLoadingMode, "bulk -> search must take effect")

	backToBulk := newTestAgentDefinition("flip-agent")
	backToBulk.ToolLoadingMode = session.ToolLoadingModeBulk
	supersedeAgentDefinition(t, ctx, s, db, backToBulk)

	afterBulk, err := s.AgentDefinitions().GetCurrent(ctx, "flip-agent")
	require.NoError(t, err)
	require.NotNil(t, afterBulk)
	assert.Equal(t, session.ToolLoadingModeBulk, afterBulk.ToolLoadingMode, "search -> bulk must take effect")

	old, err := s.AgentDefinitions().GetByID(ctx, first.ID)
	require.NoError(t, err)
	require.NotNil(t, old)
	assert.Equal(t, session.ToolLoadingModeBulk, old.ToolLoadingMode)
}

// TestAgentDefinition_ToolLoadingMode_RawInsertOmittingColumn_DefaultsToBulk
// proves migration 013's column default, not just the Go layer's own
// zero-value normalization: a row inserted by raw SQL that never mentions
// tool_loading_mode at all reads back as 'bulk'.
func TestAgentDefinition_ToolLoadingMode_RawInsertOmittingColumn_DefaultsToBulk(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO agent_definition (agent_id, model, tool_set)
		VALUES ('raw-insert-agent', 'test-model', '[]'::jsonb)
	`)
	require.NoError(t, err)

	got, err := s.AgentDefinitions().GetCurrent(ctx, "raw-insert-agent")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, session.ToolLoadingModeBulk, got.ToolLoadingMode)
}

// TestAgentDefinition_ToolLoadingMode_CheckConstraint_RejectsUnknownValue
// proves migration 013's CHECK constraint is real DB enforcement, not just
// an application-layer rule (config.Validate's own check) -- a raw SQL
// insert of an unrecognized value must be rejected outright.
func TestAgentDefinition_ToolLoadingMode_CheckConstraint_RejectsUnknownValue(t *testing.T) {
	ctx := context.Background()
	_, db := newStore(t)

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO agent_definition (agent_id, model, tool_set, tool_loading_mode)
		VALUES ('bad-mode-agent', 'test-model', '[]'::jsonb, 'semantic')
	`)
	require.ErrorContains(t, err, "tool_loading_mode", "an unrecognized tool_loading_mode must be rejected by the CHECK constraint")
}

// TestAgentDefinitionStore_PartialUniqueIndex_RejectsTwoOpenRows proves the
// partial unique index itself is real DB enforcement (NFR6): inserting a
// second open (valid_to IS NULL) session_agent row directly -- bypassing
// AssignToSession's close-and-open transaction entirely -- must be
// rejected.
func TestAgentDefinitionStore_PartialUniqueIndex_RejectsTwoOpenRows(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)

	defA := upsertAgent(t, ctx, s, "agent-a")
	require.NoError(t, s.AgentDefinitions().AssignToSession(ctx, sess.SessionID, defA.ID))

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO session_agent (session_id, agent_id, agent_definition_id) VALUES ($1, 'agent-a', $2)
	`, sess.SessionID, defA.ID)
	assert.Error(t, err, "a second open session_agent row for the same session must be rejected by the partial unique index")

	var openCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM session_agent WHERE session_id = $1 AND valid_to IS NULL
	`, sess.SessionID).Scan(&openCount))
	assert.Equal(t, 1, openCount, "the rejected insert must not leave a second open row behind")
}

func TestAgentDefinitionStore_Register_CreatesOnceAndNeverUpdates(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	first := newTestAgentDefinition("reg-agent")
	created, err := s.AgentDefinitions().Register(ctx, first)
	require.NoError(t, err)
	require.True(t, created)

	changed := newTestAgentDefinition("reg-agent")
	changed.MaxTurns = 5
	created, err = s.AgentDefinitions().Register(ctx, changed)
	require.NoError(t, err)
	assert.False(t, created)

	got, err := s.AgentDefinitions().GetCurrent(ctx, "reg-agent")
	require.NoError(t, err)
	assert.Equal(t, first.ID, got.ID)
	assert.Equal(t, 100, got.MaxTurns)
	assert.True(t, first.ValidFrom.Equal(got.ValidFrom))
}

func TestAgentDefinitionStore_Register_ConcurrentLeavesOneCurrentRow(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	createdCount := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			createdCount[i], errs[i] = s.AgentDefinitions().Register(ctx, newTestAgentDefinition("race-agent"))
		}(i)
	}
	wg.Wait()

	wins := 0
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		if createdCount[i] {
			wins++
		}
	}
	assert.Equal(t, 1, wins)

	var rows int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_definition WHERE agent_id = 'race-agent' AND valid_to IS NULL`).Scan(&rows))
	assert.Equal(t, 1, rows)
}
