package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	whagentpb "github.com/whale-net/everything/whagent_net/protos"
)

// TestSessionState_ExpiredRoundTrip proves the UI renders and parses the
// expired state, so the session list filter can select it.
func TestSessionState_ExpiredRoundTrip(t *testing.T) {
	assert.Equal(t, "expired", sessionStateString(whagentpb.SessionState_SESSION_STATE_EXPIRED))
	state, ok := sessionStateFromString("expired")
	assert.True(t, ok)
	assert.Equal(t, whagentpb.SessionState_SESSION_STATE_EXPIRED, state)
}
