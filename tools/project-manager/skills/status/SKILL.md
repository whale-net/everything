---
name: status
description: Read-only status dashboard for a project-manager root plan or product brief — for a plan, its lifecycle state and a breakdown of Project items across swimlanes; for a product:approved brief, the milestone roadmap ledger. Use to check where a plan or product stands before deciding which orchestration skill to run next.
---

# status

Pure read — never edits items or dispatches personas. See `tools/project-manager/CONVENTIONS.md` for what each swimlane means.

## Usage

```
/project-manager:status 123
```

## Steps

1. `gh issue view <n> --comments` — report the issue's title and status:
   - If labeled `product:approved`, this is a product tracking issue, not a plan. Read `<domain>/PRODUCT.md` (path from the issue's first line) from `main`, follow its jump table to `product/03-roadmap.md` for the milestone list, then reconstruct each milestone's status from the comments already fetched: take the **last** `Ledger: M<n> → <status> (<link>)` comment per milestone (no comment means `not started` — CONVENTIONS.md § Roadmap ledger). Report milestone, status, and the link each ledger comment carried. Recurse into step 1 for each milestone that has a root plan issue so the user sees a one-line state per milestone. Name the next action: `/project-manager:design <n> --milestone M<k>` for the first `not started` milestone, or the appropriate skill for whichever milestone is furthest along. Stop here — the rest of these steps are per-plan.
   - If labeled `plan:approved` with a `Project board: <url>` comment, proceed to step 2. If its body's first line names a product brief (`Product: #<p> — Milestone M<k>`), report that too.
   - If labeled `plan:approved` without a `Project board:` comment, report that task breakdown hasn't started yet and `/project-manager:plan <n>` is next.
   - Find the last `Stakeholder meeting round <N>: <url>` link comment on the root issue or on its intake discussion (first body line, `Intake discussion: <url>`), follow it, and report the `Stakeholder meeting: cleared` / `Stakeholder meeting: blocked (<k> blockers)` line from that round's minutes comment on the linked meeting discussion. If neither has a link comment, report that no stakeholder meeting has been held and that `/project-manager:stakeholder-meeting <n>` is available.

2. List every item on the plan's Project and its `Status` (swimlane):
   ```sh
   gh project item-list <project-number> --owner whale-net --field "Status" --format json \
     | jq '[.items[] | select(.content.body | test("Part of #<n>([^0-9]|$)"))]'
   ```
   Group items by `Status` (`Scaffold`, `Implementation`, `Testing`, `Validation`, `Done`, `Noted`, `Carry-over`, `Deferred`). For each item not yet `Done`, check its `assignees` field (claimed vs. unclaimed) and whether its `Depends on:` issues are closed (ready vs. blocked).

3. Report a compact table: Swimlane × (Blocked / Ready / Claimed / Done count). Highlight items currently in progress or waiting to be claimed.

4. If all task items are `Done` and there are no open `Validation` finding items, report that `/project-manager:validate <n>` is available.
