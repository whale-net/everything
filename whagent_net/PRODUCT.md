# whagent-net — Product brief

Product discussion: https://github.com/whale-net/everything/discussions/2075
Design record: [`ARCHITECTURE.md`](ARCHITECTURE.md)

This file is the index. Vision, Personas, Load-bearing decisions, and Non-goals are inline; the three sections with no natural ceiling are split out (`tools/project-manager/CONVENTIONS.md` § Layout):

| Section | File | Read it when |
|---|---|---|
| Current state | [`product/01-current-state.md`](product/01-current-state.md) | Checking what already exists, what is in the way, and what is genuinely missing before scoping a milestone |
| Capability map | [`product/02-capability-map.md`](product/02-capability-map.md) | Citing a `Cn` from an FR, or deciding whether a request is a new capability |
| Roadmap | [`product/03-roadmap.md`](product/03-roadmap.md) | Running `/project-manager:design <tracking-issue> --milestone M<n>`; the milestone entry is the scope contract |

Live milestone status is not in this file or its splits — it is the last `Ledger: M<n> → <status>` comment on the tracking issue (`Product: whagent-net`, label `product:approved`).

## Vocabulary

An **agent** is a named definition — a model, a tool set (one or more domain MCP servers), caps, and the role required to run it. A **session** is one running instance of an agent; its **transcript** is the append-only record of what happened; **context** is what the model sees on a given turn. "Agent" always means the definition and "session" always means the running thing; the word "preset" is not used. Context is deliberately invisible to every persona until C24. (Not to be confused with Claude Code plugin agents such as `tools/project-manager/agents/*` — whagent-net never uses "agent" for a persona.)

## Vision

whagent-net is the one backend for every agent whale-net runs: a simple harness, reachable natively in the cloud, that can be tool-enabled. A session is a long-lived conversation whose history outlives any process, whose tools are the domain-owned MCP servers its agent is pointed at, and which can be driven and watched from Claude Code, a web UI, another session, or a service — all reading the same transcript. A year out, new agent services are built on it rather than as one-off processes, existing agent services have been replaced by it, and a domain adds an agent by writing an MCP server and an agent definition, not by writing an agent loop.

## Personas

- **Operator / developer** — builds and runs agents; drives sessions from Claude Code (via the MCP surface) or the standalone web UI; owns agent definitions (model, tools, caps, required role).
- **Consumer-domain developer** — maintains a domain (first: `audience_score_system`) and wants an agent over that domain's tools: ships the domain's MCP server against the shared tool contract, maps on-behalf-of identities to the domain's own user records, and later embeds the session component in the domain's own web UI.
- **ASS Creator / Analyst** — a signed-in `audience_score_system` user who runs a research agent inside ASS's web UI; the session acts as them against ASS's existing MCP tools.
- **Parent session** — a session that starts and drives other sessions through the MCP surface, as one of its tools.
- **Headless service caller** — a scheduler or another service (a service account, e.g. a future `ass-worker` Keycloak client — ASS's worker has none today, so this is a consumer-side prerequisite, not something a milestone may assume) that starts sessions with no human present, wrapping session start in its own Temporal workflow.
- **On-call viewer** — a human watching a live or finished session to understand what a session did; reads, never drives.
- **Slack user** — asks an agent something from Slack and reads its replies in the thread (user story only; not wired through `friendly_computing_machine` in this iteration).

## Load-bearing decisions

*(architect, discussion #2075 round 1 — verbatim except where an answered question or an accepted round-2 nitpick closed a clause; those edits are marked)*

```
LB1 — Transcript event record: identity, ordering, and one shape across all three tiers
  At risk: C17 (live follow, dedup on re-publish), C18 (cold reads must be the same record),
  C24 (per-turn context = list of event IDs), C21 (child sessions readable the same way).
  Decide now: every transcript event has a globally unique, stable `event_id` (UUIDv7 or
  equivalent time-ordered), a per-session monotonic `seq`, `turn`, `type`, and a typed JSON
  `payload`; the Postgres row, the RMQ message body, and the S3 jsonl line are that same
  record, not three projections. Each turn persists the ordered event-ID list its context was
  built from. Consumers dedup on `event_id`, order on `seq`.
  Stays cheap: the set of event types, per-type payload internals, summarization strategy,
  hot-tier TTL, whether S3 is per-session jsonl or something else.

LB2 — Session identity and subject shape
  At risk: C9, C10 (headless + on-behalf-of), C15 (search by who started), C20, C21
  (parent/child link), C25 (rollup per user).
  Decide now: session_id == Temporal workflow ID; the `sessions` row carries `subject`
  (the authenticated caller: `iss`+`sub`, plus kind human|service) AND `on_behalf_of` (same
  shape, never null) as separate columns from day one, and a nullable self-FK
  `parent_session_id`. `on_behalf_of` is always populated: it equals `subject` whenever the
  caller acts for itself, regardless of kind; it differs only when a host or service is
  delegating for a human. [round-2 nitpick 2 folded: one encoding of "acting for self".]
  Stays cheap: authorization policy, how the UI renders "started by", parent/child views,
  adding a display name cache.

LB3 — Persona claim: the wire contract a domain MCP server verifies
  At risk: C9, C12, C20; and ASS's own LB4 (its idempotency guard is keyed on person_id).
  Decide now: `libs/go/whagent` defines one verifiable credential the worker presents on every
  tool call — a short-lived JWT minted and signed by whagent-net's `api` with whagent-net's own
  key (Keycloak is verified only at the front door; no per-domain token-exchange config),
  carrying `sub` and `sub_iss` (the on-behalf-of subject's `iss`+`sub`, i.e. LB2's shape
  verbatim), `act` (acting subject + agent id), `whagent_session_id`, `aud` = the domain
  server — and a verifier the domain server mounts against whagent-net's public key (for ASS:
  `libs/go/whagent`'s verifying middleware runs before `mcpauth`; ASS's `CallerResolver`
  reads the verified claims off the request and maps `(iss, sub)` → Person).
  whagent-net is the trust root for agent actions. [Q5 resolved: whagent-minted.
  Round-2 nitpicks 3 and 4 folded: `sub_iss` in the claim set; the whagent piece is a
  verifying middleware, not a `CallerResolver`.]
  The `sub` → local identity map is owned by the consuming domain, as part of C12. [Q2 resolved.]
  Stays cheap: extra claims, role sets, how each domain maps sub → local identity, rotation.

LB4 — Tool-call idempotency key: name, derivation, scope
  At risk: C11, C12, C23 (a tool-set change mid-session must not reset keys), and every
  future domain server.
  Decide now: the contract field is `idempotency_key` (string, top-level tool arg — matches
  what ASS already ships), derived deterministically from `(session_id, turn, call_index)`
  and never regenerated on activity retry; domain servers scope their guard on
  `(tool, resolved identity, key)`. whagent keeps its own ledger of (key → outcome).
  Stays cheap: ledger storage, retry policy, fingerprint rules, read-only tools ignoring it.

LB5 — Agent definition as a versioned record, pinned to the session with SCD2 assignment
  At risk: C22 (whagent-side allowed-tool list is a field on the definition), C23
  (mid-flight change is a new SCD2 assignment row), C25 (rollup per agent needs a stable id),
  C5 (per-session model override recorded, not applied by mutating the definition),
  C8 (the required role is a field on the definition, not a whagent ACL table). [Q6 resolved.]
  Decide now: agent definitions live in a table with a stable `agent_id`, versioned rows,
  a tool set expressed as `[{server_url, allowed_tools: []|null}]` (null = whatever the
  server exposes), model, caps, and `required_role` (a Keycloak realm/client role checked
  against `grpcauth.Claims.Roles`). `session_agent` (SCD2, valid_from/valid_to, partial
  index on valid_to IS NULL) records which version a session runs; the session row stores
  its own model/cap overrides. M1 may seed the table from config, but it is a table.
  Stays cheap: definition CRUD/UI, whether whagent enforces allowed_tools yet (C22),
  config-file vs. UI authoring, role naming scheme.

LB6 — Per-turn usage record
  At risk: C6 (cost cap accuracy), C16 (usage vs caps), C25 (rollups).
  Decide now: each turn commits a usage event/row with `model`, prompt/completion tokens,
  cost (fixed-point USD, never null: provider-reported when present, otherwise estimated
  from tokens against a configurable per-model price table) with an `estimated` flag, and
  the provider's generation id; the worker always requests provider usage reporting; caps
  are evaluated from the sum, never from a mutable counter alone. Unknown cost is never
  treated as zero. [Q4 resolved: estimate + flag, never fail-open.]
  Stays cheap: the price table's contents and source, rollup queries, currency display.

LB7 — Event bus contract owned by one package
  At risk: C14, C17, C19 (a host UI in another domain binds to the exchange), C26.
  Decide now: `whagent_net/events` (same shape as `tools/app_registry/events`) owns the
  exchange name, `DeclareArgs()`, the routing-key scheme `session.{id}.{type}`, and the LB1
  payload; publisher and every consumer import it. Publish after commit; duplicates allowed.
  Host UIs that embed the session component (C19) subscribe to this exchange directly with
  their own `htmxsse.Hub`; C17 (`api.StreamEvents`) is an independent capability and not a
  prerequisite of C19. [Q7 resolved.]
  Stays cheap: queue TTLs, consumer count, whether `api.StreamEvents` fronts it.
```

## Non-goals

- **Multi-provider abstraction in v1.** One provider (OpenRouter) is the serving path. A second provider, if ever needed, is a later capability — not a v1 abstraction layer.
- **Multi-tenant hosting.** Single tenant: whale-net's own agents only.
- **Cron-scheduled sessions as a service offering.** whagent-net does not schedule sessions; a consumer that needs recurring agent runs wraps session start in its own Temporal workflow.
- **Human approval gate on tool calls.** No "pause until a human approves this tool call" state, for now; focusing an agent is done by tool selection (C7, C22), not approval.
- **Python or other non-Go consumers.** No Python SDK, no JS bundle; the embeddable component is a Go package and consumers are Go services.
- **Owning domain tools.** whagent-net never ships a *domain's* MCP server; domains own theirs against the shared contract. The one MCP server whagent-net does ship (`whagent_net/mcp`) is the framework's own control surface — start/send/stop/read sessions — not a domain tool set.
- **manmanv2 integration.** Not part of this product; manmanv2 agent work is still at idea stage and would be its own request.
- **Uncapped sessions.** Every session has a turn and cost cap; there is no "unlimited" setting.
