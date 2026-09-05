---
name: outcomes
description: Run Audience Score System's Loop 3 for one Channel — resolve any pending outcome matches, then compare what published videos actually did against the viability verdicts that predicted them — by dispatching the analyst persona against the ASS MCP server. Use once videos from a Channel's committed schedule have published and their synced metrics are ready to check against predictions.
---

# outcomes

Orchestrates ASS's outcome loop (C9, C10, C14): clears the pending-match queue the sync worker populates, browses prediction-vs-outcome comparisons and a whole-Channel overview, and reads/sets the Channel's outcome bar plus its calibration trend. Mostly read/confirm, plus one narrow write (the outcome bar): this skill reports comparisons and the calibration rate in plain language, it never computes or invents an aggregate score itself.

## Usage

```
/audience-score-system:outcomes <channel_id> [idea_id] [--since <date>] [--server dev|prod]
```

If `--server` is omitted, ask which of `audience-score-system-mcp-dev` / `audience-score-system-mcp-prod` to use before doing anything.

## Steps

1. Call `whoami` on the chosen server to confirm the resolved Person.
2. Call `list_pending_matches` (channel_id). If any rows exist, dispatch the **analyst** agent to review each (published video + latest metrics + matcher's best-guess entry + confidence) and call `resolve_pending_match` — confirm the plausible ones, reject the wrong ones, and leave genuinely ambiguous ones (no plausible candidate) for the human to decide, per the analyst persona's own rules.
3. Call `get_prediction_vs_outcome` (channel_id, optional `idea_id`/`since`) to pull predicted-verdict-vs-actual-metrics rows. If the response's `truncated` flag is set, note that more rows exist than were returned and ask whether to narrow with `since` or a specific `idea_id`.
4. Call `get_channel_overview` (channel_id, optional `since`, `sections`) for a one-shot browse of ideas/research/schedule/outcomes together (C10) when the human wants the whole picture rather than one comparison.
5. Call `get_outcome_bar` (channel_id). If `configured` is `false`, tell the human no outcome bar is set yet and offer to set one via `set_outcome_bar` (channel_id, metric_name, threshold_value) — dispatch the **analyst** agent to make that call once the human confirms the metric and threshold; never default or guess a threshold in its place.
6. Call `get_calibration_trend` (channel_id, optional `since`/`before`/`limit`) for the Channel's calibration trend (C14): one row per calendar month with `candidates`/`calibrated`/`miscalibrated`/`calibration_rate`, classified against the *current* outcome bar. If `outcome_bar.configured` is `false` on the response, no trend was computed — that's the same "not configured" case as step 5, not an error.
7. Dispatch the **analyst** agent to write up the prediction-vs-outcome comparison and, if a trend was returned, the calibration trend, in plain language for the human: for each comparison row, what the verdict said, what actually happened, and whether they lined up; for the trend, report `calibration_rate` and the counts exactly as returned. An idea with a viable verdict that hasn't published yet is not yet resolved — neither calibrated nor miscalibrated — and `get_calibration_trend` already excludes it from the counts; don't report it as a miss. This is narrated back to the human, not saved anywhere new (beyond whatever outcome bar was set in step 5).

## Rules

- Never invent a numeric calibration score or accuracy rate — the only source for one is `get_calibration_trend`'s `calibration_rate`, reported exactly as returned. A Channel with `configured: false` gets an offer to set an outcome bar (step 5), never a made-up threshold or a guessed rate.
- A pending match with no plausible candidate is not a task to force-clear — leaving it pending and flagging it to the human is the correct outcome, not a failure of this skill.
- Resolving a match is a real, idempotency-guarded write (`resolve_pending_match`) — always let the analyst persona supply an `idempotency_key`, and never re-resolve a match that's already resolved just to "double check" it.
