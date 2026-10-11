package main

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/whale-net/everything/whagent_net/session"
)

// errIdleSweepNotImplemented is returned until the sweep body lands.
var errIdleSweepNotImplemented = errors.New("idle sweep: not implemented")

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
func RunIdleSweep(ctx context.Context, sessions IdleSweepSessionLister, resolver IdleSweepTimeoutResolver, signaler IdleSweepSignaler, pageSize int) (IdleSweepResult, error) {
	_, _, _, _ = ctx, sessions, resolver, signaler
	_ = pageSize
	return IdleSweepResult{}, errIdleSweepNotImplemented
}
