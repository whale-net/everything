package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RoleStore covers `channel_person` (migration 001, the LB2 join table).
// This is the ONLY table M1 authorization reads from -- see authz.go's
// CanApprove/CanInvite/CanReconnect/CanRead/CanWrite, the sanctioned entry
// points built on top of it (NFR5).
type RoleStore interface {
	// RolesFor returns every currently-held (valid_to IS NULL) Role for
	// the (channelID, personID) pair -- ordinarily zero or one, but
	// callers must not assume at most one (LB2: no single-Creator
	// assumption is baked in).
	RolesFor(ctx context.Context, channelID, personID uuid.UUID) ([]Role, error)

	// AddRole grants role to personID on channelID, following the SCD2
	// close-and-open pattern from AGENTS.md: closes any existing open row
	// for the same (channelID, personID) pair, then inserts a new open
	// row for role.
	AddRole(ctx context.Context, channelID, personID uuid.UUID, role Role) error
}

// roleStore implements RoleStore against `channel_person` (migration 001).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1568's "Implementation" scope).
type roleStore struct{ pool *pgxpool.Pool }

var _ RoleStore = roleStore{}

func (s roleStore) RolesFor(ctx context.Context, channelID, personID uuid.UUID) ([]Role, error) {
	return nil, errors.New("not implemented")
}

func (s roleStore) AddRole(ctx context.Context, channelID, personID uuid.UUID, role Role) error {
	return errors.New("not implemented")
}
