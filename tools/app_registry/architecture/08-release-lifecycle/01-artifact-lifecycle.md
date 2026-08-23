# Artifact lifecycle: `allocated → publishing → published`

The registry stops learning about an artifact *after* the fact. It records the
intent to publish **before** the push, and completes the record after.

> **Fixed: issue [#585](https://github.com/whale-net/everything/issues/585) & issue [#784](https://github.com/whale-net/everything/issues/784).**
> `RecordArtifact` and `AdoptArtifact` idempotent-replay steps are scoped by `(digest, owner, kind, version)`.
> Within a single minor release series (`vX.Y`), content digests must remain unique (`UNIQUE (owner_id, kind, version_major, version_minor, digest)` in migration 013), preventing redundant patch releases with identical digests.
> However, across distinct minor or major version promotions/releases (e.g. `v0.1.5` -> `v0.2.0`), content digests may be shared to establish new version baselines for unchanged subcomponents. Version uniqueness remains strictly guarded by `UNIQUE (owner_id, kind, version)`. Replays for the exact same `(owner, kind, version, digest)` return the existing published row, while a new minor/major version with a shared digest creates a distinct artifact record.

| State | Written by | version | build_id | digest |
|---|---|---|---|---|
| `allocated` | `AllocateVersion` (AR-5) | ✓ | — | — |
| `publishing` | release run, immediately **before** the GHCR/chart push | ✓ | ✓ | — |
| `published` | release run, after the push, carrying the digest the push returned | ✓ | ✓ | ✓ |
| `failed` | release run on error, or the reaper on timeout | ✓ | ✓ | — |

**This removes a table rather than adding one.** `version_allocation` exists
only because `artifact.digest`/`build_id` are `NOT NULL` and allocation
happens before a build — i.e. it already *is* the `allocated` state, stored
elsewhere. Migration 007 makes those two columns nullable, adds `state`, folds
`version_allocation` rows into `artifact`, and drops it. `UNIQUE (owner_id,
kind, version)` then spans every state, which is strictly stronger than
today's two-table arrangement: it is the allocation collision guard, and
"next version" collapses from a max across two tables into one query.
`artifact_digest_idx` becomes `UNIQUE ... WHERE digest IS NOT NULL`.

Legal transitions, enforced server-side; anything else is `FailedPrecondition`:

- `∅ → allocated` (`AllocateVersion`), `∅ → publishing` (`BeginPublish`
  without a prior allocation — the pre-cutover path, see below),
  `allocated → publishing` (`BeginPublish`), `publishing → published`
  (`RecordArtifact`), `publishing → failed` (`FailPublish`, or the reaper),
  `failed → publishing` (a later run retrying the same version).
- `published` is terminal. Re-recording the same digest is an idempotent
  success; recording a *different* digest for an already-`published` version
  is rejected — that is a real conflict, not a retry.

**What this buys.** Ordering 3's hard reject stops being a trap: the image row
exists from `publishing` onward, so a chart failing on "pins an image the
registry doesn't have" now means the image genuinely was never published,
which is worth failing on. The registry never reconstructs or infers a record
it didn't observe (an explicitly rejected alternative — see below).

**The reaper is not optional.** A cancelled workflow leaves a `publishing` row
forever, and "what is incomplete?" is only a usable recovery query if it does
not accumulate ghosts. `app-registry-worker` sweeps `publishing` rows older
than `WRITEBACK`-style configured staleness to `failed` with reason `stale`.
Ships with AR-7b, not after it.

**Backward compatibility during rollout — removed by the AR-5 cutover.**
`RecordArtifact` against no existing row used to keep working, creating the
row directly in `published` — allowed while a domain was at adoption stage
`observe`, rejected at `allocate` (where allocation must have happened
first). That let CI adopt `BeginPublish` per domain instead of in one
cutover. The AR-5 cutover removed the fallback entirely, for every domain:
`RecordArtifact` now unconditionally requires a prior successful
`BeginPublish` for that exact `(kind, owner, version)`, or it fails with
`FailedPrecondition` ("no publishing artifact found ... `BeginPublish` must
run before `RecordArtifact`"). See "Availability, restated per adoption
stage" below for why this is no longer gated per domain, and PLAN.md's
"AR-5 — cutover status" for how the cutover landed.

**As built (AR-7b).** Everything above is real: migration `007` ships
exactly this shape, plus an `artifact.fail_reason TEXT` column (not
originally named in this design) so `FailPublish`'s caller-supplied reason
and the reaper's hardcoded `"stale"` are both recorded, not just implied by
the state transition. One decision this section left implicit and the
implementation had to make explicit at the time: the direct-create fallback
was legal **only** at `observe`, not at `promote` too — `promote`'s own row
in "Availability, restated per adoption stage" below already made recording
mandatory there, so a domain at `promote` with no prior `publishing` row
meant `BeginPublish` itself had failed (or was skipped), and that had to
surface as a rejection, not a silent fallback to the old behavior. **The AR-5
cutover superseded this distinction entirely** — see the "Backward
compatibility" callout above: the fallback is gone for every domain, not
just gated more strictly at `promote`/`allocate`, so there is no longer a
per-stage question to ask here. The reaper (`worker/reaper`) is a third loop
in `app-registry-worker`, alongside the outbox drainer, configured via
`ENV.md`'s `ARTIFACT_REAPER_TIMEOUT` / `ARTIFACT_REAPER_POLL_INTERVAL` — see
`worker/README.md`.

**Where `artifact.repository` comes from on `∅ → publishing`.** That branch
creates the row from nothing, so it needs a value for the `NOT NULL`
`repository` column, and there is no single source that serves both kinds.
An image's is derivable server-side from the owning app's stored
`image_repository`. A chart's is **not**: `chart.chart_repository` has never
been populated by any write path, and migration `008` hardcodes it to `''` in
`v_current_chart` — a chart lives at a ChartMuseum URL that is deployment
configuration the registry has no way to derive. So `BeginPublishRequest`
carries an optional `repository`, which wins over the owner row when set;
chart callers must set it, and `release.yml` passes the same
`$CHART_REPO_URL/<published-name>` its `RecordArtifact` call already passed.
A chart taking this branch with neither is rejected rather than recorded with
an empty repository. This was AR-7b's original blind spot: because
`BeginPublish` resolved the repository *only* from the owner, every chart
release failed the transition, so no chart ever reached `publishing` and
`FailPublish` — gated on that step having succeeded — could never arm.

