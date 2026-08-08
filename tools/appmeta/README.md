# appmeta — release manifest schema of record

Proto definition of the JSON emitted by the `app_metadata` and
`helm_chart_metadata` Starlark rules in [`//tools/bazel:release.bzl`](../bazel/release.bzl).

**Design stage.** The proto exists; consumers have not been migrated yet. See
[Migration](#migration) and `//tools/app_registry/PLAN.md` (phase AR-2).

## Why this exists

`release_app` manifests are the declarative source of truth for every app in the
monorepo, and the JSON they produce is a real interface between Starlark and Go.
That interface had no schema, so each consumer wrote its own struct — and they
drifted:

| Field | `release.bzl` emits | `release_helper_go` | `helm/composer.go` |
|---|:---:|:---:|:---:|
| `description`, `app_type`, `port`, `replicas` | ✅ | ❌ | ✅ |
| `health_check`, `ingress`, `resources`, `command`, `args` | ✅ | ❌ | ✅ |
| `version` | ✅ | ❌ | ✅ |
| `binary_target`, `openapi_spec_target` | ✅ | ✅ | ❌ |
| `labels`, `annotations`, `dependencies` | ❌ | ❌ | ✅ phantom |

The missing `version` field has already cost something real:
`tools/release_helper_go/cmd/plan.go` stores the release version in the
`Language` field and reads it back with `strings.HasPrefix(app.Language, "v")`,
because the struct it decodes into has nowhere else to put it.

Adding the app registry as a third consumer would have made this worse. Instead
the registry is the forcing function to collapse to one definition.

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

The Starlark rules write plain `snake_case` JSON. `protojson` accepts proto
field names as written, so **no change to the emitted format is required** —
existing manifests parse as-is.

Always decode with:

```go
protojson.UnmarshalOptions{DiscardUnknown: false}
```

`DiscardUnknown: false` is the whole point. Any key a Starlark rule emits that
has no field in `appmeta.proto` becomes a hard error instead of silently
vanishing — which is exactly how the current drift went unnoticed.

## The contract test

Shared types alone do not prevent drift; a rule attribute can still be added
without anyone extending the proto. The enforcement is a test:

1. `bazel query 'kind(app_metadata, //...)'`, build every target, and unmarshal
   each output with `DiscardUnknown: false`. Catches **rule → proto** drift.
2. A fixture app in `testdata/` sets *every* `release_app` attribute; assert the
   decoded message has no unset field. Catches **proto → rule** drift, i.e. a
   field defined here that the rule never populates.

Both directions fail loudly at `bazel test` time.

## Adding a field

Three edits, and the contract test fails until all three are done:

1. Add the attr to `app_metadata` in `release.bzl` and emit it.
2. Add the field to `AppManifest` in `appmeta.proto`.
3. Extend the fixture in `testdata/`.

## Migration

Sequenced with AR-2 in `//tools/app_registry/PLAN.md`, which already has to
touch `release.bzl` (for `deploy_unit`) and `composer.go` (for the chart
lockfile) — so this rides along rather than being a separate disruption.

1. Land `appmeta.proto` and the contract test against today's rule output.
2. Replace `helm/composer.go`'s `AppMetadata` with `appmetapb.AppManifest`.
   Delete the phantom `labels` / `annotations` / `dependencies` fields, or add
   them to the rule if they were meant to exist.
3. Replace `release_helper_go`'s `AppMetadata` with `appmetapb.AppManifest`, and
   **remove the `Language`-as-version hack** in `plan.go` now that `version` is
   a real field.
4. Add `deploy_unit` to `release_app` and to the proto.
5. `release_helper_go` emits `AppManifestSet`; the registry's `ReconcileApps`
   consumes it directly with no conversion.

Step 3 is the one that changes existing behaviour rather than just types — the
version-sentinel removal should be its own reviewable commit.
