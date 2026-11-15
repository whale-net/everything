# Bazel Cache Image (POC)

An experiment in baking a fully warmed Bazel `output_base` into a Docker
image, so a CI job running in that container has nothing to fetch. It
didn't pay off on GitHub-hosted runners, and it is switched off. This doc
records what was built, what was measured, and what would have to change
for it to pay off.

## Result

The consumer job, `test-bazel-cache-image` in `ci.yml`, is commented out.
The repo variable `BAZEL_CACHE_IMAGE_ENABLED` is `false`.

The bake worked. A cold container built all of `//...` in 77s:

```
INFO: 221 processes: 4610 action cache hit, 26 remote cache hit, 182 internal, 13 processwrapper-sandbox.
```

Delivering that cache to the runner cost more than it saved. The image is
5.45 GB, and one layer, the baked `output_base`, is 5.14 GB. GitHub-hosted
runners start each job on a fresh VM with no Docker layer cache, so every
job pulls the whole image:

| Phase | Duration |
|---|---|
| Pull image (`Initialize containers`) | 3m26s |
| Checkout + remote cache config | ~4s |
| `bazel build --config=ci //...` | 77s |
| **Total** | **~4m7s** |

The normal `test` job finishes in about 2m50s (about 30s of setup plus a
139s build), so it wins.

For this approach to win, one of these would have to change:

- **Persistent self-hosted runners**, where the image stays in the local
  Docker cache and the pull is paid once. The repo has no such runners.
- **A much smaller image**, warming a curated set of targets instead of
  `//...`. The ESP32/Pigweed toolchains account for a large share of the
  5.14 GB.

## How it works

[`.devcontainer/Dockerfile.bazel-cache`](../.devcontainer/Dockerfile.bazel-cache)
does three things. It installs Bazelisk, copies the repo to `/workspace`,
and runs `bazel --output_base=/var/cache/bazel/ob build ${WARM_TARGETS}`.
`WARM_TARGETS` defaults to `//...`.

The fixed `--output_base` is the point. Bazel's default output base is a
hash of the workspace path, and GitHub Actions mounts the checkout at a
path the repo doesn't control. A consumer passes the same
`--output_base=/var/cache/bazel/ob` and gets cache hits from any checkout
path. This was verified locally with a different mount path and
`--network=none`.

The base image (`mcr.microsoft.com/devcontainers/base:ubuntu-24.04`) is
much smaller than GitHub's runner image. The Dockerfile installs the host
tools that `//...` turned out to need, such as `python3` for
`rules_pycross` and `zstd` for `//tools/lib32:lib32_extracted`. If a warm
build fails on a missing binary, add it there.

## Remote cache during the warm build

The warm build can optionally read the real remote cache, which mostly
makes the bake itself faster. Credentials reach it only through a BuildKit
secret mount (`id=bazelrc_remote`). The workflow renders that file with
[`setup-bazel-remote-cache-config`](../.github/actions/setup-bazel-remote-cache-config/action.yml).
They never go through a build arg or `ENV`, which would leak into
`docker history`. Without the secret, `.bazelrc`'s
`try-import %workspace%/.bazelrc.remote` does nothing and the build runs
uncached.

The warm build deliberately does not use `--config=ci`. That config's
`--remote_download_minimal` would leave cache hits off local disk, so they
wouldn't be baked into the image.

## Trying it

Local smoke test with a narrow target set, with no credentials and no push:

```bash
docker build -f .devcontainer/Dockerfile.bazel-cache \
  --build-arg WARM_TARGETS=//tools/... \
  -t bazel-cache-devcontainer:local .
```

In CI, run **Bazel Cache Image (POC)**
(`devcontainer-bazel-cache-image.yml`, manual only). It always builds. It
pushes `ghcr.io/<owner>/bazel-cache-devcontainer:latest` and `:<sha>` only
when `BAZEL_CACHE_IMAGE_ENABLED` is `true`. A full `//...` bake took about
23 minutes.

To re-enable the consumer, uncomment `test-bazel-cache-image` in `ci.yml`
and run:

```bash
gh variable set BAZEL_CACHE_IMAGE_ENABLED --repo whale-net/everything --body true
```

Don't call `bazel shutdown` at the end of the consumer job. Inside a job
`container:` it hangs and fails the job after the build has already
passed.

The regular devcontainer (`.devcontainer/Dockerfile`) is a separate
tooling-only image with no baked cache, and shouldn't be swapped for this
one. A 5 GB layer would make every container rebuild slow, and the cache
goes stale as soon as a checkout drifts.
