package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
)

// TestToolUnlockEventType proves toolUnlockEventType folds callIndex into
// the committed `type` column the same way toolCallEventType/
// toolResultEventType do (issue #2668's Testing phase).
func TestToolUnlockEventType(t *testing.T) {
	assert.Equal(t, "tool_unlock:0", toolUnlockEventType(0))
	assert.Equal(t, "tool_unlock:3", toolUnlockEventType(3))
}

// TestMarshalToolUnlockPayload_RoundTrips proves the tool_unlock payload
// round-trips both ToolNames and Query through JSON unchanged.
func TestMarshalToolUnlockPayload_RoundTrips(t *testing.T) {
	raw, err := marshalToolUnlockPayload([]string{"search_things", "delete_things"}, "things about widgets")
	require.NoError(t, err)

	var payload toolUnlockEventPayload
	require.NoError(t, json.Unmarshal(raw, &payload))
	assert.Equal(t, []string{"search_things", "delete_things"}, payload.ToolNames)
	assert.Equal(t, "things about widgets", payload.Query)
}

// TestEventsToMessages_ToolUnlockEventIsSkipped proves a tool_unlock event
// produces no llm.Message: eventsToMessages must decode a transcript
// containing one identically to the same transcript without it, since
// FR5/FR6's bookkeeping is not part of the model's view of a search_tools
// call (context.go's eventsToMessages doc comment).
func TestEventsToMessages_ToolUnlockEventIsSkipped(t *testing.T) {
	sessionID := uuid.New()

	userPayload, err := marshalMessagePayload(llm.Message{Role: llm.RoleUser, Content: "find me a tool"})
	require.NoError(t, err)
	callPayload, err := marshalToolCallPayload(llm.ToolCall{ID: "call-1", Name: "search_tools", Arguments: `{"query":"widgets"}`})
	require.NoError(t, err)

	// marshalToolResultPayload takes a tools.Result -- build the payload
	// bytes directly instead so this test doesn't need to import
	// whagent_net/worker/tools just for a literal.
	resultRaw, err := json.Marshal(toolResultEventPayload{ToolCallID: "call-1", Name: "search_tools", Content: "found: widget_get", IsError: false})
	require.NoError(t, err)

	unlockPayload, err := marshalToolUnlockPayload([]string{"widget_get"}, "widgets")
	require.NoError(t, err)

	now := time.Now()
	base := []events.Event{
		{EventID: uuid.New(), SessionID: sessionID, Seq: 1, Turn: 1, Type: events.EventTypeUserMessage, Payload: userPayload, CommittedAt: now},
		{EventID: uuid.New(), SessionID: sessionID, Seq: 2, Turn: 1, Type: toolCallEventType(0), Payload: callPayload, CommittedAt: now},
		{EventID: uuid.New(), SessionID: sessionID, Seq: 3, Turn: 1, Type: toolResultEventType(0), Payload: resultRaw, CommittedAt: now},
	}
	withUnlock := append(append([]events.Event{}, base...), events.Event{
		EventID: uuid.New(), SessionID: sessionID, Seq: 4, Turn: 1, Type: toolUnlockEventType(0), Payload: unlockPayload, CommittedAt: now,
	})

	withoutMessages, err := eventsToMessages(base)
	require.NoError(t, err)
	withMessages, err := eventsToMessages(withUnlock)
	require.NoError(t, err)

	assert.Equal(t, withoutMessages, withMessages, "a tool_unlock event must not change eventsToMessages' output")
}
