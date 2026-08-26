# release_helper --fast: static discovery without Bazel

## What it is

`release_helper`'s app/chart discovery (`ListAllApps`/`ListAllHelmCharts` in
`tools/release_helper_go/cmd/metadata.go` and `plan_helm.go`) normally shells out to Bazel
twice per call: a `bazel query 'kind(app_metadata, //...) ...'` (loading phase) followed by a
`bazel cquery ... --output=starlark` scoped to the matched labels (analysis phase, to read each
target's `AppMetadataInfo`/`HelmChartMetadataInfo` provider). That's a real Bazel server round
trip — on this repo, low single-digit seconds even with a warm server — for every command that
needs the app/chart list: `manifest-set`, `plan`, `plan-helm-release`, `list`, `changes`,
`build-app`, `build-chart`, `build-helm-chart`, `release-notes`, and more.

`--fast` (a persistent flag on the root command, `root.go`) replaces that with
`tools/release_helper_go/cmd/discover_fast.go`: it walks every `BUILD.bazel` file under the
workspace root, parses each one with
[`github.com/bazelbuild/buildtools/build`](https://github.com/bazelbuild/buildtools) — the same
Starlark-AST parser buildifier/buildozer/gazelle use to manipulate BUILD files without
evaluating them — and, for every `release_app(...)`/`release_helm_chart(...)` call it finds,
replicates that macro's derivation logic (from `tools/bazel/release.bzl`) directly in Go to
produce the same `appmetapb.AppManifest`/`ChartManifest` a real `cquery` would. No Bazel process
is ever started.

## The correctness assumption

This only works because every real `release_app`/`release_helm_chart` call site in this repo
passes **literal** keyword arguments — strings, ints, bools, string lists, or (for a chart's
`apps` attr) a reference to a top-level `NAME = [...]` list constant defined earlier in the same
file (e.g. `tools/app_registry/BUILD.bazel`'s `APP_REGISTRY_APPS`). Nothing is computed: no
variables holding a scalar, no string concatenation, no `glob()`, no `select()`, no function
calls. `discover_fast.go` resolves exactly one level of that constant indirection
(`resolveStringListAttr`); anything deeper (a second level of indirection, a computed value)
is not recognized and either silently produces an empty list (if the attribute parses as some
other non-list expression) or is caught by the correctness check below failing loudly.

This assumption is checked, not just asserted:

- `appManifestFromRule`/`helmChartManifestFromRule` fail hard (return an error, not a partial
  or best-guess manifest) on anything they can't make sense of — a missing `domain`/`namespace`,
  an invalid `deploy_unit`, or a chart `apps` entry that doesn't resolve to a discovered
  `app_metadata` target. A `--fast` run either produces the same manifest set the slow path
  would, or fails outright.
- Direct `app_metadata(...)`/`helm_chart_metadata(...)` rule calls (as opposed to the
  `release_app`/`release_helm_chart` macros) are never matched by the fast scan at all. This is
  also how it gets testonly-fixture exclusion for free: `tools/appmeta/testdata/BUILD.bazel`'s
  `fixture-app_metadata` calls the rule directly specifically so it can set `testonly = True`
  (the macros have no `testonly` passthrough — see that file's own comments), which is how
  `ListAllApps`'s real bazel query excludes it (`except attr(testonly, 1, //...)`). A raw rule
  call structurally can't be a `release_app(...)`/`release_helm_chart(...)` call, so the fast
  scanner skips it the same way without needing to know about `testonly` at all.
- `chart_target` is `attr.string` on the `helm_chart_metadata` rule, not `attr.label` — so real
  Bazel never canonicalizes it; it stays exactly as `release_helm_chart` composes it
  (`":" + name`). `discover_fast.go` mirrors that literally rather than resolving it to
  `//pkg:name` the way every *label*-typed field (`binary_target`, `image_target`,
  `openapi_spec_target`, the target's own `BazelTarget`) is resolved. This one is easy to get
  wrong by treating every string that looks like a label as a label — verified by the diff
  below, which is exactly how it was caught.

**How to keep this honest as the repo grows**: `bazel run //tools/release_helper_go -- --fast
manifest-set --git-sha <sha>` and `bazel run //tools/release_helper_go -- manifest-set --git-sha
<sha>` (the default, real-Bazel path) must produce byte-identical output aside from the
`discoveredAt` timestamp. Run both and diff after any change to `release.bzl`'s
`release_app`/`release_helm_chart` macros, `discover_fast.go`, or when a new
`release_app`/`release_helm_chart` call site uses a parameter this file doesn't yet cover. There
is no CI job wired up for this yet (see "Not done" below) — it's a manual check today.

## What's out of scope

- `changes` (`DetectChangedApps`, `changes.go`)'s `rdeps(...)` query — figuring out which apps a
  file diff affects — is a real Bazel dependency-graph reachability query. `--fast` still speeds
  up that command's "list every app" baseline (it calls `ListAllApps` internally, so it inherits
  `--fast`), but the `rdeps` step itself always goes through real `bazel query`. Reimplementing
  Bazel's dependency graph and reachability outside Bazel is out of scope.
- `base` and `additional_tars` (`release_app` params) only affect the `multiplatform_image(...)`
  target, never the `app_metadata` JSON — `discover_fast.go` doesn't need to read them, and
  doesn't.
- `custom_repo_name` (`release_app` param) is accepted by the macro but never actually used to
  set `repo_name` in `_app_metadata_impl` (`repo_name` is always `domain + "-" + effective_name`
  — a pre-existing quirk of the real macro, not something `--fast` introduces). `discover_fast.go`
  matches that behavior rather than "fixing" it.

## CI toggle: `vars.RELEASE_HELPER_FAST_MODE`

`.github/actions/app-registry-reconcile` (the `manifest-set` → `apps reconcile` pipeline invoked
from `ci.yml`'s `reconcile-app-registry` job) and `ci.yml`'s own `Plan Docker builds using
release tool` step both consult the repository variable `RELEASE_HELPER_FAST_MODE`: `--fast` is
passed to `release_helper_go` only when it's exactly `"true"`. An unset variable evaluates to an
empty string, which is not `"true"`, so **the default is false** with no variable creation
required — a repo that never sets `RELEASE_HELPER_FAST_MODE` behaves exactly as it did before
this toggle existed.

To turn it on: `gh variable set RELEASE_HELPER_FAST_MODE --body true` (repo admin), or via repo
Settings → Secrets and variables → Actions → Variables. This is a live CI-behavior change, so
treat it the same as any other production toggle — a human decision, not something to flip from
a PR. To turn it back off, delete the variable or set it to `false`.

Not every discovery call site is wired to this variable — only the two above (the `manifest-set`/
`apps reconcile` "reconcileapps" pipeline and the PR/push build-planning `plan` call). Other
`release_helper_go` commands (`build-app`, `build-chart`, `list`, `release-notes`, ...) still
default to real Bazel discovery everywhere, including when run locally; pass `--fast` by hand for
those, or extend this same pattern if a future workflow needs it.

## Not done / possible follow-ups

- No CI job runs the fast-vs-slow diff automatically; it was checked manually against this
  repo's full manifest set (29 apps, 7 charts) before this flag shipped. Wiring that into a test
  (e.g. an integration test gated behind a real Bazel invocation, which `bazel test` doesn't
  normally allow) or a periodic CI check would close the gap between "checked once" and "stays
  correct."
- `release.yml`/`release-v2.yml` are not wired to `RELEASE_HELPER_FAST_MODE` — their `plan`
  invocations either aren't discovery calls (`--from-resolved-plan` skips `ListAllApps`/
  `ListAllHelmCharts` entirely) or weren't found to shell out to `release_helper_go` discovery
  commands directly. Revisit if that changes.
