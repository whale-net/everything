# `init` and the write gate (FR3, #2489, Implementation phase)

`api` now exposes `POST /sessions/init` (`api/handlers/session.go`),
wired in `routes.go`: a caller posts its acting and on-behalf-of `(iss,
sub, kind)` triples (and, optionally, a `whagent_session_id` correlation
value) and gets back the krill-native session id `InitSession` minted.
**`init` is intentionally unauthenticated in M1** — no bearer-token
verification is mounted on the `api` binary (see "No auth wired up on
`api`" below); `init` trusts the caller's asserted identity fields rather
than re-deriving them from a verified credential. This is a deliberate M1
boundary, not an oversight: NFR1's two-front-door pattern (`mcpauth` +
`libs/go/whagent`) is scoped entirely to the separate `krill/mcp` binary
(issue #2494) — `api`'s HTTP surface has no equivalent front door in this
milestone. `krill/mcp` itself did not touch `krill_session` at all as of
issue #2494; that changed with issue #2547's design-session write tools —
see "The design-session MCP surface" below for how `krill/mcp/tools`
validates a caller-presented krill session id without `krill/mcp/server`
ever depending on `store.SessionStore`.

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

