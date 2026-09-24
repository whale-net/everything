# The MCP spec surface (FR10/NFR1, issue #2494)

`krill/mcp` exposes the FR5-FR9 scoped-slice query (`krill/slice.Querier`,
above) over MCP, so any MCP-capable harness (Claude Code today) reaches
it with no krill-specific harness code -- FR10's own wording. It mirrors
the two-front-door pattern already shipped in `audience_score_system/mcp`
and `whagent_net/mcp` rather than inventing a third shape.

**One mount point, four thin-wrapper tools.** `krill/mcp/tools/slice.go`
registers `get_feature_set_slice` (FR5), `get_feature_slice` (FR6),
`get_requirement_slice` (FR7), and `get_product_slice` (FR8) — each takes
a single surrogate id (LB2) and calls the matching `slice.Querier` method
directly, returning its `slice.Document` **unchanged**. This is LB7's
"M1's MCP tool is a thin wrapper over it, not the thing itself" applied
literally: no tool file defines its own output struct, so the MCP
response and `api`'s own `GET /slices/...` HTTP response are the same
Go value serialized twice, never two independently-maintained shapes that
could silently drift. All four tools are mounted at `krill/mcp/server`'s
`specMountPath` (`/mcp/spec`) — its own pre-filtered endpoint, following
`whagent_net`'s `/mcp/readonly` vs `/mcp/ops` split
(`whagent_net/ARCHITECTURE.md` "Domain-owned MCP servers and the tool
contract"): no write tool is registered on this endpoint. This sentence
used to predict a dedicated `/mcp/work` mount for the future work-axis
surface (M4), then a later revision said that never happened and folded
the work axis onto `/mcp/design` instead: neither is the final state.
M2's write tools (`open_design_session`, `append_revision_event`,
`propose_entities`) mount at `/mcp/design` (see "The design-session MCP
surface" below), but the work axis's own task-lifecycle tools
(`create_task` and its siblings, issue #2719 onward) mount on a fourth
surface of their own, `/mcp/work` — `mcp/main.go` builds four
`*mcp.Server`s in total (`/mcp/spec`, `/mcp/design`, `/mcp/work`,
`/mcp/ops`), not the two this section originally described. This sentence
used to read "there is no `RegisterWrite` in `krill/mcp/server` at all" —
that stopped being true as of issue #2547, which adds `RegisterWrite` to
`registry.go`; `/mcp/spec` itself still carries zero write tools.

**Two front doors, one mount point, authorized by persona (NFR1).**
`krill/mcp/server/auth.go` (auth/human) and `whagent_auth.go`
(whagent-net/agent) are structured identically to
`audience_score_system/mcp/server`'s own `auth.go`/`whagent_auth.go`
split — `DualAuthHTTPHandler` routes each request to exactly one door by
bearer-token *shape* (a whagent Claim is always a three-segment JWT; an
auth credential is always a 64-character hex string with no dots),
never by trial-and-error against both verifiers. The one deliberate
departure from that precedent: NFR1 authorizes by **persona** (Swarm
Operator / Requirement Contributor / Agent — `krill/PRODUCT.md`'s
Personas section), not by individual identity, so there is no
`store.Person`/`PersonStore` anywhere in `krill/mcp` — `server.Persona`
is the only identity-shaped value either middleware ever places on
context. Today that resolution is fixed, not looked up: the auth door
always resolves `PersonaSwarmOperator`, the whagent door always resolves
`PersonaAgent`. This is not an oversight — `PRODUCT.md` is explicit that
"The Requirement Contributor exists in the model and in permissions from
M1, but has no unmediated path into krill until C12 lands in M2", so
there is no second human persona for M1's auth door to distinguish,
and a whagent Claim never carries a human profile to resolve further
(`//libs/go/whagent`'s FR10). `krill/mcp/server/registry.go`'s
`RegisterRead` requires only that *some* Persona resolved before a tool
handler runs — none of the four FR5-FR8 tools is persona-sensitive, so
there is no per-tool allow-list on the read side. `RegisterWrite` (issue
#2547) is where a per-tool allow-list first exists — see "The
design-session MCP surface" below.

**Getting a static token for a non-OAuth2 MCP client.** `krill/ui` mounts
`libs/go/auth`'s self-serve credential API
(`Provider.MountSelfServe`, `krill/ui/main.go`'s `setupRoutes`) alongside
the OAuth2 endpoints: `POST /credentials` mints a long-lived bearer
credential for the signed-in operator's session, `GET /credentials` lists
their own (never showing the raw token again), `DELETE /credentials/{id}`
revokes one. This is for a harness that cannot or should not run the
authorization-code + PKCE flow itself (a headless script, a non-MCP-aware
HTTP client) — sign into `krill/ui` once, `POST /credentials` from that
same session, and use the returned token as `Authorization: Bearer <token>`
against `/mcp/spec` directly, same as a token obtained through
`/authorize`/`/token`. See `libs/go/auth/README.md`'s "Self-serve
credential API" section for the full contract.

**The auth door's migration (`006_mcpauth_credential`) now exists.**
`libs/go/auth.NewCredentialStore` preflights a `mcp_credential`-shaped
table at boot, exactly like `audience_score_system`'s migration 006 and
`whagent_net`'s migration 004; migration 006 now provides it. Until it is
applied against a given deployment, `krill/mcp/main.go` still degrades
rather than failing to boot entirely (which would also break the agent
door, which does not need Postgres at all): a failed `NewCredentialStore`
call logs a warning and substitutes `rejectingCredentialStore`, a
`CredentialStore` of last resort whose every method fails with the same
opaque error `auth.TokenVerifier` already produces for a revoked
credential — so a caller presenting an auth-shaped token against a
not-yet-migrated deployment gets a clean 401, never a panic on a nil
interface. The agent door is unaffected either way.

