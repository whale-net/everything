package main

import (
	"encoding/json"
	"strings"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
)

// searchModeContextBudget is the single shared per-turn budget FR10 (issue
// #2673, root plan #2602) charges a search-mode session's tool definitions
// and transcript content against together, in fitToBudget below.
//
// Unit: characters, counted over the same JSON-ish serialization that will
// actually be sent to the provider -- for a tool definition, the length of
// its name+description+parameters as `llm` renders it onto the wire; for a
// transcript event, the length of its decoded message content (plus tool-
// call arguments / tool-result content where present). A character count
// needs no tokenizer dependency, is monotonic in real token cost, and
// charges both halves the same way, so "unlocking more tools narrows the
// room left for transcript content" (FR10) is arithmetically true rather
// than approximate. If a real tokenizer is introduced later, only this
// file changes.
//
// Value: 120,000 characters, a ~30K-token budget at a ~4-chars/token
// heuristic -- comfortable headroom under the context windows of the
// models whagent-net targets (ARCHITECTURE.md), while still being small
// enough that unlocking several tools measurably narrows the transcript
// half in practice. This is a compile-time constant deliberately, not a
// new env var, for this milestone (ENV.md unchanged).
const searchModeContextBudget = 120_000

// minFloorEvents is the number of most-recent events fitToBudget keeps
// even when toolDefs alone consume the entire budget (see fitToBudget's
// doc comment) -- a turn with zero context at all is a worse failure mode
// than a turn that runs over the nominal budget, so this floor trades a
// small, bounded overage for never handing the model nothing to work
// from.
const minFloorEvents = 1

// toolDefCharge is one tool definition's contribution to
// searchModeContextBudget: the length of its JSON serialization over
// exactly the fields that reach the wire (llm.ToolDefinition's Name,
// Description, Parameters -- see llm/client.go's toWireTools, which this
// mirrors field-for-field without importing it, since toWireTools itself
// is unexported and OpenAI-SDK-shaped rather than a plain byte count).
func toolDefCharge(t llm.ToolDefinition) int {
	raw, err := json.Marshal(struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters,omitempty"`
	}{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	if err != nil {
		// Only reachable if Parameters holds something json.Marshal
		// rejects (a func/chan value) -- never produced by
		// tools.ListToolDefinitions, whose Parameters always decodes from
		// an MCP server's JSON Schema. Fall back to the name+description
		// length rather than panicking a budgeting helper over it.
		return len(t.Name) + len(t.Description)
	}
	return len(raw)
}

// toolDefsCharge sums toolDefCharge across every entry of toolDefs --
// fitToBudget's "charge every entry of Tools, including search_tools
// itself and every sticky-unlocked tool" (issue #2673), never skipping or
// re-ordering any of them.
func toolDefsCharge(toolDefs []llm.ToolDefinition) int {
	total := 0
	for _, t := range toolDefs {
		total += toolDefCharge(t)
	}
	return total
}

// eventCharge is one transcript event's contribution to
// searchModeContextBudget, measured the same way as toolDefCharge so the
// two halves are comparable: the decoded message content plus tool-call
// arguments / tool-result content where present. Mirrors
// eventsToMessages' (context.go) own decode switch field-for-field, since
// that is the exact content that would reach the wire if this event is
// kept -- an event eventsToMessages does not turn into a message at all
// (tool_call, tool_unlock: whagent-net's own bookkeeping, never sent to
// the provider -- see eventsToMessages' doc comment) costs nothing here
// either. A payload that fails to decode costs nothing rather than
// failing the whole budgeting pass over it.
func eventCharge(ev events.Event) int {
	switch {
	case ev.Type == events.EventTypeUserMessage || ev.Type == events.EventTypeAssistantMessage ||
		strings.HasPrefix(ev.Type, events.EventTypeAssistantMessage+":"):
		var payload transcriptMessagePayload
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			return 0
		}
		total := len(payload.Content)
		for _, tc := range payload.ToolCalls {
			total += len(tc.Arguments)
		}
		return total
	case strings.HasPrefix(ev.Type, events.EventTypeToolResult+":"):
		var payload toolResultEventPayload
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			return 0
		}
		return len(payload.Content)
	default:
		return 0
	}
}

// fitToBudget returns the subset of evs that fits within budget once
// toolDefs have been charged in full -- the search-mode replacement for
// context.go's flat maxContextEvents truncation (BuildContext branches on
// ToolLoadingMode to choose between the two).
//
// Accounting: toolDefs are charged first and in full and are never
// dropped -- they are what the model needs to act at all, and this is
// exactly what makes "unlocking more tools narrows the room left for
// transcript content" (FR10) arithmetically true. The remaining budget
// then fills evs newest-first (evs is assumed to already be in seq
// order); an event that does not fit stops the fill entirely rather than
// being skipped over in favor of an older, possibly-cheaper one -- so the
// kept set is always a contiguous most-recent suffix of evs, the same
// shape context.go's maxContextEvents truncation already produces for
// bulk mode, just budget-bounded instead of count-bounded. Never splits
// an event: one event either fits whole or is dropped whole. If toolDefs
// alone consume the whole budget (or more), the normal fill would keep
// zero events -- minFloorEvents guards against that "no context at all"
// failure mode by keeping the most recent event(s) anyway; the caller
// (BuildContext, context.go) is what logs the WARNING this overage
// triggers, since it has the session ID this deliberately pure function
// does not take.
//
// Purity/determinism: fitToBudget is a pure function of its inputs --
// same toolDefs/evs/budget always produces the same result, and toolDefs
// is only ever read (summed via toolDefsCharge), never mutated, reordered,
// or re-filtered. #2669 pins Tools' order for prompt-cache prefix
// stability; rebuilding or re-sorting toolDefs here, even transiently,
// would undo that.
func fitToBudget(toolDefs []llm.ToolDefinition, evs []events.Event, budget int) []events.Event {
	remaining := budget - toolDefsCharge(toolDefs)

	if remaining <= 0 || len(evs) == 0 {
		if len(evs) <= minFloorEvents {
			return evs
		}
		return evs[len(evs)-minFloorEvents:]
	}

	kept := make([]events.Event, 0, len(evs))
	used := 0
	for i := len(evs) - 1; i >= 0; i-- {
		c := eventCharge(evs[i])
		if used+c > remaining {
			break
		}
		used += c
		kept = append(kept, evs[i])
	}

	if len(kept) == 0 {
		// Not triggered by toolDefs alone this time (remaining > 0 above),
		// but the newest single event still doesn't fit -- the same "no
		// context at all" failure mode the doc comment above describes,
		// so the same floor applies.
		if len(evs) <= minFloorEvents {
			return evs
		}
		return evs[len(evs)-minFloorEvents:]
	}

	// kept was built newest-first; reverse it back to evs' original seq
	// order -- BuildContext's ordered-projection contract.
	for l, r := 0, len(kept)-1; l < r; l, r = l+1, r-1 {
		kept[l], kept[r] = kept[r], kept[l]
	}
	return kept
}
