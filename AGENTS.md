# Everything Monorepo — Agent Instructions

## Behavioral Directives

- When refactoring libraries, search for usages and patches across the entire repo first.
- Provide short, straightforward responses. Elaborate only when necessary.
- Do not apologize for mistakes or praise the developer.
- If given a GitHub link for debugging, use GitHub MCP tools when available.
- Do not patch production environments — rely on release actions and human inputs.
- Read relevant docs before falling back to search or bash exploration.

## Finishing a Task

When you finish a task, tell the user exactly how to verify it themselves:
the Bazel commands to run, the inputs to provide, or the steps to reproduce.
Prefer verification that a human should perform directly (concrete manual
behavior checks) over only listing test commands. Don't leave the user
guessing how to confirm the result — tell them exactly what to do.

## Code Comments

Keep comments short and focused on the code, not on the change history.

- **Brief** — one or two lines; avoid more than three. If you need more, the
  code likely needs refactoring or a doc string, not a wall of inline
  commentary.
- **Describe the scenario, not the change** — explain *what* the code
  handles or *why* it exists, in terms a future reader needs. Don't
  reference PR numbers, issue numbers, or ticket IDs (`#1646`,
  `fixes JIRA-123`) — the scenario should be clear without chasing external
  links.

## Logging Levels

Use these levels consistently across all code (Go, Python, etc.) — they signal severity to on-call humans and downstream alerting, so don't pick one by feel:

- **INFO** — something notable happened and completed normally; worth knowing but requires no action (e.g. a sync finished, a schedule was created).
- **WARNING** — the system had to adjust something to keep going (e.g. a fallback path was taken, a retry succeeded, an optional dependency was skipped) — the operation still completed but not exactly as expected.
- **ERROR** — the system cannot continue the current operation; something failed and needs attention.

Do not log expected/handled control flow at WARNING or ERROR — reserve those for genuine deviations or failures.

## Effective Subagent Usage

Prompt-cache read cost per turn grows with a session's own turn count (roughly 9x higher in 300-500 turn sessions vs. under-50-turn sessions, measured across this account's history) — every turn re-sends and re-reads the full prior transcript, so cost compounds as a session's transcript grows. Subagents are one of the two effective levers against this (the other is starting a fresh session); use them to keep the *main* session's turn count down, not as an end in themselves.

- **Fork/spawn a subagent for exploratory or investigative work whose intermediate output you don't need to keep**: multi-step searches, log/codebase investigations, research questions, broad reads across many files. This is the highest-leverage use — it keeps the heavy tool-output slog (grep noise, file reads, search results) out of the main transcript entirely, instead of dumping it into the parent session where it gets re-read on every subsequent turn.
- **Don't defeat the purpose by pulling detail back in.** Spawning a subagent and then asking for its full transcript, or requesting verbose intermediate output, reintroduces the cost the fork was supposed to avoid. Ask for a synthesized result, not a raw dump.
- **Don't chain many small one-off subagent calls** for trivial lookups — each spawn pays its own cache-write on shared context (system prompt, tool schemas) without meaningfully shrinking the parent transcript. Batch related exploration into one fork when possible.
- **When a single session is running long from inline exploration** (not delegated work), prefer forking the next investigative step rather than continuing to accumulate turns in the main thread.

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
# For fast kind() discovery across //...:
#   bazel query 'kind("foo", //...)' --universe_scope=//... --noimplicit_deps --nodep_deps --output=label
```

**When to break out of Bazel:** Only use raw shell commands, `go` tooling, or direct interpreters when a task explicitly requires it (e.g. interacting with a live process, running a one-off script with no BUILD target, or debugging a Bazel configuration issue itself).

## Documentation Conventions

Each domain follows a standard file set. Use these as your primary reference before searching.

| File | Purpose | When to read it |
|------|---------|-----------------|
| `README.md` | Setup, local dev, and general usage | Starting work in a domain |
| `ARCHITECTURE.md` | System design, component relationships, data flow | Before making structural or cross-cutting changes |
| `ENV.md` | All environment variables for the domain or component | Configuring, deploying, or debugging runtime behavior |
| `TOC.md` | Index of concepts pointing to deeper docs | Finding domain-specific docs on a topic |
| `PRODUCT.md` | Vision, capability map, load-bearing decisions, and milestone roadmap for the domain | Before scoping or designing anything in a domain built via `/project-manager:product` — see `tools/project-manager/CONVENTIONS.md` § Product brief & milestones |

Not every domain has all five files — `ENV.md` is only present where runtime configuration applies, `ARCHITECTURE.md` may be omitted for simple utilities, and `PRODUCT.md` only exists for a domain scoped through `/project-manager:product`.

### Generated-doc carve-out: krill-rendered `PRODUCT.md` / `product/*`

A domain whose product brief has been migrated into `krill/` (today, only `krill/` itself) has its `PRODUCT.md` and `product/*.md` files rendered by `krill/render`, not hand-authored — a hand edit is **silently lost** on the next re-render, since there's no reconciliation step. See `krill/render/README.md` for the full rule (why, and how to change these files correctly). Every other domain's `PRODUCT.md`, and every domain's `ARCHITECTURE.md`/`README.md`/`ENV.md`/`TOC.md` (even krill's own), stay ordinary hand-authored docs.

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
| `PRODUCT.md` | A milestone ships or is amended, a load-bearing decision is revisited, or the roadmap is re-cut — via `/project-manager:product`, never a hand edit |

**Scope:** Only update what your change actually affects. Do not rewrite a doc because it could be better — only correct what is now wrong or missing.

**New files:** If you create a doc that isn't one of the five standard files (e.g. a component-specific guide or style doc), add an entry to the domain's `TOC.md` so it is discoverable.

### Size Limits & Splitting

**When to split.** If a doc can't be read in one pass — roughly 800–1000 lines / ~20K tokens — split it before adding more. Re-check this every time a doc grows during review — a file that was fine at 600 lines can silently cross the line months later.

Splitting follows a fixed set of rules, several referenced by number elsewhere in this repo:

1. **Pick the boundary before it's needed, not at the moment a line-count trips** — and pick an algorithmic one (chronological phase, one-file-per-concern, current-vs-historical, one-per-persona) a future agent can predict, never a line-count cut.
2. Name split files predictably (`<DOC>-HISTORY.md` for a current/history split, `<DOC>/<NN>-<slug>.md` for a directory split).
3. Keep **one canonical entry point** — the original filename must never become a content-free redirect stub.
4. Index every split file from the domain's `TOC.md`.
5. Grep-verify no dangling references before merging. In a directory split, each file keeps its section's exact heading text as its own title, so a citation by heading name stays grep-discoverable even before it's updated to the new path.
6. Re-check each resulting file's own size — don't stop at two files if a natural third boundary already exists.
7. Only then, minimize a normal reader's friction — a tiebreaker, not a reason to weaken the boundary.

For file-type-specific guidance (planning docs, heavily cross-referenced reference docs, code modules, persona docs) and worked examples, see the `doc-splitting` skill.

## SCD2 (Slowly Changing Dimensions Type 2)

**Column convention — always use `valid_from` / `valid_to`:**
- `valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW()` — when this row became the current value
- `valid_to TIMESTAMPTZ` — when it was superseded; `NULL` = still current

Do not use synonyms (`assigned_at`/`unassigned_at`, `start_at`/`end_at`, etc.).

**Do not apply SCD2 to** append-only event logs or soft-delete tables — those have different semantics. If a table needs a SCD2-shaped view over it, derive one with a window function (`LEAD(valid_from) OVER (PARTITION BY entity_id ORDER BY version)`) instead of adding real `valid_to` writes.

For the close-and-open write path, current-value/point-in-time query patterns, the partial-index convention, and worked examples, see the `architecture-scd2` skill.

## GitHub Labels

Beyond the standard `bug`/`enhancement`/`chore`/etc. and the project-manager plugin's lifecycle labels (`product:*`, `plan:*`, `phase:*`, `status:*`), apply these when filing or triaging issues:

- `idea` — not yet a concrete task
- `high-effort` / `low-effort` — sizing, for human token-budget triage
- `agent:ready` — safe for an agent to pick up unattended; combine with the above for queries
- `source:scope-note` — deferred item surfaced during a plan
- `source:validation` — generated from system-validator findings
- `type:spike` — investigation/exploration, not a concrete deliverable
- `domain:<name>` — apply to root `Plan:` issues so plans are filterable by domain later (matches the Domains table below, e.g. `domain:manmanv2`, `domain:app-registry`)
- `duplicate` — flag actual duplicate issues; check before filing

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
| `audience_score_system/` | YouTube creator research/schedule/outcome tracking system, MCP-exposed (Go) | [TOC](audience_score_system/TOC.md) |
| `whagent_net/` | whagent-net — Temporal-backed AI agent framework: session service, transcript store, MCP surface, embeddable session UI (Go) | [TOC](whagent_net/TOC.md) |
| `krill/` | krill — spec-of-record and work-tracking substrate for agent swarms | [TOC](krill/TOC.md) |
| `demo/` | Example applications — see individual READMEs | — |
| `generated/` | Auto-generated OpenAPI clients — do not edit manually | — |
