package main

import (
	"context"

	"github.com/google/uuid"

	"github.com/whale-net/everything/whagent_net/session"
)

// BuildContextInput is BuildContext's activity input.
type BuildContextInput struct {
	SessionID uuid.UUID
	Turn      int
	// Definition is the agent definition ResolveAgentDefinition (activities.go)
	// resolved for this turn -- passed through rather than re-resolved, so
	// BuildContext and CallModel (activities.go) agree on exactly the same
	// definition a turn used even if it drifts again before the turn ends.
	Definition session.AgentDefinition
	// Input is this turn's new user input (SendTurnSignal.Input,
	// workflow.go), the one piece of this turn's context BuildContext did
	// not already have sitting in the transcript before this call.
	Input string
}

// BuildContextResult is BuildContext's activity result: only the ordered
// event-ID list the context projection was built from -- never the
// assembled message bodies themselves. This is what ARCHITECTURE.md
// "Three nouns: session, transcript, context" means by "context is
// derived, ephemeral, rebuilt every turn... never stored; each turn
// records the event-ID list it was built from" and what "Activity payload
// discipline" means by "activities pass event IDs, not transcript
// bodies" -- CallModel (activities.go) receives this same EventIDs slice
// and re-reads the rows itself rather than receiving bodies over the
// activity boundary a second time.
type BuildContextResult struct {
	EventIDs []uuid.UUID
}

// BuildContext is per-turn activity #2 (ARCHITECTURE.md "Session
// workflow"): a budgeted projection over the transcript -- recent events
// plus summaries plus the agent definition, fitted to a token budget --
// selecting the ordered event-ID list that projection was built from. The
// projection itself (the assembled llm.Message list) is derived and
// ephemeral and must never be returned from this activity or otherwise
// enter workflow history; what BuildContextResult carries back to the
// workflow is only EventIDs, which Implementation phase also persists
// into `turn_context` (#2109) so a later debug GetTurnContext (C24) needs
// no schema change.
//
// Scaffold phase: a no-op stub returning a zero-value BuildContextResult
// (EventIDs nil). Implementation phase (#2114) replaces this body with the
// real budget-fitting selection (and the turn's new-input event append,
// and the turn_context persistence) -- BuildContextInput/BuildContextResult's
// shape is already the one that work needs, so this replacement changes
// only this method's body, never workflow.go's processTurn call site.
func (a *Activities) BuildContext(ctx context.Context, in BuildContextInput) (BuildContextResult, error) {
	return BuildContextResult{}, nil
}
