package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/whagent_net/events"
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
	ActivityUpdateSessionStatus    = "UpdateSessionStatus"
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
	// LLM is the OpenRouter client (issue #2112) CallModel calls.
	LLM *llm.Client
	// Prices is the per-model price table (WHAGENT_PRICE_TABLE_PATH,
	// ENV.md) CommitTurn uses to estimate cost when the provider omits it
	// (LB6/FR7). May be nil in a dev/test process that never exercises
	// that fallback -- resolveCost only dereferences it when the provider
	// omitted cost, see resolveCost's doc comment.
	Prices *llm.PriceTable
}

// ResolveAgentDefinitionResult is ResolveAgentDefinition's activity result.
type ResolveAgentDefinitionResult struct {
	Definition session.AgentDefinition
	// Model is the effective model for this turn (FR5): the session's
	// ModelOverride when set, else Definition.Model. Resolved here (rather
	// than left for a later step to compute) because it needs the same
	// fresh, never-cached session/definition reads this activity already
	// does -- a separate activity would just re-read both rows again.
	Model string
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

	sess, err := a.Store.Sessions().GetByID(ctx, sessionID)
	if err != nil {
		return ResolveAgentDefinitionResult{}, fmt.Errorf("resolve agent definition: get session: %w", err)
	}
	if sess == nil {
		return ResolveAgentDefinitionResult{}, fmt.Errorf("resolve agent definition: session %s not found", sessionID)
	}

	model := def.Model
	if sess.ModelOverride != nil && *sess.ModelOverride != "" {
		model = *sess.ModelOverride
	}

	return ResolveAgentDefinitionResult{Definition: *def, Model: model}, nil
}

// CallModelInput is CallModel's activity input. EventIDs is the ordered
// event-ID list BuildContext selected -- never transcript bodies
// (ARCHITECTURE.md "Activity payload discipline": activities pass event
// IDs, not transcript bodies, so Temporal history stays small). Model is
// carried explicitly (the session's resolved model -- agent definition
// default or FR5 per-session override, ResolveAgentDefinitionResult.Model)
// rather than re-derived from a Definition, since CallModelInput must stay
// small and self-contained the same way EventIDs does.
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
// over the activity boundary itself -- see CallModelInput's doc comment),
// decodes them back into an llm.Request via eventsToMessages
// (context.go), and calls a.LLM.Complete using the session's resolved
// model. No tools are attached to the request -- the tool-call dispatch
// step is a no-op hook in this task (processTurn, workflow.go); a model
// response that happens to request tool calls anyway is still recorded
// verbatim by CommitTurn, just not acted on until the follow-up
// tool-dispatch task fills that step in.
func (a *Activities) CallModel(ctx context.Context, in CallModelInput) (CallModelResult, error) {
	if a.Store == nil {
		return CallModelResult{}, fmt.Errorf("worker: Activities.Store is nil")
	}
	if a.LLM == nil {
		return CallModelResult{}, fmt.Errorf("worker: Activities.LLM is nil")
	}

	evs, err := a.Store.Transcript().ReadByIDs(ctx, in.SessionID, in.EventIDs)
	if err != nil {
		return CallModelResult{}, fmt.Errorf("call model: read context events: %w", err)
	}

	messages, err := eventsToMessages(evs)
	if err != nil {
		return CallModelResult{}, fmt.Errorf("call model: decode context events: %w", err)
	}

	resp, err := a.LLM.Complete(ctx, llm.Request{Model: in.Model, Messages: messages})
	if err != nil {
		return CallModelResult{}, fmt.Errorf("call model: %w", err)
	}

	return CallModelResult{Response: resp}, nil
}

// CommitTurnInput is CommitTurn's activity input.
type CommitTurnInput struct {
	SessionID uuid.UUID
	Turn      int
	// Model is the same effective model CallModel used
	// (ResolveAgentDefinitionResult.Model) -- recorded on the turn_usage
	// row and used to look up a price-table estimate when the provider
	// omits cost (resolveCost).
	Model string
	// EventIDs is BuildContext's selected list, carried through so a real
	// implementation can correlate the turn's context with what actually
	// got committed if it ever needs to (informational only -- CommitTurn
	// does not re-read these rows).
	EventIDs []uuid.UUID
	Response llm.Response
}

// CommitTurnResult is CommitTurn's activity result.
type CommitTurnResult struct {
	// Done reports whether the agent finished the whole session (session
	// -> `done`) as opposed to waiting for the next turn (session ->
	// `awaiting_input`, ARCHITECTURE.md "Session workflow" step 6).
	// Terminal classification -- deciding when the agent itself has
	// finished the task, as opposed to merely finishing one turn -- is a
	// follow-up task per the issue body ("Tool dispatch, cap enforcement,
	// and terminal classification land in follow-up tasks"), so this is
	// always false in this task: every turn ends in `awaiting_input`,
	// waiting for the next signalled turn, until a future task teaches
	// CommitTurn to recognize a real finish signal.
	Done bool
}

// CommitTurn is per-turn activity #5 (ARCHITECTURE.md "Session workflow"):
// commits the turn's model-response event and usage record together via
// a.Store.CommitTurn (session/turn_commit.go) -- one atomic, retry-safe
// operation (Testing phase: "a turn whose commit activity fails once and
// is retried commits exactly one set of events"). Session status
// transitions (awaiting_input/done) are written by the workflow itself
// (workflow.go's updateSessionStatus) after this activity returns, not
// here -- keeping this activity scoped to exactly ARCHITECTURE.md's step
// 5 ("commit turn events... publish... commit usage record") makes its
// own retry-safety story self-contained, independent of the session
// status write's.
func (a *Activities) CommitTurn(ctx context.Context, in CommitTurnInput) (CommitTurnResult, error) {
	if a.Store == nil {
		return CommitTurnResult{}, fmt.Errorf("worker: Activities.Store is nil")
	}

	cost, estimated, err := resolveCost(a.Prices, in.Response.Usage, in.Model)
	if err != nil {
		return CommitTurnResult{}, fmt.Errorf("commit turn: resolve cost: %w", err)
	}

	payload, err := marshalMessagePayload(in.Response.Message)
	if err != nil {
		return CommitTurnResult{}, fmt.Errorf("commit turn: marshal model response: %w", err)
	}

	var generationID *string
	if in.Response.Usage.GenerationID != "" {
		genID := in.Response.Usage.GenerationID
		generationID = &genID
	}

	_, err = a.Store.CommitTurn(ctx, session.CommitTurnParams{
		SessionID: in.SessionID,
		Turn:      in.Turn,
		EventType: events.EventTypeAssistantMessage,
		Payload:   payload,
		Usage: session.TurnUsage{
			Model:            in.Model,
			PromptTokens:     in.Response.Usage.PromptTokens,
			CompletionTokens: in.Response.Usage.CompletionTokens,
			CostUSD:          cost.Float64(),
			CostEstimated:    estimated,
			GenerationID:     generationID,
		},
	})
	if err != nil {
		return CommitTurnResult{}, fmt.Errorf("commit turn: %w", err)
	}

	return CommitTurnResult{Done: false}, nil
}

// resolveCost is CommitTurn's cost-resolution step (FR7/LB6): the
// provider-reported cost when usage supplies one, otherwise an estimate
// from prices (FR7's fail-open guard against a silent zero/free cost when
// neither is available). Safe to call with a nil prices whenever
// usage.ProviderCostUSD is non-nil -- llm.PriceTable.ResolveCost returns
// before ever dereferencing its receiver in that case -- but a nil prices
// with no provider cost is reported as an explicit error here rather than
// panicking inside ResolveCost's *PriceTable.Lookup.
func resolveCost(prices *llm.PriceTable, usage llm.UsageReport, model string) (llm.CostUSD, bool, error) {
	if usage.ProviderCostUSD == nil && prices == nil {
		return 0, false, fmt.Errorf("provider omitted cost and no price table is configured for model %q", model)
	}
	return prices.ResolveCost(usage, model)
}

// UpdateSessionStatusInput is UpdateSessionStatus's activity input.
type UpdateSessionStatusInput struct {
	SessionID uuid.UUID
	Status    session.Status
	// Terminal carries cap_kind/error_category/error_detail for a capped
	// or failed transition (session.TerminalReason) -- always nil in this
	// task's calls (workflow.go only ever writes running/awaiting_input/
	// done/stopped); kept on the input so a future cap-enforcement or
	// terminal-classification task can populate it without changing this
	// activity's shape.
	Terminal *session.TerminalReason
}

// UpdateSessionStatusResult is UpdateSessionStatus's activity result --
// empty; the workflow only needs to know the call succeeded.
type UpdateSessionStatusResult struct{}

// UpdateSessionStatus is SessionWorkflow's session-status write path (step
// 6, ARCHITECTURE.md "Session workflow"): a thin wrapper over
// a.Store.Sessions().UpdateStatus's compare-and-swap. A session already at
// a terminal status (session.ErrTerminalStatus) is not surfaced as an
// activity failure -- the compare-and-swap already refused to overwrite
// whichever terminal status is already recorded (e.g. a concurrent
// StopSession call landed first), and that outcome is correct, not an
// error this activity should fail the workflow over.
func (a *Activities) UpdateSessionStatus(ctx context.Context, in UpdateSessionStatusInput) (UpdateSessionStatusResult, error) {
	if a.Store == nil {
		return UpdateSessionStatusResult{}, fmt.Errorf("worker: Activities.Store is nil")
	}

	err := a.Store.Sessions().UpdateStatus(ctx, in.SessionID, in.Status, in.Terminal)
	if err != nil {
		if errors.Is(err, session.ErrTerminalStatus) {
			return UpdateSessionStatusResult{}, nil
		}
		return UpdateSessionStatusResult{}, fmt.Errorf("update session status: %w", err)
	}
	return UpdateSessionStatusResult{}, nil
}
