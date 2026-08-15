# Compose-time chart hermeticity (AR-7f, issue #558)

**Built.** The reject in "Chart → image lockfile" below ("a chart may not
pin an unknown artifact") only fires at *record* time — after the chart has
already been packaged and pushed to ChartMuseum. AR-7f moves the same rule
earlier, to *compose* time, for domains that have earned the stricter
guarantee: `domain_adoption.stage = 'allocate'` (see "Availability, restated
per adoption stage" above — the same per-domain gate every other AR-7
tightening in this document uses, and "Rejected alternatives" below for why
it is per-domain and not repo-wide).

**Where the check runs, and why that is hermetic.** `ArtifactRegistry`
gained one new read-only RPC, `CheckChartHermeticity(chart_domain, pins)`.
Server-side (`server/handlers/chart_hermeticity.go`) it reads
`chart_domain`'s adoption stage; at anything other than `allocate` it
returns `enforced = false` and does no further work. At `allocate` it looks
up each pin (`app_full_name`, `version`) via the same `Artifacts().
GetArtifact` the CLI's `artifacts get` already exposes, and reports a
violation for anything not found or not yet `ArtifactStatePublished`.

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
same best-effort posture release.yml's other App Registry steps have at
adoption stage `observe`/`promote`. Only an actual `enforced = true`
response naming violations fails the chart build, and it fails naming every
offending `app_full_name`/version pair.

**Ships inert.** No domain is at adoption stage `allocate` today (see
"Relationship to AR-5" above) — `CheckChartHermeticity` always returns
`enforced = false` in every environment that exists right now, the same way
AR-5a shipped `AllocateVersion` fully implemented but unreachable. It starts
to bite the first time a domain's `domain_adoption.stage` row is moved to
`allocate`, which is a separate, explicit operational action, not part of
this change.

