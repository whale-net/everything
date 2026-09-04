# app-registry-migration

Schema migration runner for the App Registry database. Built in **AR-1**.

Not yet implemented.

## Pattern

Follows `manmanv2/migrate` in spirit — embedded SQL plus `libs/go/migrate` —
with one deviation: the `//go:embed` lives in a small `schema` sub-package
(`schema.Migrations` / `schema.Dir`) rather than directly in `main.go`, so the
Postgres integration tests under `server/repository/postgres` can apply the
same real migrations instead of duplicating the SQL as hand-written DDL.

```go
// migrate/schema/schema.go
//go:embed migrations/*.sql
var Migrations embed.FS
const Dir = "migrations"

// migrate/main.go
func main() { migrate.RunCLI(schema.Migrations, schema.Dir) }
```

Deployed as a Helm pre-sync job (`app_type: job`), ordered ahead of the API via
ArgoCD sync waves. See `friendly_computing_machine/docs/argocd-integration.md`.

## Planned migrations

| File | Contents |
|---|---|
| `001_initial_schema` | `app`, `chart`, `chart_app`, `build`, `artifact`, `artifact_link`, `idempotency_key`, `domain_adoption` |
| `002_environment_registry` | `environment`, seeded with `dev`/`stage`/`prod` |
| `003_promotion` (AR-3c) | `promotion` (SCD2), `promotion_event`, `v_current_promotion` |
| `004_writeback_outbox` (AR-4b) | `writeback_outbox` |
| `005_version_allocation` (AR-5a) | `artifact.version_major/minor/patch` + backfill, `artifact_version_order_idx`, `version_allocation` |
| `006_reconcile_watermark` (issue #545) | `reconcile_watermark` — singleton row guarding `ReconcileApps` against a stale (older-commit) call landing after a newer one |
| `007_artifact_lifecycle` (AR-7b, issue #558) | `artifact.state`/`provenance`/`version_source`/`state_changed_at`/`fail_reason`, nullable `digest`/`build_id`/`published_at`, `artifact_state_shape` CHECK, partial-unique `artifact_digest_idx` (`WHERE digest IS NOT NULL`), `artifact_state_idx` (reaper sweep), `version_allocation` folded into `artifact` as `state = 'allocated'` and dropped |
| `008_app_identity_split` (AR-7c, issue #558) | Append-only `app_manifest`/`chart_manifest` snapshots (`(owner_id, source_git_sha)` unique, verbatim protojson `JSONB`; `app_manifest` alone gets stored generated `deploy_unit`/`image_repository` columns — `chart_manifest` gets neither, see the migration's own comments); `artifact.manifest_id` (no FK — polymorphic) and `artifact.promotability` (stored, `artifact_promotability_shape` CHECK ties it to `state = 'published'`); `app`/`chart` lose their mutable metadata columns, becoming pure identity; `v_current_app`/`v_current_chart` views (`v_current_promotion`'s pattern). Backfills one snapshot per existing app/chart row and `artifact.promotability` for every existing published row before dropping the columns those are sourced from. |
| `009_idempotency_key_method` (issue #575) | `idempotency_key`'s primary key moves from `(idempotency_key)` alone to `(idempotency_key, method)`, matching `Get` now taking `method` as a required parameter — see ARCHITECTURE.md "Idempotency" → "Fixed: cross-method replay via a reused key (issue #575)". No backfill needed: every pre-migration row's key was already globally unique, so it stays unique alongside its own method. |
| `010_manifest_history` (AR-8, issue #587) | Splits migration 008's per-commit `app_manifest`/`chart_manifest` snapshot tables into content-addressed `app_manifest`/`chart_manifest` (one row per DISTINCT manifest per owner, ever), SCD2 `app_manifest_history`/`chart_manifest_history` (the `main` sweep timeline, `ReconcileApps`-only), `app_manifest_release`/`chart_manifest_release` (release-time observations, `AssertApps`-only), and `reconcile_run` (one row per sweep, replacing the old one-row-per-app-per-sweep record). `v_current_app` becomes a point lookup against `app_manifest_history`'s partial index instead of a `LEFT JOIN LATERAL ... ORDER BY ... LIMIT 1` scan; `v_current_chart` is unchanged (never joined `chart_manifest` to begin with). `artifact.manifest_id` is remapped in place from the old per-commit snapshot id to the new content id. Row count now scales with manifest CONTENT change, not `main` commit frequency — see issue #587/#581. |
| `011_binary_firmware_artifacts` (issue #703) | Extends `artifact.kind` CHECK constraint to include `binary` and `firmware` artifact kinds, and updates `artifact_owner_matches_kind` to associate them with `app_id`. |
| `014_drop_stored_promotability` (issue #833) | Drops `artifact.promotability` and its `artifact_promotability_shape` CHECK (added in `008`) — reverses AR-7c's "store once at publish time" for promotability ONLY (`artifact.manifest_id` is untouched). `postgres/artifact.go`'s read paths now derive `Promotability` live via a join to the owning app's/chart's CURRENT `deploy_unit` (`v_current_app`/`v_current_chart`, plus an `app_manifest_release` fallback for owners never swept by `ReconcileApps`) and `repository.DerivePromotability`, instead of reading a stored value. No backfill needed for existing rows — they resolve correctly automatically on their next read. See `architecture/08-release-lifecycle/02-manifest-snapshot.md` "As built (issue #833, migration `014`)" for the full history. (Migrations `012`/`013`/`015`-`019` are not yet documented in this table — a pre-existing gap, out of scope here.) |
| `020_promotion_sync_event` (issue #1028, FR6, NFR4, NFR5) | Adds `promotion_sync_event`: append-only ArgoCD sync/health observation log, `promotion_id` FK, `source` CHECK (`refresh_triggered`/`poll_observed`/`retry_triggered`/`retry_observed`), `sync_status`/`health_status` free text, `promotion_sync_event_promotion_id_idx`/`promotion_sync_event_occurred_at_idx` (mirroring `promotion_event`'s pair from `003`). NOT SCD2 — see that migration's own doc comment. Schema-and-repository-only: nothing writes real rows yet, that's a later task once the poll/retry activities exist. |
| `021_writeback_outbox_result` (issue #1029, FR7a) | Adds `writeback_outbox.location`/`commit_sha` (`TEXT NOT NULL DEFAULT ''`) — what `GitOpsActivities.Publish` actually produced, set by `RecordWritebackResult` after `Publish` succeeds. Distinct write from `MarkDone` (fires when the workflow *starts*, not when `Publish` *completes*); `commit_sha` stays `''` on the no-op `Skipped` path and on `StubActivities`' no-git dev/test path. |
| `024_drop_domain_adoption` | Drops `domain_adoption` — `AllocateVersion`, `CheckChartHermeticity`, and `RecordArtifact` no longer branch on a per-domain stage; every domain is unconditionally allocated. |

Split this way so AR-2 needs only `001`, AR-3b adds `002`, AR-3c adds `003`,
AR-4b adds `004`, and AR-5a adds `005` — each phase ships an independently
applicable migration. `002` was originally planned to also carry `promotion`,
but AR-3b's scope is environments only (no promotion logic), so that table
moved out to its own migration in AR-3c, which needs
`environment.environment_id` to already exist as an FK target.

AR-4b and AR-5a were developed in parallel and both initially claimed `004`.
AR-4b is the lower branch in the stack, so it kept `004` and AR-5a renumbered
to `005`. Migration numbers must be unique: golang-migrate fails on a
duplicate version at **deploy** time, not build time, so a collision is
invisible to CI.

**Numbering note (AR-7b):** PLAN-HISTORY.md's AR-7 section names this
migration `007_artifact_lifecycle` throughout, and that is exactly what it
is numbered here too — `006` was already taken by `006_reconcile_watermark`
(issue #545, landed between the AR-7 design session and AR-7b's
implementation), so there was no free `006` to reuse and no renumbering was
needed. Called out explicitly because AR-4b/AR-5a's `004` collision above is
the kind of mistake this note exists to prevent a future phase from
repeating.

## Required indexes

Do not omit these; they are load-bearing, not optimizations.

```sql
-- makes double-promotion structurally impossible, and is the hot read path
CREATE UNIQUE INDEX promotion_current_idx
  ON promotion (environment_id, target_key)
  WHERE valid_to IS NULL;

-- historical "state at time T" queries
CREATE INDEX promotion_window_idx
  ON promotion (environment_id, target_key, valid_from DESC);

-- artifact digest index. As of migration 013 (issue #784), digests are unique within each
-- (owner, kind, major, minor) series, preventing redundant patch releases from recording identical
-- digests while allowing distinct major or minor releases (e.g. v0.1.5 and v0.2.0) to share content digests.
CREATE UNIQUE INDEX artifact_digest_major_minor_idx ON artifact (owner_id, kind, version_major, version_minor, digest) WHERE digest IS NOT NULL;
CREATE INDEX artifact_digest_idx ON artifact (digest) WHERE digest IS NOT NULL;

-- version allocation collision guard (AR-5 depends on this). As of
-- migration 007, this ALSO spans allocated/publishing/failed rows (the
-- version_allocation table it used to share this job with is gone -- see
-- "Planned migrations" above), so it now does double duty as both the
-- published-artifact collision guard and the allocation-collision guard.
CREATE UNIQUE INDEX artifact_version_idx ON artifact (owner_id, kind, version);

-- numeric ordering for "latest"/"next" -- TEXT ordering on `version` is
-- lexical and wrong ("v1.10.0" sorts before "v1.9.0"); see the AR-5
-- addendum in ARCHITECTURE.md's "Version model" (architecture/04-version-model.md)
-- and migration 004's comments.
CREATE INDEX artifact_version_order_idx
  ON artifact (owner_id, kind, version_major DESC, version_minor DESC, version_patch DESC);

-- AR-7b (migration 007): backs the stale-row reaper's sweep
-- ("state IN ('allocated','publishing') AND state_changed_at < cutoff") --
-- see ENV.md's ARTIFACT_REAPER_* variables and worker/README.md. Partial so
-- it only ever indexes the small, transient set of in-flight rows,
-- regardless of how large the published/failed tail of the table grows.
CREATE INDEX artifact_state_idx ON artifact (state, state_changed_at)
  WHERE state IN ('allocated', 'publishing');

-- AR-7c (migration 008): backs v_current_app's/v_current_chart's "latest
-- main-sweep snapshot per owner" LATERAL join -- the hot path every
-- ListApps/GetApp/ListCharts read, and every RecordBuild/RecordArtifact/
-- BeginPublish owner-repository lookup, now goes through.
CREATE INDEX app_manifest_current_idx
  ON app_manifest (owner_id, provenance, source_committed_at DESC, recorded_at DESC);
CREATE INDEX chart_manifest_current_idx
  ON chart_manifest (owner_id, provenance, source_committed_at DESC, recorded_at DESC);
```

`reconcile_watermark` (`006`) is seeded with exactly one sentinel row at
migration time (`id = 1`, `git_sha = ''`, timestamps `0`) rather than left
empty — `SELECT ... FOR UPDATE` locks only rows it matches, so a genuinely
empty table gives two concurrent "first ever reconcile" calls nothing to
serialize against. See the migration's own comments for the full rationale
and how the sentinel row is distinguished from a "real" watermark.

`domain_adoption` existed from migration `001` through migration `023` —
one row per domain, with a stage of `observe` / `promote` / `allocate`,
gating recording/promotion/allocation per domain (see
[ARCHITECTURE.md "Resolved questions"](../architecture/19-resolved-questions.md)
for the original design). Every domain was made unconditional instead, so
the table had nothing left to gate; migration `024` drops it.

See the SCD2 section of [ARCHITECTURE.md's "Data model"](../architecture/03-data-model.md#scd2-on-promotion)
and the repo-wide convention in `AGENTS.md`.
