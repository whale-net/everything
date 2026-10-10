//go:build integration

// Proves UpdateAgent: admin gating, full-field supersede, validation,
// NOT_FOUND, and that pinned sessions keep their original definition.
package handlers_test

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

func validUpdate(agentID string) *pb.UpdateAgentRequest {
	return &pb.UpdateAgentRequest{
		AgentId: agentID, Model: "model-v2", ToolSet: `[{"server_url":"https://mcp.example.com/x"}]`,
		MaxTurns: 9, MaxCostUsd: 3.5, MaxToolIterations: 6, ToolLoadingMode: "search",
		Scope: "sc", SystemPrompt: "be nice",
	}
}

func TestUpdateAgent_Admin_UpdatesEveryField_HistoryContiguous(t *testing.T) {
	srv, store, _ := newAgentsServer(t, testAdminRole)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "ag", nil)
	role := "some-role"
	req := validUpdate("ag")
	req.RequiredRole = &role

	resp, err := srv.UpdateAgent(ctxAs(humanClaims("u", testAdminRole)), req)
	require.NoError(t, err)
	assert.Equal(t, "model-v2", resp.GetCurrent().GetModel())

	got, err := srv.GetAgent(ctxAs(humanClaims("u", testAdminRole)), &pb.GetAgentRequest{AgentId: "ag", IncludeHistory: true})
	require.NoError(t, err)
	c := got.GetCurrent()
	assert.Equal(t, "model-v2", c.GetModel())
	assert.Equal(t, int32(9), c.GetMaxTurns())
	assert.Equal(t, 3.5, c.GetMaxCostUsd())
	assert.Equal(t, int32(6), c.GetMaxToolIterations())
	assert.Equal(t, "search", c.GetToolLoadingMode())
	assert.Equal(t, "sc", c.GetScope())
	assert.Equal(t, "be nice", c.GetSystemPrompt())
	assert.Equal(t, "some-role", c.GetRequiredRole())
	assert.Contains(t, c.GetToolSet(), "mcp.example.com/x")
	require.Len(t, got.GetHistory(), 2)
	old, nw := got.History[0], got.History[1]
	require.NotNil(t, old.GetValidTo())
	assert.True(t, old.GetValidTo().AsTime().Equal(nw.GetValidFrom().AsTime()))
}

func TestUpdateAgent_NonAdminAndUnsetRole_PermissionDenied_NoWrite(t *testing.T) {
	for name, tc := range map[string]struct {
		adminRole string
		roles     []string
	}{
		"non-admin":       {testAdminRole, []string{"other"}},
		"admin env unset": {"", []string{testAdminRole, ""}},
	} {
		t.Run(name, func(t *testing.T) {
			srv, store, _ := newAgentsServer(t, tc.adminRole)
			ctx := context.Background()
			seedServiceTestAgent(t, ctx, store, "ag", nil)
			before, err := store.AgentDefinitions().GetCurrent(ctx, "ag")
			require.NoError(t, err)

			_, err = srv.UpdateAgent(ctxAs(humanClaims("u", tc.roles...)), validUpdate("ag"))
			assert.Equal(t, codes.PermissionDenied, status.Code(err))
			after, err := store.AgentDefinitions().GetCurrent(ctx, "ag")
			require.NoError(t, err)
			assert.Equal(t, before.ID, after.ID)
			hist, err := store.AgentDefinitions().History(ctx, "ag")
			require.NoError(t, err)
			assert.Len(t, hist, 1)
		})
	}
}

func TestUpdateAgent_InvalidArguments(t *testing.T) {
	srv, store, _ := newAgentsServer(t, testAdminRole)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "ag", nil)
	missing := uuid.NewString()
	both := uuid.NewString()
	cases := map[string]func(r *pb.UpdateAgentRequest){
		"empty agent_id":  func(r *pb.UpdateAgentRequest) { r.AgentId = "" },
		"neither model":   func(r *pb.UpdateAgentRequest) { r.Model = "" },
		"both models":     func(r *pb.UpdateAgentRequest) { r.ModelDefinitionId = &both },
		"missing mdef":    func(r *pb.UpdateAgentRequest) { r.Model = ""; r.ModelDefinitionId = &missing },
		"bad mdef uuid":   func(r *pb.UpdateAgentRequest) { bad := "nope"; r.Model = ""; r.ModelDefinitionId = &bad },
		"zero max_turns":  func(r *pb.UpdateAgentRequest) { r.MaxTurns = 0 },
		"neg max_cost":    func(r *pb.UpdateAgentRequest) { r.MaxCostUsd = -1 },
		"zero iterations": func(r *pb.UpdateAgentRequest) { r.MaxToolIterations = 0 },
		"bad mode":        func(r *pb.UpdateAgentRequest) { r.ToolLoadingMode = "weird" },
		"blank scope":     func(r *pb.UpdateAgentRequest) { r.Scope = "   " },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := validUpdate("ag")
			mutate(req)
			_, err := srv.UpdateAgent(ctxAs(humanClaims("u", testAdminRole)), req)
			assert.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		})
	}
	hist, err := store.AgentDefinitions().History(ctx, "ag")
	require.NoError(t, err)
	assert.Len(t, hist, 1)
}

func TestUpdateAgent_UnknownAgent_NotFound(t *testing.T) {
	srv, _, _ := newAgentsServer(t, testAdminRole)
	_, err := srv.UpdateAgent(ctxAs(humanClaims("u", testAdminRole)), validUpdate("ghost"))
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestUpdateAgent_PinnedSessionKeepsOriginalDefinition(t *testing.T) {
	srv, store, _ := newAgentsServer(t, testAdminRole)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "ag", nil)
	orig, err := store.AgentDefinitions().GetCurrent(ctx, "ag")
	require.NoError(t, err)
	sess := createSession(t, ctx, store, devSubject, session.StatusRunning)
	require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, orig.ID))

	_, err = srv.UpdateAgent(ctxAs(humanClaims("u", testAdminRole)), validUpdate("ag"))
	require.NoError(t, err)

	a, err := store.AgentDefinitions().CurrentAssignment(ctx, sess.SessionID)
	require.NoError(t, err)
	assert.Equal(t, orig.ID, a.AgentDefinitionID)
	cur, err := store.AgentDefinitions().GetCurrent(ctx, "ag")
	require.NoError(t, err)
	assert.NotEqual(t, orig.ID, cur.ID)
}
