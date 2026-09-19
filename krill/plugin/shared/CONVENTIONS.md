# krill plugins — shared conventions

This is the shared contract for the `krill-design` and `krill-work` plugins, both
forked from `tools/project-manager` (see that plugin's own `CONVENTIONS.md` for the
GitHub-native pipeline this one is meant to eventually supersede — read it when a
persona here says "same as project-manager's X").

This file lives in `krill/plugin/shared/` and is symlinked into both plugins'
directories rather than copied, so it never drifts between them the way
project-manager's inlined-copy convention accepts drift as a tradeoff.

## Status of this fork

**First iteration.** The design axis (this file's "Design session model" section)
is real and callable today against krill's `/mcp/spec` and `/mcp/design` MCP
surfaces (M1/M2, merged). The work axis (this file's "Work axis: bridging on
GitHub" section) has **no krill-native backend yet** — krill's M3 (milestone
authoring) and M4 (claim/heartbeat/complete/abandon/note work-tracking) are
unbuilt as of this fork. Every work-axis persona/skill in `krill-work` still
drives GitHub Issues/Projects exactly as `project-manager`'s does, with
`TODO(M3)`/`TODO(M4)` markers at each point that should become a krill MCP call
once those milestones ship. Do not treat a `TODO(M3)`/`TODO(M4)` marker as
optional polish — it marks a real gap, not a nice-to-have.

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
  parent_id | parent_proposal_index, name, body?, position,
  requirement_kind?, summary_line}]}` → `{revision_event_id, seq_no, entities:
  [{kind, id}]}`. This is **mediated intake** (FR9/FR10) and is the *only* way
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

## Work axis: bridging on GitHub (TODO(M3)/TODO(M4))

krill's roadmap reserves six work-tracking verbs — `init`, `claim`,
`heartbeat`, `complete`, `abandon`, `note` — for M4, and per-milestone
authoring/delivery-status query for M3. **Neither exists yet**: no `/mcp/work`
mount, no task/lease/attempt tables. Until they ship, `krill-work`'s
personas/skills keep operating exactly like `project-manager`'s — GitHub
Issues as task records, a GitHub Project's `Status` field as the swimlane,
`gh` CLI for every state transition. See `tools/project-manager/CONVENTIONS.md`
§§ "Project setup", "Task issues & swimlane progression", "Worker lifecycle",
"Git hygiene" for the mechanics in full; this fork does not repeat them.

The one open seam: project-manager's planner reads FR/NFR text out of the root
plan Issue's body. krill-work's planner instead reads it live from
`get_feature_set_slice`/`get_feature_slice` against the FeatureSet/Feature id
the design session signed off — **there is currently no MCP write path to mint
a `PointerArtifact` linking that FeatureSet to the GitHub tracking issue
krill-work creates for it** (the only precedent, krill's own self-hosted
product, was wired by hand/importer tooling, not via MCP). `TODO(M3)`: until a
`PointerArtifact`-minting tool exists, krill-work's planner cites the
FeatureSet/Feature id directly in the tracking issue body as plain text (see
`agents/planner.md`) — treat that as the explicit interim, not a design
decision to preserve.

Each work-axis persona file below marks its own `TODO(M4)` points inline,
where a `gh issue`/Project `Status` transition should become a `claim` /
`heartbeat` / `complete` / `abandon` / `note` MCP call once M4 ships.

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
