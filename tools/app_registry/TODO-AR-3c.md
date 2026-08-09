# TODO-AR-3c — PromotionRegistry

Execution tracking for AR-3c (see [PLAN.md](PLAN.md) → "AR-3 — Promotion").
Scope: `PromotionRegistry` (`Promote`, `Rollback`, `GetEnvironmentState`,
`ListPromotions`, `ListPromotionEvents`) — SCD2 close-and-open, event log,
promotability enforcement, `allow_override` + drift reporting.

## Done

- [x] Migration `003_promotion` (up/down) in `migrate/schema/migrations/`:
      `promotion` (SCD2 — `valid_from`/`valid_to`, partial unique index
      `promotion_current_idx ON promotion (environment_id, target_key)
      WHERE valid_to IS NULL`, plus `promotion_window_idx` for
      historical/`--at` reads), `promotion_event` (append-only, NOT SCD2 per
      AGENTS.md), `v_current_promotion` (pre-joins promotion → artifact →
      environment). FK to `environment(environment_id)` from AR-3b.
      `target_key` is `"<kind>:<owner_full_name>"`, denormalized so the
      partial unique index doesn't need a nullable two-column target.
- [x] `repository.Promotion` / `PromotionEvent` / `PromotionState` /
      `PromotionAction` models + `PromotionRepository` interface + `TargetKey`
      helper in `server/repository/models.go` and `repository.go`.
- [x] `postgres/promotion.go`: `promotionRepo`. `Promote` does the SCD2
      close-and-open as three statements (select current, close it, insert
      the new row); callers MUST wrap it in `Registry.WithTx` — verified by
      deliberately bypassing `WithTx` in a test and observing a partial
      write (see "Verification" below). Historical reads (`GetPrevious`,
      `StateAt(at != nil)`, `ListPromotions(include_history=true)`) query
      base tables joined the same way as `v_current_promotion`; pure current
      reads (`GetCurrent`, `StateAt(at == nil)`) query the view directly.
- [x] `fake/fake.go`: `promotionFake` (distinct type wrapping `*Registry`,
      matching `environmentFake`'s pattern) over `state.Promotions`/
      `state.PromotionEvents`.
- [x] `server/handlers/promotion.go`: all five RPCs implemented.
      `Promote`/`Rollback` require `auth.RequirePromoter(ctx,
      environment_key)`; the read RPCs require `auth.RequireAuthenticated`.
      Business rules enforced here (repository stays free of gRPC/proto
      concerns):
      - Promotability: `NOT_PROMOTABLE` rejected outright; `VIA_CHART`
        requires `allow_override`, and when set the promotion is stored
        with `is_override = true`.
      - Drift: `GetEnvironmentState` cross-references every `is_override`
        image promotion against the pinned digest of any chart promotion
        covering the same app, reporting a mismatch as a `DriftEntry` on
        the chart's `EnvironmentStateEntry`.
      - `reason` required when the target environment's `rank > 0`, for
        both `Promote` and `Rollback`.
      - `already_promoted` short-circuit: re-promoting the artifact that's
        already current returns the existing row instead of writing a
        redundant SCD2 row.
      - `Rollback` = `GetPrevious` + `Promote`, recording
        `PROMOTION_ACTION_ROLLBACK`.
- [x] `server/handlers/convert.go`: `promotionToPB`/`promotionEventToPB` +
      state/action enum conversions.
- [x] `server/main.go`: `NewPromotionServer(repo)` wired in (was a no-arg
      stub); doc comments updated.
- [x] Unit tests against the fake (`server/handlers/promotion_test.go`):
      promotability enforcement (all three DeployUnit rows),
      `allow_override` + `is_override` round-trip, reason-required-above-
      rank-0, promote→promote→exactly-one-current-row via `ListPromotions`,
      `already_promoted` short-circuit, `dry_run` writes nothing, rollback
      round-trip, rollback-with-no-history rejected, `GetEnvironmentState`
      drift reporting.
- [x] Authorization tests (`server/handlers/authz_test.go`): `Promote`/
      `Rollback` require the environment-scoped promoter role (not a flat
      one) — promoter-dev may promote to dev but not prod; builder cannot
      promote at all; reads require only authentication.
- [x] Postgres integration tests
      (`server/repository/postgres/postgres_integration_test.go`), all
      verified against a real Postgres container via `bazel test
      //tools/app_registry/server/repository/postgres:postgres_integration_test
      --nocache_test_results`:
      - `TestPromotion_CurrentIdxRejectsConcurrentCurrentRows` — the
        partial unique index itself (raw SQL, not the repository) rejects
        two concurrent current rows. This is the test PLAN.md/AR-2d's
        carry-over note flagged as deferred until the `promotion` table
        existed.
      - `TestPromotionRepo_PromoteTwice_ExactlyOneCurrentRow`.
      - `TestPromotionRepo_StateAt_HistoricalWindow` — a timestamp strictly
        between two promotions returns the first, not the second; seeded
        via direct SQL (not wall-clock `time.Now()`) since
        `Promotion.valid_from`/`valid_to` are Unix-second proto fields and a
        real-time test would be flaky by construction.
      - `TestPromotionRepo_Rollback_RoundTrips`.
      - `TestPromotionRepo_Promote_TransactionAbortLeavesNoPartialWrite`.
- [x] `bazel run //:gazelle` run; only `tools/app_registry/server/handlers`
      and `.../repository/postgres`'s `BUILD.bazel` needed new deps/srcs.
      (Gazelle also wanted to touch `cli/BUILD.bazel` and
      `protos/BUILD.bazel` for unrelated pre-existing attribute-ordering
      drift on this branch; reverted both — out of this change's scope.)
- [x] Docs: `migrate/README.md`'s "Planned migrations" row for `003`
      un-marked as planned; `TOC.md` and `PLAN.md` status lines updated;
      this file.

## Verification transcripts (deliberate-break passes)

- **Auth guard.** Removed the `auth.RequirePromoter` call from `Promote`;
  `TestPromote_Authorization`'s "wrong environment" / "builder" / "no
  claims" subtests all failed as expected (`expected PermissionDenied, got
  ... NotFound` / `Unauthenticated ... NotFound`). Reverted; suite green
  again.
- **Partial unique index.** Commented out `CREATE UNIQUE INDEX
  promotion_current_idx ...` in the migration;
  `TestPromotion_CurrentIdxRejectsConcurrentCurrentRows` failed (`expected a
  second concurrent 'current' row ... to be rejected, got nil error`).
  Reverted; full integration suite green again.
- **Transaction wrapping.** Changed the integration test's `promoteTx`
  helper to call `Promotions().Promote` directly against the pool instead
  of through `Registry.WithTx` (simulating the AR-2a-style "forgot to wrap
  in a transaction" bug).
  `TestPromotionRepo_Promote_TransactionAbortLeavesNoPartialWrite` failed
  (`found 0 current rows` — the UPDATE that closed the first promotion
  committed on its own before the second statement's FK violation, leaving
  the target with zero current rows). Reverted; full integration suite
  green again.

## Deferred / out of scope

- `PENDING_APPROVAL`/`APPROVE`/`REJECT` — schema and enum values exist
  (`environment.requires_approval`, `PromotionState`/`PromotionAction`) but
  are never written; see ARCHITECTURE.md "Future: approval gate".
- `writeback_outbox` and the Temporal writeback workflow — AR-4.
- CLI commands (`promote`/`rollback`/`status`/`history`/`diff`) and
  `promote.yml` — AR-3d.
- `ListPromotions`/`ListPromotionEvents` do not implement real
  `PageRequest.page_token` pagination (they return everything matching the
  filter, `Page.TotalSize` only) — matches every other `List*` RPC in this
  package (`ListArtifacts`, `ListApps`, ...), none of which paginate either.
