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

### M2.1 — A Creator or Analyst can turn a viable idea into a greenlit video script and see how it tracked to the published video, with no schedule/calendar step in between

*(M2.1 is not a sub-phase of shipped M2 — the decimal denotes sequence position only, between M2 and M3, following the same independent-milestone precedent M4.x already set.)*

Delivers: C18, C19
C9/C10 note: not (re-)delivered here — C9 was shipped in M1 and its capability text is unchanged by this milestone; C10 is FK-retargeted in place (schedule drafts → video scripts, see `02-capability-map.md`) as part of this milestone's migration, not delivered fresh.
Must not foreclose: LB2, LB3 (as rewritten by amendment `video-script-model`), LB4
Deliberately deferred: none — this milestone re-cuts Loop 2 rather than extending it; pacing/calendar mechanics are removed, not deferred (see amendment `video-script-model`, issue #1562, for C6's specific disposition).
FR budget: 12 — new `video_script` store methods and authz checks (C18/C19), retiring the pacing/schedule-draft/commit store surface, C9/C10's FK retarget, and re-anchoring the *already-shipped* M1 outcome-matching path (`list_pending_matches`/`resolve_pending_match` and the `get_prediction_vs_outcome` read) from `schedule_entry` to `video_script`. That re-anchor is not a mechanical FK swap: `worker/sync/matching.go` scores auto-link candidates as `titleWeight(0.7)*titleSimilarity + dateWeight(0.3)*dateProximity` against `MatchConfidenceThreshold = 0.8`, and removing the scheduled-date signal caps title-only scoring at 0.7 — below auto-link threshold by construction, for every candidate. Resolving this (a target-date field on `video_script` feeding `dateProximity`, retuned weights/threshold, or accepting auto-link's effective removal as a stated behavior change) is real matching-logic design work that has to happen inside this same milestone alongside the migration — both are why the budget sits at 12, the upper end of the milestone-sized range, rather than the smaller read-side-only shape M3's 8 has. 12 is a ceiling under any of that open question's resolutions (tracked on amendment `video-script-model`, issue #1562), not a placeholder pending which one is picked.

**Dependency flag: M4.x needs re-spec once this lands.** M4.2 (`ui-parity` amendment, issue #1811 — present only in that open PR, not yet on `main` as of this amendment) was specced as web UI over the existing schedule/pacing store methods (C6/C7/C8) delivered in M1, including a C10 "schedule-drafts browsing" slice. Once M2.1's `video_script` model lands, M4.2 can no longer target those store methods or that C10 slice — its scope needs to be re-derived from M2.1's actual data model (`video_script` propose/approve/browse), not from the pre-amendment C6/C7/C8 text. This spans all three M4.x milestones (also not yet on `main`): #1811's C10-slice enumeration covers research-notes/verdicts in M4.1, schedule-drafts in M4.2, and prediction-vs-outcome comparisons in M4.3, and M4.3's "deliberately deferred: none further" closure claim rests on that three-way enumeration being complete and correct — revising C10's text here means the "schedule-drafts" slice named in that enumeration won't exist under the new model, so M4.3's closure claim needs re-checking against M2.1's actual object set too, not just M4.2's store methods. Merge order matters: whichever of #1811 or this amendment (`video-script-model`) merges second must reconcile the other's now-stale text against whatever is actually on `main` at that time — this note does not itself rewrite any M4.x text.

LB5's `At risk` list (also present only in open PR #1811, not on `main`) needs the same flag: it lists "C4, C5, C6, C7, C9, C10, C14" as capabilities that become dual-surface as their own M4.x milestone ships. Of those, C6 and C7 are the ones this amendment retires — LB5's forward-looking claim that they'll get a web surface can't hold once they're gone. C9 is unaffected by this amendment, C10 is revised in place (not retired, still a valid dual-surface target), and C14 is untouched. Same merge-order rule applies: whichever of #1811 or this amendment merges second reconciles the other's now-stale C6/C7 claim in LB5.

### M3 — A Creator or Analyst can see how well the system's predictions have tracked reality across every idea on a Channel, not just one at a time

Delivers: C14
Must not foreclose: LB2 (trend queries must stay correctly scoped per person's Channel associations as they broaden in M2), LB3 (this milestone is the aggregate query LB3's FK chain exists to make cheap)
Deliberately deferred: Creator delegation of research guidance (C15 → Later, unscheduled), deeper Analytics metrics (C16 → Later, unscheduled)
FR budget: 8 — a read-side aggregate view over data every prior milestone already produces; no new write path.

### M4.1 — A Creator or Analyst can save and browse Loop 1 (research notes + viability verdicts) entirely from the web UI

Delivers: web surface for C4 (save/browse slice only, per LB5's scoping — the reasoning conversation itself stays MCP-agent-only; MCP surface delivered in M1), web surface for C5 (save/browse slice only, same scoping; MCP surface delivered in M1), web surface for C10 (research-notes & verdict browsing slice; MCP surface delivered in M1)
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
