---
name: research
description: Run Audience Score System's Loop 1 end-to-end for one topic — research it into cited notes, then reach a viability verdict — by dispatching the researcher and analyst personas against the ASS MCP server. Use when a Creator or Analyst has a topic/idea for a Channel and wants a grounded viable/not-viable/needs-more-research call, not just raw data fetched.
---

# research

Orchestrates ASS's research→verdict loop (C4, C5): a topic goes in, a cited verdict (with its supporting notes) comes out. This skill itself makes no MCP calls beyond `whoami`/`list_ideas` bookkeeping — it dispatches the `researcher` and `analyst` agents (in this same plugin) to do the actual gathering and judging, and loops between them until the verdict stops asking for more research.

## Usage

```
/audience-score-system:research <channel_id> "<topic or idea title>" [--server dev|prod]
```

If `--server` is omitted, ask which of `audience-score-system-mcp-dev` / `audience-score-system-mcp-prod` to use before doing anything — don't default silently, dev and prod are different Channels/Ideas/Persons.

## Steps

1. Call `whoami` on the chosen server to confirm the resolved Person, then `list_ideas` (channel_id) to check whether an Idea matching the topic already exists and, if so, what its current verdict state is (`list_ideas`' summary includes whether a verdict exists yet).
2. Dispatch the **researcher** agent with: the channel_id, the topic/title, which MCP server to use, and (if an Idea already exists) its idea_id plus a note to read existing research before adding more. It creates the Idea if needed, writes cited research notes, and reports back what it found and any gaps it couldn't close.
3. Dispatch the **analyst** agent with the same channel_id/idea_id/server to hold the viability discussion strictly against the research store (`list_research_notes` + `get_viability_verdict`) and call `save_viability_verdict`.
4. If the analyst's verdict is `needs-more-research`, read its `reasoning` for what's missing, dispatch the **researcher** agent again scoped to exactly that gap, then dispatch the **analyst** agent again. Repeat until the verdict is `viable`/`not-viable`, or until the human tells you to stop (e.g. the gap isn't answerable, or they want to weigh in with a Creator/Analyst-level judgment call themselves).
5. Report the final verdict, its `reasoning`, the cited research note IDs, and the verdict's version number (`get_viability_verdict` returns the full history) to the human.

## Rules

- Never skip straight to a verdict without at least one `researcher` pass — even an Idea with prior research from an earlier session should have that research re-read (not re-gathered) by the analyst before judging.
- This skill does not decide viability itself; it only routes between the two personas and reports their output. If the human disagrees with the analyst's verdict, that's a new `save_viability_verdict` call (a new version) with the human's own reasoning, not an edit to this skill's run.
- A verdict of `not-viable` or a second consecutive `needs-more-research` on the same gap is a valid, complete outcome — stop and report it rather than forcing another research pass.
