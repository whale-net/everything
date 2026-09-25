package main

import (
	"fmt"
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

// fitToBudgetTestEvent builds a user_message transcript event carrying
// exactly contentLen characters of content -- eventCharge (budget.go)
// measures a message event's cost as len(payload.Content), so this
// fixture's cost under fitToBudget is deterministic and known up front,
// letting the tests below assert on exact kept/dropped counts rather than
// approximate behavior.
func fitToBudgetTestEvent(t *testing.T, seq int64, contentLen int) events.Event {
	t.Helper()
	payload, err := marshalMessagePayload(llm.Message{Role: llm.RoleUser, Content: strings.Repeat("x", contentLen)})
	require.NoError(t, err)
	return events.Event{
		EventID:     uuid.New(),
		SessionID:   uuid.New(),
		Seq:         seq,
		Turn:        int(seq),
		Type:        events.EventTypeUserMessage,
		Payload:     payload,
		CommittedAt: time.Now(),
	}
}

// fitToBudgetTestTool builds a minimal tool definition with a
// caller-controlled description length, so a test can make one toolDefs
// slice cost strictly more than another via toolDefCharge (budget.go).
func fitToBudgetTestTool(name string, descLen int) llm.ToolDefinition {
	return llm.ToolDefinition{Name: name, Description: strings.Repeat("d", descLen)}
}

// TestFitToBudget_EverythingFits_KeepsEveryEventInSeqOrder proves the
// no-pressure case: when toolDefs plus every event together fit inside
// budget, fitToBudget keeps every event, in original seq order.
func TestFitToBudget_EverythingFits_KeepsEveryEventInSeqOrder(t *testing.T) {
	toolDefs := []llm.ToolDefinition{fitToBudgetTestTool("search_tools", 10)}
	evs := []events.Event{
		fitToBudgetTestEvent(t, 1, 50),
		fitToBudgetTestEvent(t, 2, 50),
		fitToBudgetTestEvent(t, 3, 50),
	}

	kept := fitToBudget(toolDefs, evs, 1_000_000)

	require.Len(t, kept, 3)
	assert.Equal(t, []int64{1, 2, 3}, []int64{kept[0].Seq, kept[1].Seq, kept[2].Seq},
		"every event must be kept, in original seq order")
}

// TestFitToBudget_TightBudget_DropsOldestEventsFirst proves the
// budget-bound fill direction: newest-first, so under a tight budget the
// oldest events are the ones dropped and the kept set is a contiguous
// most-recent suffix, returned back in seq order.
func TestFitToBudget_TightBudget_DropsOldestEventsFirst(t *testing.T) {
	evs := []events.Event{
		fitToBudgetTestEvent(t, 1, 100),
		fitToBudgetTestEvent(t, 2, 100),
		fitToBudgetTestEvent(t, 3, 100),
	}
	// No tool defs charged, so the whole budget goes to events: exactly
	// room for the two newest (100 + 100 = 200 <= 250) but not all three
	// (300 > 250).
	kept := fitToBudget(nil, evs, 250)

	require.Len(t, kept, 2, "only the two newest events fit under a 250-char budget")
	assert.Equal(t, int64(2), kept[0].Seq)
	assert.Equal(t, int64(3), kept[1].Seq)
}

// TestFitToBudget_MoreUnlockedTools_StrictlyFewerEventsKept is FR10's core
// assertion: charging a larger toolDefs set against the SAME budget and
// the SAME event set narrows the transcript half strictly -- unlocking
// more tools costs transcript room.
func TestFitToBudget_MoreUnlockedTools_StrictlyFewerEventsKept(t *testing.T) {
	evs := make([]events.Event, 10)
	for i := range evs {
		evs[i] = fitToBudgetTestEvent(t, int64(i+1), 1000)
	}

	small := []llm.ToolDefinition{fitToBudgetTestTool("search_tools", 5)}
	large := append(append([]llm.ToolDefinition{}, small...),
		fitToBudgetTestTool("tool_a", 2000),
		fitToBudgetTestTool("tool_b", 2000),
		fitToBudgetTestTool("tool_c", 2000),
	)
	require.Greater(t, toolDefsCharge(large), toolDefsCharge(small), "fixture sanity: large must actually cost more than small")

	// Budget sized to fit every event exactly when only the small toolDefs
	// set is charged.
	budget := toolDefsCharge(small) + 10_000

	keptSmall := fitToBudget(small, evs, budget)
	keptLarge := fitToBudget(large, evs, budget)

	require.Len(t, keptSmall, 10, "the small tool set must leave room for every event")
	assert.Less(t, len(keptLarge), len(keptSmall),
		"unlocking more tools (large) must strictly narrow the transcript room left, versus the small tool set, against the same budget and event set")
}

// TestFitToBudget_ToolDefsExceedBudget_FloorBehaviorNotEmpty proves the
// "no context at all is not a useful failure mode" guard: when toolDefs
// alone consume the whole budget (or more), fitToBudget still returns the
// most recent minFloorEvents event(s) rather than an empty projection.
func TestFitToBudget_ToolDefsExceedBudget_FloorBehaviorNotEmpty(t *testing.T) {
	toolDefs := []llm.ToolDefinition{fitToBudgetTestTool("huge_tool", 10_000)}
	evs := []events.Event{
		fitToBudgetTestEvent(t, 1, 50),
		fitToBudgetTestEvent(t, 2, 50),
		fitToBudgetTestEvent(t, 3, 50),
	}
	budget := toolDefsCharge(toolDefs) - 1 // strictly less than toolDefs alone cost

	kept := fitToBudget(toolDefs, evs, budget)

	require.Len(t, kept, minFloorEvents, "the floor must keep exactly minFloorEvents event(s) rather than returning an empty projection")
	assert.Equal(t, evs[len(evs)-1].EventID, kept[0].EventID, "the floor must keep the MOST RECENT event(s), not the oldest")
}

// TestFitToBudget_ToolDefsExceedBudget_FewerEventsThanFloor_KeepsAll proves
// the floor's boundary: when the whole transcript already has fewer events
// than minFloorEvents, the floor keeps all of them (there is nothing more
// recent to fall back to), rather than panicking on a short slice.
func TestFitToBudget_ToolDefsExceedBudget_FewerEventsThanFloor_KeepsAll(t *testing.T) {
	toolDefs := []llm.ToolDefinition{fitToBudgetTestTool("huge_tool", 10_000)}
	evs := []events.Event{fitToBudgetTestEvent(t, 1, 50)}
	budget := toolDefsCharge(toolDefs) - 1

	kept := fitToBudget(toolDefs, evs, budget)

	require.Len(t, kept, 1)
	assert.Equal(t, evs[0].EventID, kept[0].EventID)
}

// TestFitToBudget_NewestSingleEventDoesNotFit_FloorApplies proves the
// floor also applies when toolDefs alone fit (remaining > 0) but the
// single newest event still doesn't fit in what's left -- a case distinct
// from toolDefs alone exceeding budget, exercising fitToBudget's second
// floor branch.
func TestFitToBudget_NewestSingleEventDoesNotFit_FloorApplies(t *testing.T) {
	toolDefs := []llm.ToolDefinition{fitToBudgetTestTool("search_tools", 5)}
	evs := []events.Event{
		fitToBudgetTestEvent(t, 1, 50),
		fitToBudgetTestEvent(t, 2, 5_000), // far larger than the tiny remaining budget below
	}
	budget := toolDefsCharge(toolDefs) + 10 // remaining=10, too small for either event

	kept := fitToBudget(toolDefs, evs, budget)

	require.Len(t, kept, minFloorEvents)
	assert.Equal(t, evs[len(evs)-1].EventID, kept[0].EventID)
}

// TestFitToBudget_NeverSplitsAnEvent proves an event that does not fit
// stops the fill entirely rather than being skipped over in favor of an
// older, possibly-cheaper one: a big newest event blocks a smaller older
// one from being picked up instead.
func TestFitToBudget_NeverSplitsAnEvent(t *testing.T) {
	evs := []events.Event{
		fitToBudgetTestEvent(t, 1, 10),  // small, older
		fitToBudgetTestEvent(t, 2, 500), // big, newest -- doesn't fit
	}
	// Room for the small, older event alone, but not the big newest one.
	kept := fitToBudget(nil, evs, 20)

	require.Len(t, kept, minFloorEvents, "the newest event doesn't fit and stops the fill -- the floor keeps the newest event anyway rather than falling back to the older, cheaper one")
	assert.Equal(t, evs[1].EventID, kept[0].EventID, "fitToBudget must never skip the newest non-fitting event in favor of an older cheaper one")
}

// TestFitToBudget_Deterministic proves fitToBudget is a pure function of
// its inputs: repeated calls over identical (freshly-copied) toolDefs/evs
// slices always produce the same result.
func TestFitToBudget_Deterministic(t *testing.T) {
	toolDefs := []llm.ToolDefinition{fitToBudgetTestTool("search_tools", 5), fitToBudgetTestTool("tool_a", 50)}
	evs := make([]events.Event, 8)
	for i := range evs {
		evs[i] = fitToBudgetTestEvent(t, int64(i+1), 200)
	}
	budget := toolDefsCharge(toolDefs) + 900

	first := fitToBudget(toolDefs, evs, budget)
	for i := 0; i < 10; i++ {
		// Fresh copies each call so aliasing can't mask a mutation bug as
		// "determinism".
		toolDefsCopy := append([]llm.ToolDefinition{}, toolDefs...)
		evsCopy := append([]events.Event{}, evs...)
		again := fitToBudget(toolDefsCopy, evsCopy, budget)
		require.Len(t, again, len(first))
		for j := range first {
			assert.Equal(t, first[j].EventID, again[j].EventID, "fitToBudget must return the identical kept set on every call over the same inputs")
		}
	}
}

// TestFitToBudget_DoesNotMutateOrReorderToolDefs proves fitToBudget never
// rebuilds, re-sorts, or otherwise mutates toolDefs -- #2669 pins Tools'
// order for prompt-cache prefix stability, and this accounting step is
// exactly where an implementation is tempted to rebuild it from a map or
// re-sort it.
func TestFitToBudget_DoesNotMutateOrReorderToolDefs(t *testing.T) {
	toolDefs := []llm.ToolDefinition{
		fitToBudgetTestTool("search_tools", 5),
		fitToBudgetTestTool("z_tool", 20),
		fitToBudgetTestTool("a_tool", 20),
	}
	original := append([]llm.ToolDefinition{}, toolDefs...)
	evs := []events.Event{fitToBudgetTestEvent(t, 1, 50), fitToBudgetTestEvent(t, 2, 50)}

	_ = fitToBudget(toolDefs, evs, 1_000_000)
	assert.Equal(t, original, toolDefs, "fitToBudget must not mutate or reorder toolDefs")

	// A budget that also exercises the floor path (toolDefs alone exceed
	// budget) must leave toolDefs untouched too.
	_ = fitToBudget(toolDefs, evs, 1)
	assert.Equal(t, original, toolDefs, "fitToBudget must not mutate or reorder toolDefs even on the floor path")
}

// fitToBudgetToolCallEvent builds an assistant_message event requesting
// toolCalls tool calls, the event a following run of tool_result events
// answers. Its cost under eventCharge is the summed call arguments, so a
// caller controls it exactly as fitToBudgetTestEvent does for a message.
func fitToBudgetToolCallEvent(t *testing.T, seq int64, argLen int) events.Event {
	t.Helper()
	payload, err := marshalMessagePayload(llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:        "call_test",
			Name:      "get_channel_overview",
			Arguments: strings.Repeat("a", argLen),
		}},
	})
	require.NoError(t, err)
	return events.Event{
		EventID:     uuid.New(),
		SessionID:   uuid.New(),
		Seq:         seq,
		Turn:        int(seq),
		Type:        events.EventTypeAssistantMessage,
		Payload:     payload,
		CommittedAt: time.Now(),
	}
}

// fitToBudgetToolResultEvent builds the tool_result:<call_index> event
// answering the call above, carrying contentLen characters of result.
func fitToBudgetToolResultEvent(t *testing.T, seq, callIndex int64, contentLen int) events.Event {
	t.Helper()
	payload, err := marshalToolResultPayload(tools.Result{
		ToolCallID: "call_test",
		Name:       "get_channel_overview",
		Content:    strings.Repeat("r", contentLen),
	})
	require.NoError(t, err)
	return events.Event{
		EventID:     uuid.New(),
		SessionID:   uuid.New(),
		Seq:         seq,
		Turn:        int(seq),
		Type:        fmt.Sprintf("%s:%d", events.EventTypeToolResult, callIndex),
		Payload:     payload,
		CommittedAt: time.Now(),
	}
}

// TestFitToBudget_CutBetweenRequestAndResults_NeverOpensOnAnOrphan proves
// the invariant that matters most about a budgeted window, across every
// budget value rather than one hand-picked one: whatever the cut lands on,
// the returned window never opens with a tool_result whose requesting
// assistant message was dropped. Such a window is rejected by the provider
// as a malformed request, and no retry can fix it.
//
// The transcript below is the shape that actually produces the hazard: an
// assistant message requesting a call, the results answering it, then more
// conversation. Sweeping the budget walks the cut across every boundary in
// that sequence, including the one between the assistant message and its
// own results.
func TestFitToBudget_CutBetweenRequestAndResults_NeverOpensOnAnOrphan(t *testing.T) {
	evs := []events.Event{
		fitToBudgetTestEvent(t, 1, 30),           // user
		fitToBudgetToolCallEvent(t, 2, 30),       // assistant requesting 1 call
		fitToBudgetToolResultEvent(t, 3, 0, 200), // its result
		fitToBudgetTestEvent(t, 4, 300),          // next user turn
		fitToBudgetToolCallEvent(t, 5, 30),       // assistant requesting 1 call
		fitToBudgetToolResultEvent(t, 6, 1, 200), // its result
		fitToBudgetTestEvent(t, 7, 300),          // newest user turn
	}

	for budget := 0; budget <= 1_200; budget++ {
		kept := fitToBudget(nil, evs, budget)
		if len(kept) == 0 {
			continue
		}
		assert.False(t, strings.HasPrefix(kept[0].Type, events.EventTypeToolResult+":"),
			"budget %d produced a window opening on an orphaned tool result (first kept seq %d, type %s)",
			budget, kept[0].Seq, kept[0].Type)
	}
}

// TestFitToBudget_OversizedToolResult_StaysWithinBudget proves the
// per-result clamp reaches the accounting, not just the rendering: a
// single tool result far larger than the entire budget is charged its
// clamped size, so it is kept as one ordinary event instead of evicting
// every event around it. This is the dev session's 84KB
// get_channel_overview result, which under raw-length accounting consumed
// the whole budget on its own.
func TestFitToBudget_OversizedToolResult_StaysWithinBudget(t *testing.T) {
	oversized := maxToolResultContentChars * 4
	evs := []events.Event{
		fitToBudgetTestEvent(t, 1, 500),                // user
		fitToBudgetToolResultEvent(t, 2, 0, oversized), // one enormous result
		fitToBudgetTestEvent(t, 3, 500),                // newest user turn
	}

	// A budget with room for the newest user turn and the clamped result,
	// but nowhere near the result's raw length.
	kept := fitToBudget(nil, evs, 25_000)

	require.Len(t, kept, 3, "a clamped oversized result must still be kept, not treated as unaffordable")
	assert.Equal(t, int64(1), kept[0].Seq)
	assert.Equal(t, int64(2), kept[1].Seq)
	assert.Equal(t, int64(3), kept[2].Seq)
}

// TestFitToBudget_OversizedAssistantMessage_FallsBackToFloorWindow covers
// the one case trimming forward cannot repair: every kept event is an
// orphaned result because the assistant message that requested them was
// itself too big to fit. The result must be the floor window, which
// reaches back to that message -- over budget, but a request the provider
// accepts.
func TestFitToBudget_OversizedAssistantMessage_FallsBackToFloorWindow(t *testing.T) {
	evs := []events.Event{
		fitToBudgetToolCallEvent(t, 1, 50_000),   // assistant, far over budget
		fitToBudgetToolResultEvent(t, 2, 0, 100), // its result
	}

	kept := fitToBudget(nil, evs, 1_000)

	require.NotEmpty(t, kept)
	assert.Equal(t, events.EventTypeAssistantMessage, kept[0].Type,
		"an all-orphan window must fall back to the assistant message, not stay orphaned")
	assert.Len(t, kept, 2)
}

// TestClampToolResultContent_UnderLimit_ReturnsInputUnchanged proves the
// clamp is invisible to the overwhelmingly common case of a result that
// already fits, so nothing is marked as truncated that was not.
func TestClampToolResultContent_UnderLimit_ReturnsInputUnchanged(t *testing.T) {
	content := strings.Repeat("r", maxToolResultContentChars)
	assert.Equal(t, content, clampToolResultContent(content))
	assert.Equal(t, "", clampToolResultContent(""))
}

// TestClampToolResultContent_OverLimit_KeepsBothEndsAndMarksTheGap proves
// an oversized result degrades to head-and-tail with an explicit marker,
// rather than a silent head-only cut -- a model told the list was
// truncated can reason about that; one silently handed half a list cannot.
func TestClampToolResultContent_OverLimit_KeepsBothEndsAndMarksTheGap(t *testing.T) {
	head := strings.Repeat("h", 10_000)
	tail := strings.Repeat("t", 10_000)
	content := head + strings.Repeat("m", 60_000) + tail

	got := clampToolResultContent(content)

	assert.Contains(t, got, head, "the head of an oversized result must survive")
	assert.Contains(t, got, tail, "the tail of an oversized result must survive")
	assert.NotContains(t, got, strings.Repeat("m", 100), "the middle must actually be dropped")
	assert.Contains(t, got, "truncated", "the cut must be stated, not silent")
	assert.Less(t, len(got), len(content), "an oversized result must get smaller")
}

// TestFloorWindow_NewestIsToolResult_IncludesTheRequestingMessage proves
// the floor's widening: a floor window whose newest event is a tool result
// must reach back to the assistant message that requested it, since a lone
// orphan is not "minimal context" but a request the provider rejects.
func TestFloorWindow_NewestIsToolResult_IncludesTheRequestingMessage(t *testing.T) {
	evs := []events.Event{
		fitToBudgetTestEvent(t, 1, 10),
		fitToBudgetToolCallEvent(t, 2, 10),
		fitToBudgetToolResultEvent(t, 3, 0, 10),
	}

	kept := floorWindow(evs)

	require.NotEmpty(t, kept)
	assert.Equal(t, events.EventTypeAssistantMessage, kept[0].Type,
		"the floor window must start at the message that requested the result")
}

// TestFloorWindow_EmptyInput_ReturnsEmpty guards the degenerate input.
func TestFloorWindow_EmptyInput_ReturnsEmpty(t *testing.T) {
	assert.Empty(t, floorWindow(nil))
	assert.Empty(t, floorWindow([]events.Event{}))
}
