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
// transitions") is this convention's first real usage: the Scaffold-phase
// loop never wrote `sessions.status` at all, so this Implementation-phase
// addition of a genuinely new control-flow branch is exactly the shape
// NFR1 exists to protect -- any run already open on the old (no-op)
// behavior when this change deploys keeps taking the old branch, forever,
// for that run. The next behavior-changing edit to this file (the
// follow-up tool-dispatch task's ExecuteActivity call per tool call,
// noted at processTurn's tool-dispatch step below) must add its own change
// ID the same way.
//
// # Implementation status (issue #2114)
//
// SessionWorkflow's signal-per-turn loop, Stop/cancellation handling, and
// session-status transitions are all real as of this task -- not a
// Scaffold-phase stub. Deliberately still deferred to follow-up tasks per
// the issue body ("Tool dispatch, cap enforcement, and terminal
// classification land in follow-up tasks; this task builds the durable
// loop they hang off"): the tool-call dispatch step in processTurn stays a
// no-op hook, no turn/cost cap is checked before or after a turn, and
// CommitTurnResult.Done (activities.go) is always false, so this task's
// workflow only ever reaches `awaiting_input` or `stopped`, never `done`
// or `capped`.
package main

import (
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

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

	if err := updateSessionStatus(ctx, in.SessionID, session.StatusAwaitingInput); err != nil {
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
			return updateSessionStatus(ctx, in.SessionID, session.StatusStopped)
		}

		turn++
		if err := updateSessionStatus(ctx, in.SessionID, session.StatusRunning); err != nil {
			return err
		}

		outcome, err := runTurn(ctx, stopCh, in.SessionID, turn, signal, logger)
		if err != nil {
			return err
		}
		if outcome.stopped {
			return updateSessionStatus(ctx, in.SessionID, session.StatusStopped)
		}

		nextStatus := session.StatusAwaitingInput
		if outcome.done {
			nextStatus = session.StatusDone
		}
		if err := updateSessionStatus(ctx, in.SessionID, nextStatus); err != nil {
			return err
		}
		if outcome.done {
			return nil
		}
	}
}

// turnOutcome is runTurn's result: exactly one of done/stopped is
// meaningful when err is nil (neither is set when the turn simply
// completed and the session should keep waiting for the next signalled
// turn).
type turnOutcome struct {
	done    bool
	stopped bool
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
	return turnOutcome{done: result.commit.Done}, nil
}

// processTurn runs one turn's activity sequence (ARCHITECTURE.md "Session
// workflow"): resolve the current agent definition, build context, call
// the model, (tool-call dispatch -- a no-op hook in this task, filled in
// by the follow-up tool-dispatch task), then commit the turn.
func processTurn(ctx workflow.Context, sessionID uuid.UUID, turn int, in SendTurnSignal) (CommitTurnResult, error) {
	var resolved ResolveAgentDefinitionResult
	if err := workflow.ExecuteActivity(ctx, ActivityResolveAgentDefinition, sessionID).Get(ctx, &resolved); err != nil {
		return CommitTurnResult{}, err
	}

	var built BuildContextResult
	buildIn := BuildContextInput{
		SessionID:  sessionID,
		Turn:       turn,
		Definition: resolved.Definition,
		Input:      in.Input,
	}
	if err := workflow.ExecuteActivity(ctx, ActivityBuildContext, buildIn).Get(ctx, &built); err != nil {
		return CommitTurnResult{}, err
	}

	var modelResult CallModelResult
	callIn := CallModelInput{
		SessionID: sessionID,
		Turn:      turn,
		Model:     resolved.Model,
		EventIDs:  built.EventIDs,
	}
	if err := workflow.ExecuteActivity(ctx, ActivityCallModel, callIn).Get(ctx, &modelResult); err != nil {
		return CommitTurnResult{}, err
	}

	// Tool-call dispatch step: a no-op hook in this task (issue body,
	// "Scaffold"). The follow-up tool-dispatch task adds an
	// ExecuteActivity call per modelResult.Response.ToolCalls entry here,
	// each carrying the idempotency key and persona claim
	// (ARCHITECTURE.md "Idempotency", "Identity and auth chaining") --
	// under a workflow.GetVersion("session-workflow-tool-dispatch", ...)
	// gate per this file's NFR1 doc comment, since it changes processTurn's
	// control flow for any run already open when it deploys.

	var commitResult CommitTurnResult
	commitIn := CommitTurnInput{
		SessionID: sessionID,
		Turn:      turn,
		Model:     resolved.Model,
		EventIDs:  built.EventIDs,
		Response:  modelResult.Response,
	}
	if err := workflow.ExecuteActivity(ctx, ActivityCommitTurn, commitIn).Get(ctx, &commitResult); err != nil {
		return CommitTurnResult{}, err
	}

	return commitResult, nil
}

// updateSessionStatus is SessionWorkflow's write path for a session's
// control-plane status (`sessions.status`, ARCHITECTURE.md "Session
// workflow" step 6) -- see this file's package doc comment, "NFR1", for
// why this is gated behind workflow.GetVersion rather than called
// unconditionally.
func updateSessionStatus(ctx workflow.Context, sessionID uuid.UUID, status session.Status) error {
	v := workflow.GetVersion(ctx, "session-workflow-status-transitions", workflow.DefaultVersion, 1)
	if v == workflow.DefaultVersion {
		// Pre-existing behavior for any run whose history predates this
		// change: no status write.
		return nil
	}
	return workflow.ExecuteActivity(ctx, ActivityUpdateSessionStatus, UpdateSessionStatusInput{
		SessionID: sessionID,
		Status:    status,
	}).Get(ctx, nil)
}
