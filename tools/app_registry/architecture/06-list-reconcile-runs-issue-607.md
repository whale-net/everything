# ListReconcileRuns (issue #607)

`AppRegistry.ListReconcileRuns` browses `reconcile_run` (migration 010,
AR-8 — see "Data model" above): "which commits were actually reconciled, and
when" for an operator confirming a release landed (see OPERATIONS.md
"browsing reconcile history"). Ordered most-recent-`applied_at`-first,
tie-broken by `reconcile_run_id` (`ORDER BY applied_at DESC,
reconcile_run_id DESC`); an optional `since` (Unix timestamp) filters to
`applied_at >= since`.

**Real pagination**, not a client-side slice: `page_size`/`page_token` drive
an actual `LIMIT` and a keyset (seek) predicate, `(applied_at,
reconcile_run_id) < (cursor_ts, cursor_id)`, matching the `DESC, DESC`
ordering above. The server fetches `page_size + 1` rows to detect whether a
next page exists without a separate `COUNT(*)`; the opaque cursor
(`postgres/keyset_cursor.go`'s `encodeKeysetCursor`/`decodeKeysetCursor`)
encodes the last returned row's `(applied_at, reconcile_run_id)`. A
malformed `page_token` is rejected with `ErrInvalidArgument`. `page_size <=
0` falls back to a server default of 50 — at the time this RPC was built,
there was no existing `ListPromotionEvents`/`ListArtifacts` precedent to
follow (both still read their whole result set unpaginated), so this was a
fresh, deliberate choice for this package's first RPC with real pagination;
#603 later closed that gap for the other two, reusing this same default —
see "ListPromotionEvents/ListArtifacts/ListPromotions real pagination (issue
#603)" below. The in-memory
`fake` implementation (used by handler-level tests) mirrors the same
ordering and keyset semantics with its own same-shape, independent cursor
codec (`fake/keyset_cursor.go`) — only its own round-trip needs to hold,
never cross-compatibility with postgres's token format.

**`reconcile_run.applied_at` has no supporting index.** At
current/foreseeable sweep volume (~380/yr — one `main`-push reconcile per
day, roughly), an unindexed `ORDER BY applied_at DESC, reconcile_run_id DESC
LIMIT ...` full sort is fine by deliberate choice, not an oversight. An
index is the natural follow-up if volume grows or a future monitoring
consumer's poll frequency (see plan #601's NFR2 — the stable, poll-safe
ordering this pagination shape exists to give such a consumer) makes the
full sort measurably expensive.

**`PageResponse.total_size` is a page-local count**, not a true total row
count across all pages: `len(reconcile_runs) <= page_size` on this and every
other RPC using this same pagination shape. Real (LIMIT-based) pagination
means the server no longer reads the whole table to answer the request, so
an accurate total would cost a separate `COUNT(*)` over an ever-growing
table for a field no caller (CLI or otherwise) reads today.

