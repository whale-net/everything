# Roadmap

Five milestones. The default guide is three, but two of them were made mandatory at intake — design sessions (round 1, Q1) and a Swarm Operator console (round 1, Q3) — and the work axis does not compress into either. Five is the cut that keeps each entry independently useful; past about six the map is two products.

**Zero numbered FRs or NFRs appear here or anywhere in this brief.** Milestones deliver capabilities; FRs are written per-milestone by `/project-manager:design --milestone`.

**Where the adoption POC landed, and why.** krill self-hosts in **M1** — this brief becomes krill's first record and regenerating `krill/PRODUCT.md` from it is M1's proof that the entity model holds. The **adoption of `whagent_net` opens M2**, now as its own capability **C27** rather than as a second application of C8, for four reasons: (1) M1's proof obligation is that the model can hold *its own* brief — a model that cannot has already failed, and re-running the same importer tests nothing new about M1's claim, whereas holding a product krill did not author tests whether the model generalizes past its author, which is a different claim and therefore a capability rather than a verification activity; (2) `whagent_net`'s brief is the richest in the repo (26 capabilities, 7 LB entries, 3 milestones, four files, `Must not foreclose` lists citing LB ids, one milestone in flight), so importing it will surface entity-model and renderer gaps that are *work*, not verification, and that work should not be charged against M1's budget; (3) M1 already absorbs the new-domain scaffolding tax architect flagged — `BUILD.bazel` with `release_helm_chart`, a `migrate` job app, `Tiltfile`, the doc set, a `plugin/` directory — none of which any capability covers; (4) opening M2 with the adoption gives the design-session surface a live, externally-owned product to run against from its first day rather than only krill's own.

The deferral's risk — discovering in M2 that the model cannot hold someone else's brief — is bought down inside M1 for free: `whagent_net/PRODUCT.md` and its `product/*` split are **read as an input** to M1's entity-model and renderer design (they are the hardest shape in the repo), they are simply not **imported** until M2.

**Success condition for both migrations, M1 and M2 alike:** every entity round-trips, and the regenerated doc set is reviewed once as **semantically equivalent** to the committed original — every capability, decision, persona, non-goal, and milestone present, with the same identifiers and the same meaning. Explicitly **not** byte-equivalence: forcing the renderer to reproduce hand-written prose quirks tests the wrong thing. The *milestone* clause of that condition is what obliges M1 to hold milestones it cannot author — see M1's LB6 note.

---

### M1 — An Agent can pull exactly the spec slice its task touches out of krill, for a product whose brief now lives in krill and is rendered back into the repo

krill self-hosts: immediately after the initial API/MCP build-out, the first product migrated in is **krill itself**, and this brief is its first record. The outcome names an Agent deliberately, for the reason recorded under *Personas* — the Requirement Contributor has no unmediated path until C12 in M2, and no RC-facing surface is added here to paper over that.

```
Delivers: C1, C2, C3, C4, C5, C6, C7, C8, C9
Must not foreclose: LB1, LB2, LB3, LB4, LB5, LB6, LB7 — all seven, each for its own reason:
  LB1 — M1 creates every spec table there is, so `scope_id` is non-null from the first
    migration or C22 and C23 are later a backfill across every row that exists.
  LB2 — M1 mints the ids C7's rendered docs cite by number and C8's import must
    round-trip; a stored display number is the renumbering migration LB2 exists to prevent.
  LB3 — M1 creates no work tables, so M1 can only get the *spec* side of the boundary
    wrong, and getting that side wrong forecloses C5 and C6, which are M1's own.
  LB4 — both subject slots are populated from M1 even though the acting subject is always
    the human who called `init`; the second slot is what makes C24 a column populate
    rather than a backfill against rows whose agent identity is unrecoverable.
  LB5 — M1 builds both the renderer and the importer, the only two code paths that could
    ever make a committed doc writable; C21 is computable only if they stay one-way.
  LB6 — the non-obvious entry, and architect verified it earns its place: M1 imports a
    document that *contains* a roadmap, so M1 is where a `milestone_id` column would
    actually get written. What M1 ships instead is in the LB6 note below.
  LB7 — the scoped slice C3 returns is the same typed document M4's claim payload
    enriches; shaped instead as a tool response for an interactive reader, M4 needs a
    second projection of the same data and the two drift.
Deliberately deferred: design sessions (C10, C11 → M2); mediated intake for the Requirement
  Contributor (C12 → M2 — see Personas for why nothing here papers over the gap); adoption
  of a product krill did not author (C27 → M2); authoring the delivery axis (C13 → M3) and
  delivery-status visibility (C28 → M3) — M1 holds and renders milestones that arrive
  inside an imported document and authors none, per the LB6 note below; the entire work
  surface (C14, C15, C16, C25 → M4); operator console and escalation (C17, C18, C26 → M5);
  web UI (C19 → Later); krill owning branch/PR lifecycle (C20 → Later, references only
  here); drift detection (C21 → Later); second repo or tenant (C22 → Later, scope column
  present from M1 per LB1); cross-product decisions (C23 → Later); agent identity
  (C24 → Later, column shape present from M1 per LB4)
FR budget: 12
```

**The LB6 note — what M1 ships on the delivery axis, and what it does not.** M1 renders `product/03-roadmap.md`, and the success condition above requires every milestone in an imported brief to come back with the same identifiers and the same meaning — including `Must not foreclose: LB1, LB4` lines, which are cross-entity references, not prose. So M1 ships **LB6's association table, keyed `(entity_id, milestone_id)`, and nothing else**: no status semantics, no milepebbles, no `partially complete`, no authoring surface. **M1 can hold and render milestones that arrived inside an imported document; M3 owns authoring (C13) and status (C28). Holding is not planning.** FRs about the roadmap section therefore cite **C8** (nothing lost in the move) and **C7** (rendered back), never C13 — an M1 FR that wants to *create* a milestone or *status* one is out of scope and belongs to M3. This is the cheaper of the two options architect offered: it avoids a prose→entity migration in M3 for both already-imported products, and it keeps each milestone's `Must not foreclose` list as real references C21's drift detection can eventually see rather than untracked prose it cannot.

**Notes for design.** Two things architect surfaced are cheap now and awkward later: the two-front-door pattern is already built twice in this repo (`audience_score_system/mcp/main.go` mounts `mcpauth` and `libs/go/whagent`'s verifier side by side, both env-gated), so agent-vs-human authentication is not an open problem to be scoped as one; and mounting the spec and work surfaces as two pre-filtered MCP endpoints (`whagent_net`'s `/mcp/readonly` vs `/mcp/ops`) would make "a worker never reads the spec surface" a structural invariant in M4 instead of a rule workers are trusted to honor — the spec endpoint is the half that exists in M1. The `AGENTS.md` carve-out for generated docs must land in M1, before anyone but the requester depends on a rendered file, or an agent will hand-edit one and lose the edit. **And one piece of consumer-side work no capability covers and none should: something in `tools/project-manager` or in the agent instructions has to actually start *querying* krill for krill's own spec during M1, or M1's self-hosting loop is real but never exercised** — same class as the scaffolding tax M1 already absorbs, and it should be found at design time rather than at validation.

**Pre-agreed over-budget cut**, taken only if the design draft exceeds budget with every FR tracing correctly, in this order: **C5 → M2 first** — the store is SCD2 from M1 regardless (LB3), so the as-of *read* surface can follow without a migration — then **C9 → M2**: nothing else in M1 depends on the GitHub pointer artifact, LB1 puts the forge coordinates on the `scope` table whether or not C9 ships, and C20's branch-name condition does not bite until M4. Never widen an FR to keep a capability in. **C6 cannot move in either position**: supersede-rather-than-overwrite is the write path SCD2 exists for.

---

### M2 — A Requirement Contributor can bring an idea to krill in plain language and watch a producer-role Agent turn it into spec entities through a recorded design session, on a product krill did not write

Opens with the **`whagent_net` adoption (C27)** — the first product in krill that is not krill, and the first evidence the model generalizes past its own author — and then closes the Requirement Contributor's path in, so the persona that has existed in the model since M1 finally has a surface. The Discussion-plus-gist dance this brief is currently being written through is what M2 deletes.

```
Delivers: C10, C11, C12, C27
Must not foreclose: LB1, LB2, LB3, LB4, LB5, LB7 — LB2 because a design session's draft
  revisions are a supersession chain over entities whose surrogate ids must outlive every
  revision, which is C6's machinery rather than a second copy of it; LB3 because M2 adds
  tables M1 never saw, so the per-table boundary call genuinely happens here (reading
  below); LB4 because C12 is the first milestone in the roadmap where the acting subject
  and the on-behalf-of subject genuinely differ (reading below); LB5 because a mediated
  intake path writes to krill and only to krill and must never acquire an "and also update
  the doc" branch; LB1 and LB7 as in M1 — C27's import writes scope-qualified rows, and
  the design surface reads the same typed slice document rather than a second projection.
Deliberately deferred: authoring the delivery axis (C13 → M3) and delivery-status
  visibility (C28 → M3); the work surface (C14, C15, C16, C25 → M4); operator console and
  escalation (C17, C18, C26 → M5); web UI (C19 → Later) — design sessions are API/MCP-only
  here, as M1's surfaces are
FR budget: 12
```

**Notes for design — LB3's reading in M2, stated rather than assumed.** A design session's revisions are **append-only with a derived current view, not SCD2**. `AGENTS.md` § SCD2 explicitly carves out append-only event logs and points at `LEAD(valid_from) OVER (...)` for an SCD2-shaped *view* over one, which is the right shape here: the session record is an event log and "the current draft" is a window over it, not a `valid_to` write on every revision. LB3 is **listed** rather than omitted so architect re-checks this per-table at design time instead of inheriting it from this note — M2 is the first milestone that creates tables M1's boundary call never covered.

**Notes for design — LB4 in M2.** C12 has a producer-role Agent writing spec entities on a Requirement Contributor's behalf. That is the first genuine divergence of the two subjects — LB4 itself concedes "in M1 the acting subject is the human who called `init`" — so if the mediated path records only the RC, or writes the RC into both slots, C24 becomes LB4's own stated failure: "a backfill against rows whose agent identity is unrecoverable." Both slots, distinctly populated, on every write C12 makes.

---

### M3 — A Requirement Contributor can cut a krill product into shippable milestones and agent-shippable milepebbles and see, from krill alone, what is planned versus what is merely spec'd

Small, and useful on its own: with M1–M3, the whole producer/architect loop runs inside krill and delivery status is a column. This is the milestone that deletes the `Ledger: M<n> → <status>` append-only comment register, the "last one wins" rule, `adopted-but-pending-amendment`, and the cap of two pending adoptions — a hand-rolled last-writer-wins register that exists only because GitHub has no transactional row. Work is still executed through the existing pipeline at this point; krill is where the plan lives.

```
Delivers: C13, C28
Must not foreclose: LB2, LB6, LB7
Deliberately deferred: the work surface (C14, C15, C16, C25 → M4); operator console and
  escalation (C17, C18, C26 → M5); branch/PR lifecycle (C20 → Later)
FR budget: 12
```

**Notes for design.** LB6 is the whole point of this milestone and the easiest to violate while building exactly what it describes: the delivery axis is an association keyed `(entity_id, milestone_id)`, never a `milestone_id` column on a Feature or an FR. M1 already ships that association (see M1's LB6 note), so M3 is **not** a prose→entity migration of the two already-imported roadmaps — it adds the authoring surface (C13), milepebble granularity, and the status semantics (C28), "truncate, never rewind" and `partially complete` both carried from intake, on top of rows that already exist. C13 and C28 are two capabilities rather than one precisely so FR-cites-a-capability does real work against a budget of 12: an FR about cutting a milestone cites C13, an FR about what a status means cites C28, and an FR that can cite neither is not in this milestone.

---

### M4 — An Agent can claim a task from krill, get everything it needs in the claim payload, report a verdict without knowing where the task goes next, and hand a half-finished task to a fresh run on another host

```
Delivers: C14, C15, C16, C25
Must not foreclose: LB1, LB3, LB4, LB6, LB7 — LB1 is listed here because M4 creates the
  largest family of new tables in the roadmap (task, claim, lease, attempt, note) and a
  missed `scope_id` on any of them is a backfill across the entire work axis; LB3 governs
  the mutation shape of every one of those tables; LB4 puts both subjects on every claim,
  verdict, and note; LB6 keeps the work axis hanging off delivery rather than off the spec
  chain; LB7 is what makes C14 enrichment instead of a second projection.
Deliberately deferred: the human-attention queue, the rejection-history carry, and all
  operator-facing escalation handling (C26 → M5) — M4 does **not** ship an uncapped loop:
  its reclaim path refuses to re-serve a task past its attempt cap, per the second
  condition below, so what M5 adds is where exhausted work *goes* and who can see and act
  on it, not the stop itself; operator console (C17, C18 → M5); krill owning branch/PR
  lifecycle (C20 → Later)
FR budget: 12
```

**A condition recorded from architect's C20 rejection, inherited by this milestone: krill must never derive task state from a branch name.** Today's pipeline is forced into exactly that — `CONVENTIONS.md` § Git hygiene derives the branch deterministically from the issue title and attempt number and states it is "never invented fresh or persisted anywhere separately," so the retry counter lives in a git ref. A krill task's attempt count, lane, and lease are rows. Violating this is the single thing that turns C20 from cheap into expensive.

**A second condition, inherited from C26 in M5: M4's reclaim path must refuse to re-serve a task past its attempt cap; the cap's value, the escalation destination, and any operator-facing handling are C26 in M5.** It is recorded here because M4 does not ship a queue somebody has to poke: it ships claim **plus** lease expiry and stale-reclaim (the `ClaimBatch` shape the current-state section cites, reclaiming on `claimed_at < staleBefore`) **plus** C16, resume on another host. That combination is automatic, cross-host redelivery, so a crash-looping task re-hands itself to a fresh agent on a fresh host at machine speed — and "must be run attended" is not a mitigation for a loop faster than a human can read it, least of all in a milestone that defers both the console that would show it (C17) and the verb that would stop it (C18). An attempt counter and a terminal state the reclaim path will not re-serve is Agent-axis behavior, sits inside M4's own outcome sentence, and is roughly one FR.

**Notes for design.** LB7 is what makes this milestone small rather than large: the claim payload's core *is* M1's scoped slice document, so C14 is enrichment over a typed document that already exists — if it turns into a second projection of the same data, the two drift, which is `whagent_net`'s LB1 learned expensively. **The work surface holds at six verbs** (`init`, `claim`, `heartbeat`, `complete`, `abandon`, `note`), and it holds on LB7 plus payload-enrichment-is-not-a-verb: answer "but the agent needs to coordinate" with a richer payload, never a seventh verb. C25 sits here rather than in M3 because `note` is what justifies its place among those six.

**On architect's nitpick that C16 cannot ship apart from C14** — agreed on the substance, declined as a merge. They ship in the same milestone, which is the part that matters for the FR budget, but they remain two lines because they fail differently: C14 fails when a payload is incomplete, C16 when it is complete but not *durable* across a host change. An FR citing "resume on another host" should not have to cite a capability about payload contents.

**Pre-agreed over-budget cut**, taken only if the design draft exceeds budget with every FR tracing correctly — one lever, and it is **C25's lifecycle → M5**, nothing else. The `note` verb stays in M4 as a flat append (it is the sixth verb and the payload's escape hatch) while the lifecycle — noted → carried over / deferred / closed — moves to M5, where notes are read anyway and the operator console is the thing that acts on them. LB3 already makes notes append-only, so adding lifecycle states later is columns, not a migration. Never widen an FR to keep a capability in. **C16 is explicitly not a viable lever:** if LB7 holds, resume is a re-fetch of a typed document by task id, so cutting it saves almost nothing and removes the only thing that distinguishes M4's outcome from a plain queue. Nor is the claim+payload / verdict+notes seam a viable *split* of this milestone: an agent that can claim and work but cannot report leaves every task to be released by lease timeout, which is the same footgun the second condition above exists to close.

---

### M5 — A Swarm Operator can leave the swarm running unattended and afterwards answer, from krill alone, what is claimed, what is stuck, what escalated to a human and why — and act on any of it without hand-editing storage

The milestone that makes an unattended swarm safe to leave running. M4 ships the bare stop (its second recorded condition); C26 is what makes that stop *useful* — where exhausted work goes, with its rejection history, and how a human is told. Two independent counters with different caps and different destinations are in scope here: run attempts (crashes, `abandon`) and lane thrash (repeated negative verdicts ping-ponging a task between lanes). Every other loop in the pipeline already has a cap — stakeholder rounds, design-panel rounds, FR budget, LB entry count — and unbounded Implementation↔Testing ping-pong is a real undetected failure mode today.

```
Delivers: C17, C18, C26
Must not foreclose: LB1, LB3, LB4
Deliberately deferred: a web UI for the console (C19 → Later) — an API-only first cut
  satisfies this milestone and no web UI is required to close it; krill owning branch/PR
  lifecycle (C20 → Later); drift detection (C21 → Later); second repo or tenant
  (C22 → Later); cross-product decisions (C23 → Later); agent identity (C24 → Later)
FR budget: 12
```

**Notes for design.** LB4 is what makes an intervention answerable rather than merely possible — "who requeued this, on whose behalf" is two subjects on a row, not an audit log bolted on afterwards. LB1 matters here for the first time in a visible way: the console's every query predicate is scope-qualified, so a second tenant later is a filter rather than a rewrite. **Conditional inbound scope:** if M4's pre-agreed cut is taken, C25's note lifecycle lands in this milestone and counts against this budget — design M5 expecting it, since the console is the surface that acts on a note either way.
