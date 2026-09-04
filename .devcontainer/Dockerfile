# syntax=docker/dockerfile:1
#
# Bazel-cache-warmed image (POC). Bakes a `bazel build //...` output_base
# (external repos/toolchains + every action's compiled output) into image
# layers at a fixed, explicit --output_base path. Any container started
# FROM this image that runs Bazel with that same --output_base flag reuses
# the pre-populated cache directly -- no remote cache round-trip, no
# external-repository re-fetch, no toolchain download -- regardless of
# where the actual source checkout lives on disk in that container, since
# Bazel's local action cache keys on input digests, not absolute paths.
#
# Rationale, cadence, and CI wiring: docs/BAZEL_CACHE_IMAGE.md.
FROM mcr.microsoft.com/devcontainers/base:ubuntu-24.04

USER root

# GitHub's ubuntu-latest runner (where `ci.yml`'s `test` job already runs a
# full `bazel build //...` successfully) ships a huge preinstalled toolset;
# this devcontainer base image is deliberately minimal and has almost none
# of it. Packages below were added as real `bazel build //...` failures
# surfaced a missing host tool -- see docs/BAZEL_CACHE_IMAGE.md for the
# specific error each one fixed (e.g. python3: rules_pycross's
# wheel_installer runs as `/usr/bin/env python3` before any hermetic Python
# toolchain exists; zstd: //tools/lib32:lib32_extracted shells out to it to
# unpack a .deb).
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
         python3 \
         build-essential \
         unzip \
         zip \
         git \
         ca-certificates \
         zstd \
         xz-utils \
         pkg-config \
         cmake \
         ninja-build \
         rsync \
    && rm -rf /var/lib/apt/lists/*

# Bazelisk resolves the exact version pinned by .bazelversion (8.3.1 today),
# matching what CI and local devs use -- never hardcode a Bazel version here.
ARG BAZELISK_VERSION=v1.20.0
RUN curl -fsSL -o /usr/local/bin/bazel \
      "https://github.com/bazelbuild/bazelisk/releases/download/${BAZELISK_VERSION}/bazelisk-linux-$(dpkg --print-architecture)" \
    && chmod +x /usr/local/bin/bazel

# Fixed, explicit output_base -- deliberately NOT the default hash-derived
# path (which is keyed off the workspace's absolute directory and would
# silently miss if a later container checks the repo out somewhere else).
ENV BAZEL_OUTPUT_BASE=/var/cache/bazel/ob

WORKDIR /workspace
COPY . .

# Which targets to warm. Defaults to everything; override with a narrower
# pattern (e.g. `--build-arg WARM_TARGETS=//tools/...`) for a fast local
# smoke test of this Dockerfile without paying for a full //... build.
ARG WARM_TARGETS=//...

# Optional remote-cache config, supplied only via a BuildKit secret mount
# (never a build-arg or ENV, which would land in `docker history`/layers)
# -- see docs/BAZEL_CACHE_IMAGE.md "Remote cache during the warm build".
# CI passes a rendered .bazelrc.remote (see the setup-bazel-remote-cache-config
# composite action) via `secret-files`/`--secret`; a plain local `docker
# build` without `--secret` leaves the mount target simply absent, and
# .bazelrc's `try-import` no-ops on that exactly like it would on a
# missing file anywhere else.
#
# Deliberately NOT --config=ci here: that config's --remote_download_minimal
# would leave remote-cache-hit outputs unmaterialized on local disk, which
# defeats the entire point of this image -- every action's output must
# actually land in ${BAZEL_OUTPUT_BASE}, whether it was freshly compiled or
# pulled from the remote cache, so a later fully-offline consumer still
# finds it there.
RUN --mount=type=secret,id=bazelrc_remote,target=/workspace/.bazelrc.remote \
    bazel --output_base=${BAZEL_OUTPUT_BASE} build ${WARM_TARGETS} \
    && bazel --output_base=${BAZEL_OUTPUT_BASE} shutdown
