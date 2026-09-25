# The operator identity on `ui`'s own write path (LB4)

`ui` reaches krill's write API the same way every other krill client does:
it mints a krill session through `POST /sessions/init` and presents that
session id on every mutating request. What makes a **UI-issued** write
different from an agent's is where its identity comes from — an agent
presents a session it minted for itself, while a UI write must carry the
*signed-in operator's* identity, resolved server-side from the Keycloak
session the browser presented, never from anything the browser supplies.

`krill/ui/identity.go` is that resolution, and it is the **same** one
`krill/ui/auth.go`'s `mcpCallerResolver` uses for the MCP OAuth2 front
door: `operatorIdentity` reads the request's `htmxauth` session and returns
the operator's real `(iss, sub)` — `app.oidcIssuer` (the one configured
Keycloak realm) paired with the session's subject, validated through
`//krill/identity`. Three shapes come off it:

- `operatorEncodedIdentity` — the opaque `Identity` string auth's
  credential/auth-code stores hold (the MCP front door's use).
- `operatorSubject` — a `store.Subject{Iss, Sub, Kind: human}` (LB4).
- `requireOperator` — the per-request gate: it resolves the Subject once,
  puts it on the request context, and 401s the handler when the identity
  does not resolve.

An unresolved identity is the only failure mode, never a fallback: a
missing, tampered, or expired session resolves to nothing, and so does the
`AuthModeNone` dev user, because with no configured issuer there is no real
`(iss, sub)` pair to encode. There is therefore no synthetic, dev-only, or
empty identity available to a write in any environment, deployed or not —
the write simply cannot be attributed, and does not happen.

`krill/ui/writeclient.go` is the client the app pages call. `InitSession`
posts the operator's real Subject as **both** `acting` and `on_behalf_of`
(a signed-in operator acts for themselves — the two are never inferred
from one another, mirroring `api/handlers/session.go`), and `Write` carries
the returned session id in `X-Krill-Session-Id`, the header
`api/handlers/gate.go`'s `RequireSession` resolves back into that same
`GatedSession.Acting` / `.OnBehalfOf` / `.ScopeID` triple every krill write
handler attributes its mutation to. The chain is therefore:

```
browser page  →  requireOperator (Keycloak session → real (iss, sub))
             →  writeClient.InitSession (acting = on_behalf_of = that Subject)
             →  writeClient.Write (X-Krill-Session-Id)
             →  api RequireSession → GatedSession → handler
```

`krill/ui/writes.go` is where that chain is walked, once, for every
mutating action the UI performs: `withKrillSession` reads the Subject
`requireOperator` put on the request context, resolves the deployment's
scope (`ScopeStore.GetSole` — a browser has no way to learn a scope id, and
there is exactly one), mints the session, and hands the session id to the
route's write. Nothing a browser sends can influence any of those three:
a request body carries the action's own arguments (`reason`,
`product_id`, `opening_submission`) and nothing else — there is no
identity, scope, or session field on the wire to tamper with. The two
routes it serves are `POST /tasks/{id}/escalate` (a Swarm Operator's manual
task intervention) and `POST /design-sessions` (a Requirement Contributor's
submission); both are mounted behind `RequireAuth` + `requireOperator` in
`setupRoutes`. `api`'s own verdict on a write — an unknown task, a
cross-scope product — is relayed to the browser unchanged, so a rejected
write is visibly rejected rather than silently dropped.

A session is minted per write rather than cached: each `krill_session` row
is the durable record of which real identity performed which mutation, and
a UI's write volume does not make that cost interesting.

`KRILL_API_URL` names the `api` base URL this client targets and is
required: a `ui` with no configured `api` refuses to boot rather than
serve pages whose writes would go unattributed. Persona resolution stays
where it already was (`krill/mcp/server/auth.go` resolves every
auth-authenticated caller to `PersonaSwarmOperator`); the Subject here is
the *who*, never the *what may they do*.
