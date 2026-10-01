package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotImplemented marks a scaffold stub whose behavior lands in Implementation.
var ErrNotImplemented = errors.New("krill/store: not implemented")

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

func (withdrawalStore) WithdrawMustNotForeclose(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, Subject, Subject) error {
	return ErrNotImplemented
}

func (withdrawalStore) WithdrawDelivers(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, Subject, Subject) error {
	return ErrNotImplemented
}
