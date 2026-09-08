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
// calling the model, committing transcript events) lives behind the
// activity methods on *Activities (activities.go, context.go) and is
// invoked only via workflow.ExecuteActivity -- mirrors
// tools/app_registry/worker/release/workflow.go's and
// audience_score_system/worker/sync's identical discipline and
// AGENTS.md/PLAN.md's "Workflow determinism" hazard.
//
// # NFR1 -- Temporal versioning discipline
//
// Every change to this workflow's code must go through workflow.GetVersion
// (or equivalent) so a run already open across a deploy continues
// correctly under the new code rather than replaying into a
// non-determinism error. Convention for this package: one change ID per
// behavior-changing edit, named "session-workflow-<short-slug>" (e.g.
// "session-workflow-tool-dispatch" for the follow-up task that fills in
// the tool-call dispatch hook in processTurn below), added at the exact
// point in SessionWorkflow/processTurn where the new branch diverges from
// old behavior:
//
//	v := workflow.GetVersion(ctx, "session-workflow-tool-dispatch", workflow.DefaultVersion, 1)
//	if v >= 1 {
//	    // new behavior
//	} else {
//	    // old behavior, preserved for any run already open when this
//	    // change deployed
//	}
//
// This task (#2114) establishes the convention rather than exercising it:
// SessionWorkflow has no prior deployed behavior yet to preserve, so no
// GetVersion call is needed until the first behavior-changing edit lands
// (the Implementation phase's tool-dispatch hook, cap enforcement, or
// terminal classification -- all follow-up tasks per the issue body).
// Every later task that changes SessionWorkflow or processTurn's control
// flow must add one, named per the convention above.
//
// # Scaffold status
//
// SessionWorkflow's signal-per-turn loop below is NOT a stub: the
// block-in-awaiting_input / signal-driven turn dispatch / Stop-signal exit
// shape is this task's Scaffold-phase deliverable (the shape neither
// tools/app_registry/worker nor ChannelSyncWorkflow has), so
// Testing-phase work asserts against it as-is. What IS scaffold-only is
// processTurn's activity bodies (activities.go, context.go): every one is
// a no-op stub returning a zero-value result, exactly like
// audience_score_system/worker/sync's original SyncSchedule/SyncOutcomes
// scaffold -- the Implementation phase replaces each body with its real
// logic without changing this file's control flow or any activity's
// input/output shape. Likewise, Stop's cancellation semantics (FR1: an
// in-flight activity is cancelled, not drained) and the awaiting_input/
// done/stopped status writes are Implementation-phase work; this file
// establishes where they attach (the stopped branch and the end of each
// loop iteration below).
package main

import (
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
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
// on SignalSendTurn while in the workflow-equivalent of the
// awaiting_input status (the actual `sessions` row status write is
// Implementation-phase work, see the package doc comment's "Scaffold
// status" section), runs one processTurn per signalled turn, and exits
// only on a SignalStop (FR1's emergency-brake path) or an unrecoverable
// processTurn error.
//
// Bounded tasks (~100 turns) are the target; Continue-As-New is
// deliberately not used here -- see ARCHITECTURE.md "Session workflow".
func SessionWorkflow(ctx workflow.Context, in SessionWorkflowInput) error {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	logger := workflow.GetLogger(ctx)

	turnCh := workflow.GetSignalChannel(ctx, SignalSendTurn)
	stopCh := workflow.GetSignalChannel(ctx, SignalStop)

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
			// FR1: a stop cancels whatever is in flight rather than
			// draining it. Nothing is in flight in this branch (the
			// selector above only ever unblocks between turns), so there
			// is no activity to cancel here -- the Implementation phase's
			// cancellable-context wiring is inside processTurn, guarding
			// the case where Stop arrives mid-turn instead of between
			// turns. Session status -> `stopped` (session.StatusStopped)
			// is also Implementation-phase work (see the package doc
			// comment's "Scaffold status" section).
			logger.Info("session stop signal received", "session_id", in.SessionID.String(), "turn", turn)
			return nil
		}

		turn++
		if err := processTurn(ctx, in.SessionID, turn, signal); err != nil {
			return err
		}
	}
}

// processTurn runs one turn's activity sequence (ARCHITECTURE.md "Session
// workflow"): resolve the current agent definition, build context, call
// the model, (tool-call dispatch -- a no-op hook in this task, filled in
// by the follow-up tool-dispatch task), then commit the turn. Every
// activity body invoked here is a Scaffold-phase no-op stub (activities.go,
// context.go); this function fixes the call sequence and the data each
// step passes to the next, which is this task's "new shape" deliverable --
// see the package doc comment's "Scaffold status" section for exactly
// what Implementation fills in without changing this sequence.
func processTurn(ctx workflow.Context, sessionID uuid.UUID, turn int, in SendTurnSignal) error {
	var resolved ResolveAgentDefinitionResult
	if err := workflow.ExecuteActivity(ctx, ActivityResolveAgentDefinition, sessionID).Get(ctx, &resolved); err != nil {
		return err
	}

	var built BuildContextResult
	buildIn := BuildContextInput{
		SessionID:  sessionID,
		Turn:       turn,
		Definition: resolved.Definition,
		Input:      in.Input,
	}
	if err := workflow.ExecuteActivity(ctx, ActivityBuildContext, buildIn).Get(ctx, &built); err != nil {
		return err
	}

	var modelResult CallModelResult
	callIn := CallModelInput{
		SessionID: sessionID,
		Turn:      turn,
		Model:     resolved.Definition.Model,
		EventIDs:  built.EventIDs,
	}
	if err := workflow.ExecuteActivity(ctx, ActivityCallModel, callIn).Get(ctx, &modelResult); err != nil {
		return err
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
		EventIDs:  built.EventIDs,
		Response:  modelResult.Response,
	}
	if err := workflow.ExecuteActivity(ctx, ActivityCommitTurn, commitIn).Get(ctx, &commitResult); err != nil {
		return err
	}

	return nil
}
