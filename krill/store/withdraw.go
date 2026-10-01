package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WithdrawalStore withdraws wrong delivery-axis edges (entity_milestone rows,
// migration 029) by stamping withdrawn_* on the row; the row is kept and a
// later re-add inserts a fresh active row.
type WithdrawalStore interface {
	// WithdrawMustNotForeclose withdraws the active must_not_foreclose edge
	// (entityID, milestoneID). Errors, writing nothing, naming both ids if
	// no active edge exists.
	WithdrawMustNotForeclose(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, reason string, acting, onBehalfOf Subject) error

	// WithdrawDelivers withdraws the active delivers edge (entityID,
	// milestoneID) of a milestone or milepebble. Refuses, writing nothing,
	// if the item already shipped there, if no active edge exists, or (for a
	// milestone) while an active milepebble edge for the entity exists under
	// it -- that error names the milepebble(s).
	WithdrawDelivers(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, reason string, acting, onBehalfOf Subject) error
}

type withdrawalStore struct{ pool *pgxpool.Pool }

var _ WithdrawalStore = withdrawalStore{}

// Sentinels for the refusal cases; each wrapped error names the ids involved.
var (
	ErrEdgeNotActive       = errors.New("krill/store: no active edge to withdraw")
	ErrWithdrawShipped     = errors.New("krill/store: entity already shipped in this milestone; its delivers edge cannot be withdrawn")
	ErrWithdrawMilepebbles = errors.New("krill/store: entity is still delivered by active milepebble(s) under this milestone; withdraw those first")
)

func (s withdrawalStore) WithdrawMustNotForeclose(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, reason string, acting, onBehalfOf Subject) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := withdrawEdgeTx(ctx, tx, scopeID, milestoneID, entityID, MilestoneRelationMustNotForeclose, reason, acting, onBehalfOf); err != nil {
		return err
	}
	return commitTx(ctx, tx)
}

func (s withdrawalStore) WithdrawDelivers(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, reason string, acting, onBehalfOf Subject) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	kind, _, err := deliversTarget(ctx, tx, scopeID, milestoneID)
	if err != nil {
		return err
	}
	var shipped bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM delivery_shipment WHERE entity_id = $1 AND milestone_id = $2)`,
		entityID, milestoneID).Scan(&shipped); err != nil {
		return fmt.Errorf("check delivery_shipment: %w", err)
	}
	if shipped {
		return fmt.Errorf("%w: milestone %s, entity %s", ErrWithdrawShipped, milestoneID, entityID)
	}
	if kind == MilestoneKindMilestone {
		rows, err := tx.Query(ctx, `
			SELECT em.milestone_id FROM entity_milestone_active em
			JOIN milestone_ref mr ON mr.id = em.milestone_id AND mr.valid_to IS NULL
			WHERE em.entity_id = $1 AND em.relation = $2 AND mr.parent_milestone_id = $3
			ORDER BY em.milestone_id`, entityID, string(MilestoneRelationDelivers), milestoneID)
		if err != nil {
			return fmt.Errorf("check milepebble edges: %w", err)
		}
		var ids []string
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("scan milepebble edge: %w", err)
			}
			ids = append(ids, id.String())
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("check milepebble edges: %w", err)
		}
		if len(ids) > 0 {
			return fmt.Errorf("%w: milestone %s, entity %s, milepebble(s) %s", ErrWithdrawMilepebbles, milestoneID, entityID, strings.Join(ids, ", "))
		}
	}
	if err := withdrawEdgeTx(ctx, tx, scopeID, milestoneID, entityID, MilestoneRelationDelivers, reason, acting, onBehalfOf); err != nil {
		return err
	}
	return commitTx(ctx, tx)
}

func commitTx(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// withdrawEdgeTx stamps withdrawn_* on the one active edge, or errors naming
// the milestone and entity ids when none exists.
func withdrawEdgeTx(ctx context.Context, tx pgx.Tx, scopeID, milestoneID, entityID uuid.UUID, relation MilestoneRelation, reason string, acting, onBehalfOf Subject) error {
	tag, err := tx.Exec(ctx, `
		UPDATE entity_milestone SET
			withdrawn_at = NOW(),
			withdrawn_by_acting_iss = $5, withdrawn_by_acting_sub = $6, withdrawn_by_acting_kind = $7,
			withdrawn_by_on_behalf_iss = $8, withdrawn_by_on_behalf_sub = $9, withdrawn_by_on_behalf_kind = $10,
			withdrawal_reason = $11
		WHERE scope_id = $1 AND milestone_id = $2 AND entity_id = $3 AND relation = $4 AND withdrawn_at IS NULL
	`, scopeID, milestoneID, entityID, string(relation),
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind), reason)
	if err != nil {
		return fmt.Errorf("withdraw entity_milestone: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: milestone %s, entity %s (%s)", ErrEdgeNotActive, milestoneID, entityID, relation)
	}
	return nil
}
