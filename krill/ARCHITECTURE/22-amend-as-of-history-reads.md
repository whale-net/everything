# Amend and as-of history reads (FR11, FR12, issue #2493)

`krill/store/amend.go` (write) and `krill/store/history.go` (read) are the
SCD2 supersession pair `AGENTS.md`'s "SCD2" section describes, applied to
the two entity kinds this task scopes for amendment: `Requirement` (FR/NFR)
and `LoadBearingDecision`. No new migration lands with this task — every
column both files need was already present in migration 002 (see "What
this task does not build" above, now resolved).

**Amend (`AmendStore`, FR12) is the close-and-open write**, exactly the two
statements `AGENTS.md` names:

```sql
UPDATE <table> SET valid_to = NOW() WHERE id = $1 AND valid_to IS NULL;
INSERT INTO <table> (id, ..., scope_id) VALUES ($1, ..., $scope);
```

`AmendRequirement`/`AmendLoadBearingDecision` first `SELECT ... FOR UPDATE`
the current row inside the same transaction as the close-and-open pair —
this locks it for the transaction's duration so a concurrent amend of the
same `id` cannot race the `UPDATE` or the `(id) WHERE valid_to IS NULL`
partial unique index both amend and Create rely on. The new row carries
the closed row's `id`, `scope_id`, parent id (`feature_id` /
`feature_set_id`), `kind` (Requirement only), and `position` forward
unchanged — an amend replaces `name`/`body` only, never a parent id
(reparenting stays the separate, still-unbuilt operation store/decision.go's
`Create` doc comment describes) and never `position` (so no sibling's
rendered display number moves, per LB2). The surrogate `id` is never
reissued (LB2) and no sibling row of any kind is read or written by an
amend — only the one entity's own current-and-then-superseded rows.

**History reads (`HistoryStore`, FR11)** are the read side, over the same
two tables:

- **As-of** (`GetRequirementAsOf` / `GetLoadBearingDecisionAsOf`) is
  `AGENTS.md`'s "Value at time T" query verbatim:
  `WHERE id = $1 AND valid_from <= $2 AND (valid_to IS NULL OR valid_to > $2)`.
  Returns `ErrNotFound` if `asOf` predates the entity's first revision (or
  `id` never existed) — there is no row satisfying the interval in that
  case, never a zero-value success.
- **Version list** (`ListRequirementVersions` /
  `ListLoadBearingDecisionVersions`) returns every revision sharing `id`,
  oldest first (`ORDER BY valid_from`) — every prior version's own
  `ValidFrom`/`ValidTo` is exactly what a caller needs to see what
  superseded what, and when.

Neither `AmendStore` nor `HistoryStore` reads or writes anything beyond
`requirement`/`load_bearing_decision` *as history* — no other entity kind
is as-of-readable in this milestone. `AmendStore` itself has since been
generalised to every spec-axis kind, which is a supersession write only
and adds no as-of read: see
[`33-scd2-amend-all-spec-kinds.md`](33-scd2-amend-all-spec-kinds.md).

**As-of slice assembly (`krill/slice`).** Every one of C3's four
granularities (`GetFeatureSetSlice`, `GetFeatureSlice`,
`GetRequirementSlice`, `GetProductSlice`, issue #2491) has an `*AsOf` twin
(`GetFeatureSetSliceAsOf`, ..., `krill/slice/query.go`) that assembles the
same `Document` shape as of a past `asOf` instead of today: every
`Requirement`/`LoadBearingDecision` in the result is read through
`HistoryStore` (the revision current at `asOf`, not the latest), and any
entity whose first revision postdates `asOf` is dropped from the
assembly rather than reported at its current contents. `Product`,
`FeatureSet`, and `Feature` were not amendable when this was written, so
for those three "as of `asOf`" reduced to "had it been created by
`asOf`" (`entityExistedAsOf`) — the current row is their only revision, and a
top-level `*AsOf` call whose own entity postdates `asOf` returns
`store.ErrNotFound`, exactly like `HistoryStore`'s own not-found
semantics. `EntityRef.RevisionID` (`krill/slice/document.go`) is the
"as-of revisions" metadata PRODUCT.md's LB7 describes — an `*AsOf`
assembly's entities simply carry a historical row's `RevisionID` instead
of today's current row's. No new HTTP route exists for this yet — the
capability lives at the `slice.Querier` layer only, for a later task's
surface to wire up if needed.

**HTTP surface.** `krill/api/handlers/amend.go` wraps `AmendStore` behind
`RequireSession` (`routes.go`: `POST /requirements/{id}/amend`,
`POST /load-bearing-decisions/{id}/amend`) — one of this milestone's write
paths, exactly like entity create/attach. `krill/api/handlers/history.go`
wraps `HistoryStore` with no session gate at all (`GET
/requirements/{id}/as-of?at=<RFC3339>`, `GET /requirements/{id}/versions`,
and the `load-bearing-decisions` equivalents) — FR11 is a read path, and
read paths never require `init` (root plan issue #2485), exactly like
`krill/slice`'s four granularities.

**MCP surface.** `krill/mcp/tools/amend.go` exposes the same two writes as
`amend_requirement` and `amend_load_bearing_decision` on the `/mcp/design`
mount (`RegisterWrite`, `krill_session_id` required), taking the HTTP
body's `{name, body}` plus `id` and returning `handlers.IDResponse`. No MCP
twin exists yet for the history reads.
