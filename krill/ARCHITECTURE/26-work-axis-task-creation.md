# The work axis: task creation (FR1, NFR1/NFR3/NFR5/NFR6/NFR7, issue #2719)

Migration `015_work_axis` (root plan issue #2717, M4) is the one migration
number reserved for the whole work axis — `task`, `task_dependency`,
`task_claim`, `task_lease_event`, `task_attempt`, `task_note` — created
together so later M4 tasks never risk a golang-migrate version gap by
landing a later-numbered migration before an earlier one; each later task
states "no new migration" and implements against tables this one already
created. `task` is the one **append-only-plus-claimed** table this
milestone ships (NFR2): written once at create, then updated in place only
for claim/lease/lane fields a later M4 task's claim/heartbeat/reclaim path
touches — every other table is a plain append-only log. See the migration
file's own per-table comments for the full LB3 reasoning.

**`CreateTask` (`krill/store/task.go`) is FR1's whole vertical slice**: a
task is scoped to exactly one `milestone_ref` row, resolved to either a
milepebble (always allowed) or a milestone with no milepebble cut (checked
by an `EXISTS` query against `parent_milestone_id` inside the same
transaction as the insert) — a milestone that already has a cut, or any
other `milestone_ref` kind (today, only the backlog bucket), is rejected
with a named error (`ErrMilestoneHasMilepebbleCut`), never silently
retargeted. A Feature or Requirement id is rejected too, but for free:
`milestone_id` only ever resolves against `milestone_ref`, a table neither
of those ids ever appears in, so it surfaces as the same `ErrNotFound` a
stale or cross-scope milestone id would (NFR7). `lane_sequence`/
`starting_lane` validation (non-empty, no duplicates, every element one of
the five canonical lane names, in that relative order, lanes skippable)
is pure Go validation (`validateLaneSequence`) run before any DB round
trip — `current_lane`/`lane_sequence` are krill's own stored state,
never read back from a git branch name or other external ref (NFR5).

**HTTP and MCP are both thin wrappers, LB7 applied literally.** `POST
/tasks` (`krill/api/handlers/task.go`) and the `create_task` MCP tool
(`krill/mcp/tools/task.go`) both resolve `scope_id` and the two-subject
LB4 pair from the caller's session — `sess.ScopeID`/`sess.Acting`/
`sess.OnBehalfOf` on the HTTP side (`RequireSession`, NFR6's write gate),
`requireKrillSession`'s resolved `store.Session` on the MCP side — never
from the request body or tool input, mirroring every other create
endpoint in this package. `create_task` mounts on the same design-scoped
`*mcp.Server` `/mcp/design` already backs (see "The design-session MCP
surface" above and "The MCP spec surface"'s corrected note on the
`/mcp/work` mount that never happened) rather than getting a dedicated
work-axis mount, and is restricted to `PersonaSwarmOperator` — root plan
issue #2717's Personas section states the Swarm Operator, not the Agent,
creates tasks and their dependency edges; the Agent's role starts at claim,
a later M4 task.

