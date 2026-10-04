// Package identitylink stores the mapping from a whagent-net operator's
// (iss, sub) to the manmanv2 Keycloak user who confirmed it, plus the
// single-use record of consumed link assertions. The UI writes both; the MCP
// reads the mapping to act as the linked user.
package identitylink

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Outcome is the result of a successful Link call.
type Outcome int

const (
	// Created means a new mapping row was written.
	Created Outcome = iota
	// AlreadyLinked means the same mapping already existed.
	AlreadyLinked
)

var (
	// ErrLinkedToOtherUser means (iss, sub) is already linked to a different manmanv2 user.
	ErrLinkedToOtherUser = errors.New("identitylink: whagent identity is already linked to a different user")
	// ErrAssertionConsumed means the assertion's jti was already used.
	ErrAssertionConsumed = errors.New("identitylink: link assertion already consumed")
)

// Store is the Postgres-backed mapping and replay store.
type Store struct{ DB *sql.DB }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Link maps (iss, sub) to userSub. A repeat of the same mapping is
// AlreadyLinked; a different userSub for an existing pair is ErrLinkedToOtherUser.
func (s Store) Link(ctx context.Context, iss, sub, userSub string) (Outcome, error) {
	if iss == "" || sub == "" || userSub == "" {
		return 0, errors.New("identitylink: iss, sub and user are all required")
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO whagent_identity_link (iss, sub, user_sub) VALUES ($1, $2, $3)`, iss, sub, userSub)
	if err == nil {
		return Created, nil
	}
	if !isUniqueViolation(err) {
		return 0, fmt.Errorf("identitylink: insert link: %w", err)
	}
	existing, found, lookupErr := s.Resolve(ctx, iss, sub)
	if lookupErr != nil {
		return 0, lookupErr
	}
	if found && existing == userSub {
		return AlreadyLinked, nil
	}
	return 0, ErrLinkedToOtherUser
}

// Resolve returns the manmanv2 Keycloak sub linked to (iss, sub), if any.
func (s Store) Resolve(ctx context.Context, iss, sub string) (string, bool, error) {
	var userSub string
	err := s.DB.QueryRowContext(ctx,
		`SELECT user_sub FROM whagent_identity_link WHERE iss = $1 AND sub = $2`, iss, sub).Scan(&userSub)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("identitylink: resolve: %w", err)
	}
	return userSub, true, nil
}

// IsConsumed reports whether the assertion jti was already used.
func (s Store) IsConsumed(ctx context.Context, jti string) (bool, error) {
	var ok bool
	err := s.DB.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM whagent_link_assertion WHERE jti = $1)`, jti).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("identitylink: is consumed: %w", err)
	}
	return ok, nil
}

// Consume records jti as used; a second call returns ErrAssertionConsumed.
func (s Store) Consume(ctx context.Context, jti string, expiresAt time.Time) error {
	if jti == "" {
		return errors.New("identitylink: consume: jti is required")
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO whagent_link_assertion (jti, expires_at) VALUES ($1, $2)`, jti, expiresAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAssertionConsumed
		}
		return fmt.Errorf("identitylink: consume: %w", err)
	}
	return nil
}

// ReapExpired deletes consumed-assertion rows whose assertion has expired.
func (s Store) ReapExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM whagent_link_assertion WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("identitylink: reap: %w", err)
	}
	return res.RowsAffected()
}
