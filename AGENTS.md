# Everything Monorepo — Agent Instructions

## Behavioral Directives

- When refactoring libraries, search for usages and patches across the entire repo first.
- Provide short, straightforward responses. Elaborate only when necessary.
- Do not apologize for mistakes or praise the developer.
- If given a GitHub link for debugging, use GitHub MCP tools when available.
- Do not patch production environments — rely on release actions and human inputs.
- Read relevant docs before falling back to search or bash exploration.

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
| `PRODUCT.md` | Vision, capability map, load-bearing decisions, and milestone roadmap for the domain | Before scoping or designing anything in a domain built via `/project-manager:product` — see `tools/project-manager/CONVENTIONS.md` § Product brief & milestones |

Not every domain has all five files — `ENV.md` is only present where runtime configuration applies, `ARCHITECTURE.md` may be omitted for simple utilities, and `PRODUCT.md` only exists for a domain scoped through `/project-manager:product`.

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

**When to split.** If a doc can't be read in one pass — roughly 800–1000 lines / ~20K tokens — split it before adding more. That threshold sits under the ~25K-token single-`Read` an agent gets by default; past it, agents fall back to grep/search instead of reading cold. Re-check this every time a doc grows during review — a file that was fine at 600 lines can silently cross the line months later, and "length alone wasn't a reason" the last time someone looked doesn't mean it still isn't.

**How to split, by file type:**
- **Planning / status docs (`PLAN.md` and similar):** split current-state from history. Keep only what's true right now — status, open items, forward-looking scope — in the live file; move the as-built record of completed phases to a `*-HISTORY.md` sibling, linked from the live file's status table and not meant to be read start-to-finish. See `tools/app_registry/PLAN.md` / `PLAN-HISTORY.md`.
- **Reference docs with heavy internal cross-referencing (`ARCHITECTURE.md` and similar):** two variants, same underlying rule — the file must stop being where content *accumulates*.
  - **Under threshold, but growing:** an index-at-top (single file, `##` sections, a jump table by heading name) is enough — it costs nothing to maintain and there's no cross-reference risk yet, since nothing has moved.
  - **Over threshold:** move to a directory — one file per `##` section under `<DOC>/<NN>-<slug>.md` (recursing into a subdirectory for any one section that's still oversized on its own, e.g. `<DOC>/<NN>-<slug>/<MM>-<slug>.md`), with `<DOC>.md` itself rewritten down to a real index (a jump table linking every file, plus whatever handful of principles are short and referenced by number/name from everywhere — keep those inline rather than forcing a one-line file). Sections keep their exact heading text as each file's `# ` title, so a citation like `ARCHITECTURE.md "Reconcile watermark"` in a code comment stays discoverable by grepping the directory even before anyone updates the comment to the new path — grep-discoverability from unchanged prose is what makes rule 5 below tractable at volume (a comment citing a section by name is not the same maintenance burden as a `[text](#anchor)` link, which is a hard break and must be fixed). Fix anchor-style links (rule 5) always; bare prose citations in code/CI comments are lower priority at high volume — call out in the change what was left and why. See `tools/app_registry/architecture/` (directory) and `tools/app_registry/ARCHITECTURE.md` (the index + design principles that stayed inline) for a worked example, including the recursive case (`architecture/08-release-lifecycle/`).
- **Code modules:** split along the boundary the language already uses for imports — one package/module per concern. Watch for circular or stale imports introduced by the split (notably in Python, where circular imports fail late and confusingly).
- **Persona / role docs:** one file per persona or actor, cross-linked from an index, rather than one file describing every persona serially.

**Split mechanics:**

Priority order when these pull against each other: an algorithmic, agent-navigable split always wins; only minimize consumer impact within whatever that split allows — a split that's easy on a human but forces an agent to grep to find the right file is the wrong split.

1. **Pick an algorithmic boundary, not a line-count cut.** The boundary must be something an agent can name and predict *before* opening the doc — chronological phase, one-file-per-concern, current-vs-historical, one-per-persona. "First half" / "last N sections" / "wherever it got to 1000 lines" are not valid boundaries: they carry no meaning an agent can reason about on the next pass, so the doc will just need re-splitting on its own arbitrary terms next time.
2. **Name split files predictably.** Use a fixed suffix/prefix an agent can guess without reading an index: `<DOC>-HISTORY.md` for a current/history split, `<DOC>/<NN>-<slug>.md` for a directory split ordered by the same boundary as rule 1. Never split into ambiguously-named files (`part2.md`, `notes.md`, `misc.md`) — the name must say *which* boundary-value lives there.
3. **Keep one canonical entry point.** The original filename stays the file for "what's true now" / "where a cold read starts" — it must never become a content-free redirect stub. If a doc is fully superseded, delete it and repoint every inbound reference in the same change; don't leave a tombstone for an agent to open and bounce off of.
4. **Make every split file discoverable.** Add or update the entry in the domain's `TOC.md` (per "New files" above) with a one-line description of what's in the file and when to read it — a split that isn't indexed just relocates the grep-cold problem instead of solving it.
5. **Verify before merging, don't assume.** Grep the repo for the pre-split filename and any heading anchors that moved; fix every hit — other docs, `TOC.md`, code comments citing a heading or file, CI config — in the same change. A split that leaves dangling references is worse than not splitting.
6. **Re-check the split's own size.** Each resulting file should independently clear the size trigger in "When to split" above. Don't stop at two files if a natural third boundary already exists — that's deferring the same problem, not solving it.
7. **Only then, minimize consumer friction.** Within a boundary that satisfies 1–6, prefer the split that costs a normal reader the least — e.g. a reader who only wants current state shouldn't need to open the history file, and vice versa. This is a tiebreaker, not a reason to weaken the boundary itself.

## SCD2 (Slowly Changing Dimensions Type 2)

**Column convention — always use `valid_from` / `valid_to`:**
- `valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW()` — when this row became the current value
- `valid_to TIMESTAMPTZ` — when it was superseded; `NULL` = still current

Do not use synonyms (`assigned_at`/`unassigned_at`, `start_at`/`end_at`, etc.).

**Write path — close and open:**
```sql
UPDATE <table> SET valid_to = NOW() WHERE <entity_id> = $1 AND valid_to IS NULL;
INSERT INTO <table> (<entity_id>, <data_cols>) VALUES ($1, $2);
```

**Current value:**
```sql
SELECT * FROM <table> WHERE <entity_id> = $1 AND valid_to IS NULL;
```
Always back this with a partial index: `CREATE INDEX ON <table>(<entity_id>) WHERE valid_to IS NULL`.

**Value at time T** (e.g. joining a fact table to a history table at event time):
```sql
SELECT * FROM <table>
WHERE <entity_id> = $1
  AND valid_from <= $t
  AND (valid_to IS NULL OR valid_to > $t);
-- Example: leaflab's v_sensor_reading_with_plant joins plants active at recorded_at this way.
```

**Do not apply SCD2 to** append-only event logs or soft-delete tables — those have different semantics. If a table needs a SCD2-shaped view over it, derive one with a window function (`LEAD(valid_from) OVER (PARTITION BY entity_id ORDER BY version)`).

**Views:** Pre-join SCD2 history tables in `v_` views so downstream consumers (dashboards, APIs) never replicate the join logic. See `leaflab/` for a worked example.

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
| `audience_score_system/` | YouTube creator research/schedule/outcome tracking system, MCP-exposed (Go) — product brief only, no code yet | [PRODUCT](audience_score_system/PRODUCT.md) |
| `demo/` | Example applications — see individual READMEs | — |
| `generated/` | Auto-generated OpenAPI clients — do not edit manually | — |
