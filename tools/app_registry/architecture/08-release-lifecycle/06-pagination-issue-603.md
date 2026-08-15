# ListPromotionEvents/ListArtifacts/ListPromotions real pagination (issue #603)

These three RPCs always accepted a `PageRequest` (`page_size`/`page_token`)
and returned a `PageResponse`, but until #603 no handler or repository
method actually enforced it — no `LIMIT`, no cursor, an unbounded query
every time, on tables (`promotion`, `promotion_event`, `artifact`) that grow
unboundedly. #607/#608 (`ListReconcileRuns`/`ListBuilds`) shipped the first
two real-pagination RPCs in this package; this closes the gap for the three
that predate them, reusing the exact same shape.

**Ordering, per RPC** — all three now drive a real `LIMIT` + keyset (seek)
predicate, fetching `page_size + 1` rows to detect a next page without a
separate `COUNT(*)`, via the same opaque cursor codec used by
`ListReconcileRuns`/`ListBuilds` (`postgres/keyset_cursor.go`'s
`encodeKeysetCursor`/`decodeKeysetCursor`, and `fake/keyset_cursor.go`'s
independent same-shape codec for the in-memory `fake`). `page_size <= 0`
falls back to the same server default of 50 as the other two RPCs. A
malformed `page_token` is rejected with `ErrInvalidArgument`.
`PageResponse.total_size` is a page-local count, not a true total across
pages — same tradeoff as `ListReconcileRuns`/`ListBuilds`.

- `ListPromotions`: `ORDER BY valid_from DESC, promotion_id DESC` (both
  `NOT NULL`).
- `ListPromotionEvents`: `ORDER BY occurred_at DESC, event_id DESC` (both
  `NOT NULL`).
- `ListArtifacts`: `ORDER BY state_changed_at DESC, artifact_id DESC`.
  **Deliberately `state_changed_at`, not `published_at`** — `published_at`
  is `NULL` for every artifact short of `published` (an allocated/
  publishing/failed row), and a `NULL` value can never satisfy the `<`
  keyset predicate a cursor relies on, which would silently drop those rows
  from every page after the first. `state_changed_at` is `NOT NULL` for
  every row regardless of state (migration 007's `artifact_state_shape`),
  so it is safe as a cursor column. This also retires `ListArtifacts`'s old
  `BuildID`-filtered special case, which already ordered by
  `state_changed_at` for exactly this reason (`GetReleaseRun`, AR-7d) — it
  is now just the general path.

**`ArtifactListFilter.PromotableOnly` moved from a client-side post-filter
into the SQL `WHERE` clause** (`AND a.promotability = 'promotable'`). Before
#603, `ListArtifacts` fetched the whole unfiltered result set and dropped
non-promotable rows after the fact; that composes fine with no pagination,
but would silently produce short pages under real (`LIMIT page_size + 1`)
pagination — the extra-row trick that detects "is there a next page" only
works if the `LIMIT` is applied to already-filtered rows.

None of these tables (`promotion`, `promotion_event`, `artifact`) have a
supporting index on the ordering column — same deliberate tradeoff as
`reconcile_run.applied_at`/`build.recorded_at` (see "ListReconcileRuns
(issue #607)" above): an unindexed `ORDER BY ... LIMIT` full sort is fine at
current/foreseeable volume, and an index is the natural follow-up if that
changes.

