package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

// TestExpiredState_ProtoRoundTrip proves SESSION_STATE_EXPIRED maps to and
// from session.StatusExpired, so GetSession/ListSessions and the ListSessions
// state filter see the same value.
func TestExpiredState_ProtoRoundTrip(t *testing.T) {
	assert.Equal(t, pb.SessionState_SESSION_STATE_EXPIRED, statusToProto(session.StatusExpired))
	assert.Equal(t, session.StatusExpired, statusFromProto(pb.SessionState_SESSION_STATE_EXPIRED))
}
