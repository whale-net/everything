# CI/CD

All automation is GitHub Actions in [`.github/workflows/`](../.github/workflows/).
Shared setup lives in composite actions under
[`.github/actions/`](../.github/actions/). Releasing is covered in
[RELEASE.md](RELEASE.md). This doc covers what each workflow does and why
it's shaped that way.

## Workflows

| File | Name in the Actions UI | Trigger | Purpose |
|------|------------------------|---------|---------|
| `ci.yml` | CI | every `pull_request`, `push` to `main` | Tests, App Registry reconcile |
| `release.yml` | Release | manual `workflow_dispatch` | Build, push, and tag apps and Helm charts. Frozen to bug fixes while `release-v2.yml` replaces it |
| `release-v2.yml` | Release v2 | `workflow_dispatch` from the App Registry only | Release path dispatched by Temporal. The `authorize-trigger` job rejects human dispatches |
| `promote.yml` | Promote (Deprecated) | manual | Disabled (`if: false`). See [App Registry](#app-registry) |
| `cleanup-releases.yml` | Cleanup Old Releases | manual | Prunes old tags, releases, and GHCR versions. See [CLEANUP.md](CLEANUP.md) |
| `devcontainer-bazel-cache-image.yml` | Bazel Cache Image (POC) | manual | Abandoned experiment. See [BAZEL_CACHE_IMAGE.md](BAZEL_CACHE_IMAGE.md) |

## `ci.yml` jobs

| Job | Runs on | What it does |
|-----|---------|--------------|
| `test` (Test) | PRs and `main` | On PRs: runs `bazel coverage --config=ci` on only the tests affected by the diff against `main`'s tip, found with `release_helper changed-targets`. On `main`: runs `bazel coverage --config=ci //...`. Then runs `//tools/appmeta:manifest_contract_test` and uploads coverage to Codecov |
| `test-database` (Test Database Integration) | PRs and `main` | `manual`-tagged tests that need Docker: everything depending on `//libs/go/dbtest`, plus `//libs/go/s3:s3_integration_test`. Narrowed to affected targets on PRs |
| `test-container-arch` (Test Container Architecture) | `main` only | `bazel test //tools/scripts:test_cross_compilation`, which checks each platform variant of the image index has the right wheels (see [DOCKER.md](DOCKER.md#gotchas)) |
| `reconcile-app-registry` | `main` only | Downloads release tools. When `APP_REGISTRY_CICD_OPT_IN` is `true`, it also reconciles every app and chart into the App Registry and records a build log row for each. Not `continue-on-error`: a registry outage turns `main` red |
| `build-summary` (Build Summary) | always | Fails if `test` failed, or, on `main`, if `test-container-arch` failed or was skipped. Doesn't check `test-database` |
| `plan-docker`, `docker` | never | Disabled with `if: false` on `plan-docker`. Nothing consumed the images, and the change detection behind it isn't trusted yet |

Tests that need Docker are tagged `manual`, so they stay out of `//...`.
When you add one that doesn't depend on `//libs/go/dbtest`, add its label to
the `test-database` job's candidate query, or it never runs in CI.

To reproduce the PR scoping locally:

```bash
git fetch origin main
bazel run //tools:release -- changed-targets \
  --base-commit="$(git rev-parse FETCH_HEAD)" --candidates='tests(//...)'
```

### Why `test` and `test-database` are separate jobs

Merging them saves compute but slows PR feedback. On PR #1107 the merged
job used 23% fewer job-seconds (460s down to 354s), but wall clock went from
243s to 354s, because the two jobs no longer ran in parallel. The repo is
public, so runner minutes are free and latency is the only cost that
matters. Re-run the experiment if the repo goes private or per-job setup
gets much more expensive.

### Why releases build on one runner

`release.yml`'s `release` job loops over every app in the plan
sequentially on one runner instead of fanning out a matrix. Per-runner
setup (checkout, `setup-build-env`, cache auth, Docker) takes about 60–90s.
With a warm remote cache, the per-app build takes 10–30s. A matrix would
spend most of its minutes repeating setup, and a single runner keeps the
Bazel server and analysis cache warm between apps. Planning stays in its
own lightweight `plan-release` job.

## Repository variables

| Variable | Default | Effect |
|----------|---------|--------|
| `APP_REGISTRY_CICD_OPT_IN` | unset | `true` turns on every App Registry call in CI and releases. Unset means `release_helper` runs with `--skip-registry` |
| `APP_REGISTRY_BUILDER_ENV` | `dev` | Which registry environment release tools are resolved from, and which builder client ID is used |
| `RELEASE_HELPER_FAST_MODE` | unset | `true` makes `release_helper` discover apps without Bazel. See [RELEASE_HELPER_FAST_MODE.md](RELEASE_HELPER_FAST_MODE.md) |
| `RELEASE_TOOLS_FALLBACK_TO_SOURCE` | unset | `release-v2.yml` only. See [Release tools](#release-tools) |
| `BAZEL_CACHE_IMAGE_ENABLED` | `false` | Lets the cache-image POC push. See [BAZEL_CACHE_IMAGE.md](BAZEL_CACHE_IMAGE.md) |

Set one with `gh variable set <NAME> --repo whale-net/everything --body <value>`.

Remote cache secrets (`BAZEL_REMOTE_CACHE_*`, `BAZEL_REMOTE_DOWNLOADER_URL`)
are documented in [CONFIGURATION.md](CONFIGURATION.md#remote-cache-configuration).

## Release tools

The release workflows and `ci.yml` don't build `release_helper_go` or the
`app-registry` CLI on every run. They download prebuilt, checksum-verified
binaries with
[`download-release-tools`](../.github/actions/download-release-tools/action.yml).
The version is whatever is currently promoted in the App Registry for
`APP_REGISTRY_BUILDER_ENV`. That action sets `$RELEASE_HELPER` and
`$APP_REGISTRY_CLI` for later steps.

In `release.yml` and `release-v2.yml`, a `build-release-tools` job probes
once whether the prebuilt binaries are actually fetchable. If they aren't,
it builds the tools with Bazel and uploads them as a `release-tools`
workflow artifact, which later jobs use instead.

Escape hatches for when that acquisition path breaks:

- `release.yml` and `cleanup-releases.yml` have a `use_source_tools`
  dispatch input that builds the tools from source.
- `release-v2.yml` has no human input, so it uses the
  `RELEASE_TOOLS_FALLBACK_TO_SOURCE` variable instead. When it's `true`
  and a download fails, the job builds the tools from source inline instead
  of failing. Unset it once downloads work again, because source builds
  are slower and skip the checksum-verified binaries.

## App Registry

`release.yml`, `release-v2.yml`, and `ci.yml`'s `reconcile-app-registry`
job talk to the [App Registry](../tools/app_registry/TOC.md). Every call is
gated on `APP_REGISTRY_CICD_OPT_IN == 'true'`. When it's unset, CI makes no
registry calls.

When it's on, recording happens inside `release_helper`, not in separate
workflow steps. `release-app` and `release-charts` allocate the version,
then record the pushed image or chart (charts include the digests of the
images they pin). A failed `RecordArtifact` fails the command, and with it
the job. The operator runbook is
[`tools/app_registry/OPERATIONS.md`](../tools/app_registry/OPERATIONS.md).

`promote.yml` would promote or roll back a recorded artifact to
`dev`/`stage`/`prod`. It is disabled with `if: false`, because neither the
Keycloak clients nor the GitHub Environments it needs exist yet. If it is
ever turned on, its `environment:` declaration is the whole security model,
since that is what scopes the promoter secret and its required reviewers.
See [`tools/app_registry/DEPLOY.md`](../tools/app_registry/DEPLOY.md).
