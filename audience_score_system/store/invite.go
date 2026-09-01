package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
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

// inviteStore implements InviteStore against `channel_invite` (migration
// 001).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1568's "Implementation" scope).
type inviteStore struct{ pool *pgxpool.Pool }

var _ InviteStore = inviteStore{}

func (s inviteStore) Generate(ctx context.Context, channelID, byPersonID uuid.UUID) (Invite, error) {
	return Invite{}, errors.New("not implemented")
}

func (s inviteStore) Lookup(ctx context.Context, code string) (Invite, error) {
	return Invite{}, errors.New("not implemented")
}

func (s inviteStore) Consume(ctx context.Context, code string, byPersonID uuid.UUID) error {
	return errors.New("not implemented")
}
