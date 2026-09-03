---
name: schedule
description: Run Audience Score System's Loop 2 for one Channel — pull the real synced schedule and pacing context, find viable ideas without a schedule entry yet, and propose schedule-draft slots for the Creator to approve — by dispatching the analyst persona against the ASS MCP server. Use once one or more ideas have a viable verdict and need to move onto a proposed schedule.
---

# schedule

Orchestrates ASS's schedule-drafting loop (C6, C7): reads the Channel's actual YouTube schedule and pacing policy as planning context, then proposes draft slots for viable ideas. It stops at the draft — turning a draft into the Channel's committed schedule (C8) is a Creator-only action in the `web` UI, not something this skill or any MCP tool does.

## Usage

```
/audience-score-system:schedule <channel_id> [idea_id ...] [--around <date>] [--server dev|prod]
```

If `--server` is omitted, ask which of `audience-score-system-mcp-dev` / `audience-score-system-mcp-prod` to use before doing anything.

## Steps

1. Call `whoami` on the chosen server to confirm the resolved Person.
2. Call `get_pacing_policy` (channel_id). If none is set, tell the human — ask whether to set one now (`set_pacing_policy`: `target_uploads_per_week`, optional `preferred_days`) or draft without a cadence target. Don't invent a cadence unasked.
3. Call `list_ideas` and `get_viability_verdict` for each to find every Idea whose current verdict is `viable`, then cross-check against `list_schedule_entries` (channel_id) to find which of those don't have a schedule entry yet. If specific `idea_id`s were given on the command line, scope to those instead of scanning every Idea.
4. Call `list_strategies` (channel_id, `active_only`: true). For an idea that's linked to an active Strategy, call `generate_schedule_plan` (channel_id) and use its proposal's `proposed_publish_at` as that idea's slot instead of inventing one — the Strategy's cadence/preferred_weekday already encodes the human's intent for that idea. For an idea not covered by any active Strategy, fall back to the per-idea flow below.
5. For each idea needing a slot with no Strategy-derived proposal, dispatch the **analyst** agent with the channel_id, idea_id, the pacing policy, and (via `get_drafting_context`, `around` if given) the synced schedule and existing entries in the relevant window. It proposes a `proposed_publish_at` and calls `save_schedule_draft`.
6. For a Strategy-derived proposal (step 4), call `save_schedule_draft` directly with the proposal's `idea_id`/`verdict_id`/`proposed_publish_at` — no analyst dispatch needed, since the cadence decision was already made when the Strategy was saved.
7. Collect each draft's flags (`cadence_exceeded`, `off_preferred_day`, `collision`) from the analyst's report (step 5) or the direct call (step 6) and surface them plainly to the human — they're advisory, the draft is saved regardless (FR18), but the human deciding whether to approve needs to see them.
8. Report every draft created (idea, proposed time, flags, and whether it came from a Strategy) and remind the human: **approval into the committed schedule (C8) happens in `web`, by the Creator**, not here — do not proceed as if a draft were committed.

## Rules

- Never call `save_schedule_draft` yourself for an idea outside step 6's Strategy-derived path — for everything else that judgment call belongs to the analyst persona, dispatched per idea, so its reasoning about pacing/collisions stays attached to that call.
- If `get_drafting_context`/`get_channel_schedule` shows the synced schedule is stale (compare `last_synced_at` across rows), say so — a drafting decision made against stale sync data is a real risk the human should know about, not something to paper over.
- Multiple viable ideas in one run is fine; draft each independently so a flag on one doesn't block the others.
