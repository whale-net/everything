# krill plugins — shared conventions

This is the shared contract for the `krill-design` and `krill-work` plugins, both
forked from `tools/project-manager` (see that plugin's own `CONVENTIONS.md` for the
GitHub-native pipeline this one is meant to eventually supersede — read it when a
persona here says "same as project-manager's X").

This file lives in `krill/plugin/shared/` and is symlinked into both plugins'
directories rather than copied, so it never drifts between them the way
project-manager's inlined-copy convention accepts drift as a tradeoff.

## Status of this fork

**First iteration, updated as krill's M3/M4 land.** The design axis (this
file's "Design session model" section) is real and callable today against
krill's `/mcp/spec` and `/mcp/design` MCP surfaces (M1/M2, merged). M3
(milestone/milepebble authoring and delivery status) is now **also fully
merged**, and M4's first FR (`create_task`) landed right behind it — see
"Milestone and delivery-axis tools (M3, real today)" and "Work axis:
task creation is real, execution still bridges on GitHub (M4 FR1 only)"
below for exactly what that does and does not cover. `krill-work`'s
persona/skill files carry `TODO(M4)` markers at the points still gapped —
treat those as real gaps, not optional polish, and re-check this file
against `krill/README.md`'s own tool tables before trusting an older
`TODO` marker in a persona file over what's actually shipped.

**Everything write-capable in this section — milestone authoring, delivery
status/shipment/recut/abandon, and `create_task` — mounts on the same
`/mcp/design` server the design-session tools use (`krill/mcp/main.go`'s
`designReg`), not a separate `/mcp/work` mount.** An earlier draft of this
fork assumed krill would stand up a distinct `/mcp/work` endpoint mirroring
whagent_net's `readonly`/`ops` split; it didn't — krill kept one write
mount. `krill-work`'s `mcp_config.json`/`.mcp.json` therefore register the
same `krill-mcp-design-{tilt,dev,prod}` servers `krill-design` does, not a
work-specific set.

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
  tool. **Restricted to `PersonaAgent`** (never callable as
  `PersonaSwarmOperator`/human) and requires the resolved `krill_session`'s
  `Acting` and `OnBehalfOf` identities to be distinct (`ErrMediatedIdentitySame`
  otherwise) — i.e. an Agent proposing on behalf of a specific human, never an
  Agent proposing "on behalf of itself." `parent_proposal_index` (0-based, into
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
tool below allow-listed to `{PersonaRequirementContributor, PersonaAgent}`
— in practice today that means `PersonaAgent` only, since
`PersonaRequirementContributor` has no auth path that ever resolves to it
yet (reserved for a later capability, `krill/mcp/server/auth.go`). Unlike
`propose_entities` (Agent-only, mediated, requiring a distinct
acting/on-behalf-of pair), these tools need no mediated session — an
ordinary Agent-authenticated session calling on its own behalf is fine. This
is the layer that replaces project-manager's markdown roadmap milestones
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

## Work axis: task creation is real, execution still bridges on GitHub (M4 FR1 only)

M4's full verb set is `init`, `claim`, `heartbeat`, `complete`, `abandon`,
`note` (six verbs total, deliberately capped — LB7/"payload-enrichment-is-
not-a-verb"). **Only the first slice of this has shipped: `create_task`.**
Nothing else in that list exists yet — no `claim`, no `heartbeat`, no
`complete`/`abandon`-for-a-task, no `note`, and critically **no
dependency-declaration tool** (the `task_dependency` table exists in
migration 015's schema, but no store/API/MCP method writes to it yet) and
**no lane/status query tool** for a worker or validator to discover ready
work by lane. Until those ship, `krill-work`'s swimlane execution — finding
ready work, claiming it, advancing/rolling back its lane, dependency
gating — still runs entirely on GitHub Issues/a Project's `Status` field,
exactly like `project-manager`'s does. See
`tools/project-manager/CONVENTIONS.md` §§ "Project setup", "Task issues &
swimlane progression", "Worker lifecycle", "Git hygiene" for those mechanics
in full; this fork does not repeat them.

**`create_task {krill_session_id, milestone_id, title, body?,
lane_sequence[], starting_lane}` → `{id}` is real, and its five-value lane
vocabulary (`Scaffold, Implementation, Testing, Validation, Done`) is
exactly project-manager's swimlane list — not a coincidence, this is the
shape M4 is converging toward.** `milestone_id` must be a milepebble, or a
milestone with no milepebbles cut from it yet (NFR7 — never a bare
Feature/Requirement id). **Restricted to `PersonaSwarmOperator`, not
`PersonaAgent`** — per the tool's own doc comment, "the Swarm Operator, not
the Agent, creates tasks... the Agent's role starts at claim (a later M4
task)." Concretely: `krill-work:planner` calling `create_task` **works**
when dispatched inside an ordinary interactive Claude Code session (the
session's MCP connection authenticates as the signed-in human via auth,
resolving `PersonaSwarmOperator`) — which is the normal way this plugin
gets used today. It **fails** if `planner` is ever dispatched through a
fully unattended whagent-net-authenticated pipeline with no human present,
since that resolves `PersonaAgent`; there is no interim workaround for that
case besides falling back to a GitHub-only task record and noting the gap.

Because `current_lane` has no MCP mutation yet (only set at `create_task`
time), a krill `Task`'s `current_lane` goes stale the moment a worker
actually advances the task past its `starting_lane` on the GitHub Project
board — **the GitHub Project's `Status` field remains the authoritative
swimlane state** until `claim`/`heartbeat`/`complete`/`abandon` land. Treat
the krill `Task` row `krill-work:planner` creates as the entity of record
for *what the task is*, and the GitHub issue/Project item as the entity of
record for *what lane it's currently in* — two records for one task is an
explicitly temporary seam, not a design to preserve past M4.

`krill-work` persona files mark their own `TODO(M4)` points inline where a
`gh issue`/Project `Status` transition should become a `claim`/`heartbeat`/
`complete`/`abandon`/`note` MCP call once those verbs ship.

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
