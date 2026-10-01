# krill plugins — shared conventions

Shared contract for the `krill-design` and `krill-work` plugins. Fallback for
mechanics not covered in a persona file itself.

This file is symlinked into both plugins' directories — edit only here.

## Known limitations

**A GitHub issue or project call in any plugin step is a bug — fix it, don't
route around it.** Spec, design, and work tracking (design sessions, milestone
status, claim, lane advance/revert, notes, dependencies) is entirely
krill-native. The only `git`/`gh` use is code integration: `mergepush`
pushing task branches and opening/merging code PRs, and the worker/validator
branch-and-worktree commit mechanics.

@snippets/task-lifecycle-blocker.md

Tools that work today from an ordinary session (resolve
`PersonaSwarmOperator`): `create_task`, `declare_task_dependencies`, the M5
ops-write tools (`release_task`, `requeue_task`, `escalate_task`,
`cancel_task`), `transition_note_lifecycle`, and every milestone/product/
delivery-authoring tool (`create_product`, `create_feature_set`,
`create_load_bearing_decision`, `create_persona`, `create_non_goal`,
`amend_product`, `amend_feature_set`, `amend_feature`, `amend_requirement`,
`amend_load_bearing_decision`, `amend_persona`, `amend_non_goal`,
`amend_milestone`, `amend_milepebble`, `amend_deferral`, `propose_entities`,
`create_milestone`, `set_fr_budget`, `add_delivers`,
`add_must_not_foreclose`, `add_deferral`, `create_milepebble`,
`add_milepebble_scope`, `add_discovered_scope`, `move_delivery_scope`,
`mark_delivered_item_shipped`, `abandon_milestone`, `set_milestone_status`).

**One open capability gap, not a plugin oversight:** `list_tasks
{milestone_id}` (ungated, any persona, `/mcp/work`, also `/mcp/design`)
returns every task under a milestone/milepebble — id, title, `current_lane`,
attempt count, and whether a claim is currently live — so `planner`'s
hand-carried summary isn't the only way to find a milestone's task ids (see
"Work axis" below). It does **not** filter by claimable: unresolved
dependencies, the attempt cap, and an active escalation are invisible to it —
`claim_task` still needs a `task_id` already in hand, and a caller still
cross-checks `get_task {id}` per candidate before calling it.

Milestone authoring and delivery status/shipment/recut/abandon mount on
`/mcp/design` (`krill/mcp/main.go`'s `designReg`); the work axis's own
task-lifecycle tools (`create_task`, `declare_task_dependencies`,
`claim_task`, `heartbeat_task`, `complete_task`, `record_note`,
`transition_note_lifecycle`) mount on a separate `/mcp/work` server
(`workReg`) instead — `krill-work`'s `.mcp.json`/`mcp_config.json`
register `krill-mcp-work-{tilt,dev,prod}` (pointing at `/mcp/work`), not
`krill-design`'s `krill-mcp-design-{tilt,dev,prod}`, so the two plugins no
longer share a write surface. `get_task`, `list_tasks`, and `abandon_task`
are the deliberate exception: all three are registered on **both**
`/mcp/design` and `/mcp/work`, so `krill-design` keeps ad hoc task
discovery/read/cleanup access even though no design persona's own
instructions call any of them today. A **Swarm-Operator-only** `/mcp/ops`
mount (`list_claimed_tasks`, `list_cancelled_tasks`, `list_escalated_tasks`,
`list_open_notes`, `release_task`, `requeue_task`, `escalate_task`,
`cancel_task`) exists for human debugging only — no persona here needs or
registers it.

## Session bootstrapping

Every write tool on `/mcp/design` and `/mcp/work` — including `claim_task`,
`heartbeat_task`, `complete_task` and `abandon_task` — requires a
`krill_session_id` (every read tool is ungated). Mint one first: `init_session {}` (no arguments) →
`{session_id, scope_id}` — no persona restriction. Your identity
(`acting`/`on_behalf_of`, and `whagent_session_id` for a whagent-net agent) is
derived server-side from your verified credential; the tool accepts no
identity or scope fields, and supplying any is rejected. `scope_id` is the
deployment's sole scope — the value `list_products`, `list_tasks`, and the ops
console tools (`list_claimed_tasks` etc.) take as input. To learn it without
minting a session, call the read-only `get_scope {}`.

A mediated call (a Requirement Contributor's ask relayed by an Agent via
`propose_entities`) gets distinct acting/on-behalf-of from the credential
itself (a whagent claim), never from client input.

`krill_session_id` values don't survive a plugin-connection reset
(`/reload-plugins`, a dropped MCP auth session) — mint a fresh one rather
than reusing an id from a prior connection.

### Two MCP mounts, one backend

`krill-design` and `krill-work` each declare their own connection to the
same krill deployment. Authenticating one plugin's connection does **not**
authenticate the other's — after a `/reload-plugins` or similar, expect to
re-`authenticate`/`complete_authentication` on whichever plugin's tools you
call next. A `krill_session_id` minted through either plugin's
`init_session` works identically in the other's tools (same backend) —
only the MCP-level connection/auth is per-plugin.

## Design session model

A design conversation is a **DesignSession**: an `opening_submission` (free
text, no entity refs) plus an append-only log of **RevisionEvents**. There
is no separate "working draft" document — the log *is* the draft, and
`get_design_session_slice` gives you the current typed slice
(FeatureSet/Feature/Requirement/Decision) of every entity any event in the
session has touched.

- **Open a session**: `open_design_session {krill_session_id, product_id,
  opening_submission}` → `{id}`. One session per design conversation.
- **Record a step**: `append_revision_event {krill_session_id,
  design_session_id, event_type, verified_against?, entity_deltas[],
  open_questions_delta?, signoff_status?}` → `{id, seq_no}`.
  - `event_type` is a closed enum: `draft | reconciliation | answer | signoff |
    ruling`. This *is* the phase model — there is no separate state machine.
    - `draft` — producer drafting/revising.
    - `reconciliation` — architect reconciling.
    - `answer` — producer answering open questions raised in a reconciliation.
    - `signoff` — a human or the `reviewer` persona approving/requesting
      changes. Requires `signoff_status` (`approved | changes_requested`);
      forbidden on any other event type.
    - `ruling` — the `reviewer` persona breaking a stakeholder disagreement.
  - `verified_against` (e.g. `"main@<sha>"`) is **required** for
    `draft`/`reconciliation` and **forbidden** otherwise.
  - `entity_deltas: [{entity_id, change: created|updated, summary_line}]`
    names what the event actually touched. Don't leave this empty for an
    event that changed entities.
  - `open_questions_delta: {opened: [{question_id, blocking, text}],
    resolved: [question_id, ...]}` raises and closes open questions. There
    is no separate "open question" table — `list_open_questions` derives
    the open set by replaying deltas in `seq_no` order (last mention wins).
    Give every question a stable `question_id` you'll reuse when resolving
    it.
- **Propose new entities**: `propose_entities {krill_session_id,
  design_session_id, verified_against, proposals: [{kind: feature|requirement,
  parent_id | parent_proposal_index, name, body?,
  requirement_kind?, summary_line}]}` → `{revision_event_id, seq_no, entities:
  [{kind, id}]}`. This is the *only* way new Features/Requirements get
  created. Requires a **mediated** session (`Acting` distinct from
  `OnBehalfOf` — `ErrMediatedIdentitySame` otherwise): propose on behalf of
  a specific human, never on behalf of oneself. `parent_proposal_index`
  (0-based, into this same call's `proposals[]`) lets one call create a
  Feature and its Requirements together.
- **Read back**: `get_design_session {id}` (session + full revision_events
  log), `get_design_session_slice {id}` (typed entity slice),
  `list_open_questions {id, blocking?}`.
- **There is no separate "root plan" artifact.** Once a `signoff` event with
  `signoff_status: approved` lands, the Feature/Requirement entities *are*
  the approved plan — query them with `get_feature_set_slice`/
  `get_feature_slice`.

Read-only slice queries (`get_feature_set_slice`, `get_feature_slice`,
`get_requirement_slice`, `get_product_slice`, all `{id}` → `slice.Document`)
are never gated and available to every persona. Each takes a surrogate id
you must already have — `list_products {scope_id}` → `{products: [{id,
name, vision}]}` (ungated, `/mcp/design`) is the discovery entry point for
`get_product_slice`'s `product_id`; its `scope_id` comes from
`init_session`'s response (see "Session bootstrapping" above).

### Stakeholder meeting records

A stakeholder meeting round is recorded on the DesignSession itself, as open
questions, so every persona reads it back with `list_open_questions`/
`get_design_session` and no second store exists. The meeting skill appends
one `reconciliation` event per round (the round reconciles the draft against
each persona's needs; `verified_against` is required like any
`reconciliation`) whose `open_questions_delta.opened` carries:

- `SM-<N>` (`blocking: false`, opened **and** `resolved` in the same event) —
  the round's marker: attending personas and `cleared` or `blocked (<k>
  blockers)`. Counting `SM-` ids in `get_design_session` gives the next
  round number.
- `SB-<N>.<n>` (`blocking: true`) — a consolidated blocker: the Requirement
  or entity id it attaches to, what breaks for the persona, and the outcome
  that resolves it. Producer resolves it with an `answer` event, exactly like
  an architect question.
- `SF-<N>.<n>` (`blocking: false`) — guidance or non-blocking feedback,
  text prefixed `Guidance (<persona>):` / `Feedback (<persona>):`. Producer
  resolves it in an `answer` event once folded in or declined.

`reviewer`'s ruling is a `ruling` event whose `resolved` names every
overruled `SB-` id (each with a non-blocking `SR-<N>.<n>` opened alongside:
`Overruled SB-<N>.<n>: <rationale>`) and which re-opens every sustained
`SB-` id under the same id with text `Sustained: <the Requirement change
producer must make>`.

## Milestone and delivery-axis tools

Every write tool below is allow-listed to `{PersonaRequirementContributor,
PersonaAgent, PersonaSwarmOperator}` — an ordinary session's
`PersonaSwarmOperator` identity works today, calling on its own behalf, same
as the read-only tools below (`get_milestone`/`list_milepebbles`/
`list_product_delivery`/`get_backlog`, never gated).

- `create_milestone {krill_session_id, product_id, name, outcome,
  fr_budget?}` → `{id}`. `name` is the bare identifier (`"M3"`); `outcome`
  is a one-sentence outcome. `fr_budget` is optional and has no default —
  see "FR budget" below.
- `set_fr_budget {krill_session_id, milestone_id, fr_budget}` — revises a
  milestone's or milepebble's budget (`milestone_id` takes either id); only
  the latest value reads back.
- `add_delivers {krill_session_id, milestone_id, entity_id}` — associates a
  Feature or Requirement into the milestone's `Delivers` set. Call once per
  entity; idempotent.
- `add_must_not_foreclose {krill_session_id, milestone_id, entity_id}` —
  same mechanism, for the `Must not foreclose` list architect's
  Load-bearing check reads.
- `add_deferral {krill_session_id, milestone_id, body, destination}` —
  records one deliberately-deferred item; `destination` is required.
- `get_milestone {id}` → authoring fields plus `Delivers`/`Must not
  foreclose`/deferrals, read-only, no session gate.
- `create_milepebble {krill_session_id, milestone_id, name, outcome,
  fr_budget?}` → cuts a sub-milestone container; `add_milepebble_scope` associates an entity
  already in the parent milestone's own `Delivers` set (rejected
  otherwise); `add_discovered_scope` creates a new Feature/Requirement
  *and* associates it to a milepebble (and the parent milestone's
  `Delivers` set) in one call, for scope discovered mid-milestone;
  `list_milepebbles {milestone_id}` → every milepebble in position order,
  each with its `fr_budget`.
- **FR budget (default 12) is per milepebble, not per milestone.** A
  milestone has no Requirement cap, so a complex milestone can be planned
  in full and cut into milepebbles of at most 12 Requirements each (every
  Requirement delivered by exactly one milepebble). A milestone with no
  milepebbles cut yet is its own single milepebble, so the 12 applies to it
  directly.
- `set_milestone_status {krill_session_id, milestone_id, status, note?}` —
  `status` is one of the fixed eight: `not started, in design, designed,
  planned, in progress, shipped, partially complete, abandoned`. Only the
  transitions below are accepted; anything else is rejected naming the
  illegal edge and the legal alternatives, and re-setting the status a
  milestone already holds is a no-op that writes no history row.

  ```
  not started        -> in design | abandoned
  in design          -> designed | abandoned
  designed           -> planned | in design | abandoned
  planned            -> in progress | abandoned
  in progress        -> partially complete | shipped | abandoned
  partially complete -> in progress | shipped | abandoned
  shipped            -> (terminal)
  abandoned          -> (terminal)
  ```

  `designed` is the rung between a decided design and a committed plan;
  `designed -> in design` is the rework edge (`planned -> in design` is
  not legal), and `in progress <-> partially complete` is the
  partial-completion loop. `get_milestone_status` (current) and
  `get_milestone_status_history` (every transition, chronological, with
  actor) are real queries.
- `mark_delivered_item_shipped {krill_session_id, milestone_id, entity_id,
  note?}` and `get_delivery_breakdown {milestone_id}` (→ shipped/unshipped
  entity sets) — per-item shipment tracking within a milestone/milepebble.
- `move_delivery_scope {krill_session_id, entity_ids[], from, to}` and
  `get_backlog {product_id}` — re-cuts not-yet-shipped scope between
  milestones/milepebbles/the backlog bucket; refuses (writes nothing) if
  any entity is already shipped in its `from` container. `get_backlog`
  returns the bucket's own `milestone_ref` id alongside its contents, so
  `to` can be that id on a product whose bucket has never been used.
- `abandon_milestone {krill_session_id, milestone_id, note?}` — marks a
  milestone or milepebble abandoned and sweeps its not-yet-shipped scope
  into the backlog in one transaction. **Not reversible.**
- `list_product_delivery {product_id, statuses?}` — every milestone/
  milepebble under a product, optionally filtered by status.

## Work axis: task lifecycle

The full verb set: `create_task`, `declare_task_dependencies`,
`claim_task`, `heartbeat_task`, `complete_task`, `abandon_task`,
`record_note`, `transition_note_lifecycle`, plus `get_task` to re-read
current state. **A krill `Task`'s `current_lane` is never stale** — every
verb below mutates it directly; there is no second source of truth.

- `create_task {krill_session_id, milestone_id, title, body?,
  lane_sequence[], starting_lane}` → `{id}`. `lane_sequence` is an ordered
  subset of `{Scaffold, Implementation, Testing, Validation, Done}` (lanes
  skippable); `starting_lane` must be a member. `milestone_id` must be a
  milepebble, or a milestone with no milepebbles cut from it yet (NFR7 —
  never a bare Feature/Requirement id). **Restricted to
  `PersonaSwarmOperator`** — works from an ordinary interactive session
  (the normal way this plugin is used). Fails "forbidden" under a fully
  unattended whagent-net-authenticated dispatch with no human present —
  stop and say so plainly rather than working around it.
- `declare_task_dependencies {krill_session_id, task_id,
  depends_on_task_ids[]}` — `task_id` is excluded from the claimable set
  until every id in `depends_on_task_ids` reaches its own `Done` lane.
  Idempotent per edge; rejects a self-edge (`ErrSelfDependency`) or a cycle
  (`ErrDependencyCycle`). Same `PersonaSwarmOperator` restriction as
  `create_task`.

@snippets/task-lifecycle-blocker.md

- `claim_task {krill_session_id, task_id}` → the task's full `work.Payload`
  document: `{slice, task: {id, milestone_id, title, body, current_lane,
  lane_sequence, dependencies, attempt_number, current_claim: {claim_id,
  session_id, claimed_at, lease_expires_at, released, release_reason?},
  notes[], state, escalation_reason?, current_escalation_id?}}`. Mints a
  lease and records one attempt.
- `heartbeat_task {krill_session_id, task_id, claim_id}` — extends the live
  lease; call periodically during a long-running phase. Rejected
  (`ErrClaimNotCurrent`) once `claim_id` is no longer the task's current,
  live claim.
- `complete_task {krill_session_id, task_id, claim_id, verdict: "pass" |
  "fail", summary?}` → full `work.Payload`. **krill, not the caller,
  decides the lane delta**: `pass` advances one lane, `fail` reverts one
  lane. There is no destination-lane field on this call.
- `abandon_task {krill_session_id, task_id, claim_id, reason?}` → full
  `work.Payload`. Releases the claim immediately with **no verdict and no
  lane change** — use when a phase can't be finished and the task should go
  back to claimable as-is; use `complete_task` whenever you have an actual
  pass/fail judgment.
- `record_note {krill_session_id, task_id | (entity_kind, entity_id), kind:
  "scope-note" | "comment", body}` → `{id}`. Exactly one of `task_id` or
  `entity_kind`+`entity_id` (one of `product`, `feature_set`, `feature`,
  `requirement`, `load_bearing_decision` — **not** `milestone`, which has
  no note target). Any Agent may call this whether or not it holds the
  task's current claim.
- `list_entity_notes {entity_kind, entity_id}` → `{entity_kind, entity_id,
  notes: [{id, entity_kind, entity_id, kind, body, status}]}`, oldest
  first, every lifecycle status (not just `noted`). Ungated, no session
  required — the read-back path for notes `record_note` attached to a
  spec-axis entity (same `entity_kind` enum). Task notes come back on
  `get_task` instead.
- `transition_note_lifecycle {krill_session_id, note_id, status: "noted" |
  "carried-over" | "deferred" | "closed"}` → `{id}`. Open to any resolved
  persona.
- `get_task {id}` → the same `work.Payload` shape every write tool above
  returns. Ungated, no session required — the way to re-read a task's
  current lane/claim/notes/attempts without re-deriving them yourself.
- `list_tasks {milestone_id}` → `{tasks: [{id, title, current_lane,
  attempt_count, has_live_claim}]}`, oldest-created first. Ungated, no
  session required — the way to discover a milestone's/milepebble's task
  ids in the first place rather than needing them handed to you. Call
  `get_task` on a returned id for its full payload.

**Lane semantics, concretely, for `worker`/`validator`:** finishing a phase
cleanly is `complete_task {verdict: "pass"}` (Scaffold→Implementation,
Implementation→Testing, Testing→Validation, Validation→Done). A failed
check at `Testing`, or a failed criterion at `Validation`, is
`complete_task {verdict: "fail"}` (reverts one lane, back to
Implementation). Being blocked with no pass/fail judgment to make is
`abandon_task` (no lane change; the task goes back to claimable).

**`list_tasks {milestone_id}` is the durable manifest of a milestone's
task set** — no persona restriction, so `implement`/`validate` can
re-derive a milestone's task ids directly from krill instead of depending
on `planner`'s hand-carried summary surviving between skill invocations.
Its `current_lane`/`attempt_count`/`has_live_claim` fields are a discovery
aid, not a substitute for the real thing — still re-read each task's live
state via `get_task {id}` before acting on it.

### Milestone required (hard stop)

`create_task` needs a milestone/milepebble id, and every `krill-work` skill
and persona that creates, finds, or validates tasks needs the same Milestone.
When none scopes the work — or the product isn't hosted in krill at all —
stop and say: "No krill Milestone scopes this work. Cut one first:
`/krill-design:product <product-id>` to add a milestone to a krill-hosted
product (or host a new one), then `/krill-design:design <product-id>
--milestone M<n>`, then re-run." Never substitute an issue, a board, or any
other tracking store.

## Git hygiene

Each task gets its own branch and worktree and, once pushed, its own small
PR based on its real dependency's branch (or `main`), merged into `main`
continuously as soon as the task is `Done` and its dependencies are already
on trunk. The PR is the code-review mechanism only; it carries no issue,
board, or label bookkeeping, and task state lives in krill.
- Only `implement`, `validate`, and `mergepush` run `git push`/`gh pr`;
  `worker`/`validator` write code only inside the worktree they're handed.
- Branch name: `pm[<attempt>]-<milestone>/<task>-<slug>` (ids are the krill
  Milestone and Task ids; slug = 3-5 word kebab-case of the task title).
  Before creating a branch, look for an existing `pm*-<milestone>/<task>-*`
  branch (local, then remote) and reuse it; mint `pm2-`, `pm3-`, ... only
  when a branch must be abandoned. Never delete and recreate the same name.
- Create with `git fetch origin main` then `git worktree add
  .claude/worktrees/<task> -b <branch> <fork-point>`, where `<fork-point>`
  is `origin/main` (never local `main`) or the single dependency's branch;
  with several dependency branches, fork from one and `git merge --no-edit`
  the rest inside the worktree, resolving conflicts before dispatching a
  worker. Set `git config rerere.enabled true` once.
- Phase commits use `scaffold:` / `feat:` / `test:` prefixes with
  `krill task: <task-id>` in the body.
- `mergepush` pushes each branch from its worktree, opens/finds its PR
  (`gh pr create --head <branch> --base <parent>`; title = task title,
  body = `krill task: <task-id>` plus 2-3 sentences of context, no closing
  keyword), then merges `Done` tasks in dependency order with
  `gh pr merge <branch> --squash` only when `mergeable` is `MERGEABLE` and
  every check has finished green. Pending/failing checks are a
  wait-for-next-batch condition, never a reason to merge anyway.
- Before system validation, build a local, never-pushed integration branch
  `pm-<milestone>-integration` from `main` by merging every task's tip.
- Closing out: dispatch `mergepush` once more with every task as done and
  verify every PR is `MERGED` with one `gh pr list --json
  number,url,headRefName` call.

## System validation and scope notes

After every task is `Done`, `system-validator` records each finding as a
`scope-note` on the FeatureSet (`record_note`); `planner` turns blocking
findings into follow-up tasks starting in `Scaffold` or `Implementation`
and closes each note with `transition_note_lifecycle`. Any persona noticing
out-of-scope work records a `scope-note` on its task (`record_note`);
`planner` classifies each `carried-over` (cross-cutting), `deferred`
(milestone-specific cut), or `closed`, and schedules real tasks for
actioned ones.

**Rate limits.** Never re-derive state the caller already resolved, batch
same-shaped per-item calls (one `gh pr list` + `jq`), and serialize anything
touching `main`. On a `gh` rate-limit error (403 with
`x-ratelimit-remaining: 0`, or the secondary-limit message), back off and
retry once before reporting failure.

## Working a task outside the swimlane loop

A krill `Task`'s lane moves only through `complete_task`, which requires a
live claim from `claim_task`. Code changed in a krill-hosted domain without
them leaves the task unclaimed and its lane stale, so any session that does a
task's work itself — not just a dispatched `worker`/`validator` — follows the
same order:

1. `init_session {}` (or reuse a still-valid id).
2. `claim_task` **before** the first edit; stop if it's refused.
3. `heartbeat_task` at least every 10 minutes (the lease is 15).
4. End with `complete_task` (pass/fail) or `abandon_task`; never let a claim
   lapse.
5. Put `krill task: <task-id>` at the end of each commit message.

`/krill-work:work-task` runs this for one task. If the work has no task yet,
create one under the right milestone (`/krill-work:plan`, or `create_task`)
before starting, rather than doing it untracked.

## Subagent dispatch: ids, not bodies

Every dispatch prompt a krill skill or persona writes for a subagent passes
**krill ids** (plus the `krill_session_id` and dispatch-shape fields like
mode, round number, worktree path) — never the body of an entity, event, or
slice the subagent can read itself. The subagent fetches what it needs:

| Id passed | Subagent reads it with |
|-----------|------------------------|
| `design_session_id` | `get_design_session`, `get_design_session_slice`, `list_open_questions` |
| `feature_set_id` / `feature_id` / `requirement_id` / `product_id` | `get_feature_set_slice` / `get_feature_slice` / `get_requirement_slice` / `get_product_slice` |
| `milestone_id` | `get_milestone`, `get_milestone_status`, `list_tasks` |
| `task_id` | `get_task` |

Why: the id is the lineage — the subagent reads the live, versioned record
(and cites the same id in its own events/commits) instead of a
dispatcher's paraphrased snapshot that may be stale or truncated — and it
keeps both the dispatcher's and the subagent's context to what's actually
needed. The same applies in reverse: a subagent reports back ids and a
short outcome, not the bodies it wrote.

Pass a body only when it has no krill home to read it from, and say so in
the dispatch: live human input not yet recorded (e.g. `review`'s
change-request text), and output a known blocker kept from being written
(e.g. `system-validator` findings while `record_note` is blocked). For
meeting rounds and rulings, which live on the DesignSession as open
questions (see "Stakeholder meeting records"), pass the design-session id
and round number, not their text.

**Dispatch prompt shape.** A persona's `agents/*.md` file is the subagent's
system prompt — fully caller-invariant, and it always precedes the dispatch
message. Never bake a specific id, path, or round number into a persona
file itself; every per-invocation value belongs in the dispatch message,
not the file. Within the dispatch message, put the ids/parameters from the
table above in one block at the end (after the role/mode instruction) —
keeps the message diffable and auditable, and mirrors the id-not-body rule
instead of interleaving ids through prose.

## Model tiers

| Persona | Model | Why |
|---|---|---|
| producer, architect, planner | `opus` | Deep reasoning for requirements gathering, architecture reconciliation, and task breakdown |
| stakeholder | `sonnet` | Bounded single-persona critique; runs once per persona in parallel, so cost multiplies |
| reviewer | `opus` | Stands in for a human judgment call (`loop-design-panel` only) |
| worker, validator | `haiku` | Fast, cost-efficient execution of scoped swimlane tasks |
| mergepush | `haiku` | Mechanical push/PR integration — no reasoning about code |
| system-validator | `opus` (effort: max) | Comprehensive whole-system validation in a running environment |
| help | `sonnet` | Bounded single-turn triage against a known decision table |

## API call volume

For the design axis, prefer one `get_design_session`/`get_design_session_slice`
call over re-deriving state from a replayed event log yourself. For the work
axis, never re-derive resolved state, batch same-shaped `gh pr` calls, and
serialize anything touching `main`.
