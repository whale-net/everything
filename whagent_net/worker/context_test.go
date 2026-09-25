package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/worker/tools"
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

// TestEventsToMessages_SearchToolsTriple_YieldsWellFormedAssistantThenToolResultPair
// proves the other half of FR4's transcript shape: a search_tools call's
// full committed triple (assistant message carrying the call, tool_result,
// tool_unlock) decodes into exactly the assistant-message-then-tool-result
// sequence the OpenAI wire protocol requires -- byte-identical in structure
// to a dispatched call's own pair -- with the tool_unlock event contributing
// no message at all.
func TestEventsToMessages_SearchToolsTriple_YieldsWellFormedAssistantThenToolResultPair(t *testing.T) {
	sessionID := uuid.New()
	now := time.Now()

	assistantPayload, err := marshalMessagePayload(llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "search_tools", Arguments: `{"query":"widgets"}`}},
	})
	require.NoError(t, err)
	resultRaw, err := json.Marshal(toolResultEventPayload{ToolCallID: "call-1", Name: "search_tools", Content: "widget_get: fetch a widget", IsError: false})
	require.NoError(t, err)
	unlockPayload, err := marshalToolUnlockPayload([]string{"widget_get"}, "widgets")
	require.NoError(t, err)

	evs := []events.Event{
		{EventID: uuid.New(), SessionID: sessionID, Seq: 1, Turn: 1, Type: events.EventTypeAssistantMessage, Payload: assistantPayload, CommittedAt: now},
		{EventID: uuid.New(), SessionID: sessionID, Seq: 2, Turn: 1, Type: toolResultEventType(0), Payload: resultRaw, CommittedAt: now},
		{EventID: uuid.New(), SessionID: sessionID, Seq: 3, Turn: 1, Type: toolUnlockEventType(0), Payload: unlockPayload, CommittedAt: now},
	}

	messages, err := eventsToMessages(evs)
	require.NoError(t, err)
	require.Len(t, messages, 2, "the tool_unlock event must contribute no message of its own")

	assert.Equal(t, llm.RoleAssistant, messages[0].Role)
	require.Len(t, messages[0].ToolCalls, 1)
	assert.Equal(t, "call-1", messages[0].ToolCalls[0].ID)
	assert.Equal(t, "search_tools", messages[0].ToolCalls[0].Name)

	assert.Equal(t, llm.RoleTool, messages[1].Role)
	assert.Equal(t, "call-1", messages[1].ToolCallID)
	assert.Equal(t, "widget_get: fetch a widget", messages[1].Content)
}

// TestEventsToMessages_OversizedToolResult_ClampedInTheRenderedMessage
// proves the per-result clamp is applied where tool results become
// messages, not only in the budget's accounting. Budgeting and rendering
// have to agree: an event charged at its clamped size but rendered at full
// size would produce a request far larger than the budget approved, which
// is the same unbounded-request failure the clamp exists to prevent.
//
// The transcript event itself is untouched -- this is the model's view of
// the result, not a rewrite of the record (LB1).
func TestEventsToMessages_OversizedToolResult_ClampedInTheRenderedMessage(t *testing.T) {
	raw := strings.Repeat("z", maxToolResultContentChars*3)
	payload, err := marshalToolResultPayload(tools.Result{
		ToolCallID: "call_abc",
		Name:       "list_pending_matches",
		Content:    raw,
	})
	require.NoError(t, err)
	ev := events.Event{
		EventID: uuid.New(),
		Seq:     1,
		Type:    toolResultEventType(0),
		Payload: payload,
	}

	msgs, err := eventsToMessages([]events.Event{ev})
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	assert.Less(t, len(msgs[0].Content), len(raw), "the rendered tool result must be clamped")
	assert.Contains(t, msgs[0].Content, "truncated", "the rendered clamp must be visible to the model")
	assert.Equal(t, llm.RoleTool, msgs[0].Role)
	assert.Equal(t, "call_abc", msgs[0].ToolCallID, "clamping must not disturb the call binding")
}

// TestEventsToMessages_ToolResultUnderLimit_RenderedVerbatim is the
// counterweight: a result that already fits must reach the model byte for
// byte, with no truncation marker a model could misread.
func TestEventsToMessages_ToolResultUnderLimit_RenderedVerbatim(t *testing.T) {
	raw := strings.Repeat("z", 1_000)
	payload, err := marshalToolResultPayload(tools.Result{
		ToolCallID: "call_abc",
		Name:       "get_outcome_bar",
		Content:    raw,
	})
	require.NoError(t, err)
	ev := events.Event{
		EventID: uuid.New(),
		Seq:     1,
		Type:    toolResultEventType(0),
		Payload: payload,
	}

	msgs, err := eventsToMessages([]events.Event{ev})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, raw, msgs[0].Content)
}

// TestEventsToMessages_ToolResultsCommittedBeforeTheirAssistantMessage_
// RendersInProtocolOrder is the regression test for the commit-order bug,
// built from the event order a real turn actually produced: dispatch
// commits tool_call/tool_result events first, and the assistant_message
// carrying those tool_calls only afterwards (workflow.go's processTurn).
//
// The provider requires a `tool` message to answer a tool call in a
// PRECEDING assistant message. Rendered in raw seq order this transcript
// is [tool, tool, assistant(tool_calls)] and is rejected outright; the
// session is then unable to complete another model call, because the
// malformed events are permanently in its transcript.
func TestEventsToMessages_ToolResultsCommittedBeforeTheirAssistantMessage_RendersInProtocolOrder(t *testing.T) {
	ids := []string{"call_a", "call_b"}
	var evs []events.Event
	// tool_call:0, tool_result:0, tool_call:1, tool_result:1, assistant_message:0
	// -- the real ordering, tool_call events included so the skip path runs.
	seq := int64(0)
	for i, id := range ids {
		callPayload, err := marshalToolCallPayload(llm.ToolCall{ID: id, Name: "get_outcome_bar", Arguments: "{}"})
		require.NoError(t, err)
		evs = append(evs, events.Event{EventID: uuid.New(), Seq: seq, Type: toolCallEventType(i), Payload: callPayload})
		seq++

		resPayload, err := marshalToolResultPayload(tools.Result{ToolCallID: id, Name: "get_outcome_bar", Content: "ok"})
		require.NoError(t, err)
		evs = append(evs, events.Event{EventID: uuid.New(), Seq: seq, Type: toolResultEventType(i), Payload: resPayload})
		seq++
	}
	asstPayload, err := marshalMessagePayload(llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: ids[0], Name: "get_outcome_bar", Arguments: "{}"}, {ID: ids[1], Name: "get_outcome_bar", Arguments: "{}"}},
	})
	require.NoError(t, err)
	evs = append(evs, events.Event{EventID: uuid.New(), Seq: seq, Type: assistantMessageEventType(0), Payload: asstPayload})

	msgs, err := eventsToMessages(evs)
	require.NoError(t, err)
	require.Len(t, msgs, 3, "tool_call events are bookkeeping and must not become messages")

	// Every tool result must be preceded by the assistant message that
	// requested it -- the exact invariant the provider enforces.
	seen := map[string]bool{}
	for idx, m := range msgs {
		switch m.Role {
		case llm.RoleAssistant:
			for _, tc := range m.ToolCalls {
				seen[tc.ID] = true
			}
		case llm.RoleTool:
			assert.True(t, seen[m.ToolCallID],
				"message %d is a tool result for %q with no preceding assistant message requesting it (wire order: %v)",
				idx, m.ToolCallID, rolesOf(msgs))
		}
	}
}

// TestEventsToMessages_AlreadyCorrectOrder_IsUnchanged proves the hoist is
// a no-op on a transcript already in protocol order -- an assistant message
// followed by its own results must not be disturbed.
func TestEventsToMessages_AlreadyCorrectOrder_IsUnchanged(t *testing.T) {
	asstPayload, err := marshalMessagePayload(llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "call_a", Name: "get_outcome_bar", Arguments: "{}"}},
	})
	require.NoError(t, err)
	resPayload, err := marshalToolResultPayload(tools.Result{ToolCallID: "call_a", Name: "get_outcome_bar", Content: "ok"})
	require.NoError(t, err)

	msgs, err := eventsToMessages([]events.Event{
		{EventID: uuid.New(), Seq: 0, Type: assistantMessageEventType(0), Payload: asstPayload},
		{EventID: uuid.New(), Seq: 1, Type: toolResultEventType(0), Payload: resPayload},
	})
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, llm.RoleAssistant, msgs[0].Role)
	assert.Equal(t, llm.RoleTool, msgs[1].Role)
}

// TestEventsToMessages_TwoConsecutiveIterations_AreBothRepaired walks the
// multi-iteration case the dev session actually hit, where each
// iteration's results are followed by that iteration's assistant message.
// Repairing one must not disturb the next.
func TestEventsToMessages_TwoConsecutiveIterations_AreBothRepaired(t *testing.T) {
	var evs []events.Event
	seq := int64(0)
	addIteration := func(iter int, ids ...string) {
		for i, id := range ids {
			resPayload, err := marshalToolResultPayload(tools.Result{ToolCallID: id, Name: "t", Content: "ok"})
			require.NoError(t, err)
			evs = append(evs, events.Event{EventID: uuid.New(), Seq: seq, Type: toolResultEventType(iter*10 + i), Payload: resPayload})
			seq++
		}
		calls := make([]llm.ToolCall, len(ids))
		for i, id := range ids {
			calls[i] = llm.ToolCall{ID: id, Name: "t", Arguments: "{}"}
		}
		asstPayload, err := marshalMessagePayload(llm.Message{Role: llm.RoleAssistant, ToolCalls: calls})
		require.NoError(t, err)
		evs = append(evs, events.Event{EventID: uuid.New(), Seq: seq, Type: assistantMessageEventType(iter), Payload: asstPayload})
		seq++
	}
	addIteration(0, "call_a", "call_b")
	addIteration(1, "call_c", "call_d")

	msgs, err := eventsToMessages(evs)
	require.NoError(t, err)
	require.Len(t, msgs, 6)

	seen := map[string]bool{}
	for idx, m := range msgs {
		switch m.Role {
		case llm.RoleAssistant:
			for _, tc := range m.ToolCalls {
				seen[tc.ID] = true
			}
		case llm.RoleTool:
			assert.True(t, seen[m.ToolCallID],
				"message %d is an orphan tool result (wire order: %v)", idx, rolesOf(msgs))
		}
	}
	// Each assistant message must still immediately precede its own results.
	assert.Equal(t, []string{
		"assistant[call_a,call_b]", "tool:call_a", "tool:call_b",
		"assistant[call_c,call_d]", "tool:call_c", "tool:call_d",
	}, describe(msgs))
}

// rolesOf renders just the roles, for a failure message that has to show
// the wire order that was wrong.
func rolesOf(msgs []llm.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = string(m.Role)
	}
	return out
}

// describe renders roles plus a tool result's call id, or an assistant
// message's call ids -- enough to assert exact wire order in a failure.
func describe(msgs []llm.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		switch m.Role {
		case llm.RoleTool:
			out[i] = "tool:" + m.ToolCallID
		case llm.RoleAssistant:
			ids := make([]string, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				ids[j] = tc.ID
			}
			out[i] = "assistant[" + strings.Join(ids, ",") + "]"
		default:
			out[i] = string(m.Role)
		}
	}
	return out
}

// TestEventsToMessages_GrowingTranscript_RendersPriorTurnsAsAnUnchangedPrefix
// is the prompt-cache guard. The provider caches on a prefix of the request,
// so a message that keeps its exact position as a session grows costs
// nothing to re-send, while any per-turn reshuffle of already-rendered
// messages invalidates the whole cache.
//
// Two things could churn that order and neither may: the tool-loop commit
// order (repaired by hoistAssistantToolCalls) and the context projection
// itself. So render a transcript, append a further turn exactly as the
// workflow would, and assert the earlier rendering is a byte-for-byte
// prefix of the later one -- with no budget trim forced in between, which
// is the case this covers. (A forced trim legitimately shifts the window
// start and costs the cache once; that is truncation, not churn.)
func TestEventsToMessages_GrowingTranscript_RendersPriorTurnsAsAnUnchangedPrefix(t *testing.T) {
	// One realistic turn: a user message, then a tool-loop iteration whose
	// results are committed before the assistant message that requested
	// them -- the order processTurn actually produces.
	iteration := func(seq *int64, iter int, ids ...string) []events.Event {
		var out []events.Event
		for i, id := range ids {
			resPayload, err := marshalToolResultPayload(tools.Result{ToolCallID: id, Name: "t", Content: "ok"})
			require.NoError(t, err)
			out = append(out, events.Event{EventID: uuid.New(), Seq: *seq, Type: toolResultEventType(iter*10 + i), Payload: resPayload})
			*seq++
		}
		calls := make([]llm.ToolCall, len(ids))
		for i, id := range ids {
			calls[i] = llm.ToolCall{ID: id, Name: "t", Arguments: "{}"}
		}
		asstPayload, err := marshalMessagePayload(llm.Message{Role: llm.RoleAssistant, ToolCalls: calls})
		require.NoError(t, err)
		out = append(out, events.Event{EventID: uuid.New(), Seq: *seq, Type: assistantMessageEventType(iter), Payload: asstPayload})
		*seq++
		return out
	}

	var seq int64
	var evs []events.Event
	userPayload, err := marshalMessagePayload(llm.Message{Role: llm.RoleUser, Content: "first question"})
	require.NoError(t, err)
	evs = append(evs, events.Event{EventID: uuid.New(), Seq: seq, Type: events.EventTypeUserMessage, Payload: userPayload})
	seq++
	evs = append(evs, iteration(&seq, 0, "call_a", "call_b")...)

	first, err := eventsToMessages(evs)
	require.NoError(t, err)
	require.NotEmpty(t, first)

	// Append a second turn, including its own tool-loop iteration, the way
	// a real session grows.
	userPayload2, err := marshalMessagePayload(llm.Message{Role: llm.RoleUser, Content: "second question"})
	require.NoError(t, err)
	evs = append(evs, events.Event{EventID: uuid.New(), Seq: seq, Type: events.EventTypeUserMessage, Payload: userPayload2})
	seq++
	evs = append(evs, iteration(&seq, 1, "call_c", "call_d")...)

	second, err := eventsToMessages(evs)
	require.NoError(t, err)

	require.Greater(t, len(second), len(first), "the second turn must actually add messages")
	assert.Equal(t, describe(first), describe(second[:len(first)]),
		"the earlier turn's messages must render identically and in the same order; a change here busts the prompt cache for the whole conversation")
}
