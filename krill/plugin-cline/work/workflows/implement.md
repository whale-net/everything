# implement

*Runs the swimlane execution phase of a krill-work plan — orchestrates worker and validator personas in parallel batches, via per-task branches created in dedicated worktrees, over ready krill Tasks from planner's manifest until every task reaches Done, then hands each batch's push/PR integration and continuous trunk-merge off to a mergepush subagent. Requires the task manifest the `plan` workflow produced (Milestone path) or the Project board it created (no-Milestone path).*


An orchestrator, not a worker: it gives each task its own branch and
worktree, dispatches `worker`/`validator` subagents concurrently against
them, then dispatches `mergepush` once per batch to push, open/refresh PRs,
and merge into `main` whatever is `Done` and safe to land. The pipeline is
trunk-oriented — most tasks land individually as they validate, not all at
the end. Git mechanics (branch naming, worktree creation, PR shape) are in
`krill/plugin-cline/shared/CONVENTIONS.md` § "No-Milestone GitHub fallback" ->
"Git hygiene".

**Responsibilities.** The orchestrator creates every branch and its
worktree itself and resolves any merge conflict that arises (inline for a
small one, or an ad-hoc `general-purpose` subagent scoped to that one merge
when it's large or ambiguous — never hand a worker an unresolved conflict).
Workers/validators only edit and commit inside the worktree they're handed;
they never push, merge, or open PRs. Neither does the orchestrator —
`mergepush` runs all of that, dispatched once per batch and waited on
before the next scan, so `git`/`gh` output stays out of this session.

## Usage

```
the `implement` workflow <milestone-id>     # Milestone path — needs the task manifest plan produced
the `implement` workflow 123                # no-Milestone GitHub fallback: the tracking issue number
the `implement` workflow <milestone-id> --max-subagents 2
```

`--max-subagents <N>` — workers/validators run concurrently per batch;
defaults to 4.

## Steps (Milestone path)

0. **Prerequisites:** `git config rerere.enabled true`, `git config
   remote.pushDefault origin`, `git fetch origin main`. Track
   `<plan-branches>`: every branch on this plan, in dependency order. Seed
   it from every task past `Implementation` (`Testing`, `Validation`,
   `Done`) via `list_tasks`/`get_task`, so a resumed session recomputes it
   from krill rather than memory; append each batch's new branches after
   every `mergepush` call.
1. **Discovery**: use the task manifest the `plan` workflow/`planner`
   reported (every task id, title, starting lane, and dependency edges) if
   you have it. If you don't, call `list_tasks {milestone_id}`
   (CONVENTIONS.md "Work axis") for the task ids, then `get_task {id}` on
   each for its dependency edges — `list_tasks` itself doesn't carry
   dependencies, only id/title/`current_lane`/attempt count/live-claim.
2. For each task whose dependencies are all `Done` — call `get_task {id}`
   on each to confirm current state rather than trusting the manifest's
   snapshot — take up to `max-subagents` ready tasks (lanes can mix in one
   batch), prepare each one's branch/worktree, then dispatch in parallel
   (one `Agent` call each, all in a single message):
   - **Branch/worktree:** branch `pm[<attempt>]-<milestone>/<task>-<slug>`;
     look for an existing `pm*-<milestone>/<task>-*` branch first (local,
     then remote) and reuse it. Otherwise `git worktree add
     .claude/worktrees/<task> -b <branch> <fork-point>`, where
     `<fork-point>` is the freshly fetched `origin/main` (never local
     `main`) or its dependency's branch; with several dependency branches,
     fork from one and `git -C .claude/worktrees/<task> merge --no-edit`
     the rest. Record `<parent>` — the PR base (`main`, or the dependency
     branch) — alongside branch and worktree; `mergepush` needs it.
   - **Dispatch:** the `krill-worker` agent (Scaffold/Implementation/Testing) or
     the `krill-validator` agent (Validation) with `<krill-session-id>`,
     `<task-id>`, and `<worktree-path>` only — not the task's title/body;
     they read those via `get_task` (CONVENTIONS.md "Subagent dispatch:
     ids, not bodies").
3. Once the whole batch returns, re-read each task via `get_task {id}` to
   see the lane krill itself moved it to (`complete_task`'s pass/fail
   delta) — that decides the next dispatch, not a field you set yourself.
4. Dispatch `mergepush` once for the batch with `<milestone-id>` as root,
   the `<task-id>`, `<branch-name>`, `<worktree-path>`, `<parent>` tuple for
   every batch member in a fixed order (whether or not its lane advanced),
   and `<done-task-ids>` — every task in `<plan-branches>` plus this batch
   whose `current_lane` is now `Done`. It pushes, opens/refreshes PRs, and
   merges ready tasks in dependency order (merging nothing is not a
   failure), and returns per-task PR URLs, merge results, and the updated
   `<plan-branches>`. A failed task is resolved like a merge conflict
   (inline or via a `general-purpose` subagent) before it's eligible again.
5. Re-scan and repeat from step 1 until every task is `Done` or a full pass
   finds no ready work.
6. **Report** each task's PR URL and whether it has merged (use the URLs
   already collected; otherwise one `gh pr list` call, not one `gh pr view`
   per branch). If every task is `Done`, point to the `validate` workflow
   <milestone-id>`; report anything blocked on a dependency clearly.

Every `worker`/`validator` dispatch has a working
`claim_task`/`complete_task` surface — see `the krill-worker agent in .cline/agents`. A
`forbidden` from either is a real regression; don't route around it,
surface it in your report as-is.

## Steps (no-Milestone GitHub fallback)

`<n>` is the GitHub tracking issue the `krill-planner` agent minted. Same loop
as above with the Project board in place of krill:
1. Confirm a `Project board: <url>` comment exists on `<n>`
   (`gh issue view <n> --comments`); if not, point to the `plan` workflow
   <feature-set-id>` and stop.
2. Scan swimlanes in order (`Scaffold`, `Implementation`, `Testing`,
   `Validation`) for unassigned items with `Part of #<n>` whose `Depends
   on:` issues are all `CLOSED` (mechanics in CONVENTIONS.md § "No-Milestone
   GitHub fallback"); branch names use the tracking issue number and the
   task issue number.
3. Spawn a subagent (Cline: new_task) with the `krill-worker` agent as its mode/`validator` with `<n>`, `<project-number>`,
   the swimlane, the task issue number, and the worktree path; then
   `mergepush` with `<n>`, the tuples, and `<done-task-numbers>` (tasks now
   at `Status: Done`, re-queried from the board). Seed `<plan-branches>`
   from the board's tasks past `Implementation`.
4. Report as above, pointing to the `validate` workflow <n>` when all are
   `Done`.
