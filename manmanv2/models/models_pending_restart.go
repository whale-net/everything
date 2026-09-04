package manman

import "time"

// PendingRestart is a durable, control-plane-local record of a Start that is
// pending on gating_session_id's Stop reaching a terminal status. It gives
// finishRestartInBackground's intent (manmanv2/ui/handlers_deployment_actions.go)
// a durable home instead of a goroutine stack, with at-most-one-pending-per-
// deployment enforced by the DB (see migration 036_pending_restarts).
//
// Deliberately not SCD2 (AGENTS.md § SCD2): this is a short-lived work
// intent with a terminal state machine, not dimension history, so it uses
// Status/ResolvedAt instead of valid_from/valid_to.
type PendingRestart struct {
	PendingRestartID   int64      `db:"pending_restart_id"`
	ServerGameConfigID int64      `db:"server_game_config_id"`
	GatingSessionID    int64      `db:"gating_session_id"`
	Status             string     `db:"status"`
	StallDeadline      time.Time  `db:"stall_deadline"`
	StartedSessionID   *int64     `db:"started_session_id"`
	FailureReason      *string    `db:"failure_reason"`
	CreatedAt          time.Time  `db:"created_at"`
	ResolvedAt         *time.Time `db:"resolved_at"`
}
