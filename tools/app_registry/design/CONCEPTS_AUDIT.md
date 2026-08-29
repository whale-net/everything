# Concepts Audit — Wireframe vs. Documented Registry Capability

The wireframe in `design/wireframes/` invents fake data freely, but a few
screens quietly assume a *capability* — an RPC, a query, a stored fact — that
either doesn't exist yet, isn't part of this service's API surface at all, or
is explicitly called out in ARCHITECTURE.md/OPERATIONS.md as not-yet-real.
This document lists every one found, checked against the actual proto surface
(`protos/api.proto`, full `rpc` list below) and the architecture/operations
docs — not guessed from the wireframe's own copy.

```
$ grep -n "rpc " protos/api.proto
```
gives the complete, current RPC surface: `ReconcileApps`, `AssertApps`,
`ListApps`, `GetApp`, `ListCharts`, `SetAppStatus`, `RecordBuild`,
`RecordArtifact`, `BeginPublish`, `FailPublish`, `BeginPublishBatch`,
`ListArtifacts`, `GetArtifact`, `GetReleaseRun`, `ResolveArtifact`,
`AllocateVersion`, `AdoptArtifact`, `CheckChartHermeticity`, `Promote`,
`Rollback`, `GetEnvironmentState`, `ListPromotions`, `ListPromotionEvents`,
`UpsertEnvironment`, `GetEnvironment`, `ListEnvironments`,
`ArchiveEnvironment`. Anything a screen needs that isn't derivable from this
list is flagged below.

## Confirmed gaps — no backing RPC/CLI today

### 1. "List builds/runs" — `30-builds.html`

The Builds screen shows a table of *several* recent CI runs, each with a
run id, commit, recording-health badge, and artifact count. There is no
`ListBuilds`/`ListReleaseRuns` RPC — `GetReleaseRun(workflow_run_id[,
attempt])` and its CLI form, `app-registry builds status <workflow-run-id>`,
both take exactly **one** run id. Nothing in the API lets a caller ask "what
are the recent runs" — the wireframe's run-search table has no capability to
call today. An admin would have to already know the run id (from a GitHub
Actions link) before this screen becomes queryable at all; the table of
several rows as drawn implies a listing capability that doesn't exist.

### 2. "Reconcile Runs" list — `32-reconcile-runs.html`

Same shape of gap. AR-8 (#592) adds `reconcile_run`, one row per sweep, but
there is no RPC exposing it — `ReconcileApps` *writes* a sweep, nothing
*reads* the sweep history back. `apps reconcile` in the CLI triggers a
reconcile; it does not list past ones. This screen is entirely speculative
against today's API.

### 3. Reverse pin lookup ("which charts pin this image") — `21-artifact-detail.html`, `11-app-detail.html`

Checked directly: `Artifact.contains` and `ResolveArtifact` both walk
**one direction only** — chart → the image digests it pins
(`ResolveArtifactRequest`'s own doc comment: "walks a chart artifact down to
the concrete image digests it pins"). There is no reverse query (image →
charts that pin it). Two wireframe elements assume this exists:
- Artifact-detail's "Pinned by" card (a chart-kind artifacts pinning this
  image, with the version mismatch/drift note).
- App-detail's "part of chart" link, which requires knowing which chart (if
  any) currently composes a given `VIA_CHART` app.

Note the wireframe's own `21-artifact-detail.html` note already flags the
one-directional nature of `ResolveArtifact` — this audit confirms that note
was right, and that "Pinned by" has no query to back it, not just an
awkward one.

### 4. "Recording health" as a queryable verdict — `30-builds.html`, `31-build-detail.html`, dashboard stat tile

Confirmed via `.github/workflows/release.yml`: **"App Registry recording
health" is a GitHub Actions job step**, not a registry concept. It checks
each App Registry CI step's own `continue-on-error` outcome within that
workflow run and fails the step if any did — that computation happens
entirely inside GitHub Actions, over GitHub Actions' own step-outcome data.
The registry API has no RPC that returns "was this run's recording
healthy" as a verdict; `GetReleaseRun`/`builds status` returns only
per-artifact state (`PUBLISHING`/`PUBLISHED`/`FAILED`/`ALLOCATED`).

A "Healthy"/"Failed" badge is *derivable* client-side from those per-artifact
states (all `PUBLISHED` ⇒ healthy) — that part is fine. **"Skipped (opt-in
off)" is not derivable at all**: per OPERATIONS.md, a run with
`APP_REGISTRY_CICD_OPT_IN` off produces **zero artifact rows**, which is
indistinguishable, from the registry's own data, from any other reason
nothing got recorded. Telling those apart needs GitHub Actions' own run/job
data (were the App Registry steps skipped, or did they run and fail) — a
different system this UI has no documented integration with.

## Documented as schema-only / explicitly not enforced

### 5. Approval gate — `09-environments.html`, `53-environment-form.html`

Already called out inline in both screens (a wf-note and an in-form alert),
included here for completeness: `environment.requires_approval`,
`PromotionState.PENDING_APPROVAL`, and `PromotionAction.APPROVE`/`REJECT`
all exist in the schema (confirmed: the CLI's `env upsert --requires-approval`
flag is real and wired to `UpsertEnvironmentRequest`), but ARCHITECTURE.md
states plainly this is "deliberately unimplemented" — `Promote` does not
check it. The wireframe shows the field but labels it inert rather than
pretending it gates anything today. This is not a gap the wireframe missed;
it's a real field with no enforcement yet, and the screens say so.

## Out of this API's scope entirely (different system)

### 6. `APP_REGISTRY_CICD_OPT_IN` visibility

Nowhere in the wireframe today, but worth recording since it came up in
design discussion: this is a **GitHub repository variable** read directly by
CI workflows, not a registry-stored value. No RPC could expose "is opt-in
on" without this UI also integrating with the GitHub Actions API — a
materially different capability than anything else here.

### 7. `domain_adoption.stage` visibility ("is this domain actually in use") — RESOLVED, moot

Also not present in any screen. This was flagged because surfacing "is
domain X at `observe`/`promote`/`allocate`" anywhere in this UI would have
needed a new RPC that didn't exist. The question is now moot: `domain_adoption`
is dropped, and every domain is unconditionally allocated — there is no
per-domain stage left to surface, so this gap cannot recur.

## UI-imposed policy, not a server-side constraint

### 8. Dry-run-before-real-promote gating — `50-promote.html`

`Promote`'s `dry_run` field is real; the API does not require a dry run
before a real promotion, and nothing server-side would reject one submitted
without a prior dry run. The wireframe's "Promote for real" stays disabled
until a dry run has been run against the current form state is a **UI
workflow policy**, matching OPERATIONS.md's advisory ("prefer dry_run: true
first"), not an enforced rule. Worth keeping, but worth knowing it lives
entirely in the UI layer if this is ever built for real — a direct
`app-registry promote` CLI call skips it entirely today, and would continue
to.

### 9. Chart-level "drift" badge — `10-environment-status.html`, `22-chart-detail.html`

`DriftEntry` (per `status`'s output) is reported per *promoted target* — an
individual overridden artifact — not aggregated to "this chart has drift."
The matrix's chart-row drift badge is a client-side rollup ("does any
composed app underneath show drift") over real per-entry data, not a field
any RPC returns directly. Fine to compute this way; just not literally what
the API hands back.

## Lower-confidence / worth a second look before building

### 10. `chart_app` (live declaration) vs. `artifact_link` (frozen at build time) — `22-chart-detail.html`

ARCHITECTURE.md ("Resolved questions" #4) is explicit that these answer
different questions and must not be confused: `chart_app` is the chart's
*current, reconciled* composition (informational only, never used in
promotion rendering); `artifact_link` is what one specific, already-recorded
chart *artifact* actually pinned at build time, and never changes
afterward. Chart-detail's "Apps published in leaflab-edge v0.9.1 (stage)"
table is written to use the latter (correct, per the docs' own recommended
usage), but nothing on the screen visually distinguishes it from "what does
this chart declare *today*" (`chart_app`, `ListCharts`' `app_ids`) — a real
implementation should make sure it never conflates the two, since they can
legitimately disagree (a chart's declared composition can change between
when an old promoted version was built and now).

### 11. Role-aware UI (permission-denied / disabled-by-role states)

Not a single-screen finding — a structural one. ARCHITECTURE.md's
"Authorization" section documents five flat, non-implying roles
(`app-registry-builder`, three per-environment `app-registry-promoter-*`,
`app-registry-admin`), and states the constraint is deliberately strict: a
promoter role for one environment does not imply another. Nothing in this
wireframe shows what an admin sees when they hold, say,
`app-registry-promoter-dev` but not `-stage`/`-prod` — every screen assumes
the viewer can do everything shown. A real build needs disabled/hidden
states per environment column driven by the caller's actual roles, especially
given `10-environment-status.html`'s whole redesign is about making the
*environment* explicit at the point of action — role gating is the other
half of "can this admin actually do this, here."

## Not a gap — reuse limitations already noted inline

Several screens reuse one static fragment to represent multiple conceptual
entities (e.g. `leaflab-migrate` and `manmanv2-worker` both render as
whatever `app-detail`'s single fragment currently shows). That's a wireframe
tooling limitation, already called out via `wf-note`s at the relevant
screens, not a registry-capability gap — omitted from the numbered list
above to keep it focused on capability, not on wireframe mechanics.
