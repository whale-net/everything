# The run log: Temporal orchestrates, CI still pushes, the registry records

**Updated by #886/#889 (App Registry v2 release job).** This section
originally argued that the registry keeps the saga *log* but never
*orchestrates* it, and that no Temporal workflow sits on the inbound path.
That claim is now **superseded for UI-triggered releases**: the Temporal
`ReleaseWorkflow` (`worker/release/`, #889) is the actor that drives
trigger→build→publish→record end to end for a release triggered from the App
Registry UI (#890), replacing the human who used to run `gh workflow run
release.yml`/fill in the Actions form (cutover, #891). See
`architecture/08-release-lifecycle/11-rejected-alternatives.md`'s updated
"Registry orchestrates the release saga" row and `ARCHITECTURE.md` design
principle 5's clarifying note for why this does not violate "Record, don't
act": the actor that now orchestrates is `app-registry-worker` (the same
binary that already runs `WritebackWorkflow`), not the registry/gRPC server
itself — the server component still only mutates rows and emits writeback
intents.

What did **not** change, for `release.yml` (v1): CI still performs every
actual GHCR/chart-repo push and still calls
`BeginPublish`/`BeginPublishBatch`/`RecordArtifact` exactly as the rest of
this file describes — those RPC mechanics, the `allocated → publishing →
published` state machine, and the run-log/resume semantics below are
unchanged for v1. What changed is *who calls CI and polls it to completion*:
a Temporal workflow now sits above `release.yml` as its caller/poller instead
of a human, and it resolves the release plan once (`ResolvePlan`) and passes
that resolved plan into `release.yml` as a literal input (bypassing
`plan-release`'s own independent resolution for that invocation) rather than
`plan-release` resolving it itself — the "resolved once, reused everywhere"
property FR7/FR8 describe. A release run still spans real GHCR pushes, so it
still cannot be one database transaction, and it still must not become one:
re-pushing eight images because the ninth failed is exactly wrong — the run
log below is unaffected by any of this.

**Updated again by #928, for `release-v2.yml` (v1 unaffected).** The
paragraph above ("CI still performs every actual GHCR/chart-repo push and
still calls `BeginPublish`/.../`RecordArtifact`") is no longer true for v2:
`release-v2.yml`'s merged `build-release-artifacts` job only pushes app
images by digest (a build-scoped tag, not the final version) and composes
chart source trees — it calls neither `BeginPublish` nor `RecordArtifact`.
Those calls, along with the registry-side GHCR retag to the final version
and the ChartMuseum upload, now happen in `app-registry-worker` itself, in
the new `FinalizePublish` Temporal activity
(`worker/release/finalize.go`), which runs after `PollBuild` observes the
GHA run complete and before `VerifyPublished`. It reuses the exact same
`BeginPublish`/`FailPublish`/`RecordArtifact` RPC mechanics this file
describes — via `finalize-app`/`finalize-chart`, new `release_helper_go`
subcommands that call `ExecuteRelease` exactly as `release-app`/
`release-charts` already did (see `tools/release_helper_go/cmd/
finalize_app.go`/`finalize_chart.go`) — so the `allocated → publishing →
published` state machine and run-log/resume semantics below are unchanged
in substance, only in *which process* (`app-registry-worker` instead of a
GHA runner) makes the calls, for v2 only.

**This needs no new tables either.** `build` is already the run aggregate
(`(workflow_run_id, workflow_attempt)` unique); the artifact rows in
`allocated`/`publishing`/`published`/`failed` are its children. So:

- "What is incomplete?" = artifacts for this build not in `published`.
- **Re-running a release job becomes a resume**, not a blind retry: the run log
  names exactly which children are short of `published`, so the job re-attempts
  those and then the chart, instead of redoing everything or failing the same
  way.
- Exposed as `GetReleaseRun(workflow_run_id[, attempt])` and
  `app-registry builds status --incomplete` (AR-7d).

**The intent set is written up front, at every adoption stage** (decided in
review of PR #559). The release plan step writes one `allocated` artifact row
per target *before* anything is pushed — at stage `allocate` that row is the
`AllocateVersion` result; at `observe`/`promote` the version still comes from
the tag path and the registry is merely recording the intent. So "is this run
complete?" is exact from the first phase rather than only after the AR-5
cutover, and the cutover itself becomes a change of *who authors the version*,
not a new write.

Two consequences that are part of the design, not caveats:

- `allocated` means "this run intends to publish this version" — who *chose*
  the version is a separate axis, `artifact.version_source ∈ ('registry',
  'tag')`. That column is not bookkeeping for its own sake: AR-5's parity exit
  criterion ("allocated versions match what tag-scanning would have produced")
  becomes a query over rows that carry both, instead of a manual comparison
  against soak data.
- **The reaper must expire stale `allocated` rows too**, not just
  `publishing`. A cancelled run would otherwise hold a version number in
  `UNIQUE (owner_id, kind, version)` forever. Same sweep, same timeout
  configuration, `failed` with reason `stale`.

Declaring the set in a separate `build_target` table was considered and
rejected: the `allocated` state already is the declaration, and a second table
would restate it in a shape that can disagree with what the run actually did.

**As built (AR-7b), the intent-set write is narrower than "before anything
is pushed" describes.** No third RPC exists to declare intent independently
of `BeginPublish` (the phase's scope named exactly two new RPCs,
`BeginPublish`/`FailPublish`), and `AllocateVersion`'s per-domain gate
(this document's "Version model" section) structurally cannot serve `observe`/
`promote` domains — it would misattribute `version_source` to `registry` for
a version the tag path actually chose. So AR-7b implements the intent write
as `BeginPublish` called as the **first step of each matrix leg**, strictly
before that leg's own push, rather than from the plan job before the whole
matrix fans out. This is `allocated|∅ → publishing` directly, not a separate
`∅ → allocated` row for `observe`/`promote` domains — the `allocated` state
in that case is authored by `AllocateVersion` only at `allocate` stage, per
the table above. The gap this leaves: a matrix leg that never starts at all
(the job itself failed to schedule) has no row of any kind, so "is this run
complete?" cannot distinguish that from "not part of this run" for such a
leg. Closing that gap needs either a new RPC or loosening
`AllocateVersion`'s stage gate, and is deliberately left to AR-7d, which
owns `GetReleaseRun`/resume semantics — see PLAN-HISTORY.md's AR-7b
"Deliberately NOT done".

**As built (AR-7d), the gap above is closed — with one adaptation the
original design didn't anticipate.** The intent write is now genuinely
up-front: `release.yml`'s `plan-release` job calls a new bulk RPC,
`BeginPublishBatch`, once, before the release matrix fans out, for every
planned app target. But it writes straight to `publishing`, not `allocated`
as this section originally described. Migration 007's own
`artifact_state_shape` CHECK constraint — shipped by AR-7b, before AR-7d was
implemented — requires `build_id IS NULL` for `state = 'allocated'` and
`build_id IS NOT NULL` for `state = 'publishing'`. An intent row written
before any push has no digest either way, but it can be written with or
without a `build_id`; the constraint forces the choice, and "no schema
change" (AR-7d's own scope boundary) rules out relaxing it. Going straight
to `publishing` is the schema-compatible choice, and it turns out to be the
more useful one anyway: it means the intent row already carries the
`build_id` that ties it to a specific run, which an `allocated` row
structurally cannot. `GetReleaseRun`'s query is exactly `SELECT * FROM
artifact WHERE build_id = $1` — simple only because every intended target,
reached or not, already carries that build's id from the moment
`BeginPublishBatch` runs. `AllocateVersion`'s own `∅ → allocated` write
(the `allocate`-stage path, still inert — no domain has reached that stage)
is unaffected; this only changes how the pre-cutover, `observe`/`promote`
intent row is written.

Concretely:

- `BeginPublishBatch(build_id, targets[], idempotency_key_prefix)`
  (`protos/api_messages_artifact.proto`) takes a build already recorded by
  `RecordBuild` (called once from `plan-release`, ahead of the matrix, and
  again — idempotently, same key — from each matrix leg as before) and a
  list of `(kind, owner_full_name, version)` targets. Each target is
  processed independently: `server/handlers/artifact.go`'s
  `BeginPublishBatch` loops and calls the exact same `beginPublishOne`
  transition `BeginPublish` itself calls (refactored out so both RPCs are
  provably the same code path), so `∅|allocated|failed → publishing`'s
  legal-transition rules are enforced identically either way. A per-target
  failure (unresolved owner, malformed version) is reported in that
  target's own `BeginPublishBatchResult.error` and does not block the
  others — the same partial-apply shape AR-7a's `ReconcileApps` established
  for a structurally identical problem (one bad chart must not roll back
  every other app/chart in the same call).
- **The reaper hazard this up-front write introduces, and the fix.**
  Stamping `state_changed_at` once, at plan time, for every target has a
  consequence the original design didn't anticipate: the stale-row
  reaper's `ARTIFACT_REAPER_TIMEOUT` (see ENV.md) now measures the *whole
  run*, not one matrix leg, for any target whose leg hasn't started yet.
  A first version of this phase also removed `release.yml`'s per-leg
  "Begin publish (image)" step as redundant — but a row the reaper reaps
  to `failed` before its leg starts cannot be revived by anything: the
  leg pushes to GHCR anyway and `RecordArtifact` then rejects the
  completion (`failed → published` is not a legal transition), an
  unrecorded published image, exactly the failure ordering 3 and the rest
  of AR-7 exist to prevent. The fix has two parts:
  - `BeginPublish`'s legal-transition set gains `publishing → publishing`
    as an idempotent heartbeat/re-arm — it refreshes `state_changed_at`
    (and `build_id`) without changing state, rather than being rejected as
    `FailedPrecondition`. Every other illegal transition is still
    rejected. See `repository.ArtifactRepository.BeginPublish`'s doc
    comment and `ArtifactState`'s doc comment.
  - `release.yml`'s per-leg "Begin publish (image)" step is **restored**,
    immediately before that leg's own push. It now does two jobs a
    plan-time-only write cannot: re-arm the staleness clock right before
    the work it bounds (a heartbeat, restoring a one-leg budget for any
    leg that actually runs), and revive (`failed → publishing`) a row the
    reaper already expired while the leg was still queued — the
    already-legal `failed → publishing` retry transition, just triggered
    by the reaper instead of a whole re-run.
  - **This means the batch call's and the per-leg call's idempotency keys
    must NOT collide**, unlike the collision-safety property originally
    designed here — if they did, `runIdempotent` would replay the batch's
    cached response for the per-leg call instead of letting it
    re-execute, silently defeating the heartbeat. So
    `BeginPublishBatch`'s derived key carries an `-intent` suffix
    (`<idempotency_key_prefix>-<owner_full_name>-<kind>-intent`),
    deliberately distinct from the per-leg call's own
    `<prefix>-<owner_full_name>-<kind>` (no suffix). A stray duplicate
    individual `BeginPublish` call using the batch's `-intent`-suffixed
    key would still replay safely; a stray duplicate of the per-leg call
    itself is exactly the heartbeat this fix wants, so re-executing
    rather than replaying is correct there too.
- `GetReleaseRun(workflow_run_id[, attempt])` (`workflow_attempt == 0` means
  "the latest attempt recorded for this run id") returns the `build` row
  plus every `artifact` row sharing its `build_id`, via a new
  `BuildRepository.GetBuildByWorkflowRun` and a `BuildID` field on
  `ArtifactListFilter` (reusing `ListArtifacts` rather than a second query
  path). "What is incomplete?" stays exactly `state != PUBLISHED`, filtered
  client-side by the CLI's `--incomplete` flag rather than a second
  response field — it is a filter over `artifacts`, not separately derived
  data.
- **Scope: this covers app images only, not helm charts.** The exit
  criterion this closes is stated in terms of an app ("a run killed before
  reaching an app"), and `release-helm-charts` already begins publishing
  each chart before its own push (AR-7b, unchanged) — it is a single job
  with an internal loop over charts, not a GitHub Actions matrix, so "a leg
  that never gets scheduled" is not a failure mode it has in the same way a
  matrix job does. Extending `BeginPublishBatch` to charts is a natural
  follow-on, not a design change, if that gap is ever observed in practice.
- `OPERATIONS.md` "A release run didn't complete" is the runbook: query
  `app-registry builds status <run-id> --incomplete`, then re-run the
  workflow — a resume, not a blind retry, since every already-`published`
  child's recording calls replay idempotently rather than re-executing.

