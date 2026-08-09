# TODO-AR-3a — Auth

Execution tracking for AR-3a (see [PLAN.md](PLAN.md) → "AR-3 — Promotion").
Scope: role model and its enforcement on the RPCs that exist today, plus the
plumbing AR-3b/3c build promotion logic on top of. No promotion/environment
business logic.

## Done

- [x] `server/auth` package: role constants (`app-registry-builder`,
      `app-registry-promoter-{dev,stage,prod}`, `app-registry-admin`,
      matching KEYCLOAK.md exactly) + `Require`/`RequirePromoter`/
      `RequireAuthenticated` helpers over `grpcauth.ClaimsFromContext`.
      `codes.Unauthenticated` with no claims, `codes.PermissionDenied` with
      the wrong role. Roles are flat — no implication between them,
      documented on `Require`.
- [x] Enforced on today's RPCs:
      `AppRegistry.ReconcileApps`/`ArtifactRegistry.RecordBuild`/
      `RecordArtifact` → `RoleBuilder`; `AppRegistry.SetAppStatus` →
      `RoleAdmin`; `ListApps`/`GetApp`/`ListCharts`/`ListArtifacts`/
      `GetArtifact`/`ResolveArtifact` → `RequireAuthenticated` only.
- [x] `libs/go/grpcauth`: added `ServerConfig.DevRoles` (AuthModeNone dev
      claims default to `["admin"]`, which satisfies none of
      `app-registry-*`'s checks — app-registry's `main.go` overrides it with
      `server/auth.AllRoles()`) and `ContextWithClaims` (exported so
      handler tests can inject claims without an unexported context key).
      Both additive/backward compatible — `manmanv2` unaffected.
- [x] CLI (`cli/cmd/client.go`): wired `grpcauth.NewServiceAccountDialOption`
      from `GRPC_AUTH_MODE`/`GRPC_AUTH_TOKEN_URL`/`GRPC_AUTH_CLIENT_ID`/
      `GRPC_AUTH_CLIENT_SECRET`, same call shape as `manmanv2/host` and
      `manmanv2/log-processor`. `GRPC_AUTH_MODE=none` (default) unchanged.
- [x] `server/main_test.go`'s `startTestServer` now wires the real auth
      interceptor (`AuthModeNone` + `DevRoles: registryauth.AllRoles()`),
      matching production wiring in `main.go`, instead of no interceptor at
      all.
- [x] Docs: `ARCHITECTURE.md` Authorization section role names now match
      KEYCLOAK.md; `ENV.md` documents the CLI's new auth vars and the
      `DevRoles` local-dev fix; `TOC.md` links KEYCLOAK.md and its status
      paragraph reflects AR-M–AR-2c merged / AR-3 underway.
- [x] Tests: `server/auth/auth_test.go` (unit, the role-check logic itself)
      and `server/handlers/authz_test.go` (per protected RPC: correct role
      allowed, wrong role → `PermissionDenied`, no claims →
      `Unauthenticated`). Existing `app_test.go`/`artifact_test.go` business
      logic tests now run authenticated as `auth.AllRoles()` via a shared
      `authedCtx()` helper — they were never about authorization and keep
      exercising the same behavior as before this phase.

## Verification

```
bazel test //tools/app_registry/... //libs/go/grpcauth/...   # 4/4 pass
bazel build //tools/...                                       # green
```

## Explicitly not done (AR-3b/3c/3d)

- `EnvironmentRegistry`/`PromotionRegistry` business logic — still
  `Unimplemented`. `RequirePromoter(ctx, env)` already exists for AR-3b/3c to
  call unmodified.
- No changes to `.github/workflows/release.yml` — CI still runs with
  `GRPC_AUTH_MODE` unset (`none`), which now works correctly because of the
  `DevRoles` fix above. Wiring real Keycloak secrets into CI is out of scope
  here; see KEYCLOAK.md section 6 for the shape when that lands.
