---
name: plan
description: Task breakdown — converts an approved project-manager root plan Issue (plan:approved) into a GitHub Project board with swimlanes and cohesive task issues, by dispatching the planner persona. Idempotent: if the Project board already exists, reports it instead of recreating it. Run after /project-manager:review approves the spec, before /project-manager:implement. Also the right target for requests like "just set up the board" / "create the tasks but don't start work" / "plan only".
---

# plan

Turns a `plan:approved` root plan Issue into executable, dependency-tracked task issues on a Project board, by dispatching `project-manager:planner`. This is pure task breakdown — no code is written and no branches are touched; see `tools/project-manager/CONVENTIONS.md` § Project setup and § Task issues & swimlane progression for the mechanics `planner` follows.

## Usage

```
/project-manager:plan 123
/project-manager:plan 123 --planner-model sonnet
```

- `--planner-model <model>` — model override passed to the `Agent` call that dispatches `planner`. **Defaults to `opus`.** Task breakdown (swimlaning, dependency wiring, issue scoping) is the highest-leverage reasoning step in this pipeline and worth the strongest available model; running it as a subagent also keeps the board/issue-creation tool traffic out of this skill's own context.

## Steps

1. `gh issue view <n>` — confirm the root issue is labeled `plan:approved`. If not, point the user to `/project-manager:design` or `/project-manager:review`.

2. **Idempotency check.** `gh issue view <n> --comments` — if a `Project board: <url>` comment already exists, the task breakdown has already run: report the existing project number and its task issues (grouped by swimlane, per `/project-manager:status`) and stop here rather than re-dispatching planner.

3. **Task breakdown.** Otherwise, dispatch `project-manager:planner` — via `Agent` with `model` set to `--planner-model` (default `opus`) — with the root issue number, to create the Project board with swimlanes, create cohesive task issues, and post the summary comment.

   **If the root issue's first line names a product brief** (`Product: #<p> — Milestone M<k>`), post `gh issue comment <p> --body "Ledger: M<k> → in progress (Project board)"` on the product tracking issue once the board exists — never a body edit (CONVENTIONS.md § Roadmap ledger). Ordinary single-feature plans skip this; it's the only product-aware step this skill has.

4. **Report.** Summarize the Project board URL and the created task issues, grouped by starting swimlane, to the user. Tell them `/project-manager:implement <n>` is the next step.
