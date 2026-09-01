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

// InviteStore covers `channel_invite` (migration 001, FR5-FR8).
type InviteStore interface {
	// Generate creates a new, high-entropy (crypto/rand) invite code for
	// channelID, invalidating any prior live (unconsumed, uninvalidated)
	// code for that Channel in the same transaction (FR5) -- at most one
	// live code per Channel at a time.
	Generate(ctx context.Context, channelID, byPersonID uuid.UUID) (Invite, error)

	// Lookup returns the Invite for code, live or not, or an error if no
	// such code exists.
	Lookup(ctx context.Context, code string) (Invite, error)

	// Consume atomically sets consumed_at/consumed_by_person_id and adds a
	// role=analyst channel_person row for byPersonID (FR8). Returns an
	// error without side effects if code is already consumed or
	// invalidated.
	Consume(ctx context.Context, code string, byPersonID uuid.UUID) error
}

// ErrInviteConsumed is returned by Consume when code has already been
// consumed.
var ErrInviteConsumed = errors.New("invite code already consumed")

// ErrInviteInvalidated is returned by Consume when code has been
// invalidated (superseded by a newer Generate call, FR5).
var ErrInviteInvalidated = errors.New("invite code invalidated")

// inviteStore implements InviteStore against `channel_invite` (migration
// 001).
type inviteStore struct{ pool *pgxpool.Pool }

var _ InviteStore = inviteStore{}

const inviteColumns = `id, channel_id, code, created_by_person_id, created_at, consumed_at, consumed_by_person_id, invalidated_at`

func scanInvite(row pgx.Row) (Invite, error) {
	var inv Invite
	err := row.Scan(&inv.ID, &inv.ChannelID, &inv.Code, &inv.CreatedByPersonID, &inv.CreatedAt, &inv.ConsumedAt, &inv.ConsumedByPersonID, &inv.InvalidatedAt)
	return inv, err
}

// Generate invalidates any prior live code for channelID and inserts a
// new one, in one transaction (FR5) -- the channel_invite_channel_id_live
// partial unique index (migration 001) is the backstop that makes "at
// most one live code" hold even under a race.
func (s inviteStore) Generate(ctx context.Context, channelID, byPersonID uuid.UUID) (Invite, error) {
	code, err := generateInviteCode()
	if err != nil {
		return Invite{}, fmt.Errorf("generate invite code: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Invite{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE channel_invite SET invalidated_at = NOW()
		WHERE channel_id = $1 AND consumed_at IS NULL AND invalidated_at IS NULL
	`, channelID); err != nil {
		return Invite{}, fmt.Errorf("invalidate prior invite: %w", err)
	}

	inv, err := scanInvite(tx.QueryRow(ctx, `
		INSERT INTO channel_invite (channel_id, code, created_by_person_id)
		VALUES ($1, $2, $3)
		RETURNING `+inviteColumns,
		channelID, code, byPersonID))
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
// still live, then atomically stamps it consumed and grants role=analyst
// via addRoleTx (role.go) -- all in one transaction (FR8).
func (s inviteStore) Consume(ctx context.Context, code string, byPersonID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var id, channelID uuid.UUID
	var consumedAt, invalidatedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, channel_id, consumed_at, invalidated_at
		FROM channel_invite
		WHERE code = $1
		FOR UPDATE
	`, code).Scan(&id, &channelID, &consumedAt, &invalidatedAt)
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

	if err := addRoleTx(ctx, tx, channelID, byPersonID, RoleAnalyst); err != nil {
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
