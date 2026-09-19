package main

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
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
