# krill plugins — shared conventions

Shared contract for the `krill-design` and `krill-work` plugins. Fallback for
mechanics not covered in a persona file itself. When a persona says "same as
project-manager's X," see `tools/project-manager/CONVENTIONS.md`.

This file is symlinked into both plugins' directories — edit only here.

## Known limitations

**A `gh issue`/`gh project` call anywhere in a Milestone-path step of
`worker.md`/`validator.md`/`planner.md` is a bug — fix it, don't route
around it.** On a krill-hosted Milestone, swimlane execution (claim, lane
advance/revert, notes, dependencies) is entirely krill-native.
`mergepush`'s `git`/`gh` calls are exempt — that's the actual merge
mechanism, not spec/work tracking.

@snippets/task-lifecycle-blocker.md

Tools that work today from an ordinary session (resolve
`PersonaSwarmOperator`): `create_task`, `declare_task_dependencies`, the M5
ops-write tools (`release_task`, `requeue_task`, `escalate_task`,
`cancel_task`), `transition_note_lifecycle`, and every milestone/product/
delivery-authoring tool (`create_product`, `create_feature_set`,
`create_load_bearing_decision`, `create_persona`, `create_non_goal`,
`amend_requirement`, `amend_load_bearing_decision`, `propose_entities`,
`create_milestone`, `set_fr_budget`, `add_delivers`,
`add_must_not_foreclose`, `add_deferral`, `create_milepebble`,
`add_milepebble_scope`, `add_discovered_scope`, `move_delivery_scope`,
`mark_delivered_item_shipped`, `abandon_milestone`, `set_milestone_status`).

**Two open capability gaps, not plugin oversights:**
1. **No Task container exists outside a Milestone/Milepebble** —
   `create_task` requires `milestone_id`; a bare FeatureSet/Requirement id
   is rejected (NFR7), and most domains' `PRODUCT.md` still isn't
   krill-hosted. For that case, `krill-work` falls back to
   `tools/project-manager`'s GitHub Issues/Project mechanics verbatim —
   say so explicitly when you take this path.
2. **`list_tasks` covers milestone-wide task discovery, but not
   claimability.** `list_tasks {milestone_id}` (ungated, any persona,
   `/mcp/design`) returns every task under a milestone/milepebble — id,
   title, `current_lane`, attempt count, and whether a claim is currently
   live — so `planner`'s hand-carried summary is no longer the only way to
   find a milestone's task ids (see "Work axis" below). It does **not**
   filter by claimable: unresolved dependencies, the attempt cap, and an
   active escalation are invisible to it — `claim_task` still needs a
   `task_id` already in hand, and a caller still cross-checks `get_task
   {id}` per candidate before calling it.

Every work-axis-write tool (milestone authoring, delivery status/shipment/
recut/abandon, `create_task`, and the full task lifecycle) mounts on the
same `/mcp/design` server the design-session tools use — no separate
`/mcp/work` mount, no config change required. A **Swarm-Operator-only**
`/mcp/ops` mount (`list_claimed_tasks`, `list_cancelled_tasks`,
`list_escalated_tasks`, `list_open_notes`, `release_task`, `requeue_task`,
`escalate_task`, `cancel_task`) exists for human debugging only — no
persona here needs or registers it.

## Session bootstrapping

Every write tool on `/mcp/design` requires a `krill_session_id` (every read
tool is ungated). Mint one first: `init_session {acting, on_behalf_of,
whagent_session_id?}` → `{session_id, scope_id}` — no persona restriction.
`scope_id` is the scope the session was minted under — the value
`list_products`, `list_tasks`, and the ops console tools (`list_claimed_tasks`
etc.) take as input (`POST /sessions/init` returns the same shape).

`acting`/`on_behalf_of` are each a `{iss, sub, kind}` triple:

- `kind` is exactly `"human"` or `"service"` — no third value, no `"agent"`
  spelling. Use `"human"` for an ordinary interactive session; unattended
  personas (e.g. `loop-design-panel`'s `reviewer`) use `"service"`.
- `iss`/`sub` only need to be non-empty free text (e.g. `iss:
  "whalenet-cli"`, `sub: "<caller's email>"`). For the common case, `acting
  == on_behalf_of` — set them to the same value. Set them differently only
  for a genuinely mediated call (a Requirement Contributor's ask relayed by
  an Agent via `propose_entities`) — never the same value there, or the
  write is rejected (`ErrMediatedIdentitySame`).

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

### record_note fallback for anchor-less designs

A design conversation normally gets a durable link comment posted somewhere
GitHub-native — a Discussion link on the product tracking issue's `Ledger:`
comment. That anchor doesn't exist for a krill-hosted product/milestone
(one whose status is tracked purely via
`set_milestone_status`/`get_milestone_status`, never a `Ledger:`
tracking-issue comment — see "Milestone and delivery-axis tools" above).
Any skill that needs to leave a durable pointer against a design in that
situation (a stakeholder meeting round's link, an amendment note, or
similar) uses this standardized fallback instead of improvising one per
run:

- Call `record_note {entity_kind: "feature_set", entity_id: <anchor>,
  kind: "comment", body: <the same fixed-format string the GitHub path
  would have posted, e.g. "Stakeholder meeting round <N>: <url>">}`.
- `<anchor>` is the FeatureSet the design's Requirements/Features roll up
  under: read it from `get_design_session_slice`'s FeatureSet entries, or
  — if the session's events never touched the FeatureSet itself, only
  Features/Requirements under a pre-existing one — resolve it via that
  Feature's/Requirement's `feature_set_id`.
- `design_session` is not itself a valid `record_note` `entity_kind` (the
  fixed enumeration is `product, feature_set, feature, requirement,
  load_bearing_decision`), which is why the FeatureSet, not the session, is
  the target.
- Keep the body string identical in shape to whatever the GitHub-anchored
  path would have posted, so both paths stay grep-discoverable the same
  way — this is a location fallback, not a different format.

`record_note` hits the same known blocker described under "Work axis"
below — make the call anyway; if it fails, report the exact error and the
link/body text you tried to record so it isn't lost, and do not fall back
to opening a GitHub Discussion/issue to route around it.

See `krill-design:stakeholder-meeting`'s step 4 for the concrete
application.

## Milestone and delivery-axis tools

Every write tool below is allow-listed to `{PersonaRequirementContributor,
PersonaAgent, PersonaSwarmOperator}` — an ordinary session's
`PersonaSwarmOperator` identity works today, calling on its own behalf, same
as the read-only tools below (`get_milestone`/`list_milepebbles`/
`list_product_delivery`/`get_backlog`, never gated).

- `create_milestone {krill_session_id, product_id, name, outcome,
  fr_budget?}` → `{id}`. `name` is the bare identifier (`"M3"`); `outcome`
  is a one-sentence outcome.
- `set_fr_budget {krill_session_id, milestone_id, fr_budget}` — revises the
  budget; only the latest value reads back.
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
- `create_milepebble {krill_session_id, milestone_id, name, outcome}` → cuts
  a sub-milestone container; `add_milepebble_scope` associates an entity
  already in the parent milestone's own `Delivers` set (rejected
  otherwise); `add_discovered_scope` creates a new Feature/Requirement
  *and* associates it to a milepebble (and the parent milestone's
  `Delivers` set) in one call, for scope discovered mid-milestone;
  `list_milepebbles {milestone_id}` → every milepebble in position order.
- `set_milestone_status {krill_session_id, milestone_id, status, note?}` —
  `status` is one of the fixed seven: `not started, in design, planned, in
  progress, shipped, partially complete, abandoned`. `get_milestone_status`
  (current) and `get_milestone_status_history` (every transition,
  chronological, with actor) are real queries.
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
  milepebble under a product, optionally filtered by status.

## Work axis: task lifecycle

The full verb set: `create_task`, `declare_task_dependencies`,
`claim_task`, `heartbeat_task`, `complete_task`, `abandon_task`,
`record_note`, `transition_note_lifecycle`, plus `get_task` to re-read
current state. **A krill `Task`'s `current_lane` is never stale** — every
verb below mutates it directly; there is no second, GitHub-side source of
truth on the Milestone path.

- `create_task {krill_session_id, milestone_id, title, body?,
  lane_sequence[], starting_lane}` → `{id}`. `lane_sequence` is an ordered
  subset of `{Scaffold, Implementation, Testing, Validation, Done}` (lanes
  skippable); `starting_lane` must be a member. `milestone_id` must be a
  milepebble, or a milestone with no milepebbles cut from it yet (NFR7 —
  never a bare Feature/Requirement id). **Restricted to
  `PersonaSwarmOperator`** — works from an ordinary interactive session
  (the normal way this plugin is used). Fails "forbidden" under a fully
  unattended whagent-net-authenticated dispatch with no human present —
  stop and say so plainly rather than falling back silently.
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

For a FeatureSet with no krill Milestone to scope `create_task` to,
`krill-work` runs entirely on GitHub Issues/a Project's `Status` field, like
`project-manager` does. See `tools/project-manager/CONVENTIONS.md` §§
"Project setup", "Task issues & swimlane progression", "Worker lifecycle",
"Git hygiene" for those mechanics; every persona that falls back to them
must say so in its output.

## Model tiers

Same assignment as `project-manager` — see its CONVENTIONS.md § "Model
tiers".

## API call volume

For the design axis, prefer one `get_design_session`/`get_design_session_slice`
call over re-deriving state from a replayed event log yourself. For the work
axis, project-manager's § "API call volume & rate limits" principles
(never re-derive resolved state, batch same-shaped `gh` calls, serialize
anything touching `main`) apply unchanged.
