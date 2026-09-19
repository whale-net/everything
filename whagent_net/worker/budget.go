package main

import (
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

// fitToBudget returns the subset of evs that fits within budget once
// toolDefs have been charged in full -- the search-mode replacement for
// context.go's flat maxContextEvents truncation (BuildContext branches on
// ToolLoadingMode to choose between the two). Scaffold-phase stub: compiles
// and returns evs unfiltered; the real accounting (charge toolDefs first,
// fill newest-first, never split an event, floor behavior when toolDefs
// alone exceed budget) lands in this issue's Implementation phase.
func fitToBudget(toolDefs []llm.ToolDefinition, evs []events.Event, budget int) []events.Event {
	return evs
}
