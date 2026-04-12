# Everything Monorepo — Agent Instructions

## Behavioral Directives

- When refactoring libraries, search for usages and patches across the entire repo first.
- Provide short, straightforward responses. Elaborate only when necessary.
- Do not apologize for mistakes or praise the developer.
- If given a GitHub link for debugging, use GitHub MCP tools when available.
- Do not patch production environments — rely on release actions and human inputs.
- Read relevant docs before falling back to search or bash exploration.

## Bazel — Default Build, Test, and Query Tool

Use Bazel as the primary tool for building, running, testing, and exploring the codebase. Do not fall back to `go build`, `go test`, `python`, or direct binary invocations unless you have confirmed there is no Bazel target for the task.

**Build and run:**
```
bazel build //path/to/target
bazel run //path/to/target
```

**Test — always use Bazel for tests:**
```
bazel test //path/to/...          # all tests in a subtree
bazel test //path/to:specific_test
```

**Query — use before reading files to understand structure and dependencies:**
```
bazel query //path/to/...                         # list all targets
bazel query 'deps(//some:target)'                 # transitive deps
bazel query 'rdeps(//..., //some:lib)'            # reverse deps (who uses this?)
bazel query 'kind(go_binary, //...)'              # find targets by rule type
bazel query 'attr(name, foo, //...)'              # find by attribute
```

**When to break out of Bazel:** Only use raw shell commands, `go` tooling, or direct interpreters when a task explicitly requires it (e.g. interacting with a live process, running a one-off script with no BUILD target, or debugging a Bazel configuration issue itself).

## ⚠️ Critical: Cross-Compilation

Before touching image builds, platform targets, or container tooling: read [`docs/DOCKER.md`](docs/DOCKER.md).

This repo uses true cross-compilation for ARM64. Breakage is **silent at build time** and only fails at runtime. If `image-integration` tests fail, **do not merge**.

## Documentation Conventions

Each domain follows a standard file set. Use these as your primary reference before searching.

| File | Purpose | When to read it |
|------|---------|-----------------|
| `README.md` | Setup, local dev, and general usage | Starting work in a domain |
| `ARCHITECTURE.md` | System design, component relationships, data flow | Before making structural or cross-cutting changes |
| `ENV.md` | All environment variables for the domain or component | Configuring, deploying, or debugging runtime behavior |
| `TOC.md` | Index of concepts pointing to deeper docs | Finding domain-specific docs on a topic |

Not every domain has all four files — `ENV.md` is only present where runtime configuration applies, `ARCHITECTURE.md` may be omitted for simple utilities.

### Navigation Protocol

**Starting work in a domain:**
1. Read the domain's `TOC.md` first — it lists what docs exist and when each one is relevant to a specific task or question.
2. Read the specific file the TOC points you to. Do not read everything — use the TOC entry's description to decide if it applies to your current task.
3. If no TOC exists, read `README.md` then `ARCHITECTURE.md`.

**Cross-domain work:**
When a task touches multiple domains (e.g. modifying a shared library used by an app, or wiring a new tool into the release pipeline), navigate to each affected domain's `TOC.md` before making changes. The Domains table below is your cross-domain map — if you are unsure whether a change affects another domain, check its `TOC.md` and `ARCHITECTURE.md` before proceeding.

**When docs are missing or stale:**
If a relevant doc file is a skeleton (`<!-- TODO: -->`) or clearly out of date, fall back to reading source code and `BUILD.bazel` files directly. Do not treat a skeleton file as authoritative.

### Maintaining Docs

Update documentation as part of the same task that changes the code — not as a separate follow-up. The standard files have clear ownership:

| File | Update when... |
|------|---------------|
| `README.md` | Setup steps change, new commands are added, ports/services change |
| `ARCHITECTURE.md` | A component is added/removed, a data flow or integration changes, a key design decision is made |
| `ENV.md` | An environment variable is added, removed, renamed, or its behaviour changes |
| `TOC.md` | A new doc file is created, a file is moved or deleted, or a new concept emerges that an agent would need to find |

**Scope:** Only update what your change actually affects. Do not rewrite a doc because it could be better — only correct what is now wrong or missing.

**New files:** If you create a doc that isn't one of the four standard files (e.g. a component-specific guide or style doc), add an entry to the domain's `TOC.md` so it is discoverable.

## Domains

| Domain | Description | Reference |
|--------|-------------|-----------|
| `manmanv2/` | Active game server orchestration platform (Go + Python) | [TOC](manmanv2/TOC.md) |
| `manman/` | Legacy V1 system — maintenance mode only | [TOC](manman/TOC.md) |
| `libs/` | Shared Python and Go libraries | [TOC](libs/TOC.md) |
| `tools/` | Build, release, Helm, and development tooling | [TOC](tools/TOC.md) |
| `friendly_computing_machine/` | Slack bot with Temporal workflows | [TOC](friendly_computing_machine/TOC.md) |
| `docs/` | Cross-cutting infrastructure and build system docs | [TOC](docs/TOC.md) |
| `firmware/` | Board-agnostic C++ sensor libraries (ISensor, II2CBus, MQTTWriter) | [TOC](firmware/TOC.md) |
| `leaflab/` | Plant monitoring firmware and data pipeline | [TOC](leaflab/TOC.md) |
| `demo/` | Example applications — see individual READMEs | — |
| `generated/` | Auto-generated OpenAPI clients — do not edit manually | — |
