# whagent-net — Architecture

Design record for `//whagent_net` (WHale AGENT NETwork), the Temporal-backed
AI agent framework. Read [`README.md`](README.md) first for what the domain
is and its status, and `PRODUCT.md` (once published via
`/project-manager:product`) for the vision and load-bearing decisions this
architecture implements.

Origin: GitHub issue #1552 ("Design: MCP server + Temporal agent service for
manmanv2") explored this shape for one domain. This document generalizes it
into a framework that any domain can consume, with manmanv2 demoted to the
first pilot consumer.

## Index

- [Positioning](#positioning)
- [Language and stack](#language-and-stack)
- [Three nouns: session, transcript, context](#three-nouns-session-transcript-context)
- [Component map](#component-map)
- [Transcript storage tiers](#transcript-storage-tiers)
- [Service boundary vs. package boundary](#service-boundary-vs-package-boundary)
- [Event bus](#event-bus)
- [Session workflow](#session-workflow)
- [Workflow versioning (NFR1)](#workflow-versioning-nfr1)
- [Domain-owned MCP servers and the tool contract](#domain-owned-mcp-servers-and-the-tool-contract)
- [Guardrails](#guardrails)
- [Embeddable session UI](#embeddable-session-ui)
- [Identity and auth chaining](#identity-and-auth-chaining)
- [Idempotency](#idempotency)
- [Phasing](#phasing)
- [`mcp`'s start_session: two RPCs, one tool (issue #2120)](#mcps-start_session-two-rpcs-one-tool-issue-2120)
- [Open items](#open-items)

## Positioning

whagent-net is the *framework*, not any particular agent. It owns:

- session lifecycle and orchestration (Temporal),
- transcript persistence with hot/cold tiering,
- per-turn context assembly,
- the tool contract that domain-owned MCP servers conform to,
- an embeddable session UI component and a standalone agent UI,
- an MCP surface so agents (and Claude Code) can drive agents.

It does **not** own domain tools. `audience_score_system`'s MCP server
already lives under `audience_score_system/mcp/` and is the first consumer;
a future `manmanv2` server would live under `manmanv2/` (out of scope for
this product — see #1552), and each future domain ships its own. This
keeps whagent-net decoupled from every domain's proto surface and release
cadence; the cost is a shared contract (`//libs/go/whagent`) that every
domain MCP server must satisfy — see
[Domain-owned MCP servers](#domain-owned-mcp-servers-and-the-tool-contract).

Contrast with the field: most agent frameworks are Python and *stateful* —
the agent loop is the process, so a session dies with it. Here the loop is a
Temporal workflow, the transcript lives in a store, and every UI is a
stateless reader. A session survives worker restarts, can be watched from
several UIs at once, and any consuming domain gets those semantics for free.

## Language and stack

**Go throughout**, reusing the repo's hardened libraries:

| Concern | Library | Notes |
|---|---|---|
| Temporal | `//libs/go/temporal` | Bootstrap only (`NewClient`, `NewWorker`, `UpsertSchedule`, config, logger). Workflow precedents exist elsewhere: `tools/app_registry/worker` (release, writeback, outbox) and ASS's `ChannelSyncWorkflow`, with `testsuite` tests. New here: a long-lived signal-per-turn workflow and workflow-code versioning under open runs. |
| S3 | `//libs/go/s3` | Exists. `manmanv2/log-processor/archiver` is a direct Postgres → S3 archiver precedent for `worker`'s archive workflow. |
| Postgres | `//libs/go/db`, `//libs/go/migrate` | Same as `audience_score_system`. |
| RabbitMQ | `//libs/go/rmq`, `//libs/go/htmxsse` | `htmxsse.Hub` + `DefaultAttachFunc` for SSE fan-out; reference implementation `tools/app_registry/ui/main.go` `initializeSSEHub`. |
| Web UI | `//libs/go/htmxbase`, `//libs/go/htmxui`, `//libs/go/htmxauth` | Go + `templ` + htmx + daisyUI, CDN-pinned, no Node — the convention every UI in this repo follows. |
| MCP | `github.com/modelcontextprotocol/go-sdk` | Precedent: `audience_score_system/mcp` (first Go MCP server in the repo, so #1552's "would be the first" note is stale). Auth via `//libs/go/mcpauth`. |
| gRPC auth | `//libs/go/grpcauth` | Verifies Keycloak OIDC (`coreos/go-oidc/v3`; `Claims{Subject, Roles, Audience, ClientID, IsServiceAccount}`; user-token and service-account dial options). `IsServiceAccount` (FR6/#2243) is what `api` derives a session's `subject`/`on_behalf_of` `kind` from — see [Identity](#identity-and-auth-chaining). |
| LLM client | `openai/openai-go` (candidate; architect to verify) | Serving is via **OpenRouter**, which is OpenAI-wire-compatible — the Anthropic SDK does not target it. One client with a base-URL override covers every OpenRouter model; a future second provider is another base URL, not an abstraction layer. |
| Identity | Keycloak (OIDC) | Humans and service accounts alike — see [Identity](#identity-and-auth-chaining). |

## Three nouns: session, transcript, context

#1552 used "context" for all three of these. Separating them is what makes
the API shape obvious:

| Noun | What it is | Mutability | Owner |
|---|---|---|---|
| **Session** | Control-plane record: id, subject (who it runs as), agent definition, model, caps, `parent_session_id`, status (`running` / `awaiting_input` / `done` / `stopped` / `capped` / `failed`), Temporal workflow handle | Small, mutable | `api` + Temporal (source of truth for status is the workflow; the `sessions` row mirrors it with compare-and-swap on terminal writes, same as manmanv2 control-api/event-processor) |
| **Transcript** | Append-only event log: user turns, model messages, tool calls, tool results, summaries | Immutable, grows | `session` store package, tiered — see [Transcript storage tiers](#transcript-storage-tiers) |
| **Context** | What the model *sees* at turn N: a projection over the transcript — recent events + summaries + agent definition, fitted to a token budget | Derived, ephemeral, rebuilt every turn | `worker` activity. Never stored; each turn records the event-ID list it was built from so a debug `GetTurnContext` can be added later without a schema change |

Programmatic integrators need `GetSession` (is it done? waiting on me?),
`ReadTranscript` (what happened), and `SendTurn`. They do not need context.

## Component map

| Component | Kind | Role |
|---|---|---|
| `whagent_net/session` | Go package + migrations | Store: `sessions`, `transcript_events` (append-only — explicitly **not** SCD2), idempotency ledger, agent definition assignment (SCD2, `valid_from`/`valid_to`). Publishes every committed event to the `whagent/events` exchange. Transparent S3 hydration on cold reads. |
| `whagent_net/api` | gRPC service, `external-api` | Session service facade: `StartSession`, `SendTurn`, `StopSession`, `GetSession`, `ListSessions`, `ReadTranscript`, `StreamEvents` (FR5/C17, issue #2239) — a server-streaming bridge over the exchange so programmatic clients never need RabbitMQ credentials. Writes signal the Temporal workflow; reads go to `session` directly. Mirrors `manmanv2/control-api`. |
| `whagent_net/worker` | Temporal worker, `worker` | `SessionWorkflow` (one per session, long-lived, signal-per-turn) + activities: resolve agent definition → build context → LLM call → dispatch tool calls to domain MCP servers → commit turn → publish. Imports `session` directly. Also hosts `ArchiveWorkflow` (FR7/C18, `worker/archive.go`): a Temporal Schedule (`ArchiveScheduleID`, interval `WHAGENT_ARCHIVE_INTERVAL`) fires a `RunArchiveBatch` activity that moves a terminal session's transcript from Postgres to S3 once past TTL and trims the hot-tier rows, leaving an index row with the S3 pointer — no separate deployable, since Temporal already is this repo's scheduled-job engine (`//libs/go/temporal`'s `UpsertSchedule`, the same mechanism `audience_score_system/worker/sync` uses for `ChannelSyncWorkflow`). Selection is a poll of `sessions`/`transcript_archive` on the schedule's own cadence, not a consumer of `whagent/events` — a session's terminal transition is a row write (`sessions.updated_at`), not something archival needs a bus message to discover. Registered only when `WHAGENT_S3_BUCKET` is set; unset, `worker` runs `SessionWorkflow` with no archive schedule at all. |
| `whagent_net/mcp` | MCP server, `external-api` | `start_session`, `send_turn`, `get_session`, `read_transcript` over `api`. Phase-1 test surface (Claude Code drives it directly) and, later, how agents spawn agents. Same `web` + `mcp` sibling shape as `audience_score_system`. |
| `whagent_net/embed` | Go package | The shareable UI component: `templ` session components (transcript, turn composer, session list) + `embed.Mount(mux, apiClient, sseHub, opts)` registering fragment + SSE routes under a host-chosen prefix + a persona-mapping hook. Cross-app primitives only, per `libs/go/htmxui`'s rule. |
| `whagent_net/ui` | Web UI, `external-api` | Standalone agent UI: session list, session view, "run an agent" form, view any session. `htmxui.Shell` + `embed` + an `htmxsse.Hub` on `whagent/events`. First consumer of `embed`; first *clean* `htmxui` adopter. |
| `whagent_net/migrate` | Job | Applies `session` migrations, then runs the agent-definition seeder (`whagent_net/migrate/seed`, issue #2121) as a post-migration step — upserts `whagent_net/config/agents.yaml`'s checked-in definitions into `agent_definition`, minting a new version row whenever a definition's fields drift from the latest seeded one, never editing a version already pinned to a session. |
| `whagent_net/config` | Go package | The checked-in agent-definition seed source (`agents.yaml` + `Load`/`Validate`) `whagent_net/migrate/seed` consumes — LB5/NFR6: config-driven seeding, but `agent_definition` stays a real, versioned table, never replaced by a config lookup. Also the seed source for `model_definitions` (below), decoded via the same `Load`. |
| `model_definition` | Table (`whagent_net/session`) | A named, reusable model id + OpenRouter provider-routing preference bundle (`session.ModelDefinition`/`ProviderPreferences`) an `agent_definition` row may reference (`model_definition_id`) instead of naming a model directly — exactly one of `agent_definition.model` / `model_definition_id` is set (migration 006's CHECK constraint). Seeded from `agents.yaml`'s `model_definitions` the same way `agent_definition` is, but NOT versioned/SCD2 itself — see `session/modeldef.go`'s `ModelDefinitionStore` doc comment for why. This is what "target specific providers"/quantization/etc. per agent is configured through, not an env var. |
| `whagent_net/llm` | Go package | The OpenRouter model client (issue #2112): a single OpenAI-wire `Client` pointed at OpenRouter's base URL, `Catalog` (FR5's "does the provider serve this model" gate, cached), and per-turn cost accounting (`cost.go`/`pricing.go`, FR7/LB6). One LLM provider, one client — see [Open items](#open-items) "Provider abstraction"; `Request.Provider` (OpenRouter's own upstream-provider routing, resolved from `model_definition` above) does not revisit that non-goal. |
| `//libs/go/whagent` | Go package | Tool contract for domain-owned MCP servers: idempotency-key field, persona claim shape, agent definition registration format. |

## Transcript storage tiers

RabbitMQ is a bus, not a database — classic queues are consume-once and
neither classic queues nor streams are queryable by session. So:

| Tier | Store | Role |
|---|---|---|
| **Bus** | RabbitMQ exchange `whagent/events` | Every transcript event, published on commit. Consumers each bind their own TTL'd queue: UI SSE hubs, metrics, future programmatic subscribers. Never the system of record — `worker`'s archive workflow does not consume it either (see the component map above). |
| **Hot** | Postgres `transcript_event` | Append-only. Serves `ReadTranscript` and the worker's context build. Retention = TTL. |
| **Cold** | S3 `sessions/{session_id}.jsonl.gz` | Written by `worker`'s `ArchiveWorkflow`/`RunArchiveBatch` once a session is terminal and past TTL; hot-tier bodies trimmed, index row (`transcript_archive`) keeps the pointer. `ReadTranscript` hydrates from S3 transparently. |

Postgres is the queryable tier because it already exists in every domain;
adding a second query engine for transcripts is not justified at this scale.

### Cold-object contract (FR8/FR7, issue #2240)

This is the interop contract between `session`'s tier-transparent reader
(issue #2240) and `worker`'s `ArchiveWorkflow`/`RunArchiveBatch` (FR7,
issue #2244, `worker/archive.go`): both agree on the exact object shape
without either owning the other's code.

- **Index row:** `transcript_archive` (migration 002), one row per archived
  session — `session_id` (PK, `REFERENCES sessions`), `s3_bucket`, `s3_key`,
  `event_count`/`min_seq`/`max_seq` (let a reader sanity-check what it
  downloads without opening it), `archived_at`, and `hot_trimmed_at`
  (`NULL` until `RunArchiveBatch` trims the session's hot rows).
  Append-only-ish, like `transcript_event` —
  explicitly **not** SCD2 (a session is archived at most once;
  `hot_trimmed_at` is the one field ever revised after insert). A row
  existing here is itself the "hydrate from S3" signal — readers never
  consult `hot_trimmed_at` to decide whether to hydrate.
- **Object key:** `sessions/{session_id}.jsonl.gz`.
- **Object body:** gzip-compressed JSON Lines, one `whagent_net/events.Event`
  per line (`event_id`, `session_id`, `seq`, `turn`, `type`, `payload`,
  `committed_at` — LB1's single record definition, the same JSON shape as
  the Postgres row and the bus message), in ascending `seq`, covering
  exactly `[min_seq, max_seq]` with `event_count` lines. Never summarized,
  reshaped, or dropped (LB1) — the cold copy is byte-for-byte the same
  events the hot tier held, not a derived digest.
- **Read-side merge:** `TranscriptStore.Read` (`session/transcript.go`)
  serves from `transcript_event` first; when the requested range is not
  fully satisfied from hot rows and a `transcript_archive` row exists, it
  hydrates the object via `//libs/go/s3`, decodes it, and merges hot +
  cold in ascending `seq` with hot rows winning on a duplicate `seq` (the
  same event committed to both tiers is emitted once, never twice). No
  archive row and no hot rows is an empty result, not an error; an archive
  row whose object is missing is an `Internal` error (ERROR-level log),
  never a silently truncated transcript.

## Service boundary vs. package boundary

The **service boundary is the gRPC API** in `api`: that is what `ui`, `mcp`,
`embed` hosts, other domains, and programmatic integrators talk to.
**Internally, `api` and `worker` share `session` as a Go package** — no
network hop on the hot path. This is the same shape as manmanv2's
`control-api` + `event-processor` sharing one database with compare-and-swap
on status transitions. #1552's motivation for a separate context service
(keep Temporal history small) is satisfied by activities passing event IDs
rather than transcript bodies; it does not require a process boundary.

If a non-Go consumer ever needs direct transcript access, `session` is the
seam to promote to a service.

## Event bus

`whagent/events` is a topic exchange. Routing key `session.{id}.{event_type}`.
Payloads are the same event records committed to `transcript_events`
(full-state, not deltas — matching `htmxsse`'s fragment-swap model). The
worker publishes *after* commit, from the activity, so a retry can
re-publish but never publish an uncommitted event; consumers must tolerate
duplicates (event IDs are stable).

`api`'s `StreamEvents` RPC (FR5/C17, issue #2239) is a programmatic
consumer of this same exchange: at startup `api` declares the exchange and
binds one shared, per-process ephemeral queue to it (`"#"`, mirroring
`tools/app_registry/ui/main.go`'s `initializeSSEHub` attach shape) — never
a queue per stream, so no client of the RPC ever needs its own broker
connection or credentials. A `StreamEvents` call backfills from
`session.TranscriptStore.Read` starting at the request's `from_seq`, then
tails the shared queue's deliveries filtered to that session's routing
keys, emitting strictly in `seq` order and deduplicating on `event_id`
(NFR4/LB1) across the backfill/tail handoff the same way a polling
`ReadTranscript` client's `next_from_seq` cursor does, just pushed instead
of polled.

## Session workflow

One `SessionWorkflow` per session, started by `api.StartSession` with
workflow ID = session ID. Turns arrive as signals (`SendTurn`); the workflow
never returns between turns. Per turn:

1. Activity: resolve the session's *current* agent definition (never cached in
   workflow history — sessions are long-lived and agent definitions drift).
2. Activity: build context (event-ID list + summaries, budgeted).
3. Activity: LLM call.
4. Activities: dispatch each tool call to the agent definition's MCP server(s),
   carrying the idempotency key and persona claim.
5. Activity: commit turn events to `session`, publish to the bus.
6. Workflow updates status (`awaiting_input` / `done`), records the turn's
   context event-ID list.

Bounded tasks (~100 turns) are the target; Continue-As-New is deferred
until that assumption changes. `parent_session_id` is set from day one so a
session started via `mcp` by another session is an ordinary session.

## Workflow versioning (NFR1)

`SessionWorkflow` is long-lived and signal-per-turn (unlike every other
in-repo workflow, which is one-shot or scheduled -- "Language and stack"
above), so it is the first workflow in this repo that can have open runs
spanning a worker deploy that changes its own code. Every behavior-changing
edit to `SessionWorkflow` or `processTurn`'s control flow must go through
`workflow.GetVersion`, from the first one onward, so a run already open
across that deploy keeps replaying its recorded history correctly instead
of hitting a non-determinism error.

Convention (established in `whagent_net/worker/workflow.go`, issue #2114):
one change ID per behavior-changing edit, named
`session-workflow-<short-slug>`, added at the exact point the new branch
diverges from old behavior:

```go
v := workflow.GetVersion(ctx, "session-workflow-<slug>", workflow.DefaultVersion, 1)
if v >= 1 {
    // new behavior
} else {
    // old behavior, preserved for any run already open when this change deployed
}
```

`workflow.go`'s `updateSessionStatus` (change ID
`session-workflow-status-transitions`, issue #2114) was the first real
usage: the Scaffold-phase loop never wrote `sessions.status` at all, so
Implementation phase's addition of that write was a genuine new branch,
not just documentation. Issue #2119 (`session-workflow-cap-enforcement`)
added the turn/cost cap checks and failure-classification path. Issue
#2121 (`session-workflow-tool-dispatch`) added the `ActivityListToolDefinitions`
call ahead of the model call and the per-tool-call `ActivityDispatchTool`
loop after it — the tool-dispatch step `processTurn` had left a no-op hook
since #2114. Every later task that changes `SessionWorkflow`/`processTurn`'s
control flow inherits this convention rather than reinventing it.

## Domain-owned MCP servers and the tool contract

An **agent definition** is a named, role-shaped tool set: a list of MCP endpoints plus
allowed tool names (v1: static, config- or table-driven; e.g. manmanv2's
`/mcp/readonly` vs `/mcp/ops`). Agent definitions are assigned to sessions with SCD2
history so a session's tool set can be tightened or widened mid-flight and
the change is auditable.

`//libs/go/whagent` defines what a domain MCP server must accept to be
usable from an agent definition:

- the idempotency-key field for any tool that mutates state,
- the persona claim shape (see [Identity](#identity-and-auth-chaining)),
- the agent definition-registration format the domain publishes.

**Tool selection** — an agent definition names which of a server's tools an agent may
see, so an agent can be focused. Initially this is enforced on the MCP
server side (the server exposes a pre-filtered tool list at the agent definition's
endpoint, e.g. `/mcp/research`); whagent-side filtering of a server's full
tool list is a later capability, not an M1 requirement.

First consumer: `audience_score_system/mcp` (exists; research tools are the
embedded-agent target). `manmanv2` is out of scope for this product — it
would need the `ControlClient` extraction described in #1552 first.

## Guardrails

Every session carries a **model** (chosen per agent definition, overridable
per session — model selection is a hard requirement) and two **caps**: max
turns and max cost, defaults 100 turns / $1, overridable per agent. The
worker checks both before each turn and after each LLM response. Cost
accrues from provider-reported usage (`usage.include=true` on OpenRouter);
when the provider omits cost it is estimated from tokens against a
configurable per-model price table and the turn's usage record is flagged
`estimated` — never treated as free. Tripping either cap ends the session in
`capped`, a terminal status distinct from `done` with its own transcript
event, so consumers can react to "ran out" differently from "finished".
Cap evaluation always reads `UsageStore.SumCost`'s committed running total,
never a separately-mutated counter, and the turn cap/cost cap check itself
(`whagent_net/worker/caps.go`'s `checkCaps`) is agent-definition-level only
in M1 — there is no per-session cap override. The `GetSessionUsage` read
path (`whagent_net/api/handlers/usage.go`, backed by
`UsageStore.Summary`) sums the same `turn_usage` rows `SumCost` does — one
COUNT/COALESCE(SUM)/bool_or query, not a separately-mutated counter — so a
client reading usage never sees a figure that could diverge from what cap
evaluation itself used to decide `capped`.

There is no human-approval gate for tool calls (non-goal for now).

### Terminal outcome classification (FR2, FR3)

A session's `GetSession` result reports which of `done` / `stopped` /
`failed` / `capped` it ended in; for `capped`, which cap (`cap_kind`:
`turns` or `cost`); for `failed`, an **error category** of `retryable` or
`non_retryable` plus a short human-readable `error_detail`. A session that
ends `failed` also commits a failure transcript event carrying the exact
same category and detail `GetSession` reports — one classification
(`whagent_net/worker/classify.go`'s `classifyError`), computed once per
failure, never independently re-derived for the transcript event and the
session row. A tool result a domain server returns with its own `isError`
flag set is an ordinary tool-result event, never this failure event or an
input to this classification — that boundary is whagent-net's own
judgement, never originated or tagged by a domain server.

Classification rules:

| Category | Causes |
|---|---|
| `retryable` | A provider rate limit (HTTP 429); a transient transport failure (connection reset, DNS failure, a provider 5xx); a timeout (the activity's `StartToCloseTimeout` elapsing, or a lower-level transport timeout). |
| `non_retryable` | A model the provider does not serve; an auth/permission failure (HTTP 401/403); a malformed agent definition; an exhausted retry budget (Temporal's `MaximumAttempts` exhausted with no more specific cause identified) — also the safe default for any error this classification does not otherwise recognize. |

The goal: an operator can decide retry-vs-escalate from `GetSession`'s
category alone, without reading the transcript.

## Embeddable session UI

Every web UI in this repo is Go + `templ` + htmx, so "a session component
other UIs can embed" is a **Go package, not a JS bundle**. As of M2, that
package does not exist yet: `whagent_net/ui/components` (e.g.
`session.templ`, the session detail page's transcript/state-badge/composer
component) renders in place, directly inside `ui`, with no `embed` package
in between. Extraction of these components into a standalone `embed`
package — imported by a host binary and mounted same-origin, so the host's
existing authn (`htmxauth`, or `audience_score_system/web`'s own Google
OAuth flow) applies unchanged — is deferred to M3/C19, when
`audience_score_system/web` becomes the first other-domain consumer. Live
updates already come from an `htmxsse.Hub` the host builds on
`whagent/events` (LB7); at M3, `embed` renders fragments via `api` reads
and swaps them on SSE the same way `ui` does today. Hosts that have no SSE
today (ASS `web` is form-POST-only) will gain it only on the embedded
routes.

## Identity and auth chaining

Identity provider is **Keycloak over OIDC** for every caller. Two kinds of
subject, treated uniformly:

- a **human** (signed into a host UI, or Claude Code holding a token), and
- a **service account** (a scheduler, another domain's worker, a parent
  agent session) — a Keycloak client credential, no human behind it.

`api` authenticates the OIDC token on every call and **authorizes** the
subject against the requested agent definition ("may this subject run this
agent?") before starting a session. Authorization is by **Keycloak realm /
client roles**: an agent definition declares a required role and `api`
checks it against `grpcauth`'s `Claims.Roles` — no whagent-side ACL table.
A caller may act **on behalf of** a user (a host UI starting a session for its signed-in user; a service
account running a job for someone); the session records both the acting
subject and the on-behalf-of subject as `(iss, sub, kind)`. `on_behalf_of`
is always populated — it equals the acting subject whenever a caller acts
for itself, whatever its kind. Keeping `iss` a real column is what lets a
non-Keycloak identity (an ASS Person is keyed on Google `sub`) be an
on-behalf-of subject later without a schema change; C8's role check
applies to the *acting* subject, identically for a human or a service
account — `api`'s `StartSession` runs the same `required_role` check
either way (FR6/#2243), never a service-specific branch.

**Kind (FR6/#2243).** `grpcauth.Claims.IsServiceAccount` (derived from a
Keycloak client-credentials token's `preferred_username`, see
`libs/go/grpcauth/KEYCLOAK.md` § "Service accounts") is what
`callerSubject` (`whagent_net/api/handlers/session.go`) maps to
`SubjectKindService` vs. `SubjectKindHuman` when it reconstructs the
acting subject — the only place kind is decided. `StartSession` then
writes `on_behalf_of = subject` as usual (M1's caller-acts-for-itself
default, above), so a service account's session records `kind = service`
on both columns with no other code path aware of the distinction —
`worker`'s claim-minting and tool-dispatch paths (`api/persona`,
`worker/tools/dispatch.go`) carry no service-specific branch (LB3): the
stored `on_behalf_of.kind` is the only thing that differs.

**Read vs. control are two different rules, not one ownership check
(#2237).** `SessionService`'s two read RPCs (`GetSession`, `ReadTranscript`)
apply no ownership check at all — any authenticated caller may read any
session, whoever started or controls it (FR2/C14: on-call viewers are the
point). The two write RPCs that act on a running session (`SendTurn`,
`StopSession`) are gated on `on_behalf_of`, never `subject` — a caller may
control a session only when it *is* (or is acting for) the session's
`on_behalf_of` `(iss, sub)` pair (FR1/C13), matched on both fields per LB2
(same `sub` under a different `iss` does not control). `on_behalf_of ==
subject` for every M1 row, so this is behavior-preserving on existing data
while being the correct rule once a caller acts on behalf of someone else.
There is no admin override in M1 — a caller that is neither the acting nor
the on-behalf-of subject simply cannot control a session it didn't start.
`whagent_net/api/handlers/session.go`'s `canControl` is the single place
this control rule lives; no other handler re-derives it.

**`mcp`'s delegated-grant credential acquisition (FR7/FR8/FR9, plan
#2421).** `api` verifies real Keycloak-signed JWTs and nothing else —
that verifier is unchanged by this design (`libs/go/grpcauth`). `mcp`
accepts a second credential shape alongside the manual Keycloak access
token (`README.md` "Browser-based sign-in"): an opaque bearer credential
`ui`'s own OAuth2 authorization-server front end (`libs/go/mcpauth.Provider`,
mounted by `whagent_net/ui/mcpauth.go`) mints for an operator already
signed in there. That credential resolves (via
`mcpauth.CredentialStore.Verify`) to the operator's own real Keycloak
`(iss, sub)` — packed into `mcpauth.CredentialStore`'s opaque `Identity`
string by `whagent_net/mcpidentity.Encode`/`Decode`, the one place that
packing happens — never a new whagent-net-only identity.

Because `api` cannot verify that opaque credential directly, a *working*
Keycloak-signed JWT still has to come from somewhere — but `mcp` no
longer mints one itself. `whagent_net/mcp/server/auth.go`'s
`AuthMiddleware` places the resolved `(iss, sub)` on ctx
(`mcpidentity.ContextWithIdentity`) and stops there: it exchanges or
mints nothing (issue #2430 deleted `server/tokenexchange.go`'s
`KeycloakExchanger` and its RFC 8693 impersonation-exchange call
entirely, not left dormant — FR19). Acquisition happens later, at
MCP tool-dispatch time (`whagent_net/mcp/tools/dispatch.go`), once a
call's target scope is actually known — `AuthMiddleware` has no notion
of "which scope" for any given call, since that requires the request's
own `agent_id`/`session_id`, which only a tool handler has parsed (FR7):

- `start_session` resolves the target scope via `ScopeForAgent(ctx,
  agentID)` (backed by the chosen `AgentDefinition.Scope`, FR1/#2424);
  every other tool (`send_turn`/`stop_session`/`get_session`/
  `read_transcript`) resolves it via `ScopeForSession(ctx, sessionID)`,
  backed by that session's already-recorded agent-definition assignment
  — never a scope re-derived from a fresh `agent_id` on every call.
- The resolved scope becomes a grant key via `whagent_net/grantkey.ForScope`
  — the *only* permitted derivation (FR4): the grant key for scope `d`
  is `d` itself, once validated as well-formed. Deriving one from
  `agent_id`, `required_role`, or `tool_set[].server_url` is forbidden.
- `dispatch.go`'s `acquireGrantToken` calls `grant.TokenSource(identity.Sub,
  grantKey).Token(ctx)` (`libs/go/grpcauth.DelegatedGrantSource`, keyed
  on the operator's raw Keycloak `sub`, never the mcpidentity-encoded
  composite) to obtain a real, working access token — re-read from
  `Store` on every call, never cached here or anywhere else (FR8:
  `tokenexchange.go`'s in-memory per-identity cache is gone, not
  replaced with a new one).
- A scope with no active grant (`grpcauth.ErrGrantNotFound`) or a
  revoked one (`ErrGrantRevoked`) fails the call outright — there is no
  fallback to any other credential path.
- `AgentDefinition.Scope` is nullable (migration 010): when
  `ScopeForAgent`/`ScopeForSession` resolves a nil scope, dispatch skips
  grant-key derivation and token acquisition entirely and forwards the
  call unchanged — a scope-less agent definition carries no
  delegated-grant scoping at all, but still runs with whatever
  `tool_set` it is configured with.

The result is the same as before: a session started through the
browser/OAuth2 path is indistinguishable downstream from one started
with a manually-pasted token — same `subject` shape for every rule
above — just acquired through a scoped grant instead of an
impersonation exchange.

**One-time per-scope consent (FR2/FR3/FR5/FR6, issue #2428).** The
token a `TokenSource` call above resolves only exists once the operator
has completed a one-time, per-scope browser consent:
`whagent_net/ui/handlers_consent.go`'s `GET`/`POST
/mcp/consent(?scope=<d>)` drives `DelegatedGrantSource.BeginAuthorization`/
`CompleteAuthorization` (requesting `offline_access`) for one explicit
scope and records a bookkeeping-index entry (FR12, `grantindex`) on
success — this route never infers or guesses a scope itself.
`authorizeConsentGate` wraps `GET /authorize` (`ui`'s mcpauth-hosted
OAuth2 endpoint for the MCP client) with a prerequisite that the operator
hold an active grant for `WHAGENT_UI_DEFAULT_SCOPE` before a credential
is minted — deliberately scope-agnostic at the OAuth layer rather than
resource/scope-driven: `libs/go/mcpauth` is scope-agnostic by design
(its own "zero scope-specific types" NFR) and `mcp`'s RFC 9728 resource
identifier is one single, instance-wide URL, not one per scope —
per-scope resolution happens later, at dispatch time (above). Consent
for scope `D` grants standing access to `D` only (FR3): an operator who
has only consented for `audience_score_system` cannot reach `manmanv2`'s
agent without separately consenting for it, and there is no
session-based shortcut around this for an already-`ui`-authenticated
operator (FR6) — accessing a new scope for the first time is routed
through this same flow at the moment of first access (FR5), never at
`ui` sign-in.

**Mid-call reauth (FR18, issue #2431).** A stored refresh token can stop
working after it had been working (Keycloak rejects it in a way only
re-consent fixes) — `acquireGrantToken` detects this distinctly
(`errors.Is(err, grpcauth.ErrGrantNeedsReauth)`) and returns a
`reauthRequiredError` naming the scope, rather than a plain acquisition
failure, whether the failing call is `start_session`'s initial connect
or an existing session's mid-call dispatch. No retry is attempted and no
other grant is substituted — the operator is routed back through the
consent flow above for that scope specifically, the next time they
access it through `ui`. Logged at WARNING (this is a genuine deviation
needing a human, not an ERROR — the system itself behaved correctly).

**Self-service and admin grant lists (FR14–FR17, issues #2432/#2433).**
`GET /grants` (`whagent_net/ui/handlers_grants.go`) lists the signed-in
operator's own delegated grants with a live per-scope status read
(`grpcauth.Store.Status`, never the bookkeeping index, which carries no
status column of its own — FR12) and lets them revoke any one
individually (`POST /grants/revoke`); it never shows another operator's
grants (FR16). `GET /admin/grants` (`handlers_grants_admin.go`) is the
equivalent for every operator's grants, reachable only to an operator
whose token carries the `WHAGENT_GRANT_ADMIN_ROLE` realm role (FR14/FR15)
— checked against the roles on a freshly-refreshed access token
(`htmxauth.DBSessionManager.GetAccessToken`), never the 24h-cached
`htmxauth.GetUser(ctx).Roles` snapshot, so a revoked admin role stops
working on the very next request rather than up to a day later (NFR3).
Both revoke actions are scoped to exactly one `(subject, grant)` pair
(FR17) and take effect immediately — there is no cache for a revoke to
race against (FR8/NFR7).

**NFR2 — scope isolation is a property of whagent_net's own dispatch
code, not of the Keycloak JWT.** The underlying Keycloak-signed JWT
`TokenSource(...).Token(ctx)` returns is not scope-narrowed by which
grant produced it — nothing at the IdP layer or in `grpcauth` itself
prevents a JWT obtained via scope A's grant from being technically
usable against scope B's `required_role` check if it were ever
forwarded there. The guarantee this design makes is **structural
correctness of whagent_net's own grant→scope routing**: FR4's grant key
ties one grant to exactly one scope, and no code path in `mcp` ever
resolves or forwards a grant for any scope other than the one the
current call (above) is actually targeting — there is no code path that
accepts a caller-supplied or mismatched grant/scope pair. This is
**not** a claim that the JWT itself is cryptographically restricted to
one scope; it is a claim about what whagent_net's dispatch code will
and will not do with the JWT it obtains.

**NFR1 — what compromising a secret alone can and cannot do.** No single
secret held by `mcp` or `ui` — including `WHAGENT_GRANT_CLIENT_SECRET`,
the one shared confidential client's secret (NFR5, below) — can mint a
working credential for an operator who has not personally completed that
scope's consent; it only lets a holder refresh already-consented,
still-active grants it can otherwise reach. This is a materially smaller
blast radius than the removed impersonation-exchange design, whose
equivalent secret could mint a JWT as *any* operator currently signed
into `ui`, for any scope, without that operator ever having consented
to anything.

**NFR5 — one shared client, not one per scope.**
`WHAGENT_GRANT_CLIENT_ID`/`_CLIENT_SECRET`/`_REDIRECT_URI`/
`_ENCRYPTION_KEY` (`ENV.md`) configure a *single* confidential Keycloak
client used as the caller identity by both `ui` and `mcp` — distinct
from `WHAGENT_OIDC_CLIENT_ID`/`_CLIENT_SECRET` (which only ever verifies
or forwards a token neither binary minted itself) and unlike
`KEYCLOAK.md`'s usual "one client per caller identity" principle: scope
isolation for this flow is carried entirely by the grant key derived
from `AgentDefinition.Scope` (FR4/NFR2 above), not by provisioning a
separate Keycloak client per scope. The secret is read from the
environment only, provisioned as a Kubernetes secret, never checked in,
never logged, never echoed in an error. See
`libs/go/grpcauth/KEYCLOAK.md` § 11 (and its whagent-net-specific runbook
subsection) for the Keycloak-side client and admin-role setup this
requires.

**NFR6 — why the grant index is not a local identity store.** "No new
local identity table" means no new whagent-net-only *user/account*
identity, not "no new table": grant lookups still key on Keycloak `(iss,
sub)` plus domain (FR4) — nothing here mints, verifies, or stores a
whagent-net-local notion of "who this operator is" that could drift from
Keycloak. FR12's bookkeeping index (`grantindex`,
`(subject_iss, subject_sub, domain, preferred_username, granted_at)`) is
compatible with this: it is a pure existence index over
already-Keycloak-resolved identities, written once at successful consent
and never updated afterward — it carries no status column (every render
reads live status from `grpcauth.Store.Status` instead, so this index
and `grpcauth`'s own store never need to be kept in sync), and its one
non-identity field (`preferred_username`) is a captured display-only
snapshot, not a live lookup, since the admin page has no way to
re-derive another operator's username later.

**Cutover (FR11/NFR8, issue #2434).** Migration `009_mcpauth_cutover` is
a single, one-time deploy: every row in `mcp_credential`/`mcp_auth_code`
is deleted outright (not revoked, not time-boxed), so every
previously-minted opaque `mcpauth` credential stops working immediately
and permanently, and every operator who used the browser-OAuth2 path
before cutover must redo the per-domain consent above to regain access.
There is no feature flag, dual-read, or coexistence window between the
old and new paths. RFC 7591 client registrations (`mcp_oauth_client`)
are left alone — a registration identifies the MCP client software, not
an operator's authority. See `README.md` "Cutover" for the full
operator-facing runbook note.

Chain: subject → `api` → `worker` → domain MCP server → domain API. A
short-lived **whagent-signed JWT** (`sub` + `sub_iss` = the on-behalf-of
subject and its issuer, exactly the session's stored shape; `act` =
agent/session; `aud` = the domain server) is minted and forwarded by
`worker` on every tool call; domains verify it against whagent-net's
public key via the `//libs/go/whagent` adapter, and own the mapping from
Keycloak `sub` to their local identity (e.g. ASS `person_id`). whagent-net
is the trust root for agent actions — no Keycloak token-exchange
configuration per domain. "Which agent, for which user, did X" is then
answerable from any domain's audit log. The Phase-3 goal (an ASS research
agent acting *as the signed-in ASS user* against ASS's own MCP tools) is
the first concrete exercise of this. This is a load-bearing decision for
the product brief.

**Issuance mechanism (issue #2115).** `api` owns the signing key(s) and
publishes the public JWKS: `whagent_net/api/persona`'s `KeySet`/
`LoadKeySet` load the active asymmetric signing key (plus any retired keys
kept only for JWKS publication during a rotation window) from config/
secret — never checked in, never a symmetric fallback, fatal at startup
if unconfigured — and `JWKSHandler`/`NewMux` serve them at the fixed
`/.well-known/jwks.json` path over a small `net/http` mux alongside `api`'s
gRPC surface.

Minting itself happens **in `worker`'s own process**, not over an RPC to
`api`: `worker` is configured with the identical signing-key material (the
same `WHAGENT_SIGNING_KEY`/`WHAGENT_SIGNING_KEY_ID`/`WHAGENT_ISSUER` values
`api` reads — see `ENV.md`) and constructs its own `persona.Issuer` (via
`persona.LoadKeySet` + `persona.NewIssuer`) to mint each tool call's Claim
locally, immediately before dispatch (#2118) — one call per target server,
never reused across servers. This mirrors the "no RPC hop" package-boundary
`api` and `worker` already share for `session` (see
[Service boundary vs. package boundary](#service-boundary-vs-package-boundary)
above) rather than adding a new internal RPC surface purely to move a JWT
from one process to another.

`persona.Issuer.Issue` is therefore never registered on any gRPC or MCP
service, public or otherwise — there is nothing to reach over the network
at all. The only way to obtain a token is to already be the trusted
`worker` process, holding both the signing-key secret and direct `session`
store access needed to resolve a real session's `on_behalf_of`/`subject`
columns; there is no code path by which an external gRPC or MCP caller (or
a compromised public RPC) can mint a credential for a subject other than an
existing session's own (NFR4).

## Idempotency

Temporal guarantees at-least-once activity execution. Every tool call that
mutates state carries a key derived from `(session_id, turn, call_index)`;
`session`'s idempotency ledger records outcomes so a retried activity
returns the recorded result instead of re-executing. Domain MCP servers
must honour the key end-to-end for mutating tools (#1552 noted manmanv2's
API lacks such a field — one reason it is not a consumer of this product).

## Phasing

1. **gRPC + MCP, no UI** — `session`, `migrate`, `api`, `worker`, `mcp`
   (`worker`'s archive workflow + S3 may trail). Driven from Claude Code,
   with an agent definition targeting `audience_score_system/mcp`.
2. **Agent UI** — `embed` + `ui`.
3. **Embedded research agent in ASS** — `audience_score_system/web` imports
   `embed`, agent definition targets `audience_score_system/mcp`, persona chaining
   live.

Milestone cuts are owned by `PRODUCT.md` / `product/03-roadmap.md`; this
list is the architectural dependency order, not the roadmap.

## `mcp`'s start_session: two RPCs, one tool (issue #2120)

`mcp`'s `start_session` tool takes an optional `first_turn` field
(issue #2120's Implementation section: "agent name, optional first turn,
optional model override"), but `StartSessionRequest` (`protos/session.proto`,
issue #2117) carries no such field — the RPC surface only ever grew a
`StartSession` and a separate `SendTurn`. Rather than adding a first-turn
field to the proto (which would special-case `start_session`'s first turn
differently from every later one, for no real benefit — `SendTurn` already
exists and does exactly this job), `start_session`'s handler
(`mcp/tools/start_session.go`) calls `StartSession`, then, only if
`first_turn` was given, calls `SendTurn` against the session it just
created — two RPCs under one MCP tool call. This is a narrow, deliberate
exception to "one tool per RPC, no more" (the issue's own Implementation
section), justified because it is what lets an operator start a session
and immediately queue its first turn in one Claude Code round-trip, which
is the issue's stated intent. If `SendTurn` fails after `StartSession`
already succeeded, the tool's error says so explicitly (session created,
first turn not queued) rather than reading as if `start_session` failed
outright — the session still exists and a caller should retry with
`send_turn`, not `start_session`, against the returned `session_id`.

## Open items

- **Front door for humans before Phase 2**: `mcp` from Claude Code is the
  v1 answer; Slack via `friendly_computing_machine` is plausible later.
- **Context budgeting strategy** (summarization vs. truncation, when to
  write summary events): worker-internal, defer to the milestone that
  first hits the budget.
- **Provider abstraction**: non-goal — one provider (OpenRouter) and a
  base-URL swap covers the foreseeable need.
- **Cron-scheduled sessions**: non-goal *as a service offering* — a
  consumer wraps `StartSession` in its own Temporal schedule/workflow.
- **Human approval gate for tool calls**: non-goal for now.
- **Whagent-side tool filtering** of a server's full tool list: later
  capability; v1 relies on server-side pre-filtered endpoints.
