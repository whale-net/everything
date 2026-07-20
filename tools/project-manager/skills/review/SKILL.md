---
name: review
description: The human review gate for a project-manager plan — shows a plan:architect-approved root issue and its reconciliation, then either sets plan:approved on your say-so or routes your feedback back through producer/architect.
---

# review

Drives the one lifecycle transition that only a human may make (`tools/project-manager/CONVENTIONS.md` § Root plan lifecycle: `plan:architect-approved → plan:approved`). Never set that label yourself outside this skill's explicit confirmation step — and never let a persona set it.

## Usage

```
/project-manager:review 123
```

## Steps

1. `gh issue view <n> --comments` — fetch the root plan issue. If its label isn't `plan:architect-approved`, tell the user its actual state (e.g. still `plan:needs-answers` — not ready for review yet; or already `plan:approved` — nothing to do) and stop.

2. Summarize for the user: the user stories/FRs/NFRs from the issue body, and the key points of architect's reconciliation comment(s) — what was checked, any nitpicks noted (non-blocking, just surface them for awareness).

3. Use AskUserQuestion to ask how to proceed:
   - **Approve** — release it to planner.
   - **Request changes** — leave feedback for producer/architect to address.

4. **If approved:** `gh issue edit <n> --add-label "plan:approved" --remove-label "plan:architect-approved"`. Tell the user `/project-manager:implement <n>` is the next step.

5. **If changes requested:** ask the user for the feedback text, post it as a comment (`gh issue comment <n> --body "..."`), then `gh issue edit <n> --add-label "plan:needs-answers" --remove-label "plan:architect-approved"`. Dispatch `project-manager:producer` (foreground, via Agent tool) with the issue number to address the feedback (Mode 2), then `project-manager:architect` (foreground) for a follow-up reconciliation round — same loop as `/project-manager:plan` step 5. Once architect returns to `plan:architect-approved`, come back to step 2 and show the user the updated state — don't silently loop past them again; the whole point of this skill is that a human looks at every round before it proceeds.
