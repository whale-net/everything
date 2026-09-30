# Current state

*(architect, discussion #2463 — Product mode. `krill/` does not exist; this surveys what krill **replaces** and what it **builds on**.)*

## Summary

Three findings change how M1 should be scoped:

1. **The pipeline being replaced is 1,986 lines of markdown and zero lines of code.** `tools/project-manager/` has no implementation — the only executable it references is `tools/agentsync-mcp` (Python, file+`flock`). Migrating off it is a conventions migration, not a port. There is no legacy code to strangle and no compatibility window to design.
2. **Postgres-as-queue with `SKIP LOCKED` + lease + stale-reclaim already ships in this repo**, and its own migration comment says queue tables are explicitly *not* SCD2. That is an in-repo precedent that contradicts the intake's "SCD2 throughout" framing — see LB3.
3. **`libs/go/whagent`'s shipped `Claim` already carries `act.AgentID` alongside `act.Subject`.** The wire slot for C24 (verifiable agent identity) exists today. C24 is cheap *if* krill's own records carry the same two-subject shape from M1 — see LB4.

## What exists and is reusable

### `tools/project-manager/` — the pipeline being replaced

Prose only: `CONVENTIONS.md` (557 lines, the contract), 11 `agents/*.md` personas, 11 `skills/*/SKILL.md` entry points, `plugin.json`, `mcp_config.json`. It has worked well, so the survey question is not "what's wrong with it" but **which parts are the product model and which parts are GitHub's shape leaking into the model.**

**Genuine conventions — krill should model these as entities, not delete them:**

| Convention | Where | Why it's real |
|---|---|---|
| FR cites the capability it serves (`FR4 (C3) — ...`); an FR that can't cite one from `Delivers` doesn't belong | § Scope control | This is *the* scope control. "A bare FR cap just gets gamed by writing wider FRs." Maps 1:1 to krill's parent-FK chain. |
| LB three-clause form; `Delivers` / `Must not foreclose` / `Deliberately deferred` / `FR budget` per milestone; outcome sentence names an actor | § Brief sections, § Load-bearing decisions | These are the rigid write-time entry rules #2423 wants enforced at write instead of at review. |
| `Depends on:` gates **both** starting and merging | § Task granularity & merge cadence, § Continuous merge to trunk | The single most load-bearing convention in the doc. Continuous merge is "safe by construction, not by inspection at merge time" *because* a task can't start until its deps are `Done`, and deps are on trunk by then. This is exactly why krill's queue must be a dependency-gated **view**, not a FIFO — the intake's call is correct and it is inherited, not invented. |
| Per-task lane sequence, lanes skippable ("docs-only tasks or pure refactors start at or skip to the appropriate swimlane"), failure routes *back* a lane | § Task issues & swimlane progression | Already per-task, already a graph, already bidirectional. |
| Validation at two levels: `validator` per task (read-only, acceptance criteria), `system-validator` per plan | § Worker lifecycle, § System validation | #2423's "per-pebble validator is just a task that `Depends on:` every task in the pebble" is a strict improvement on this, not a new idea. |
| Caps on every loop: stakeholder rounds 2/3, design-panel rounds 3, FR budget 12, LB entries 3–8 | throughout | Krill's attempt cap and thrash cap are the same family. The *discipline* is proven; what's missing today is that no loop in the **work** axis has one (see Open question 2). |
| "Ship what shipped, change what is ahead" | § Amendments | Directly becomes `partially complete` + truncate-never-rewind. |
| Expand-contract sequencing is planner's job, and continuous merge "trusts that graph; it doesn't re-derive it" | § Continuous merge to trunk | Krill routes from the task's own recorded sequence for the same reason. |

**Workarounds for GitHub's shape — krill deletes these rather than porting them:**

| Workaround | Why it exists (the doc says so) | What replaces it |
|---|---|---|
| The working-draft gist (§ Working draft) | "A draft reposted in full on every round makes every later read replay the *entire* comment history… cost grows with round count." | C10: typed draft revisions. This convention is a whole subsection that evaporates. |
| `Ledger: M<n> → <status>` / `Amendment: <slug> → <status>` append-only comments, "the **last** one wins" | "A read-modify-write update to one shared body table would race." A hand-rolled last-writer-wins register over issue comments, because GitHub has no transactional row. | A status column and an `UPDATE`. |
| `adopted-but-pending-amendment`, "the ledger never carries more than 2 pending adoptions" | Ground truth drifts ahead of the spec because the spec is a file a human can hand-edit. | LB5 (docs-as-projection) removes the second writable source. |
| Zero-FRs-in-a-brief | #2423 diagnosed this precisely: it exists only because a brief is one markdown file read cold. | Relaxable once FRs are queryable and scoped. **Not yet** — this brief is a today-conventions artifact, and producer's call to keep the rule for it is right. |
| `AGENTS.md` § Size Limits & Splitting (the whole thing) | An agent must remember to notice a file crossed ~1000 lines. | Renderer logic (C7). |
| § API call volume & rate limits (batch aliases, "never re-derive what the caller already resolved", serialize anything touching `main`) | One shared `gh` budget across concurrent plans. | The trigger for the whole product. |
| Branch name `pm[<attempt>]-<root>/<task>-<slug>`, "derived the same deterministic way every time it's needed… never invented fresh or persisted anywhere separately" | There is nowhere else to put task state, so task state is **encoded in a branch name**. `<attempt>` is a retry counter living in a git ref. | A row. This is the cleanest single example of GitHub's shape forcing a design — and it is the condition attached to C20 below. |
| Claim = `gh issue edit --add-assignee @me` | Assignee-as-lock. No lease, no expiry, no heartbeat, no attempt counter. | `claim`/`heartbeat` + lease. Today a dead worker holds its task forever and nothing notices; there is no code path that can distinguish it. |
| `plan:approved` vs `plan:agent-approved` — "nothing downstream branches on which label is present" | Labels as the only enum available. | A column. |

**One ambiguous case worth naming.** `CONVENTIONS.md` line 5 concedes that each persona file inlines mechanics from `CONVENTIONS.md` and that "the inlined copies can drift from this doc over time — that's an accepted tradeoff for keeping routine dispatches cheap." That is the context-scoping thesis stated as a known, accepted defect, in the document krill replaces. It is also a good conformance target: if krill's scoped query works, that tradeoff stops being necessary.

### `whagent_net/` — shipped M1, M2 in flight

- **Three-noun split (session / transcript / context)** and, importantly, **a deliberate mix of mutation shapes**: `agent_definition` assignment is SCD2; `transcript_event` and `transcript_archive` are annotated "explicitly **not** SCD2". Precedent for LB3's boundary, not against it.
- **Storage tiering** (RMQ bus / PG hot / S3 cold) with a written **Cold-object contract**: index row, object key, object body, read-side merge with hot winning on duplicate `seq`. Their LB1 is "one record shape across all three tiers, not three projections" — the directly analogous discipline for krill is LB7.
- **The tool contract** (`ARCHITECTURE.md` § Domain-owned MCP servers): an *agent definition* is a named tool set, and **tool filtering is enforced server-side by exposing a pre-filtered endpoint** (`/mcp/readonly` vs `/mcp/ops`). This matters more than it looks: krill's "a worker never reads the spec surface" can be a **mount-point property** (two MCP endpoints, one server) rather than a rule agents are trusted to obey. That is the difference between a convention and an invariant.
- **Identity** (`ARCHITECTURE.md` § Identity and auth chaining, their LB2): `subject` and `on_behalf_of` as `(iss, sub, kind)` pairs, **both always populated**, `on_behalf_of = subject` when acting for self; `iss` a real column so a non-Keycloak issuer is a row, not a migration. Read *vs* control are two different rules (reads unowned, writes gated on `on_behalf_of`). This is krill's accountability-chains-to-a-human problem already solved one layer up.
- **whagent-net non-goals cron-scheduled sessions**, so krill schedules its own jobs — confirmed.
- **No roadmap collision.** whagent M1 (Claude-Code session against ASS), M2 (standalone UI + cold tier), M3 (embed in ASS's UI). Nothing does spec-of-record or work tracking. #2423's finding holds.

### `libs/go/`

| Package | State | Relevance |
|---|---|---|
| `whagent` | Shipped, **no dependency on `whagent_net/`** | The published claim contract. `Claim` = `sub` + `sub_iss` + `act` (`Actor.Subject` **and** `Actor.AgentID`) + `whagent_session_id` + `aud` + `iss`. The README forbids adding convenience profile fields ("that would be a contract change, not a nicety"). Cost of entry: mount the middleware, verify against the JWKS, accept a top-level `idempotency_key`, own the `(sub_iss, sub)` → local identity map. |
| `grpcauth` | Shipped and **actively moving** — #2397/#2399/#2400/#2406/#2407/#2408 landed delegated grants, PKCE authorization-code consent, encrypted `pgstore`, RFC 7009 revocation, all within the last few weeks | Keycloak OIDC verification, `Claims{Subject, Roles, Audience, ClientID, IsServiceAccount}`. Worth re-checking at M1 design time rather than at brief time — the delegated-grant work is close to krill's "accountability chains to the human who called `init`." |
| `mcpauth` | Shipped | OAuth2 authorization-server front end for MCP clients. Half of the two-front-door pattern. |
| `db`, `migrate` | Shipped | `migrate.RunCLI(embed.FS, "migrations")` + seeder; deployed as a Helm `job`-type app run as a pre-install/pre-upgrade hook. The standard new-domain shape. |
| `s3`, `rmq` | Shipped | Cold tier and optional wake-up, per #2423. |

### `audience_score_system/mcp` — the working two-front-door template

`main.go` mounts `mcpauth` (human/OAuth2) **and** `whagent`'s verifying middleware (agent), both optional by env var, with `server/idempotency.go` alongside. This is the exact shape krill needs (Swarm Operator + Requirement Contributor via Keycloak roles; Agent via a minted capability), already built and tested. `audience_score_system/` is also the best template for the domain's doc set and its `product/*.md` split.

### `tools/app_registry` — two precedents nobody named in intake

1. **The Postgres queue already exists.** `migrate/schema/migrations/004_writeback_outbox.up.sql` + `server/repository/postgres/writeback.go`'s `ClaimBatch`: one atomic `UPDATE … WHERE id IN (SELECT … ORDER BY created_at LIMIT n FOR UPDATE SKIP LOCKED)`, `status`/`claimed_by`/`claimed_at`/`attempts`/`last_error` columns, stale-claim reclaim via `claimed_at < staleBefore` ("a worker killed mid-run leaves a row `claimed` with a stale `claimed_at`; `ClaimBatch` reclaims it… rather than stranding it forever"), partial indexes on `(created_at) WHERE status='pending'` and `(claimed_at) WHERE status='claimed'`. That is lease expiry, redelivery, and attempt counting, shipped. Krill's work axis is a generalization of a working thing, which lowers its risk considerably.
2. **The migration comment is explicit:** *"Append-only + claimed, not SCD2: this is a work queue, not a slowly changing dimension — see AGENTS.md, queue/event tables get their own shape, not `valid_from`/`valid_to`."* Combined with `AGENTS.md` § SCD2's own "Do not apply SCD2 to append-only event logs," this is a settled repo position that "SCD2 throughout" collides with. See LB3 and Open question 6.

`tools/app_registry` is also the worked example `AGENTS.md` cites for the doc shapes C7's renderer must produce, including the recursive split (`architecture/08-release-lifecycle/`) and the current/history split (`PLAN-HISTORY.md`).

## What exists and is in the way

- **`AGENTS.md` § Maintaining Docs** requires docs to be updated in the same task that changes the code, and `AGENTS.md` § Documentation Conventions treats a skeleton file as non-authoritative. Generated read-only files contradict both. #2423 already flagged the carve-out as deferred; it is cheap to write but it must land *before* anyone other than the requester depends on generated docs, or an agent will hand-edit a rendered file and lose the edit.
- **`CONVENTIONS.md` § Product brief & milestones**: "A product maps 1:1 to a top-level domain… Multiple products per domain, or one product spanning several domains, isn't supported today." Krill's C23 (decisions spanning several products) and the monorepo-ADR thread both sit outside what today's model admits. Not an M1 problem; it is the reason LB1 uses a surrogate scope key.

## What is half-built or recently reverted

**`tools/agentsync-mcp` is a live MCP server for a feature that was removed from the pipeline.** Commit `140be0e4 project-manager: remove agent-sync mode from design/product loops (#2074)` took agent-sync out of the design and product loops, but the server still exists, `tools/project-manager/mcp_config.json` still declares it, and `agents/architect.md` still documents an "Agent-sync mode" section. So the repo contains one prior attempt at agent-to-agent rendezvous, backed out of the loops that used it, with its plumbing still in place. That is the factual record.

> **Retraction, by this section's author, applied by producer in round 3.** The original paragraph drew an inference from the above: that a withdrawn rendezvous primitive is evidence for holding krill's work surface at six verbs. Architect retracted the load-bearing part of that in its round-2 reconciliation — "arguing the six-verb hold *from* a withdrawal whose reason was never recorded is an argument from an undiagnosed event" — and authorized this edit; the requester has separately put `tools/agentsync-mcp` out of scope for krill entirely. The six-verb decision now stands on LB7 plus payload-enrichment-is-not-a-verb, in M4's design notes, and is stronger without the crutch. Nothing else in this brief leans on the retracted inference.

Also: `git log --oneline -30` shows `release.bzl`/`release_helper_go` churn in the last three commits (`3265f61b`, `2bb4eceb`, `1117e0c5`) fixing a Bazel/Go desync in domain-app image naming. Standing up a new domain touches that machinery; it is settled as of `main` but it moved this week.

## What genuinely does not exist

- `krill/` — no directory, no module, no schema, no BUILD target. Confirmed: `grep -ri krill` returns nothing outside `.git`.
- **A markdown renderer over a store.** Every other piece of krill has an in-repo template. C7 does not — nothing in this repo generates committed documentation from a database. It is the one genuinely novel component in `Now`, and the one with no precedent to copy.
- **New-domain scaffolding** is well-trodden but not free: `BUILD.bazel` with `release_helm_chart`, a `migrate` job app, `Tiltfile`, `ENV.md`/`TOC.md`/`ARCHITECTURE.md`/`README.md`, and a `plugin/` directory for the Claude Code MCP entries (cf. `whagent_net/plugin`, `tools/app_registry` plugin). No capability covers it and none should — but M1's FR budget has to survive it.
