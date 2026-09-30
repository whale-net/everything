# The operator identity on `ui`'s own write path (LB4)

`ui` reaches krill's write API through `api`'s auth front door (see
`16-init-and-write-gate.md`): it forwards the signed-in operator's own
Keycloak access token as the bearer credential, mints a krill session with
`POST /sessions/init`, and presents the session id on every mutating
request. The identity recorded is therefore the one `api` verified from that
token, never anything the browser or `ui` asserts.

## Credential path

`krill/ui/identity.go`'s `requireOperator` resolves the request's
`htmxauth` session -- DB-backed (`ui_sessions`, migration 007; always
required, never cookie-only) -- to the operator's real `(iss, sub)` and
access token, and 401s when it does not resolve. A missing, tampered, or
expired session, and the `AUTH_MODE=none` dev user, resolve to nothing: there
is no synthetic identity, so an unauthenticated write does not happen. The
same resolution backs `mcpCallerResolver` for the MCP OAuth2 front door.

The token is put on the context with `apiclient.WithUserToken`;
`apiclient.Transport` (wrapped around `writeClient`'s HTTP client) sends it
as `Authorization: Bearer`. `apiclient` also offers a Keycloak
`client_credentials` token source for machine callers.

## Chain

```
browser page  ->  requireOperator (htmxauth session -> operator + access token)
              ->  writeClient.InitSession (empty body; bearer token)
              ->  api authdoor -> InitSessionHandler (identity + scope derived)
              ->  writeClient.Write (bearer + X-Krill-Session-Id)
              ->  api RequireSession -> GatedSession -> handler
```

`init` sends an empty body: identity and scope are derived by `api`, so
there is no acting/on_behalf_of on the wire. `writes.go`'s
`withKrillSession` mints one session per write (each `krill_session` row is
the durable record of which identity did which mutation). Routes are
`POST /tasks/{id}/escalate` and `POST /design-sessions`, mounted through
`App.operatorRoute`. `api`'s verdict (401/403, unknown task, cross-scope
product) is relayed unchanged.

Because `api` enforces the operator role, the signed-in user must hold
`KRILL_ROLE_OPERATOR` for writes to succeed; a reader gets 403. `ui` and
`api` must share `KRILL_OIDC_ISSUER` / `KRILL_OIDC_CLIENT_ID`.

`KRILL_API_URL` is required: `ui` refuses to boot without it.
