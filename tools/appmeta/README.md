# appmeta — release manifest schema of record

Proto definition of the JSON emitted by the `app_metadata` and
`helm_chart_metadata` Starlark rules in [`//tools/bazel:release.bzl`](../bazel/release.bzl).

**Migrated.** `helm/composer.go` and `release_helper_go` both decode into
`appmetapb.AppManifest` / `appmetapb.ChartManifest`; no hand-written manifest
struct remains in `tools/`. See [Migration](#migration) and
`//tools/app_registry/PLAN.md` (phase AR-M).

## Why this exists

`release_app` manifests are the declarative source of truth for every app in the
monorepo, and the JSON they produce is a real interface between Starlark and Go.
That interface had no schema, so each consumer wrote its own struct — and they
drifted:

| Field | `release.bzl` emitted | `release_helper_go` (before) | `helm/composer.go` (before) |
|---|:---:|:---:|:---:|
| `description`, `app_type`, `port`, `replicas` | ✅ | ❌ | ✅ |
| `health_check`, `ingress`, `resources`, `command`, `args` | ✅ | ❌ | ✅ |
| `version` | ✅ | ❌ | ✅ |
| `binary_target`, `openapi_spec_target` | ✅ | ✅ | ❌ |
| `labels`, `annotations`, `dependencies` | ❌ | ❌ | ✅ phantom (removed separately) |

Charts drifted the same way: `HelmChartMetadata` in `plan_helm.go` omitted
`version` and `environment`, both of which `helm_chart_metadata` emits.

The missing `version` field cost something real: `tools/release_helper_go/cmd/plan.go`
stored the release version in the `Language` field and read it back with
`strings.HasPrefix(app.Language, "v")`, because the struct it decoded into had
nowhere else to put it. Both consumers now decode `appmetapb.AppManifest` /
`appmetapb.ChartManifest` directly — see [Migration](#migration) — so this
table describes the problem this package fixed, not the current state.

Adding the app registry as a third consumer would have made this worse. Instead
the registry is the forcing function that collapsed it to one definition.

## Dependency direction

```
              //tools/appmeta/proto:appmetapb
                 (schema of record)
                  ^      ^       ^
                  |      |       |
      release_helper_go  |   app_registry
                    helm/composer
```

`appmeta` depends on nothing. In particular the schema must **not** live inside
`app_registry`, or the helm composer would depend on the registry to build a
chart.

## Wire format

The Starlark rules produce plain `snake_case` JSON, delivered two ways from the
same `metadata` dict:

- the `*_metadata.json` file output (`DefaultInfo`), and
- the `AppMetadataInfo` / `HelmChartMetadataInfo` Starlark providers, which
  `release_helper_go` reads via `bazel cquery --output=starlark` with
  `json.encode(...metadata)` — the discovery path since #444, and the fast one,
  since it runs no actions.

Both carry identical bytes, so one schema covers both.

`protojson` accepts proto field names as written, so **no change to the emitted
format is required** — existing manifests parse as-is.

Always decode with:

```go
protojson.UnmarshalOptions{DiscardUnknown: false}
```

`DiscardUnknown: false` is the whole point. Any key a Starlark rule emits that
has no field in `appmeta.proto` becomes a hard error instead of silently
vanishing — which is exactly how the current drift went unnoticed.

## The contract test

Shared types alone do not prevent drift; a rule attribute can still be added
without anyone extending the proto. The enforcement is
`//tools/appmeta:manifest_contract_test`, in both directions:

1. **rule → proto** (`TestAllManifestsDecodeAgainstProto`): discover every
   `app_metadata` / `helm_chart_metadata` target the same *shape* of query
   `release_helper_go` uses for release discovery — `bazel query` for labels,
   then `bazel cquery --output=starlark` over the providers — and unmarshal
   each result with `DiscardUnknown: false`. Unlike `release_helper_go`'s
   `ListAllApps`/`ListAllHelmCharts`, this query does **not** exclude
   `testonly` targets: it needs to validate the fixture below, not release it.
2. **proto → rule** (`TestFixtureLeavesNoFieldUnset` /
   `TestChartFixtureLeavesNoFieldUnset`): the fixtures in `testdata/` call
   `app_metadata` / `helm_chart_metadata` directly (not the `release_app` /
   `release_helm_chart` macros) so they create no image/helm_chart targets,
   and are marked `testonly = True` so `ListAllApps`/`ListAllHelmCharts`
   exclude them from `plan --apps all` — a test fixture must never become a
   real release. Each still sets every attribute on its rule (the chart
   fixture composes the app fixture, so `apps`/`app_refs` are non-empty
   too); assert the decoded message has no unset field, to catch a field
   defined here that the rule never populates.

Direction 2 is fully hermetic — it just reads the fixture's built metadata
JSON as a `data` dependency, so it runs under a normal `bazel test`.

Direction 1 needs a real `bazel query`/`cquery` against the whole checkout,
which cannot run hermetically inside a sandboxed `bazel test` action: an
action only sees its declared inputs, not the full source tree, and even
with sandboxing disabled the test binary's working directory sits under
Bazel's execroot/cache tree, which is not an ancestor of the real checkout —
there is no path to walk up to find it. The one thing that does carry the
real checkout path into a subprocess is `BUILD_WORKSPACE_DIRECTORY`, and
Bazel only sets that for `bazel run`. So this target is tagged `manual`
(same idiom as `//tools/scripts:test_cross_compilation`, excluded from
`//tools/...` and `//...` wildcards) and must be invoked as:

```
bazel run //tools/appmeta:manifest_contract_test
```

`bazel test //tools/appmeta:manifest_contract_test` still runs and still
exercises direction 2; direction 1 skips with an explanatory message rather
than silently passing. CI invokes the target with `run`, not `test`, so
direction 1 still executes on every build.

Verified by deliberately adding an undeclared key to the metadata dict in
`_app_metadata_impl` and confirming `TestAllManifestsDecodeAgainstProto`
fails for every discovered target with `unknown field "<name>"`, then
reverting.

## Adding a field

Three edits, and the contract test fails until all three are done:

1. Add the attr to `app_metadata` (or `helm_chart_metadata`) in `release.bzl`
   and emit it.
2. Add the field to `AppManifest` (or `ChartManifest`) in `appmeta.proto`.
3. Extend the corresponding fixture in `testdata/`.

`ChartManifest.app_refs` (AR-7a) is a worked example: the attr
(`helm_chart_metadata`'s `apps` label_list, reading each app's `domain` off
`AppMetadataInfo`), the proto field, and `fixture-helm_chart_metadata`'s
composed app all went in together. `ChartManifest.apps` (bare app names) is
now deprecated in favor of it — see the proto's doc comment and
`//tools/app_registry/PLAN.md`'s AR-7a section.

## Migration

Sequenced as phase AR-M in `//tools/app_registry/PLAN.md`, ahead of AR-2 so the
registry doesn't add a third manifest representation on top of two that
already disagreed.

**Done:**

1. `appmeta.proto` and `//tools/appmeta:manifest_contract_test` against
   today's rule output.
2. `helm/composer.go`'s `AppMetadata` replaced with `appmetapb.AppManifest`
   (`LoadMetadata` decodes with `protojson` instead of `encoding/json`;
   `GetImage`/`GetImageTag` became package functions since a proto type can't
   carry methods). Verified byte-identical output for
   `//demo:all_types_chart`, `//demo:fastapi_chart`, and
   `//manmanv2:manmanv2_chart` before/after, plus a golden-output regression
   test (`TestGenerateChart_Golden` in `composer_test.go`).
3. `release_helper_go`'s `AppMetadata` / `HelmChartMetadata` replaced with
   thin wrappers around `appmetapb.AppManifest` / `appmetapb.ChartManifest`
   plus the discovery-time `BazelTarget` label (not part of the manifest
   JSON). The **`Language`-as-version hack** in `plan.go` is removed —
   `assignVersions` now records per-app versions into an explicit map instead
   of overloading the `Language` field with a `"v"`-prefixed sentinel.
4. `deploy_unit` added to `release_app` and to the proto, mapped from the
   Starlark attr string (`"chart"` / `"image"` / `"none"`) to the
   `DeployUnit` enum's JSON name so `protojson` decodes it directly.
   `manmanv2-host-manager` is the only app set to `"image"` — it runs on bare
   metal with Docker socket access and is explicitly documented as not
   deployed via the control-services Helm chart (see
   `manmanv2/host/DEPLOYMENT.md`). Every other app keeps the `"chart"`
   default.

5. `release_helper_go manifest-set` emits `AppManifestSet` (apps + charts +
   `git_sha` + `discovered_at`) via `protojson`, so enums serialize as names.
   This is what AR-2c's registry recording step passes to `ReconcileApps`
   directly — no conversion code, since discovery already decodes into
   `appmetapb` types (AR-2b).
