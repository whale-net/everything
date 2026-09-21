// This file (issue #2868, root plan #2851, M5's C26 escalation/
// intervention axis) is store-layer groundwork every later M5 task builds
// on -- it ships no verb of its own (that is FR2/FR3/FR7/FR8/FR9, later
// tasks #2870-#2873): the Go enums mirroring 016_escalation_axis.up.sql's
// CHECKs, the EscalationEvent/InterventionEvent models over
// task_escalation_event/task_intervention_event, and two shared
// transactional helpers a later verb calls rather than forking its own
// copy:
//
//   - recordEscalationTx inserts one task_escalation_event row and sets
//     task.current_escalation_id to it, inside a caller-supplied
//     transaction (FR2's complete path, FR3, FR8, FR9).
//   - forceCloseClaimTx closes a task's open task_claim row (if any) and
//     clears task.current_claim_id/lease_expires_at, inside a
//     caller-supplied transaction (FR7, FR8, FR9), so a heartbeat/
//     complete/abandon arriving afterward against that claim is rejected
//     exactly the way task_lease.go's Heartbeat already rejects one
//     arriving after a reclaim (ErrClaimNotCurrent).
//
// Every enum here is kept in lockstep with its DB CHECK by hand -- an
// unknown value is rejected at both layers, the same discipline
// task_note.go's NoteKind comment demands.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EscalationReason is the fixed enumeration of `task_escalation_event.
// reason` values (016_escalation_axis.up.sql's CHECK; #2851 Assumption 4:
// exactly three reasons, no others) -- an escalation is either one of the
// two automatic counter-driven reasons, or a manual one with no counter at
// all (see EscalationEvent's own doc comment on CounterValue/CapValue).
type EscalationReason string

const (
	// EscalationReasonThrashCap is FR1/FR2's lane-thrash escalation --
	// recorded automatically by a thrash-cap `complete`.
	EscalationReasonThrashCap EscalationReason = "thrash-cap"
	// EscalationReasonAttemptCap is FR3's attempt-cap escalation --
	// recorded automatically by a capped `ReclaimExpired`/`AbandonClaim`/
	// `release`.
	EscalationReasonAttemptCap EscalationReason = "attempt-cap"
	// EscalationReasonManual is FR9's operator-initiated escalation, the
	// one reason with no triggering counter or cap (Assumption 4).
	EscalationReasonManual EscalationReason = "manual"
)

// validEscalationReasons is the Go-layer half of the "unknown value
// rejected at both layers" contract; 016_escalation_axis.up.sql's CHECK is
// the DB-layer half.
var validEscalationReasons = map[EscalationReason]bool{
	EscalationReasonThrashCap:  true,
	EscalationReasonAttemptCap: true,
	EscalationReasonManual:     true,
}

// ErrUnknownEscalationReason is recordEscalationTx's named, loud rejection
// of a Reason outside EscalationReason's fixed enumeration -- checked in Go
// ahead of the INSERT, mirroring RecordNote's ErrUnknownNoteKind check
// (task_note.go).
var ErrUnknownEscalationReason = errors.New("krill/store: unknown escalation reason")

// ErrInvalidEscalationCounter is recordEscalationTx's named, loud rejection
// of a CounterValue/CapValue pairing that does not match Reason --
// EscalationReasonManual must carry neither, every other reason must carry
// both (016_escalation_axis.up.sql's own CHECK on task_escalation_event,
// mirrored here in Go ahead of the INSERT the same way ErrUnknownNoteKind
// mirrors task_note's CHECK).
var ErrInvalidEscalationCounter = errors.New("krill/store: counter_value/cap_value must both be set for an automatic escalation reason and both nil for a manual one")

func validateEscalationReason(reason EscalationReason) error {
	if !validEscalationReasons[reason] {
		return fmt.Errorf("%w: %q", ErrUnknownEscalationReason, reason)
	}
	return nil
}

// InterventionAction is the fixed enumeration of `task_intervention_event.
// action` values (016_escalation_axis.up.sql's CHECK) -- the one operator-
// driven mutation each of FR6 (requeue), FR7 (cancel), FR8 (release), and
// FR9 (manual escalate) appends exactly one row for.
type InterventionAction string

const (
	InterventionActionRequeue  InterventionAction = "requeue"
	InterventionActionCancel   InterventionAction = "cancel"
	InterventionActionRelease  InterventionAction = "release"
	InterventionActionEscalate InterventionAction = "escalate"
)

// validInterventionActions is the Go-layer half of the "unknown value
// rejected at both layers" contract for InterventionAction.
var validInterventionActions = map[InterventionAction]bool{
	InterventionActionRequeue:  true,
	InterventionActionCancel:   true,
	InterventionActionRelease:  true,
	InterventionActionEscalate: true,
}

// ErrUnknownInterventionAction is a later verb's named, loud rejection of
// an Action outside InterventionAction's fixed enumeration -- declared here
// (mirroring ErrUnknownEscalationReason/ErrUnknownNoteKind) so every FR6/
// FR7/FR8/FR9 verb validates against the same check rather than each
// forking its own.
var ErrUnknownInterventionAction = errors.New("krill/store: unknown intervention action")

// ValidateInterventionAction is InterventionAction's Go-layer enum check --
// exported so each later M5 verb (FR6-FR9, its own task) validates a
// caller-supplied action against this one definition rather than
// reimplementing the lookup.
func ValidateInterventionAction(action InterventionAction) error {
	if !validInterventionActions[action] {
		return fmt.Errorf("%w: %q", ErrUnknownInterventionAction, action)
	}
	return nil
}

// NoteLifecycleStatus is the fixed enumeration of `task_note_lifecycle_
// event.status` / `task_note.current_status` values (016_escalation_axis.
// up.sql's CHECK, FR11) -- any persona may transition a note through these
// four states.
type NoteLifecycleStatus string

const (
	NoteLifecycleStatusNoted       NoteLifecycleStatus = "noted"
	NoteLifecycleStatusCarriedOver NoteLifecycleStatus = "carried-over"
	NoteLifecycleStatusDeferred    NoteLifecycleStatus = "deferred"
	NoteLifecycleStatusClosed      NoteLifecycleStatus = "closed"
)

// validNoteLifecycleStatuses is the Go-layer half of the "unknown value
// rejected at both layers" contract for NoteLifecycleStatus.
var validNoteLifecycleStatuses = map[NoteLifecycleStatus]bool{
	NoteLifecycleStatusNoted:       true,
	NoteLifecycleStatusCarriedOver: true,
	NoteLifecycleStatusDeferred:    true,
	NoteLifecycleStatusClosed:      true,
}

// ErrUnknownNoteLifecycleStatus is a later verb's named, loud rejection of
// a Status outside NoteLifecycleStatus's fixed enumeration -- declared here
// so FR11's status-transition verb (its own task) validates against this
// one definition.
var ErrUnknownNoteLifecycleStatus = errors.New("krill/store: unknown note lifecycle status")

// ValidateNoteLifecycleStatus is NoteLifecycleStatus's Go-layer enum check,
// mirroring ValidateInterventionAction's role.
func ValidateNoteLifecycleStatus(status NoteLifecycleStatus) error {
	if !validNoteLifecycleStatuses[status] {
		return fmt.Errorf("%w: %q", ErrUnknownNoteLifecycleStatus, status)
	}
	return nil
}

// ErrTaskEscalated is ClaimTask's (task_claim.go) named rejection when the
// task's current_escalation_id is non-NULL, and recordEscalationTx's named
// rejection when a task that already has one active escalation is escalated
// again (FR9's one-event rule: at most one active escalation per task).
// FR2 is explicit this must hold "regardless of claim state" -- an
// escalated task that was never claimed, or one escalated through a normal
// complete (current_claim_id already NULL), is excluded identically.
var ErrTaskEscalated = errors.New("krill/store: task is escalated")

// ErrTaskCancelled is ClaimTask's (task_claim.go) named rejection when the
// task's cancelled_at is non-NULL (FR7's dead-letter terminal state).
var ErrTaskCancelled = errors.New("krill/store: task is cancelled")

// ErrTaskNotEscalated is a later verb's named rejection (FR6's requeue) of
// an attempt to resolve an escalation on a task that has none active --
// declared here so that verb's own task validates against this one
// definition rather than inventing its own.
var ErrTaskNotEscalated = errors.New("krill/store: task has no active escalation")

// EscalationEvent is one row of `task_escalation_event`
// (016_escalation_axis.up.sql) -- append-only, no resolution column (NFR2,
// NFR5): requeue/cancel resolve an escalation by clearing task.
// current_escalation_id and appending a task_intervention_event, never by
// rewriting this row. CounterValue/CapValue are both nil for
// EscalationReasonManual (no triggering counter) and both non-nil for
// every other Reason (FR5/FR9, the DB CHECK's own pairing rule).
type EscalationEvent struct {
	ID      uuid.UUID
	ScopeID uuid.UUID
	TaskID  uuid.UUID

	Reason       EscalationReason
	CounterValue *int
	CapValue     *int

	// LaneAtEscalation is the task's current_lane at the moment this
	// escalation was recorded -- a snapshot, never re-derived from the
	// task's (possibly since-changed) current_lane.
	LaneAtEscalation Lane

	// CreatedByActing/CreatedByOnBehalfOf are always populated (NFR3,
	// LB4) -- recordEscalationTx is the only write path onto this table,
	// and every caller passes a real subject pair.
	CreatedByActing     Subject
	CreatedByOnBehalfOf Subject
	CreatedAt           time.Time
}

// InterventionEvent is one row of `task_intervention_event`
// (016_escalation_axis.up.sql) -- append-only, one row per operator action
// (FR6-FR9). EscalationEventID is set only by a requeue action, naming the
// specific escalation it resolved (FR6); nil for every other action.
// Reason is free-text and optional -- an operator's rationale, distinct
// from EscalationEvent.Reason's fixed vocabulary.
type InterventionEvent struct {
	ID      uuid.UUID
	ScopeID uuid.UUID
	TaskID  uuid.UUID

	Action            InterventionAction
	EscalationEventID *uuid.UUID
	Reason            *string

	CreatedByActing     Subject
	CreatedByOnBehalfOf Subject
	CreatedAt           time.Time
}

const escalationEventColumns = `id, scope_id, task_id, reason, counter_value, cap_value, lane_at_escalation, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at`

func scanEscalationEvent(row pgx.Row) (EscalationEvent, error) {
	var e EscalationEvent
	var reason, laneAtEscalation string
	var actingKind, onBehalfOfKind string
	err := row.Scan(
		&e.ID, &e.ScopeID, &e.TaskID, &reason, &e.CounterValue, &e.CapValue, &laneAtEscalation,
		&e.CreatedByActing.Iss, &e.CreatedByActing.Sub, &actingKind,
		&e.CreatedByOnBehalfOf.Iss, &e.CreatedByOnBehalfOf.Sub, &onBehalfOfKind,
		&e.CreatedAt,
	)
	if err != nil {
		return EscalationEvent{}, err
	}
	e.Reason = EscalationReason(reason)
	e.LaneAtEscalation = Lane(laneAtEscalation)
	e.CreatedByActing.Kind = SubjectKind(actingKind)
	e.CreatedByOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	return e, nil
}

const interventionEventColumns = `id, scope_id, task_id, action, escalation_event_id, reason, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at`

func scanInterventionEvent(row pgx.Row) (InterventionEvent, error) {
	var e InterventionEvent
	var action string
	var actingKind, onBehalfOfKind string
	err := row.Scan(
		&e.ID, &e.ScopeID, &e.TaskID, &action, &e.EscalationEventID, &e.Reason,
		&e.CreatedByActing.Iss, &e.CreatedByActing.Sub, &actingKind,
		&e.CreatedByOnBehalfOf.Iss, &e.CreatedByOnBehalfOf.Sub, &onBehalfOfKind,
		&e.CreatedAt,
	)
	if err != nil {
		return InterventionEvent{}, err
	}
	e.Action = InterventionAction(action)
	e.CreatedByActing.Kind = SubjectKind(actingKind)
	e.CreatedByOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	return e, nil
}

// RecordEscalationParams is recordEscalationTx's input -- every later M5
// verb that escalates a task (FR2, FR3, FR8, FR9) builds one of these
// rather than writing its own INSERT.
type RecordEscalationParams struct {
	ScopeID          uuid.UUID
	TaskID           uuid.UUID
	Reason           EscalationReason
	CounterValue     *int
	CapValue         *int
	LaneAtEscalation Lane
	Acting           Subject
	OnBehalfOf       Subject
}

// recordEscalationTx inserts one task_escalation_event row and sets
// task.current_escalation_id to it, inside tx -- the one shared write path
// every later M5 verb that escalates a task (FR2, FR3, FR8, FR9) must use
// rather than forking its own copy. Refuses, with ErrTaskEscalated and
// nothing written, to escalate a task that already has an active
// escalation (FR9's one-event rule: at most one active escalation per
// task) -- the caller is expected to have already row-locked the task
// (e.g. the same `SELECT ... FOR UPDATE` ClaimTask/Heartbeat take) inside
// tx; the FOR UPDATE below is a no-op re-acquisition of that same lock on
// the same connection when so, and the sole lock taken when not.
func recordEscalationTx(ctx context.Context, tx pgx.Tx, params RecordEscalationParams) (EscalationEvent, error) {
	if err := validateEscalationReason(params.Reason); err != nil {
		return EscalationEvent{}, err
	}
	if params.Reason == EscalationReasonManual {
		if params.CounterValue != nil || params.CapValue != nil {
			return EscalationEvent{}, fmt.Errorf("%w: manual escalation must carry neither", ErrInvalidEscalationCounter)
		}
	} else if params.CounterValue == nil || params.CapValue == nil {
		return EscalationEvent{}, fmt.Errorf("%w: %q escalation must carry both", ErrInvalidEscalationCounter, params.Reason)
	}

	var currentEscalationID *uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT current_escalation_id FROM task WHERE id = $1 AND scope_id = $2 FOR UPDATE
	`, params.TaskID, params.ScopeID).Scan(&currentEscalationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return EscalationEvent{}, errParentNotFound("task", params.TaskID)
	}
	if err != nil {
		return EscalationEvent{}, fmt.Errorf("lock task: %w", err)
	}
	if currentEscalationID != nil {
		return EscalationEvent{}, fmt.Errorf("%w: task id %s already has an active escalation", ErrTaskEscalated, params.TaskID)
	}

	event, err := scanEscalationEvent(tx.QueryRow(ctx, `
		INSERT INTO task_escalation_event (
			scope_id, task_id, reason, counter_value, cap_value, lane_at_escalation,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING `+escalationEventColumns,
		params.ScopeID, params.TaskID, string(params.Reason), params.CounterValue, params.CapValue, string(params.LaneAtEscalation),
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
	))
	if err != nil {
		return EscalationEvent{}, fmt.Errorf("insert task_escalation_event: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE task SET current_escalation_id = $1 WHERE id = $2
	`, event.ID, params.TaskID); err != nil {
		return EscalationEvent{}, fmt.Errorf("update task current_escalation_id: %w", err)
	}

	return event, nil
}

// forceCloseClaimTx closes taskID's open task_claim row (released_at,
// release_reason) and clears task.current_claim_id/lease_expires_at,
// inside tx -- the one shared write path FR7 (cancel), FR8 (release), and
// FR9 (escalate) each use rather than forking their own copy, so a
// subsequent Heartbeat/CompleteTask/AbandonClaim against that claim is
// rejected with ErrClaimNotCurrent exactly the way task_lease.go's
// Heartbeat already rejects one arriving after a reclaim
// (task_reclaim.go). releaseReason is one of task_claim.release_reason's
// widened CHECK values ('release', 'cancel', 'escalate') -- a bare string,
// mirroring task_claim.go's/task_reclaim.go's own untyped release_reason
// literals rather than a new Go enum.
//
// A no-op, not an error, when taskID has no open claim (current_claim_id
// is already NULL) -- FR7/FR8/FR9 all apply whether or not the task
// happens to be claimed at the moment. Like recordEscalationTx, this
// expects the caller to have already row-locked the task inside tx.
func forceCloseClaimTx(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, releaseReason string) error {
	var currentClaimID *uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT current_claim_id FROM task WHERE id = $1 FOR UPDATE
	`, taskID).Scan(&currentClaimID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errParentNotFound("task", taskID)
		}
		return fmt.Errorf("lock task: %w", err)
	}
	if currentClaimID == nil {
		return nil
	}

	if _, err := tx.Exec(ctx, `
		UPDATE task_claim SET released_at = NOW(), release_reason = $1
		WHERE id = $2 AND released_at IS NULL
	`, releaseReason, *currentClaimID); err != nil {
		return fmt.Errorf("force-close task_claim: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE task SET current_claim_id = NULL, lease_expires_at = NULL WHERE id = $1
	`, taskID); err != nil {
		return fmt.Errorf("clear task claim state: %w", err)
	}

	return nil
}
