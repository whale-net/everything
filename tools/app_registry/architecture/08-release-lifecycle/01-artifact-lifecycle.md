# Artifact lifecycle: `allocated → publishing → published`

The registry stops learning about an artifact *after* the fact. It records the
intent to publish **before** the push, and completes the record after.

> **Fixed: issue [#585](https://github.com/whale-net/everything/issues/585).**
> `RecordArtifact`'s idempotent-replay step used to match an existing row by
> `digest` alone, with no `(owner, kind, version)` scoping. A reproducible,
> no-op rebuild (routine in this monorepo) can produce a digest identical to
> an *older* published version of the same app; the lookup matched that
> older row, reported success, and never transitioned the new version's
> `publishing` row — it sat until the reaper reaped it to `failed`. Confirmed
> live against `dev`: every image released by run
> [31660476677](https://github.com/whale-net/everything/actions/runs/31660476677)
> hit this. The lookup is now scoped to the request's own `(owner, kind,
> version)` identity, so a same-digest/different-version request no longer
> short-circuits against the wrong row — it falls through to `artifact_digest_idx`'s
> real uniqueness constraint instead, which correctly rejects it (`digest` is
> globally unique by design — see the `artifact` row in "Data model" above).
> That rejection is still a live problem for AR-5: it's harmless at
> adoption stage `observe` (recording is best-effort) but will hard-fail a
> routine no-op rebuild's release the moment a domain reaches `promote` or
> `allocate`, where recording becomes mandatory — see PLAN.md's "AR-5" for
> the design work still needed before that cutover.

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

**Backward compatibility during rollout.** `RecordArtifact` against no
existing row keeps working, creating the row directly in `published` —
allowed while a domain is at adoption stage `observe`, rejected at
`allocate` (where allocation must have happened first). That is what lets CI
adopt `BeginPublish` per domain instead of in one cutover.

**As built (AR-7b).** Everything above is real: migration `007` ships
exactly this shape, plus an `artifact.fail_reason TEXT` column (not
originally named in this design) so `FailPublish`'s caller-supplied reason
and the reaper's hardcoded `"stale"` are both recorded, not just implied by
the state transition. One decision this section left implicit and the
implementation had to make explicit: the direct-create fallback is legal
**only** at `observe`, not at `promote` too — `promote`'s own row in
"Availability, restated per adoption stage" below already makes recording
mandatory there, so a domain at `promote` with no prior `publishing` row
means `BeginPublish` itself failed (or was skipped), and that must surface
as a rejection, not a silent fallback to the old behavior. The reaper
(`worker/reaper`) is a third loop in `app-registry-worker`, alongside the
outbox drainer, configured via `ENV.md`'s `ARTIFACT_REAPER_TIMEOUT` /
`ARTIFACT_REAPER_POLL_INTERVAL` — see `worker/README.md`.

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

