package main

import (
	"encoding/json"
	"fmt"
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

// maxToolResultContentChars is the size threshold at which one tool result
// is truncated, and roughly the size it is truncated TO: a domain MCP
// server returns whatever it likes -- a list endpoint answering with 50
// records runs to tens of kilobytes -- and a single such result otherwise
// consumes most or all of a turn's budget on its own, crowding out the
// conversation around it. 20,000 characters is ~5K tokens: enough
// head-and-tail to keep a list result's shape and both ends, small enough
// that several can coexist in one turn. The clamped output is this figure
// plus the truncation marker, which is why this is a threshold rather than
// a hard ceiling on the returned string.
const maxToolResultContentChars = 20_000

// clampToolResultContent trims one tool result's content to roughly
// maxToolResultContentChars, keeping a head and a tail with an explicit
// marker between them, and returns content unchanged when it already fits.
// Head-and-tail rather than head-only because a truncated JSON list is
// usually unusable, while its first and last records are usually the ones a
// model reasons about.
//
// The marker is stated in the content rather than left implicit: a model
// that believes it is reading a complete 84KB list will draw conclusions
// from a record it never saw, which is worse than knowing the list was cut.
func clampToolResultContent(content string) string {
	if len(content) <= maxToolResultContentChars {
		return content
	}
	marker := fmt.Sprintf("\n\n[... truncated: %d characters of tool result omitted, showing the first and last %d ...]\n\n",
		len(content)-2*headTailChars, headTailChars)
	return content[:headTailChars] + marker + content[len(content)-headTailChars:]
}

// headTailChars is how many characters clampToolResultContent keeps from
// each end of an over-long tool result.
const headTailChars = maxToolResultContentChars / 2

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
//
// A tool result is charged clampToolResultContent's output, not its raw
// body, so budgeting agrees with what eventsToMessages actually renders:
// charging the raw length would let a single over-long result be
// measured as too expensive to keep and so evict the entire conversation
// around it, when what the provider would actually receive is small.
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
		return len(clampToolResultContent(payload.Content))
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
// kept set is always a contiguous most-recent suffix of evs, just
// budget-bounded in characters rather than count-bounded in events -- the
// shape both modes now share. Never splits
// an event: one event either fits whole or is dropped whole. If toolDefs
// alone consume the whole budget (or more), the normal fill would keep
// zero events -- minFloorEvents guards against that "no context at all"
// failure mode by keeping the most recent event(s) anyway (floorWindow,
// widened backwards so the pair stays well-formed); the caller
// (BuildContext, context.go) is what logs the WARNING this overage
// triggers, since it has the session ID this deliberately pure function
// does not take.
//
// Well-formedness: a contiguous newest-suffix can cut between an assistant
// message that requested tool calls and the results answering them, which
// the provider rejects as a malformed request, so a filled window goes
// through trimOrphanedToolResults before being returned. The floor path
// cannot produce that shape and skips the check.
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
		return floorWindow(evs)
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
		return floorWindow(evs)
	}

	// kept was built newest-first; reverse it back to evs' original seq
	// order -- BuildContext's ordered-projection contract.
	for l, r := 0, len(kept)-1; l < r; l, r = l+1, r-1 {
		kept[l], kept[r] = kept[r], kept[l]
	}
	if trimmed := trimOrphanedToolResults(kept); len(trimmed) > 0 {
		return trimmed
	}
	// Every kept event was an orphaned result, meaning the assistant
	// message that requested them was itself too large for the budget.
	// The floor window reaches back past the results to that message and
	// is over budget -- but it is a request the provider will accept,
	// where an empty or all-orphan window is one it rejects outright.
	return floorWindow(evs)
}

// floorWindow is what fitToBudget returns when the budget cannot hold even
// the newest event: the most recent minFloorEvents of evs, widened backwards
// to include the assistant message that requested them when the newest is a
// tool result.
//
// The widening is what keeps the floor from handing the provider a window
// it will reject. The floor already accepts a bounded overage by design --
// a turn with no context at all is the worse failure -- but a single orphan
// tool result is not "less context", it is a malformed request, and it
// costs the whole turn. The assistant message it answers is the one event
// that makes the pair well-formed, so it is the one event worth spending
// overage on. After the widening every leading tool result in the returned
// window is answered by the message immediately before it, so
// trimOrphanedToolResults is a no-op here and is not applied.
func floorWindow(evs []events.Event) []events.Event {
	if len(evs) == 0 {
		return evs
	}
	start := len(evs) - minFloorEvents
	if start < 0 {
		start = 0
	}
	if strings.HasPrefix(evs[len(evs)-1].Type, events.EventTypeToolResult+":") {
		// Walk back over the run of results the newest belongs to, to the
		// assistant message that requested them.
		for start > 0 && strings.HasPrefix(evs[start-1].Type, events.EventTypeToolResult+":") {
			start--
		}
		if start > 0 {
			start--
		}
	}
	return evs[start:]
}

// trimOrphanedToolResults drops any leading tool_result events from a
// budgeted context window.
//
// The kept set is always a contiguous newest-suffix of the transcript, so
// the cut can land immediately after the assistant message that requested
// a set of tool calls -- leaving a window that opens with tool results
// whose originating assistant message (carrying their tool_calls) was
// dropped. The provider requires every tool result to answer a tool call
// in the immediately preceding assistant message, so such a window is
// rejected outright as a malformed request: the turn fails on a 400 that
// no retry can fix, and the session's own transcript is blameless.
//
// Advancing forward to the next real message is the only correct repair --
// the dropped assistant message cannot be re-added, since the events
// before the cut were dropped precisely because the budget could not hold
// them. This is the same reason the assistant-message side needs no
// equivalent guard: a window starting at an assistant message keeps every
// result that follows it.
//
// Returns nil when every event in kept is an orphaned result, which asks
// the caller to fall back to floorWindow rather than hand CallModel a
// window it would reject.
func trimOrphanedToolResults(kept []events.Event) []events.Event {
	i := 0
	for i < len(kept) && strings.HasPrefix(kept[i].Type, events.EventTypeToolResult+":") {
		i++
	}
	if i == 0 {
		return kept
	}
	return kept[i:]
}
