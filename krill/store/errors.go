package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned by a GetCurrentByID when no current
// (valid_to IS NULL) row exists for the given id, and by a Create* method
// when the given parent id has no current row.
var ErrNotFound = errors.New("krill/store: not found")

// txQuerier is the subset of pgx.Tx / *pgxpool.Pool every Create* method
// needs for its parent-existence check -- satisfied by both, so the check
// and the INSERT that follows it can run inside one transaction.
type txQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// currentRowExists reports whether table has a current (valid_to IS NULL)
// row whose `id` column equals id. It is the store-layer enforcement LB2's
// parentage design calls for: migration 002 deliberately does not declare
// a DB-level REFERENCES for parent columns (a parent's immutable `id` is
// not unique table-wide -- see that migration's comment), so every
// Create* method on a child entity calls this, inside the same
// transaction as its INSERT, before writing a row that would otherwise
// silently reference a parent that does not (or no longer) exist.
//
// table is always one of this package's own constant table names, never
// caller input, so building the query with fmt.Sprintf carries no
// injection risk.
func currentRowExists(ctx context.Context, q txQuerier, table string, id uuid.UUID) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1 AND valid_to IS NULL)`, table), id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check current row exists in %s: %w", table, err)
	}
	return exists, nil
}

// errParentNotFound reports table/parentID as the missing parent of an
// attempted Create -- e.g. "krill/store: not found: no current feature_set
// row for id <uuid>".
func errParentNotFound(table string, parentID uuid.UUID) error {
	return fmt.Errorf("%w: no current %s row for id %s", ErrNotFound, table, parentID)
}
