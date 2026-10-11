package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// TestSessionStateString_Expired proves the MCP surface reports an expired
// session as "expired", not "unspecified".
func TestSessionStateString_Expired(t *testing.T) {
	assert.Equal(t, "expired", sessionStateString(pb.SessionState_SESSION_STATE_EXPIRED))
}
