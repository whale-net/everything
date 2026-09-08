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
// Scaffold phase (this task): capCheck's shape and defaultMaxTurns/
// defaultMaxCostUSD are fixed; checkCaps itself is a stub. Implementation
// phase fills in the real turns-then-cost evaluation order and wires this
// function (plus activities.go's SumCost activity for the cost side)
// into processTurn under a
// workflow.GetVersion("session-workflow-cap-enforcement", ...) gate per
// workflow.go's NFR1 doc comment.
package main

import (
	"fmt"

	"github.com/whale-net/everything/whagent_net/session"
)

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
// Not implemented in this Scaffold-phase task -- see this file's package
// doc comment for what Implementation phase fills in, including the
// documented check order (turns before cost) and the red/green discipline
// the issue body's Testing section describes ("switch cost-cap evaluation
// to an in-workflow mutable counter, observe the SumCost test go red,
// revert").
func checkCaps(turn int, def session.AgentDefinition, costSoFar float64) (capCheck, error) {
	return capCheck{}, fmt.Errorf("worker: checkCaps not implemented (issue #2119 Implementation phase)")
}
