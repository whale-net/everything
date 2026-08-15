# Release-vs-reconcile gap (issue #547)

> **Closed by AR-7c (merged, #566).** Issue #558 rejected "accept
> the gap" as the end state — see "Release lifecycle (issue #558)" →
> "AssertApps (additive) vs. ReconcileApps (absence sweep)" below.
> `release.yml` now calls `AssertApps` from a dedicated `app-registry-assert`
> job that runs once, before every job that later resolves an owner by full
> name (`release` and `release-helm-charts` both `needs:` it — hoisted out
> of each of those jobs by issue #622, which found the repo-wide discovery +
> RPC being repeated once per matrix leg plus once more for charts), so the
> window this section describes
> — a release reaching `RecordArtifact`'s owner lookup before that commit's
> `main`-push reconcile has run — can no longer produce
> `ReasonOwnerNotReconciled` / exit code 3. Everything below this callout
> describes the PRE-AR-7c mechanism (the `::warning::` annotation, exit
> code 3, the "wait and re-run" runbook) — kept for historical/rollback
> context (a domain that somehow skips the `AssertApps` step, e.g. a
> workflow YAML that hasn't been updated, still hits this exact path) and
> because the underlying RPC/exit-code machinery is unchanged, just
> unreachable through the normal path now. OPERATIONS.md's runbook entry
> for this has been retired accordingly.

`ReconcileApps` runs only from `ci.yml` on push to `main` (#543); `release.yml`
is a `workflow_dispatch` a human triggers, often immediately after merging —
this is normal usage, not an edge case. That decoupling opens a window: a
release can run, and reach `RecordArtifact`'s owner lookup
(`resolveOwner`), *before* the corresponding `main`-push reconcile for that
commit has finished, or even started. If the commit introduces a genuinely
new app/chart, `resolveOwner` fails exactly the way it does when reconcile
never ran at all (#539/#542/#548) — now reproducible per-release, not just
per-outage.

**Decision: accept the gap, make it loud instead of silent (issue #547's
options 3 + 4). Two other options were considered and rejected:**

- **Not chosen: a release-time provisional upsert (option 1).** Reintroduces
  a second write path to `app`/`chart` — exactly what the "App/chart
  registration" row below already rejected for the same reason. It also
  cannot be made safe against a `release.yml` ref that diverges from `main`
  (see the second-order case below): a provisional upsert from such a ref
  would write metadata (`deploy_unit`, `image_repository`, `bazel_label`,
  description, …) that's only "corrected" whenever the next `main`-push
  reconcile happens to run, with nothing surfacing the interim drift. Even
  scoped as "always superseded by the next real reconcile" (which the
  watermark from issue #545 would make *orderable*, not safe by itself), it
  is a second mechanism to reason about for a case that resolves itself
  within one `main`-push cycle anyway.
- **Not chosen: gate `release.yml` on `main`'s reconcile (option 2).** Avoids
  a second write path entirely, at the cost of every release blocking on
  `main` CI. The repo owner releases immediately after merging as normal
  practice and does not want releases blocking on `main` CI — this would
  turn a one-off recording miss into a mandatory wait on every release.

**What "accept the gap" means concretely:**

- Releases are never gated on `main`'s reconcile, in either direction —
  `release.yml` does not wait for it, and does not skip publishing anything
  because of it.
- Recording stays best-effort by design (see "Availability and bootstrap"
  above): a release that runs ahead of reconcile simply does not get that
  one artifact/build recorded. The image/chart still publishes normally;
  only the App Registry's record of it is missing.
- **Identity self-heals; the artifact record does not.** The next
  `main`-push reconcile registers the app/chart (an app going `MISSING` →
  reappearing → `ACTIVE` is automatic, see "Triage" below), so a *later*
  release for the same app records fine. But nothing re-records the specific
  build/artifact that failed to record the first time — `ReconcileApps`
  (`server/handlers/app.go`) only ever calls `repository.Registry.Apps().Reconcile`;
  it has no path to `RecordBuild`/`RecordArtifact`, so there is no mechanism
  by which a missed release-time record gets filled in after the fact. If
  that artifact matters (e.g. it needs to be promotable later), the fix is
  to re-run the release job once `main`'s reconcile has caught up, not to
  wait for anything automatic.
- CI makes this failure loud rather than silent: `RecordArtifact` returning
  the owner-not-reconciled case is classified as its own actionable
  `::warning::` (naming the app/chart, pointing at `ci.yml`'s
  `reconcile-app-registry` job, and saying "re-run this release after main's
  CI completes") distinct from a generic registry-error warning — see
  `.github/actions/app-registry-record-image/action.yml`,
  `.github/actions/app-registry-record-build/action.yml`, and `release.yml`'s
  inline chart-recording loop. The distinguishing signal is a structured
  gRPC status detail (`apierrors.ReasonOwnerNotReconciled`, set by
  `mapRepoErr` in `server/handlers/errors.go`) that the CLI (`cli/cmd/root.go`)
  turns into a distinct process exit code (3) — not a parse of the error
  message — so CI's classification is robust to message wording changing
  later. `tools/app_registry/OPERATIONS.md` documents what an operator does
  when they see it.

**Second-order case: a ref that never becomes part of `main`.**
`release.yml` can be dispatched against an arbitrary ref — a branch that
later gets rebased or squashed differently before merging, or an old tag for
a hotfix. If that ref's manifests differ from what's currently on `main`
(e.g. a `deploy_unit` or `bazel_label` change that gets reverted before
merge), there is today no mechanism by which anything from that release
would ever land in the registry, because `release.yml` never writes app
identity itself (see "App/chart registration" below) — only `main`-push
reconcile does, and it reconciles `main`'s current tree, never that ref's.
Concretely: the artifact/build for that specific run either recorded
successfully against whatever app/chart state existed on `main` at the time
(if the owner already existed and its identity metadata happened to still
match), or it didn't record at all (owner never existed) and, since it never
merges to `main` in that form, never becomes recordable later either. This
is arguably correct — nothing *should* write app identity from a
non-canonical ref — but it does mean "release from an unmerged/divergent
ref" cannot self-heal the way "release right after merging to `main`" can.
No machinery is planned for this; it is accepted as part of the same
tradeoff above, not a separate gap to close.

