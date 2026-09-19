---
name: implement
description: Runs the swimlane execution phase of a krill-work plan (fork of project-manager's implement) — orchestrates worker and validator personas in parallel batches, via per-task branches created in dedicated worktrees, over ready tasks across swimlanes until all tasks reach Done, then hands each batch's push/PR integration and continuous trunk-merge off to a mergepush subagent. Requires the Project board (from /krill-work:plan) to already exist.
---

# implement

Forked verbatim from `tools/project-manager/skills/implement` —
**mechanics unchanged**. The orchestrator/branch/worktree/batch/mergepush
machinery never touches krill; `<n>` here is the GitHub tracking issue
`krill-work:planner` minted (see `agents/planner.md`), not a
project-manager root plan Issue, and the persona names dispatched are
`krill-work:worker`, `krill-work:validator`, `krill-work:mergepush` in place
of the `project-manager:*` ones. Every other detail — the responsibilities
split, `<plan-branches>` tracking, the swimlane work loop, branch/worktree
creation, batching, and the report format — is identical.

## Usage

```
/krill-work:implement 123
/krill-work:implement 123 --max-subagents 2
```

Read `tools/project-manager/skills/implement/SKILL.md` for the full process
— it is not duplicated here since none of it changed by this fork. Once
krill's M4 work-tracking MCP surface ships, this skill's swimlane-scan and
dispatch steps become the first candidate for replacing `gh project
item-list`/`gh issue edit` calls with `claim`/`heartbeat` MCP calls — see
`krill/plugin/shared/CONVENTIONS.md`.
