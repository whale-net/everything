package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxExecer is satisfied by both *pgxpool.Pool and pgx.Tx -- it lets
// addRoleTx run the SCD2 close-and-open pattern either as its own
// transaction (RoleStore.AddRole) or as part of a caller's larger
// transaction (InviteStore.Consume, which must atomically consume the
// code and grant role=analyst -- FR8).
type pgxExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

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
type roleStore struct{ pool *pgxpool.Pool }

var _ RoleStore = roleStore{}

func (s roleStore) RolesFor(ctx context.Context, channelID, personID uuid.UUID) ([]Role, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT role FROM channel_person
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, channelID, personID)
	if err != nil {
		return nil, fmt.Errorf("roles for channel/person: %w", err)
	}
	defer rows.Close()

	var roles []Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r); err != nil {
			return nil, fmt.Errorf("scan role: %w", err)
		}
		roles = append(roles, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("roles for channel/person: %w", err)
	}
	return roles, nil
}

// AddRole follows the SCD2 close-and-open pattern from AGENTS.md "SCD2":
// close any open row for (channelID, personID), then insert a new open
// row for role, in its own transaction.
func (s roleStore) AddRole(ctx context.Context, channelID, personID uuid.UUID, role Role) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := addRoleTx(ctx, tx, channelID, personID, role); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// addRoleTx is the SCD2 close-and-open pattern from AGENTS.md "SCD2",
// runnable against any pgxExecer so it can be composed into a larger
// caller-owned transaction (see InviteStore.Consume in invite.go).
func addRoleTx(ctx context.Context, exec pgxExecer, channelID, personID uuid.UUID, role Role) error {
	if _, err := exec.Exec(ctx, `
		UPDATE channel_person SET valid_to = NOW()
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, channelID, personID); err != nil {
		return fmt.Errorf("close existing role: %w", err)
	}

	if _, err := exec.Exec(ctx, `
		INSERT INTO channel_person (channel_id, person_id, role) VALUES ($1, $2, $3)
	`, channelID, personID, role); err != nil {
		return fmt.Errorf("insert role: %w", err)
	}
	return nil
}
