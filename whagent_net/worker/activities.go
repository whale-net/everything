package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
	"github.com/whale-net/everything/whagent_net/worker/tools"
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
	// ActivitySumCost and ActivityCommitTerminalEvent are issue #2119's two
	// new activities -- FR7's cost-cap input (usage.go's UsageStore.SumCost,
	// the running committed total, never a mutable counter) and the
	// terminal transcript event a cap trip or failure commits before the
	// session's status write goes terminal (FR6/FR7/FR2). See this file's
	// SumCost/CommitTerminalEvent doc comments and caps.go/classify.go.
	ActivitySumCost             = "SumCost"
	ActivityCommitTerminalEvent = "CommitTerminalEvent"

	// ActivityListToolDefinitions and ActivityDispatchTool are issue
	// #2121's two new activities, filling in the tool-dispatch step
	// workflow.go's processTurn has left a no-op hook since #2114 (see
	// that file's package doc comment, "the follow-up tool-dispatch
	// task"): ActivityListToolDefinitions resolves what CallModel should
	// even offer the model this turn (FR8), and ActivityDispatchTool
	// routes one model-requested tool call to its target server (FR2/
	// FR8/FR10/FR11), via the already-implemented
	// whagent_net/worker/tools.Dispatcher (issue #2118). See those two
	// activities' doc comments below.
	ActivityListToolDefinitions = "ListToolDefinitions"
	ActivityDispatchTool        = "DispatchTool"
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
	// Dispatcher routes a turn's model-requested tool calls to their
	// target domain-owned MCP server (issue #2118, ARCHITECTURE.md
	// "Domain-owned MCP servers and the tool contract"). Constructed in
	// main.go from worker's own in-process *persona.Issuer (the same
	// signing-key configuration api reads, ENV.md "Persona claim
	// issuance") plus a.Store.Idempotency() -- see
	// whagent_net/worker/tools/dispatch.go's package doc comment for the
	// full contract. May be nil in a dev/test process that never
	// exercises DispatchTool, same as Prices above.
	Dispatcher *tools.Dispatcher
}

// ResolveAgentDefinitionResult is ResolveAgentDefinition's activity result.
type ResolveAgentDefinitionResult struct {
	Definition session.AgentDefinition
	// Model is the effective model for this turn (FR5): the session's
	// ModelOverride when set, else Definition.Model or (when
	// Definition.ModelDefinitionID is set instead) the referenced
	// model_definition row's Model. Resolved here (rather than left for a
	// later step to compute) because it needs the same fresh, never-cached
	// session/definition reads this activity already does -- a separate
	// activity would just re-read both rows again.
	Model string
	// Provider is the OpenRouter provider-routing preferences that go with
	// Model, resolved from Definition.ModelDefinitionID's model_definition
	// row (converted from session.ProviderPreferences to
	// llm.ProviderPreferences -- see that type's doc comment for why the
	// two packages each carry their own copy of this shape). Nil when
	// Definition names a model directly, or when the session's
	// ModelOverride replaced the model_definition-resolved model --  a
	// caller-supplied override model has no routing preferences of its
	// own to inherit.
	Provider *llm.ProviderPreferences
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

	model, provider, err := resolveModel(ctx, a.Store, *def)
	if err != nil {
		return ResolveAgentDefinitionResult{}, fmt.Errorf("resolve agent definition: %w", err)
	}
	if sess.ModelOverride != nil && *sess.ModelOverride != "" {
		model = *sess.ModelOverride
		provider = nil
	}

	return ResolveAgentDefinitionResult{Definition: *def, Model: model, Provider: provider}, nil
}

// resolveModel resolves def's effective model and OpenRouter
// provider-routing preferences (before any FR5 session ModelOverride is
// applied -- see ResolveAgentDefinition, the only caller): def.Model
// directly when set, or def.ModelDefinitionID's referenced model_definition
// row otherwise -- exactly one of the two is set, per AgentDefinition's
// doc comment.
func resolveModel(ctx context.Context, store *session.Store, def session.AgentDefinition) (string, *llm.ProviderPreferences, error) {
	if def.ModelDefinitionID == nil {
		if def.Model == nil {
			return "", nil, fmt.Errorf("agent definition %s v%d has neither model nor model_definition_id set", def.AgentID, def.Version)
		}
		return *def.Model, nil, nil
	}

	modelDef, err := store.ModelDefinitions().GetByID(ctx, *def.ModelDefinitionID)
	if err != nil {
		return "", nil, fmt.Errorf("get model definition %s: %w", *def.ModelDefinitionID, err)
	}
	if modelDef == nil {
		return "", nil, fmt.Errorf("model definition %s not found (referenced by agent definition %s v%d)", *def.ModelDefinitionID, def.AgentID, def.Version)
	}
	return modelDef.Model, toLLMProviderPreferences(modelDef.Provider), nil
}

// toLLMProviderPreferences converts session.ProviderPreferences (a
// model_definition row's stored routing preferences) into
// llm.ProviderPreferences (the wire client's identical shape) -- the one
// call site that needs both packages, per each type's doc comment on why
// they are not the same Go type.
func toLLMProviderPreferences(p session.ProviderPreferences) *llm.ProviderPreferences {
	return &llm.ProviderPreferences{
		Only:              p.Only,
		Ignore:            p.Ignore,
		Order:             p.Order,
		Quantizations:     p.Quantizations,
		Sort:              p.Sort,
		AllowFallbacks:    p.AllowFallbacks,
		RequireParameters: p.RequireParameters,
		DataCollection:    p.DataCollection,
	}
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
	// Provider is ResolveAgentDefinitionResult.Provider, forwarded
	// verbatim into llm.Request.Provider -- see that field's doc comment.
	Provider *llm.ProviderPreferences
	EventIDs []uuid.UUID
	// Tools is what the model may call this turn (FR8) -- llm.Request.Tools
	// verbatim, so an empty/nil Tools produces the identical
	// no-tools-attached request this activity has always sent (issue
	// #2114/#2117's "no tools are attached to the request" scaffold-phase
	// behavior, preserved for any caller that does not yet populate this
	// field). Implementation phase (issue #2121) populates it from
	// ActivityListToolDefinitions' result, called once per turn ahead of
	// CallModel in processTurn (workflow.go) -- see that activity's doc
	// comment below.
	Tools []llm.ToolDefinition
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
// model plus in.Tools (FR8) -- forwarded to llm.Request.Tools verbatim,
// so a caller that leaves Tools nil/empty gets the identical
// no-tools-attached request this activity has always sent. A model
// response that requests tool calls is still recorded verbatim by
// CommitTurn regardless of whether they were ever dispatched -- see
// ActivityDispatchTool's doc comment for the dispatch step itself, still
// a no-op hook in processTurn (workflow.go) as of this Scaffold-phase
// task.
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

	resp, err := a.LLM.Complete(ctx, llm.Request{Model: in.Model, Messages: messages, Tools: in.Tools, Provider: in.Provider})
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

// CommitTurnResult is processTurn's (workflow.go) per-turn result --
// historically exactly whatever the CommitTurn activity returned, and
// still is on the ordinary (no cap trip, no failure) path. Capped/CapKind/
// Failed/ErrorCategory/ErrorDetail (issue #2119) are folded in by
// processTurn's cappedTurn/failTurn helpers (workflow.go), never set by
// the CommitTurn activity itself -- checkCaps/classifyError are pure,
// I/O-free workflow code, not activities, so there is no second activity
// result type to define; a real CommitTurn invocation that neither caps
// nor fails always returns these five fields at their zero value.
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

	// Capped and CapKind report a tripped turn or cost cap (FR6/FR7):
	// SessionWorkflow's loop (workflow.go) writes session.StatusCapped
	// with CapKind in the terminal reason when Capped is true. CapKind is
	// only meaningful when Capped is true.
	Capped  bool
	CapKind session.CapKind

	// Failed, ErrorCategory, and ErrorDetail report a session-ending
	// failure (FR2/FR3): SessionWorkflow's loop writes
	// session.StatusFailed with ErrorCategory/ErrorDetail in the terminal
	// reason when Failed is true -- the exact same (category, detail)
	// pair failTurn (workflow.go) already committed to the failure
	// transcript event via classifyError, never independently
	// recomputed. ErrorCategory/ErrorDetail are only meaningful when
	// Failed is true.
	Failed        bool
	ErrorCategory session.ErrorCategory
	ErrorDetail   string
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
	// or failed transition (session.TerminalReason) -- nil for every
	// non-terminal transition (running/awaiting_input) and for done/
	// stopped, populated by workflow.go's updateSessionStatus only for
	// the capped/failed calls (issue #2119).
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

// SumCostInput is SumCost's activity input.
type SumCostInput struct {
	SessionID uuid.UUID
}

// SumCostResult is SumCost's activity result.
type SumCostResult struct {
	// CostUSD is a.Store.Usage().SumCost's committed running total (LB6,
	// FR7) -- includes every turn_usage row for the session, estimated or
	// provider-reported alike (usage.go's TurnUsage.CostEstimated doc
	// comment: "unknown cost is never treated as free").
	CostUSD float64
}

// SumCost is FR7's cost-cap input activity: a fresh read of
// a.Store.Usage().SumCost on every call, never a value cached in workflow
// state (caps.go's package doc comment: "never a separately-mutated
// counter"). SessionWorkflow's evaluateCaps (workflow.go) calls this both
// before and after each turn, then evaluates the result via caps.go's
// checkCaps.
func (a *Activities) SumCost(ctx context.Context, in SumCostInput) (SumCostResult, error) {
	if a.Store == nil {
		return SumCostResult{}, fmt.Errorf("worker: Activities.Store is nil")
	}
	total, err := a.Store.Usage().SumCost(ctx, in.SessionID)
	if err != nil {
		return SumCostResult{}, fmt.Errorf("sum cost: %w", err)
	}
	return SumCostResult{CostUSD: total}, nil
}

// CommitTerminalEventInput is CommitTerminalEvent's activity input.
// EventType must be events.EventTypeCapped or events.EventTypeFailure
// (events.go) -- this activity is the one commit path both terminal
// events share, mirroring how CommitTurn is the one commit path every
// per-turn model-response event shares.
type CommitTerminalEventInput struct {
	SessionID uuid.UUID
	Turn      int
	EventType string
	Payload   json.RawMessage
}

// CommitTerminalEventResult is CommitTerminalEvent's activity result --
// empty; the workflow only needs to know the commit succeeded before it
// writes the session's terminal status.
type CommitTerminalEventResult struct{}

// CommitTerminalEvent commits the terminal transcript event a cap trip
// (FR6/FR7) or a session failure (FR2) writes before the session's status
// write goes terminal (issue body: "Tripping either cap writes its own
// transcript event before the session goes terminal... It is committed
// and published like any other event"). SessionWorkflow calls this before
// updateSessionStatus's terminal write, never after -- a consumer reading
// the transcript must be able to see why a session ended by the time
// GetSession reports it as ended.
//
// Goes through a.Store.Transcript().AppendIfAbsent, not Append: the same
// retry-safe, publish-after-commit path (NFR2) every other transcript
// event already uses, so a re-invoked activity (Temporal's at-least-once
// execution) commits the terminal event exactly once (keyed on
// (SessionID, Turn, EventType), same as CommitTurn's own event) rather
// than duplicating it.
func (a *Activities) CommitTerminalEvent(ctx context.Context, in CommitTerminalEventInput) (CommitTerminalEventResult, error) {
	if a.Store == nil {
		return CommitTerminalEventResult{}, fmt.Errorf("worker: Activities.Store is nil")
	}
	if _, err := a.Store.Transcript().AppendIfAbsent(ctx, in.SessionID, in.Turn, in.EventType, in.Payload); err != nil {
		return CommitTerminalEventResult{}, fmt.Errorf("commit terminal event: %w", err)
	}
	return CommitTerminalEventResult{}, nil
}

// ListToolDefinitionsInput is ListToolDefinitions' activity input.
type ListToolDefinitionsInput struct {
	SessionID uuid.UUID
	// AgentID is the current turn's agent definition ID, passed through
	// like DispatchToolInput.AgentID below (persona.Issuer.Issue's doc
	// comment: a session's assignment can drift, SCD2, so this is always
	// supplied explicitly rather than re-derived from the session row).
	AgentID string
	// ToolSet is the current agent definition's tool_set
	// (session.ToolServerRef) -- ResolveAgentDefinitionResult.Definition.
	// ToolSet, unchanged.
	ToolSet []session.ToolServerRef
}

// ListToolDefinitionsResult is ListToolDefinitions' activity result.
type ListToolDefinitionsResult struct {
	// Tools is what CallModelInput.Tools (above) forwards to
	// llm.Request.Tools (FR8) -- the union of every configured server's
	// exposed tool set, converted from each server's MCP tool schema
	// (mcp.Tool.InputSchema) into llm.ToolDefinition.Parameters.
	Tools []llm.ToolDefinition
}

// ListToolDefinitions is per-turn activity #2.5 (ARCHITECTURE.md "Session
// workflow" step 3, immediately ahead of CallModel): resolves FR8's
// "which tools may this turn's model call" by connecting to every server
// in in.ToolSet and listing its exposed tools, mirroring
// whagent_net/worker/tools/dispatch.go's resolveTarget -- a fresh
// credential minted per server (FR10), never reused across servers, and
// never whagent-side-filtered against ToolServerRef.AllowedTools (C22/
// Later, dispatch.go's package doc comment "Tool selection").
//
// Implemented via whagent_net/worker/tools.ListToolDefinitions
// (listdefs.go), over a.Dispatcher.Issuer (minting) -- this activity is a
// thin activity-boundary wrapper: read in.SessionID's *session.Session
// (needed by mintCredential's sub/sub_iss/act derivation, keys.go) then
// delegate.
func (a *Activities) ListToolDefinitions(ctx context.Context, in ListToolDefinitionsInput) (ListToolDefinitionsResult, error) {
	if a.Dispatcher == nil {
		return ListToolDefinitionsResult{}, fmt.Errorf("worker: Activities.Dispatcher is nil")
	}
	if a.Store == nil {
		return ListToolDefinitionsResult{}, fmt.Errorf("worker: Activities.Store is nil")
	}

	sess, err := a.Store.Sessions().GetByID(ctx, in.SessionID)
	if err != nil {
		return ListToolDefinitionsResult{}, fmt.Errorf("list tool definitions: get session: %w", err)
	}
	if sess == nil {
		return ListToolDefinitionsResult{}, fmt.Errorf("list tool definitions: session %s not found", in.SessionID)
	}

	defs, err := tools.ListToolDefinitions(ctx, a.Dispatcher.Issuer, sess, in.AgentID, in.ToolSet)
	if err != nil {
		return ListToolDefinitionsResult{}, fmt.Errorf("list tool definitions: %w", err)
	}
	return ListToolDefinitionsResult{Tools: defs}, nil
}

// DispatchToolInput is DispatchTool's activity input.
type DispatchToolInput struct {
	SessionID uuid.UUID
	// AgentID and ToolSet are the current turn's resolved agent
	// definition fields tools.DispatchInput needs (dispatch.go) -- passed
	// through from ResolveAgentDefinitionResult the same way
	// ListToolDefinitionsInput's do above.
	AgentID string
	ToolSet []session.ToolServerRef
	// Turn and CallIndex are tools.DispatchInput's idempotency-key
	// derivation inputs (FR11) -- CallIndex is this call's 0-based
	// position within modelResult.Response.ToolCalls, stable across an
	// activity retry of the same call.
	Turn      int
	CallIndex int
	// Call is the model-requested tool call (llm.ToolCall,
	// modelResult.Response.ToolCalls[CallIndex]) to dispatch.
	Call llm.ToolCall
}

// DispatchToolResult is DispatchTool's activity result: exactly
// tools.Dispatcher.Dispatch's Result (dispatch.go) -- this activity is a
// thin activity-boundary wrapper over that already-implemented (issue
// #2118) call, not a second copy of its logic.
type DispatchToolResult struct {
	Result tools.Result
}

// DispatchTool is per-turn activity #4 (ARCHITECTURE.md "Session
// workflow" step 4): routes one model-requested tool call to its target
// domain-owned MCP server via a.Dispatcher.Dispatch (issue #2118, fully
// implemented -- see whagent_net/worker/tools/dispatch.go). Called once
// per entry of modelResult.Response.ToolCalls from processTurn
// (workflow.go), under this task's own
// workflow.GetVersion("session-workflow-tool-dispatch", ...) gate per
// that file's NFR1 doc comment.
//
// Commits an events.EventTypeToolCall transcript event for in.Call before
// dispatch (a.Store.Transcript().AppendIfAbsent, retry-safe the same way
// CommitTerminalEvent above is), looks up in.SessionID's *session.Session
// (a.Store.Sessions().GetByID) to build tools.DispatchInput and call
// a.Dispatcher.Dispatch, then commits the matching
// events.EventTypeToolResult event carrying the returned tools.Result --
// both events go through the same AppendIfAbsent path every other
// transcript event uses, so a re-invoked activity (Temporal's
// at-least-once execution) commits each exactly once rather than
// duplicating it, and a tool result's own IsError never gets reinterpreted
// as a whagent-net failure (dispatch.go's package doc comment, "isError
// is not a whagent-net failure").
//
// AppendIfAbsent's idempotency key is (session_id, turn, type) only --
// see TranscriptStore.AppendIfAbsent's doc comment -- so a turn with more
// than one tool call cannot commit two events both literally typed
// "tool_call"/"tool_result": the second AppendIfAbsent call would find the
// first call's row already present for that (session, turn, type) and
// silently return it unchanged, dropping the second call's own event.
// toolCallEventType/toolResultEventType (context.go) fold in.CallIndex
// into the stored `type` column (e.g. "tool_call:1") to keep each call's
// event distinct while remaining exactly as retry-safe per call --a
// retried DispatchTool activity for the same (session, turn, call_index)
// still dedupes correctly, since CallIndex is stable across a Temporal
// retry of the same call (DispatchToolInput's doc comment).
func (a *Activities) DispatchTool(ctx context.Context, in DispatchToolInput) (DispatchToolResult, error) {
	if a.Store == nil {
		return DispatchToolResult{}, fmt.Errorf("worker: Activities.Store is nil")
	}
	if a.Dispatcher == nil {
		return DispatchToolResult{}, fmt.Errorf("worker: Activities.Dispatcher is nil")
	}

	callPayload, err := marshalToolCallPayload(in.Call)
	if err != nil {
		return DispatchToolResult{}, fmt.Errorf("dispatch tool: marshal tool call payload: %w", err)
	}
	if _, err := a.Store.Transcript().AppendIfAbsent(ctx, in.SessionID, in.Turn, toolCallEventType(in.CallIndex), callPayload); err != nil {
		return DispatchToolResult{}, fmt.Errorf("dispatch tool: commit tool call event: %w", err)
	}

	sess, err := a.Store.Sessions().GetByID(ctx, in.SessionID)
	if err != nil {
		return DispatchToolResult{}, fmt.Errorf("dispatch tool: get session: %w", err)
	}
	if sess == nil {
		return DispatchToolResult{}, fmt.Errorf("dispatch tool: session %s not found", in.SessionID)
	}

	result, err := a.Dispatcher.Dispatch(ctx, tools.DispatchInput{
		Session:   sess,
		AgentID:   in.AgentID,
		ToolSet:   in.ToolSet,
		Turn:      in.Turn,
		CallIndex: in.CallIndex,
		Call:      in.Call,
	})
	if err != nil {
		return DispatchToolResult{}, fmt.Errorf("dispatch tool: %w", err)
	}

	resultPayload, err := marshalToolResultPayload(result)
	if err != nil {
		return DispatchToolResult{}, fmt.Errorf("dispatch tool: marshal tool result payload: %w", err)
	}
	if _, err := a.Store.Transcript().AppendIfAbsent(ctx, in.SessionID, in.Turn, toolResultEventType(in.CallIndex), resultPayload); err != nil {
		return DispatchToolResult{}, fmt.Errorf("dispatch tool: commit tool result event: %w", err)
	}

	return DispatchToolResult{Result: result}, nil
}
