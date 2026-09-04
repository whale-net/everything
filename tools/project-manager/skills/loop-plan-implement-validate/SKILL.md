---
name: loop-plan-implement-validate
description: Drives a plan:approved root plan all the way to a merged PR stack unattended — dispatches /project-manager:plan, /project-manager:implement, and /project-manager:validate each to their own fresh subagent (which in turn dispatch planner/worker/validator/mergepush/system-validator themselves), looping implement→validate again whenever validate routes findings back to Implementation, until validate reports a clean merge. Finishes with an independent final-verification subagent that re-checks the merged stack from a branch/PR description handed to it, so this orchestrating session's own context never absorbs any phase's gh/git output, start to finish. Use for "run this plan end to end", "loop plan/implement/validate until done", or "get this whole plan merged without me babysitting each phase".
---

# loop-plan-implement-validate

Chains `/project-manager:plan` → `/project-manager:implement` → `/project-manager:validate` to completion, re-looping `implement`→`validate` whenever `validate` produces follow-up findings (CONVENTIONS.md § System validation), until the plan's whole PR stack is merged into `main`. Each phase is dispatched to its own fresh subagent that re-enters that phase's own skill via the `Skill` tool — so this session never runs the phase's own `gh`/`git` steps itself, only relays each subagent's short summary. Those phase subagents then dispatch their own personas exactly as their skill's own instructions describe (`plan` → `planner`; `implement` → `worker`/`validator`/`mergepush`; `validate` → `system-validator`/`planner`) — this orchestrator never talks to a persona directly, only to the three phase subagents.

This is a convenience wrapper, not a new mechanic: it doesn't touch GitHub state itself and introduces no git hygiene of its own — everything it does is already covered by CONVENTIONS.md via the phases it dispatches.

## Usage

```
/project-manager:loop-plan-implement-validate 123
/project-manager:loop-plan-implement-validate 123 --max-subagents 2 --planner-model opus
/project-manager:loop-plan-implement-validate 123 --max-iterations 8
```

- `--max-subagents <N>` — forwarded verbatim to `/project-manager:implement`'s own `--max-subagents` on every iteration. Defaults to 4 (matching `implement`'s own default).
- `--planner-model <model>` — forwarded verbatim to `/project-manager:plan`'s own `--planner-model`, used only on the first iteration (task breakdown doesn't repeat). Defaults to `opus`.
- `--max-iterations <N>` — safety cap on `implement`↔`validate` cycles before stopping and reporting the plan stuck rather than looping forever. Defaults to 5. A well-scoped plan should converge in 1-2 cycles; hitting the cap points at findings that keep recurring rather than a slow plan.

## Steps

1. **Confirm root state.** `gh issue view <n>` — must be labeled `plan:approved`; if not, point the user to `/project-manager:design` or `/project-manager:review` and stop. Note from the comments whether a `Project board: <url>` comment already exists.

2. **Plan phase (subagent), only if no board exists yet.** Dispatch a fresh `general-purpose` subagent with a self-contained prompt: invoke the `Skill` tool with `skill: "project-manager:plan"`, `args: "<n>"` (append `--planner-model <model>` if given), let it run to completion, then report back *only*: the Project board URL/number and the created task issues grouped by swimlane. Nothing else from this phase — no raw `gh`/`git` output — should reach this session.

   If step 1 already found a board, skip the dispatch and just carry its URL/number forward.

3. **Implement phase (subagent).** Dispatch a fresh `general-purpose` subagent: invoke `Skill` with `skill: "project-manager:implement"`, `args: "<n>"` (append `--max-subagents <N>` if given), let it run to completion, then report back *only*: whether every task on the board reached `Done`, and for each task — issue number, branch name, PR number/URL (`implement`'s own `<plan-branches>` list). This subagent dispatches `worker`/`validator`/`mergepush` in batches exactly per `implement`'s own instructions; none of that traffic should surface here.

   Keep this batch's `{task issue → branch → PR}` list — it's what step 6 hands to final verification later, so this session never has to re-derive it from git/gh itself.

   If the report shows tasks still blocked (not `Done`, not just waiting on a dependency that will clear next pass), surface that to the user and stop the loop — a stuck task issue is a condition for a human, not something to keep looping past.

4. **Validate phase (subagent).** Dispatch a fresh `general-purpose` subagent: invoke `Skill` with `skill: "project-manager:validate"`, `args: "<n>"`, let it run to completion, then report back *only*:
   - **On a clean pass:** the finalized `<plan-branches>` (branch → PR number/URL, now merged) and the PR number `validate` ran `gh stack merge` on.
   - **On findings:** the new follow-up task issue numbers `validate` had `planner` create in `Scaffold`/`Implementation`.

5. **Loop control.**
   - Clean pass → go to step 6.
   - Findings → increment the iteration counter. If it has now reached `--max-iterations`, stop and report the plan stuck: list the outstanding finding issue numbers and tell the user `/project-manager:implement <n>` is available to keep working them manually. Otherwise go back to step 3 — the next `implement` dispatch picks up the new follow-up tasks `planner` just added.

6. **Final stack verification (subagent).** Dispatch one more fresh `general-purpose` subagent — never inline in this session. Hand it *only* the `{task issue → branch → PR number/URL}` description already collected across steps 3-4 (the running `<plan-branches>` from the last `implement`/`validate` reports) plus the merged-stack PR number from step 4, not an open-ended mandate to go rediscover the plan's state itself. Ask it to independently confirm, using that description as its worklist:
   - every listed PR is `MERGED` (`gh pr view <branch-or-number> --json state,mergedAt` per entry),
   - no PR belonging to this plan is still open,
   - `main` actually contains every task's work (e.g. `git log --oneline origin/main | grep <task-issue-number>` per task, or equivalent).

   Have it report back a short pass/fail plus any discrepancy found — nothing else. This is the only point in the whole run that re-touches `git`/`gh` after step 4's own merge, and it happens in a subagent specifically so this session's own context stays clean at the tail end too, not just mid-run.

7. **Report.** Summarize to the user: the final PR list (numbers + URLs, merged), and step 6's verdict. If step 6 found a discrepancy, surface it plainly instead of declaring the plan done — that's a condition for the user to look at, not something to paper over.
