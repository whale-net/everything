---
name: validate
description: Runs whole-system validation for a project-manager plan — dispatches system-validator to exercise the merged result in Tilt against the root plan's acceptance criteria, then routes any findings to planner for follow-up tasks.
---

# validate

Drives `tools/project-manager/CONVENTIONS.md` § System validation. Only meaningful once all swimlane tasks are `Done`.

## Usage

```
/project-manager:validate 123
```

## Steps

1. Find the plan's Project (`Project board: <url>` comment on the root issue) and confirm readiness: every task item with `Part of #<n>` must be in `Status: Done`:
   ```sh
   gh project item-list <project-number> --owner whale-net --query "-status:Done" --format json \
     | jq -r '.items[] | select(.content.body | test("Part of #<n>([^0-9]|$)")) | .content.number'
   ```
   If any task issues remain in `Scaffold`, `Implementation`, `Testing`, or `Validation`, implementation is not complete — inform the user to finish `/project-manager:implement <n>` first and stop.

2. **Build the integration branch** per CONVENTIONS.md § Git hygiene step 7 — most tasks will typically already be on `main` by now via continuous merge during `/project-manager:implement` (CONVENTIONS.md § Git hygiene step 6), but build this branch explicitly from every task's own tip regardless, right before dispatching system-validator, and never push it — merging an already-merged tip is a harmless no-op:
   ```sh
   git branch -D pm-<n>-integration 2>/dev/null
   git checkout main && git pull
   git checkout -b pm-<n>-integration
   for tip in <topmost active branch of every task on this plan>; do
     git merge --no-edit "$tip"
   done
   ```
   A conflict here is real cross-task integration work — resolve it and re-run; it doesn't affect the individual task PRs.

3. Dispatch `project-manager:system-validator` with the root issue number. System-validator brings the system up via Tilt, exercises it against the FRs/NFRs, and files finding issues (added to the plan's Project at `Status: Validation` with `from:system-validator`) for anything that isn't a clean pass.

4. **If everything passed:** Finalize per CONVENTIONS.md § Git hygiene step 8:
   - Make sure every task branch is registered into the plan's stack: dispatch `mergepush` once more with `<n>`, the full `<plan-branches>` list (every task branch on this plan, in dependency order — reconstruct it the same way `implement`'s "Plan stack" section does if this session didn't just run `/project-manager:implement` itself), an empty batch-to-integrate list, and `<done-task-numbers>` set to every task on the plan (step 1 already confirmed all are `Done`) — the `gh stack link` part of this is a no-op re-run for anything already registered, and the merge part lands whatever `mergepush`'s continuous-merge attempts during `/project-manager:implement` hadn't already landed. In the common case where continuous merge already cleared the stack, this call merges nothing and just confirms it.
   - Check `gh pr view <top-branch> --json number,state` for `<plan-branches>`'s last (topmost) entry — if it isn't `MERGED` yet after the `mergepush` call above, something's still blocking it (a failing check, a stale approval); resolve that before considering the plan closed out, same as any other merge failure (CONVENTIONS.md § Git hygiene, "Division of responsibility").
   - Collect every task's PR URL (`gh pr view <branch> --json number,url` per branch, or the URLs already gathered during `/project-manager:implement`).
   - Post `gh issue comment <n> --body "PRs: <url>, <url>, ..."` on the root issue.
   - **If the root issue's first line names a product brief** (`Product: #<p> — Milestone M<k>`), post `gh issue comment <p> --body "Ledger: M<k> → shipped"` on the product tracking issue — never a body edit (CONVENTIONS.md § Roadmap ledger). Ordinary single-feature plans skip this; it's the only product-aware step this skill has.
   - Report that the plan is fully validated and merged, with the full list of PR URLs — the deliverable is this sequence of small, individually reviewable PRs landed on trunk, not one PR for the whole plan.

5. **If there are findings:** Dispatch `project-manager:planner` with the finding issue numbers to run its "Handling system-validator findings" process — converting blocking findings into properly sequenced follow-up task issues starting in `Scaffold` or `Implementation` on the same Project. Report the new task issue numbers to the user, and point them to `/project-manager:implement <n>`.
