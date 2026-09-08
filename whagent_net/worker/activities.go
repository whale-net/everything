package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
)

// Activity name constants. SessionWorkflow (workflow.go) dispatches every
// activity by these string names, not by Go method value, so the workflow
// depends on this call-and-response shape rather than a concrete
// *Activities value -- the same convention
// tools/app_registry/worker/writeback.ActivityRenderEnvironmentState and
// audience_score_system/worker/sync.ActivityLoadChannelState document. main.go
// registers *Activities' methods under these same names.
const (
	ActivityResolveAgentDefinition = "ResolveAgentDefinition"
	ActivityBuildContext           = "BuildContext"
	ActivityCallModel              = "CallModel"
	ActivityCommitTurn             = "CommitTurn"
)

// Activities groups the per-turn activities SessionWorkflow drives
// (ARCHITECTURE.md "Session workflow") over shared dependencies. One
// Activities value per worker process (see main.go). Every method may
// perform I/O and must never be invoked directly from workflow code --
// only via workflow.ExecuteActivity (see workflow.go's package doc
// comment, "Determinism").
type Activities struct {
	// Store is the shared whagent_net/session package -- the same store
	// api imports directly (ARCHITECTURE.md "Service boundary vs. package
	// boundary": no RPC hop between api and worker).
	Store *session.Store
	// LLM is the OpenRouter client (issue #2112) CallModel's
	// Implementation-phase body calls.
	LLM *llm.Client
	// Prices is the per-model price table (WHAGENT_PRICE_TABLE_PATH,
	// ENV.md) CallModel's Implementation-phase body uses to estimate cost
	// when the provider omits it (LB6/FR7). May be nil in a dev/test
	// process that never exercises that fallback.
	Prices *llm.PriceTable
}

// ResolveAgentDefinitionResult is ResolveAgentDefinition's activity result.
type ResolveAgentDefinitionResult struct {
	Definition session.AgentDefinition
}

// ResolveAgentDefinition is per-turn activity #1 (ARCHITECTURE.md "Session
// workflow"): resolves sessionID's CURRENT agent definition assignment
// fresh on every call. It is never cached in workflow state or history --
// sessions are long-lived and agent definitions drift, so the workflow
// re-executes this activity every turn rather than remembering a prior
// turn's result (the property Testing phase's "a definition changed
// between two turns is picked up on the second turn" case guards, and
// this task's red/green discipline: cache it once, watch that test go
// red, then revert).
func (a *Activities) ResolveAgentDefinition(ctx context.Context, sessionID uuid.UUID) (ResolveAgentDefinitionResult, error) {
	if a.Store == nil {
		return ResolveAgentDefinitionResult{}, fmt.Errorf("worker: Activities.Store is nil")
	}

	assignment, err := a.Store.AgentDefinitions().CurrentAssignment(ctx, sessionID)
	if err != nil {
		return ResolveAgentDefinitionResult{}, fmt.Errorf("resolve agent definition: get current assignment: %w", err)
	}
	if assignment == nil {
		return ResolveAgentDefinitionResult{}, fmt.Errorf("resolve agent definition: session %s has no agent definition assignment", sessionID)
	}

	def, err := a.Store.AgentDefinitions().GetVersion(ctx, assignment.AgentID, assignment.AgentVersion)
	if err != nil {
		return ResolveAgentDefinitionResult{}, fmt.Errorf("resolve agent definition: get version: %w", err)
	}
	if def == nil {
		return ResolveAgentDefinitionResult{}, fmt.Errorf("resolve agent definition: %s v%d not found", assignment.AgentID, assignment.AgentVersion)
	}

	return ResolveAgentDefinitionResult{Definition: *def}, nil
}

// CallModelInput is CallModel's activity input. EventIDs is the ordered
// event-ID list BuildContext selected -- never transcript bodies
// (ARCHITECTURE.md "Activity payload discipline": activities pass event
// IDs, not transcript bodies, so Temporal history stays small). Model is
// carried explicitly (the session's resolved model -- agent definition
// default or FR5 per-session override) rather than re-derived from
// Definition, since CallModelInput must stay small and self-contained the
// same way EventIDs does.
type CallModelInput struct {
	SessionID uuid.UUID
	Turn      int
	Model     string
	EventIDs  []uuid.UUID
}

// CallModelResult is CallModel's activity result.
type CallModelResult struct {
	Response llm.Response
}

// CallModel is per-turn activity #3 (ARCHITECTURE.md "Session workflow"):
// re-reads in.EventIDs' rows from a.Store (never receives their bodies
// over the activity boundary itself -- see CallModelInput's doc comment)
// to assemble an llm.Request, then calls a.LLM.Complete using the
// session's resolved model.
//
// Scaffold phase: a no-op stub returning a zero-value CallModelResult.
// Implementation phase (#2114) replaces this body with the real
// read+assemble+call sequence -- CallModelInput/CallModelResult's shape is
// already the one that sequence needs, so this replacement changes only
// this method's body, never workflow.go's processTurn call site.
func (a *Activities) CallModel(ctx context.Context, in CallModelInput) (CallModelResult, error) {
	return CallModelResult{}, nil
}

// CommitTurnInput is CommitTurn's activity input.
type CommitTurnInput struct {
	SessionID uuid.UUID
	Turn      int
	// EventIDs is BuildContext's selected list, carried through so a real
	// implementation can correlate the turn's context with what actually
	// got committed if it ever needs to (informational only -- CommitTurn
	// does not re-read these rows).
	EventIDs []uuid.UUID
	Response llm.Response
}

// CommitTurnResult is CommitTurn's activity result.
type CommitTurnResult struct {
	// Done reports whether the agent finished (session -> `done`) as
	// opposed to waiting for the next turn (session -> `awaiting_input`,
	// ARCHITECTURE.md "Session workflow" step 6). Always false in this
	// task's Scaffold-phase no-op body.
	Done bool
}

// CommitTurn is per-turn activity #5 (ARCHITECTURE.md "Session workflow"):
// commits the turn's events to a.Store.Transcript() (publish-after-commit
// is Append's job, see whagent_net/session/transcript.go) and the turn's
// usage record via a.Store.Usage().RecordTurn.
//
// Scaffold phase: a no-op stub returning a zero-value CommitTurnResult.
// Implementation phase (#2114) replaces this body with the real commit
// sequence, including the turn_context event-ID list write (context.go's
// BuildContextResult.EventIDs is what that write persists) and the
// session status update (step 6) -- CommitTurnInput/CommitTurnResult's
// shape is already the one that sequence needs.
func (a *Activities) CommitTurn(ctx context.Context, in CommitTurnInput) (CommitTurnResult, error) {
	return CommitTurnResult{}, nil
}
