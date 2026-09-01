package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
type personStore struct{ pool *pgxpool.Pool }

var _ PersonStore = personStore{}

// UpsertByGoogleSubject relies on the ON CONFLICT (google_subject) DO
// UPDATE + `xmax = 0` idiom to atomically find-or-create in a single
// round trip and report whether the row was newly inserted: within the
// same statement, xmax is 0 for a row the INSERT branch created and
// non-zero for a row the DO UPDATE branch touched.
func (s personStore) UpsertByGoogleSubject(ctx context.Context, sub, email, displayName string) (Person, bool, error) {
	var p Person
	var created bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO person (google_subject, email, display_name)
		VALUES ($1, $2, $3)
		ON CONFLICT (google_subject) DO UPDATE
			SET email = EXCLUDED.email, display_name = EXCLUDED.display_name
		RETURNING id, google_subject, COALESCE(email, ''), COALESCE(display_name, ''), created_at, (xmax = 0)
	`, sub, email, displayName).Scan(&p.ID, &p.GoogleSubject, &p.Email, &p.DisplayName, &p.CreatedAt, &created)
	if err != nil {
		return Person{}, false, fmt.Errorf("upsert person by google_subject: %w", err)
	}
	return p, created, nil
}

func (s personStore) GetByID(ctx context.Context, id uuid.UUID) (Person, error) {
	var p Person
	err := s.pool.QueryRow(ctx, `
		SELECT id, google_subject, COALESCE(email, ''), COALESCE(display_name, ''), created_at
		FROM person
		WHERE id = $1
	`, id).Scan(&p.ID, &p.GoogleSubject, &p.Email, &p.DisplayName, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Unwrapped pgx.ErrNoRows so callers can errors.Is(err,
			// pgx.ErrNoRows) directly (matches
			// tools/app_registry/server/repository/postgres convention).
			return Person{}, pgx.ErrNoRows
		}
		return Person{}, fmt.Errorf("get person by id: %w", err)
	}
	return p, nil
}
