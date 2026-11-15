# Helm charts

Charts are generated from `release_app` metadata. You declare which apps go in
a chart; `helm_composer` (`//tools/helm:helm_composer`) writes the templates,
values, and `Chart.yaml`. This doc covers declaring, building, and releasing
charts. For what the generated chart contains and how to override values, see
[tools/helm/README.md](../tools/helm/README.md) and
[tools/helm/APP_TYPES.md](../tools/helm/APP_TYPES.md).

## Declare a chart

Use `release_helm_chart` so the chart is discoverable by release tooling:

```starlark
load("//tools/bazel:release.bzl", "release_helm_chart")

release_helm_chart(
    name = "fastapi_chart",
    apps = ["//demo/hello_fastapi:hello-fastapi_metadata"],
    chart_name = "hello-fastapi",
    domain = "demo",
    namespace = "demo",
)
```

| Attribute | Required | Default | Notes |
|-----------|----------|---------|-------|
| `apps` | yes | | `<app>_metadata` targets from `release_app` |
| `domain` | yes | | Prefixes the chart name |
| `namespace` | yes | | Kubernetes namespace |
| `chart_name` | no | `name` | Dashes only. Becomes `helm-<domain>-<chart_name>` |
| `chart_version` | no | `0.0.0-dev` | Leave unset. Release tooling sets the real version |
| `environment` | no | `production` | |
| `manual_manifests` | no | `[]` | Extra YAML or `k8s_manifests` targets to include |

The macro creates `:<name>` (the chart, tagged `manual`, so `bazel build //...`
skips it) and `:<name>_chart_metadata`. Each app's `app_type` decides which
resources it gets; ingress hosts, resources, and health checks come from
`release_app` attributes and can be overridden in values at deploy time.

The lower-level `helm_chart` rule in `//tools/helm:helm.bzl` takes the same
`apps`, `chart_name`, `chart_version`, `namespace`, `environment`, and
`manual_manifests`, but release tooling can't see it. Use it only for charts
you never publish.

### Names

A chart has three names. For the example above:

| Where | Name |
|-------|------|
| Bazel output and git tag | `helm-demo-hello-fastapi` (tag `helm-demo-hello-fastapi.vX.Y.Z`) |
| `Chart.yaml` and ChartMuseum | `demo-hello-fastapi` |
| Release inputs (`helm_charts`, `--charts`) | `helm-demo-hello-fastapi` or the domain, `demo` |

## Build and inspect locally

```bash
bazel build //demo:fastapi_chart
# -> bazel-bin/demo/helm-demo-hello-fastapi.tar.gz
# -> bazel-bin/demo/helm-demo-hello-fastapi_chart/demo-hello-fastapi/

helm lint bazel-bin/demo/helm-demo-hello-fastapi_chart/demo-hello-fastapi
helm template test bazel-bin/demo/helm-demo-hello-fastapi_chart/demo-hello-fastapi
```

Image tags in a Bazel-built chart are `latest`. To package a chart the way a
release does, with a version and pinned image tags:

```bash
bazel run //tools:release -- build-helm-chart helm-demo-hello-fastapi \
  --version v0.0.0-local --use-released=false --output-dir /tmp/charts
```

`--use-released` (the default) pins each member app to its latest
`<domain>-<app>.v*` git tag instead of `latest`, and fails if an app has none.

List every releasable chart with
`bazel query "kind('helm_chart_metadata', //...)"`.

## Releasing charts

Charts are released by the same workflows as apps; see
[RELEASE.md](RELEASE.md). In `release.yml`, set the `helm_charts` input
(chart name, domain, or `all`). Leave it empty to skip charts. App and chart
releases can run in the same dispatch or separately.

The `release-helm-charts` job runs `release_helper_go release-charts`, which
for each chart:

1. allocates the chart's own next version (charts are versioned independently
   of their apps);
2. composes the chart with each member app pinned to the version just released
   in this run, or its latest published version otherwise, and writes those
   pins to `image-lockfile.json` inside the chart;
3. asks the App Registry to confirm every pinned image is published (the
   hermeticity check). A violation fails the chart; a registry outage only
   warns;
4. packages the chart and uploads it to ChartMuseum, then records it in the
   App Registry.

If a different package already sits at the candidate version in ChartMuseum,
the release moves to the next version rather than overwrite it. Packages are
compared by their extracted file contents, not tarball bytes, because
`helm package` output isn't byte-reproducible. A retry of an unchanged chart
reuses the existing version.

In `release-v2.yml` the chart source tree is built in CI and the App Registry
worker packages and uploads it with `release_helper_go finalize-chart`.

## Chart repository

Charts are published to ChartMuseum at `https://charts.whalenet.dev`, behind
basic auth.

```bash
helm repo add whalenet https://charts.whalenet.dev --username <user> --password <pass>
helm repo update
helm search repo whalenet
```

| Setting | Kind | Purpose |
|---------|------|---------|
| `CHART_REPO_USER`, `CHART_REPO_PASS` | Secrets | ChartMuseum credentials |
| `CHART_REPO_URL` | Variable | ChartMuseum base URL; defaults to `https://charts.whalenet.dev`. Also used for the repository recorded in the App Registry and the `helm repo add` line in the run summary |

The workflows don't delete chart versions. Removing one means calling
ChartMuseum's delete API directly.

## Tests

```bash
bazel test //tools/helm:helm_test          # composer and type unit tests
bazel test //tools/helm:integration_test   # manual; needs helm on PATH, lints //demo:fastapi_chart
```
