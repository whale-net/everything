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
  snapshot at publish time.

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
   correctness fix, not a refactor.
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

