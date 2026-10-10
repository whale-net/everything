//go:build integration

// Proves ListAgents/GetAgent: current-only rows, NOT_FOUND, admin-only
// history in valid_from order, and the fail-closed API admin role.
package handlers_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/whagent_net/api/handlers"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

const testAdminRole = "whagent-api-admin"

func newAgentsServer(t *testing.T, adminRole string) (*handlers.SessionServer, *session.Store, *dbtest.Postgres) {
	t.Helper()
	srv, store, db := newServiceAccountTestServerWithDB(t)
	srv.SetAgentAdminRole(adminRole)
	return srv, store, db
}

// supersedeAgent closes agentID's open row and opens a new one with model.
func supersedeAgent(t *testing.T, ctx context.Context, store *session.Store, db *dbtest.Postgres, agentID, model string) {
	t.Helper()
	_, err := db.Pool.Exec(ctx, `UPDATE agent_definition SET valid_to = NOW() WHERE agent_id = $1 AND valid_to IS NULL`, agentID)
	require.NoError(t, err)
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, &session.AgentDefinition{
		AgentID: agentID, Model: strPtr2(model), MaxTurns: 7, MaxCostUSD: 2.5,
	}))
}

func TestListAgents_CurrentOnlyWithCaps_NonAdmin(t *testing.T) {
	srv, store, db := newAgentsServer(t, testAdminRole)
	ctx := context.Background()
	for _, id := range []string{"b-agent", "a-agent"} {
		require.NoError(t, store.AgentDefinitions().Upsert(ctx, &session.AgentDefinition{
			AgentID: id, Model: strPtr2("m1"), MaxTurns: 7, MaxCostUSD: 2.5, MaxToolIterations: 4,
		}))
	}
	supersedeAgent(t, ctx, store, db, "a-agent", "m2")

	resp, err := srv.ListAgents(ctxAs(humanClaims("u")), &pb.ListAgentsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetAgents(), 2)
	assert.Equal(t, "a-agent", resp.Agents[0].GetAgentId())
	assert.Equal(t, "b-agent", resp.Agents[1].GetAgentId())
	assert.Equal(t, "m2", resp.Agents[0].GetModel())
	assert.Nil(t, resp.Agents[0].GetValidTo())
	assert.Equal(t, int32(7), resp.Agents[0].GetMaxTurns())
	assert.Equal(t, 2.5, resp.Agents[0].GetMaxCostUsd())
	assert.Equal(t, int32(4), resp.Agents[1].GetMaxToolIterations())
}

func TestGetAgent_CurrentNonAdmin_AndNotFound(t *testing.T) {
	srv, store, _ := newAgentsServer(t, testAdminRole)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-x", nil)

	resp, err := srv.GetAgent(ctxAs(humanClaims("u")), &pb.GetAgentRequest{AgentId: "agent-x"})
	require.NoError(t, err)
	assert.Equal(t, "agent-x", resp.GetCurrent().GetAgentId())
	assert.Empty(t, resp.GetHistory())

	_, err = srv.GetAgent(ctxAs(humanClaims("u")), &pb.GetAgentRequest{AgentId: "nope"})
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetAgent_HistoryAdmin_OrderedByValidFrom(t *testing.T) {
	srv, store, db := newAgentsServer(t, testAdminRole)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-h", nil)
	supersedeAgent(t, ctx, store, db, "agent-h", "m2")
	supersedeAgent(t, ctx, store, db, "agent-h", "m3")

	resp, err := srv.GetAgent(ctxAs(humanClaims("admin", testAdminRole)), &pb.GetAgentRequest{AgentId: "agent-h", IncludeHistory: true})
	require.NoError(t, err)
	require.Len(t, resp.GetHistory(), 3)
	assert.Equal(t, "m3", resp.GetCurrent().GetModel())
	assert.Equal(t, "test-model", resp.History[0].GetModel())
	assert.Equal(t, "m2", resp.History[1].GetModel())
	assert.Equal(t, "m3", resp.History[2].GetModel())
	assert.Nil(t, resp.History[2].GetValidTo())
	assert.NotNil(t, resp.History[0].GetValidTo())
}

func TestGetAgent_HistoryNonAdmin_PermissionDenied(t *testing.T) {
	srv, store, _ := newAgentsServer(t, testAdminRole)
	seedServiceTestAgent(t, context.Background(), store, "agent-h", nil)

	resp, err := srv.GetAgent(ctxAs(humanClaims("u", "other-role")), &pb.GetAgentRequest{AgentId: "agent-h", IncludeHistory: true})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Nil(t, resp)
}

func TestGetAgent_HistoryAdminRoleUnset_FailsClosed(t *testing.T) {
	srv, store, _ := newAgentsServer(t, "")
	seedServiceTestAgent(t, context.Background(), store, "agent-h", nil)

	for _, roles := range [][]string{{testAdminRole}, {"admin"}, {""}, nil} {
		resp, err := srv.GetAgent(ctxAs(humanClaims("u", roles...)), &pb.GetAgentRequest{AgentId: "agent-h", IncludeHistory: true})
		assert.Equal(t, codes.PermissionDenied, status.Code(err), "roles=%v", roles)
		assert.Nil(t, resp)
	}
}

func TestAgents_Unauthenticated(t *testing.T) {
	srv, _, _ := newAgentsServer(t, testAdminRole)
	ctx := context.Background()

	_, err := handlers.RequireClaimsUnaryInterceptor(ctx, &pb.ListAgentsRequest{}, &grpc.UnaryServerInfo{}, func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.ListAgents(ctx, req.(*pb.ListAgentsRequest))
	})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	_, err = handlers.RequireClaimsUnaryInterceptor(ctx, &pb.GetAgentRequest{}, &grpc.UnaryServerInfo{}, func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.GetAgent(ctx, req.(*pb.GetAgentRequest))
	})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}
