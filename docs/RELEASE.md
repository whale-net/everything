# Releasing

How apps and Helm charts get versioned and published. For how an app becomes
releasable in the first place (`release_app`), see
[ARCHITECTURE.md](../ARCHITECTURE.md). Chart-specific details are in
[HELM.md](HELM.md).

There are two release workflows right now:

| Workflow | Trigger | Used for |
|----------|---------|----------|
| `release.yml` ("Release") | Humans, from the Actions tab or `gh` | Any domain |
| `release-v2.yml` ("Release v2") | Machine-only, dispatched by App Registry | Domains whose releases are triggered from the App Registry UI |

`release-v2.yml` is replacing `release.yml` one domain at a time; which
domains use which is tracked in
[tools/app_registry/OPERATIONS.md](../tools/app_registry/OPERATIONS.md#app-registry-v2-release-job--migration-checklist).
`release.yml` takes bug fixes only until it is retired. `promote.yml` is
deprecated and disabled.

## Run a release (`release.yml`)

Actions → **Release** → **Run workflow**, or:

```bash
# Dry run first: plans and prints what would be released
gh workflow run release.yml \
  -f apps=demo-hello-python,demo-hello-go \
  -f increment_patch=true \
  -f dry_run=true

# Release every app and chart in a domain at the next minor version
gh workflow run release.yml -f apps=manman -f helm_charts=manman -f increment_minor=true
```

| Input | Meaning |
|-------|---------|
| `apps` | Comma-separated `<domain>-<app>` names (or `<domain>/<app>`), a domain name, or `all`. Empty skips app release. |
| `helm_charts` | Chart names (`helm-<domain>-<chart>`), a domain name, or `all`. Empty skips chart release. |
| `version` | Explicit version, e.g. `v1.2.3` or `v1.2.3-rc1`. Leave empty when using an increment. |
| `increment_minor` / `increment_patch` | Bump each app and chart from its own latest version. Applies to apps and charts alike. |
| `include_demo` | `all` excludes the `demo` domain unless this is set. Naming `demo` explicitly always works. |
| `dry_run` | Prints what each app and chart would be released as. Nothing is pushed, uploaded, tagged, or recorded. |
| `use_source_tools` | Build `release_helper_go` and the `app-registry` CLI from source instead of downloading the pinned prebuilt binaries. |
| `app_versions` | Advanced override for chart member versions. Leave empty for manual runs. |

Bare app names like `hello-python` are rejected; use the full
`<domain>-<app>` name. `bazel run //tools:release -- list` prints every app
with its domain.

### What the workflow does

1. `plan-release` runs `release_helper_go plan`, which resolves the app list
   and allocates a version for each app and chart.
2. `release` runs `release-app` for each app: build the multi-arch image, push
   it tagged `latest` and the git SHA, then add the `vX.Y.Z` tag. CLI and
   firmware apps are packaged as release assets instead.
3. `release-helm-charts` runs `release-charts`: compose each chart with its
   member apps pinned to the versions just released, package it, and upload it
   to ChartMuseum. See [HELM.md](HELM.md#releasing-charts).
4. `create-github-releases` creates a GitHub Release for each chart and each
   standalone app. Images that belong to a chart don't get their own release;
   the chart's release notes list them.

If an app or chart rebuilds to the same digest as an existing version (no real
change), no new version is minted. The existing version is reused and the run
prints a `No-op rebuild` notice.

### Tags and names

| Artifact | Version tag | Published as |
|----------|-------------|--------------|
| App | `<domain>-<app>.vX.Y.Z` | `ghcr.io/whale-net/<domain>-<app>:vX.Y.Z` (also `:latest` and `:<git-sha>`) |
| Chart | `helm-<domain>-<chart>.vX.Y.Z` | `<domain>-<chart>` version `vX.Y.Z` in ChartMuseum |

## Versions and the App Registry

`APP_REGISTRY_CICD_OPT_IN` (a repository variable, currently `true`) decides
whether release tooling talks to the [App Registry](../tools/app_registry/TOC.md):

- **`true`:** increments are allocated by the registry's `AllocateVersion`
  RPC, and every published image and chart is recorded there. A registry
  error fails the release instead of falling back to git tags.
- **Anything else:** no registry calls at all (`--skip-registry`), and
  increments are computed from git tags.

An explicit `version` input is used as given either way. Check the current
setting with `gh variable list`. The registry's own docs cover the rest:
[OPERATIONS.md](../tools/app_registry/OPERATIONS.md) for running it, and
[architecture/16-availability-and-bootstrap.md](../tools/app_registry/architecture/16-availability-and-bootstrap.md)
for what the opt-in changes.

### App Registry integration

`release-v2.yml` is the registry-driven path. A release triggered from the App
Registry UI starts a Temporal `ReleaseWorkflow`, which:

1. resolves the plan once and dispatches `release-v2.yml` with it;
2. `release-v2.yml` builds every artifact in one Bazel session
   (`release_helper_go build-release`), pushing images by digest only;
3. the App Registry worker then runs `finalize-app` (retag the image to its
   version) and `finalize-chart` (package and upload to ChartMuseum) and
   records the results.

`release-v2.yml` rejects any dispatch whose triggering actor isn't a bot, so
there is no manual entry point. Use the App Registry UI.

## Running `release_helper_go` locally

`//tools:release` is an alias for `//tools/release_helper_go`. Run
`bazel run //tools:release -- <command> --help` for flags.

```bash
bazel run //tools:release -- list                          # every releasable app
bazel run //tools:release -- build demo-hello-python --platform amd64
docker run --rm demo-hello-python:latest

# Preview a plan without touching the registry
bazel run //tools:release -- plan --event-type workflow_dispatch \
  --apps demo --increment-patch --skip-registry --dry-run

bazel run //tools:release -- release-notes demo-hello-python \
  --current-tag demo-hello-python.v1.2.3
```

Discovery commands accept `--fast`, which reads `BUILD.bazel` files directly
instead of running `bazel query`. See
[RELEASE_HELPER_FAST_MODE.md](RELEASE_HELPER_FAST_MODE.md).

### Change detection

The Release workflow doesn't detect changes; it releases exactly what you name.
Change detection is available as two commands:

```bash
# Which release_app targets changed since a commit
bazel run //tools:release -- changes --base-commit origin/main

# Which targets in an arbitrary pool changed (used by ci.yml to gate slow tests)
bazel run //tools:release -- changed-targets \
  --base-commit origin/main \
  --candidates 'tests(//libs/...) intersect rdeps(//libs/..., //libs/go/dbtest:dbtest)'
```

`changes` is conservative: any edit to `MODULE.bazel`, `.bazelrc`,
`.bazelversion`, a `WORKSPACE` file, a `.bzl`, or a `.lock` file marks every
app changed. `changed-targets` scopes `.bzl` and `.lock` edits through `rdeps`
and only treats `MODULE.bazel`, `WORKSPACE*`, `.bazelrc`, and
`.bazelversion` as global. When it can't scope (no `--base-commit`, or one of
those files changed), it prints every candidate, which callers should read as
"run everything". `ci.yml`'s `test-database` job is a worked example.

### Native image push

`release-app` pushes images through the rules_oci `_image_push` script, which
shells out to `crane` and `jq`. Setting `RELEASE_NATIVE_IMAGE_PUSH=true` makes
it push the same OCI layout with go-containerregistry instead. It is opt-in
and not set in any workflow yet.

## Cleaning up old releases

`cleanup-releases.yml` (manual dispatch) deletes old git tags and, optionally,
their GHCR packages. It wraps `release_helper_go cleanup-releases`; start with
its dry-run input.
