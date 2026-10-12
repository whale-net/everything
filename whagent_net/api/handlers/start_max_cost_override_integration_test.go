//go:build integration

// Proves max_cost_usd_override handling on StartSession (range check, pinning),
// the effective limits GetSession reports, and UpdateAgent's $10 ceiling.
package handlers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

func TestStartSession_MaxCostUSDOverride(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)
	caller := ctxAs(humanClaims("alice"))

	countSessions := func() int {
		l, _, err := store.Sessions().List(ctx, session.SessionFilter{}, session.SessionPage{PageSize: 100})
		require.NoError(t, err)
		return len(l)
	}

	t.Run("zero, negative and over-ceiling rejected naming field, value and ceiling", func(t *testing.T) {
		for _, v := range []float64{0, -1, 10.01, 50} {
			before := countSessions()
			v := v
			_, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-1", MaxCostUsdOverride: &v})
			require.Error(t, err, "value %g", v)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, codes.InvalidArgument, st.Code())
			assert.Contains(t, st.Message(), "max_cost_usd_override")
			assert.Contains(t, st.Message(), fmt.Sprintf("supplied %g", v))
			assert.Contains(t, st.Message(), "ceiling 10")
			assert.Equal(t, before, countSessions(), "no row written on rejection")
		}
	})

	t.Run("valid override is pinned on the sessions row", func(t *testing.T) {
		v := 2.5
		resp, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-1", MaxCostUsdOverride: &v})
		require.NoError(t, err)
		sess, err := store.Sessions().GetByID(ctx, uuid.MustParse(resp.Session.SessionId))
		require.NoError(t, err)
		require.NotNil(t, sess.MaxCostUSDOverride)
		assert.Equal(t, 2.5, *sess.MaxCostUSDOverride)
	})

	t.Run("exact ceiling of $10 accepted", func(t *testing.T) {
		v := session.MaxCostUSDCeiling
		resp, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-1", MaxCostUsdOverride: &v})
		require.NoError(t, err)
		sess, err := store.Sessions().GetByID(ctx, uuid.MustParse(resp.Session.SessionId))
		require.NoError(t, err)
		require.NotNil(t, sess.MaxCostUSDOverride)
		assert.Equal(t, 10.0, *sess.MaxCostUSDOverride)
	})

	t.Run("absent override leaves the column NULL", func(t *testing.T) {
		resp, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-1"})
		require.NoError(t, err)
		sess, err := store.Sessions().GetByID(ctx, uuid.MustParse(resp.Session.SessionId))
		require.NoError(t, err)
		assert.Nil(t, sess.MaxCostUSDOverride)
	})
}

func TestGetSession_ReportsEffectiveLimits(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)
	caller := ctxAs(humanClaims("alice"))
	def, err := store.AgentDefinitions().GetCurrent(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, def)

	t.Run("no override reports the definition's values", func(t *testing.T) {
		resp, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-1"})
		require.NoError(t, err)
		get, err := srv.GetSession(caller, &pb.GetSessionRequest{SessionId: resp.Session.SessionId})
		require.NoError(t, err)
		assert.Equal(t, int32(session.EffectiveMaxTurns(nil, *def)), get.GetEffectiveMaxTurns())
		assert.Equal(t, session.EffectiveMaxCostUSD(nil, *def), get.GetEffectiveMaxCostUsd())
	})

	t.Run("zero definition values fall back to the platform defaults", func(t *testing.T) {
		require.NoError(t, store.AgentDefinitions().Upsert(ctx, &session.AgentDefinition{
			AgentID:    "agent-zero",
			Model:      strPtr2("test-model"),
			ToolSet:    []session.ToolServerRef{},
			MaxTurns:   0,
			MaxCostUSD: 0,
		}))
		resp, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-zero"})
		require.NoError(t, err)
		get, err := srv.GetSession(caller, &pb.GetSessionRequest{SessionId: resp.Session.SessionId})
		require.NoError(t, err)
		assert.Equal(t, int32(session.DefaultMaxTurns), get.GetEffectiveMaxTurns())
		assert.Equal(t, session.DefaultMaxCostUSD, get.GetEffectiveMaxCostUsd())
	})

	t.Run("pinned overrides are reported as the effective limits", func(t *testing.T) {
		turns, cost := int32(3), 4.25
		resp, err := srv.StartSession(caller, &pb.StartSessionRequest{
			AgentId: "agent-1", MaxTurnsOverride: &turns, MaxCostUsdOverride: &cost,
		})
		require.NoError(t, err)
		get, err := srv.GetSession(caller, &pb.GetSessionRequest{SessionId: resp.Session.SessionId})
		require.NoError(t, err)
		assert.Equal(t, int32(3), get.GetEffectiveMaxTurns())
		assert.Equal(t, 4.25, get.GetEffectiveMaxCostUsd())
	})
}

func TestUpdateAgent_MaxCostUSDCeiling_OnlyWhenValueChanges(t *testing.T) {
	srv, store, _ := newAgentsServer(t, testAdminRole)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "ag", nil)
	admin := ctxAs(humanClaims("u", testAdminRole))

	t.Run("value over the ceiling rejected naming field, value and ceiling", func(t *testing.T) {
		req := validUpdate("ag")
		req.MaxCostUsd = 10.5
		_, err := srv.UpdateAgent(admin, req)
		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.InvalidArgument, st.Code())
		assert.Contains(t, st.Message(), "max_cost_usd")
		assert.Contains(t, st.Message(), "10.5")
		assert.Contains(t, st.Message(), "ceiling 10")
	})

	t.Run("exact ceiling accepted", func(t *testing.T) {
		req := validUpdate("ag")
		req.MaxCostUsd = session.MaxCostUSDCeiling
		_, err := srv.UpdateAgent(admin, req)
		require.NoError(t, err)
	})

	t.Run("unchanged over-ceiling value is not re-checked", func(t *testing.T) {
		// Seed an over-ceiling definition directly, bypassing the handler.
		require.NoError(t, store.AgentDefinitions().Upsert(ctx, &session.AgentDefinition{
			AgentID: "legacy", Model: strPtr2("m"), ToolSet: []session.ToolServerRef{}, MaxTurns: 5, MaxCostUSD: 12,
			MaxToolIterations: 4, ToolLoadingMode: session.ToolLoadingModeBulk,
		}))
		req := validUpdate("legacy")
		req.MaxCostUsd = 12
		req.MaxTurns = 7
		_, err := srv.UpdateAgent(admin, req)
		require.NoError(t, err, "same over-ceiling value must still allow other field edits")
	})
}
