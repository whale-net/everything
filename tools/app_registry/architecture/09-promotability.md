# Promotability

The rule the whole system hangs on. Each app declares its `deploy_unit` in its
`release_app` manifest; the registry derives artifact promotability from it.

| App/Chart `deploy_unit` | Image artifacts | Chart artifacts | Binary artifacts | Firmware |
|---|---|---|---|---|
| `chart` | `VIA_CHART` | `PROMOTABLE` | `NOT_PROMOTABLE` | `NOT_PROMOTABLE` |
| `image` | `PROMOTABLE` | n/a | `PROMOTABLE` | `NOT_PROMOTABLE` |
| `binary` | n/a | n/a | `PROMOTABLE` | `NOT_PROMOTABLE` |
| `none` | `NOT_PROMOTABLE` | n/a | `PROMOTABLE` | `NOT_PROMOTABLE` |

**Binary artifacts are `PROMOTABLE` regardless of `ownerDeployUnit`** (see
`DerivePromotability` in `server/repository/promotability.go`). Tool binaries
(`release_helper_go`, the `app-registry` CLI) are packaged with
`DEPLOY_UNIT_BINARY` — distributed standalone via S3, with no Bazel image
target (`release.bzl`'s `release_app` macro never builds one for `cli`/
`binary` `app_type`) — and deliberately kept out of Helm/K8s chart
composition (#534/NFR-4) — that isolation is enforced independently in
`composer.go`, which ignores binary artifacts regardless of promotability
or `deploy_unit`. "Promotable via `PromotionRegistry`"
and "deployable via Helm" are separate, unrelated concerns: binaries are
promotable so CI can resolve "what version of this tool is current in env X"
(see #780 — `download-release-tools` queries `PromotionRegistry` for this),
but that promotability never makes a binary eligible for chart composition.
Firmware stays `NOT_PROMOTABLE` unconditionally — it has no CI
version-resolution use case analogous to tool binaries.

**Derived live, not stored (issue #833).** Every read (`GetArtifact`,
`ListArtifacts`, `ResolveArtifact`, ...) computes `Promotability` fresh, by
joining the artifact's owning app/chart to its CURRENT `deploy_unit` and
calling `DerivePromotability` at that moment — `postgres/artifact.go`'s
`scanArtifact`, mirrored by `fake.Registry`'s `livePromotability`. There is no
`artifact.promotability` column; `Promotability` is not part of the artifact
row at all, on either backend. This means editing an app's `deploy_unit`, or
fixing a bug in `DerivePromotability` itself (e.g. #810), changes what is read
back for an artifact published before the edit/fix. A brief phase (AR-7c,
migration 008) instead stored this value once at publish time specifically to
prevent that; #833 reverses it after that tradeoff proved worse in practice —
see `architecture/08-release-lifecycle/02-manifest-snapshot.md` "As built
(issue #833, migration 014)" for the full history and reasoning.

**Override.** Promoting a `VIA_CHART` image directly is rejected unless the
caller passes `allow_override`. When allowed, the promotion is stored with
`is_override = true` and `GetEnvironmentState` reports it as a `DriftEntry`
against the chart's pinned digest. This makes the manmanv2-host-manager style
of hotfix possible without making it invisible.

**Why this is on the app, not the artifact:** it is a property of how the app is
deployed, which is declarative and belongs next to the code — the same place
`app_type` and `port` already live. The registry reads it; it does not own it.

**Identifying a binary on the wire (#780).** `Artifact.app_id` /
`Artifact.chart_id` (and `Promotion.app_id` / `Promotion.chart_id`) are
"exactly one is set, depending on kind" — `app_id` for every kind except
CHART, not just IMAGE. `handlers/convert.go`'s `artifactToPB`/`promotionToPB`
used to branch on `Kind == IMAGE`, which left BINARY (and FIRMWARE) artifacts
with *both* fields empty on the wire even though `RecordArtifact` always
resolves `owner_full_name` to an `AppID` for non-CHART kinds server-side. A
caller reading `GetEnvironmentState`'s `entries[].artifact.app_id` (e.g. CI
resolving "what version of `tools-release_helper_go` is current in dev")
therefore has a real, populated identifier today — match it against
`AppRegistry.GetApp`'s `full_name` (`<domain>-<name>`, e.g.
`tools-release_helper_go` / `tools-app_registry_cli`), not against
`repository` (identical for every artifact published from this repo) or
`version`/`digest` alone (identify the artifact, not which tool it is).

**Promoting a binary.** Promotion is 100% human-triggered via `promote.yml`
for every artifact kind — there is no auto-promotion anywhere today, not even
to dev, and `promote.yml` itself is currently disabled (`if: false`) pending
live Keycloak promoter clients. `release.yml`'s tool-binary publish step only
holds `app-registry-builder-*` credentials, which cannot call `Promote`
(`RequirePromoter` has no builder fallback for any environment), so it
deliberately stops at `RecordArtifact`. `promote.yml`'s `kind` input accepts
`binary` alongside `image`/`chart` so tool binaries promote through that same
reviewed path once it's re-enabled.

## Required change to `release_app`

`tools/bazel/release.bzl` gains a `deploy_unit` attribute (default `"chart"`),
mirrored by `DeployUnit` in `//tools/appmeta/proto`. This is one of three
changes that touch existing code paths rather than being purely additive —
the others being the chart lockfile below and the manifest-schema
consolidation.

