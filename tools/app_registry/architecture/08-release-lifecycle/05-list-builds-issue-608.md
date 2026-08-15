# ListBuilds (issue #608)

`ArtifactRegistry.ListBuilds` browses `build` (migration 001, no schema
change): "what has CI actually built recently" for an operator scanning
history rather than looking up one known run — additive to
`GetReleaseRun`/`app-registry builds status` above, which stay a point
lookup for a single `workflow_run_id`. Ordered most-recent-`recorded_at`-first,
tie-broken by `build_id` (`ORDER BY recorded_at DESC, build_id DESC`); an
optional `since` (Unix timestamp) filters to `recorded_at >= since`.

**Deliberately `recorded_at`, not `started_at`.** `build.started_at` is
nullable and caller-supplied (`RecordBuild`'s handler only sets it when
given a nonzero value); `recorded_at` is `NOT NULL DEFAULT NOW()`,
server-set. Ordering or filtering on `started_at` would sort `NULL` rows
first (wrong) and would let a row with no `started_at` never satisfy a
`since` filter, permanently breaking plan #601's NFR2 poll-safety guarantee
for that row. This was an explicit architect/owner decision on the root
plan, not a default that happened to be convenient.

**Real pagination, same shape as `ListReconcileRuns` (issue #607) above** —
`page_size`/`page_token` drive an actual `LIMIT` and a keyset predicate,
`(recorded_at, build_id) < (cursor_ts, cursor_id)`, matching the `DESC,
DESC` ordering; the server fetches `page_size + 1` rows to detect a next
page without a separate `COUNT(*)`, reusing the same opaque cursor codec
(`postgres/keyset_cursor.go`'s `encodeKeysetCursor`/`decodeKeysetCursor`,
and `fake/keyset_cursor.go`'s independent same-shape codec for the
in-memory `fake` implementation). `page_size <= 0` falls back to the same
server default of 50 as `ListReconcileRuns`, kept consistent across both
real-pagination RPCs. `build.recorded_at` has no supporting index and
`PageResponse.total_size` is a page-local count, not a true total across
pages — see "ListReconcileRuns (issue #607)" above for both notes in full;
the same deliberate tradeoff applies here unchanged.

