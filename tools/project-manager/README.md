# project-manager

Claude Code plugin providing a multi-persona project-management pipeline for the
`everything` monorepo, tracked entirely in GitHub Issues. See
[`CONVENTIONS.md`](CONVENTIONS.md) for the full label/dependency contract.

## Personas

| Persona | Role | Model |
|---|---|---|
| `project-manager` | Lightweight single-session planning; doesn't use the GitHub pipeline | sonnet |
| `producer` | Interviews the requester for requirements and user stories, writes them up as a GitHub root-plan issue; answers architect questions and human feedback | opus |
| `architect` | Reconciles the plan against repo conventions, asks questions, hands off at `plan:architect-approved` | opus |
| *(human)* | Reviews the architect-approved plan, either sets `plan:approved` or sends feedback back through producer/architect | — |
| `planner` | Converts a human-approved plan into dependency-ordered, phased GitHub task issues | opus |
| `writer` | Executes one scaffolding/implementation task issue | haiku |
| `tester` | Executes one testing task issue | haiku |
| `validator` | Checks one task issue's acceptance criteria against merged work | haiku |
| `system-validator` | Runs the whole system end-to-end in Tilt against the root plan's criteria; files follow-up findings | opus (max effort) |

## Pipeline

```
(human) ──intake──▶ producer ──user stories/FR/NFR──▶ architect ──questions──▶ producer
                                                            │   (loop until plan:architect-approved)
                                                            ▼
                                                    (human) review gate
                                                     │                  │
                                          plan:approved      feedback ──▶ producer/architect (loop back up)
                                                     │
                                                     ▼
                                                  planner  ──creates──▶  scaffold → implementation → testing → validation issues
                                                                              (status:blocked / status:ready, Depends on: #n)
                                                                              │
                                                                              ▼
                                                                 writer / tester / validator workers
                                                                 (query status:ready, execute, close, unblock dependents)
                                                                              │
                                                                              ▼
                                                                    system-validator (Tilt)
                                                                              │
                                                                    findings ──▶ planner (new tickets)
```

Every phase reads/writes GitHub Issues directly via `gh` — there is no separate task store. Query available work at any time with:

```sh
gh issue list --label "status:ready" --label "phase:implementation" --state open
```

## Skills

You don't have to invoke each persona by hand — five skills orchestrate the pipeline, each driving the segment of the lifecycle in `CONVENTIONS.md` its name matches. All are read/dispatch-only except `review`, the one place a human decision is required.

| Skill | Drives | Dispatches |
|---|---|---|
| `/project-manager:plan "<feature>"` or `/project-manager:plan <n>` | Intake → root issue → producer/architect loop, up to `plan:architect-approved` | `producer`, `architect` |
| `/project-manager:review <n>` | The human gate: `plan:architect-approved` → `plan:approved`, or feedback → re-loop | `producer`, `architect` (only if you request changes) |
| `/project-manager:implement <n>` | Task breakdown, then the worker loop until no `status:ready` work remains | `planner`, `writer`, `tester`, `validator` |
| `/project-manager:validate <n>` | Whole-system validation and follow-up task creation | `system-validator`, `planner` |
| `/project-manager:status <n>` | Read-only: current lifecycle state and task-issue breakdown | *(none — pure `gh` reads)* |

Typical flow: `plan` → `review` → `implement` → `validate` → (if findings) `implement` again.

## Try it locally

```bash
claude --plugin-dir tools/project-manager
```

## Install from the repo marketplace

```
/plugin marketplace add ./.claude-plugin/marketplace.json
/plugin install project-manager@everything-marketplace
```
