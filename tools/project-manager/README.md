# project-manager

AGY and Claude Code plugin providing a multi-persona project-management pipeline for the
`everything` monorepo, tracked entirely in GitHub: intake in a Discussion, the root
plan as an Issue, and task execution on a GitHub Project (v2) board. See
[`CONVENTIONS.md`](CONVENTIONS.md) for the full contract.

## Personas

| Persona | Role | Model |
|---|---|---|
| `project-manager` | Lightweight single-session planning; doesn't use the GitHub pipeline | sonnet |
| `producer` | Runs intake as a GitHub Discussion, interviews the requester for requirements and user stories, writes them up as a GitHub root-plan issue; answers architect questions and human feedback | opus |
| `architect` | Reconciles the plan against repo conventions, asks questions, hands off at `plan:architect-approved` | opus |
| *(human)* | Reviews the architect-approved plan, either sets `plan:approved` or sends feedback back through producer/architect | — |
| `planner` | Converts a human-approved plan into a GitHub Project with dependency-ordered, phased task issues | opus |
| `worker` | Executes one scaffold/implementation/testing task issue (build it, or verify it with tests) | haiku |
| `validator` | Checks one task issue's acceptance criteria against merged work, read-only | haiku |
| `system-validator` | Runs the whole system end-to-end in Tilt against the root plan's criteria; files follow-up findings | opus (max effort) |

## Pipeline

```
(human) ──intake──▶ Discussion ──▶ producer ──user stories/FR/NFR──▶ architect ──questions──▶ producer
                                                                          │   (loop until plan:architect-approved)
                                                                          ▼
                                                                  (human) review gate
                                                                   │                  │
                                                        plan:approved      feedback ──▶ producer/architect (loop back up)
                                                                   │
                                                                   ▼
                                                                planner  ──creates──▶  Project board (Status: Scaffold → Implementation → Testing → Validation → Done)
                                                                                            (Depends on: #n drives readiness; no persisted blocked/ready state)
                                                                                            │
                                                                                            ▼
                                                                               worker / validator
                                                                               (query unassigned items at their phase, claim via assignee, close + Status: Done)
                                                                                            │
                                                                                            ▼
                                                                                  system-validator (Tilt)
                                                                                            │
                                                                                  findings ──▶ planner (new tickets)
```

Any persona can also file a **scope note** (`Status: Noted` → `Carry-over`/`Deferred`/rejected → scheduled or closed) for scope it notices but doesn't act on — a lifecycle instead of a stray comment, so a deferred decision stays queryable. See `CONVENTIONS.md` § Scope notes.

Every phase reads/writes GitHub directly via `gh` — there is no separate task store. Query available work on a plan's Project at any time with:

```sh
gh project item-list <project-number> --owner whale-net --query "status:Implementation no:assignee"
```

## Skills

You don't have to invoke each persona by hand — five skills orchestrate the pipeline, each driving the segment of the lifecycle in `CONVENTIONS.md` its name matches. All are read/dispatch-only except `review`, the one place a human decision is required.

| Skill | Drives | Dispatches |
|---|---|---|
| `/project-manager:plan "<feature>"` or `/project-manager:plan <n>` | Intake discussion → root issue → producer/architect loop, up to `plan:architect-approved` | `producer`, `architect` |
| `/project-manager:review <n>` | The human gate: `plan:architect-approved` → `plan:approved`, or feedback → re-loop | `producer`, `architect` (only if you request changes) |
| `/project-manager:implement <n>` | Project setup + task breakdown, then the worker loop until no ready work remains | `planner`, `worker`, `validator` |
| `/project-manager:validate <n>` | Whole-system validation and follow-up task creation | `system-validator`, `planner` |
| `/project-manager:status <n>` | Read-only: current lifecycle state and Project board breakdown | *(none — pure `gh` reads)* |

Typical flow: `plan` → `review` → `implement` → `validate` → (if findings) `implement` again.

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
