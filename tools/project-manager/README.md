# project-manager

AGY and Claude Code plugin providing a multi-persona project-management pipeline for the
`everything` monorepo, tracked entirely in GitHub plus one committed doc: optional product scoping into a **`<domain>/PRODUCT.md` spec**, tracked by a thin **product Issue**; intake, design drafting, and architect reconciliation in a **Discussion**; the final approved root plan as an **Issue**; task breakdown and task execution progressing through **swimlanes** on a GitHub **Project (v2)** board. See [`CONVENTIONS.md`](CONVENTIONS.md) for the full contract.

**Scoping first, for anything product-sized.** A single `design` pass over a whole product produces 60-80 FRs — too big to implement in one shot and too big to hold in context. `/project-manager:product` cuts that up front: a capability map instead of requirements, the **load-bearing decisions** later capabilities depend on, and a roadmap of milestones each defined by one user-visible outcome. The rest of the pipeline then runs once per milestone, small enough to be safe, with architect checking each milestone's spec against the load-bearing decisions so *small* doesn't mean *painted into a corner*. A feature added to an existing system skips this and goes straight to `design`.

## Personas

| Persona | Role | Model |
|---|---|---|
| `project-manager` | Lightweight single-session planning; doesn't use the GitHub pipeline | sonnet |
| `producer` | Runs intake in a GitHub Discussion, interviews requester, drafts requirements/user stories, reconciles with architect in the Discussion, and publishes the final approved root plan Issue. Also writes the product brief (capability map, milestone roadmap) in its `P0`–`P3` modes | opus |
| `architect` | Reconciles the plan against repo conventions inside the Discussion, asks questions via Discussion comments, and signs off when ready for human review. In product mode, writes the current-state survey and the load-bearing decisions; on each milestone, checks the draft doesn't foreclose them | opus |
| `stakeholder` | Represents exactly one persona from the spec during a stakeholder meeting round; posts guidance, non-blocking feedback, and numbered blockers | sonnet |
| *(human)* | Reviews the architect-approved draft in the Discussion, either approves it (triggering root plan Issue creation) or requests changes | — |
| `planner` | Converts an approved root plan Issue into a GitHub Project with swimlanes and task issues | opus |
| `worker` | Executes tasks in `Scaffold`, `Implementation`, and `Testing` swimlanes inside a dedicated worktree, commits to the task's own `gh stack` branch, and advances tasks to the next swimlane | haiku |
| `validator` | Checks a task's acceptance criteria in the `Validation` swimlane against merged work (read-only), moves to `Done`, and closes the issue | haiku |
| `mergepush` | Dispatched once per batch by `implement`/`validate` after worker/validator return: removes each finished task's worktree, then registers its branch into the plan's one real `gh stack` stack via `gh stack link` — pushes it, opens/refreshes its PR, and corrects the PR base so the plan stays one reviewable, atomically-mergeable stack (`main → task-a → task-b → ...`) instead of a tree, without needing to touch the branch's own git history — and sets title/body on first creation. Keeps the orchestrator's own context free of `git`/`gh stack` command output | haiku |
| `system-validator` | Runs the whole system end-to-end in Tilt against the root plan's criteria once all tasks are `Done`; files follow-up findings if needed | opus (max effort) |
| `help` | Triage: reads a free-form question and recommends the exact next skill/command, grounded in `CONVENTIONS.md` and (when named) a plan's live GitHub state. Never writes anything | sonnet |

## Pipeline

```
(product-sized request only)
(human) ──▶ Product Discussion ──▶ producer ──vision/personas/capability map──▶ architect ──current state + load-bearing decisions──▶ producer ──roadmap──▶ architect
                                                                    │   (loop until architect sign-off)
                                                                    ▼
                                                          (human) product gate
                                                                    │
                                                                    ▼
                              producer commits <domain>/PRODUCT.md (doc PR) + tracking Issue (product:approved)
                                            capability map C1..Cn · load-bearing LB1..LBn · milestones M1..Mn
                                            (roadmap ledger tracked as comments on the tracking Issue)
                                                                    │
                                            ┌───────────────────────┘  once per milestone: design --milestone M<n>
                                            ▼
(human) ──intake──▶ Discussion ──▶ producer ──user stories/FR/NFR──▶ architect ──questions──▶ producer
                                                                           │   (loop in Discussion until architect sign-off)
                                                                           ▼
                                                     stakeholder meeting (optional: --stakeholder-meeting)
                                                     one stakeholder per persona ──blockers──▶ producer/architect (re-loop)
                                                                           │   (cleared)
                                                                           ▼
                                                                   (human) review gate
                                                                    │                  │
                                                            approved      feedback ──▶ producer/architect (loop in Discussion)
                                                                    │
                                                                    ▼
                                                       producer creates root Issue (plan:approved)
                                                                    │
                                                                    ▼
                                                        /project-manager:plan ──dispatches──▶  planner  ──creates──▶  Project board with Swimlanes:
                                                                                        [Scaffold] ──▶ [Implementation] ──▶ [Testing] ──▶ [Validation] ──▶ [Done]
                                                                                        (Depends on: #n drives readiness; task moves across swimlanes)
                                                                                        │
                                                                                        ▼
                                                                           worker (Scaffold / Impl / Test) ──▶ validator (Validation)
                                                                           (claim via assignee, advance Status, unassign / close when Done)
                                                                                        │
                                                                                        ▼
                                                                              mergepush (gh stack link into plan's stack, open/refresh PR — once per batch)
                                                                                        │
                                                                                        ▼
                                                                              system-validator (Tilt)
                                                                                        │
                                                                              findings ──▶ planner (new task issues)
```

Any persona can also file a **scope note** (`Status: Noted` → `Carry-over`/`Deferred`/rejected → scheduled or closed) for scope it notices but doesn't act on — a lifecycle instead of a stray comment, so a deferred decision stays queryable. See `CONVENTIONS.md` § Scope notes.

Query available work on a plan's Project at any time with:

```sh
gh project item-list <project-number> --owner whale-net --query "status:Implementation no:assignee"
```

## Skills

Nine skills orchestrate the pipeline:

| Skill | Drives | Dispatches |
|---|---|---|
| `/project-manager:product "<product>"` or `/project-manager:product <issue-number>` | Product scoping → capability map, load-bearing decisions, milestone roadmap → human gate → commits `<domain>/PRODUCT.md` and creates the tracking Issue (`product:approved`). Re-run against the issue to amend the spec | `producer`, `architect` |
| `/project-manager:design "<feature>"` or `/project-manager:design <discussion-url>` | Intake discussion → draft spec → producer/architect loop in Discussion until architect sign-off; with `--stakeholder-meeting`, a stakeholder round before hand-off | `producer`, `architect`, *(optionally)* `stakeholder` |
| `/project-manager:stakeholder-meeting <discussion-url\|issue-number>` | One meeting round: every persona in the spec posts guidance, non-blocking feedback, and blockers; blockers re-loop producer/architect, cleared hands off to review | `stakeholder` (one per persona), `producer`, `architect` |
| `/project-manager:review <discussion-url>` | The human gate: review architect-approved draft in Discussion → create root Issue (`plan:approved`), or leave feedback | `producer`, `architect` |
| `/project-manager:plan <issue-number> [--planner-model model]` | Task breakdown: converts the approved root Issue into a Project board with swimlanes and cohesive task issues (idempotent — reports the existing board instead of recreating it) | `planner` |
| `/project-manager:implement <issue-number> [--max-subagents N]` | Orchestrates worker/validator subagents in parallel batches — up to `--max-subagents` (default 4) at a time — each in its own dedicated worktree from branch creation onward, over `gh stack`-managed per-task branches until all tasks are `Done`; each batch's push/PR integration is handed to `mergepush`, which registers every task into one real `gh stack` stack (`main → task-a → task-b → ...`) via `gh stack link` so the orchestrator's own session stays free of `git`/`gh stack` output. Requires a Project board to already exist | `worker`, `validator`, `mergepush` |
| `/project-manager:validate <issue-number>` | Whole-system validation in Tilt (against a local branch merging every task's tip together, since the stack itself doesn't contain any single branch with everyone's combined work) once all tasks are `Done`; merges the whole plan's stack atomically with `gh stack merge` once validation passes, or routes findings to planner | `system-validator`, `planner` |
| `/project-manager:status <issue-number>` | Read-only: current lifecycle state and Project board breakdown by swimlane | *(none — pure `gh` reads)* |
| `/project-manager:help "<question>"` | Not sure which skill applies? Describe the situation in plain language and get back the exact next skill/command and why | `help` |

Typical flow for one feature: `design` → `review` → `plan` → `implement` → `validate` → (if findings) `implement` again.

For a product: `product` once, then that same flow per milestone — `design <product-issue> --milestone M1` → `review` → `plan` → `implement` → `validate`, then `M2`, and so on. `<domain>/PRODUCT.md` is read fresh from `main` at the start of each milestone's design, which is what keeps milestone N+1 aware of decisions made in milestone N without anyone re-reading milestone N's spec. `plan` and `validate` each gain one conditional step for this: posting a `Ledger: M<n> → in progress` / `→ shipped` comment on the tracking issue when the root plan names a product brief; ordinary single-feature plans are unaffected.

`stakeholder-meeting` is off the critical path: it runs inside `design --stakeholder-meeting` before the review gate, or on demand against an approved root plan issue.

## MCP Servers

`mcp_config.json` at the plugin root (exposed for AGY via `.agents/plugins/project-manager`
and symlinked to `.mcp.json` for Claude Code — see `.claude-plugin/marketplace.json`) wires up:

| Server | Purpose |
|---|---|
| `agentsync-mcp` | Cross-agent-session rendezvous (`start_session`/`join_session`/`sync`/`leave_session`/`end_session`) — lets a parent orchestrator (e.g. `/project-manager:implement`) start a session and hand its id to worker/validator subagents so they can coordinate directly instead of only relaying through the parent. See `tools/agentsync-mcp/README.md`. |

## Setup & Usage

### Antigravity (AGY)

Discovered automatically via `.agents/plugins.json` and `.agents/plugins/project-manager`.

### Claude Code

#### Try it locally

```bash
claude --plugin-dir tools/project-manager
```

#### Install from the repo marketplace

```
/plugin marketplace add ./.claude-plugin/marketplace.json
/plugin install project-manager@everything-marketplace
```
