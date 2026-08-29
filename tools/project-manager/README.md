# project-manager

AGY and Claude Code plugin providing a multi-persona project-management pipeline for the
`everything` monorepo, tracked entirely in GitHub: intake, design drafting, and architect reconciliation in a **Discussion**; the final approved root plan as an **Issue**; task breakdown and task execution progressing through **swimlanes** on a GitHub **Project (v2)** board. See [`CONVENTIONS.md`](CONVENTIONS.md) for the full contract.

## Personas

| Persona | Role | Model |
|---|---|---|
| `project-manager` | Lightweight single-session planning; doesn't use the GitHub pipeline | sonnet |
| `producer` | Runs intake in a GitHub Discussion, interviews requester, drafts requirements/user stories, reconciles with architect in the Discussion, and publishes the final approved root plan Issue | opus |
| `architect` | Reconciles the plan against repo conventions inside the Discussion, asks questions via Discussion comments, and signs off when ready for human review | opus |
| `stakeholder` | Represents exactly one persona from the spec during a stakeholder meeting round; posts guidance, non-blocking feedback, and numbered blockers | sonnet |
| *(human)* | Reviews the architect-approved draft in the Discussion, either approves it (triggering root plan Issue creation) or requests changes | — |
| `planner` | Converts an approved root plan Issue into a GitHub Project with swimlanes and task issues | opus |
| `worker` | Executes tasks in `Scaffold`, `Implementation`, and `Testing` swimlanes inside a dedicated worktree, commits to the task's own `gh stack` branch, and advances tasks to the next swimlane | haiku |
| `validator` | Checks a task's acceptance criteria in the `Validation` swimlane against merged work (read-only), moves to `Done`, and closes the issue | haiku |
| `system-validator` | Runs the whole system end-to-end in Tilt against the root plan's criteria once all tasks are `Done`; files follow-up findings if needed | opus (max effort) |

## Pipeline

```
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

Seven skills orchestrate the pipeline:

| Skill | Drives | Dispatches |
|---|---|---|
| `/project-manager:design "<feature>"` or `/project-manager:design <discussion-url>` | Intake discussion → draft spec → producer/architect loop in Discussion until architect sign-off; with `--stakeholder-meeting`, a stakeholder round before hand-off | `producer`, `architect`, *(optionally)* `stakeholder` |
| `/project-manager:stakeholder-meeting <discussion-url\|issue-number>` | One meeting round: every persona in the spec posts guidance, non-blocking feedback, and blockers; blockers re-loop producer/architect, cleared hands off to review | `stakeholder` (one per persona), `producer`, `architect` |
| `/project-manager:review <discussion-url>` | The human gate: review architect-approved draft in Discussion → create root Issue (`plan:approved`), or leave feedback | `producer`, `architect` |
| `/project-manager:plan <issue-number> [--planner-model model]` | Task breakdown: converts the approved root Issue into a Project board with swimlanes and cohesive task issues (idempotent — reports the existing board instead of recreating it) | `planner` |
| `/project-manager:implement <issue-number> [--max-subagents N]` | Orchestrates worker/validator subagents in parallel batches — up to `--max-subagents` (default 4) at a time — over `gh stack`-managed per-task branches until all tasks are `Done`. Requires a Project board to already exist | `worker`, `validator` |
| `/project-manager:validate <issue-number>` | Whole-system validation in Tilt (against a local integration branch merging every task's stack) once all tasks are `Done`; ensures every task branch has an open PR or routes findings to planner | `system-validator`, `planner` |
| `/project-manager:status <issue-number>` | Read-only: current lifecycle state and Project board breakdown by swimlane | *(none — pure `gh` reads)* |

Typical flow: `design` → `review` → `plan` → `implement` → `validate` → (if findings) `implement` again.

`stakeholder-meeting` is off the critical path: it runs inside `design --stakeholder-meeting` before the review gate, or on demand against an approved root plan issue.

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
