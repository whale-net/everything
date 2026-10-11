//go:build integration

// Proves StartSession's max_turns_override handling: values below 1 are
// rejected before any row exists, valid values are pinned on the sessions
// row. Cost overrides are covered by start_max_cost_override_integration_test.go.
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

func TestStartSession_MaxTurnsOverride(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)
	caller := ctxAs(humanClaims("alice"))

	countSessions := func() int {
		l, _, err := store.Sessions().List(ctx, session.SessionFilter{}, session.SessionPage{PageSize: 100})
		require.NoError(t, err)
		return len(l)
	}

	t.Run("zero and negative rejected as INVALID_ARGUMENT naming the field and value", func(t *testing.T) {
		for _, v := range []int32{0, -3} {
			before := countSessions()
			v := v
			_, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-1", MaxTurnsOverride: &v})
			require.Error(t, err, "value %d", v)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, codes.InvalidArgument, st.Code())
			assert.Contains(t, st.Message(), "max_turns_override")
			assert.Contains(t, st.Message(), fmt.Sprintf("supplied %d", v))
			assert.Equal(t, before, countSessions(), "no row written on rejection")
		}
	})

	t.Run("valid override is pinned on the sessions row", func(t *testing.T) {
		v := int32(7)
		resp, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-1", MaxTurnsOverride: &v})
		require.NoError(t, err)
		sess, err := store.Sessions().GetByID(ctx, uuid.MustParse(resp.Session.SessionId))
		require.NoError(t, err)
		require.NotNil(t, sess.MaxTurnsOverride)
		assert.EqualValues(t, 7, *sess.MaxTurnsOverride)
	})

	t.Run("value of 1 accepted (single-turn)", func(t *testing.T) {
		v := int32(1)
		resp, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-1", MaxTurnsOverride: &v})
		require.NoError(t, err)
		sess, err := store.Sessions().GetByID(ctx, uuid.MustParse(resp.Session.SessionId))
		require.NoError(t, err)
		require.NotNil(t, sess.MaxTurnsOverride)
		assert.EqualValues(t, 1, *sess.MaxTurnsOverride)
	})

	t.Run("absent override leaves the column NULL", func(t *testing.T) {
		resp, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-1"})
		require.NoError(t, err)
		sess, err := store.Sessions().GetByID(ctx, uuid.MustParse(resp.Session.SessionId))
		require.NoError(t, err)
		assert.Nil(t, sess.MaxTurnsOverride)
	})
}
