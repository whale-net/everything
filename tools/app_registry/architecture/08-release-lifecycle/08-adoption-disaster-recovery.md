# Adoption and disaster recovery

Charts pin digests resolved from GHCR by tag, so a chart can pin an image
published before the registry existed, or while the opt-in was off. There is no
run to resume for those, they will never be in the registry, and under
"registry is source of truth" the chart correctly — and permanently — fails.

**The way out is an explicit, bounded adoption path** (`AdoptArtifact`, admin
role only, not the builder credential): record a pre-existing GHCR image or
chart as `published` with `provenance = 'adopted'` and a required reason.
`artifact.provenance ∈ ('observed', 'adopted')` makes "which rows did we take
on faith?" a query rather than an archaeology exercise. It is used lazily —
when a chart release fails on an unknown pin — not as a bulk backfill, which
"Resolved questions" #3 still rejects.

The same path is the disaster-recovery path if the registry is lost or
restored behind: GHCR and the chart repository still hold the artifacts, so
state is re-adoptable. OPERATIONS.md carries the runbook (AR-7e). The design
goal is that this is *rare and deliberate*, not a recurring chore — every
other part of AR-7 exists to keep it that way.

**As built (AR-7e).** Everything above is real: `ArtifactRegistry.
AdoptArtifact` (`server/handlers/artifact.go`), gated on
`auth.RoleAdmin` — never `auth.RoleBuilder`, the one deliberate exception to
every other `ArtifactRegistry` write RPC requiring the builder role, because
this is the single RPC that asserts an artifact into existence rather than
recording an observed publish. Two decisions this section left implicit and
the implementation had to make explicit:

- **State-collision semantics**, since `AdoptArtifact` is a new entry point
  into the same `artifact` state machine `postgres/artifact.go` already
  enforces, not a separate table. An existing `published` row with the SAME
  digest (observed or previously adopted) is an idempotent no-op —
  Provenance/State are never rewritten, so adoption can never downgrade an
  `observed` row to `adopted` after the fact. A DIFFERENT digest on an
  already-`published` row is `ErrAlreadyExists`, mirroring
  `RecordArtifact`'s identical conflict rule. An `allocated` row is
  `ErrFailedPrecondition` — a live reservation is not what adoption is for.
  A `failed` or in-progress/abandoned `publishing` row can be completed by
  adoption (`failed|publishing → published(adopted)`) — the disaster-recovery
  case: an existing attempt was interrupted, failed recording, or gave up,
  but the artifact demonstrably exists in the container/chart registry.
- **`artifact.build_id` is a real foreign key, and migration 007's
  `artifact_state_shape` CHECK requires it `NOT NULL` once `published` —
  but by definition there is no CI run behind a pre-registry artifact.**
  Rather than a schema change (ruled out by this phase's own scope),
  `AdoptArtifact` writes a synthetic `build` row: `workflow_run_id`
  `"adopted:<uuid>"` (non-numeric, cannot collide with a real GitHub
  Actions run id), `git_ref = "adopted"` as the same marker, `actor` the
  calling admin's own identity. The `failed → published` branch reuses the
  existing row's `build_id` when it already has one (true whenever the
  reaper reaped a `publishing` row; false when it reaped an `allocated`
  row, which never had one) — the real CI run that actually attempted the
  push is more honest provenance than a synthetic placeholder.

`reason` is required (validated at the handler layer) and logged
structurally on every call, but — like `SetAppStatusRequest.reason` — not
stored in a new column: no schema change was needed, matching this section's
"no backfill" framing. `ListArtifactsRequest.provenance` /
`ArtifactListFilter.Provenance` make "which rows did we take on faith?" a
query, satisfying the exit criterion's "distinguishable in one query" half.

