# Release lifecycle (issue #558)

**Status: every sub-phase, AR-7a through AR-7f, is built and merged to
`main`** (#561, #562, #566, #567, #571, #572). Verified live: release run
[31660476677](https://github.com/whale-net/everything/actions/runs/31660476677)
(2026-08-13) exercised `AssertApps`, `BeginPublishBatch`, and the artifact
lifecycle against the deployed `dev` registry for real. That verification
also surfaced a defect, since fixed — issue
[#585](https://github.com/whale-net/everything/issues/585) — in
`RecordArtifact`'s digest-replay path; see "Artifact lifecycle" below for
how it was fixed. Delivery
is PLAN-HISTORY.md's AR-7. Six slices of this section describe real code: AR-7a's —
ordering 4, partial-apply reconcile, domain-qualified chart app references,
and the `continue-on-error` drop on `ci.yml`'s sweep job — AR-7b's — the
artifact lifecycle (`allocated → publishing → published → failed`),
migration `007`, `BeginPublish`/`FailPublish`, `RecordArtifact`'s
`publishing → published` transition, and the stale-row reaper — AR-7c's —
migration `008`, the app identity / manifest-snapshot split, `AssertApps`,
and `artifact`'s stored `manifest_id`/`promotability` — AR-7d's —
`GetReleaseRun`, the up-front intent write, `builds status --incomplete`,
and re-run-as-resume — AR-7e's — `AdoptArtifact`, its admin-only
authorization, the `∅|failed → published(adopted)` state-collision rules,
and `ListArtifacts`' provenance filter — and AR-7f's —
`ArtifactRegistry.CheckChartHermeticity` and its call site in
`tools/release_helper_go`'s `build-helm-chart`, see "Compose-time chart
hermeticity" below. Every sub-phase of AR-7 is now implemented. This section
supersedes "Release-vs-reconcile gap (issue #547)" above — AR-7c closes that
gap for real, see the callout there — and changes what "Availability and
bootstrap" below promises; both are cross-referenced where that happens.

**Updated by #886/#889 (App Registry v2 release job).** For a release
triggered from the App Registry UI, the Temporal `ReleaseWorkflow`
(`worker/release/`, #889) is now the actor that resolves the release plan and
invokes `release.yml`'s matrix, replacing the human who used to run `gh
workflow run` (cutover, #891) and replacing `release.yml`'s own `plan-release`
job's independent resolution for that invocation. What this section describes
below — the `build`/`artifact` state machine, the four cross-run orderings,
and how each is (or isn't) enforced — is unchanged: this is a change in *who
drives ordering 1's plan resolution and who invokes the matrix*, not a change
to the ordering guarantees themselves or to the state machine that enforces
them. CI (the GHA build job) still performs every push and still calls
`BeginPublish`/`BeginPublishBatch`/`RecordArtifact` exactly as "The run log"
below describes. See `architecture/08-release-lifecycle/04-run-log.md`'s
updated framing and `ARCHITECTURE.md` design principle 5's clarifying note
for the full account.

**Updated by #979 (GitHub Release/notes/git-tag removal from v2).**
`release-v2.yml`'s `create-github-releases` job — which used to run
`create-combined-github-release-with-notes` at the end of every v2 release
run — is removed entirely (#980). No component of the v2/Temporal pipeline
creates a GitHub Release, generates release notes, or produces (or relies
on) a git tag GitHub would otherwise create as a Release side effect; v2's
version-of-record lives in App Registry, not in a git tag or a GitHub
Release. That job also made a second, independent `RecordArtifact` call per
released app/chart, keyed to `plan-release`'s pre-finalization guessed
version rather than `FinalizePublish`'s confirmed one — that call is
eliminated, not relocated: only `FinalizePublish`'s own
`BeginPublish`/`FailPublish`/`RecordArtifact` calls (via
`finalize-app`/`finalize-chart`, described in "The run log" section of
`04-run-log.md`) remain for v2. Separately, `FinalizePublish`'s
`finalize-app`/`finalize-chart` no longer pass `--create-git-tag` (#982):
`FinalizePublish` no longer performs a real git checkout/clone at all for
either shell-out — neither reads anything relative to a git working tree
(the app retag uses `--repository`/`--digest` directly; chart packaging
uses `--chart-dir`, an absolute path under the run's scratch temp dir), so
the plain `os.MkdirTemp` scratch directory `FinalizePublish` already
created for downloaded build artifacts now doubles as both commands' `cmd.Dir`,
replacing the former clone-per-invocation workspace. v1 (`release.yml`,
`release-app`/`release-charts`) is unaffected by any of this — it still
creates GitHub Releases, release notes, and git tags exactly as before.

## The problem: four cross-run orderings, three of them unenforced

A release run and a `main`-push reconcile are separate CI runs with no
ordering between them. Four orderings must hold for the registry to be
correct; only one is actually enforced.

| # | Required ordering | Enforced by | Failure |
|---|---|---|---|
| 1 | reconcile of commit `C` **before** `RecordArtifact` for an app introduced in `C` | nothing — accepted gap (#547) | exit 3, artifact never recorded and **never backfilled**; that build can never be promoted |
| 1v2 | reconcile of commit `C` **before** a v2 release dispatch builds against `C` | `app_build_log`'s current-pointer resolution (#923, FR8-FR12) — see below | fixed for the v2 dispatch path only; ordering 1's own v1 gap (`RecordArtifact` itself) is unchanged |
| 2 | newer reconcile **after** older reconcile | `reconcile_watermark` (#545) + CI concurrency (#546) | solved |
| 3 | `RecordArtifact(IMAGE, digest D)` **before** any chart pinning `D` | hard server-side reject (`postgres/artifact.go`, "chart pins unrecorded image digest") | chart artifact not recorded |
| 4 | app rows exist **before** a chart manifest referencing them resolves | `resolveChartApps` skips and reports the one bad chart (AR-7a, built) | solved — see below |

**Ordering 1v2 (#923):** the v1 gap in row 1 is about `RecordArtifact` racing
ahead of reconcile for an app introduced in the same commit. A related but
distinct gap existed on the v2 dispatch path: `DispatchBuild`
(`worker/release/activities.go`) used to dispatch `release-v2.yml` against
the literal `main` branch pointer, which can itself race ahead of whatever
commit `ci.yml`'s `reconcile-app-registry` job has actually finished
processing — the same failure class as ordering 1 (act against commit `C`
before `C` is reconciled), just at the dispatch-ref step instead of the
artifact-record step. Fixed by `app_build_log` (migration 019): `ci.yml`
writes one `app_build_log` row per discovered app/chart, unconditionally,
immediately after its reconcile step, so a row's presence proves reconcile
already saw that commit for that owner. `DispatchBuild` now resolves each
release target's build ref against `app_build_log`'s current-pointer row
(`buildref.go`'s `resolveDispatchRef`/`resolveBuildRef`), falling back to
the literal branch name only when no `app_build_log` row exists yet for a
target (a fresh environment, or an app added in a commit reconcile hasn't
processed). That resolved ref is **not** passed as
`GitHubDispatcher.Dispatch`'s `ref` parameter — GitHub's `workflow_dispatch`
REST API only accepts a branch or tag there and 422s ("No ref found for:
&lt;sha&gt;") on a raw commit SHA, even one that genuinely is a branch's
current tip (this broke every v2 dispatch in production once
`app_build_log` had current rows, until corrected). `Dispatch` is always
called against the plain branch (`GitHubDispatcherConfig.Ref`); the
resolved ref is instead forwarded as `release-v2.yml`'s `build_ref`
workflow input, which every job in that workflow checks out explicitly
(`actions/checkout@v4`'s `ref:`) instead of relying on the trigger ref. This
does not touch `release.yml` (v1) or ordering 1's own gap (`RecordArtifact`
racing reconcile) at all.

Ordering 3 is the expensive one, because charts pin digests resolved from
**GHCR by tag** (`docker buildx imagetools inspect ${IMG_REPO}:${IMG_VERSION}`
in `release.yml`), not from the current run's outputs — so any chart-only
release pins images published by earlier runs. One image that never got
recorded (it hit ordering 1, or recording was `continue-on-error` during a
registry blip) therefore breaks **every future chart release containing that
app**, permanently: re-running the chart release doesn't fix it (the digest is
still unrecorded), and reconcile doesn't fix it (reconcile writes identity,
never artifacts). It only clears when that app is rebuilt and recorded again.

Ordering 4 was the quiet one: one chart manifest naming a removed app, or an
ambiguous bare app name across two domains, used to roll back the whole
transaction, so the watermark never advanced and no app registered at all —
while the job stayed green because the step was `continue-on-error`. **Fixed
by AR-7a** (built): `resolveChartApps` now reports the bad chart in
`ReconcileAppsResponse.unresolved_charts` and skips only it; every other
app/chart in the same call still applies and the watermark still advances.
Chart manifests also carry a domain-qualified `app_refs` field now (see
"AssertApps (additive) vs. ReconcileApps (absence sweep)" below), so the
cross-domain-ambiguity half of this failure mode can no longer be produced by
`tools/helm` at all — only the deprecated bare-name compatibility path can
still hit it. And `ci.yml`'s `reconcile-app-registry` job no longer has
`continue-on-error`, so a genuine registry outage during the sweep is
visible immediately instead of hiding inside a green step.

## The principle that resolves it

**The registry is the source of truth for the pipeline. GHCR and the chart
repository remain the source of truth for artifacts.** Those are different
claims and keeping them apart is what makes the rest coherent:

- Nothing reads the registry to find out whether an image *exists* — the push
  is what makes it exist, and the digest the push returned is what gets
  recorded. A `published` row is proof-of-existence *at the instant it was
  written*, and deliberately not a live existence check (an image deleted from
  GHCR afterwards leaves a row that lies; a periodic verifier is a possible
  later addition, not a guarantee offered now).
- Because the artifacts themselves survive independently, a registry that is
  lost, restored from backup, or wrong can be repaired out of band. That path
  is disaster recovery ("shit is fucked"), not routine maintenance — see
  "Adoption and disaster recovery" below.

