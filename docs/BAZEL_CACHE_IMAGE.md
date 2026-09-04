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

## Remote cache during the warm build

The image build *also* wires in the real Bazel remote cache (optional --
the build works fine with none configured). This isn't redundant with the
image cache: the remote cache only stores *compiled action outputs*, and
`ci.yml`'s `test` job on `main` keeps it populated continuously via
`--remote_upload_local_results=true`, so a from-scratch warm build can
mostly *download* already-compiled outputs instead of recompiling
everything -- a large speedup for the warm build's own wall-clock time. It
does not help the external-repo/toolchain *download* step (a separate
mechanism, the repository cache / remote asset downloader), so this is a
build-speed optimization, not a replacement for anything else here.

Credentials reach the build only via a BuildKit secret mount
(`RUN --mount=type=secret,id=bazelrc_remote,...` in the Dockerfile, fed by
`docker/build-push-action`'s `secret-files` input, itself rendered by the
new `.github/actions/setup-bazel-remote-cache-config` composite action) --
never a build-arg or `ENV`, which would land in `docker history` and the
image's layers. Verified locally: a fake unreachable remote-cache URL
passed via `--secret` produced the expected DNS-resolution failure inside
the build, and a build with no `--secret` at all still succeeds (the
mount target is simply absent; `.bazelrc`'s `try-import` no-ops on that
like it would on any missing file) -- so a plain local
`docker build -f .devcontainer/Dockerfile` (no credentials available)
keeps working exactly as before.

Deliberately **not** `--config=ci` for this warm build: that config's
`--remote_download_minimal` would leave remote-cache-hit outputs
unmaterialized on local disk, which defeats the point of this image --
every action's output, whether freshly compiled or pulled from the
remote cache, must actually land under `${BAZEL_OUTPUT_BASE}` so a later
fully-offline consumer still finds it there.

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

### Real `//...` run against `main`

First dispatch of `devcontainer-bazel-cache-image.yml` with the default
`WARM_TARGETS=//...` against `main` failed at `//tools/lib32:lib32_extracted`,
a genrule that shells out to a system `zstd` binary to unpack a `.deb` --
present on GitHub's `ubuntu-latest` runner (where `ci.yml`'s `test` job
already builds `//...` successfully) but not in this deliberately-minimal
devcontainer base image. Fixed by installing `zstd` plus a broader set of
common build-time tools GitHub's runner ships and this base image doesn't
(`git`, `ca-certificates`, `xz-utils`, `pkg-config`, `cmake`, `ninja-build`,
`rsync`, `zip`).

The re-dispatch with that fix (no remote cache configured yet -- this
predates the remote cache work below) **succeeded**: full `//...`,
including the ESP32/Pigweed toolchains, in **23m9s**, and pushed
`ghcr.io/whale-net/bazel-cache-devcontainer:latest`.

Consuming that image from `ci.yml`'s `test-bazel-cache-image` job then
validated the actual POC claim at real scale:

```
INFO: Elapsed time: 77.435s, Critical Path: 5.76s
INFO: 221 processes: 4610 action cache hit, 26 remote cache hit, 182 internal, 13 processwrapper-sandbox.
INFO: Build completed successfully, 221 total actions
```

The entire monorepo's `//...` build, from a cold container, completed in
under 80 seconds -- 4610 actions served straight from the image's baked
cache, 26 more from the remote cache covering drift since the bake, only
13 (the same non-hermetic OpenAPI codegen `[for tool]` steps noted above)
re-executed.

One more real gap this surfaced, unrelated to the build itself: a trailing
`bazel shutdown` after this build hung and had to be SIGKILL'd, failing
the job despite the build having already succeeded -- apparently specific
to how GH Actions execs steps into a job `container:` (a plain `docker
build` RUN step, both locally and in the real image-build workflow, shuts
down cleanly with the identical command). Fixed by simply not calling
`bazel shutdown` in that job -- the container is destroyed the moment the
job ends regardless, so a clean shutdown buys nothing there.

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
- `ci.yml`'s `test-bazel-cache-image` job runs a full `bazel build --config=ci //...`
  inside that image and reports its own (non-required) result. It skips
  `./.github/actions/setup-build-env` entirely -- no GH Actions cache
  restore, no `bazel-contrib/setup-bazel` -- since the image already bakes
  in what that would restore; it only adds the remote cache (via the same
  `setup-bazel-remote-cache-config` action used by the image build) to
  cover anything that's drifted since the image was last built.

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
