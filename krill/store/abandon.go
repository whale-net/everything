// This file (issue #2688, FR6, C13) is krill's abandon verb -- the
// delivery-axis "stop committing to this" primitive composing three
// pieces #2685-#2687 each shipped independently: the append-only
// `abandoned` status value (milestone_status.go's RecordTransition), the
// not-yet-shipped definition (delivery_shipment.go's DeliveryBreakdown),
// and the backlog bucket plus scope-move primitive (recut.go's
// GetOrCreateBacklog/MoveScope). Abandon is the one place in this package
// that opens a single transaction spanning all three, reusing each one's
// own transaction-scoped core (recordTransitionTx, deliveryBreakdown,
// getOrCreateBacklogTx, moveScopeTx) rather than a second copy of any of
// their logic: resolving the container, rejecting a double-abandon,
// sweeping its not-yet-shipped scope to the backlog bucket, and appending
// the `abandoned` transition, so a caller (or a crash) can never observe
// the status set without the sweep, or the sweep without the status --
// exactly the two partial-abandon shapes NFR3 rules out.
//
// ============================================================================
// Cascade: abandoning a milestone abandons every one of its milepebbles too
// ============================================================================
// A milestone's milepebbles are sub-commitments of the same outcome
// (migration 011, issue #2684, FR3) -- once the parent is no longer being
// pursued, none of its milepebbles are either. Abandon therefore cascades:
// abandoning a milestone with live milepebbles abandons each one in the
// same transaction, in the same call, each getting its own `abandoned`
// MilestoneStatusEvent row (so FR9/FR12's per-container history stays
// accurate for every affected container) and its own not-yet-shipped
// sweep into the same backlog bucket the parent's own sweep used (a
// milepebble always shares its parent's product, migration 011).
//
// If any milepebble under the milestone is already abandoned, the whole
// call is rejected (ErrAlreadyAbandoned) rather than silently skipped --
// consistent with the double-abandon check this file applies to the
// top-level target. A milestone with an already-abandoned milepebble must
// be dealt with explicitly (the caller can inspect status first via
// MilestoneStatusEventStore.CurrentStatus/CurrentStatuses) rather than
// have the cascade quietly decide what to leave alone.
//
// Abandoning a milepebble directly (not via cascade) is the same
// operation against a narrower target: its own not-yet-shipped subset of
// its own Delivers association, with no further cascade -- a milepebble
// has no children of its own.
//
// ============================================================================
// Not reversible: truncate, never rewind
// ============================================================================
// There is no un-abandon verb in this milestone. A revived commitment is
// a new milestone or milepebble; the backlog's scope re-cuts into it via
// RecutStore.MoveScope (recut.go) -- the documented recovery path. Adding
// an "un-abandon" later would have to decide which of the swept items to
// pull back into which container, a decision this package deliberately
// leaves to a fresh MoveScope call rather than an inverse of this one.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AbandonStore covers the abandon verb (issue #2688, FR6) -- composing
// MilestoneStatusEventStore, DeliveryShipmentStore, and RecutStore into
// one atomic operation.
type AbandonStore interface {
	// Abandon marks containerID (a milestone or milepebble) `abandoned`
	// (MilestoneStatusEventStore's append-only history) and, in the same
	// transaction, sweeps its not-yet-shipped `delivers` scope
	// (DeliveryShipmentStore.DeliveryBreakdown's unshipped half) into its
	// product's backlog bucket (RecutStore.GetOrCreateBacklog/MoveScope).
	// Shipped scope is left exactly where it is (NFR3) -- an abandoned
	// container remains a readable record of what it shipped before it
	// was abandoned; no `delivery_shipment` row is ever touched.
	//
	// Rejects, writing nothing, if:
	//   - containerID names the backlog bucket itself
	//     (ErrCannotAbandonBacklog) -- the bucket is a move destination,
	//     never a container that can itself be abandoned;
	//   - containerID's current status (MilestoneStatusEventStore.
	//     CurrentStatus's own derivation) is already `abandoned`
	//     (ErrAlreadyAbandoned) -- loudly, so a double-abandon is visible
	//     rather than a silent second sweep;
	//   - containerID is a milestone with a milepebble that is already
	//     abandoned (ErrAlreadyAbandoned) -- see this file's own cascade
	//     note for why that is rejected rather than silently skipped.
	//
	// Cascade (see this file's package doc comment): abandoning a
	// milestone also abandons every one of its milepebbles, each with its
	// own not-yet-shipped sweep and its own `abandoned` transition row,
	// in the same transaction. Abandoning a milepebble directly cascades
	// no further (a milepebble has no children).
	//
	// Not reversible (see this file's own note): there is no un-abandon
	// verb. Reviving scope means re-cutting it out of the backlog bucket
	// via RecutStore.MoveScope into a new or different container.
	Abandon(ctx context.Context, scopeID, containerID uuid.UUID, note *string, acting, onBehalfOf Subject) (AbandonResult, error)
}

// abandonStore is the pgx-backed AbandonStore implementation.
type abandonStore struct{ pool *pgxpool.Pool }

var _ AbandonStore = abandonStore{}

// ErrCannotAbandonBacklog is Abandon's named rejection when containerID is
// the backlog bucket itself (MilestoneKindBacklog) -- the bucket is
// Abandon's own move destination, never a container abandon can target.
var ErrCannotAbandonBacklog = errors.New("krill/store: the backlog bucket itself cannot be abandoned")

// ErrAlreadyAbandoned is Abandon's named rejection when containerID's (or,
// under cascade, one of its milepebbles') current status is already
// `abandoned` -- a double-abandon is rejected loudly rather than silently
// re-running the sweep a second time.
var ErrAlreadyAbandoned = errors.New("krill/store: container is already abandoned")

// AbandonResult reports what one Abandon call actually did to
// ContainerID (FR6): the entity ids it swept into the backlog bucket (the
// not-yet-shipped half of DeliveryBreakdown), the entity ids it left
// exactly where they were (the shipped half, NFR3), and the status
// transition it appended. MilepebbleResults is populated only for a
// milestone-kind ContainerID that had live milepebbles cascaded (this
// file's own cascade note) -- one entry per milepebble, each never
// nesting further (a milepebble has no children of its own).
type AbandonResult struct {
	ContainerID       uuid.UUID
	BacklogID         uuid.UUID
	MovedToBacklogIDs []uuid.UUID
	ShippedIDs        []uuid.UUID
	StatusEvent       MilestoneStatusEvent
	MilepebbleResults []AbandonResult
}

func (s abandonStore) Abandon(ctx context.Context, scopeID, containerID uuid.UUID, note *string, acting, onBehalfOf Subject) (AbandonResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AbandonResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	result, kind, err := abandonContainerTx(ctx, tx, scopeID, containerID, note, acting, onBehalfOf)
	if err != nil {
		return AbandonResult{}, err
	}

	// Cascade (see this file's package doc comment): a milestone's
	// milepebbles are abandoned too, each in this same transaction, each
	// getting its own not-yet-shipped sweep and its own `abandoned`
	// transition row. A milepebble target never reaches here -- it has no
	// children to cascade into.
	if kind == MilestoneKindMilestone {
		milepebbles, err := listMilepebblesByMilestone(ctx, tx, containerID)
		if err != nil {
			return AbandonResult{}, err
		}
		for _, mp := range milepebbles {
			childResult, _, err := abandonContainerTx(ctx, tx, scopeID, mp.ID, note, acting, onBehalfOf)
			if err != nil {
				return AbandonResult{}, fmt.Errorf("cascade abandon milepebble %s: %w", mp.ID, err)
			}
			result.MilepebbleResults = append(result.MilepebbleResults, childResult)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return AbandonResult{}, fmt.Errorf("commit: %w", err)
	}
	return result, nil
}

// abandonContainerTx is Abandon's single-container core, shared by the
// top-level target and, via Abandon's cascade loop, every milepebble
// cascaded from it: resolve, reject a backlog target or a double-abandon,
// compute the not-yet-shipped scope, sweep it into the backlog bucket,
// then append the `abandoned` transition -- all inside tx, the one
// transaction Abandon opens. Returns the resolved container's Kind
// alongside its AbandonResult so the caller can decide whether to cascade
// without a second lookup.
func abandonContainerTx(ctx context.Context, tx pgx.Tx, scopeID, containerID uuid.UUID, note *string, acting, onBehalfOf Subject) (AbandonResult, MilestoneKind, error) {
	ref, err := getMilestoneRefTx(ctx, tx, scopeID, containerID)
	if err != nil {
		return AbandonResult{}, "", err
	}
	if ref.Kind == MilestoneKindBacklog {
		return AbandonResult{}, "", fmt.Errorf("%w: milestone_ref id %s", ErrCannotAbandonBacklog, containerID)
	}

	status, err := currentStatusTx(ctx, tx, containerID)
	if err != nil {
		return AbandonResult{}, "", err
	}
	if status == MilestoneStatusAbandoned {
		return AbandonResult{}, "", fmt.Errorf("%w: milestone_ref id %s", ErrAlreadyAbandoned, containerID)
	}

	shipped, unshipped, err := deliveryBreakdown(ctx, tx, containerID)
	if err != nil {
		return AbandonResult{}, "", err
	}

	backlog, err := getOrCreateBacklogTx(ctx, tx, scopeID, ref.ProductID, acting, onBehalfOf)
	if err != nil {
		return AbandonResult{}, "", err
	}

	// Nothing to sweep is not an error (a fully shipped, or never
	// delivered, container abandons cleanly) -- moveScopeTx requires at
	// least one entity id, unlike this method.
	if len(unshipped) > 0 {
		if err := moveScopeTx(ctx, tx, scopeID, unshipped, containerID, backlog.ID, acting, onBehalfOf); err != nil {
			return AbandonResult{}, "", err
		}
	}

	event, err := recordTransitionTx(ctx, tx, scopeID, containerID, MilestoneStatusAbandoned, note, acting, onBehalfOf)
	if err != nil {
		return AbandonResult{}, "", err
	}

	return AbandonResult{
		ContainerID:       containerID,
		BacklogID:         backlog.ID,
		MovedToBacklogIDs: unshipped,
		ShippedIDs:        shipped,
		StatusEvent:       event,
	}, ref.Kind, nil
}

// getMilestoneRefTx resolves id's full MilestoneRef row inside tx, scoped
// to scopeID -- Abandon's own container lookup, which (unlike
// milestoneRefKindAndParent, recut.go) also needs ProductID to resolve
// the right backlog bucket.
func getMilestoneRefTx(ctx context.Context, tx pgx.Tx, scopeID, id uuid.UUID) (MilestoneRef, error) {
	ref, err := scanMilestoneRef(tx.QueryRow(ctx, `
		SELECT `+milestoneRefColumns+`
		FROM milestone_ref
		WHERE id = $1 AND scope_id = $2
	`, id, scopeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return MilestoneRef{}, errParentNotFound("milestone_ref", id)
	}
	if err != nil {
		return MilestoneRef{}, fmt.Errorf("get milestone_ref: %w", err)
	}
	return ref, nil
}
