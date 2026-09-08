// Cap evaluation (issue #2119; ARCHITECTURE.md "Guardrails"): FR6's turn
// cap and FR7's cost cap, both sourced from the session's pinned agent
// definition (session.AgentDefinition.MaxTurns/MaxCostUSD) -- there is no
// per-session cap override in M1 (deliberate: caps are a safety backstop
// the agent's owner sets, not a per-run dial the operator tunes). Defaults
// are defaultMaxTurns/defaultMaxCostUSD when the definition's fields are
// zero-valued.
//
// checkCaps is invoked by SessionWorkflow.processTurn (workflow.go) both
// before a turn starts and after the turn's LLM response is committed
// (this file's package doc comment mirrors the issue body's "before each
// turn and after each LLM response" -- see workflow.go for exactly where
// each call site lands once wired in). costSoFar must always be the
// UsageStore.SumCost committed running total (usage.go) -- never a
// separately-mutated counter -- so this function takes it as a plain
// argument rather than reading it itself, keeping checkCaps I/O-free and
// safe to call directly from workflow code (no activity needed for the
// turn-count half; the cost half's SumCost read is a separate activity
// call, see activities.go's SumCost).
//
// Implementation phase (this task): checkCaps evaluates the real
// turns-then-cost order documented below; workflow.go's processTurn calls
// it twice per turn (evaluateCaps) -- once "before" (using the turn
// count/cost already committed by prior turns, turn-1) and once "after"
// (using this turn's own just-committed turn count/cost, turn) -- both
// under the workflow.GetVersion("session-workflow-cap-enforcement", ...)
// gate per workflow.go's NFR1 doc comment. See evaluateCaps' doc comment
// in workflow.go for why the pre-check uses turn-1 rather than turn (the
// turn that trips a cap must still be allowed to run once and produce its
// own transcript event -- Testing phase's "ends capped... on the turn
// that reaches the cap" case, not the turn before it).
package main

import (
	"encoding/json"

	"github.com/whale-net/everything/whagent_net/session"
)

// cappedEventPayload is EventTypeCapped's transcript-event payload (FR6/
// FR7, events.go's EventTypeCapped doc comment): which cap tripped, using
// session.CapKind's wire value ("turns" or "cost") -- the exact same value
// UpdateStatus's cap_kind column stores, so a transcript reader and
// GetSession never disagree about which cap ended the session.
type cappedEventPayload struct {
	CapKind session.CapKind `json:"cap_kind"`
}

// marshalCappedEvent is workflow.go's cappedTurn's payload-construction
// step, pulled out here (rather than inlined) so cappedEventPayload's
// field shape has exactly one write site.
func marshalCappedEvent(capKind session.CapKind) (json.RawMessage, error) {
	return json.Marshal(cappedEventPayload{CapKind: capKind})
}

// defaultMaxTurns and defaultMaxCostUSD are FR6/FR7's cap defaults
// (ARCHITECTURE.md "Guardrails": "defaults 100 turns / $1"), applied when
// an agent definition's MaxTurns/MaxCostUSD is the Go zero value -- M1
// seeds agent_definition rows with explicit values (session/agentdef.go's
// Upsert doc comment), but checkCaps must not silently treat an
// unpopulated field as "uncapped".
const (
	defaultMaxTurns   = 100
	defaultMaxCostUSD = 1.0
)

// capCheck is checkCaps' result. At most one of the two caps is ever the
// tripped one -- Capped false means neither cap was reached; Capped true
// means exactly the cap named by CapKind was.
type capCheck struct {
	Capped  bool
	CapKind session.CapKind
}

// checkCaps evaluates FR6 (turn >= the definition's effective MaxTurns)
// and FR7 (costSoFar >= the definition's effective MaxCostUSD) against
// def, applying defaultMaxTurns/defaultMaxCostUSD wherever def's fields
// are zero-valued. turn is the turn number just reached; costSoFar is the
// UsageStore.SumCost committed running total (never a mutable counter,
// FR7).
//
// Check order is turns before cost (documented here per the issue body's
// Testing section): the turn cap is a pure integer comparison, independent
// of whatever the cost read produced, so evaluating it first means a
// turn-cap trip is reported correctly even in the degenerate case where
// costSoFar is 0 because no turn has recorded any usage yet. At most one
// cap is ever reported tripped per call (capCheck's doc comment) --
// checkCaps returns on the first cap it finds, so a call where both caps
// happen to be simultaneously exceeded still reports exactly CapKindTurns,
// never both.
func checkCaps(turn int, def session.AgentDefinition, costSoFar float64) (capCheck, error) {
	maxTurns := def.MaxTurns
	if maxTurns == 0 {
		maxTurns = defaultMaxTurns
	}
	maxCostUSD := def.MaxCostUSD
	if maxCostUSD == 0 {
		maxCostUSD = defaultMaxCostUSD
	}

	if turn >= maxTurns {
		return capCheck{Capped: true, CapKind: session.CapKindTurns}, nil
	}
	if costSoFar >= maxCostUSD {
		return capCheck{Capped: true, CapKind: session.CapKindCost}, nil
	}
	return capCheck{}, nil
}
