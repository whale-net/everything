# TODO — AR-2d: Postgres repository test coverage

Execution tracking for AR-2d. See PLAN.md's AR-2d section for scope/exit
criteria. Delete this file when the phase's PR merges.

## Done

- [x] Made the real migrations reusable without duplicating SQL: moved
      `migrate/migrations/*.sql` to `migrate/schema/migrations/*.sql` behind a
      new `migrate/schema` package (`schema.Migrations embed.FS`,
      `schema.Dir`). `migrate/main.go` now calls
      `migrate.RunCLI(schema.Migrations, schema.Dir)`. This was necessary
      because `//go:embed` cannot reach outside its own package directory, and
      `migrate/main.go` is `package main` (not importable from a test
      package). `migrate/README.md` updated to describe the split.
- [x] `server/repository/postgres/postgres_integration_test.go`, gated behind
      `//go:build integration`, using `libs/go/dbtest.NewPostgres` for the
      container and `libs/go/migrate.NewRunner` + `schema.Migrations` to apply
      the real schema (not hand-written DDL).
- [x] Hand-written `go_test` target `postgres_integration_test` in
      `server/repository/postgres/BUILD.bazel` with the exact tag set from
      `//libs/go/dbtest:postgres_constraints_test` (external, integration,
      manual, no-cache, no-sandbox, requires-network) and a `# keep` marker.
      Verified `bazel run //:gazelle` leaves it untouched.
- [x] Four tests, each verified to fail when the behaviour it checks is
      deliberately broken (see PR description / report for exact output):
      1. `TestRecordArtifact_ChartLinkFailureRollsBackTransaction` — a
         mid-transaction constraint failure rolls back the whole
         `RecordArtifact` write, no partial artifact row survives.
      2. `TestRecordBuild_IdempotencyKeyReplay_DoesNotDoubleWrite` — a
         replayed call with a different payload returns the original stored
         response and does not touch the DB a second time.
      3. `TestRecordArtifact_DuplicateOwnerKindVersionRejectedByRealIndex` —
         the real `artifact_version_idx` unique index (not application logic)
         rejects a same-owner/kind/version, different-digest artifact.
      4. `TestResolveArtifact_ChartToImageJoin` — the real
         artifact → artifact_link → artifact → build join resolves correctly.
- [x] `bazel test //tools/app_registry/server/repository/postgres:postgres_integration_test --test_output=all`
      passes against real Postgres containers (Docker Desktop, WSL2).
- [x] `bazel test //tools/app_registry/...`, `bazel test //tools/...`, and
      `bazel build //tools/...` all stay green — the `manual` tag keeps the
      new target out of wildcard expansion.
- [x] `TESTING.md` updated with how to run the new target.
- [x] `PLAN.md`'s AR-2d section updated to note the schema-export detour and
      that the SCD2 partial-unique-index item is deferred (see below).

## Scope note: SCD2 partial index deferred

PLAN.md's original AR-2d scope listed "the SCD2 partial unique index" as a
priority-3 coverage target. The `promotion` table (and its
`WHERE valid_to IS NULL` partial index) does not exist yet — it ships in
migration `002` under AR-3, per `migrate/README.md`'s "Planned migrations"
table. There is no schema to test against yet. Covered the unique-index
requirement instead against `artifact_version_idx`, the one partial/unique
index AR-2's schema actually has, and flagged the SCD2 partial index as a
carry-over for whoever adds migration `002` in AR-3 — it should get the same
treatment `libs/go/dbtest`'s own PoC test already gives it in isolation.

## Not done / follow-ups

- No CI job invokes `postgres_integration_test` yet (PLAN.md's AR-2d scope
  mentions "a CI job... mirroring `test_cross_compilation`'s
  `setup-docker: 'true'` job"). Deliberately left for a separate PR/commit
  since it touches `.github/workflows/*` outside `tools/app_registry/` and
  the task scope for this change was `tools/app_registry/` (+ the
  `migrate/schema` split and `libs/go/dbtest` if strictly needed — no
  `libs/go/dbtest` changes were needed, it was reused as-is).
- AR-3's SCD2 promotion table has no test coverage yet — see above. Add a
  `TestPromotion_PartialUniqueIndexOnlyGuardsCurrentRow`-style test (mirrors
  `libs/go/dbtest`'s own PoC) once migration `002` lands.
