# `AssertApps` (additive) vs. `ReconcileApps` (absence sweep)

**`AssertApps` is built (AR-7c, merged, #566).** The partial-apply and
domain-qualified-reference paragraphs below are AR-7a, built earlier; the
split this heading names is now real: `AppRegistry.AssertApps`
(`protos/api.proto`/`api_messages_app.proto`) exists alongside
`ReconcileApps`, implemented identically in `postgres/app.go`'s
`appRepo.AssertApps` and `fake/reconcile.go`'s `assertApps`. `release.yml`
calls it (via the new `.github/actions/app-registry-assert` composite
action) from a dedicated `app-registry-assert` job that runs once, ahead of
`Record build`/`Begin publish`, and that both the `release` matrix job and
`release-helm-charts` `needs:` — so every subsequent owner-resolving call in
either job succeeds. (Originally this ran as the first step inside each of
those jobs instead; issue #622 hoisted it to a single upfront job once the
per-job repetition — the exact same repo-wide discovery + RPC, once per
matrix leg plus once more for charts — showed up as wasted CI time.) See
"Release-vs-reconcile gap (issue #547)" above for the resulting status of
that gap.

`ReconcileApps` conflates two jobs: assert identity, and assert *absence*.
Only absence needs a canonical complete tree, and it is identity that releases
depend on. Split them:

| RPC | Runs from | Writes | Never does |
|---|---|---|---|
| `AssertApps` | any ref — `release.yml`, first step of a release | identity rows (`∅→ACTIVE`, `MISSING→ACTIVE` recovered) + manifest snapshots | mark anything `MISSING` |
| `ReconcileApps` | `main` push only, unchanged watermark | `MISSING` transitions, `chart_app` membership, `main` snapshots | assert identity from a non-canonical ref |

`AssertApps` against an `ARCHIVED` app is **rejected** per item — a human said
that app is gone for good, and a release resurrecting it silently is worse
than a red step. `RecordArtifact` against an `ARCHIVED` owner is likewise
rejected (today it succeeds, which is not intended).

**The sweep is partially-applying** (ordering 4, AR-7a, built): a chart whose
apps don't resolve is reported as unresolved in
`ReconcileAppsResponse.unresolved_charts` and skipped; every other app and
chart still applies and the watermark still advances. A skipped chart is
deliberately NOT marked `MISSING` as a side effect — it is present in the
manifest set, just unresolvable, and `ReconcileApps`'s absence sweep only
means to flag what is genuinely absent. Separately, chart manifests carry
**domain-qualified app references** (`ChartManifest.app_refs`, `"<domain>/
<name>"`, emitted by `helm_chart_metadata` in `tools/bazel/release.bzl`), so
`resolveChartApps`'s cross-domain ambiguity (`SELECT app_id FROM app WHERE
name = $1` with more than one match) cannot arise from anything `tools/helm`
produces — only the deprecated `ChartManifest.apps` (bare names) fallback,
kept for one release cycle's backward compatibility, can still hit it,
and it is now a per-chart skip there too, not a whole-sweep failure. Both were
small, independent of everything else here, and fixed a failure mode that was
silent — they landed first, as AR-7a.

