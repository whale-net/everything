---
name: outcomes
description: Run Audience Score System's Loop 3 for one Channel — resolve any pending outcome matches, then compare what published videos actually did against the viability verdicts that predicted them — by dispatching the analyst persona against the ASS MCP server. Use once videos from a Channel's committed schedule have published and their synced metrics are ready to check against predictions.
---

# outcomes

Orchestrates ASS's outcome loop (C9, C10): clears the pending-match queue the sync worker populates, then browses prediction-vs-outcome comparisons and a whole-Channel overview. Purely read/confirm — there is no calibration-trend write path yet (that's C14/M3); this skill reports comparisons in plain language, it doesn't compute or persist an aggregate score.

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
5. Dispatch the **analyst** agent to write up the prediction-vs-outcome comparison in plain language for the human: for each row, what the verdict said, what actually happened, and whether they lined up — this is narrated back to the human, not saved anywhere new.

## Rules

- Never invent a numeric calibration score or accuracy rate — that aggregate view is explicitly out of scope until C14 (M3); state qualitatively whether predictions and outcomes lined up, and let the human draw the quantitative trend once that capability exists.
- A pending match with no plausible candidate is not a task to force-clear — leaving it pending and flagging it to the human is the correct outcome, not a failure of this skill.
- Resolving a match is a real, idempotency-guarded write (`resolve_pending_match`) — always let the analyst persona supply an `idempotency_key`, and never re-resolve a match that's already resolved just to "double check" it.
