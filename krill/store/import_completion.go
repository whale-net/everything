package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ImportCompletion is one row of `import_completion` (migration 007,
// issue #2548, FR12, NFR3) -- the one-time, one-way marker recording that
// a `whagent_net`-style import for a given (scope, product) pair has
// completed. Plain, append-only fact row -- not SCD2 (LB3) -- see
// migration 007_import_completion.up.sql's boundary comment: there is no
// "current row" distinction here, and no update path.
//
// SourcePath is recorded for the audit trail only (NFR3) -- nothing in
// krill ever reads it back to open a file. See this type's one intended
// consumer, the report/audit output, and migration 007's column comment.
type ImportCompletion struct {
	ID             uuid.UUID
	ScopeID        uuid.UUID
	ProductID      uuid.UUID
	SourcePath     string
	SourceRevision string
	CompletedAt    time.Time
}

// ErrAlreadyComplete is returned by MarkComplete when the given
// (scopeID, productID) pair already has an import_completion row.
// There is no un-complete verb and no overwrite path -- a genuine
// re-import is a deliberate future operation, not something MarkComplete
// silently absorbs. Callers (krill/importer.Import) surface this as a
// refusal, not a retry.
var ErrAlreadyComplete = errors.New("krill/store: import already complete for this scope and product")

// ImportCompletionStore covers `import_completion` (migration 007, issue
// #2548, FR12, NFR3). Keyed by (scope_id, product_id), not scope_id
// alone -- see migration 007's comment for why: FR12 reads as a
// scope-level flag, but keying on the product as well is strictly safer
// under M1's one-Product-per-scope shape and does not wrongly block a
// second product's import into the same scope later.
type ImportCompletionStore interface {
	// MarkComplete records that the import of productID into scopeID has
	// completed, from sourcePath (audit trail only, NFR3 -- never read
	// back) at sourceRevision (the repo commit SHA the import was taken
	// from). Returns ErrNotFound if productID has no current `product`
	// row in scopeID (LB2 parentage, same enforcement as every other
	// Create in this package). Returns ErrAlreadyComplete -- without
	// touching the existing row's CompletedAt or SourceRevision -- if
	// (scopeID, productID) is already marked complete; there is no
	// un-complete verb, so this is never a silent overwrite.
	MarkComplete(ctx context.Context, scopeID, productID uuid.UUID, sourcePath, sourceRevision string) (ImportCompletion, error)

	// IsComplete reports whether (scopeID, productID) has a completed
	// import, returning the row when true. Returns (zero value, false,
	// nil) -- not an error -- when no completion exists yet.
	IsComplete(ctx context.Context, scopeID, productID uuid.UUID) (ImportCompletion, bool, error)

	// ListByScope returns every import_completion row for scopeID,
	// ordered by CompletedAt. This is what krill/importer.Import's
	// pre-parse refusal check (FR12) uses to look for a prior completion
	// whose SourcePath matches the requested import path, since the
	// target Product is not resolved until after parsing.
	ListByScope(ctx context.Context, scopeID uuid.UUID) ([]ImportCompletion, error)
}

type importCompletionStore struct{ pool *pgxpool.Pool }

var _ ImportCompletionStore = importCompletionStore{}

const importCompletionColumns = `id, scope_id, product_id, source_path, source_revision, completed_at`

func scanImportCompletion(row pgx.Row) (ImportCompletion, error) {
	var c ImportCompletion
	err := row.Scan(&c.ID, &c.ScopeID, &c.ProductID, &c.SourcePath, &c.SourceRevision, &c.CompletedAt)
	return c, err
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505), via errors.As against *pgconn.PgError
// rather than string matching -- mirrors api/handlers/types.go's own
// helper of the same name (kept as a separate copy here so this store
// package does not depend on the api/handlers package).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s importCompletionStore) MarkComplete(ctx context.Context, scopeID, productID uuid.UUID, sourcePath, sourceRevision string) (ImportCompletion, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ImportCompletion{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	exists, err := currentRowExists(ctx, tx, "product", productID, scopeID)
	if err != nil {
		return ImportCompletion{}, err
	}
	if !exists {
		return ImportCompletion{}, errParentNotFound("product", productID)
	}

	completion, err := scanImportCompletion(tx.QueryRow(ctx, `
		INSERT INTO import_completion (scope_id, product_id, source_path, source_revision)
		VALUES ($1, $2, $3, $4)
		RETURNING `+importCompletionColumns,
		scopeID, productID, sourcePath, sourceRevision))
	if isUniqueViolation(err) {
		return ImportCompletion{}, fmt.Errorf("%w: scope %s product %s", ErrAlreadyComplete, scopeID, productID)
	}
	if err != nil {
		return ImportCompletion{}, fmt.Errorf("insert import_completion: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ImportCompletion{}, fmt.Errorf("commit: %w", err)
	}
	return completion, nil
}

func (s importCompletionStore) IsComplete(ctx context.Context, scopeID, productID uuid.UUID) (ImportCompletion, bool, error) {
	completion, err := scanImportCompletion(s.pool.QueryRow(ctx, `
		SELECT `+importCompletionColumns+`
		FROM import_completion
		WHERE scope_id = $1 AND product_id = $2
	`, scopeID, productID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ImportCompletion{}, false, nil
	}
	if err != nil {
		return ImportCompletion{}, false, fmt.Errorf("get import_completion: %w", err)
	}
	return completion, true, nil
}

func (s importCompletionStore) ListByScope(ctx context.Context, scopeID uuid.UUID) ([]ImportCompletion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+importCompletionColumns+`
		FROM import_completion
		WHERE scope_id = $1
		ORDER BY completed_at
	`, scopeID)
	if err != nil {
		return nil, fmt.Errorf("list import_completion by scope: %w", err)
	}
	defer rows.Close()

	var completions []ImportCompletion
	for rows.Next() {
		c, err := scanImportCompletion(rows)
		if err != nil {
			return nil, fmt.Errorf("scan import_completion: %w", err)
		}
		completions = append(completions, c)
	}
	return completions, rows.Err()
}
