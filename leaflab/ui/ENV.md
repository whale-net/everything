# LeafLab UI — Environment Variables

> All environment variables required to run leaflab-ui, the HTMX browser
> surface for LeafLab (A8, FR13). Read this when configuring, deploying, or
> debugging.

<!-- TODO (Phase 1 docs task): document each env var loaded by main.go's
     LoadConfig — HOST, PORT, AUTH_MODE, OIDC_ISSUER, OIDC_CLIENT_ID,
     OIDC_CLIENT_SECRET, OIDC_REDIRECT_URI, OIDC_POST_LOGOUT_REDIRECT_URI,
     SECRET_KEY, LEAFLAB_API_URL, GRPC_AUTH_MODE, PG_DATABASE_URL — with
     purpose, required/optional, and default. See tools/app_registry/ui/ENV.md
     and manmanv2/ui/ENV.md for the pattern this should follow. -->

## `LEAFLAB_UI_EXPOSURE_ALLOWLIST`

A30 Phase 1 exit criterion 7's non-exposure gate (see `leaflab/ui/exposure.go`):
a comma-separated list of authenticated principal emails permitted past any
protected route. **Fail-closed** — unset, empty, or all-blank means every
authenticated user is refused, not admitted. Mirrors
`LEAFLAB_API_EXPOSURE_ALLOWLIST` (`leaflab/api`'s own copy of this gate,
keyed by subject rather than email) — the two are independent deployables
with independent env, not a shared chart value in Phase 1.

`TODO(FR5/NFR1.b)`: this variable and the gate that reads it are removed in
#1339, the Phase 2 task that lands per-entity household authorization and
makes this UI safe to expose to real users on its own scoping.
