package store

// This file is the shared "bounded by construction" idiom every list-shaped
// store method with a caller-supplied limit uses (issues #1808/#1812/#1813's
// follow-up: filtering and pagination belong in this layer, against
// Postgres, not re-implemented in Go over an unboundedly large fetch in
// mcp/tools). A method taking a `limit int` passes fetchLimit(limit) as its
// query's LIMIT parameter, then runs the returned rows through paginate
// before returning -- callers that want everything (no cap at all) pass
// limit <= 0 and get every matching row back, truncated always false.

// fetchLimit converts a caller-supplied limit into the *int64 a query
// passes as its LIMIT parameter. limit <= 0 (the "no limit" case) maps to
// nil -- Postgres treats a NULL LIMIT identically to omitting the clause
// entirely, so a NULL-safe `LIMIT $n` idiom works whether or not a cap was
// requested. Otherwise the query asks for one row past limit (limit+1) so
// paginate can tell "more rows exist beyond limit" from "limit was the
// exact count" without a second COUNT query.
func fetchLimit(limit int) *int64 {
	if limit <= 0 {
		return nil
	}
	n := int64(limit) + 1
	return &n
}

// paginate trims rows -- fetched with fetchLimit(limit) as the query's
// LIMIT -- down to at most limit, reporting whether the limit+1'th row was
// actually present (i.e. whether more matching rows exist beyond limit).
// limit <= 0 means the caller asked for everything: rows is returned
// unmodified and truncated is always false.
func paginate[T any](rows []T, limit int) (out []T, truncated bool) {
	if limit <= 0 || len(rows) <= limit {
		return rows, false
	}
	return rows[:limit], true
}
