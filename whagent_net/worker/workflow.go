// Package main is whagent-net's Temporal worker (issue #2114;
// ARCHITECTURE.md "Session workflow", "Component map"): one long-lived
// SessionWorkflow execution per session, started by whagent_net/api's
// StartSession with workflow ID == session ID (LB2), plus the per-turn
// activities it drives. Unlike tools/app_registry/worker (release,
// writeback, outbox each their own subpackage) this worker hosts exactly
// one workflow, so main.go/workflow.go/activities.go/context.go all live
// flat in this one package rather than splitting into a workflow-specific
// subpackage -- there is nothing else here for a subpackage boundary to
// protect yet.
//
// # Determinism
//
// SessionWorkflow is replayed by the Temporal SDK, so its code must be
// deterministic: no direct network/disk I/O, no time.Now(), nothing that
// could produce a different result on replay than it did the first time.
// Every side effect (resolving the agent definition, building context,
// calling the model, committing transcript events, writing session
// status) lives behind the activity methods on *Activities (activities.go,
// context.go) and is invoked only via workflow.ExecuteActivity -- mirrors
// tools/app_registry/worker/release/workflow.go's and
// audience_score_system/worker/sync's identical discipline and
// AGENTS.md/PLAN.md's "Workflow determinism" hazard. workflow.WithCancel/
// workflow.Go/workflow.NewSelector/workflow.NewBufferedChannel (below) are
// all part of the Temporal SDK's deterministic workflow API -- safe to use
// here for exactly that reason, unlike a raw goroutine/channel/select.
//
// # NFR1 -- Temporal versioning discipline
//
// Every change to this workflow's code must go through workflow.GetVersion
// (or equivalent) so a run already open across a deploy continues
// correctly under the new code rather than replaying into a
// non-determinism error (see ARCHITECTURE.md "Workflow versioning
// (NFR1)"). Convention for this package: one change ID per
// behavior-changing edit, named "session-workflow-<short-slug>", added at
// the exact point in SessionWorkflow/processTurn where the new branch
// diverges from old behavior:
//
//	v := workflow.GetVersion(ctx, "session-workflow-<slug>", workflow.DefaultVersion, 1)
//	if v >= 1 {
//	    // new behavior
//	} else {
//	    // old behavior, preserved for any run already open when this
//	    // change deployed
//	}
//
// updateSessionStatus below (change ID "session-workflow-status-
// transitions") was this convention's first real usage (issue #2114): the
// Scaffold-phase loop never wrote `sessions.status` at all, so that
// Implementation-phase addition of a genuinely new control-flow branch was
// exactly the shape NFR1 exists to protect. issue #2119 (turn/cost caps,
// terminal classification, failure events) is the second: every new
// branch it adds to processTurn -- the pre/post cap checks (evaluateCaps)
// and the classify-and-commit failure path (failTurn) -- lives behind one
// shared change ID, "session-workflow-cap-enforcement", gated at the top
// of processTurn, per this task's own scope note (one change ID per task
// here, not per individual branch, since every branch this task adds
// ships in the same deploy and a run open across that deploy must keep
// taking the pre-#2119 path for all of them uniformly, not some subset).
// The next behavior-changing edit to this file (the follow-up
// tool-dispatch task's ExecuteActivity call per tool call, noted at
// processTurn's tool-dispatch step below) must add its own change ID the
// same way.
//
// # Implementation status (issues #2114, #2119, #2121)
//
// SessionWorkflow's signal-per-turn loop, Stop/cancellation handling,
// session-status transitions, turn/cost cap enforcement (FR6/FR7), and
// failure classification (FR2/FR3) are all real. Still deferred to issue
// #2121's Implementation phase (this file's Scaffold-phase task adds the
// ListToolDefinitions/DispatchTool activities, activities.go, but does not
// yet call either from processTurn): the tool-call dispatch step in
// processTurn stays a no-op hook, and CommitTurnResult.Done
// (activities.go) is always false (no task yet teaches CommitTurn to
// recognize a real agent-initiated finish signal), so this workflow can
// reach `awaiting_input`, `stopped`, `capped`, or `failed`, but never
// `done`.
package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/session"
)

// TaskQueue is the Temporal task queue SessionWorkflow and its activities
// are registered/dispatched on. A constant (not env-configurable) because
// nothing outside this binary needs to override it -- api starts/signals
// workflows on this same queue name, not via TEMPORAL_TASK_QUEUE (see
// libs/go/temporal's Config.TaskQueue doc comment for the same
// distinction tools/app_registry/worker/writeback.TaskQueue draws).
const TaskQueue = "whagent-net-session"

// Signal name constants. SendTurn (FR1) delivers one turn's user input to
// a running SessionWorkflow; Stop (FR1) requests immediate, best-effort
// interruption of whatever the session is doing right now.
const (
	SignalSendTurn = "SendTurn"
	SignalStop     = "Stop"
)

// SessionWorkflowInput is SessionWorkflow's single argument. SessionID is
// carried explicitly (rather than read back from workflow.GetInfo) so
// activities and processTurn depend on a plain value, not the Temporal SDK
// context -- the same "activities depend on the interface, not on
// Temporal-specific state" discipline the package doc comment's
// Determinism section describes for I/O.
type SessionWorkflowInput struct {
	SessionID uuid.UUID
}

// SendTurnSignal is SendTurn's signal payload -- mirrors
// whagent_net/protos/session.proto's SendTurnRequest.input field (the only
// caller-supplied field besides session_id, which is already the workflow
// ID and so is never re-sent in the signal itself).
type SendTurnSignal struct {
	Input string
}

// defaultActivityOptions applies to every activity SessionWorkflow
// executes, unless overridden per call site. MaximumAttempts is bounded --
// no infinite retry -- matching every other workflow precedent in this
// repo (tools/app_registry/worker/writeback, audience_score_system/worker/
// sync).
var defaultActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 2 * time.Minute,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts: 5,
	},
}

// SessionWorkflow is the long-lived, one-per-session loop (LB2,
// ARCHITECTURE.md "Session workflow"): started by api.StartSession with
// workflow ID == in.SessionID, it never returns between turns. It blocks
// on SignalSendTurn/SignalStop while in the `awaiting_input` status,
// writes `running` for the duration of a signalled turn (runTurn), then
// writes `awaiting_input` again (or `done`, once a future task teaches
// CommitTurn real terminal classification) once the turn finishes. It
// exits, writing `stopped`, on a SignalStop (FR1's emergency-brake path,
// whether it arrives between turns or cancels an in-flight turn) or an
// unrecoverable processTurn error.
//
// Bounded tasks (~100 turns) are the target; Continue-As-New is
// deliberately not used here -- see ARCHITECTURE.md "Session workflow".
func SessionWorkflow(ctx workflow.Context, in SessionWorkflowInput) error {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	logger := workflow.GetLogger(ctx)

	turnCh := workflow.GetSignalChannel(ctx, SignalSendTurn)
	stopCh := workflow.GetSignalChannel(ctx, SignalStop)

	if err := updateSessionStatus(ctx, in.SessionID, session.StatusAwaitingInput, nil); err != nil {
		return err
	}

	turn := 0
	for {
		var signal SendTurnSignal
		stopped := false

		sel := workflow.NewSelector(ctx)
		sel.AddReceive(turnCh, func(c workflow.ReceiveChannel, more bool) {
			c.Receive(ctx, &signal)
		})
		sel.AddReceive(stopCh, func(c workflow.ReceiveChannel, more bool) {
			var empty struct{}
			c.Receive(ctx, &empty)
			stopped = true
		})
		sel.Select(ctx)

		if stopped {
			// Nothing is in flight in this branch (the selector above only
			// ever unblocks between turns, where the session is already
			// idle) -- writing `stopped` is the only work left to do.
			logger.Info("session stop signal received while awaiting input", "session_id", in.SessionID.String(), "turn", turn)
			return updateSessionStatus(ctx, in.SessionID, session.StatusStopped, nil)
		}

		turn++
		if err := updateSessionStatus(ctx, in.SessionID, session.StatusRunning, nil); err != nil {
			return err
		}

		outcome, err := runTurn(ctx, stopCh, in.SessionID, turn, signal, logger)
		if err != nil {
			return err
		}
		if outcome.stopped {
			return updateSessionStatus(ctx, in.SessionID, session.StatusStopped, nil)
		}
		if outcome.failed {
			// FR2/FR3: category/detail are exactly what failTurn
			// (processTurn) already classified and committed to the
			// failure transcript event -- never recomputed here, so
			// GetSession and that event always agree.
			category, detail := outcome.errorCategory, outcome.errorDetail
			return updateSessionStatus(ctx, in.SessionID, session.StatusFailed, &session.TerminalReason{
				ErrorCategory: &category,
				ErrorDetail:   &detail,
			})
		}
		if outcome.capped {
			// FR6/FR7: capKind is exactly what cappedTurn (processTurn)
			// already committed to the capped transcript event.
			capKind := outcome.capKind
			return updateSessionStatus(ctx, in.SessionID, session.StatusCapped, &session.TerminalReason{
				CapKind: &capKind,
			})
		}

		nextStatus := session.StatusAwaitingInput
		if outcome.done {
			nextStatus = session.StatusDone
		}
		if err := updateSessionStatus(ctx, in.SessionID, nextStatus, nil); err != nil {
			return err
		}
		if outcome.done {
			return nil
		}
	}
}

// turnOutcome is runTurn's result: at most one of done/stopped/failed/
// capped is ever set when err is nil (none set means the turn simply
// completed and the session should keep waiting for the next signalled
// turn). errorCategory/errorDetail are only meaningful when failed is
// true; capKind is only meaningful when capped is true.
type turnOutcome struct {
	done    bool
	stopped bool
	failed  bool
	capped  bool

	errorCategory session.ErrorCategory
	errorDetail   string
	capKind       session.CapKind
}

// turnGoroutineResult carries processTurn's result across the
// workflow.Go coroutine boundary in runTurn.
type turnGoroutineResult struct {
	commit CommitTurnResult
	err    error
}

// runTurn runs processTurn under a cancellable child context, racing it
// against stopCh so a Stop signal that arrives mid-turn interrupts
// whatever activity is currently in flight rather than waiting for it to
// finish (FR1: "an in-flight tool call or in-flight model call is
// cancelled, not allowed to run to completion"). This is the
// "cancellable-context wiring... guarding the case where Stop arrives
// mid-turn instead of between turns" the package doc comment's
// Implementation-status section describes.
func runTurn(ctx workflow.Context, stopCh workflow.ReceiveChannel, sessionID uuid.UUID, turn int, signal SendTurnSignal, logger log.Logger) (turnOutcome, error) {
	turnCtx, cancelTurn := workflow.WithCancel(ctx)
	defer cancelTurn()

	resultCh := workflow.NewBufferedChannel(ctx, 1)
	workflow.Go(turnCtx, func(gctx workflow.Context) {
		commitResult, err := processTurn(gctx, sessionID, turn, signal)
		resultCh.Send(gctx, turnGoroutineResult{commit: commitResult, err: err})
	})

	var result turnGoroutineResult
	stopped := false
	sel := workflow.NewSelector(ctx)
	sel.AddReceive(resultCh, func(c workflow.ReceiveChannel, more bool) {
		c.Receive(ctx, &result)
	})
	sel.AddReceive(stopCh, func(c workflow.ReceiveChannel, more bool) {
		var empty struct{}
		c.Receive(ctx, &empty)
		stopped = true
	})
	sel.Select(ctx)

	if stopped {
		logger.Info("session stop signal received mid-turn; cancelling in-flight work", "session_id", sessionID.String(), "turn", turn)
		cancelTurn()

		// Drain the coroutine's result: workflow.Go coroutines must run to
		// completion (here, observe the cancellation and return) before
		// this workflow execution can safely proceed, and every branch
		// below expects resultCh to have exactly one pending send. The
		// error (if any) is expected to be a cancellation propagated up
		// from whichever activity was in flight and is deliberately not
		// surfaced: FR1's stop is a controlled shutdown, not a workflow
		// failure. Whatever processTurn had already durably committed
		// before the cancellation landed stays exactly as committed --
		// nothing is rolled back, and nothing further is fabricated.
		drain := workflow.NewSelector(ctx)
		drain.AddReceive(resultCh, func(c workflow.ReceiveChannel, more bool) {
			c.Receive(ctx, &result)
		})
		drain.Select(ctx)
		return turnOutcome{stopped: true}, nil
	}

	if result.err != nil {
		return turnOutcome{}, result.err
	}
	return turnOutcome{
		done:          result.commit.Done,
		failed:        result.commit.Failed,
		capped:        result.commit.Capped,
		errorCategory: result.commit.ErrorCategory,
		errorDetail:   result.commit.ErrorDetail,
		capKind:       result.commit.CapKind,
	}, nil
}

// processTurn runs one turn's activity sequence (ARCHITECTURE.md "Session
// workflow"): resolve the current agent definition, check caps (FR6/FR7,
// "before" half), build context, call the model, (tool-call dispatch -- a
// no-op hook in this task, filled in by the follow-up tool-dispatch
// task), commit the turn, check caps again ("after" half), then return.
// Any activity error along the way is routed to failTurn (FR2/FR3)
// instead of propagating as a raw workflow error -- see failTurn's doc
// comment for why.
//
// issue #2119's change ID ("session-workflow-cap-enforcement", this
// file's package doc comment "NFR1") gates every branch below that did
// not exist before this task: for a run already open when this change
// deploys, v == workflow.DefaultVersion and processTurn takes exactly the
// pre-#2119 path -- no cap check, and an activity error still propagates
// as a raw error (the old, pre-failTurn behavior) rather than writing a
// failed status.
func processTurn(ctx workflow.Context, sessionID uuid.UUID, turn int, in SendTurnSignal) (CommitTurnResult, error) {
	v := workflow.GetVersion(ctx, "session-workflow-cap-enforcement", workflow.DefaultVersion, 1)

	var resolved ResolveAgentDefinitionResult
	if err := workflow.ExecuteActivity(ctx, ActivityResolveAgentDefinition, sessionID).Get(ctx, &resolved); err != nil {
		if v == workflow.DefaultVersion {
			return CommitTurnResult{}, err
		}
		return failTurn(ctx, sessionID, turn, err)
	}

	if v >= 1 {
		// "Before" half of FR6/FR7's "before each turn and after each LLM
		// response" (ARCHITECTURE.md "Guardrails"): evaluated against
		// turn-1 (turns already completed, not this one), so the turn
		// that actually reaches a cap is still allowed to run once and
		// produce its own transcript event -- see evaluateCaps' doc
		// comment for the full reasoning. In ordinary operation this
		// never trips (the "after" check on the prior turn already ended
		// the session and this workflow's own loop never calls
		// processTurn again once terminal); it exists as defense in
		// depth against a resumed/replayed run whose prior "after" check
		// committed the cap event but was interrupted before the session
		// status write landed.
		check, err := evaluateCaps(ctx, sessionID, turn-1, resolved.Definition)
		if err != nil {
			return failTurn(ctx, sessionID, turn, err)
		}
		if check.Capped {
			return cappedTurn(ctx, sessionID, turn, check.CapKind)
		}
	}

	var built BuildContextResult
	buildIn := BuildContextInput{
		SessionID:  sessionID,
		Turn:       turn,
		Definition: resolved.Definition,
		Input:      in.Input,
	}
	if err := workflow.ExecuteActivity(ctx, ActivityBuildContext, buildIn).Get(ctx, &built); err != nil {
		if v == workflow.DefaultVersion {
			return CommitTurnResult{}, err
		}
		return failTurn(ctx, sessionID, turn, err)
	}

	var modelResult CallModelResult
	callIn := CallModelInput{
		SessionID: sessionID,
		Turn:      turn,
		Model:     resolved.Model,
		EventIDs:  built.EventIDs,
	}
	if err := workflow.ExecuteActivity(ctx, ActivityCallModel, callIn).Get(ctx, &modelResult); err != nil {
		if v == workflow.DefaultVersion {
			return CommitTurnResult{}, err
		}
		return failTurn(ctx, sessionID, turn, err)
	}

	// Tool-call dispatch step: a no-op hook as of this Scaffold-phase task
	// (issue #2121). Implementation phase adds an ActivityDispatchTool
	// ExecuteActivity call (activities.go) per modelResult.Response.
	// ToolCalls entry here, each carrying the idempotency key and persona
	// claim (ARCHITECTURE.md "Idempotency", "Identity and auth chaining")
	// -- under a workflow.GetVersion("session-workflow-tool-dispatch", ...)
	// gate per this file's NFR1 doc comment, since it changes processTurn's
	// control flow for any run already open when it deploys. The same
	// change also adds an ActivityListToolDefinitions call ahead of
	// ActivityCallModel above, populating CallModelInput.Tools (FR8) so
	// the model has something to request tool calls against in the first
	// place.

	var commitResult CommitTurnResult
	commitIn := CommitTurnInput{
		SessionID: sessionID,
		Turn:      turn,
		Model:     resolved.Model,
		EventIDs:  built.EventIDs,
		Response:  modelResult.Response,
	}
	if err := workflow.ExecuteActivity(ctx, ActivityCommitTurn, commitIn).Get(ctx, &commitResult); err != nil {
		if v == workflow.DefaultVersion {
			return CommitTurnResult{}, err
		}
		return failTurn(ctx, sessionID, turn, err)
	}

	if v >= 1 {
		// "After" half of the same FR6/FR7 check: this turn's own cost
		// (just committed above) is now included in SumCost's running
		// total, so this is where a turn or cost cap this turn itself
		// reaches actually gets caught, ending the session immediately
		// rather than waiting for a further signal that will never come
		// (Testing phase's "does not process a further signalled turn").
		check, err := evaluateCaps(ctx, sessionID, turn, resolved.Definition)
		if err != nil {
			return failTurn(ctx, sessionID, turn, err)
		}
		if check.Capped {
			return cappedTurn(ctx, sessionID, turn, check.CapKind)
		}
	}

	return commitResult, nil
}

// evaluateCaps reads FR7's cost-cap input fresh (the SumCost activity;
// caps.go's package doc comment: "never a separately-mutated counter")
// and evaluates it, together with turnForCapCheck, against def via
// checkCaps (caps.go). processTurn calls this twice per turn with two
// different turnForCapCheck values, both named "the turn number just
// reached" by checkCaps' own doc comment:
//
//   - the "before" call passes turn-1 (this turn has not run yet, so the
//     turns/cost "just reached" are whatever the previous turn left
//     committed);
//   - the "after" call passes turn (this turn's own commit has already
//     landed by the time it runs).
//
// Both calls read SumCost fresh rather than sharing one read, since the
// "after" call's whole point is to observe this turn's own
// just-committed cost, which the "before" call's read necessarily
// predates.
func evaluateCaps(ctx workflow.Context, sessionID uuid.UUID, turnForCapCheck int, def session.AgentDefinition) (capCheck, error) {
	var sum SumCostResult
	if err := workflow.ExecuteActivity(ctx, ActivitySumCost, SumCostInput{SessionID: sessionID}).Get(ctx, &sum); err != nil {
		return capCheck{}, err
	}
	return checkCaps(turnForCapCheck, def, sum.CostUSD)
}

// cappedTurn is processTurn's shared cap-trip terminal path (FR6/FR7):
// commits the capped transcript event (CommitTerminalEvent,
// AppendIfAbsent-backed and therefore safe to retry) carrying capKind,
// then returns a CommitTurnResult reporting Capped so SessionWorkflow's
// loop (workflow.go) writes the session's `capped` status with the same
// capKind in its terminal reason -- the event is committed before that
// status write, per the issue body ("Tripping either cap writes its own
// transcript event before the session goes terminal").
func cappedTurn(ctx workflow.Context, sessionID uuid.UUID, turn int, capKind session.CapKind) (CommitTurnResult, error) {
	payload, err := marshalCappedEvent(capKind)
	if err != nil {
		return CommitTurnResult{}, fmt.Errorf("marshal capped event payload: %w", err)
	}
	if err := workflow.ExecuteActivity(ctx, ActivityCommitTerminalEvent, CommitTerminalEventInput{
		SessionID: sessionID,
		Turn:      turn,
		EventType: events.EventTypeCapped,
		Payload:   payload,
	}).Get(ctx, nil); err != nil {
		return CommitTurnResult{}, err
	}
	return CommitTurnResult{Capped: true, CapKind: capKind}, nil
}

// failureEventPayload is EventTypeFailure's transcript-event payload
// (FR2). ErrorCategory/ErrorDetail are exactly the pair classifyError
// (classify.go) produced -- the same pair failTurn folds into the
// CommitTurnResult SessionWorkflow's loop uses for the session's
// error_category/error_detail columns (FR3), never independently
// recomputed.
type failureEventPayload struct {
	ErrorCategory session.ErrorCategory `json:"error_category"`
	ErrorDetail   string                `json:"error_detail"`
}

// failTurn is processTurn's shared terminal-failure path (FR2/FR3):
// classifies cause exactly once via classifyError, commits the failure
// transcript event carrying that classification (CommitTerminalEvent,
// AppendIfAbsent-backed and therefore safe to retry), and returns a
// CommitTurnResult reporting Failed so SessionWorkflow's loop writes the
// session's `failed` status with the same category/detail -- one
// classification, two surfaces (issue body), computed exactly once here,
// never independently derived a second time for the status write.
//
// Returns a nil error deliberately: a session ending `failed` is a
// legitimate terminal business outcome this workflow execution completes
// normally over, not a Temporal workflow execution failure --
// SessionWorkflow itself still finishes (returns nil, same as the done/
// stopped/capped paths) once the session's terminal status is durably
// written. Only commitTerminalEvent's own activity failure (i.e.
// whagent-net cannot even record why the session failed) propagates a
// real error here, since there is nothing safe to report in that case.
func failTurn(ctx workflow.Context, sessionID uuid.UUID, turn int, cause error) (CommitTurnResult, error) {
	category, detail := classifyError(cause)

	payload, err := json.Marshal(failureEventPayload{ErrorCategory: category, ErrorDetail: detail})
	if err != nil {
		return CommitTurnResult{}, fmt.Errorf("marshal failure event payload: %w", err)
	}
	if err := workflow.ExecuteActivity(ctx, ActivityCommitTerminalEvent, CommitTerminalEventInput{
		SessionID: sessionID,
		Turn:      turn,
		EventType: events.EventTypeFailure,
		Payload:   payload,
	}).Get(ctx, nil); err != nil {
		return CommitTurnResult{}, err
	}

	return CommitTurnResult{Failed: true, ErrorCategory: category, ErrorDetail: detail}, nil
}

// updateSessionStatus is SessionWorkflow's write path for a session's
// control-plane status (`sessions.status`, ARCHITECTURE.md "Session
// workflow" step 6) -- see this file's package doc comment, "NFR1", for
// why this is gated behind workflow.GetVersion rather than called
// unconditionally. terminal carries cap_kind/error_category/error_detail
// for a capped or failed status (nil for every other status, issue
// #2119) -- passed straight through to UpdateSessionStatusInput.Terminal,
// which the underlying compare-and-swap (session.SessionStore.UpdateStatus)
// applies only when status is itself terminal.
func updateSessionStatus(ctx workflow.Context, sessionID uuid.UUID, status session.Status, terminal *session.TerminalReason) error {
	v := workflow.GetVersion(ctx, "session-workflow-status-transitions", workflow.DefaultVersion, 1)
	if v == workflow.DefaultVersion {
		// Pre-existing behavior for any run whose history predates this
		// change: no status write.
		return nil
	}
	return workflow.ExecuteActivity(ctx, ActivityUpdateSessionStatus, UpdateSessionStatusInput{
		SessionID: sessionID,
		Status:    status,
		Terminal:  terminal,
	}).Get(ctx, nil)
}
