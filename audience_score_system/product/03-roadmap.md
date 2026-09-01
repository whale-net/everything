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

Later-bucket capabilities (C15, C16) are deliberately left unscheduled past M3: the founder's V1 scope philosophy is that V2 iterates on whichever loop proves weakest, which isn't knowable until M1-M3 are in dogfood use.
