# App Registry UI — User Journeys, Wave 2 (2026-08-23)

A second round of point-in-time usability findings from 20 more simulated user
sessions (personas 11-30) against the App Registry web UI (`app-registry-ui`),
run the same day and in the same local `tilt up` instance as
[USER_JOURNEYS_2026-08-23.md](USER_JOURNEYS_2026-08-23.md)'s first ten. Read
that file first — this one assumes its methodology, scorecard format, and
finding numbering, and extends rather than repeats them. Like its
predecessor, this is a dated snapshot, not a living doc; see
[USER_JOURNEYS_2026-08-23_WAVE2_TRANSCRIPTS.md](USER_JOURNEYS_2026-08-23_WAVE2_TRANSCRIPTS.md)
for the full per-persona evidence behind every finding below.

## Methodology

Same shape as wave 1: 20 personas, the same shared 10-question interview set,
each an independent fresh-context agent driving a real Chromium browser via
Playwright MCP against `http://localhost:8090`, UI-only, seeded by the same
[`scripts/seed_tilt_walkthrough.py`](../scripts/seed_tilt_walkthrough.py)
dataset. Run in 5 waves of 4 concurrent agents (30 total across both rounds).
This cohort deliberately covers ground wave 1 didn't: an intern and a curious
non-technical explorer with no task at all, a designer doing a pure visual
critique, an accessibility specialist working keyboard-only, a mobile-viewport
session, an edge-case/input-validation probe, a compliance auditor hunting
for exports, a data analyst hunting for trend metrics, an automation
engineer hunting for an API, a platform lead evaluating onboarding a new
domain, a chart owner doing blast-radius analysis, an incident commander
under a hard time budget, and a platform engineer explicitly comparing the
tool to ArgoCD's UI.

| # | Persona | Role / technical level | Task |
|---|---|---|---|
| 11 | Taylor Kim | Intern, non-technical, day one | "Is leaflab's worker live in prod?" — name unknown, must search |
| 12 | Morgan Ellis | Product designer, non-technical | Open-ended visual/design critique, no fixed task |
| 13 | Devon Park | Senior on-call SRE, expert | 3am page: "is this a bad deploy?" — 5-minute patience budget |
| 14 | Nadia Osei | Compliance/audit specialist, semi-technical | Produce a handoff-ready quarterly promotion export for an auditor |
| 15 | Owen Bryant | Platform team lead, technical | Could another org onboard a new domain to this tool? |
| 16 | Ravi Deshmukh | Automation engineer, technical | Find an API/webhook to build a Slack bot against |
| 17 | Ellis Moreau | Accessibility specialist, semi-technical | Full journey to a promote form, keyboard-only |
| 18 | Casey Nakamura | New engineer, week two, semi-technical | 20-minute self-guided onboarding tour, hunting for a glossary |
| 19 | Vic Torres | Staff engineer, expert, skeptical | Deliberately break forms with edge-case input |
| 20 | Ingrid Larsen | Data/tooling analyst, semi-technical | Build promotion-frequency and lead-time numbers |
| 21 | Malik Osei | Support engineer, semi-technical | Support ticket: who approved a prod rollback, and why |
| 22 | Fiona Grant | Release manager, technical | Coordinate a 3-app, 2-domain release train |
| 23 | Priyanka Shah | Senior SRE, expert | Full forensic reconstruction of the one seeded drift case |
| 24 | Theo Baptiste | Junior engineer, semi-technical | Check prod status and open a promote form at a 390px mobile viewport |
| 25 | Sunny Park | Curious non-technical explorer | No task — could you explain this tool to a friend afterward? |
| 26 | Grace Liu | Engineering director, semi-technical | 15-minute fleet-wide health snapshot for a leadership update |
| 27 | Baxter Wells | Chart maintainer, technical | Pre-bump blast-radius analysis for a shared chart |
| 28 | Noor Aziz | Incident commander, expert | Scope "elevated errors in manmanv2 prod" in under 60 seconds |
| 29 | Lena Kowalski | Platform engineer, ex-ArgoCD-heavy, technical | Explicit compare-and-contrast against ArgoCD's UI |
| 30 | Marcus Iyer | Power user, expert, daily user | Hunt for keyboard shortcuts, bulk actions, sortable columns |

Full reports:
[USER_JOURNEYS_2026-08-23_WAVE2_TRANSCRIPTS.md](USER_JOURNEYS_2026-08-23_WAVE2_TRANSCRIPTS.md).

## A methodology finding that changes how to read wave 1's #1

Every wave-2 persona was explicitly told, up front, that other concurrent
test sessions might be driving the same local browser context, and to
attribute a stray navigation to that rather than to the product unless a
repeatable mechanism could be pinned down. **Nearly every persona hit it**
(Vic, Ellis, Owen, Ravi, Casey, Priyanka, Baxter, Noor, Lena, Marcus, Sunny,
Malik, Ingrid, Fiona, Theo, Grace — 16 of 20), several severely: Vic
accumulated 20+ hijacked tabs and had to fall back to atomic JS
`evaluate()`/`fetch()` calls to get any clean signal at all; Baxter needed
five navigation attempts before one stuck. With that warning in hand, every
one of these personas correctly identified it as shared-session noise, not a
single-user product defect — Playwright MCP tool calls in this exercise run
against one real Chromium instance, and 4 concurrent agents each opening and
clicking through tabs in the same browser context will absolutely steal each
other's tabs.

Wave 1's personas ran under the **exact same concurrency** (4-at-a-time) but
were never told this was a possible explanation, and independently guessed
at product-side mechanisms instead ("a live-activity feed force-navigating
the current view," "dev-server hot-reload noise" — see wave 1 finding #1).
Given how cleanly wave 2 reproduces the same symptom under the same
concurrency and how confidently wave 2 attributes it to session-sharing once
warned, **wave 1's "navigation gets hijacked mid-task" finding was very
likely substantially — possibly entirely — this artifact, not a product
bug.** This doesn't fully clear the product: a few wave-1 reproductions
(Sam's mid-keystroke redirect while alone on one form, in particular) are
harder to explain via tab-sharing alone, so it's worth a small, cheap,
single-agent (no concurrency) re-check before fully retiring wave 1's #1 —
but it should no longer be treated as this codebase's top-priority
engineering investigation on the strength of the original 7-8/10
corroboration count, since that count was never controlled for the
concurrency artifact this wave surfaced. Treat wave 1 finding #1 as
**downgraded to "unconfirmed, likely a test-harness artifact"** pending that
single-agent re-check, and read the rest of this document's findings as
being logged by personas actively aware of and correcting for the artifact.

## Scorecard

| Persona | Navigability /5 | Usefulness /5 | UI over CLI/alternative? |
|---|---|---|---|
| Taylor (intern) | 2.5 | 4 | Yes, with a double-check |
| Morgan (designer) | 4 | 4 | Yes, for "what's live," no for "why" |
| Devon (on-call) | 2 | 2 | No — straight to kubectl/ArgoCD |
| Nadia (auditor) | 2 | 2 | No — ask an engineer, check server/DB directly |
| Owen (platform lead) | 2 | 3 | Not yet — onboarding contract undocumented |
| Ravi (automation) | 4 | 2 | No — go find the gRPC/CLI client |
| Ellis (accessibility) | 3 | 4 | Yes for the happy path, gaps for AT users |
| Casey (onboarding) | 3 | 4 | Yes, with one teammate check before a real promote |
| Vic (edge cases) | 4 | 3 | N/A — robustness probe |
| Ingrid (analyst) | 2 | 2 | Not today — no trend/metrics surface |
| Malik (support) | 2 | 3 | Partial — facts check out, premise doesn't |
| Fiona (release mgr) | 3 | 3 | Yes, single-app; no batch concept yet |
| Priyanka (drift deep-dive) | 4 | 5 | Yes, unusually thorough |
| Theo (mobile) | 2 | 3 | No — not on a phone today |
| Sunny (explorer) | 4 | 4 (n/a) | Yes, once you find Drift & Audit |
| Grace (director) | 3 | 3 | Yes for env-level, no for domain-level |
| Baxter (chart owner) | 3 | 4 | Yes, forward lookup; reverse lookup partial |
| Noor (incident cmdr) | 4 | 5 | Yes — beat the 60-second budget |
| Lena (ArgoCD compare) | 4 | 4 | Additive, not redundant, with a vocabulary caveat |
| Marcus (power user) | 3 | 4 | Yes for correctness, no for throughput |
| **Average** | **3.0** | **3.4** | **11 yes / 6 conditional / 3 no** |

Slightly lower navigability than wave 1's 3.5 average — expected, since this
cohort deliberately targeted harder edges (mobile, keyboard-only, edge-case
input, export/audit, onboarding) that wave 1 didn't probe. Usefulness holds
roughly steady. Read both loosely, same as wave 1: 30 opinions total now, still
not a powered survey.

## Cross-cutting findings

Ranked by corroboration and actionability, continuing wave 1's numbering
where a finding is the same one deepening, and starting fresh numbers for
genuinely new findings this wave surfaced.

### 2b. Wave 1 finding #2 (Apps Catalog vs. app detail contradiction) — reconfirmed twice more

Taylor and Grace both independently hit the exact same "not promoted" cell
meaning "not promoted *directly*, still live via its chart" confusion wave 1
already flagged (Marcus, Dana, Vik, Jordan) — Taylor nearly gave her manager
wrong information because of it, Grace flagged it as "a landmine for someone
skimming." Now corroborated by 6 of 30 personas across both waves. No change
to wave 1's assessment beyond raising confidence it's a real, high-priority
bug in the catalog's per-cell computation, not a one-off misreading.

### 4b. Wave 1 finding #4 (promote/rollback direction reads backwards) — escalated from n=1 to n=3, independently

Wave 1 flagged this at n=1 (Leo) specifically *because* it needed
verification. Malik and Ingrid — working completely independently, on
different tasks, on the same seeded `manmanv2-host-manager` rollback event —
both hit it on their own: Malik nearly clicked a live rollback icon thinking
it was a history marker, then found the actual sequence read "promote:
v1.1.0 → v1.0.0" followed by "rollback: v1.0.0 → v1.1.0" and had to
explicitly recompute which one restored the prior state; Ingrid flagged the
same pair as "reads backwards to me." Lena separately confirmed the
*mechanism* isn't a bug — `rollback` correctly restores whatever version was
previously current, which can be numerically higher — but three independent
testers misreading the same real event on the same day is no longer a "flag
for verification," it's a confirmed UX gap. **Upgrade this to a real
priority**: show a plain before→after version display next to the
promote/rollback label instead of relying on the verb alone.

### 3b. Wave 1 finding #3 (Pending / not-yet-committed vs. live badges) — deepened, and now shown to be intentional architecture

Nadia, Ingrid, Priyanka, Malik, and Lena all independently drilled into
promotion detail pages and hit the same "Committed: Not yet committed / Sync
triggered at: Not yet triggered" state wave 1 flagged as possibly a
seed-environment writeback artifact. Lena's session resolved the open
question wave 1 left hanging: the "Current sync status" / "Current health
status" fields on a Promotion Details page are **not decorative placeholders
— they're named to intentionally echo ArgoCD's own Sync Status and Health
Status signal**, populated once the GitOps writeback actually lands and
ArgoCD observes it. In this local seed environment that pipeline never
completes, so every record shows the pre-writeback state, which is exactly
wave 1's "seed-data explanation" — now confirmed, not just hypothesized.
That resolves the "is this a bug or a seed artifact" question. It does not
resolve the underlying UX problem wave 1 already named: Priyanka's forensic
session shows the UI still never states the practical conclusion ("this
override has not reached the cluster") in plain language anywhere — you get
three neutral-sounding fields and have to infer the stakes yourself. The fix
is unchanged from wave 1: propagate an explicit, loud, plain-language
sync/commit status to the summary screens, not just the one detail page four
clicks deep.

### 6. No batch/bulk promote, despite the batching machinery already existing for a different verb

Fiona (coordinating a 3-app, 2-domain "release train") and Marcus (a daily
power user) independently arrived at the identical finding from opposite
directions. Fiona went looking for a way to promote several apps together
and found that **Trigger Release** has real multi-select/batch machinery — a
checkbox tree, a resolved "Draft — N target(s)" step, a single Release ID
backed by a Temporal workflow — but it's wired to *cutting new builds*, not
to *promoting already-built artifacts between environments*. She completed
her release train as three fully independent Promote transactions with no
shared batch ID, hand-writing a cross-reference into each Reason field as
her own manual paper trail. Marcus separately named "multi-select checkboxes
on Deployments with a bulk promote" as the single biggest tax the tool
imposes on his day, for the same reason. **This is one of the more
actionable findings in either wave**: the UI pattern and the backend
grouping concept both already exist in the codebase (Trigger Release proves
it); it just needs to be pointed at the promote/environment-progression
workflow instead of only the build-cutting one.

### 7. Accessibility: primary nav has no visible focus indicator, and one core control is keyboard-unreachable

Ellis's keyboard-only session (no mouse clicks at all, verified via
`browser_press_key` + accessibility-tree snapshots + computed CSS) found:
the seven primary nav links all compute `outline: none` / `box-shadow: none`
on focus — confirmed both via computed style and a screenshot with
"Deployments" programmatically focused, which is pixel-identical to its
unfocused neighbors — while every other interactive element on the same
pages (close button, form fields, buttons) correctly shows a 2px focus ring,
meaning this is one missed CSS rule, not a systemic gap. Separately, the
"click a chart row for detail" affordance on the Deployments matrix is a
plain `<div tabindex="-1">` with no ARIA role — genuinely unreachable by
keyboard, not just poorly labeled. The promote/rollback/copy icon controls
(⬆ ↺ ⧉) are reachable but their accessible name is the bare glyph with no
app/env context. No `<label>` elements exist anywhere in the app; the
promote form's Version dropdown has no accessible name at all. On the
positive side, the actual promote-form happy path (tab to Version, tab to
Reason, type, tab to "Run dry run," Enter) worked end-to-end and returned a
correct result — the core workflow is keyboard-operable, it's the
supporting affordances (focus visibility, one unreachable control, missing
labels) that need work.

### 8. Mobile: the two primary "browse everything" screens don't work at phone width; single-entity pages do

Theo's session at a 390×844 viewport found the top nav renders fully
unrolled (no hamburger/collapse), eating ~230px before any content, with the
"App Registry" wordmark visually lost behind it. Every page carries a
phantom horizontal-scroll region (a fixed-width layout wider than the
viewport, present even on pages that look complete). Worse for the actual
task: the Apps Catalog and Deployments matrix — the two screens most people
would land on first — are six/multi-column tables that clip exactly the
columns (dev/stage/prod) a mobile user came for, forcing sideways scrolling.
By contrast, single-app/chart detail pages already use a stacked-card layout
for the same dev/stage/prod data that reads cleanly at this width — that
pattern exists in the codebase, it's just not applied to the catalog/matrix
views. The promote (⬆) and rollback (↺) icons, reachable only via the
matrix, are single unlabeled glyphs packed tightly together — a real
small-tap-target problem. The Promote form itself, once reached, rendered
and worked cleanly at this width, dry-run gate included.

### 9. No cross-app audit/activity log, no export, and chart-type entities have zero promotion history UI at all

Nadia (compliance export task) and Malik (support ticket) both independently
concluded there is no way to answer "who did X, when, and why" across the
whole system in one place — history only exists per-entity, one page at a
time, and "Drift & Audit" (the one page with "Audit" in its name) turns out
to be drift-summary plus adopted-artifact listing, not an activity log; it
even says the promotion "why" is "logged server-side; cross-reference server
logs" for anything not already shown. Nadia additionally found a real gap
distinct from wave 1's findings: **image-type entities have a real
Promotion History section (requester, timestamp, action, reason); chart-type
entities — 4 of 6 things actually live in prod in this dataset — have none
at all**, just a bare relative "promoted X minutes ago." Guessing a
`/apps/<chart>/history` URL surfaced a raw 502 with a leaked gRPC NotFound
message (see finding 10 below). No export/CSV/print view exists anywhere
either. Malik separately confirmed there's no distinct "approved by" field —
only "requested by" — matching wave 1's Aisha finding and adding a second
independent corroboration.

### 10. Raw backend errors leak to the browser as a recurring pattern, not a one-off

Wave 1 flagged a single Tailwind CSS MIME-type bug on `/charts/<name>`
routes. This wave shows it's one instance of a broader pattern: Vic's
edge-case probe found `GET /apps/<bad-slug>` returns an **HTTP 502** with a
raw gRPC error string (`rpc error: code = NotFound desc = not found`) —
wrong status code for a not-found condition, wrong audience for the message
— and `GET /promote` with missing/bad query params returns a **plaintext
HTTP 400** entirely outside the app shell, no nav, no way back except the
browser's own back button. Sunny hit the same bare 400 page independently.
Devon's on-call session hit the original Tailwind MIME bug live, mid-task,
and confirmed it genuinely breaks page layout, not just the console. All
three read as the same underlying issue: unhandled/under-handled error
paths render raw backend output instead of a styled in-app error state.

### 11. ArgoCD-adjacent vocabulary means something different here, and the visual weight doesn't distinguish them

Lena's explicit compare-and-contrast (heavy prior ArgoCD UI experience)
found three separate terms that collide with ArgoCD's own vocabulary while
meaning something materially different: "drift" here is a registry-internal
bookkeeping check (an override no longer matching a chart's *current* pin),
not ArgoCD's live-cluster-vs-Git drift — and it's rendered in the same
alarm-weight badge slot ArgoCD would use for its own Sync Status pill.
"Reconcile Runs" sounds like ArgoCD's continuous reconciliation loop; it's
actually a periodic CI identity-pipeline job, unrelated. The Promotion
Details page's "Current sync status" / "Current health status" fields are a
deliberate echo of ArgoCD's own signal (see finding 3b) but read, out of
context, as this tool's own opinion. Lena's overall verdict was that the
tool is genuinely additive alongside ArgoCD (answers questions ArgoCD
structurally can't — cross-environment promotion intent, provenance, a
declared-vs-promoted composition diff) but that a team running both needs to
actively teach new hires which "drift" they're looking at before it causes a
bad-day misread.

### 12. Smaller findings worth a look

- **No domain-level health rollup**, despite domain being a first-class
  filterable field elsewhere — Grace could get a confident environment-level
  headline in 15 seconds but had to eyeball app-name prefixes to guess which
  *domain* owned the one drift case, with no confidence she didn't miss a
  similar case in an untouched domain. (Grace)
- **No self-service path to onboard a new app/domain** — Owen found
  Environments is a genuine, well-documented, domain-agnostic admin screen,
  but apps/domains have no equivalent; the only mechanism (a CI job,
  `ReconcileApps` in `ci.yml`) is undocumented in-app and was only
  discoverable via a stray sentence on an unrelated page. (Owen)
- **No API/webhook/integration surface** — confirmed by design, not by
  omission: Ravi's network-trace investigation shows this is a server-rendered
  htmx app with zero XHR/JSON calls, so there's genuinely nothing to
  integrate against yet; the one real finding for automation builders is
  that promotion auth runs on Keycloak realm roles
  (`app-registry-promoter-<env>`), discoverable only via a column tooltip.
  (Ravi)
- **No keyboard shortcuts, command palette, or sortable columns** — Marcus
  confirmed via direct JS event dispatch that none of `?`, `Ctrl+K`, `/`,
  `j`/`k`, `g`+letter do anything anywhere in the app; URL-based
  deep-linking does work and is a real, if partial, substitute. (Marcus)
- **Internal ticket/enum references leaking into shipped copy is a
  recurring pattern, not a one-off** — wave 1 already found "screen 52"
  (Aisha, Riley); this wave independently found "FR-9" and "screen S2"
  (Morgan) and a raw `PromotionState.PENDING_APPROVAL` enum name (Owen) in
  different places. Worth a single copy pass across the whole app rather
  than three separate one-line fixes.
- **Visual consistency** (Morgan, design-critique persona) — the
  "App Registry" wordmark is near-invisible against the nav bar (near-black
  on near-black, confirmed via computed color); orange is reused for four
  unrelated meanings (a provenance badge, a dry-run button, a primary CTA, an
  empty-state banner); a missing-space typo in the dry-run result text; the
  Environments table overflows horizontally with no scroll affordance; and
  Trigger Release is the one page missing the shared gradient hero every
  other page opens with.
- **Reverse "which charts pin this app" lookup works but is scoped to one
  digest at a time**, not a full cross-version rollup for an app — confirmed
  working correctly (aggregates multiple chart versions sharing one digest)
  but requires manual per-digest checking for exhaustive analysis. (Baxter)
- **The word "ArgoCD" never appears anywhere in the app's own text**, despite
  being central to the mental model this tool assumes ("this records intent,
  ArgoCD does the deploying") — both Sunny and Casey finished full sessions
  without the app ever telling them that, and would have walked away
  believing App Registry itself deploys things had they not already known
  otherwise externally. (Sunny, Casey)

## What's working well

Wave 1 already named the promote form's mandatory dry run and the rollback
form's before/after comparison as the standout positives. Wave 2 reconfirms
both **almost unanimously** — Owen, Ravi, Vic, Ellis, Casey, Theo, Sunny,
Marcus, and Fiona all independently called out the dry-run gate specifically,
several using nearly identical language ("a real, considered piece of
defensive UX," "exactly the kind of safety rail"). Add one more, new this
wave:

- **The Environments page's self-disclosing copy about which fields are
  real vs. decorative** ("Requires approval — stored, not enforced, Promote
  does not check it," "Allowed principals — does not restrict who can
  promote, the promoter role check is the only access control in force")
  drew unprompted praise from six different personas this wave alone (Owen,
  Ravi, Casey, Sunny, Lena, Marcus) — likely the single most-loved specific
  design choice across all 30 personas in both waves combined. Multiple
  testers used nearly the same words: they trust the tool *more*, not less,
  for admitting what isn't wired up yet, and several called it out as a
  pattern worth applying more broadly (Lena: "I have never seen a UI so
  aggressively honest about which of its own fields are decorative").
- **Cross-linked chart/app/artifact/promotion pages hold up under real
  forensic pressure** — Priyanka's deep drift investigation and Baxter's
  blast-radius analysis both independently built a complete, self-consistent
  picture entirely by following in-app links, with facts cross-confirmed
  across three or more independent pages every time.

## Caveats on this data

Same caveats as wave 1 apply (one seeded local dataset, one point in time,
20 more simulated — not human — users), plus two specific to this round:

- **The single biggest finding of this round is about the exercise's own
  methodology, not the product** — see "A methodology finding that changes
  how to read wave 1's #1" above. Any future wave should either run
  single-agent (no concurrency) or have personas explicitly told to
  attribute stray navigation to session-sharing, the way this wave was.
- Several personas (Vic, Baxter, Priyanka, Marcus) had parts of their task
  genuinely disrupted by tab-hijacking badly enough that they couldn't
  complete every planned edge case (Vic completed roughly half his planned
  robustness probes) — treat their reports as thorough but not fully
  exhaustive for that reason.
