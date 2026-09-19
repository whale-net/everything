package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// nextSiblingPosition returns the position value (FR7) the next row
// created under (parentColumn = parentID, scope_id = scopeID) in table
// should carry: one greater than the current max position among that
// parent's current (valid_to IS NULL) sibling rows, or 0 if it has none
// yet. Every Create* method in this package calls this inside the same
// transaction as its INSERT, so two concurrent creates under the same
// parent can never read the same max and tie at the same position --
// without an explicit position, ListCurrent* queries (which order by
// position, then name) silently degrade to alphabetical-by-name instead
// of reflecting creation order.
//
// Product has no parent column of its own -- its sibling set is scoped by
// scope_id alone (LB2) -- so productStore.Create passes "scope_id" for
// parentColumn and scopeID for parentID, making the WHERE clause's two
// conditions redundant but still correct.
//
// table and parentColumn are always one of this package's own constant
// names, never caller input, so building the query with fmt.Sprintf
// carries no injection risk (matches currentRowExists' same note in
// errors.go).
func nextSiblingPosition(ctx context.Context, q txQuerier, table, parentColumn string, parentID, scopeID uuid.UUID) (int, error) {
	var position int
	err := q.QueryRow(ctx, fmt.Sprintf(
		`SELECT COALESCE(MAX(position), -1) + 1 FROM %s WHERE %s = $1 AND scope_id = $2 AND valid_to IS NULL`,
		table, parentColumn,
	), parentID, scopeID).Scan(&position)
	if err != nil {
		return 0, fmt.Errorf("next sibling position in %s: %w", table, err)
	}
	return position, nil
}

// nextSiblingPositionPlain is nextSiblingPosition's counterpart for a
// table with no `valid_to` column at all -- `milestone_ref` and
// `milestone_deferral` (migration 010, issue #2683) are not SCD2 (LB3:
// see 004_milestone_assoc.up.sql's and 010_milestone_authoring.up.sql's
// boundary comments), so every row simply exists or does not; there is no
// "current row" distinction to filter on the way every nextSiblingPosition
// caller's table has. Same FR7 shape otherwise: COALESCE(MAX(position),
// -1) + 1 over the parent's siblings, read inside the same transaction as
// the INSERT that follows.
func nextSiblingPositionPlain(ctx context.Context, q txQuerier, table, parentColumn string, parentID, scopeID uuid.UUID) (int, error) {
	var position int
	err := q.QueryRow(ctx, fmt.Sprintf(
		`SELECT COALESCE(MAX(position), -1) + 1 FROM %s WHERE %s = $1 AND scope_id = $2`,
		table, parentColumn,
	), parentID, scopeID).Scan(&position)
	if err != nil {
		return 0, fmt.Errorf("next sibling position in %s: %w", table, err)
	}
	return position, nil
}
