package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InviteStore covers `channel_invite` (migration 001, FR5-FR8; widened by
// migration 009 for M2, FR29/FR30/NFR11).
type InviteStore interface {
	// Generate returns the live (unconsumed, uninvalidated) invite code for
	// (channelID, role) if one already exists, or creates a new
	// high-entropy (crypto/rand) one otherwise (FR30) -- idempotent per
	// LB4: re-issuing a generate request for a tier that already has a
	// live code on this Channel returns that same code rather than
	// creating a second one. "At most one live code per Channel" (M1's
	// FR5) is rescoped to "at most one live code per (Channel, tier)"
	// (NFR11), so a live Co-Creator invite and a live Analyst invite can
	// coexist on one Channel. role must be RoleCoCreator or RoleAnalyst --
	// RoleCreator (or any other value) is rejected with
	// ErrInviteRoleUnsupported before touching the DB, since no invite
	// path ever grants Founder (FR25/FR29).
	Generate(ctx context.Context, channelID, byPersonID uuid.UUID, role Role) (Invite, error)

	// Lookup returns the Invite for code, live or not, or an error if no
	// such code exists.
	Lookup(ctx context.Context, code string) (Invite, error)

	// Consume atomically sets consumed_at/consumed_by_person_id and adds a
	// channel_person row for byPersonID at the invite's own Role (FR8,
	// widened for FR30 -- co_creator or analyst depending on which tier
	// was generated). Returns an error without side effects if code is
	// already consumed or invalidated.
	Consume(ctx context.Context, code string, byPersonID uuid.UUID) error
}

// ErrInviteConsumed is returned by Consume when code has already been
// consumed.
var ErrInviteConsumed = errors.New("invite code already consumed")

// ErrInviteInvalidated is returned by Consume when code has been
// invalidated.
var ErrInviteInvalidated = errors.New("invite code invalidated")

// ErrInviteRoleUnsupported is returned by Generate when role is not an
// invitable tier (FR25/FR29: only RoleCoCreator and RoleAnalyst are --
// Founder is never granted by an invite).
var ErrInviteRoleUnsupported = errors.New("invite role must be co_creator or analyst")

// inviteStore implements InviteStore against `channel_invite` (migration
// 001, widened by migration 009).
type inviteStore struct{ pool *pgxpool.Pool }

var _ InviteStore = inviteStore{}

const inviteColumns = `id, channel_id, code, created_by_person_id, created_at, consumed_at, consumed_by_person_id, invalidated_at, role`

func scanInvite(row pgx.Row) (Invite, error) {
	var inv Invite
	err := row.Scan(&inv.ID, &inv.ChannelID, &inv.Code, &inv.CreatedByPersonID, &inv.CreatedAt, &inv.ConsumedAt, &inv.ConsumedByPersonID, &inv.InvalidatedAt, &inv.Role)
	return inv, err
}

// Generate is idempotent per (channel_id, role) (FR30): in one
// transaction, it locks (SELECT ... FOR UPDATE) any live row for that
// pair and returns it unchanged if one exists, otherwise inserts a new
// one. The channel_invite_channel_id_role_live partial unique index
// (migration 009) is the backstop that makes "at most one live code per
// (Channel, tier)" hold even under a race.
func (s inviteStore) Generate(ctx context.Context, channelID, byPersonID uuid.UUID, role Role) (Invite, error) {
	if role != RoleCoCreator && role != RoleAnalyst {
		return Invite{}, ErrInviteRoleUnsupported
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Invite{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	existing, err := scanInvite(tx.QueryRow(ctx, `
		SELECT `+inviteColumns+` FROM channel_invite
		WHERE channel_id = $1 AND role = $2 AND consumed_at IS NULL AND invalidated_at IS NULL
		FOR UPDATE
	`, channelID, role))
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return Invite{}, fmt.Errorf("commit: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, fmt.Errorf("lookup live invite: %w", err)
	}

	code, err := generateInviteCode()
	if err != nil {
		return Invite{}, fmt.Errorf("generate invite code: %w", err)
	}

	inv, err := scanInvite(tx.QueryRow(ctx, `
		INSERT INTO channel_invite (channel_id, code, created_by_person_id, role)
		VALUES ($1, $2, $3, $4)
		RETURNING `+inviteColumns,
		channelID, code, byPersonID, role))
	if err != nil {
		return Invite{}, fmt.Errorf("insert invite: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Invite{}, fmt.Errorf("commit: %w", err)
	}
	return inv, nil
}

func (s inviteStore) Lookup(ctx context.Context, code string) (Invite, error) {
	inv, err := scanInvite(s.pool.QueryRow(ctx, `SELECT `+inviteColumns+` FROM channel_invite WHERE code = $1`, code))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Invite{}, pgx.ErrNoRows
		}
		return Invite{}, fmt.Errorf("lookup invite: %w", err)
	}
	return inv, nil
}

// Consume locks the invite row (SELECT ... FOR UPDATE) so a concurrent
// double-consume of the same code cannot both succeed, checks it is
// still live, then atomically stamps it consumed and grants the invite's
// own Role via addRoleTx (role.go) -- all in one transaction (FR8, FR30).
// The invite's own created_by_person_id (the Person who generated the
// code) is recorded as the grant's granted_by_person_id (FR34) -- the
// actor who authorized this Person joining the Channel, distinct from
// byPersonID (the redeemer).
func (s inviteStore) Consume(ctx context.Context, code string, byPersonID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var id, channelID, createdByPersonID uuid.UUID
	var role Role
	var consumedAt, invalidatedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, channel_id, created_by_person_id, role, consumed_at, invalidated_at
		FROM channel_invite
		WHERE code = $1
		FOR UPDATE
	`, code).Scan(&id, &channelID, &createdByPersonID, &role, &consumedAt, &invalidatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgx.ErrNoRows
		}
		return fmt.Errorf("lookup invite for consume: %w", err)
	}
	if consumedAt != nil {
		return ErrInviteConsumed
	}
	if invalidatedAt != nil {
		return ErrInviteInvalidated
	}

	if _, err := tx.Exec(ctx, `
		UPDATE channel_invite SET consumed_at = NOW(), consumed_by_person_id = $1 WHERE id = $2
	`, byPersonID, id); err != nil {
		return fmt.Errorf("mark invite consumed: %w", err)
	}

	if err := addRoleTx(ctx, tx, channelID, byPersonID, role, createdByPersonID); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// generateInviteCode returns a high-entropy (crypto/rand), hex-encoded
// invite code (FR5-FR8).
func generateInviteCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
