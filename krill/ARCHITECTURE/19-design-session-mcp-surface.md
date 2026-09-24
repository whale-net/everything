# The design-session MCP surface (FR1-FR10 over MCP, NFR4, issue #2547)

`krill/mcp/tools/design.go` exposes the design-session/mediated-intake
capability the HTTP surface already ships (design_session.go,
revision_event.go, mediated.go, open_questions.go, session_slice.go — see
those sections above) over MCP, so an MCP-capable harness reaches it with
no krill-specific harness code, exactly like the spec surface's own FR10
wording. This is M2's — and this codebase's — first MCP **write** path:
until this task, every tool `krill/mcp` registered anywhere was
`RegisterRead`-only.

**A second mount, not a second tool on the first one.** The six tools —
`open_design_session`, `append_revision_event`, `propose_entities` (write);
`get_design_session`, `get_design_session_slice`, `list_open_questions`
(read) — are registered on their own `*mcp.Server`, mounted at
`krill/mcp/server`'s `designMountPath` (`/mcp/design`), never at
`specMountPath`. `mcp/main.go` builds two independent `*mcp.Server`/
`Registry` pairs (`specSrv`/`specReg` and `designSrv`/`designReg`) precisely
because registering a tool is a per-`*mcp.Server` operation
(`mcp.AddTool`) — the only way to guarantee a write tool can never end up
reachable from `/mcp/spec` is to never register it on the `*mcp.Server`
that backs that mount. Both front doors (auth/human, whagent-net/agent)
apply identically to both mounts: `server.NewHTTPHandler`/
`NewDualAuthHTTPHandler` now each take both `*mcp.Server`s and mount them
under their own `newMux`-built mux, so the two caller-auth entry points can
never drift on which mounts exist or how they're guarded.

**`RegisterWrite`, and the one persona allow-list this milestone needs
(NFR1).** `krill/mcp/server/registry.go` now has `RegisterRead` (unchanged)
alongside a new `RegisterWrite[In, Out any]`, following
`audience_score_system/mcp/server/registry.go`'s `RegisterRead`/
`RegisterWrite` split in spirit — persona gating stands in for that
package's `ChannelScoped` authorization — but deliberately not carrying
over that precedent's idempotency guard (`store.Idempotency`/
`IdempotencyKeyed`) or its per-call observability wrapper
(`instrumentToolCall`): no FR/NFR in this milestone needs replay-safety for
a write tool call, and this package has no equivalent tracing middleware
for either registration path yet (registry.go's `RegisterWrite` doc
comment has the full reasoning). What `RegisterWrite` **does** carry over,
new for M2: an optional per-tool persona allow-list. Every write tool here
accepts any resolved persona except `propose_entities`, which is
restricted to `PersonaAgent` — FR9/FR10 require a producer-role Agent to be
the caller of a mediated write, since FR10's "acting must differ from
on-behalf-of" can never be satisfied by a human acting for itself. This is
a bare allow-list per tool (a `[]Persona` slice `RegisterWrite` checks
membership against), not a policy engine — nothing here needs more than
that.

**Krill-session gating now reaches `krill/mcp` (correcting "never touches
`krill_session`").** `krill/ARCHITECTURE.md` used to say the `krill/mcp`
binary "never touches `krill_session`" (see "`init` and the write gate"
above) — that stopped being true as of this task. Persona is resolved once,
from context, by middleware `krill/mcp/server` already owned; a krill
session id, by contrast, is a **per-call input field** each write tool's
argument type carries (`krillSessionInput`, embedded by
`openDesignSessionInput`/`appendRevisionEventInput`/`proposeEntitiesInput`),
validated by `krill/mcp/tools/design.go`'s one factored
`requireKrillSession` helper before that tool's mutation ever runs — never
per-tool, and never inside `krill/mcp/server` itself, which still has no
`store.SessionStore` dependency. `requireKrillSession` mirrors
`api/handlers/gate.go`'s `RequireSession` and `krill/importer`'s own
`requireSession`: the same "resolve against `store.SessionStore.GetSession`,
reject cleanly (never a panic, never a partial write) on a missing,
malformed, or unknown id" contract, adapted for a caller with no HTTP
header to carry it and no middleware chain to wrap itself in — `mcp/main.go`
now builds a `*store.Store` and a `store.SessionStore` alongside the
`slice.Querier` it already built, threading both into
`tools.RegisterDesignAll`.

**Thin wrappers, LB7 applied literally, same as slice.go.** No tool file
in `krill/mcp/tools` defines its own bespoke response shape. Five of the
six tools return one of `krill/api/handlers`' own exported wire types —
`IDResponse`, `DesignSessionResponse`, `RevisionEventCreatedResponse`,
`ProposeEntitiesResponse`, `ListOpenQuestionsResponse` — built by the exact
same `handlers.NewXxx` constructor (or struct literal) the corresponding
HTTP handler calls, never a second, MCP-local projection of the same data.
`get_design_session_slice` goes one step further, exactly like the four
FR5-FR8 tools: it returns `slice.Document` **unchanged**, via the same
`handlers.UnionEntityDeltaIDs` helper `GET /design-sessions/{id}/slice`
uses to resolve which ids belong to the session — so this tool's response
is byte-identical to that HTTP route's for the same session, not merely
similarly shaped. `append_revision_event` also reuses
`handlers.ParseEventType`/`handlers.ParseUUIDField` directly (both now
exported for this reason), so an unrecognized `event_type` or a malformed
`entity_deltas[].entity_id` fails with the exact same named message its
HTTP twin produces — no divergent validation between the two surfaces for
the same input shape.

