# ManManV2 — Authentication & Authorization

> How login, sessions, and role-based UI gating fit together across the UI
> and the gRPC backends. Read this before adding a new admin-only feature,
> debugging "why can't this user see X", or reasoning about what is and
> isn't actually enforced.

## Two separate layers

manmanv2 has two independent auth layers that are easy to conflate:

1. **UI login (`libs/go/htmxauth`)** — browser session on the UI service.
   Answers "is a human logged in, and who are they."
2. **gRPC auth (`libs/go/grpcauth`)** — JWT validation on the API and
   log-processor servers. Answers "is this RPC call carrying a valid
   token."

The UI's `WithAccessToken` middleware forwards the logged-in user's own
OIDC access token on every outgoing gRPC call (see `ui/ENV.md` — no
service-account credentials are used for user-initiated requests). Both
layers are configured independently via `AUTH_MODE` (UI) and
`GRPC_AUTH_MODE` (UI, API, log-processor) — see `ENV.md` and `ui/ENV.md`
for the actual variables. Each can independently be `none` (dev, no
Keycloak) or `oidc`.

## UI login & sessions (`libs/go/htmxauth`)

- `AuthModeNone`: no login screen; every request gets a synthetic user
  with `Roles = []string{"*"}` (`htmxauth.AllRoles`). Local/Tilt dev only.
- `AuthModeOIDC`: standard OIDC login (`/auth/login`, `/auth/callback`,
  `/auth/logout`) against the realm in `OIDC_ISSUER`.
- Every route is wrapped in `app.auth.RequireAuthFunc(...)`
  (`manmanv2/ui/main.go`) — this only checks "is there a valid session,"
  it does not check roles. Route-level role checks do not exist; see
  "What's actually enforced" below.
- Sessions are **cookie-backed** by default, or **DB-backed** if
  `DATABASE_URL` is set (`manmanv2/ui/main.go`'s `NewApp`). DB-backed
  sessions can refresh the access token when it expires; cookie-backed
  sessions cannot (size limits mean cookie sessions never persist
  `Roles` either — see below) and users will see gRPC failures once the
  token expires (typically 5–15 min). Production should always set
  `DATABASE_URL`.

## `UserInfo.Roles` semantics

`htmxauth.UserInfo.Roles` ([]string) is populated from the OIDC token's
`realm_access.roles` claim (`parseRealmRoles` in `libs/go/htmxauth/auth.go`).
Three distinct states, not just "has roles or not":

| Value | Meaning |
|---|---|
| `nil` | `realm_access` claim absent from the token entirely — a deployment/Keycloak misconfiguration, not "no roles." |
| `[]string{}` (non-nil, empty) | Claim present, user has zero realm roles — a plain viewer. |
| `[]string{"role1", ...}` | Claim present with roles. |
| `[]string{"*"}` (`AllRoles`) | Only ever set in `AuthModeNone` — "dev user, treat as every role." |

**Cookie-backed sessions never persist `Roles` at all** (cookie size
limits) — `Roles` is `nil` for every user on a cookie-backed session,
regardless of their actual Keycloak roles. This is one more reason
production needs `DATABASE_URL` set: without it, no user can ever be
detected as admin.

## Role-based UI gating (`manmanv2/ui/components.HasAdminRole`)

`manmanv2/ui/components/roles.go` is the **only** place in the UI that
checks roles:

```go
func HasAdminRole(user *htmxauth.UserInfo) bool {
	if user == nil || user.Roles == nil {
		return false
	}
	for _, r := range user.Roles {
		if r == "*" || r == "admin" || r == "server-manager" {
			return true
		}
	}
	return false
}
```

A user needs a realm role that is *exactly* `admin` or `server-manager`
(or the dev-only `*` sentinel) — any other role (e.g. a plain viewer role)
does not qualify. This is called from exactly one place today,
`handleGameDetail` (`manmanv2/ui/handlers_games.go`), which sets
`GameDetailPageData.IsAdmin` and gates:

- The **Advanced / Danger Zone tab** on a game's detail page (`Edit Game`,
  deploy-to-server, container rebuilds, delete game).

It does **not** gate viewing or editing a game's configs, volumes, addon
path presets, or workshop libraries — those live in the "View More"
section on the Overview tab and are visible to every logged-in user (see
PR #2755: this used to be gated too, which was a bug — editing your own
game's configuration is a routine ops task, not a destructive admin-only
action).

## What's actually enforced (read before adding a new admin-only feature)

**`IsAdmin` is UI-only.** It hides tab buttons and panels in the rendered
HTML; it is not re-checked anywhere else:

- The handlers behind the Advanced tab's own actions (`handleGameEdit`,
  `handleGameDelete`, deploy/teardown handlers) do **not** call
  `HasAdminRole` — nothing stops a logged-in non-admin from POSTing
  directly to those routes and having them succeed.
- `libs/go/grpcauth`'s server interceptors (`NewServerInterceptors`,
  `authenticate`) validate that a gRPC call carries a valid JWT
  (authentication) but perform no per-method role check
  (authorization) — `grpcauth.Claims.Roles` is parsed and available via
  `ClaimsFromContext`, but no handler in `manmanv2/api/handlers` currently
  reads it to gate anything.

In short: today, "admin" in manmanv2 changes what buttons you see, not
what actions the backend will actually let you perform. Treat any new
`IsAdmin`-gated UI as a convenience/declutter mechanism, not a security
boundary, until real per-method authorization is added on the API side.
If you're building something that must actually be restricted to admins,
enforce it in the handler/gRPC layer — don't rely on hiding the button.

## Checking or granting a role

Realm roles are managed in Keycloak, not in this repo. To check what
roles your own account has, or to request `admin`/`server-manager` be
added, ask whoever administers the realm configured in `GRPC_OIDC_ISSUER`
(see `ENV.md`) — this repo has no self-service role management.

## See also

- `ENV.md` / `ui/ENV.md` — the actual environment variables (`AUTH_MODE`,
  `GRPC_AUTH_MODE`, `OIDC_*`, `GRPC_OIDC_*`).
- `../libs/go/htmxauth/README.md` — the shared UI-login library in depth
  (session managers, token refresh, `UserInfo` population).
- `../libs/go/grpcauth/README.md` — the shared gRPC auth library (dev mode
  fake claims, OIDC JWT validation, service-account service-to-service
  auth, Keycloak setup gotchas).
