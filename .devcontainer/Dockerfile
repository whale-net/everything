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

# No remote cache is wired up here on purpose: this is a from-scratch local
# build, so nothing but public upstream registries is contacted, and no
# cache credentials ever need to reach the image build or its layers.
RUN bazel --output_base=${BAZEL_OUTPUT_BASE} build ${WARM_TARGETS} \
    && bazel --output_base=${BAZEL_OUTPUT_BASE} shutdown
