---
name: status
description: Read-only status dashboard for a project-manager root plan — shows its plan:* lifecycle state and a breakdown of task issues by phase and status. Use to check where a plan stands before deciding which orchestration skill (plan/review/build/validate) to run next.
---

# status

Pure read — never edits labels, comments, or dispatches any persona. See `tools/project-manager/CONVENTIONS.md` for what each label means.

## Usage

```
/project-manager:status 123
```

## Steps

1. `gh issue view <n>` — report the root issue's title and current `plan:*` label, and tell the user which orchestration skill applies next:
   - `plan:draft` / `plan:needs-answers` → `/project-manager:plan <n>`
   - `plan:architect-approved` → `/project-manager:review <n>`
   - `plan:approved` → `/project-manager:implement <n>`

2. List every task issue for this plan and group by phase and status:
   ```sh
   gh issue list --state all --json number,title,state,labels \
     | jq --arg n "<n>" '[.[] | select(.body // "" | test("Part of #" + $n + "([^0-9]|$)"))]'
   ```
   Note: `gh issue list --json` doesn't return `body` by default — add `body` to the `--json` fields, or do a second pass with `gh issue view <candidate> --json body` if filtering by body content this way proves unreliable at scale. Group the result by `phase:*` label, then by `status:*` label (or "closed" if no `status:*` label remains).

3. Report a compact table: phase × (blocked / ready / in-progress / closed count). Call out anything that looks stuck — `status:blocked` issues whose listed dependencies are actually all closed already (a sign the unblock step was missed and needs a manual `gh issue edit --add-label status:ready --remove-label status:blocked`).

4. If every `phase:implementation`/`phase:testing` issue is closed and no `phase:validation` finding issues are open, mention that `/project-manager:validate <n>` is available.
