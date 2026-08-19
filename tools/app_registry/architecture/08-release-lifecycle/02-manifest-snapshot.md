# App identity vs. per-build manifest snapshot

Today `app` carries *mutable metadata* — `deploy_unit`, `image_repository`,
`bazel_label`, `app_type`, description — and some ref has to author it. Every
answer to "which ref?" is bad: let any ref write it and it drifts; let only
`main` write it and releases depend on `main`'s CI (ordering 1). The question
is removed rather than answered:

- `app` / `chart` become **pure identity**: `(domain, name)`, `status`,
  first/last-seen provenance. Nothing mutable.
- The `AppManifest`/`ChartManifest` a run was built from is stored **verbatim**
  (protojson, `JSONB`) in an append-only `app_manifest` / `chart_manifest`
  snapshot table, keyed `(owner_id, source_git_sha)` — same commit, same
  manifest, so writes are naturally idempotent — with `source_committed_at`
  and whether it came from the `main` sweep or a release run.
- `artifact.manifest_id` records which snapshot an artifact was built from,
  and **`artifact.promotability` becomes a stored column** derived from that
  snapshot at publish time. *(Reversed by issue #833, migration `014` — see
  "As built (issue #833, migration `014`)" below. `manifest_id` remains
  stored/unchanged; only `promotability` went back to a live join.)*

Consequences, in order of importance:

1. **A release from any ref writes only facts.** Divergent branch, old tag,
   unmerged PR — none of them can write mutable state, so there is no drift to
   bound, observe, or correct, and identity assertion becomes safe from
   anywhere. This is what closes ordering 1 without the provisional-upsert
   trade #547 rejected.
2. **Promotability stops being retroactive.** `RecordArtifact` currently
   re-reads the owner's *current* `deploy_unit` (`postgres/artifact.go`'s
   "re-derive promotability against the freshly-read owner deploy_unit"), so
   editing a `release_app` rule today silently changes the promotability of
   artifacts published years ago. Deriving from the build's own snapshot is a
   correctness fix, not a refactor. *(No longer true as of issue #833 — see
   "As built (issue #833, migration `014`)" below: this consequence was
   judged, in production, to be a worse tradeoff than the retroactivity it
   was meant to prevent.)*
3. **The `main` sweep shrinks to what only it can do**: existence and absence.
4. **"Value at time T" without SCD2 on `app`.** The per-build snapshot *is*
   the history, append-only, matching how the rest of this schema works. (This
   is also why #553's answer — `chart`/`chart_app` are not SCD2 and don't need
   to be — still holds.)

Cost: reads that want "what does this app look like today" need the newest
`main` snapshot, via a `v_current_app` view (identity ⋈ latest `main`-sweep
snapshot), the same pre-joined-view pattern `v_current_promotion` uses.
`ListApps`/`GetApp` responses are unchanged on the wire.

**Storage: verbatim protojson `JSONB`, plus stored generated columns for the
fields the hot paths read** (`deploy_unit`, `image_repository`) — decided in
review of PR #559. The JSONB stays the single source of truth, so a new
appmeta field needs no migration and cannot drift from the manifest; the
generated columns keep `v_current_app` and promotability derivation ordinarily
indexable instead of resting on `->>` expression indexes. Fully typed columns
were rejected: they would reintroduce the hand-maintained duplicate of the
manifest schema that AR-M spent a phase deleting.

**As built (AR-7c, migration `008`).** Everything above is real. One
narrowing the design left implicit: **`chart_manifest` gets NEITHER
generated column**, not "the same two." `ChartManifest` (appmeta.proto)
carries no `deploy_unit` field at all — a chart's own deploy_unit was
always the hardcoded `DEPLOY_UNIT_CHART` constant (Reconcile's INSERT, both
before and after this migration), never sourced from a manifest — and no
image-repository triple either, so there is no hot path that needs either
column on that table; `v_current_chart` hardcodes
`deploy_unit`/`description`/`chart_repository` as constants instead of
projecting them off `manifest_json`, matching what those columns already
were (dead/constant) before this migration. `artifact.manifest_id` carries
no FK, for the same reason `artifact.owner_id` doesn't: it is polymorphic
(an image artifact's names an `app_manifest` row, a chart artifact's a
`chart_manifest` row), so referential integrity is enforced in Go
(`postgres/artifact.go`'s `resolveManifestForPublish`, always run inside the
same transaction as the write) rather than by the schema.
`resolveManifestForPublish` prefers the snapshot at the artifact's own
build's EXACT `git_sha` (typically the one `AssertApps` just wrote for this
release), falling back to the newest snapshot for the owner on any commit
when no exact match exists — a deliberate simplification: "derived at
publish time" means "from the best snapshot known at publish time," not "the
exact build commit, or fail" (requiring an exact match would make every
domain's first post-AR-7c publish fail until `AssertApps` runs for it once).
Migration 008's backfill also computes `artifact.promotability` for every
row that predates it, from the CURRENT (about-to-be-dropped)
`app`/`chart.deploy_unit` — the last time this repo ever computes
promotability via a live join; those rows' `manifest_id` is left `NULL`
(no snapshot honestly represents what was live when they actually
published).

**As built (AR-8, migration `010`, issue #587).** Migration 008's
`app_manifest`/`chart_manifest` wrote one row per owner on EVERY sweep,
unconditionally — ~13.6k rows/year in this repo, ~99% byte-identical to
their predecessor (issue #581). Migration 010 splits content from timeline,
replacing those two tables with four per side:

- `app_manifest`/`chart_manifest` are now **content-addressed**: one row per
  DISTINCT manifest per owner, ever, never updated after insert
  (`UNIQUE (owner_id, manifest_hash)`, `manifest_hash` a `GENERATED ALWAYS AS
  (md5(manifest_json::text)) STORED` column). The same two generated columns
  from migration 008 (`deploy_unit`/`image_repository`, app-side only) ride
  along unchanged.
- `app_manifest_history`/`chart_manifest_history` are the `main` timeline —
  SCD2 per `AGENTS.md` (`valid_from`/`valid_to`), written ONLY by
  `ReconcileApps` (`postgres/app.go`'s `recordAppManifestSweep`/
  `recordChartManifestSweep`): a sweep whose content hasn't changed since
  the owner's currently-open interval writes ZERO new rows (only
  `last_git_sha` advances); a genuine change closes that interval and opens
  exactly one new one. A partial unique index
  (`WHERE valid_to IS NULL`) backs `v_current_app`'s lookup — a point read
  instead of migration 008's `LEFT JOIN LATERAL ... ORDER BY ... LIMIT 1`
  scan over an ever-growing table. `v_current_chart` needed no change: it
  never joined `chart_manifest` to begin with (see "Why chart_manifest has
  no generated columns" above).
- `app_manifest_release`/`chart_manifest_release` are release-time
  observations, written ONLY by `AssertApps` — this is what keeps
  `resolveManifestForPublish`'s exact-`git_sha` preference working
  (`postgres/artifact.go`'s `currentAppManifest`/`currentChartManifestID` now
  check this table first, then the owner's current history interval by
  git_sha, then the current interval regardless of commit — the same
  three-tier fallback migration 008 described as two-tier, now split across
  two tables instead of one column). `AssertApps` NEVER writes to
  `*_manifest_history` — identity assertion from a divergent ref still can't
  perturb what `main`'s sweep considers current, same guarantee migration
  008 established, now enforced by table separation rather than a filtered
  read.
- `reconcile_run` replaces the per-app-per-sweep record with one row per
  sweep that actually applies (not a dry run, not rejected as stale by the
  watermark) — `git_sha`/`source_committed_at`/`applied_at`/`apps_seen`/
  `charts_seen`. Answers "which commits were reconciled," which
  `reconcile_watermark` (a singleton with no history) never could.

`valid_from`/`valid_to` use the commit's committer time
(`to_timestamp(source_committed_at)`), not wall-clock `NOW()` — falling back
to wall clock only for the `source_committed_at = 0` sentinel (an
unresolved-at-record-time commit, migration 006's convention) — so "value at
time T" answers what was actually in the tree, not when CI happened to run.
The classic SCD2 pitfall this design avoids: content-dedupe alone (a
`first_seen`/`last_seen` pair on the content row, no separate history table)
breaks on **A → B → A** — editing a `release_app` attribute and then
reverting it, which is normal, not exotic — because content-only dedup has
no way to represent "A was current, then wasn't, then was again" without
either merging the two A-periods into one interval spanning B (wrong) or
losing the second occurrence entirely. A separate history table represents
A → B → A as three non-overlapping intervals in commit order, which is the
truth; `postgres_integration_app_test.go`'s
`TestReconcile_AThenBThenAProducesThreeNonOverlappingIntervals_Postgres`
proves this directly against real Postgres.

`artifact.manifest_id` keeps its exact shape (still a polymorphic,
non-FK `UUID`) but now names an immutable CONTENT row instead of a
per-commit snapshot row — migration 010 remaps every pre-migration
`manifest_id` from its old per-commit id to the new content id sharing the
same `(owner_id, manifest_json)`, in place. Many artifacts built from
byte-identical manifests now legitimately share one `manifest_id`; that is
content-addressing's natural consequence, not a signal to read anything
into.

**As built (issue #833, migration `014`).** AR-7c's "store once, no live
join" tradeoff for `artifact.promotability` (consequence #2 above) is
reversed. `tools-app-registry`'s two `published` binary artifacts (`v0.2.1`,
`v0.2.2`, published 2026-08-16) were permanently stranded on
`promotability = not_promotable` after #810 fixed `DerivePromotability`
(binary artifacts should always be `PROMOTABLE` regardless of `deploy_unit`)
on 2026-08-17 — the fix could never reach rows published before it landed,
because the value was frozen at publish time and nothing ever recomputed
it. Every future rule fix or `deploy_unit` correction would strand a new
batch of rows the same way, each needing its own one-off manual DB
backfill — there is no general "fix forward" for a stored value. Staleness
under rule/config changes was judged worse than the extra read-time join
this reintroduces, so:

- `artifact.promotability` and its `artifact_promotability_shape` CHECK
  constraint (migration 008) are dropped. `artifact.manifest_id` is
  untouched — it remains stored, still recording build-commit provenance
  (which content an artifact was published against), a genuinely historical
  fact rather than a live-changing property of the owner's current state.
- `postgres/artifact.go`'s `scanArtifact` derives `Promotability` on every
  read: `artifactSelectBase` joins `v_current_app`/`v_current_chart` (the
  same views `ListApps`/`GetApp`/`ListCharts` already read) and calls
  `repository.DerivePromotability(kind, deployUnit)` in Go, for `published`
  rows only (matching the dropped column's old nullability).
  `resolveManifestForPublish` (and `insertArtifact`/`completePublish`/
  `completeAdoption`) no longer resolve or write a `promotability` value at
  all — only `manifest_id`.
- **`v_current_app` alone is not enough.** It deliberately reads only
  `provenance = 'sweep'` snapshots (AR-8's "what does this app look like
  today is a main-tree question") and defaults to `'chart'` when no sweep
  snapshot exists yet — correct for `ListApps`/`GetApp`, but it would have
  silently broken AR-7c's own exit criterion (issue #547,
  `TestAssertApps_ThenRecordArtifact_NoReconcileNeeded_Postgres`): a release
  from a ref that never merges — `AssertApps` only, `ReconcileApps` never
  having run for that owner — must still get a *correct* promotability from
  its *own* `deploy_unit`, not a `'chart'` default manufactured by the
  absence of sweep data. `artifactSelectBase` therefore adds a
  `app_release_fallback` `LATERAL` join: the most recently recorded
  `app_manifest_release` observation for the owner, consulted (via a
  `NOT EXISTS` guard) only when no current sweep interval exists yet. An
  owner that HAS been reconciled always uses the live sweep value — edits
  are picked up on the next read, exactly as this issue intends. An owner
  that has ONLY ever been asserted falls back to its own release content
  instead of a meaningless default. No equivalent fallback is needed on the
  chart side: `v_current_chart.deploy_unit` is a hardcoded `'chart'`
  constant that never depends on manifest content existing at all.
- `fake.Registry` mirrors this with `livePromotability`, applied at every
  point an `Artifact` leaves the registry (`findArtifact`, `ListArtifacts`,
  `ResolveArtifact`, `ListArtifactPins`, and the `RecordArtifact`/
  `AdoptArtifact` idempotent-replay branches) instead of persisting the
  value at write time. The fake never modeled manifest snapshots at all
  (`state.Apps[id].DeployUnit` is written directly by both `Reconcile` and
  `AssertApps`), so it needs no equivalent of the release-fallback join —
  an `AssertApps`-only app's `DeployUnit` is already live in `state.Apps`.
- `ListArtifacts`' `PromotableOnly` filter, previously `a.promotability =
  'promotable'`, is now a hand-inlined copy of `DerivePromotability`
  restricted to the `PROMOTABLE` outcome (`a.kind = 'binary' OR a.kind =
  'chart' OR (a.kind = 'image' AND` the same
  `COALESCE(app_release_fallback.deploy_unit, app.deploy_unit)` `= 'image')`),
  since there is no stored column left to filter on. Must be kept in sync
  with `promotability.go` by hand.
- `TestRecordArtifact_PromotabilityIsNotRetroactive[_Postgres]` — AR-7c's
  exit criterion, and until this issue the test proving the property this
  section describes — are replaced by
  `TestRecordArtifact_PromotabilityIsRetroactive[_Postgres]`, proving the
  opposite: editing an app's `deploy_unit` after publish DOES change what
  `GetArtifact` reads back for an artifact published before the edit.
- No backfill was needed for the stranded prod rows: deriving live means
  they resolve correctly automatically on their very next read, once this
  migration and code change ship.

