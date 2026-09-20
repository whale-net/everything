package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ScopeStore is a read-only view of `scope` (migration 001, issue #2487,
// LB1). It exists so a caller that already has a scope id (e.g. from a
// GatedSession) can read back that scope's forge coordinates
// (RepoFullName, DefaultBranch) -- pointer.go (issue #2496, FR20) is the
// one caller today, needing RepoFullName to create a Product's pointer
// issue against the right repository. There is deliberately no write
// method here: `scope` has no exposed CRUD (ARCHITECTURE.md "The scope
// table"), and the one in-place edit this milestone makes --
// PointerIssueNumber, once a pointer issue is minted -- is done directly
// by PointerArtifactStore.Create in the same transaction as its INSERT,
// not through this interface.
type ScopeStore interface {
	// GetByID returns the scope row for id, or ErrNotFound.
	GetByID(ctx context.Context, id uuid.UUID) (Scope, error)

	// GetSole returns the one scope row this deployment's seeder
	// (migrate/seed/seed.go) guarantees exists, or ErrNotFound before that
	// seeder has run. Added for issue #2827: an MCP-only caller has no
	// other way to learn a scope_id (there is exactly one, by
	// construction -- render/cmd/main.go's own `SELECT id FROM scope
	// LIMIT 1` already relies on the same assumption), so
	// mcp/tools.RegisterInitSession resolves it here instead of asking
	// the caller to supply or discover one.
	GetSole(ctx context.Context) (Scope, error)
}

type scopeStore struct{ pool *pgxpool.Pool }

var _ ScopeStore = scopeStore{}

const scopeColumns = `id, repo_full_name, default_branch, pointer_issue_number, created_at, updated_at`

func scanScope(row pgx.Row) (Scope, error) {
	var s Scope
	err := row.Scan(&s.ID, &s.RepoFullName, &s.DefaultBranch, &s.PointerIssueNumber, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func (s scopeStore) GetByID(ctx context.Context, id uuid.UUID) (Scope, error) {
	scope, err := scanScope(s.pool.QueryRow(ctx, `
		SELECT `+scopeColumns+`
		FROM scope
		WHERE id = $1
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Scope{}, fmt.Errorf("%w: scope id %s", ErrNotFound, id)
	}
	if err != nil {
		return Scope{}, fmt.Errorf("get scope: %w", err)
	}
	return scope, nil
}

func (s scopeStore) GetSole(ctx context.Context) (Scope, error) {
	scope, err := scanScope(s.pool.QueryRow(ctx, `
		SELECT `+scopeColumns+`
		FROM scope
		LIMIT 1
	`))
	if errors.Is(err, pgx.ErrNoRows) {
		return Scope{}, fmt.Errorf("%w: no scope row seeded yet", ErrNotFound)
	}
	if err != nil {
		return Scope{}, fmt.Errorf("get sole scope: %w", err)
	}
	return scope, nil
}
