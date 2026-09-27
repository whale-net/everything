# The milestone authoring schema (FR1, FR2, LB6, issue #2683)

Migration `010_milestone_authoring` is the first `ALTER TABLE` migration in
`krill/` -- every migration through `009` only ever `CREATE TABLE`d. Two
schema decisions worth calling out beyond what that migration's own inline
comments cover:

**`milestone_ref`'s new LB4 subject-pair columns are nullable, unlike
`pointer_artifact`'s.** `pointer_artifact` (migration 005) made its
`created_by_*` columns `NOT NULL` because every write onto that table goes
through a session-gated HTTP handler. `milestone_ref` already has a
pre-existing write path with no session to attribute to --
`krill/importer`'s `GetOrCreateRef` (FR16) -- and this issue's own scope
keeps that path "working unchanged." Making the new columns `NOT NULL`
would have broken that INSERT outright, so they are nullable instead: a
row `CreateMilestone` (the new authoring path, this issue's Implementation
phase) writes always has both populated; a row the importer writes never
does. `GetMilestone`'s two callers can tell which path produced a given row
from that alone.

**`entity_milestone.relation` replaces (not just extends) the old unique
index.** `entity_milestone_entity_milestone_idx` was `(entity_id,
milestone_id)` through migration 004 -- LB6's one association table,
implicitly always "delivers." Migration 010 adds `relation` (`'delivers'`
| `'must_not_foreclose'`, default `'delivers'` so every importer-written
row keeps its existing meaning) and rebuilds the unique index as
`(entity_id, milestone_id, relation)`, so a `Delivers` and a `Must not
foreclose` row can now coexist for the same `(entity_id, milestone_id)`
pair without a spurious duplicate rejection -- distinguishing the two
lists with a column on the one association table LB6 already settled,
never a second table and never a column on the spec entity itself.

`krill/store/milestone_authoring.go`'s `MilestoneAuthoringStore` is kept as
a sibling accessor (`(*Store).MilestoneAuthoring()`) next to the
pre-existing `MilestoneStore` (`(*Store).Milestones()`, migration 004)
rather than folded into it, so the importer's `GetOrCreateRef`/
`AddAssociation` surface is untouched by this addition.

**Implementation phase (this issue).** `MilestoneAuthoringStore`'s methods
are implemented over `milestone_ref`/`milestone_deferral`/
`entity_milestone`, `api/handlers/milestone.go`'s six endpoints are wired
into `routes.go` (five behind `RequireSession`, `GET /milestones/{id}`
ungated), and `mcp/tools/milestone.go`'s `RegisterMilestoneAll` is wired
onto the design mount (`../main.go`'s `designReg`, alongside
`RegisterDesignAll`) -- not the read-only spec mount -- since
`create_milestone`/`set_fr_budget` need the same krill-session-derived
LB4 subject pair every other write tool on that mount already resolves
(`krillSessionInput`/`requireKrillSession`, design.go).

Two schema-shape consequences worth calling out:

- `milestone_ref` and `milestone_deferral` were originally **not** SCD2 (LB3:
  see this section's earlier note and 010's own comment), which is why
  `position.go` needed a second helper, `nextSiblingPositionPlain`, with the
  same `COALESCE(MAX(position), -1) + 1` shape as `nextSiblingPosition` but
  no `valid_to` filter, used by `CreateMilestone` and `AddDeferral`. Migrations
  020 and 023 made both tables SCD2, so the special case is gone:
  `position.go`'s `nextSiblingPositionPlain` is deleted, and every
  `MilestoneAuthoringStore` method that takes a `milestoneID` and a `scopeID`
  together now uses `position.go`'s `nextSiblingPosition` and `errors.go`'s
  `currentRowExists` like every other spec-axis table. `plainRowExists` is
  still `currentRowExists`'s non-SCD2 counterpart, but its remaining callers
  are the work-axis tables (`task`, `task_note`, `task_dependency`), which
  genuinely have no `valid_to` column.
- `entity_milestone_entity_milestone_idx` widening to `(entity_id,
  milestone_id, relation)` (migration 010) breaks the pre-existing
  `MilestoneStore.AddAssociation`'s `ON CONFLICT (entity_id,
  milestone_id)` clause -- Postgres requires an `ON CONFLICT` target to
  name a real unique constraint's columns exactly, and `(entity_id,
  milestone_id)` alone stopped being one. `AddAssociation` now inserts an
  explicit `relation = 'delivers'` (the importer's only relation, FR16 has
  no "must not foreclose" concept) and targets all three columns.

`krill/store/milestone.go`'s `ListRefsByProduct` now filters
`kind = 'milestone'` explicitly, and `krill/render/render.go`'s
`renderMilestones` filters the same way defensively (belt-and-suspenders:
a `Source` implementation that forgets to filter can still never leak a
later kind into the rendered roadmap) -- migration 010 added the column
and its CHECK constraint ahead of any second kind existing, but as of this
issue every reader is kind-aware.

