package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PersonStore covers `person` (migration 001).
type PersonStore interface {
	// UpsertByGoogleSubject finds or creates the Person for sub -- the
	// stable Google OAuth identity key, not email -- updating email and
	// displayName on an existing row rather than creating a duplicate
	// (FR1/FR2). created reports whether a new row was inserted.
	UpsertByGoogleSubject(ctx context.Context, sub, email, displayName string) (Person, bool, error)

	// GetByID returns the Person for id, or an error if none exists.
	GetByID(ctx context.Context, id uuid.UUID) (Person, error)
}

// personStore implements PersonStore against `person` (migration 001).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1568's "Implementation" scope).
type personStore struct{ pool *pgxpool.Pool }

var _ PersonStore = personStore{}

func (s personStore) UpsertByGoogleSubject(ctx context.Context, sub, email, displayName string) (Person, bool, error) {
	return Person{}, false, errors.New("not implemented")
}

func (s personStore) GetByID(ctx context.Context, id uuid.UUID) (Person, error) {
	return Person{}, errors.New("not implemented")
}
