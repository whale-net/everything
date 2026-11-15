# Architecture

How an app gets from source to a cluster. Read this before adding an app or
touching the release pipeline. Domain-internal design lives in each domain's
own `ARCHITECTURE.md`.

## The pipeline

```
go_binary / py_binary
  └─ release_app            → <app>_metadata, <app>_image (amd64 + arm64)
       └─ release_helm_chart → chart tarball + chart metadata
            └─ Release workflow → image in GHCR, chart in ChartMuseum, GitHub Release
                 └─ ArgoCD (outside this repo) → cluster
```

Everything up to the publish step is Bazel. Publishing is done by
`release_helper_go` (`//tools:release`) running in GitHub Actions. Deployment
is not in this repo: ArgoCD reads values files maintained elsewhere.

## Repository layout

```
├── manmanv2/                    # Game server orchestration (Go)
├── manman/                      # ManMan V1 (Python, maintenance only)
├── whagent_net/                 # AI agent framework on Temporal (Go)
├── krill/                       # Spec-of-record and work tracking for agent swarms (Go)
├── audience_score_system/       # YouTube research/schedule/outcome tracking (Go)
├── friendly_computing_machine/  # Slack bot on Temporal (Python)
├── leaflab/                     # Plant monitoring firmware + data pipeline
├── firmware/                    # Board-agnostic C++ sensor/MQTT libraries
├── libs/                        # Shared Go and Python libraries
├── tools/                       # Bazel rules, release tooling, Helm, Tilt, App Registry
├── demo/                        # Example apps and charts
├── generated/                   # Generated OpenAPI clients (don't edit)
├── docs/                        # Cross-domain build/release/infra docs
├── .github/workflows/           # CI and release workflows
└── MODULE.bazel                 # Bazel module and external deps
```

## `release_app`: make a binary releasable

```starlark
load("//tools/bazel:release.bzl", "release_app")

release_app(
    name = "my-app",               # dashes, not underscores
    binary_name = ":my-app",       # defaults to :<name>
    language = "go",               # "go" or "python" for container apps
    domain = "demo",
    app_type = "internal-api",     # external-api | internal-api | worker | job | cli | binary | firmware
    port = 8080,
    description = "What it does",
)
```

For container app types this creates:

| Target | What it is |
|--------|------------|
| `my-app_metadata` | `app_metadata` rule; what release tooling and Helm read |
| `my-app_image` | Multi-arch OCI image index (`linux/amd64`, `linux/arm64`) |
| `my-app_image_load` | Loads one arch into local Docker as `<domain>-<app>:latest` |
| `my-app_image_push` | Pushes the index (release tooling calls this; you normally don't) |

`cli`, `binary`, and `firmware` apps get metadata but no image. They ship as
GitHub Release assets instead. The full parameter list is in the docstring in
`tools/bazel/release.bzl`.

Find every releasable app:

```bash
bazel query "kind('app_metadata', //...)"
bazel run //tools:release -- list
```

## Images

Images are named `<domain>-<app>` and pushed to `ghcr.io/whale-net/`. Both
architectures are built from the same binary target through platform
transitions (`//tools:linux_x86_64`, `//tools:linux_arm64`, defined in
`tools/bazel/platforms.bzl`). Loading locally requires picking one:

```bash
bazel run //demo/hello_python:hello-python_image_load --platforms=//tools:linux_x86_64
docker run --rm demo-hello-python:latest
```

Go binaries are static, so cross-compiling them is uneventful. Python is not:
`rules_pycross` picks per-platform wheels from `uv.lock`, and a wrong wheel
builds fine and then crashes on arm64 at runtime. Read
[docs/DOCKER.md](docs/DOCKER.md) before changing image or Python dependency
wiring. The `Test Container Architecture` CI job
(`bazel test //tools/scripts:test_cross_compilation`) catches this on pushes
to `main`.

## Charts

`release_helm_chart` composes one Helm chart from one or more apps' metadata.
The `app_type` decides which Kubernetes resources each app gets:

| `app_type` | Deployment | Service | Ingress | Job |
|------------|-----------|---------|---------|-----|
| `external-api` | yes | yes | yes | — |
| `internal-api` | yes | yes | opt-in | — |
| `worker` | yes | — | — | — |
| `job` | — | — | — | yes |

```starlark
load("//tools/bazel:release.bzl", "release_helm_chart")

release_helm_chart(
    name = "my_chart",
    apps = ["//demo/my_app:my-app_metadata"],
    chart_name = "my-app",   # becomes helm-demo-my-app
    domain = "demo",
    namespace = "demo",
)
```

See [docs/HELM.md](docs/HELM.md) for building and releasing charts, and
[tools/helm/README.md](tools/helm/README.md) /
[tools/helm/APP_TYPES.md](tools/helm/APP_TYPES.md) for what the generated
chart contains.

## Releasing

Releases are dispatched by hand from the **Release** workflow
(`.github/workflows/release.yml`), or from the App Registry UI, which drives
`release-v2.yml` through Temporal. Versions are allocated by the App Registry.
See [docs/RELEASE.md](docs/RELEASE.md).

## Dependencies and generated code

- Python: `pyproject.toml` + `uv.lock`, referenced as `@pypi//:<package>`.
  `uv.lock` must keep wheels for every platform.
- Go: one root `go.mod`, wired into Bazel by Gazelle's `go_deps` in
  `MODULE.bazel`.
- OpenAPI clients: the `openapi_client` macro
  (`tools/openapi/openapi_client_rule.bzl`) generates them into
  `//generated/`. Run `./tools/scripts/sync_generated_clients.sh` to copy them
  out of `bazel-bin` for your IDE. See
  [tools/client_codegen/README.md](tools/client_codegen/README.md).

[docs/DEPENDENCIES.md](docs/DEPENDENCIES.md) covers adding either kind of
dependency.

## CI

`ci.yml` runs on every PR and on pushes to `main`: tests, the cross-arch image
check (main only), and the App Registry reconcile that registers every
`release_app`/`release_helm_chart` manifest (main only). It does not publish
images. Details are in [docs/CI_CD.md](docs/CI_CD.md).

## Local development

Domains with services have a `Tiltfile`; run `tilt up` from the domain
directory. See [tools/tilt/README.md](tools/tilt/README.md).
