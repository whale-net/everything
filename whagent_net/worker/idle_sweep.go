package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/session"
)

// idleSweepPageSize bounds each awaiting_input List page the sweep reads.
const idleSweepPageSize = 100

// IdleSweepSessionLister lists sessions for the pre-deploy sweep. The sweep
// filters by State == awaiting_input and pages until exhausted.
type IdleSweepSessionLister interface {
	List(ctx context.Context, filter session.SessionFilter, page session.SessionPage) ([]*session.Session, session.PageInfo, error)
}

// IdleSweepTimeoutResolver resolves one session's effective idle timeout.
// It is the same resolution ResolveIdleTimeout performs inside a run.
type IdleSweepTimeoutResolver interface {
	ResolveIdleTimeout(ctx context.Context, sessionID uuid.UUID) (ResolveIdleTimeoutResult, error)
}

// IdleSweepSignaler delivers ArmIdleTimer to one workflow. Its signature
// matches temporal client.Client.SignalWorkflow.
type IdleSweepSignaler interface {
	SignalWorkflow(ctx context.Context, workflowID string, runID string, signalName string, arg interface{}) error
}

// IdleSweepResult counts one sweep pass's outcomes.
type IdleSweepResult struct {
	Signaled int
	Failed   int
}

// RunIdleSweep sends ArmIdleTimerSignal to every session parked in
// awaiting_input. Repeat runs are safe: the workflow's adopt ignores a
// signal once a timeout is known, so an armed clock is never extended.
// A per-session failure is counted and the sweep continues; only a failed
// list page aborts it.
func RunIdleSweep(ctx context.Context, sessions IdleSweepSessionLister, resolver IdleSweepTimeoutResolver, signaler IdleSweepSignaler, pageSize int) (IdleSweepResult, error) {
	logger := logging.Get("idle-sweep")
	awaiting := session.StatusAwaitingInput
	filter := session.SessionFilter{State: &awaiting}

	var result IdleSweepResult
	pageToken := ""
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		rows, info, err := sessions.List(ctx, filter, session.SessionPage{PageSize: pageSize, PageToken: pageToken})
		if err != nil {
			return result, fmt.Errorf("idle sweep: list awaiting_input sessions: %w", err)
		}

		for _, s := range rows {
			if err := sweepOne(ctx, s.SessionID, resolver, signaler); err != nil {
				result.Failed++
				logger.Warn("idle sweep: could not arm idle timer", "session_id", s.SessionID.String(), "error", err)
				continue
			}
			result.Signaled++
		}

		if info.NextPageToken == "" {
			break
		}
		pageToken = info.NextPageToken
	}

	logger.Info("idle sweep finished", "signaled", result.Signaled, "failed", result.Failed)
	return result, nil
}

// sweepOne resolves one session's effective timeout and signals its workflow.
// A non-positive timeout is an error: the workflow would ignore it and the
// session would stay parked without an idle clock.
func sweepOne(ctx context.Context, sessionID uuid.UUID, resolver IdleSweepTimeoutResolver, signaler IdleSweepSignaler) error {
	resolved, err := resolver.ResolveIdleTimeout(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("resolve idle timeout: %w", err)
	}
	if resolved.EffectiveTimeout <= 0 {
		return fmt.Errorf("resolved non-positive idle timeout %s", resolved.EffectiveTimeout)
	}
	signal := ArmIdleTimerSignal{EffectiveTimeout: resolved.EffectiveTimeout}
	if err := signaler.SignalWorkflow(ctx, sessionID.String(), "", SignalArmIdleTimer, signal); err != nil {
		return fmt.Errorf("signal %s: %w", SignalArmIdleTimer, err)
	}
	return nil
}
