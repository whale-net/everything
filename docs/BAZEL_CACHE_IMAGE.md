# Bazel Cache Image (POC)

**Status: proof of concept, opt-in, not wired into any required pipeline.**

## Problem

Every CI job pays a cold-start tax before it runs a single Bazel action:
restoring the GH Actions cache for `~/.bazel/external`, authenticating to the
Bazel remote cache, and re-resolving ~200+ repository rules (Go SDK, Python
toolchain, Pigweed/ESP32 cross toolchains, rules_oci base images). `docs/CI_CD.md`
"Runner Architecture & Bazel Server Warming" already quantifies this at
~60-90s per runner, and the real remote cache (`.github/actions/setup-build-env`)
remains the biggest win for actual build/test action reuse across runs.

This POC asks a narrower question: what if a CI job's *container image
itself* already contained a fully realized Bazel output_base -- external
repos, toolchains, and every action's compiled output -- so there is nothing
to restore or fetch over the network at all, even on a cold runner?

## How it works

`.devcontainer/Dockerfile` builds an image that:

1. Installs Bazelisk (resolves the version pinned by `.bazelversion`).
2. Copies the repo in at a fixed path (`/workspace`).
3. Runs `bazel --output_base=/var/cache/bazel/ob build //...`, which
   populates `/var/cache/bazel/ob` with every external repo/toolchain fetch
   and every action's output for the whole monorepo -- and bakes that
   directory into the image layer.

The key design choice is the **explicit, fixed `--output_base`**, not
Bazel's default (which hashes the workspace's absolute directory path).
A container started `FROM` this image can check the repo out anywhere --
GitHub Actions' `container:` jobs mount the workspace at a path this repo
doesn't control -- and still hit the baked cache, by passing the same
`--output_base=/var/cache/bazel/ob` flag. Bazel's local action cache keys
on input content digests, not absolute paths, so this works regardless of
checkout location.

No remote cache credentials are involved in building this image -- it's a
from-scratch local build against public upstream registries only, so
nothing sensitive ever reaches the image layers.

## Freshness & honest limits

The cache is only as fresh as the last image build. A CI run against a
checkout that has diverged from what the image was built against still
gets full cache hits for everything unchanged (external repos, toolchains,
and any untouched package) and rebuilds only what actually changed --
same as any other warm Bazel cache. This is a complement to, not a
replacement for, the real remote cache: it mainly kills the *cold-start*
fetch/restore cost, not incremental rebuild cost after real code changes.

`bazel build //...` includes the ESP32/Pigweed cross toolchains and every
language toolchain in the repo (Go, Python, Java, protobuf), so warming the
image is a heavier, slower build than a typical CI job -- expect it to take
meaningfully longer than the `test` job's own `//...` build. This is why
the image-build workflow is manual (`workflow_dispatch`) rather than
running on every push.

## Validated locally

Built the image with `WARM_TARGETS=//demo/hello_go_client/...` (a real
target that pulls in the Go SDK, Go toolchain build, and an OpenAPI Go
client codegen step — enough to touch the external-repo and action-cache
machinery, not just a no-op). Then, from a fresh container off that image,
mounted the checkout at a **different path** (`/checkout`, vs. the
image's `/workspace`) with **`--network=none`** and re-ran the same build:

```
INFO: Elapsed time: 16.837s, Critical Path: 3.62s
INFO: 7 processes: 76 action cache hit, 5 internal, 2 processwrapper-sandbox.
INFO: Build completed successfully, 7 total actions
```

76 of the run's actions were served from the image's baked-in output_base
with no network access and a different absolute checkout path — confirming
the explicit `--output_base` design works exactly as intended. (The
`processwrapper-sandbox` actions are the OpenAPI codegen step's own
non-hermetic `[for tool]` re-execution, unrelated to cache-key portability.)

One real gap surfaced by this test and already fixed in the Dockerfile: the
`mcr.microsoft.com/devcontainers/base:ubuntu-24.04` base image has no system
`python3`, which `rules_pycross`'s wheel installer needs even before any
hermetic Python toolchain is available (`/usr/bin/env: 'python3': No such
file or directory`) — the Dockerfile now installs `python3`,
`build-essential`, and `unzip` before running Bazel.

### Real `//...` run against `main` (first attempt failed, second passed the same gap)

Dispatching `devcontainer-bazel-cache-image.yml` with the default `WARM_TARGETS=//...`
against `main` failed at `//tools/lib32:lib32_extracted`, a genrule that
shells out to a system `zstd` binary to unpack a `.deb` — present on
GitHub's `ubuntu-latest` runner (where `ci.yml`'s `test` job already builds
`//...` successfully) but not in this deliberately-minimal devcontainer
base image. Fixed by installing `zstd` plus a broader set of common
build-time tools GitHub's runner ships and this base image doesn't
(`git`, `ca-certificates`, `xz-utils`, `pkg-config`, `cmake`, `ninja-build`,
`rsync`, `zip`) — verified locally against `//tools/lib32/...` before
re-dispatching. This section will be updated with the actual `//...`
outcome once that re-run completes; a monorepo this size touching
ESP32/Pigweed toolchains may well surface further host-tool gaps the same
way, which is exactly what this section is for.

## Trying it

**Build the image build args without pushing anything** (validates the
Dockerfile; useful for iterating on this POC):

```bash
docker build -f .devcontainer/Dockerfile \
  --build-arg WARM_TARGETS=//tools/... \
  -t bazel-cache-devcontainer:local .
```

Use a narrower `WARM_TARGETS` pattern for a fast local smoke test --
`//...` is what production usage would warm.

**Enable the real workflows.** Both are gated behind the `BAZEL_CACHE_IMAGE_ENABLED`
repo variable (unset by default -- merging this POC changes no existing
pipeline's behavior), matching this repo's other opt-in-via-repo-variable
gates (e.g. `APP_REGISTRY_CICD_OPT_IN`, see `docs/CI_CD.md`):

```bash
gh variable set BAZEL_CACHE_IMAGE_ENABLED --repo whale-net/everything --body true
```

With it set to `true`:
- `.github/workflows/devcontainer-bazel-cache-image.yml` (manual dispatch) pushes
  `ghcr.io/<owner>/bazel-cache-devcontainer:latest` and `:<sha>`.
- `ci.yml`'s `test-bazel-cache-image` job runs a full `bazel build //...`
  inside that image and reports its own (non-required) result.

## Local dev use

`.devcontainer/devcontainer.json` now builds from this same Dockerfile
(`build.dockerfile` instead of a bare `image`), so opening this repo in a
devcontainer-aware editor or Codespace gets the same pre-warmed cache --
existing `features` (Go, Python, docker-in-docker) still layer on top.

## Promoting this beyond POC

Not done here, and deliberately out of scope for this PR (see AGENTS.md --
production/release pipelines are for release actions and human decisions,
not agent patches):

- A `schedule:` trigger for `devcontainer-bazel-cache-image.yml` (e.g. weekly) once the
  build time and image size are measured from real runs.
- Wiring the image into `test`/`test-database`/`release.yml` proper, which
  should only happen after comparing measured wall-clock/cache-hit-rate
  against the existing remote-cache path on this repo's actual CI traffic.
- Image size/registry-cost tracking -- a full `//...` output_base is large;
  GHCR storage and pull time for this image are unmeasured today.
