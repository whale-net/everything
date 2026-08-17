# Promotability

The rule the whole system hangs on. Each app declares its `deploy_unit` in its
`release_app` manifest; the registry derives artifact promotability from it.

| App/Chart `deploy_unit` | Image artifacts | Chart artifacts | Binary artifacts | Firmware |
|---|---|---|---|---|
| `chart` | `VIA_CHART` | `PROMOTABLE` | `NOT_PROMOTABLE` | `NOT_PROMOTABLE` |
| `image` | `PROMOTABLE` | n/a | `PROMOTABLE` | `NOT_PROMOTABLE` |
| `none` | `NOT_PROMOTABLE` | n/a | `PROMOTABLE` | `NOT_PROMOTABLE` |

**Binary artifacts are `PROMOTABLE` regardless of `ownerDeployUnit`** (see
`DerivePromotability` in `server/repository/promotability.go`). Tool binaries
(`release_helper_go`, the `app-registry` CLI) are deliberately packaged with
`DEPLOY_UNIT_NONE` to keep them out of Helm/K8s chart composition (#534/NFR-4)
— that isolation is enforced independently in `composer.go`, which ignores
binary artifacts regardless of promotability. "Promotable via `PromotionRegistry`"
and "deployable via Helm" are separate, unrelated concerns: binaries are
promotable so CI can resolve "what version of this tool is current in env X"
(see #780 — `download-release-tools` queries `PromotionRegistry` for this),
but that promotability never makes a binary eligible for chart composition.
Firmware stays `NOT_PROMOTABLE` unconditionally — it has no CI
version-resolution use case analogous to tool binaries.

**Override.** Promoting a `VIA_CHART` image directly is rejected unless the
caller passes `allow_override`. When allowed, the promotion is stored with
`is_override = true` and `GetEnvironmentState` reports it as a `DriftEntry`
against the chart's pinned digest. This makes the manmanv2-host-manager style
of hotfix possible without making it invisible.

**Why this is on the app, not the artifact:** it is a property of how the app is
deployed, which is declarative and belongs next to the code — the same place
`app_type` and `port` already live. The registry reads it; it does not own it.

## Required change to `release_app`

`tools/bazel/release.bzl` gains a `deploy_unit` attribute (default `"chart"`),
mirrored by `DeployUnit` in `//tools/appmeta/proto`. This is one of three
changes that touch existing code paths rather than being purely additive —
the others being the chart lockfile below and the manifest-schema
consolidation.

