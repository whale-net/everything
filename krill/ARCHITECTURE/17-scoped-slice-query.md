# The scoped-slice query (LB7, issue #2491)

`krill/slice` implements FR5-FR9 — the C3 scoped query — over
`krill/store`'s spec entity model. `krill/api/handlers` is the HTTP
surface over it, wired into `api`'s mux in `routes.go`.

**One document type, five granularities.** `slice.Document` (`document.go`)
is the single typed, self-describing shape every granularity returns:

- `GetFeatureSetSlice` (FR5) — a FeatureSet, its Features, their FRs/NFRs,
  and *only* the LoadBearingDecisions attached to that FeatureSet — never
  the product-wide decision list.
- `GetFeatureSlice` (FR6) — a Feature and its FRs/NFRs, nothing else.
- `GetRequirementSlice` (FR7) — a single FR or NFR by surrogate id alone.
- `GetProductSlice` (FR8) — every FeatureSet, Feature, FR, NFR, and
  LoadBearingDecision beneath a Product, in one call.
- `GetEntitySetSlice` (FR5, M2, issue #2544) — an explicit, caller-supplied
  set of entity ids spanning any mix of FeatureSet/Feature/Requirement/
  LoadBearingDecision. This is the one granularity whose membership rule
  differs from the four above: each of those is a subtree of the spec tree
  reachable from one root id, while `GetEntitySetSlice`'s input is an
  arbitrary union with no shared root — the shape M2's design sessions need
  ("the entities one session touched" is not a FeatureSet, a Feature, or a
  Product, just a scattered set). It also differs in *when* it reads: it is
  always a live-state read of each id's current row, never a replay
  through `HistoryStore`/the `*AsOf` twins below — an id's revision as of
  whatever event last touched it is not what M2's "current draft" means.
  An id with no current row of any kind is skipped, not an error, since
  the set is heterogeneous by construction. `krill/slice` itself never
  reads `revision_event` or knows what a design session is; resolving
  *which* ids belong to one session — the union of every
  `revision_event.entity_deltas[].entity_id` for that session — is
  `krill/api/handlers/session_slice.go`'s job, behind
  `GET /design-sessions/{id}/slice`.

`Document` does not vary by granularity (FR9): it carries an explicit
`schema_version` (`slice.SchemaVersion`), and every included entity embeds
an `EntityRef` — its surrogate id (LB2) and the as-of revision (SCD2
`revision_id`) it was assembled from. A granularity that doesn't reach a
given entity kind simply leaves that field of `Document` empty; there is
no second response type. **This is the payload M4's claim will later
enrich (LB7)** — not a UI response shape, and not something a later task
should fork into a per-consumer projection. Display numbers are
deliberately absent from `Document` today; if a later consumer needs one,
it is computed at assembly time from a sibling's `Position`, never read
from a stored column (LB2) — see `document.go`'s comment on
`ProductEntity.Position` for where that boundary is written down.

**Read-only, no `init` gate.** None of the five query methods or their
HTTP routes check `session` — FR3's `init` gate is write-only, and read
paths never require it (root plan issue #2485). `krill/store`'s existing
`ListCurrentBy*`/`GetCurrentByID` methods cover three of the four M1
granularities directly; `krill/store/slice.go`'s `SliceStore` adds the
cross-table joins (`ListRequirementsByFeatureSet`,
`ListFeaturesByProduct`, `ListRequirementsByProduct`,
`ListDecisionsByProduct`) that `GetFeatureSetSlice` and `GetProductSlice`
need and no single entity's `*Store` owns on its own, plus the
by-id-set reads (`ListFeatureSetsCurrentByIDs`, `ListFeaturesCurrentByIDs`,
`ListRequirementsCurrentByIDs`, `ListDecisionsCurrentByIDs`, each a single
`WHERE id = ANY($1) AND valid_to IS NULL`) `GetEntitySetSlice` needs — one
query per entity kind rather than one query per sibling or per id,
regardless of how many FeatureSets/Features exist beneath a requested id
or how large an entity-id set is.

