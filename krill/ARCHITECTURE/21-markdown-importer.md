# The markdown importer and the delivery-axis association (FR16, FR17, issue #2492)

`krill/importer` (a library) and `krill/importer/cmd` (its runnable
entrypoint, `bazel run //krill/importer/cmd:import -- --path <dir>
--session-id <uuid>`) are the one-way markdown importer LB5 and PRODUCT.md's
C8 describe: it parses a `PRODUCT.md` + `product/*.md` doc set (the layout
`tools/project-manager/CONVENTIONS.md` § Layout defines) into
`krill/store`'s spec entities and prints FR16's entity-id report.

**This is the only code path in `krill/` that ever parses a committed
markdown document back into entities (LB5, FR15) — `krill/importer/
importer.go`'s package doc states this explicitly and names FR15.** A
future renderer (FR13, a separate task) only ever writes markdown from
krill's entities; neither it nor anything else in this tree reads a
committed doc the other direction. Within the importer itself, `parse.go`
never touches `krill/store` — it is a pure text-to-`ParsedProduct` function,
safe to run against a document this milestone does not import (see the
Testing section's `whagent_net` parse-fixture use) — and `write.go` is the
only file that calls a `Create`/`GetOrCreateRef` method, so a parse failure
never leaves a partial product behind.

**Gating.** Import is one of the six write paths `api/handlers.
RequireSession`'s doc comment names (FR3), but it is a CLI entrypoint, not
an HTTP handler, so it cannot literally wrap itself in that middleware.
`importer.Import` performs the same check directly against
`store.SessionStore.GetSession` (`requireSession` in `importer.go`) before
parsing or writing anything, and writes into the session's own `ScopeID` —
a caller with no valid krill session cannot import regardless of which
front door it comes through.

**Where a capability-map entry, a persona, and a non-goal land.** Personas
and non-goals map directly onto `persona`/`non_goal` under the imported
`Product` (issue #2488's decision). A capability-map entry (`Cn`) becomes a
`Feature`, grouped under a `FeatureSet` named for its bucket (`Now`,
`Next`, `Later`) — the bucket is a delivery-axis grouping, not a spec-axis
one, but a `FeatureSet` has to be *something* and "which bucket a
capability was in" is the only grouping the source document offers.
Load-bearing decisions have no natural bucket of their own, so the importer
gives every imported product one synthetic `FeatureSet` named "Load-bearing
decisions" (`loadBearingFeatureSetName` in `write.go`) to hold them,
created once per product and reused on a second import — never guessed
per-entry from which capability an `LBn`'s prose happens to mention.

**Milestone references and the delivery-axis association (LB6).** For each
`### M<n> — ...` roadmap heading, the importer resolves (creating on first
reference) a `milestone_ref` row scoped to `(scope, product, "M<n>")` via
`MilestoneStore.GetOrCreateRef`, then, for every capability id in that
milestone's own `Delivers:` line and every decision id in its own `Must not
foreclose:` line, adds one `entity_milestone` row via
`MilestoneStore.AddAssociation` — `(Feature.ID or LoadBearingDecision.ID,
milestone_ref.ID)`. Both methods are upserts (`ON CONFLICT DO NOTHING`), so
importing the same document twice does not duplicate either table. A
`Delivers:`/`Must not foreclose:` line's trailing prose explanation (e.g.
krill's own "— all seven, each for its own reason:" continuation) is never
scanned for ids — only the token list before the first dash on that same
line counts, so a continuation line's own cross-references to other
capabilities or decisions are never mistaken for this milestone's own list.
See migration `004_milestone_assoc.up.sql`'s LB6 note for the schema side
of this: `milestone_ref` carries only the bare `M<n>` identifier — no
status, no milepebbles, no authoring surface (those are M3's, C13/C28) —
and `entity_milestone` is the association, never a `milestone_id` column on
`feature` or `load_bearing_decision`.

**Fail loudly on an undefined milestone (FR17).** Beyond the per-milestone
`Delivers:`/`Must not foreclose:` pass, the importer scans the whole
roadmap document for every bare `M<n>` token — heading, prose, anywhere —
and returns a non-zero-exit error naming any token with no corresponding
`### M<n>` heading in that same document. This is what "the source names a
milestone its own roadmap section never defines" (the issue's own phrasing)
resolves to: a document is well-formed on this axis exactly when every
`M<n>` it mentions is also a milestone it defines.

### One-time import completion and re-import refusal (M2's FR12, NFR3, issue #2548)

Migration `009_import_completion` adds `import_completion`, the one-time,
one-way marker FR12 requires (root plan issue #2539's M2 FR12 — distinct
from, and numbered independently of, M1's own FR12 in "Amend and as-of
history reads" below; FR/NFR numbers are scoped to the milestone/plan that
defines them, not globally unique across `krill/`'s history). After an
import completes, `krill/importer.Import` records `(scope_id, product_id)`
as complete via `store.ImportCompletionStore.MarkComplete`; before parsing
anything, it checks `ListByScope` for a prior completion whose
`source_path` matches the requested `--path` and refuses
(`importer.ErrAlreadyImported`) if one exists — `refuseIfAlreadyImported`
in `importer.go`. The check is keyed on `source_path`, not `product_id`,
because the target Product does not exist (and its id is not known) until
after `Parse` and `write()` run; `MarkComplete`'s own row is still keyed on
`(scope_id, product_id)` because that is the pair FR12 actually needs to
be unique, per migration 009's "Keyed by (scope_id, product_id)" comment.

There is no un-complete verb and no update path on `import_completion` —
`MarkComplete` on an already-complete pair returns `store.
ErrAlreadyComplete` rather than overwriting `completed_at` or
`source_revision`; a genuine re-import is a deliberate future operation,
not a flag flip. NFR3's one-way guarantee: `source_path` and
`source_revision` are recorded for the audit trail only — nothing in
`krill/` ever opens `source_path` back off disk after an import completes,
and the caller (not krill) supplies `source_revision` (`--source-revision`
on `krill/importer/cmd`) so krill never shells out to git.

### Completeness accounting and the whagent_net import (M2's FR11, issue #2549)

`whagent_net` is the first product krill holds that krill did not
author (C27's adoption proof) — every other imported doc set to date
(krill's own self-import, FR18/FR19) is krill's own brief. Its doc set
(`whagent_net/PRODUCT.md` + `whagent_net/product/*.md`) already matches
the layout `parse.go` expects; no parser change was needed (confirmed
against the real files, not assumed — see
`krill/importer/importer_integration_test.go`'s
`TestParse_WhagentNetFixture_ParsesWithoutImporting`, which now also pins
exact per-section counts, not just non-emptiness).

FR11's harder half — "confirmation that nothing was lost in that import" —
is `krill/importer/coverage.go`'s `ComputeCoverage`, attached to every
`Report` as `Report.Coverage` (`report.go`) alongside FR16's entity-id
entries. `cmd/main.go`'s `--allow-unmapped` flag gates it: any non-zero
`Report.UnmappedTotal()` fails the run (non-zero exit) unless the caller
passes `--allow-unmapped`, which downgrades the failure to a logged
WARNING per `AGENTS.md`'s logging levels (a deliberate, acknowledged
partial import is "the system had to adjust something to keep going," not
an ERROR — an unacknowledged one is). See `krill/README.md` "Importing
whagent_net's brief" for the runbook and `krill/conformance/
whagent_net_import_integration_test.go` for the completeness proof (FR11
item 3): every reported entity id is looked up through
`slice.Querier.GetProductSlice` (or the relevant store getter) and its
content compared against the source text, not merely checked for
presence.

