package tools

// Pure-Go coverage for video_script.go's input types: ChannelScopeID
// (server.ChannelScoped) and IdempotencyKey (server.IdempotencyKeyed) on
// all four of save_video_script/greenlight_video_script/deny_video_script/
// archive_video_script's input structs. There is no other pure logic in
// this file to unit-test in isolation -- every mutate function's real
// behavior (viable-verdict gating, CanApprove gating, FR40's transition
// matrix, the FR39 publish freeze, idempotency replay/conflict, and
// Channel scoping) only exists against a real store.VideoScriptStore and
// a real MCP transport, which video_script_integration_test.go (build tag
// "integration") covers, per issue #1825's Testing section.

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestVideoScriptInputs_ChannelScopeID(t *testing.T) {
	id := uuid.New()

	assert.Equal(t, id, SaveVideoScriptInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, SaveVideoScriptInput{ChannelID: "not-a-uuid"}.ChannelScopeID())

	assert.Equal(t, id, GreenlightVideoScriptInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, GreenlightVideoScriptInput{ChannelID: "not-a-uuid"}.ChannelScopeID())

	assert.Equal(t, id, DenyVideoScriptInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, DenyVideoScriptInput{ChannelID: "not-a-uuid"}.ChannelScopeID())

	assert.Equal(t, id, ArchiveVideoScriptInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, ArchiveVideoScriptInput{ChannelID: "not-a-uuid"}.ChannelScopeID())
}

func TestVideoScriptInputs_IdempotencyKey(t *testing.T) {
	assert.Equal(t, "save-key", SaveVideoScriptInput{IdempotencyKeyArg: "save-key"}.IdempotencyKey())
	assert.Equal(t, "", SaveVideoScriptInput{}.IdempotencyKey())

	assert.Equal(t, "greenlight-key", GreenlightVideoScriptInput{IdempotencyKeyArg: "greenlight-key"}.IdempotencyKey())
	assert.Equal(t, "deny-key", DenyVideoScriptInput{IdempotencyKeyArg: "deny-key"}.IdempotencyKey())
	assert.Equal(t, "archive-key", ArchiveVideoScriptInput{IdempotencyKeyArg: "archive-key"}.IdempotencyKey())
}
