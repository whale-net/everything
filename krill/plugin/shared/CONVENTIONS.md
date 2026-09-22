# krill plugins — shared conventions

This is the shared contract for the `krill-design` and `krill-work` plugins, both
forked from `tools/project-manager` (see that plugin's own `CONVENTIONS.md` for the
GitHub-native pipeline this one is meant to eventually supersede — read it when a
persona here says "same as project-manager's X").

This file lives in `krill/plugin/shared/` and is symlinked into both plugins'
directories rather than copied, so it never drifts between them the way
project-manager's inlined-copy convention accepts drift as a tradeoff.

## Status of this fork

**M1-M5 are all merged. Design axis, milestone/delivery axis, and the full
work-axis task lifecycle are real and krill-native today** — see "Design
session model," "Milestone and delivery-axis tools," and "Work axis: task
lifecycle is real and krill-native on the Milestone path" below. **On a
krill-hosted Milestone, `krill-work`'s swimlane execution — claim, lane
advance/revert, notes, dependencies — never touches GitHub Issues,
Projects, or Discussions.** A `gh issue`/`gh project` call anywhere in
`worker.md`/`validator.md`/`planner.md`'s milestone-path steps means the
fork has gone stale; fix it, don't route around it. `mergepush`'s `git`/`gh`
plumbing is a different thing entirely — that's the actual code-review/merge
mechanism (branches and PRs live on GitHub because the repo does), not
spec/work tracking, and stays untouched by this rule.

**Known blocker (whale-net/everything#2930) — read this before calling
`claim_task`/`heartbeat_task`/`complete_task`/`abandon_task`/`record_note`.**
whale-net/everything#2926 ("krill-design personas can't write any entity")
was closed by #2928, which added `PersonaSwarmOperator` to the allow-list
on every design/milestone-authoring write tool. **#2928 deliberately did
not touch the work-axis task-lifecycle tools** — `claim_task`,
`heartbeat_task`, `complete_task`, `abandon_task`, `record_note` are still
`[]server.Persona{server.PersonaAgent}`-only, and `PersonaAgent` is only
resolved for a caller authenticated with a whagent-net-issued bearer token
(`krill/mcp/server/whagent_auth.go`). `krill-work`'s `worker`/`validator`
are dispatched as ordinary Claude Code subagents sharing the parent
session's mcpauth-authenticated plugin connection, which always resolves
`PersonaSwarmOperator` instead (`krill/mcp/server/auth.go`) — the persona
these five tools still forbid. Concretely, from an ordinary Claude Code
session today:
- `create_task`, `declare_task_dependencies` (`PersonaSwarmOperator`), the
  M5 ops-write tools (`release_task`, `requeue_task`, `escalate_task`,
  `cancel_task`, also `PersonaSwarmOperator`), `transition_note_lifecycle`
  (open to any persona), and — as of #2928 — every milestone/product/
  delivery-authoring tool (`create_product`, `create_feature_set`,
  `create_load_bearing_decision`, `propose_entities`, `create_milestone`,
  `set_fr_budget`, `add_delivers`, `add_must_not_foreclose`,
  `add_deferral`, `create_milepebble`, `add_milepebble_scope`,
  `add_discovered_scope`, `move_delivery_scope`,
  `mark_delivered_item_shipped`, `abandon_milestone`, `set_milestone_status`
  — all now `{PersonaRequirementContributor, PersonaAgent,
  PersonaSwarmOperator}`) **work today.**
- `claim_task`, `heartbeat_task`, `complete_task`, `abandon_task`,
  `record_note` (all still `PersonaAgent`-only) **do not** — every call
  fails with `forbidden`.

**When `worker`/`validator` hits this: call the tool, let it fail, report
the exact `forbidden` error, and stop.** Do not silently fall back to
GitHub to route around it, and do not skip the call and pretend it
succeeded — see whale-net/everything#2925 for the precedent this follows.
Closing #2930 needs the same kind of deliberate decision #2928 made for the
design axis (widen these five tools' allow-list, or bridge `worker`/
`validator` dispatch through an actual whagent-net session) — out of scope
for any single skill file to make.

**Two more real, currently-open krill capability gaps, not plugin
oversights — both are documented inline below, not silently worked
around:**
1. **No Task container exists outside a Milestone/Milepebble** (`create_task`
   requires `milestone_id`, NFR7 rejects a bare FeatureSet/Requirement id),
   and most domains' `PRODUCT.md` still isn't krill-hosted (today: krill's
   own domain, and `whagent_net` via import). For that case only,
   `krill-work` still falls back to `tools/project-manager`'s GitHub
   Issues/Project mechanics verbatim — every persona that takes this path
   must say so explicitly.
2. **No MCP tool lets a `PersonaAgent` discover claimable tasks by lane** —
   `claim_task` needs a `task_id` already in hand, and the task-listing read
   tools that exist (`list_claimed_tasks`, `list_cancelled_tasks`,
   `list_escalated_tasks`) are `PersonaSwarmOperator`-only on `/mcp/ops` and
   don't cover "unclaimed, ready" tasks anyway. `planner`'s summary is the
   only durable manifest of a milestone's task set — see "Work axis" below.

**Everything work-axis-write-capable — milestone authoring, delivery
status/shipment/recut/abandon, `create_task`, and the full task lifecycle
(`claim_task`/`heartbeat_task`/`complete_task`/`abandon_task`/`record_note`/
`transition_note_lifecycle`/`declare_task_dependencies`) — mounts on the
same `/mcp/design` server the design-session tools use (`krill/mcp/main.go`'s
`designReg`), not a separate `/mcp/work` mount.** `krill-work`'s
`mcp_config.json`/`.mcp.json` already register the `krill-mcp-design-
{tilt,dev,prod}` servers this needs — no config change required to use any
tool in this file. M5 additionally stood up a **Swarm-Operator-only**
`/mcp/ops` console mount (`list_claimed_tasks`, `list_cancelled_tasks`,
`list_escalated_tasks`, `list_open_notes`, `release_task`, `requeue_task`,
`escalate_task`, `cancel_task`) for a human debugging stuck work — none of
`krill-work`'s personas need it for normal execution, and none register it.

## Session bootstrapping

Every write tool on `/mcp/design` requires a `krill_session_id` as input
(NFR6's gate is write-only — every read tool, `get_design_session_slice`
etc., is ungated and needs no session at all). Mint one first with
`init_session {acting, on_behalf_of, whagent_session_id?}` →
`{session_id}` — no persona restriction, any resolved caller may call it.

`acting`/`on_behalf_of` are each a `{iss, sub, kind}` triple
(`api/handlers/session.go`'s `ParseSubject`, reused verbatim by
`init_session` per LB7):

- `kind` is a closed enum, exactly `"human"` or `"service"` — there is no
  third value and no `"agent"` spelling (`store.SubjectKind`,
  `003_session.up.sql`'s CHECK constraint). Use `"human"` for an ordinary
  interactive Claude Code session; the `loop-design-panel` skill's
  `reviewer`/other unattended personas use `"service"` for the same call
  shape.
- `iss`/`sub` are only required to be non-empty free-text — `ParseSubject`
  checks presence, not identity against any real issuer registry today.
  For an ordinary interactive session acting on its own behalf (the common
  case — `acting == on_behalf_of`, never inferred), a stable per-caller
  string works: e.g. `iss: "whalenet-cli"`, `sub: "<caller's email>"`.
  `acting` differs from `on_behalf_of` only for a genuinely mediated call
  (a Requirement Contributor's plain-language ask relayed by an Agent via
  `propose_entities`, FR9/FR10) — never set them to the same value there,
  or the mediated write is rejected (`ErrMediatedIdentitySame`).

`krill_session_id` values are not durable across a plugin-connection reset
(a `/reload-plugins`, a dropped MCP auth session) — mint a fresh one rather
than reusing an id from a prior connection.

### Two MCP mounts, one backend

`krill-design` and `krill-work` each declare their own
`krill-mcp-design-{tilt,dev,prod}` (and `krill-mcp-{tilt,dev,prod}`)
server entries in their own `mcp_config.json`/`.mcp.json` — both point at
the same krill deployment, but each plugin's connection (and any OAuth
login it requires) is independent of the other's. Authenticating
`krill-design`'s connection does **not** authenticate `krill-work`'s, and
vice versa: after a `/reload-plugins` or any other event that drops a
plugin's MCP connections, expect to need `authenticate`/
`complete_authentication` again on whichever plugin's tools you call next,
even if the other plugin's identically-named tools already worked in the
same session. A `krill_session_id` minted through one plugin's `init_session`
call is a krill-server-side value and works identically when passed to the
other plugin's tools (same backend) — only the MCP-level connection/auth is
per-plugin, not the krill session itself.

## Design session model (krill-native, real today)

krill has no Discussion/gist/comment-thread concept. A design conversation is a
**DesignSession**: a container of an `opening_submission` (free text, no entity
refs) plus an append-only log of **RevisionEvents**. There is no "working draft"
document to re-read — the log *is* the draft, and `get_design_session_slice`
gives you the typed slice (FeatureSet/Feature/Requirement/Decision) of every
entity any event in the session has touched.

- **Open a session**: `open_design_session {krill_session_id, product_id,
  opening_submission}` → `{id}`. One session per design conversation (the
  equivalent of project-manager's intake Discussion).
- **Record a step**: `append_revision_event {krill_session_id,
  design_session_id, event_type, verified_against?, entity_deltas[],
  open_questions_delta?, signoff_status?}` → `{id, seq_no}`.
  - `event_type` is a closed enum: `draft | reconciliation | answer | signoff |
    ruling`. This *is* the phase model — there is no separate state machine to
    track on top of it.
    - `draft` — producer drafting/revising (project-manager's producer Mode 1/2).
    - `reconciliation` — architect reconciling (project-manager's architect
      review comment).
    - `answer` — producer answering open questions raised in a reconciliation.
    - `signoff` — a human or the `reviewer` persona approving/requesting
      changes. Requires `signoff_status` (`approved | changes_requested`);
      forbidden on any other event type.
    - `ruling` — the `reviewer` persona breaking a stakeholder disagreement
      (project-manager's "Design panel ruling" comment).
  - `verified_against` (a string like `"main@<sha>"`) is **required** for
    `draft`/`reconciliation` and **forbidden** otherwise — it ties the event to
    the repo state it was actually reasoned against.
  - `entity_deltas: [{entity_id, change: created|updated, summary_line}]` names
    what the event actually touched. Don't leave this empty for an event that
    changed entities — an empty delta list on a `draft` event means "no entity
    changed," which is rarely true for a real drafting step.
  - `open_questions_delta: {opened: [{question_id, blocking, text}],
    resolved: [question_id, ...]}` is how open questions are raised and closed.
    There is no separate "open question" table — `list_open_questions` derives
    the open set by replaying `opened`/`resolved` deltas in `seq_no` order
    (last mention wins). Give every question a stable `question_id` you'll
    reuse verbatim when resolving it later.
- **Propose new entities**: `propose_entities {krill_session_id,
  design_session_id, verified_against, proposals: [{kind: feature|requirement,
  parent_id | parent_proposal_index, name, body?,
  requirement_kind?, summary_line}]}` → `{revision_event_id, seq_no, entities:
  [{kind, id}]}`. (A `position` field is still accepted on the wire for
  backward compatibility but silently ignored — krill assigns each
  proposal's sibling position server-side now, per FR7; don't bother
  setting it.) This is **mediated intake** (FR9/FR10) and is the *only* way
  new Features/Requirements get created — there is no separate "create entity"
  tool. Allow-listed to `PersonaAgent` and `PersonaSwarmOperator` (issue
  #2926 widened this from `PersonaAgent`-only, which made the tool
  unreachable from any mcpauth-authenticated caller); FR9/FR10's actual
  mediation guarantee is enforced independently, by requiring the resolved
  `krill_session`'s `Acting` and `OnBehalfOf` identities to be distinct
  (`ErrMediatedIdentitySame` otherwise) regardless of which persona is
  calling — i.e. proposing on behalf of a specific human, never on behalf
  of oneself. `parent_proposal_index` (0-based, into
  this same call's `proposals[]`) lets one call create a Feature and its
  Requirements together without a round-trip.
- **Read back**: `get_design_session {id}` (session + full revision_events
  log), `get_design_session_slice {id}` (typed entity slice, union of
  everything the log touched), `list_open_questions {id, blocking?}`.
- **There is no separate "root plan" artifact.** Once a `signoff` event with
  `signoff_status: approved` lands, the Feature/Requirement entities *are* the
  approved plan — query them with `get_feature_set_slice`/`get_feature_slice`,
  don't look for a GitHub Issue. This replaces project-manager's
  `plan:approved` root-Issue-with-a-label convention entirely for the design
  axis.

Read-only slice queries (`get_feature_set_slice`, `get_feature_slice`,
`get_requirement_slice`, `get_product_slice`, all `{id}` → `slice.Document`) are
never gated by a `krill_session` and available to every persona.

## Milestone and delivery-axis tools (M3, real today)

krill's `Milestone`/`Milepebble` are real entities now, with every write
tool below allow-listed to `{PersonaRequirementContributor, PersonaAgent,
PersonaSwarmOperator}` — `PersonaSwarmOperator` was added per issue #2926
(closed by #2928), since an allow-list naming only the other two made every
one of these tools unreachable end-to-end from any mcpauth-authenticated
caller (including every `krill-design`/`krill-work` subagent, which share
the parent session's mcpauth connection and can never resolve
`PersonaAgent` themselves). `PersonaRequirementContributor` still has no
auth path that ever resolves to it (reserved for a later capability,
`krill/mcp/server/auth.go`). Unlike `propose_entities` (mediated, requiring
a distinct acting/on-behalf-of pair, but likewise widened to
`PersonaSwarmOperator` by #2928), these tools need no mediated session at
all — an ordinary Claude Code session's mcpauth-resolved
`PersonaSwarmOperator` identity calling on its own behalf works today, same
as `get_milestone`/`list_milepebbles`/`list_product_delivery`/
`get_backlog` (read-only, never gated). This is the layer that replaces project-manager's markdown roadmap milestones
(`### M2 — <outcome> / Delivers: ... / Must not foreclose: ... /
Deliberately deferred: ...`) for
any product actually hosted in krill (today: krill's own self-hosted domain,
and whagent_net via import — **not** every domain's `PRODUCT.md`, which
still isn't imported and stays plain markdown until it is):

- `create_milestone {krill_session_id, product_id, name, outcome,
  fr_budget?}` → `{id}`. `name` is the bare identifier (`"M3"`); `outcome`
  is the one-sentence outcome, same discipline as the markdown roadmap's
  heading.
- `set_fr_budget {krill_session_id, milestone_id, fr_budget}` — revises the
  budget; only the latest value reads back.
- `add_delivers {krill_session_id, milestone_id, entity_id}` — associates a
  Feature or Requirement into the milestone's `Delivers` set. An
  `entity_milestone` row, never a column on the entity (LB6) — call once
  per entity, idempotent.
- `add_must_not_foreclose {krill_session_id, milestone_id, entity_id}` —
  same association mechanism, for the `Must not foreclose` list architect's
  Load-bearing check reads.
- `add_deferral {krill_session_id, milestone_id, body, destination}` —
  records one deliberately-deferred item; `destination` is required (no
  bare "deferred" with nowhere named).
- `get_milestone {id}` → authoring fields plus `Delivers`/`Must not
  foreclose`/deferrals, read-only, no session gate.
- `create_milepebble {krill_session_id, milestone_id, name, outcome}` → cuts
  a sub-milestone container (FR3); `add_milepebble_scope` associates an
  entity already in the parent milestone's own `Delivers` set (rejected
  otherwise — the subset invariant); `add_discovered_scope` creates a new
  Feature/Requirement *and* associates it to a milepebble (and the parent
  milestone's `Delivers` set) in one call, for scope discovered mid-milestone
  without re-drafting the whole thing; `list_milepebbles {milestone_id}` →
  every milepebble in position order.
- `set_milestone_status {krill_session_id, milestone_id, status, note?}` —
  `status` is one of the fixed seven: `not started, in design, planned, in
  progress, shipped, partially complete, abandoned`. This **replaces**
  project-manager's `Ledger: M<n> → <status>` tracking-issue-comment
  convention for a krill-hosted milestone — `get_milestone_status`
  (current) and `get_milestone_status_history` (every transition,
  chronological, with actor) are real queries, not something you reconstruct
  by scanning comments.
- `mark_delivered_item_shipped {krill_session_id, milestone_id, entity_id,
  note?}` and `get_delivery_breakdown {milestone_id}` (→ shipped/unshipped
  entity sets) — per-item shipment tracking within a milestone/milepebble.
- `move_delivery_scope {krill_session_id, entity_ids[], from, to}` and
  `get_backlog {product_id}` — re-cuts not-yet-shipped scope between
  milestones/milepebbles/the backlog bucket; refuses (writes nothing) if
  any entity is already shipped in its `from` container.
- `abandon_milestone {krill_session_id, milestone_id, note?}` — marks a
  milestone or milepebble abandoned and sweeps its not-yet-shipped scope
  into the backlog in one transaction. **Not reversible.**
- `list_product_delivery {product_id, statuses?}` — every milestone/
  milepebble under a product, optionally filtered by status. **This is the
  live, per-milestone-filtered read krill's own domain needed and didn't
  have in M1** (see `krill-design/skills/design/SKILL.md`'s "krill's own
  milestone read" — the old superset caveat there is resolved by this tool
  plus `get_milestone`, not still open).

## Work axis: task lifecycle is real and krill-native on the Milestone path (M4/M5)

M4/M5 shipped the full verb set: `create_task`, `declare_task_dependencies`,
`claim_task`, `heartbeat_task`, `complete_task`, `abandon_task`,
`record_note`, `transition_note_lifecycle`, plus `get_task` to re-read
current state. **A krill `Task`'s `current_lane` is never stale** — every
verb below mutates it directly; there is no second, GitHub-side source of
truth to keep in sync anymore on the Milestone path.

- `create_task {krill_session_id, milestone_id, title, body?,
  lane_sequence[], starting_lane}` → `{id}`. `lane_sequence` is an ordered
  subset of `{Scaffold, Implementation, Testing, Validation, Done}` (lanes
  skippable); `starting_lane` must be a member. `milestone_id` must be a
  milepebble, or a milestone with no milepebbles cut from it yet (NFR7 —
  never a bare Feature/Requirement id). **Restricted to
  `PersonaSwarmOperator`** — works when `krill-work:planner` runs inside an
  ordinary interactive Claude Code session (the signed-in human's mcpauth
  identity resolves `PersonaSwarmOperator`), which is the normal way this
  plugin gets used. Fails with "forbidden" under a fully unattended
  whagent-net-authenticated dispatch with no human present — there is still
  no interim workaround for that one case; fall back to a GitHub-only task
  record and note the gap plainly rather than retrying silently.
- `declare_task_dependencies {krill_session_id, task_id,
  depends_on_task_ids[]}` — `task_id` is excluded from the claimable set
  until every id in `depends_on_task_ids` reaches its own `Done` lane.
  Idempotent per edge; rejects a self-edge (`ErrSelfDependency`) or a cycle
  (`ErrDependencyCycle`). Same `PersonaSwarmOperator` restriction as
  `create_task`. **This is what `Depends on:` meant on the GitHub path —
  a real edge now, not an issue-body convention.**

**Blocked from an ordinary Claude Code session today (whale-net/everything#2930,
`PersonaAgent`-only — #2928 widened the design/milestone-authoring tools
above but deliberately left these five untouched): `claim_task`,
`heartbeat_task`, `complete_task`, `abandon_task`, `record_note`.** Listed
below for completeness and because
`worker`/`validator` must still call them and report the `forbidden` error
per this file's top-of-document blocker callout — not because they're
usable today.

- `claim_task {krill_session_id, task_id}` → the task's full `work.Payload`
  document: `{slice, task: {id, milestone_id, title, body, current_lane,
  lane_sequence, dependencies, attempt_number, current_claim: {claim_id,
  session_id, claimed_at, lease_expires_at, released, release_reason?},
  notes[], state, escalation_reason?, current_escalation_id?}}`. Mints a
  lease and records one attempt. `PersonaAgent`.
- `heartbeat_task {krill_session_id, task_id, claim_id}` — extends the live
  lease; call periodically during a long-running phase. Rejected
  (`ErrClaimNotCurrent`) once `claim_id` is no longer the task's current,
  live claim — a slow or stalled run must not keep writing to a task
  another run now owns. `PersonaAgent`.
- `complete_task {krill_session_id, task_id, claim_id, verdict: "pass" |
  "fail", summary?}` → full `work.Payload`. **krill, not the caller,
  decides the lane delta**: `pass` advances one lane in the task's own
  `lane_sequence`, `fail` reverts one lane. There is no destination-lane
  field on this call — never try to name one. `PersonaAgent`.
- `abandon_task {krill_session_id, task_id, claim_id, reason?}` → full
  `work.Payload`. Releases the claim immediately with **no verdict and no
  lane change** — use this when a phase can't be finished and the task
  should go back to claimable as-is; use `complete_task` instead whenever
  you have an actual pass/fail judgment to report. Counts as an attempt
  against the same cap a lease-expiry lapse counts against. `PersonaAgent`.
- `record_note {krill_session_id, task_id | (entity_kind, entity_id), kind:
  "scope-note" | "comment", body}` → `{id}`. Exactly one of `task_id` or
  `entity_kind`+`entity_id` (one of `product`, `feature_set`, `feature`,
  `requirement`, `load_bearing_decision` — **not** `milestone`, which has
  no note target). `kind: "scope-note"` is what replaces GitHub's
  `source:scope-note` label convention. Any Agent may call this whether or
  not it holds the task's current claim. `PersonaAgent`.
- `transition_note_lifecycle {krill_session_id, note_id, status: "noted" |
  "carried-over" | "deferred" | "closed"}` → `{id}`. This is how
  `planner`'s scope-note triage resolves a note now, in place of closing a
  GitHub issue at a label. Open to any resolved persona.
- `get_task {id}` → the same `work.Payload` shape every write tool above
  returns. Ungated, no session required — the way to re-read a task's
  current lane/claim/notes/attempts without re-deriving them yourself.

**Lane semantics, concretely, for `worker`/`validator`:** finishing a phase
cleanly is `complete_task {verdict: "pass"}` (Scaffold→Implementation,
Implementation→Testing, Testing→Validation, Validation→Done). A failed
check at `Testing`, or a failed criterion at `Validation`, is
`complete_task {verdict: "fail"}` (reverts one lane, back to
Implementation). Being blocked with no pass/fail judgment to make is
`abandon_task` (no lane change; the task goes back to claimable).

**No task-discovery query exists for `PersonaAgent`.** `planner`'s
summary — every task id, title, and starting lane `create_task` returned —
is the only durable manifest of a milestone's task set; there is no way to
re-derive it later from krill alone. `implement`/`validate` must be handed
that manifest explicitly by whoever dispatches them (the same way a human
today carries a plan identifier between skill invocations), and should
re-read each task's live state via `get_task {id}` rather than trust a
stale copy of it.

For the one remaining GitHub-only case — a FeatureSet with no krill
Milestone to scope `create_task` to — `krill-work` still runs entirely on
GitHub Issues/a Project's `Status` field, exactly like `project-manager`'s
does. See `tools/project-manager/CONVENTIONS.md` §§ "Project setup", "Task
issues & swimlane progression", "Worker lifecycle", "Git hygiene" for those
mechanics in full; this fork does not repeat them, and every persona that
falls back to them must say so in its output.

## Model tiers

Same assignment as `project-manager` (see its CONVENTIONS.md § "Model tiers")
— unchanged by this fork since it's a cost/quality tradeoff, not a
GitHub-vs-krill one.

## API call volume

For the design axis, prefer one `get_design_session`/`get_design_session_slice`
call over re-deriving state from a replayed event log yourself. For the work
axis, project-manager's § "API call volume & rate limits" principles
(never re-derive resolved state, batch same-shaped `gh` calls, serialize
anything touching `main`) still apply unchanged.
