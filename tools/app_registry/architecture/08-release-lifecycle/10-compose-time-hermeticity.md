# Compose-time chart hermeticity (AR-7f, issue #558)

**Built, and now live/enforced for every domain.** The reject in "Chart →
image lockfile" below ("a chart may not pin an unknown artifact") only fires
at *record* time — after the chart has already been packaged and pushed to
ChartMuseum. AR-7f moves the same rule earlier, to *compose* time. It
originally did this only for domains that had earned the stricter guarantee
via a per-domain `domain_adoption.stage = 'allocate'` gate; the AR-5 cutover
removed that gate, so the rule now applies unconditionally to every domain
(see "Relationship to AR-5" above and "Rejected alternatives" below for why
it was originally built per-domain rather than repo-wide, and PLAN.md's
"AR-5 — cutover status" for how the cutover landed).

**Where the check runs, and why that is hermetic.** `ArtifactRegistry`
gained one new read-only RPC, `CheckChartHermeticity(chart_domain, pins)`.
Server-side (`server/handlers/chart_hermeticity.go`) it looks up each pin
(`app_full_name`, `version`) via the same `Artifacts().GetArtifact` the
CLI's `artifacts get` already exposes, and reports a violation for anything
not found or not yet `ArtifactStatePublished`. It always computes real
violations and returns `enforced = true` — there is no longer a per-domain
branch that skips this and returns `enforced = false`.

The caller is `tools/release_helper_go`'s `build-helm-chart` command
(`cmd/chart_hermeticity.go`), called right after it resolves each member
app's release version and before it packages the chart or anything is
pushed. This is deliberately **not** inside `tools/helm/composer.go` or any
Bazel action: `composer.go` runs inside `bazel build` as part of
`build-helm-chart` a few lines earlier, with zero code changed and zero
registry access added — it still only bakes `AppMetadata.Version` (usually
the "latest" placeholder; see "Chart → image lockfile") into the compose-time
image lockfile, exactly as before AR-7f. `build-helm-chart` itself is a CLI
binary the release workflow invokes as its own step, outside any Bazel
action's sandbox — the same place `read-chart-lockfile` and the digest
resolution it feeds already make a registry call today (AR-2c). Putting the
new check there keeps the Bazel graph exactly as reproducible as it was:
no chart build result depends on network state, so nothing poisons the
remote cache and nothing breaks on a machine with no registry access.

**No-op posture.** `checkChartHermeticity` reads `APP_REGISTRY_CICD_OPT_IN`
directly and returns immediately — no dial, no RPC — unless it is exactly
`"true"`, the same bootstrap kill switch every other integration point in
this document already hangs on (see "`APP_REGISTRY_CICD_OPT_IN`" below).
That is what keeps `bazel build` — and `build-helm-chart` run without
`--use-released`, i.e. every contributor's laptop — untouched. A transport
or auth error talking to the registry (opt-in on, registry unreachable) is
also **not** fatal: it is logged as a warning and the build proceeds, the
same best-effort posture `release.yml`'s other App Registry steps have.
Only an actual `enforced = true` response naming violations fails the chart
build, and it fails naming every offending `app_full_name`/version pair.

**Shipped inert, now live.** For a period after AR-7f merged, no domain was
at adoption stage `allocate`, so `CheckChartHermeticity` always returned
`enforced = false` in every environment that existed — the same way AR-5a
shipped `AllocateVersion` fully implemented but unreachable. The AR-5
cutover is what changed that: `domain_adoption` is dropped, the per-domain
gate this section originally described is gone, and `CheckChartHermeticity`
now always computes real violations and returns `enforced = true` for every
domain, unconditionally, whenever the check runs at all (i.e. whenever
`APP_REGISTRY_CICD_OPT_IN=true`). The `enforced` field is kept on the wire
response for API stability even though it is now always `true` — it is not
being removed from the proto.
