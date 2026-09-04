# Roadmap

### M1 — A single Creator and Analyst can run one idea through all three loops end-to-end, on one Channel

Delivers: C1, C2, C3, C4, C5, C6, C7, C8, C9, C10
Must not foreclose: LB1, LB2, LB3, LB4
Deliberately deferred: multi-Channel (C11 → M2), Analyst-across-Channels (C12 → M2), multi-Creator-per-Channel (C13 → M2), calibration trends across many ideas (C14 → M3), Creator delegation of research guidance (C15 → Later, unscheduled), deeper Analytics metrics (C16 → Later, unscheduled)
FR budget: 24 — deliberately above the default: this milestone is the founder's stated V1 scope philosophy in full (all three loops connected, shallow rather than deep), so its 10 capabilities span OAuth/signup, invite/accept, research write-back, viability verdicts, schedule sync, schedule draft/approve, and outcome comparison. Splitting it into smaller milestones would produce a milestone ending in a component ("the research store", "the schedule API") rather than a persona doing something — not allowed per the outcome-sentence rule, and explicitly ruled out by the founder's own framing ("do not let M1 shrink to just one loop").

### M2 — A Creator with several Channels, and the Analysts and co-Creators on them, all work across every Channel they're associated with

Delivers: C11, C12, C13
Must not foreclose: LB1 (avoid needing per-Channel re-consent as connected accounts multiply), LB2 (this is the milestone that actually exercises the join table's role-scoped authority checks beyond one row per role), LB3
Deliberately deferred: calibration trends across many ideas (C14 → M3), Creator delegation of research guidance (C15 → Later, unscheduled), deeper Analytics metrics (C16 → Later, unscheduled)
FR budget: 12 — mostly authorization and list-view plumbing over the join table LB2 already established in M1; no new loop, no new external integration.

### M3 — A Creator or Analyst can see how well the system's predictions have tracked reality across every idea on a Channel, not just one at a time

Delivers: C14
Must not foreclose: LB2 (trend queries must stay correctly scoped per person's Channel associations as they broaden in M2), LB3 (this milestone is the aggregate query LB3's FK chain exists to make cheap)
Deliberately deferred: Creator delegation of research guidance (C15 → Later, unscheduled), deeper Analytics metrics (C16 → Later, unscheduled)
FR budget: 8 — a read-side aggregate view over data every prior milestone already produces; no new write path.

### M4.1 — A Creator or Analyst can save and browse Loop 1 (research notes + viability verdicts) entirely from the web UI

Delivers: web surface for C4 (save/browse slice only — see C4/C5 scoping note; MCP surface delivered in M1), web surface for C5 (save/browse slice only — same scoping; MCP surface delivered in M1), web surface for C10 (research-notes & verdict browsing slice; MCP surface delivered in M1)
Must not foreclose: LB2, LB3, LB4 (see LB5's idempotency note — the shared store method should carry LB4's contract to the web form, to be confirmed at design time), LB5 (this milestone establishes LB5's shared-store-and-authz pattern for the first time; M4.2/M4.3 must not diverge from it)
Deliberately deferred: Loop 2 UI parity (web surface for C6, C7, and C10's schedule-drafts browsing slice → M4.2), Loop 3 UI parity (web surface for C9, C14, and C10's prediction-vs-outcome browsing slice → M4.3); a web-side research/viability *reasoning* surface is not deferred — it stays MCP-agent-only permanently, per the standing no-hosted-agent-loop non-goal
FR budget: 12 — a `web` front end (save forms + read views) over `store.ResearchStore`/`store.VerdictStore` methods and the `store.CanX` authorization checks M1's MCP tools already established; no new integration, no new schema, comparable in shape to M2's join-table-plumbing scope. Includes appending this milestone's own NFR3 amendment paragraph to `ARCHITECTURE.md` per LB5's requirement.

### M4.2 — A Creator or Analyst can read the Channel's synced schedule, draft/pace a proposed schedule, and browse past schedule drafts entirely from the web UI

Delivers: web surface for C6, web surface for C7 (MCP surfaces delivered in M1), web surface for C10 (schedule-drafts browsing slice; MCP surface delivered in M1)
Must not foreclose: LB2, LB3, LB4, LB5 (must reuse the shared store+authz pattern M4.1 established, not a parallel implementation)
Deliberately deferred: Loop 3 UI parity (web surface for C9, C14, and C10's prediction-vs-outcome browsing slice → M4.3)
FR budget: 12 — same shape as M4.1: a front end over `store.ScheduleStore`'s existing sync-read and draft/pacing methods, the ones `mcp`'s schedule-draft tools and issue #1648's C8 web surface already share; C8 (commit/un-commit/edit) is already dual-surface and stays out of scope here. The added C10 schedule-drafts browsing slice reuses the same draft-list fetch this milestone's draft/pacing UI already needs — no new store method — so it doesn't move the budget. Includes appending this milestone's own NFR3 amendment paragraph to `ARCHITECTURE.md` per LB5's requirement.

### M4.3 — A Creator or Analyst can compare predicted vs. actual outcomes and browse calibration trends across ideas entirely from the web UI

Delivers: web surface for C9 (MCP surface delivered in M1), web surface for C14 (C14's own MCP delivery is M3, not yet delivered — see the M3 dependency below), web surface for C10 (prediction-vs-outcome comparison browsing slice; MCP surface delivered in M1)
Must not foreclose: LB2, LB3, LB4, LB5
Deliberately deferred: none further — this closes M4's UI-parity scope. All three of C10's browsing slices are now claimed across M4.x: research-notes/verdicts in M4.1, schedule-drafts in M4.2, prediction-vs-outcome comparisons in M4.3. C15/C16's interface allocation stays unspecified/unscheduled, unaffected by this amendment; C17 (Next, scheduled-but-unallocated per the `channel-history-grounding` amendment) is likewise unaffected by this amendment — its interface allocation is a separate decision for whichever design round schedules it.
FR budget: 10 — read-side rendering (outcome comparison, calibration-trend aggregate) plus pending-match confirm/reject, over store methods `mcp`'s C9 tools and M3's C14 aggregate query will establish; no new write path beyond confirm/reject. Includes appending this milestone's own NFR3 amendment paragraph to `ARCHITECTURE.md` per LB5's requirement. **Depends on M3 (C14) having shipped** — the calibration-trend aggregate this milestone renders doesn't exist before M3.

Later-bucket capabilities (C15, C16) are deliberately left unscheduled past M3: the founder's V1 scope philosophy is that V2 iterates on whichever loop proves weakest, which isn't knowable until M1-M3 are in dogfood use.
