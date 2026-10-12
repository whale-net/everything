//go:build integration

// Proves StartSession's idle_timeout_override handling: malformed or
// non-positive values are rejected before any row exists, a valid override is
// pinned on the sessions row without touching the agent definition, and
// GetSession reports the effective idle timeout (override, else definition).
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

func TestStartSession_IdleTimeoutOverride(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	defSecs := int32(3600)
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, &session.AgentDefinition{
		AgentID:            "idle-agent",
		Model:              strPtr2("test-model"),
		ToolSet:            []session.ToolServerRef{},
		MaxTurns:           100,
		MaxCostUSD:         1.0,
		IdleTimeoutSeconds: &defSecs,
	}))
	caller := ctxAs(humanClaims("alice"))

	countSessions := func() int {
		l, _, err := store.Sessions().List(ctx, session.SessionFilter{}, session.SessionPage{PageSize: 100})
		require.NoError(t, err)
		return len(l)
	}

	t.Run("malformed, zero and negative rejected as INVALID_ARGUMENT naming the field and value", func(t *testing.T) {
		for _, raw := range []string{"0s", "-1m", "abc"} {
			before := countSessions()
			raw := raw
			_, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "idle-agent", IdleTimeoutOverride: &raw})
			require.Error(t, err, "value %q", raw)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, codes.InvalidArgument, st.Code())
			assert.Contains(t, st.Message(), "idle_timeout_override")
			assert.Contains(t, st.Message(), fmt.Sprintf("%q", raw))
			assert.Equal(t, before, countSessions(), "no row written on rejection")
		}
	})

	t.Run("valid override pinned on the row, definition unchanged, GetSession reports it", func(t *testing.T) {
		raw := "1m"
		resp, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "idle-agent", IdleTimeoutOverride: &raw})
		require.NoError(t, err)
		id := uuid.MustParse(resp.Session.SessionId)

		sess, err := store.Sessions().GetByID(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, sess.IdleTimeoutOverride)
		assert.EqualValues(t, 60, *sess.IdleTimeoutOverride)

		def, err := store.AgentDefinitions().GetCurrent(ctx, "idle-agent")
		require.NoError(t, err)
		require.NotNil(t, def.IdleTimeoutSeconds)
		assert.EqualValues(t, 3600, *def.IdleTimeoutSeconds, "definition must not be mutated by a session override")

		got, err := srv.GetSession(caller, &pb.GetSessionRequest{SessionId: id.String()})
		require.NoError(t, err)
		assert.Equal(t, session.FormatIdleTimeout(60), got.GetEffectiveIdleTimeout())
	})

	t.Run("absent override stores NULL and GetSession falls through to the definition", func(t *testing.T) {
		resp, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "idle-agent"})
		require.NoError(t, err)
		id := uuid.MustParse(resp.Session.SessionId)

		sess, err := store.Sessions().GetByID(ctx, id)
		require.NoError(t, err)
		assert.Nil(t, sess.IdleTimeoutOverride)

		got, err := srv.GetSession(caller, &pb.GetSessionRequest{SessionId: id.String()})
		require.NoError(t, err)
		assert.Equal(t, session.FormatIdleTimeout(3600), got.GetEffectiveIdleTimeout())
	})
}
