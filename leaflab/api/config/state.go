package config

import "time"

// State is FR34.1's three distinct states a pushed config version can be
// in -- accepted, pending, rejected -- derived from device_config's
// existing accepted/acked_at/rejection_reason columns
// (leaflab/migrate/migrations/007_device_config.up.sql). There is
// deliberately no separate stored status column: DeriveState is the *only*
// place these three states are computed (this task's Validation
// criterion) -- every RPC that reports a config version's state calls it
// rather than re-deriving the same three-way branch itself.
type State string

const (
	// StatePending: acked_at IS NULL -- the board has not answered this
	// push yet. Never rendered as StateRejected (FR34.1): the two are
	// distinct return values from the same function, not a caller-side
	// inference.
	StatePending State = "pending"
	// StateAccepted: acked_at is set and device_config.accepted is true.
	StateAccepted State = "accepted"
	// StateRejected: acked_at is set and device_config.accepted is false.
	// The firmware's rejection_reason is carried separately by every
	// caller of DeriveState, verbatim -- never paraphrased or normalised
	// here or anywhere else.
	StateRejected State = "rejected"
)

// DeriveState computes a pushed config version's State from
// device_config's three existing columns:
//
//   - ackedAt == nil            -> StatePending
//   - ackedAt != nil, accepted  -> StateAccepted
//   - ackedAt != nil, !accepted -> StateRejected
//
// rejectionReason is accepted for symmetry with the three columns this
// function derives from and so every call site passes the same three
// values together, but it does not affect classification: which state
// applies is fully determined by accepted and whether ackedAt is set.
func DeriveState(accepted bool, ackedAt *time.Time, rejectionReason string) State {
	if ackedAt == nil {
		return StatePending
	}
	if accepted {
		return StateAccepted
	}
	return StateRejected
}
