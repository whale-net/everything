# everything

The whale-net monorepo. Go, Python, and ESP32 firmware, all built with Bazel.
Apps ship as multi-arch OCI images with generated Helm charts.

## What's in here

| Directory | What it is |
|-----------|------------|
| [`manmanv2/`](manmanv2/README.md) | Game server orchestration — control plane, bare-metal host managers, web UI (Go) |
| [`manman/`](manman/README.md) | ManMan V1. Python, maintenance only; new work goes in `manmanv2/` |
| [`whagent_net/`](whagent_net/README.md) | AI agent framework: Temporal-backed sessions, transcript store, MCP, embeddable UI (Go) |
| [`krill/`](krill/README.md) | Spec-of-record and work tracking for agent swarms (Go) |
| [`audience_score_system/`](audience_score_system/README.md) | YouTube research → schedule → outcome tracking, exposed over MCP (Go) |
| [`friendly_computing_machine/`](friendly_computing_machine/README.md) | Slack bot running on Temporal (Python) |
| [`leaflab/`](leaflab/README.md) | Plant monitoring: ESP32 sensor boards plus the data pipeline behind them |
| [`firmware/`](firmware/README.md) | Board-agnostic C++ sensor, MQTT, and networking libraries |
| [`libs/`](libs/README.md) | Shared Go and Python libraries (logging, db, migrations, htmx UI, auth, …) |
| [`tools/`](tools/README.md) | Bazel rules, release tooling, Helm generation, Tilt, app registry, MCP servers |
| [`demo/`](demo/) | Small example apps; useful as templates |
| [`generated/`](generated/) | Generated OpenAPI clients. Don't edit by hand |
| [`docs/`](docs/TOC.md) | Build, release, and infra docs that span domains |

Each domain has a `TOC.md` that lists its docs. Start there, not with grep.

## Getting started

You need [Bazelisk](https://github.com/bazelbuild/bazelisk) (it reads
`.bazelversion` and fetches Bazel 8.3.1), Docker, and [uv](https://docs.astral.sh/uv/)
for Python dependency work. Full setup, including WSL2 notes, is in
[docs/SETUP.md](docs/SETUP.md).

```bash
bazel run //demo/hello_go:hello-go         # sanity check
bazel test //manmanv2/...                  # test one domain
bazel test //...                           # test everything
```

To find out what exists before reading code, query Bazel:

```bash
bazel query //krill/...                            # targets in a domain
bazel query 'rdeps(//..., //libs/go/logging)'      # who uses a library
bazel query "kind('app_metadata', //...)"          # every releasable app
```

### Running a domain locally

Domains with services have their own `Tiltfile`. Run `tilt up` from that
domain's directory, for example `cd manmanv2 && tilt up`. The domain README
lists its prerequisites (usually a local Kubernetes cluster).

### Firmware

Host-side firmware tests need no hardware:

```bash
bazel test //firmware/...
bazel build //demo/blink:blink_bin --config=esp32
bazel run //demo/blink:flash -- /dev/ttyUSB0
```

See [tools/firmware/README.md](tools/firmware/README.md) for toolchains and
flashing from WSL2, and [docs/ADDING_A_BOARD.md](docs/ADDING_A_BOARD.md) to add a board.

## How apps ship

Wrap a `go_binary` or `py_binary` in `release_app` and it becomes releasable.
CI then builds the image, generates the chart, and versions it with a git tag.
Releases run from the **Release** workflow in GitHub Actions. Change detection
picks which apps to rebuild.

- [ARCHITECTURE.md](ARCHITECTURE.md): the build → image → chart → cluster pipeline
- [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md): adding an app or library
- [docs/DEPENDENCIES.md](docs/DEPENDENCIES.md): Go and Python dependencies
- [docs/DOCKER.md](docs/DOCKER.md): read before touching images
- [docs/RELEASE.md](docs/RELEASE.md) and [docs/HELM.md](docs/HELM.md): releases and charts

## Working with agents

[AGENTS.md](AGENTS.md) holds the conventions coding agents follow here.
Humans should read it too, since it covers logging levels, SCD2 tables,
migration tests, and UI rules. The `project-manager` plugin installs
automatically in [pi](https://pi.dev) sessions here via
[.pi/claude-plugins.json](.pi/claude-plugins.json). Put local overrides in
`.pi/claude-plugins.local.json`, which is gitignored.
