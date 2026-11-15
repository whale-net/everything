# Logging Environment Variables

The Go (`//libs/go/logging`) and Python (`//libs/python/logging`) logging
libraries fill in service identity from environment variables, so app code
doesn't hardcode its own name, version, or environment. This doc lists those
variables, where they come from, and how the two libraries differ.

For the APIs themselves, see [libs/go/logging/README.md](../libs/go/logging/README.md)
and [libs/python/logging/README.md](../libs/python/logging/README.md).

## Variables

A value passed explicitly in code (`logging.Config` fields in Go, keyword
arguments to `configure_logging()` in Python) wins over the environment.
Where two variables are listed, the first one set wins.

| Variable | Used as | Default if unset |
|----------|---------|------------------|
| `APP_NAME` | `service.name` | `unknown-app` |
| `APP_DOMAIN` | `service.namespace` | empty |
| `APP_TYPE` | `service.type` | empty |
| `APP_VERSION` | `service.version` | `latest` |
| `APP_ENV`, `ENVIRONMENT` | `deployment.environment` | `development` |
| `GIT_COMMIT`, `COMMIT_SHA` | `vcs.commit.id` (first 8 chars also become `service.instance.id`) | empty |
| `POD_NAME`, `HOSTNAME` | `k8s.pod.name` | empty |
| `NAMESPACE`, `POD_NAMESPACE` | `k8s.namespace.name` | empty |
| `NODE_NAME` | `k8s.node.name` | empty |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP collector address (gRPC, insecure) | Go: `localhost:4317`, Python: `http://localhost:4317` |

Python-only:

| Variable | Used as |
|----------|---------|
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | Log exporter endpoint; checked before `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Trace exporter endpoint; checked before `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `CONTAINER_NAME` | `k8s.container.name` |
| `HOSTNAME` | `host.name` (in addition to the `POD_NAME` fallback) |
| `PLATFORM`, `ARCHITECTURE` | `host.arch`, `host.cpu.family` |
| `HELM_CHART_NAME`, `HELM_RELEASE_NAME` | `helm.chart.name`, `helm.release.name` |
| `BAZEL_TARGET` | `build.target` |

Go strips any `http://` or `https://` prefix from the endpoint. Python hands
the value to the OTel gRPC exporter unchanged. The `http://host:4317` form the
Helm charts set works for both.

## Turning OTLP on and off

The two libraries have different defaults:

| | Go `logging.Config` | Python `configure_logging()` |
|---|---|---|
| Log export | `EnableOTLP`, default off | `enable_otlp`, default on |
| Tracing | `EnableTracing`, default off | `enable_tracing`, default off |
| Metrics | `EnableMetrics`, default off | separate `configure_metrics()` call |

Every long-running Go service must set `EnableOTLP: true` and
`EnableTracing: true` and call `logging.Shutdown` on every exit path. A missing
flag fails silently: the service just never shows up in Grafana. See the
Observability section of [AGENTS.md](../AGENTS.md) and the
`architecture-observability` skill.

These variables switch signals off without a code change. They override
whatever the code asked for:

| Variable | Go | Python |
|----------|----|--------|
| `OTEL_SDK_DISABLED=true` | Disables logs, traces, and metrics | Disables tracing only; log export still runs |
| `OTEL_LOGS_DISABLED=true` | Disables OTLP log export | Ignored |
| `OTEL_TRACES_DISABLED=true` | Disables tracing | Disables tracing |
| `OTEL_METRICS_DISABLED=true` | Disables metrics | Ignored |

Go accepts only `true` (any case). Python also accepts `1` and `yes`.

To run a Python app locally without a collector, pass `enable_otlp=False`;
`OTEL_SDK_DISABLED` won't stop its log exporter.

## Where the values come from

**Container image.** `release_app` (`tools/bazel/release.bzl`) bakes
`APP_NAME`, `APP_DOMAIN`, and `APP_TYPE` into every image it builds, so they're
present even under plain `docker run`. To see them for an app:

```bash
bazel build //demo/hello_fastapi:hello-fastapi_image_base
cat bazel-bin/demo/hello_fastapi/hello-fastapi_image_base.env.txt
```

**Generated Helm charts.** `tools/helm/templates/deployment.yaml.tmpl` and
`job.yaml.tmpl` set the rest when the pod starts:

| Variable | Value |
|----------|-------|
| `APP_NAME`, `CONTAINER_NAME`, `OTEL_SERVICE_NAME` | App name |
| `APP_DOMAIN` | App's `domain`, else `global.domain` |
| `APP_TYPE` | App's `type` |
| `APP_VERSION` | App's `imageTag` |
| `APP_ENV`, `ENVIRONMENT` | `global.environment` |
| `GIT_COMMIT`, `COMMIT_SHA` | App's `commitSha`, only if set |
| `POD_NAME`, `HOSTNAME` | `metadata.name` (downward API) |
| `POD_NAMESPACE`, `NAMESPACE` | `metadata.namespace` (downward API) |
| `NODE_NAME` | `spec.nodeName` (downward API) |
| `HELM_CHART_NAME`, `HELM_RELEASE_NAME` | `.Chart.Name`, `.Release.Name` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `otlp.endpoint`, default `http://otel-collector:4317` |
| `PLATFORM`, `ARCHITECTURE` | Hardcoded `linux/amd64`, `amd64` |

The chart composer (`tools/helm/composer.go`) never fills in `commitSha` yet,
so `GIT_COMMIT`/`COMMIT_SHA` are absent from generated charts and
`vcs.commit.id` is missing in practice.

**Anywhere else** (a binary run directly, a container started by a manmanv2
host manager, a Tiltfile that doesn't use the generated charts) you set these
yourself or pass the values in code.

## Running locally

```bash
APP_NAME=hello-logging APP_DOMAIN=demo APP_ENV=dev \
  bazel run //demo/hello_logging:hello_logging
```

The first log line is `Logging configured for hello-logging` with the
resolved environment and domain attached; check it to confirm the values were
picked up. It also carries `auto_detected: true` when no identity arguments
were passed in code. Go services log `logging configured` with
`service_name`, `environment`, and `domain` the same way.
