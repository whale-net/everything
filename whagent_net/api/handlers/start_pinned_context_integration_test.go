//go:build integration

// Proves StartSession's pinned_context handling: byte-limit and UTF-8
// validation before any row exists, presence/bytes reporting on every
// response, and that the text is never put in the transcript.
package handlers_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

func TestStartSession_PinnedContext(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)
	caller := ctxAs(humanClaims("alice"))

	start := func(pc string, mutate func(*pb.StartSessionRequest)) (*pb.StartSessionResponse, error) {
		req := &pb.StartSessionRequest{AgentId: "agent-1", PinnedContext: &pc}
		if mutate != nil {
			mutate(req)
		}
		return srv.StartSession(caller, req)
	}
	count := func() int {
		l, _, err := store.Sessions().List(ctx, session.SessionFilter{}, session.SessionPage{PageSize: 100})
		require.NoError(t, err)
		return len(l)
	}

	t.Run("accepted and reported on Start, Get, List, Send, Stop", func(t *testing.T) {
		pc := "be terse ☃" // 12 bytes
		resp, err := start(pc, nil)
		require.NoError(t, err)
		assert.True(t, resp.Session.PinnedContextPresent)
		assert.EqualValues(t, len(pc), resp.Session.PinnedContextBytes)
		assert.NotEqual(t, len([]rune(pc)), len(pc))

		get, err := srv.GetSession(caller, &pb.GetSessionRequest{SessionId: resp.Session.SessionId})
		require.NoError(t, err)
		assert.True(t, get.Session.PinnedContextPresent)
		assert.EqualValues(t, len(pc), get.Session.PinnedContextBytes)

		list, err := srv.ListSessions(caller, &pb.ListSessionsRequest{})
		require.NoError(t, err)
		found := false
		for _, s := range list.Sessions {
			if s.SessionId == resp.Session.SessionId {
				found = true
				assert.True(t, s.PinnedContextPresent)
				assert.EqualValues(t, len(pc), s.PinnedContextBytes)
			}
		}
		assert.True(t, found)

		_, err = srv.SendTurn(caller, &pb.SendTurnRequest{SessionId: resp.Session.SessionId, Input: "hi"})
		require.NoError(t, err)
		stop, err := srv.StopSession(caller, &pb.StopSessionRequest{SessionId: resp.Session.SessionId})
		require.NoError(t, err)
		assert.True(t, stop.Session.PinnedContextPresent)
		assert.EqualValues(t, len(pc), stop.Session.PinnedContextBytes)

		evs, err := store.Transcript().Read(ctx, uuid.MustParse(resp.Session.SessionId), 0, 100)
		require.NoError(t, err)
		for _, e := range evs {
			assert.NotContains(t, string(e.Payload), "be terse")
		}
	})

	t.Run("exactly 32000 bytes accepted", func(t *testing.T) {
		resp, err := start(strings.Repeat("a", 32000), nil)
		require.NoError(t, err)
		assert.EqualValues(t, 32000, resp.Session.PinnedContextBytes)
	})

	t.Run("32001 bytes rejected with limit and size, no row", func(t *testing.T) {
		before := count()
		_, err := start(strings.Repeat("a", 32001), nil)
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
		assert.Contains(t, err.Error(), "32000")
		assert.Contains(t, err.Error(), "32001")
		assert.Equal(t, before, count())
	})

	t.Run("limit counts bytes not characters", func(t *testing.T) {
		// 11000 three-byte runes = 33000 bytes but only 11000 characters.
		_, err := start(strings.Repeat("☃", 11000), nil)
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
		assert.Contains(t, err.Error(), "33000")
	})

	t.Run("invalid UTF-8 rejected, no row", func(t *testing.T) {
		before := count()
		_, err := start("bad\xff\xfe", nil)
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
		assert.Contains(t, err.Error(), "not valid UTF-8")
		assert.Equal(t, before, count())
	})

	t.Run("empty equals absent", func(t *testing.T) {
		resp, err := start("", nil)
		require.NoError(t, err)
		assert.False(t, resp.Session.PinnedContextPresent)
		assert.EqualValues(t, 0, resp.Session.PinnedContextBytes)
		txt, err := store.Sessions().GetPinnedContext(ctx, uuid.MustParse(resp.Session.SessionId))
		require.NoError(t, err)
		assert.Nil(t, txt)
	})

	t.Run("combines with on_behalf_of", func(t *testing.T) {
		resp, err := srv.StartSession(delegatingClaims("fcm-service", fcmClientID), &pb.StartSessionRequest{
			AgentId:       "agent-1",
			OnBehalfOf:    subjectToTestProto(assertedUser),
			PinnedContext: strPtr2("ctx"),
		})
		require.NoError(t, err)
		assert.True(t, resp.Session.PinnedContextPresent)
		assert.Equal(t, assertedUser.Sub, resp.Session.OnBehalfOf.Sub)
	})
}

func TestGetSession_IncludePinnedContext(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)
	caller := ctxAs(humanClaims("alice"))

	// Multi-byte UTF-8, leading/trailing whitespace and newlines.
	pc := "  héllo ☃ 日本\n\n\tline two \r\n  "
	started, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-1", PinnedContext: &pc})
	require.NoError(t, err)
	id := started.Session.SessionId

	t.Run("flag off returns no text but reports presence and bytes", func(t *testing.T) {
		resp, err := srv.GetSession(caller, &pb.GetSessionRequest{SessionId: id})
		require.NoError(t, err)
		assert.Nil(t, resp.PinnedContext)
		assert.True(t, resp.Session.PinnedContextPresent)
		assert.EqualValues(t, len(pc), resp.Session.PinnedContextBytes)
	})

	t.Run("flag on round-trips exact bytes", func(t *testing.T) {
		resp, err := srv.GetSession(caller, &pb.GetSessionRequest{SessionId: id, IncludePinnedContext: true})
		require.NoError(t, err)
		require.NotNil(t, resp.PinnedContext)
		assert.Equal(t, pc, resp.GetPinnedContext())
	})

	t.Run("flag on without pinned context returns none", func(t *testing.T) {
		s, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-1"})
		require.NoError(t, err)
		resp, err := srv.GetSession(caller, &pb.GetSessionRequest{SessionId: s.Session.SessionId, IncludePinnedContext: true})
		require.NoError(t, err)
		assert.Nil(t, resp.PinnedContext)
		assert.False(t, resp.Session.PinnedContextPresent)
	})

	t.Run("delegated start and other readers allowed as GetSession", func(t *testing.T) {
		d, err := srv.StartSession(delegatingClaims("fcm-service", fcmClientID), &pb.StartSessionRequest{
			AgentId:       "agent-1",
			OnBehalfOf:    subjectToTestProto(assertedUser),
			PinnedContext: strPtr2("delegated ctx"),
		})
		require.NoError(t, err)
		for _, c := range []context.Context{delegatedUserClaims(), thirdPartyClaims(), ctxAs(humanClaims("bob"))} {
			resp, err := srv.GetSession(c, &pb.GetSessionRequest{SessionId: d.Session.SessionId, IncludePinnedContext: true})
			require.NoError(t, err)
			assert.Equal(t, "delegated ctx", resp.GetPinnedContext())
		}
	})
}
