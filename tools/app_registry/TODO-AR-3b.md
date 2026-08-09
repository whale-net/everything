# TODO-AR-3b — EnvironmentRegistry

Execution tracking for AR-3b (see [PLAN.md](PLAN.md) → "AR-3 — Promotion").
Scope: `EnvironmentRegistry` (all RPCs), `dev`/`stage`/`prod` seeding,
environment-scoped `allowed_principals`. No promotion logic — `promotion`
table and `PromotionRegistry` stay out of scope for AR-3c.

## Done

- [x] Migration `002_environment_registry` (up/down) in
      `migrate/schema/migrations/`: `environment` table per
      ARCHITECTURE.md's Data model — `key` unique, `rank` (plain INTEGER,
      not unique — nothing requires distinct ranks), `requires_approval`,
      `gitops_path`, `allowed_principals TEXT[] NOT NULL DEFAULT '{}'`,
      `archived`, `created_at`. Seeds `dev`(0)/`stage`(10)/`prod`(20) with
      `requires_approval = false` for all three (the approval gate is
      unimplemented; a future admin turns it on explicitly once it exists).
      Seed insert uses `ON CONFLICT (key) DO NOTHING` so it is safe to apply
      to a database an operator already touched. **Migration-based seeding**
      was chosen over server-startup seeding — see "Seeding decision" below.
      Verified with a real Postgres container: up (1→2), down (2→1, table
      dropped, `app`/`chart` from 001 intact), and re-up all succeed; see
      report for exact output.
- [x] `migrate/README.md`'s "Planned migrations" table corrected: `002` was
      originally documented as carrying `promotion` too; AR-3b's actual
      scope is environments only, so `promotion`/`promotion_event`/
      `v_current_promotion` moved to their own `003` (AR-3c), and
      `writeback_outbox` shifted to `004` (AR-4).
- [x] `repository.Environment` model + `EnvironmentRepository` interface
      (`Upsert`, `Get`, `List`, `Archive`) + `Registry.Environments()`.
- [x] `postgres/environment.go`: `environmentRepo` implementing the
      interface. `Upsert` looks up by key first (insert if absent, update
      every field but `Key`/`Archived` if present); `Archive` is
      archived→archived idempotent no-op, matching `SetAppStatus`'s shape.
      Normalizes a nil `AllowedPrincipals` to `[]string{}` before insert —
      pgx encodes Go `nil` slices as SQL `NULL`, which the `NOT NULL` column
      rejects; caught by the integration test, see report.
- [x] `fake/fake.go`: `environmentFake` (a distinct type wrapping
      `*Registry`, NOT methods directly on `Registry`) implementing the same
      interface over `state.Environments`. Required because
      `EnvironmentRepository.Get`/`List` collide by name with
      `IdempotencyRepository.Get`/other methods already on `Registry` — Go
      has no overloading. `WithTx` snapshotting covers it automatically
      since it's still `r.state` underneath.
- [x] `server/handlers/environment.go`: all four `EnvironmentRegistry` RPCs
      implemented against `repository.Registry`. `UpsertEnvironment`/
      `ArchiveEnvironment` → `auth.RoleAdmin`; `GetEnvironment`/
      `ListEnvironments` → `auth.RequireAuthenticated`. Neither write RPC
      carries an `idempotency_key` in the proto (confirmed against
      `api_messages_environment.proto`), so neither routes through
      `runIdempotent` — same shape as `AppRegistry.SetAppStatus`, the other
      admin-only write.
- [x] `server/handlers/convert.go`: `environmentToPB`/`environmentsToPB`.
- [x] `server/main.go`: `NewEnvironmentServer(repo)` now takes the real
      repository; doc comments updated to say EnvironmentRegistry is real.
- [x] `server/main_test.go`: `TestRegisterServices_AllFourServicesReachable`'s
      `EnvironmentRegistry` subtest now asserts a real empty-but-successful
      `ListEnvironments`, matching the App/Artifact subtests, plus a new
      `ArchiveEnvironment` NotFound check. `PromotionRegistry` stays the only
      genuinely-`Unimplemented` subtest.
- [x] Tests: `server/handlers/environment_test.go` (create/update, rank
      ordering, `include_archived`, archive idempotency, not-found paths)
      and the AR-3a authorization triple for `UpsertEnvironment`/
      `ArchiveEnvironment` in `authz_test.go` (admin allowed, builder →
      `PermissionDenied`, no claims → `Unauthenticated`) plus a
      `RequireAuthenticated`-only pair for the two reads.
- [x] `server/repository/postgres/postgres_integration_test.go`: added
      `TestMigration002SeedsDevStageProd` (proves 002 applies on 001 and the
      seed data lands), `TestEnvironment_KeyUniqueConstraint` (raw INSERT
      proving the real `UNIQUE (key)` index rejects a duplicate, not app
      logic), and `TestEnvironmentRepo_UpsertCreateThenUpdate` (repository
      layer round-trip including `TEXT[]` `allowed_principals`).
- [x] BUILD.bazel: added `environment.go`/`environment_test.go` to the
      relevant `go_library`/`go_test` srcs; added `@com_github_jackc_pgx_v5//pgconn`
      to the hand-written `postgres_integration_test` target's deps (gazelle
      skips this target — `# keep` marker, see its own comment).
      `bazel run //:gazelle` was run once; every out-of-scope file it
      reformatted was reverted, keeping only the `app_registry` deps it
      added (none, in the end — no new import needed gazelle's help).

## Seeding decision

**Migration, not server startup.** `INSERT ... ON CONFLICT (key) DO NOTHING`
inside `002_environment_registry.up.sql`:

- It is a one-shot schema-migration operation like every other seed in this
  repo (`domain_adoption` has no seed rows, but the *pattern* of "DDL +
  bootstrap data in the same migration" is standard golang-migrate practice
  here).
- `ON CONFLICT DO NOTHING` makes it idempotent and non-destructive: if `dev`
  already has admin-edited `allowed_principals` by the time this runs (e.g.
  an operator called `UpsertEnvironment` by hand before upgrading, or the
  migration is retried after a partial failure), the seed is skipped
  entirely rather than clobbering that edit.
- Server-startup seeding would need its own idempotency/locking story (what
  if two API replicas start concurrently?) that migrations already solve for
  free via `golang-migrate`'s advisory lock — reinventing that in Go for no
  benefit.
- This is exactly the same trade PLAN.md already made for `domain_adoption`
  in migration `001`: ship the row so no consumer is ever missing one, at
  migration time, without a redundant application-layer bootstrap path.

This matters here because, per PLAN.md, the registry may already be deployed
to `dev` by the time this migration ships — the ON CONFLICT clause is what
makes that safe.

## Verification

```
bazel test //tools/app_registry/...                                                              # 4/4 pass
bazel test //tools/app_registry/server/repository/postgres:postgres_integration_test --test_output=all  # pass (Docker)
bazel build //tools/...                                                                          # green
```

Migration round-trip verified against a real Postgres container (not just
`bazel test`): `up` 0→2, `down -steps=1` 2→1 (environment table dropped,
`app`/`chart` intact), `up` again 1→2 (seed rows reappear). See phase report
for exact command transcript.

Deliberate-break verification (per AR-2d's lesson): removed `UNIQUE (key)`
from the migration → every integration test failed at migration-apply time
("no unique or exclusion constraint matching ON CONFLICT specification");
changed the seeded `stage` rank from 10 to 15 → `TestMigration002SeedsDevStageProd`
failed with an exact mismatch message; reverted `updated.DisplayName`'s
assignment in the fake to a constant → `TestUpsertEnvironment_CreateThenUpdate`
failed. All three reverted and reconfirmed green. See phase report for
transcripts.

## Explicitly not done (AR-3c/3d)

- `PromotionRegistry` — still `Unimplemented`. SCD2 `promotion` table,
  `promotion_event`, `v_current_promotion`, promotability enforcement,
  `allow_override`/drift reporting all land in AR-3c's own migration `003`.
- No CLI (`app-registry environments ...`) or workflow changes — AR-3d.
- `environment.requires_approval` is stored and returned but not honored by
  anything yet (no promotion path exists to honor it) — unchanged from
  ARCHITECTURE.md's "Future: approval gate".
