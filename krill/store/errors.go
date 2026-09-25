package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// rowsQuerier is txQuerier's multi-row counterpart -- satisfied by both
// pgx.Tx and *pgxpool.Pool identically. ClaimTask (task_claim.go, issue
// #2722) uses this to run UnsatisfiedDependencies' own predicate
// (task_dependency.go) against its claim transaction's row-locked
// snapshot, rather than a second, pool-backed round trip outside that
// transaction.
type rowsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// currentRowExists reports whether table has a current (valid_to IS NULL)
// row whose `id` column equals id AND whose `scope_id` column equals
// scopeID. It is the store-layer enforcement LB2's parentage design calls
// for: migration 002 deliberately does not declare a DB-level REFERENCES
// for parent columns (a parent's immutable `id` is not unique table-wide
// -- see that migration's comment), so every Create* method on a child
// entity calls this, inside the same transaction as its INSERT, before
// writing a row that would otherwise silently reference a parent that
// does not (or no longer) exist.
//
// The scope_id comparison is not optional: LB1 requires scope_id to
// never be an unchecked value, and a parent id alone is not scope-unique
// (ids are globally unique, but nothing stops a caller from citing a
// real parent id that belongs to a different scope). Without this check
// one scope's session could graft a child onto another scope's entity
// tree -- see TestCreateFeatureSet_CrossScopeProduct_IsRejected.
//
// table is always one of this package's own constant table names, never
// caller input, so building the query with fmt.Sprintf carries no
// injection risk.
func currentRowExists(ctx context.Context, q txQuerier, table string, id, scopeID uuid.UUID) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL)`, table), id, scopeID).Scan(&exists)
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

// ErrNameConflict reports that a write would give an entity a name a live
// sibling under the same parent already holds. Every spec-axis table's
// scope-qualified name index is partial on `valid_to IS NULL`, so this is
// the rejection an amend hits when its replacement name collides with a
// sibling that is still current -- the same rule, from the same index, that
// Create enforces (FR b2767a89).
var ErrNameConflict = errors.New("krill/store: a live sibling under the same parent already has this name")

// errNameConflict maps a unique-index violation from an INSERT into
// ErrNameConflict, naming table and what was being written, and passes every
// other error through unchanged. Postgres is the only thing that can tell
// whether the collision was on the name index or one of the other unique
// constraints (the `(id) WHERE valid_to IS NULL` SCD2 index, a CHECK), so
// the message is deliberately generic about which.
func errNameConflict(table, action string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s (%s)", ErrNameConflict, action, table)
	}
	return err
}

// plainRowExists is currentRowExists' counterpart for a parent table with
// no `valid_to` column at all -- the work-axis tables (`task`,
// `task_note`, migration 015) are append-or-claimed, not SCD2 (LB3), so
// every row simply exists or does not; there is no "current row"
// distinction to filter on. `milestone_ref` used to need this too, until
// migration 020 made it SCD2.
func plainRowExists(ctx context.Context, q txQuerier, table string, id, scopeID uuid.UUID) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1 AND scope_id = $2)`, table), id, scopeID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check row exists in %s: %w", table, err)
	}
	return exists, nil
}
