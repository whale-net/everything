package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

type environmentRepo struct{ ex dbtx }

const environmentColumns = `environment_id, key, display_name, rank, requires_approval, gitops_path, allowed_principals, archived, created_at`

func scanEnvironment(row pgx.Row) (repository.Environment, error) {
	var e repository.Environment
	if err := row.Scan(&e.EnvironmentID, &e.Key, &e.DisplayName, &e.Rank, &e.RequiresApproval, &e.GitopsPath, &e.AllowedPrincipals, &e.Archived, &e.CreatedAt); err != nil {
		return repository.Environment{}, err
	}
	return e, nil
}

func (r *environmentRepo) getByKey(ctx context.Context, key string) (*repository.Environment, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+environmentColumns+` FROM environment WHERE key = $1`, key)
	e, err := scanEnvironment(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &e, nil
}

// Upsert creates an environment keyed by e.Key, or updates every field but
// Key (the immutable identity) if one already exists. Archived is never
// touched here -- only Archive changes it, mirroring
// AppRepository.SetAppStatus's separation of "reconcile writes identity
// fields" from "a dedicated call changes lifecycle state".
func (r *environmentRepo) Upsert(ctx context.Context, e repository.Environment) (*repository.Environment, bool, error) {
	// allowed_principals is NOT NULL; pgx encodes a nil []string as SQL NULL
	// rather than an empty array, so a caller passing nil (the Go zero value
	// for "no principals set") must be normalized before it reaches the
	// driver.
	if e.AllowedPrincipals == nil {
		e.AllowedPrincipals = []string{}
	}

	existing, err := r.getByKey(ctx, e.Key)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, false, fmt.Errorf("upsert environment %s: look up: %w", e.Key, err)
	}

	if existing == nil {
		id := uuid.NewString()
		now := time.Now().UTC()
		if _, err := r.ex.Exec(ctx, `
			INSERT INTO environment (environment_id, key, display_name, rank, requires_approval, gitops_path, allowed_principals, archived, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, false, $8)`,
			id, e.Key, e.DisplayName, e.Rank, e.RequiresApproval, e.GitopsPath, e.AllowedPrincipals, now); err != nil {
			if de, ok := translatePgError(err, fmt.Sprintf("environment %q already recorded", e.Key)); ok {
				return nil, false, de
			}
			return nil, false, fmt.Errorf("upsert environment %s: insert: %w", e.Key, err)
		}
		e.EnvironmentID = id
		e.Archived = false
		e.CreatedAt = now
		return &e, true, nil
	}

	if _, err := r.ex.Exec(ctx, `
		UPDATE environment SET display_name = $2, rank = $3, requires_approval = $4, gitops_path = $5, allowed_principals = $6
		WHERE key = $1`,
		e.Key, e.DisplayName, e.Rank, e.RequiresApproval, e.GitopsPath, e.AllowedPrincipals); err != nil {
		return nil, false, fmt.Errorf("upsert environment %s: update: %w", e.Key, err)
	}

	updated := *existing
	updated.DisplayName = e.DisplayName
	updated.Rank = e.Rank
	updated.RequiresApproval = e.RequiresApproval
	updated.GitopsPath = e.GitopsPath
	updated.AllowedPrincipals = e.AllowedPrincipals
	return &updated, false, nil
}

func (r *environmentRepo) Get(ctx context.Context, key string) (*repository.Environment, error) {
	return r.getByKey(ctx, key)
}

func (r *environmentRepo) List(ctx context.Context, includeArchived bool) ([]repository.Environment, error) {
	query := `SELECT ` + environmentColumns + ` FROM environment`
	if !includeArchived {
		query += ` WHERE archived = false`
	}
	query += ` ORDER BY rank ASC`

	rows, err := r.ex.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repository.Environment
	for rows.Next() {
		e, err := scanEnvironment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Archive marks an environment archived. archived -> archived is a no-op
// success so a retried archive call is safe, mirroring
// AppRepository.SetAppStatus's archive path. reason is accepted (and
// required at the handler layer) but not persisted -- there is no
// environment audit log yet, same as SetAppStatus's reason today.
func (r *environmentRepo) Archive(ctx context.Context, key, reason string) (*repository.Environment, error) {
	existing, err := r.getByKey(ctx, key)
	if err != nil {
		return nil, err
	}
	if existing.Archived {
		return existing, nil
	}
	if _, err := r.ex.Exec(ctx, `UPDATE environment SET archived = true WHERE key = $1`, key); err != nil {
		return nil, fmt.Errorf("archive environment %s: %w", key, err)
	}
	existing.Archived = true
	return existing, nil
}
