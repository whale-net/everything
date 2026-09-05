---
name: schedule
description: Run Audience Score System's Loop 2 for one Channel — find viable ideas without a proposed video_script yet and propose one under an active Strategy, then leave greenlighting/denying/archiving to the Creator or Co-Creator — by dispatching the analyst persona against the ASS MCP server. Use once one or more ideas have a viable verdict and need to move onto a proposed video_script.
---

# schedule

Orchestrates ASS's video_script proposal loop (C18): for each viable Idea without a video_script yet, proposes one (`save_video_script`) bound to that Idea's viable verdict (LB3) and an active Strategy. Founder, Co-Creator, and Analyst may all propose (NFR13); it stops at the proposal — deciding it (C19: greenlighting, denying, or archiving) requires Creator-tier authority -- Founder or Co-Creator (FR37-FR39) -- that this skill never calls on its own initiative; it only happens on the human's explicit instruction, whether via `web`'s greenlight/deny/archive UI or a follow-up MCP call the human asks for by name.

## Usage

```
/audience-score-system:schedule <channel_id> [idea_id ...] [--server dev|prod]
```

If `--server` is omitted, ask which of `audience-score-system-mcp-dev` / `audience-score-system-mcp-prod` to use before doing anything.

## Steps

1. Call `whoami` on the chosen server to confirm the resolved Person.
2. Call `list_strategies` (channel_id, `active_only`: true). If none exist, tell the human — ask whether to create one (`save_strategy`) before proposing anything, or which existing Strategy an idea should be proposed under. Don't invent a Strategy unasked.
3. Call `list_ideas` and `get_viability_verdict` for each to find every Idea whose current verdict is `viable`, then cross-check against `get_channel_overview`'s `video_scripts` section (channel_id) to find which of those don't have a video_script yet. If specific `idea_id`s were given on the command line, scope to those instead of scanning every Idea.
4. For each idea needing a proposal, dispatch the **analyst** agent with the channel_id, idea_id, the idea's current verdict_id, and the Strategy to propose it under (from step 2, confirmed with the human if more than one is plausible). It calls `save_video_script` (verdict_id, strategy_id, title, script_text, optional target_publish_date).
5. Report every video_script proposed (idea, strategy, title, target publish date if given) and remind the human: **greenlighting, denying, or archiving a video_script (C19) is the Creator's or Co-Creator's call**, available either via `web`'s greenlight/deny/archive UI or `greenlight_video_script`/`deny_video_script`/`archive_video_script` if the human explicitly asks for one of them by name — do not call any of them yourself as part of this skill's own steps, and do not proceed as if a video_script were decided.

## Rules

- Never call `greenlight_video_script`, `deny_video_script`, or `archive_video_script` yourself — deciding a video_script's fate belongs to a human with Creator-tier authority, dispatched only on their explicit instruction, never this skill's own initiative.
- Never call `save_video_script` yourself for an idea outside a human-confirmed Strategy — that judgment call belongs to the analyst persona, dispatched per idea, so its reasoning about which Strategy and target date stays attached to that call.
- Multiple viable ideas in one run is fine; propose each independently so a rejection on one doesn't block the others.
