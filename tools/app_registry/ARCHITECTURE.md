# App Registry — Architecture

Design record for `//tools/app_registry`. Read [README.md](README.md) first for
what the system is and the end-to-end flows.

This doc is split into one file per topic under [`architecture/`](architecture/)
— every file there is real, current design; nothing there is history (see
[PLAN-HISTORY.md](PLAN-HISTORY.md) for phase-by-phase as-built narrative
instead). Jump straight to the file you need rather than reading serially.
Filenames are numbered in the order a first-time reader would want them, but
the number is not load-bearing — grep the table below, or `architecture/`
itself, for the topic you need.

| File | Read it for |
|---|---|
| Design principles (below, this file) | The five rules everything else in this doc follows |
| [`architecture/02-shared-manifest-schema.md`](architecture/02-shared-manifest-schema.md) | Why `AppManifest`/`ChartManifest` live in `appmeta`, not here |
| [`architecture/03-data-model.md`](architecture/03-data-model.md) | The schema, table by table; SCD2 read/write pattern on `promotion` |
| [`architecture/04-version-model.md`](architecture/04-version-model.md) | How `AllocateVersion` orders, reserves, and hands out versions for every release |
| [`architecture/05-reconcile-watermark-issue-545.md`](architecture/05-reconcile-watermark-issue-545.md) | Why `Reconcile` can safely skip a stale/out-of-order call |
| [`architecture/06-list-reconcile-runs-issue-607.md`](architecture/06-list-reconcile-runs-issue-607.md) | Real (LIMIT + keyset cursor) pagination over `reconcile_run`, and why it's unindexed |
| [`architecture/07-release-reconcile-gap-issue-547.md`](architecture/07-release-reconcile-gap-issue-547.md) | Superseded by AR-7c — kept for historical/rollback context only |
| [`architecture/08-release-lifecycle/`](architecture/08-release-lifecycle/) | The big one: artifact `allocated → publishing → published`, identity/manifest-snapshot split, the run log — start here for anything touching recording or `release.yml`. Itself split further — see its own index below |
| [`architecture/09-promotability.md`](architecture/09-promotability.md) | What makes an artifact legal to promote |
| [`architecture/10-chart-image-lockfile.md`](architecture/10-chart-image-lockfile.md) | Compose-time vs. publish-time digest pinning |
| [`architecture/11-list-artifact-pins-issue-609.md`](architecture/11-list-artifact-pins-issue-609.md) | `ResolveArtifact`'s reverse walk — which charts pin a given image; deliberately unpaginated |
| [`architecture/12-writeback-outbox-temporal.md`](architecture/12-writeback-outbox-temporal.md) | How a promotion reaches the gitops repo (stub today) |
| [`architecture/13-authorization.md`](architecture/13-authorization.md) | The role model and where environment scoping actually comes from |
| [`architecture/14-idempotency.md`](architecture/14-idempotency.md) | Key scoping, including the cross-method replay bug fixed in #575/#576 |
| [`architecture/15-triage-missing-archived.md`](architecture/15-triage-missing-archived.md) | What each app/chart status means and how it transitions |
| [`architecture/16-availability-and-bootstrap.md`](architecture/16-availability-and-bootstrap.md) | `APP_REGISTRY_CICD_OPT_IN`, version skew, and what breaks silently vs. loudly |
| [`architecture/17-rejected-alternatives.md`](architecture/17-rejected-alternatives.md) | Designs considered and why they lost |
| [`architecture/18-future-approval-gate.md`](architecture/18-future-approval-gate.md) | Not built — `PENDING_APPROVAL` exists in the schema only |
| [`architecture/19-resolved-questions.md`](architecture/19-resolved-questions.md) | Numbered Q&A cited by number elsewhere in this doc and in PLAN.md |
| [`architecture/20-open-questions.md`](architecture/20-open-questions.md) | What's still genuinely undecided |

`architecture/08-release-lifecycle/` is itself split — the parent topic alone
was too large for one file:

| File | Read it for |
|---|---|
| [`00-overview.md`](architecture/08-release-lifecycle/00-overview.md) | Status, the four cross-run orderings problem, and the principle that resolves it |
| [`01-artifact-lifecycle.md`](architecture/08-release-lifecycle/01-artifact-lifecycle.md) | `allocated → publishing → published` state machine |
| [`02-manifest-snapshot.md`](architecture/08-release-lifecycle/02-manifest-snapshot.md) | App identity vs. per-build manifest snapshot |
| [`03-assert-vs-reconcile.md`](architecture/08-release-lifecycle/03-assert-vs-reconcile.md) | `AssertApps` (additive) vs. `ReconcileApps` (absence sweep) |
| [`04-run-log.md`](architecture/08-release-lifecycle/04-run-log.md) | Temporal orchestrates (UI-triggered releases, #889), CI still pushes, the registry records; `BeginPublishBatch`, `GetReleaseRun` |
| [`05-list-builds-issue-608.md`](architecture/08-release-lifecycle/05-list-builds-issue-608.md) | Real pagination over `build` |
| [`06-pagination-issue-603.md`](architecture/08-release-lifecycle/06-pagination-issue-603.md) | Real pagination for `ListPromotionEvents`/`ListArtifacts`/`ListPromotions` |
| [`08-adoption-disaster-recovery.md`](architecture/08-release-lifecycle/08-adoption-disaster-recovery.md) | `AdoptArtifact` and the disaster-recovery path |
| [`10-compose-time-hermeticity.md`](architecture/08-release-lifecycle/10-compose-time-hermeticity.md) | `CheckChartHermeticity`, AR-7f |
| [`11-rejected-alternatives.md`](architecture/08-release-lifecycle/11-rejected-alternatives.md) | Designs considered and why they lost, scoped to issue #558 |

## Design principles

1. **Manifests stay authoritative.** The registry never invents app metadata.
   It ingests `release_app` manifests via `bazel query` and reconciles. If the
   registry and the manifests disagree, the manifests win.
2. **Digests are identity; tags are labels.** Every artifact is stored by
   `sha256:` digest. Semver tags are recorded for humans and can move.
3. **The API is the write path; git is the delivery path.** Deployment tooling
   reads state from the gitops repo (or an S3 snapshot), never synchronously
   from the API. The registry being down must not block a deploy.
4. **Additive before authoritative.** The registry observes releases for a full
   phase before it is allowed to allocate versions. Git tags remain a redundant
   record permanently — they cost nothing and are the disaster-recovery path.
5. **Record, don't act.** The registry mutates rows and emits writeback intents.
   It never touches a cluster.
   **Clarifying note (#886/#889, App Registry v2 release job):** this
   principle constrains the registry/gRPC server component specifically, not
   every actor in this system. The Temporal `ReleaseWorkflow` introduced by
   #889 is a distinct actor — the `app-registry-worker` binary, the same one
   that already runs `WritebackWorkflow` — and it *does* orchestrate: it
   drives a UI-triggered release's trigger→build→publish→record saga end to
   end, invoking CI (`release.yml`) and polling it to completion. That does
   not weaken principle 5: the registry/server itself still only mutates rows
   and emits intents, and still never touches a cluster or calls out to
   GitHub Actions directly. See
   [`architecture/08-release-lifecycle/04-run-log.md`](architecture/08-release-lifecycle/04-run-log.md)
   and
   [`architecture/08-release-lifecycle/11-rejected-alternatives.md`](architecture/08-release-lifecycle/11-rejected-alternatives.md)
   for the full account of what changed and why the original "Record, don't
   act" rejection of an inbound Temporal workflow (#558-scoped) doesn't apply
   to what #889 actually shipped.
