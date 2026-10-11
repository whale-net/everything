package main

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"
)

// SignalArmIdleTimer is the one-time pre-deploy sweep's signal: it arms the
// idle timer on a session already parked in awaiting_input by pre-change code.
const SignalArmIdleTimer = "ArmIdleTimer"

// ArmIdleTimerSignal carries the effective idle timeout resolved outside
// workflow code, so the timer duration is recorded in workflow history.
type ArmIdleTimerSignal struct {
	EffectiveTimeout time.Duration
}

// awaitIdleExpiry is the idle clock scaffold: it blocks for timeout while the
// session is in awaiting_input and reports whether the timer fired. Not wired yet.
func awaitIdleExpiry(ctx workflow.Context, sessionID uuid.UUID, timeout time.Duration) (bool, error) {
	_ = ctx
	_ = sessionID
	_ = timeout
	return false, errors.New("awaitIdleExpiry: not implemented")
}
