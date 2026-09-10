# syntax=docker/dockerfile:1
#
# Tooling-only devcontainer image. Installs everything needed to run
# `bazel build //...` and `bazel test //...` inside the container -- Bazel
# itself (via Bazelisk) plus the host packages Bazel's hermetic toolchains
# still shell out to -- and a k3d binary for local Kubernetes work. It does
# NOT bake a build/test cache into the image; that's a separate, larger,
# POC-only image built from .devcontainer/Dockerfile.bazel-cache (see
# docs/BAZEL_CACHE_IMAGE.md). Keeping this image cache-free means it stays
# small and rebuilds fast for everyday devcontainer use.
#
# docker-in-docker and the Go/Python toolchains are layered on via
# devcontainer.json's `features`, not this Dockerfile.
FROM mcr.microsoft.com/devcontainers/base:ubuntu-24.04

USER root

# GitHub's ubuntu-latest runner (where ci.yml's `test` job already runs a
# full `bazel build //...` successfully) ships a huge preinstalled toolset;
# this devcontainer base image is deliberately minimal and has almost none
# of it. This list was derived empirically from real `bazel build //...`
# failures against this base image (see docs/BAZEL_CACHE_IMAGE.md for the
# specific error each one fixed) -- e.g. python3: rules_pycross's
# wheel_installer runs as `/usr/bin/env python3` before any hermetic Python
# toolchain exists; zstd: //tools/lib32:lib32_extracted shells out to it to
# unpack a .deb.
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

# k3d for local Kubernetes work (e.g. Tilt-driven domain dev). Installed but
# untested/unused in this devcontainer today -- no cluster is created here
# and no Tilt tooling is wired in yet; that's follow-up work. docker-in-docker
# (added via devcontainer.json's `features`) is k3d's only runtime dependency.
ARG K3D_VERSION=v5.9.0
RUN curl -fsSL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh \
      | TAG=${K3D_VERSION} bash
