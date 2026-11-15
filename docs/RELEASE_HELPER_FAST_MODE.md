# release_helper --fast

`--fast` is a global `release_helper_go` flag that discovers apps and charts by
parsing `BUILD.bazel` files instead of asking Bazel. Read this before changing
the `release_app` or `release_helm_chart` macros, or
`tools/release_helper_go/cmd/discover_fast.go`.

## What it replaces

Without `--fast`, `ListAllApps` and `ListAllHelmCharts` (`cmd/metadata.go`,
`cmd/plan_helm.go`) run `bazel query 'kind(app_metadata, //...)'` and then a
`bazel cquery --output=starlark` to read each target's metadata provider. That
costs a few seconds per call even with a warm server, and nearly every command
calls it (`plan`, `list`, `manifest-set`, `build-app`, `release-notes`, ...).

With `--fast`, `discover_fast.go` walks every `BUILD.bazel`, parses it with
[buildtools](https://github.com/bazelbuild/buildtools)' Starlark parser, finds
each `release_app(...)` and `release_helm_chart(...)` call, and repeats the
macro's derivation logic from `tools/bazel/release.bzl` in Go. No Bazel process
starts.

```bash
bazel run //tools:release -- --fast list
bazel run //tools:release -- --fast manifest-set
```

## The assumption it depends on

Every `release_app` and `release_helm_chart` call site passes literal
arguments: strings, ints, bools, and string lists. The one exception allowed is
a chart's `apps` referring to a top-level `NAME = [...]` list defined earlier in
the same file (for example `APP_REGISTRY_APPS` in
`tools/app_registry/BUILD.bazel`). One level of that indirection is resolved;
nothing deeper is. Variables, string concatenation, `glob()`, `select()`, and
function calls are not supported.

What keeps this safe:

- Anything the parser can't make sense of is an error, not a guess: a missing
  `domain` or `namespace`, an invalid `deploy_unit`, or a chart `apps` entry
  that doesn't match a discovered app.
- Direct `app_metadata(...)` / `helm_chart_metadata(...)` rule calls are never
  matched. That is also how `testonly` fixtures like
  `tools/appmeta/testdata/BUILD.bazel` stay out, mirroring the slow path's
  `except attr(testonly, 1, //...)`.
- `chart_target` is an `attr.string`, not a label, so Bazel never
  canonicalizes it. `--fast` keeps it as the literal `":<name>"` the macro
  writes. Label-typed fields (`binary_target`, `image_target`,
  `openapi_spec_target`) are resolved to `//pkg:name` like Bazel does.
- `custom_repo_name` is accepted by `release_app` but ignored; `repo_name` is
  always `<domain>-<name>`. `--fast` matches that.

## Checking parity

The two paths must produce identical `manifest-set` output apart from
`discoveredAt`. Nothing in CI checks this; run it by hand after changing
`release.bzl`'s macros, `discover_fast.go`, or adding a call site that uses a
parameter `discover_fast.go` doesn't read yet:

```bash
bazel run //tools:release -- --fast manifest-set > /tmp/fast.json
bazel run //tools:release -- manifest-set > /tmp/slow.json
diff /tmp/fast.json /tmp/slow.json   # only discoveredAt should differ
```

Unit tests are in `cmd/discover_fast_test.go`
(`bazel test //tools/release_helper_go/...`).

## Limits

- `changes` still runs a real `bazel query` for its `rdeps(...)` step. `--fast`
  only speeds up its "list every app" baseline.
- `base` and `additional_tars` only affect the image target, not metadata, so
  `--fast` ignores them.

## CI toggle

The repository variable `RELEASE_HELPER_FAST_MODE` turns `--fast` on in CI when
it is exactly `true` (it currently is; check with `gh variable list`). Unset or
any other value means real Bazel discovery.

It is read in two places in `ci.yml`: the `reconcile-app-registry` job (passed
to `.github/actions/app-registry-reconcile`, which runs `manifest-set`) and the
`plan-docker` job, which is currently disabled. `release.yml` and
`release-v2.yml` don't read it. Flipping it changes production CI behavior, so
it's a human decision:

```bash
gh variable set RELEASE_HELPER_FAST_MODE --body true    # on
gh variable delete RELEASE_HELPER_FAST_MODE             # off
```
