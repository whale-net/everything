---
name: schedule
description: Run Audience Score System's Loop 2 for one Channel — pull the real synced schedule and pacing context, find viable ideas without a schedule entry yet, and propose schedule-draft slots for the Creator to approve — by dispatching the analyst persona against the ASS MCP server. Use once one or more ideas have a viable verdict and need to move onto a proposed schedule.
---

# schedule

Orchestrates ASS's schedule-drafting loop (C6, C7): reads the Channel's actual YouTube schedule and pacing policy as planning context, then proposes draft slots for viable ideas. It stops at the draft — turning a draft into the Channel's committed schedule (C8) requires Creator-tier authority -- Founder or Co-Creator, symmetrically per FR32 (`commit_schedule_draft`/`uncommit_schedule_draft`/`update_schedule_draft`, issue #1648) that this skill never calls on its own initiative; it only happens on the human's explicit instruction, whether via `web`'s approve/un-approve/edit UI or a follow-up MCP call the human asks for by name.

## Usage

```
/audience-score-system:schedule <channel_id> [idea_id ...] [--around <date>] [--server dev|prod]
```

If `--server` is omitted, ask which of `audience-score-system-mcp-dev` / `audience-score-system-mcp-prod` to use before doing anything.

## Steps

1. Call `whoami` on the chosen server to confirm the resolved Person.
2. Call `get_pacing_policy` (channel_id). If none is set, tell the human — ask whether to set one now (`set_pacing_policy`: `target_uploads_per_week`, optional `preferred_days`) or draft without a pacing target. Don't invent one unasked.
3. Call `list_ideas` and `get_viability_verdict` for each to find every Idea whose current verdict is `viable`, then cross-check against `list_schedule_entries` (channel_id) to find which of those don't have a schedule entry yet. If specific `idea_id`s were given on the command line, scope to those instead of scanning every Idea.
4. For each idea needing a slot, dispatch the **analyst** agent with the channel_id, idea_id, the pacing policy, and (via `get_drafting_context`, `around` if given) the synced schedule and existing entries in the relevant window. It proposes a `proposed_publish_at` and calls `save_schedule_draft`.
5. Collect each draft's flags (`cadence_exceeded`, `off_preferred_day`, `collision`) from the analyst's report and surface them plainly to the human — they're advisory, the draft is saved regardless (FR18), but the human deciding whether to approve needs to see them.
6. Report every draft created (idea, proposed time, flags) and remind the human: **committing, un-committing, or rescheduling within the schedule (C8) is the Creator's call**, available either via `web`'s approve/un-approve/edit UI or `commit_schedule_draft`/`uncommit_schedule_draft`/`update_schedule_draft` if the human explicitly asks for one of them by name — do not call any of them yourself as part of this skill's own steps, and do not proceed as if a draft were committed.

## Rules

- Never call `save_schedule_draft` yourself — that judgment call belongs to the analyst persona, dispatched per idea, so its reasoning about pacing/collisions stays attached to that call.
- If `get_drafting_context` shows the synced schedule is stale (compare `last_synced_at` across rows), say so — a drafting decision made against stale sync data is a real risk the human should know about, not something to paper over.
- Multiple viable ideas in one run is fine; draft each independently so a flag on one doesn't block the others.
