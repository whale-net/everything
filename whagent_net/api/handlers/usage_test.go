package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

// fakeUsageSessions serves one session from GetByID; other methods panic
// through the nil embedded interface.
type fakeUsageSessions struct {
	session.SessionStore
	sess *session.Session
}

func (f fakeUsageSessions) GetByID(context.Context, uuid.UUID) (*session.Session, error) {
	return f.sess, nil
}

// fakeUsageAgentDefs serves an optional assignment and its definition.
type fakeUsageAgentDefs struct {
	session.AgentDefinitionStore
	assignment *session.SessionAgent
	def        *session.AgentDefinition
}

func (f fakeUsageAgentDefs) CurrentAssignment(context.Context, uuid.UUID) (*session.SessionAgent, error) {
	return f.assignment, nil
}

func (f fakeUsageAgentDefs) GetByID(context.Context, uuid.UUID) (*session.AgentDefinition, error) {
	return f.def, nil
}

// fakeUsageTotals serves a fixed usage summary.
type fakeUsageTotals struct {
	session.UsageStore
	summary session.UsageSummary
}

func (f fakeUsageTotals) Summary(context.Context, uuid.UUID) (session.UsageSummary, error) {
	return f.summary, nil
}

// fakeUsageStore wires the three fakes behind the handlers' sessionStore seam.
type fakeUsageStore struct {
	sessionStore
	sessions fakeUsageSessions
	defs     fakeUsageAgentDefs
	usage    fakeUsageTotals
}

func (f fakeUsageStore) Sessions() session.SessionStore                 { return f.sessions }
func (f fakeUsageStore) AgentDefinitions() session.AgentDefinitionStore { return f.defs }
func (f fakeUsageStore) Usage() session.UsageStore                      { return f.usage }

func newUsageTestServer(sess *session.Session, assignment *session.SessionAgent, def *session.AgentDefinition) *SessionServer {
	return &SessionServer{store: fakeUsageStore{
		sessions: fakeUsageSessions{sess: sess},
		defs:     fakeUsageAgentDefs{assignment: assignment, def: def},
		usage: fakeUsageTotals{summary: session.UsageSummary{
			TurnsUsed: 3, CostUSD: 0.5, CostEstimated: true,
		}},
	}}
}

func usageRequest(id uuid.UUID) *pb.GetSessionUsageRequest {
	return &pb.GetSessionUsageRequest{SessionId: id.String()}
}

// TestGetSessionUsage_PinnedOverridesWin proves a session's pinned turn and
// cost overrides take precedence over the definition's caps.
func TestGetSessionUsage_PinnedOverridesWin(t *testing.T) {
	id := uuid.New()
	defID := uuid.New()
	maxTurns := int32(7)
	maxCost := 0.25
	sess := &session.Session{SessionID: id, MaxTurnsOverride: &maxTurns, MaxCostUSDOverride: &maxCost}
	def := &session.AgentDefinition{ID: defID, AgentID: "agent", MaxTurns: 50, MaxCostUSD: 2.0}
	assignment := &session.SessionAgent{SessionID: id, AgentID: "agent", AgentDefinitionID: defID}
	srv := newUsageTestServer(sess, assignment, def)

	resp, err := srv.GetSessionUsage(context.Background(), usageRequest(id))
	require.NoError(t, err)

	assert.Equal(t, int32(7), resp.Usage.TurnCap, "pinned max_turns_override must win over the definition's 50")
	assert.InDelta(t, 0.25, resp.Usage.CostCapUsd, 1e-9, "pinned max_cost_usd_override must win over the definition's 2.0")
	assert.Equal(t, int32(3), resp.Usage.TurnsUsed)
	assert.InDelta(t, 0.5, resp.Usage.CostUsd, 1e-9)
	assert.True(t, resp.Usage.CostEstimated)
}

// TestGetSessionUsage_NoOverrides_UsesDefinitionCaps proves that with no
// pinned overrides the definition's caps are reported.
func TestGetSessionUsage_NoOverrides_UsesDefinitionCaps(t *testing.T) {
	id := uuid.New()
	defID := uuid.New()
	sess := &session.Session{SessionID: id}
	def := &session.AgentDefinition{ID: defID, AgentID: "agent", MaxTurns: 50, MaxCostUSD: 2.0}
	assignment := &session.SessionAgent{SessionID: id, AgentID: "agent", AgentDefinitionID: defID}
	srv := newUsageTestServer(sess, assignment, def)

	resp, err := srv.GetSessionUsage(context.Background(), usageRequest(id))
	require.NoError(t, err)

	assert.Equal(t, int32(50), resp.Usage.TurnCap)
	assert.InDelta(t, 2.0, resp.Usage.CostCapUsd, 1e-9)
}

// TestGetSessionUsage_NoOverridesNoDefinition_UsesPlatformDefaults proves
// that with no overrides and no pinned definition the platform defaults
// (100 turns / $1.00) are reported.
func TestGetSessionUsage_NoOverridesNoDefinition_UsesPlatformDefaults(t *testing.T) {
	id := uuid.New()
	sess := &session.Session{SessionID: id}
	srv := newUsageTestServer(sess, nil, nil)

	resp, err := srv.GetSessionUsage(context.Background(), usageRequest(id))
	require.NoError(t, err)

	assert.Equal(t, int32(session.DefaultMaxTurns), resp.Usage.TurnCap)
	assert.InDelta(t, session.DefaultMaxCostUSD, resp.Usage.CostCapUsd, 1e-9)
}

// TestGetSessionUsage_NotFound proves an unknown session is NOT_FOUND.
func TestGetSessionUsage_NotFound(t *testing.T) {
	srv := newUsageTestServer(nil, nil, nil)

	_, err := srv.GetSessionUsage(context.Background(), usageRequest(uuid.New()))

	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}
