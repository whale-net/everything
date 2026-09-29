# `init` and the write gate (FR3, #2489, Implementation phase)

`api` authenticates every request, then `POST /sessions/init`
(`api/handlers/session.go`) mints the krill-native session from what that
authentication verified -- the caller never asserts an identity.

## The api auth front door

`api/authdoor` wraps the whole mux (`api/main.go`). Every route except
`/healthz` and `/readyz` needs `Authorization: Bearer`; a missing or invalid
token is 401. Two doors verify it:

- **Opaque mcpauth credential** (a token not shaped like a JWT): verified by
  an `auth.CredentialStore` over `mcp_credential`, wired with
  `PersonaColumn: "persona"`. The persona is the one stored at mint time
  (migrations 027/028 add the column to `mcp_credential` and
  `mcp_auth_code`). A credential with no persona -- anything minted before the
  rollout -- is refused (403); the holder must re-authenticate.
- **Keycloak JWT**: routed by its *unverified* `iss`, which must equal
  `KRILL_OIDC_ISSUER` exactly (the in-cluster and public URLs differ, so
  configure the one tokens actually carry), then verified against the
  issuer's JWKS with audience `KRILL_OIDC_CLIENT_ID` (the client needs an
  `aud` mapper). The persona comes from `realm_access.roles` only, never
  client roles. The door is off unless both variables are set.

`krill/mcp` keeps its own two doors (mcpauth for humans, whagent for agents;
agents reach krill through MCP, not `api`) and shares the role mapping
(`server.RoleConfig`). OIDC configuration is `api`-only.

### Roles

Authorization is by persona: reads need reader or operator (operator
implies read); every non-GET/HEAD request needs operator, else 403.
`KRILL_ROLE_OPERATOR` / `KRILL_ROLE_READER` name the Keycloak realm roles.
**If `KRILL_ROLE_OPERATOR` is unset no identity is an operator, so every
write is refused.** Service accounts that write -- the importer, the
renderer, and the plugins' accounts -- must be granted the operator role in
Keycloak.

### `init`

`InitSessionHandler` reads the verified caller from the door
(`authdoor.FromContext`), takes the scope from `ScopeStore.GetSole`, and
records the caller's acting / on-behalf-of subjects and any
`whagent_session_id`. The body is ignored: no identity or scope field is
accepted. The response is `{session_id, scope_id}`. The MCP `init_session`
tool behaves the same and takes no arguments; `get_scope` reads `scope_id`
without minting anything. `revision_event.acting_kind` accepts `agent`
(migration 025).

### Session expiry

Gated writes touch `krill_session.last_used_at` (migration 026). A session
unused for 12 hours (`store.SessionIdleTTL`) is refused with
`session_expired`; NULL `last_used_at` counts as expired, so every session
minted before the rollout is invalidated and must be re-`init`ed.

### Rollout order

1. Apply migrations 025-028 (`migrate`).
2. Create the Keycloak realm roles, the `aud` mapper, and grant the operator
   role to the writing service accounts.
3. Set `KRILL_ROLE_*` on `api`/`mcp`/`ui` and `KRILL_OIDC_*` on `api`/`ui`.
4. Deploy `api`, then `mcp` and `ui`. Callers that hold pre-rollout state
   must refresh it: sessions via `init`, MCP credentials via
   re-authentication.

The write gate below is unchanged in shape: it still requires an
`X-Krill-Session-Id`, now *in addition to* the bearer token.

`api/handlers/gate.go` is the write gate every mutating endpoint in this
milestone passes through (FR3's "write-only" clause): `RequireSession`
wraps a handler, requires the `X-Krill-Session-Id` header to name a row
`init` actually minted (via `SessionStore.GetSession`), and — on success —
resolves that session's two subjects and scope onto the request context
(`SessionFromContext`) for the wrapped handler to read. It rejects with
401 on a missing header, a malformed id, or an id `GetSession` cannot
find. `RequireSession` covered exactly six write paths as of M1 — entity
creates (FR1, FR2), LB attach (FR4), amend (FR12, issue #2493), import
(FR16, issue #2492), and pointer-issue create (FR20, issue #2496) — and no
read path, including FR21's live C3 query. M2's open-DesignSession/
append-RevisionEvent pair (FR1-FR4/FR8, issue #2543) is now its **seventh
and eighth** write path, wired in `routes.go` as of this task
(`api/handlers/design_session.go`, `revision_event.go` — see "The
DesignSession/RevisionEvent HTTP surface" above); `GET
/design-sessions/{id}` is, like FR21's query, a read path and stays
ungated. The M1 entity creates and LB attach are wired in `routes.go` as
of issue #2490 (`api/handlers/product.go`, `featureset.go`, `feature.go`,
`requirement.go`, `decision.go`); amend is wired as of issue #2493
(`api/handlers/amend.go` — see "Amend and as-of history reads" below);
import/pointer-issue-create remain unwired until their own tasks land.

