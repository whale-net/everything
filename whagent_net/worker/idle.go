package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"

	"github.com/whale-net/everything/whagent_net/session"
)

// SignalArmIdleTimer is the one-time pre-deploy sweep's signal: it hands a
// session parked in awaiting_input by pre-change code its effective idle
// timeout, so the idle clock starts from the signal's arrival.
const SignalArmIdleTimer = "ArmIdleTimer"

// ActivityResolveIdleTimeout resolves a session's effective idle timeout
// once at the start of a run. Its result lands in recorded history, so
// replay never re-reads config.
const ActivityResolveIdleTimeout = "ResolveIdleTimeout"

// idleExpiryChangeID gates the ResolveIdleTimeout call at workflow start
// (NFR1). A run that predates it never resolves a timeout and only starts
// an idle clock when the pre-deploy sweep delivers one.
const idleExpiryChangeID = "session-workflow-idle-expiry"

// ArmIdleTimerSignal carries the effective idle timeout resolved outside
// workflow code, so the timer duration is recorded in workflow history.
type ArmIdleTimerSignal struct {
	EffectiveTimeout time.Duration
}

// ResolveIdleTimeoutResult is ResolveIdleTimeout's output.
type ResolveIdleTimeoutResult struct {
	EffectiveTimeout time.Duration
}

// ResolveIdleTimeout returns a session's effective idle timeout: the session
// override, else the pinned agent definition's value, else the platform
// default. It reads the same rows api's StartSession and GetSession read.
func (a *Activities) ResolveIdleTimeout(ctx context.Context, sessionID uuid.UUID) (ResolveIdleTimeoutResult, error) {
	if a.Store == nil {
		return ResolveIdleTimeoutResult{}, fmt.Errorf("worker: Activities.Store is nil")
	}
	sess, err := a.Store.Sessions().GetByID(ctx, sessionID)
	if err != nil {
		return ResolveIdleTimeoutResult{}, fmt.Errorf("resolve idle timeout: get session: %w", err)
	}
	if sess == nil {
		return ResolveIdleTimeoutResult{}, fmt.Errorf("resolve idle timeout: session %s not found", sessionID)
	}
	var def session.AgentDefinition
	assignment, err := a.Store.AgentDefinitions().CurrentAssignment(ctx, sessionID)
	if err != nil {
		return ResolveIdleTimeoutResult{}, fmt.Errorf("resolve idle timeout: get current assignment: %w", err)
	}
	if assignment != nil {
		d, err := a.Store.AgentDefinitions().GetByID(ctx, assignment.AgentDefinitionID)
		if err != nil {
			return ResolveIdleTimeoutResult{}, fmt.Errorf("resolve idle timeout: get definition: %w", err)
		}
		if d != nil {
			def = *d
		}
	}
	return ResolveIdleTimeoutResult{EffectiveTimeout: effectiveIdleTimeout(sess, def, a.platformIdleTimeout())}, nil
}

// effectiveIdleTimeout applies override > definition > platform default to
// already-loaded rows.
func effectiveIdleTimeout(sess *session.Session, def session.AgentDefinition, platformDefault time.Duration) time.Duration {
	return session.EffectiveIdleTimeout(sess.IdleTimeoutOverride, def, platformDefault)
}

// platformIdleTimeout is WHAGENT_DEFAULT_IDLE_TIMEOUT as parsed at startup,
// or session.DefaultIdleTimeout when the Activities value is unset.
func (a *Activities) platformIdleTimeout() time.Duration {
	if a.DefaultIdleTimeout > 0 {
		return a.DefaultIdleTimeout
	}
	return session.DefaultIdleTimeout
}

// idleState is the idle clock for one SessionWorkflow run. timeout is zero
// while no effective timeout is known, which keeps the clock off.
type idleState struct {
	timeout time.Duration
	armed   bool
	timer   workflow.Future
	cancel  workflow.CancelFunc
}

// adopt records the effective timeout from the pre-deploy sweep. It is a
// no-op once a timeout is known, so repeated sweeps never extend a clock.
func (s *idleState) adopt(timeout time.Duration) {
	if s.timeout == 0 && timeout > 0 {
		s.timeout = timeout
	}
}

// arm starts a full-timeout timer. Callers arm only when not already armed.
func (s *idleState) arm(ctx workflow.Context) {
	timerCtx, cancel := workflow.WithCancel(ctx)
	s.timer = workflow.NewTimer(timerCtx, s.timeout)
	s.cancel = cancel
	s.armed = true
}

// disarm cancels a pending timer so a turn starting or a stop ending the
// wait never leaves an idle clock running.
func (s *idleState) disarm() {
	if s.armed {
		s.cancel()
		s.armed = false
	}
}

// waitOutcome is what awaitInput returns to SessionWorkflow's loop.
type waitOutcome int

const (
	waitTurn waitOutcome = iota
	waitStop
	waitExpired
	// waitRearm is internal: a sweep signal arrived and the loop re-selects
	// so the clock arms with the newly known timeout.
	waitRearm
)

// awaitInput blocks in awaiting_input until a turn, a stop, or the idle
// timer fires. Stop and turn are selected alongside the timer, so event
// order decides races, and the status CAS keeps a terminal status intact.
func awaitInput(ctx workflow.Context, idle *idleState, turnCh, stopCh, armCh workflow.ReceiveChannel) (waitOutcome, SendTurnSignal) {
	for {
		if idle.timeout > 0 && !idle.armed {
			idle.arm(ctx)
		}

		var (
			outcome waitOutcome
			signal  SendTurnSignal
		)
		sel := workflow.NewSelector(ctx)
		sel.AddReceive(turnCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, &signal)
			outcome = waitTurn
		})
		sel.AddReceive(stopCh, func(c workflow.ReceiveChannel, _ bool) {
			var empty struct{}
			c.Receive(ctx, &empty)
			outcome = waitStop
		})
		sel.AddReceive(armCh, func(c workflow.ReceiveChannel, _ bool) {
			var arm ArmIdleTimerSignal
			c.Receive(ctx, &arm)
			idle.adopt(arm.EffectiveTimeout)
			outcome = waitRearm
		})
		if idle.armed {
			sel.AddFuture(idle.timer, func(_ workflow.Future) {
				outcome = waitExpired
			})
		}
		sel.Select(ctx)

		if outcome == waitRearm {
			continue
		}
		idle.disarm()
		return outcome, signal
	}
}
