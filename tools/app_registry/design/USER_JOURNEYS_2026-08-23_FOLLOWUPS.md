# App Registry UI — Follow-up Stories (from the 2026-08-23 Playtests)

A prioritized backlog derived from all 30 simulated user sessions in
[USER_JOURNEYS_2026-08-23.md](USER_JOURNEYS_2026-08-23.md) (wave 1, 10
personas) and [USER_JOURNEYS_2026-08-23_WAVE2.md](USER_JOURNEYS_2026-08-23_WAVE2.md)
(wave 2, 20 personas). Those two files are findings — this one turns the
findings into stories someone can actually pick up, ranked by corroboration
strength and how clear the fix already is. Written the same day as the
exercise; treat it as a starting backlog to triage into [PLAN.md](PLAN.md),
not a committed roadmap.

Each story cites the finding(s) it comes from by number (wave 1's `#N`, wave
2's `#Nb`/new numbers) so anyone picking this up can go read the actual
transcripts before touching code — these are UI reports from simulated users,
not verified bug repros.

## P0 — verify before anything else

**0. Confirm whether wave 1's "navigation gets hijacked mid-task" (finding
#1) is a real bug or entirely a test-harness artifact.**
Wave 2's biggest discovery was methodological, not product: running personas
concurrently means they share one real browser context and steal each
other's tabs, and wave 1 ran under identical concurrency without ever being
warned to consider that. Once warned, 16/20 wave-2 personas independently
attributed the same symptom to session-sharing. That downgrades wave 1's #1
from "top-priority engineering investigation" to "unconfirmed, likely a
harness artifact" — **but doesn't clear it entirely**: Sam's wave-1
reproduction (a mid-keystroke redirect while alone on one promote form) is
harder to explain via tab-sharing than the other seven. Before spending any
engineering time chasing a navigation bug, run a **single-agent, no
concurrency** re-check of Sam's exact repro (promote form, `manmanv2`,
mid-keystroke). If it doesn't reproduce solo, retire finding #1 outright and
downgrade every P1/P2 item below that assumed it was real (there are none —
nothing below currently depends on #1). If it does reproduce solo, this
becomes the actual P0 engineering item.

## P1 — high corroboration, clear fix

**1. Fix the Apps Catalog's per-cell "not promoted" computation for
chart-inherited apps.** *(wave 1 #2, wave 2 #2b — 6/30 personas: Marcus,
Dana, Vik, Jordan, Taylor, Grace)*
The catalog (`/apps`) shows an app as "not promoted" in an environment where
it's actually live via its parent chart, while that same app's own detail
page correctly resolves the chart-inherited version. Two personas nearly
reported wrong information upward (Dana to her manager, Taylor to a leader)
because of it. This reads as the catalog's cell logic only counting
direct/explicit promotions rather than chart-inherited ones — fix the
computation, not the copy.

**2. Show a plain before→after version display next to every
promote/rollback label.** *(wave 1 #4, wave 2 #4b — 3/30: Leo, Malik,
Ingrid)*
`rollback` correctly restores whichever version was previously current
(confirmed by Lena, wave 2 #11) — but that can be numerically *higher* than
the current version, which reads as backwards to anyone going by the verb
alone. Three independent personas misread the same real seeded event on the
same day. Add "vX → vY" next to the promote/rollback label everywhere a
promotion history entry is shown, so the direction is never inferred from
the verb.

**3. Surface sync/writeback status loudly, in plain language, on the summary
screens — not just on the one promotion-detail page.** *(wave 1 #3, wave 2
#3b — 6/30: Priya, Vik, Aisha, Leo, Nadia, Ingrid, Priyanka, Malik, Lena)*
Wave 2 resolved the open question wave 1 left hanging: "Current sync
status"/"Current health status" are a deliberate, intentional echo of
ArgoCD's own signal, populated only once GitOps writeback actually lands —
not decorative placeholders, and not a bug. This is real, working
architecture. What's still missing is the UX: every summary screen
(Dashboard, Deployments matrix, Apps Catalog, app detail) confidently badges
a promotion as live "Adopted" state with no visible signal that its
writeback hasn't landed yet — you have to drill four clicks deep to a
detail page and read three neutral-sounding fields to learn that. Add a
loud, plain-language state ("not yet reached the cluster") to the summary
screens themselves, reusing the sync/health fields that already exist.

**4. Replace raw backend error output with a styled in-app error state, and
fix the status codes.** *(wave 1 "Smaller findings," wave 2 #10 — Leo, Vik,
Riley, Sunny, Devon)*
This is one pattern hit four separate ways: a Tailwind CSS MIME-type error
breaking page layout on `/charts/<name>` routes (wave 1's Riley diagnosed the
likely one-line path-relative-link fix; Devon hit the same bug live,
mid-task, in wave 2), a bad app slug returning a raw `502` with a leaked gRPC
`NotFound` error string instead of a `404`, and a bad `/promote` query
returning a bare unstyled `400` with no nav or way back except the browser's
own back button. Wrap unhandled/under-handled routes in the app's normal
error shell, and correct the status codes (`404` for not-found, not `502`)
so the errors are debuggable from the outside without leaking internal error
strings.

## P2 — real gaps, clear direction, larger lift

**5. Point the existing batch/multi-select machinery at promote, not just
build-cutting.** *(wave 2 #6 — Fiona, Marcus)*
Trigger Release already has real batch UI (a checkbox tree, a resolved
"Draft — N target(s)" step, one Release ID backed by a Temporal workflow) —
it's just wired to cutting builds. Fiona had to coordinate a 3-app,
2-domain release train as three fully independent Promote transactions with
a hand-written cross-reference in each Reason field as her own manual paper
trail; Marcus separately named bulk promote as the single biggest tax the
tool imposes on his day. The pattern and the backend grouping concept both
already exist — extend them to the promote/environment-progression workflow
instead of building a second batching mechanism from scratch.

**6. Accessibility pass: focus indicators, one unreachable control, form
labels.** *(wave 2 #7 — Ellis)*
Verified via keyboard-only navigation, computed CSS, and accessibility-tree
snapshots, not a guess: all seven primary nav links compute `outline: none`
on focus (every other interactive element on the same pages correctly shows
a focus ring — this is one missed CSS rule, not systemic); the "click a
chart row for detail" affordance on the Deployments matrix is a
`<div tabindex="-1">` with no ARIA role, genuinely unreachable by keyboard;
no `<label>` elements exist anywhere in the app (the promote form's Version
dropdown has no accessible name at all); the promote/rollback/copy icon
controls have no app/env context in their accessible name. The core
promote-form happy path is already keyboard-operable end-to-end — this is
about the supporting affordances, not the workflow itself.

**7. Apply the existing stacked-card mobile layout to the Apps Catalog and
Deployments matrix.** *(wave 2 #8 — Theo)*
At a 390px viewport, the top nav eats ~230px uncollapsed, every page carries
a phantom horizontal-scroll region, and the two screens most people land on
first — Apps Catalog and the Deployments matrix — are wide tables that clip
exactly the dev/stage/prod columns a mobile user came for. Single-app/chart
detail pages already solve this with a stacked-card layout for the same
data at the same width; it's just not applied to the catalog/matrix views.
Also worth a look while in there: the promote/rollback icons are small,
unlabeled, and packed tightly together — a real tap-target problem on
mobile specifically.

**8. Give chart-type entities the same promotion-history UI image-type
entities already have, and add a cross-app audit export.** *(wave 2 #9 —
Nadia, Malik)*
Image-type entities have a real Promotion History section (requester,
timestamp, action, reason). Chart-type entities — 4 of 6 things actually
live in prod in this seed dataset — have none at all, just a bare relative
"promoted X minutes ago," and a guessed `/history` URL 502s. Separately,
there is no cross-app activity log or export anywhere: "Drift & Audit" (the
one page with "Audit" in its name) is drift-summary plus an adopted-artifact
list, not an activity log, and its own copy says the promotion "why" is
"logged server-side; cross-reference server logs" for anything not already
shown on a per-entity page. A compliance auditor (Nadia) and a support
engineer (Malik) both independently hit this trying to answer "who did X,
when, and why" across the system. Two related but separable pieces of work:
parity for chart-type history, and a genuine cross-entity export/log view.

## P3 — smaller, worth batching into a copy/polish pass

**9. Remove internal ticket/enum leakage from shipped copy.** *(wave 1 —
Aisha, Riley; wave 2 #12 — Morgan, Owen)*
"screen 52" (wave 1), "FR-9" and "screen S2" (Morgan), and a raw
`PromotionState.PENDING_APPROVAL` enum name (Owen) have all leaked into
user-facing text independently, in different places. One copy pass across
the app, not three one-line fixes.

**10. Visual consistency pass.** *(wave 2 #12 — Morgan)*
From a dedicated design-critique session: the "App Registry" wordmark is
near-invisible against the nav bar (near-black on near-black, confirmed via
computed color); orange is overloaded across four unrelated meanings (a
provenance badge, the dry-run button, a primary CTA, an empty-state
banner); a missing-space typo in the dry-run result text; the Environments
table overflows horizontally with no scroll affordance; Trigger Release is
the one page missing the shared gradient hero every other page opens with.

**11. Disambiguate ArgoCD-adjacent vocabulary.** *(wave 2 #11 — Lena)*
"Drift" here is a registry-internal bookkeeping check (an override no
longer matching a chart's *current* pin), not ArgoCD's live-cluster-vs-Git
drift, but renders in the same alarm-weight badge slot ArgoCD uses for its
own Sync Status pill. "Reconcile Runs" sounds like ArgoCD's continuous
reconciliation loop; it's actually a periodic CI identity-pipeline job,
unrelated. Lena's overall verdict is that the tool is genuinely additive
alongside ArgoCD, not redundant with it — but a team running both needs to
actively learn which "drift" they're looking at before it causes a bad-day
misread. Worth a plain-language callout wherever these terms first appear,
distinguishing them from their ArgoCD namesakes.

**12. Say "ArgoCD" somewhere in the app's own text.** *(wave 2 #12 — Sunny,
Casey)*
Two personas (one with no assigned task, one doing a self-guided onboarding
tour) each finished a full session without the app ever stating that ArgoCD
is what actually deploys things — both would have walked away believing App
Registry itself deploys, had they not already known otherwise externally.
This is a one- or two-sentence fix, probably on the Dashboard next to its
existing "read-only by design" disclaimer.

**13. Add a domain-level health rollup.** *(wave 2 #12 — Grace)*
Domain is already a first-class filterable field elsewhere in the app, but
there's no domain-level summary anywhere — a 15-minute leadership-update
session could get a confident environment-level headline instantly but had
to eyeball app-name prefixes to guess which domain owned the one drift
case, with no confidence of not missing a similar case in an untouched
domain.

**14. Document (or build a UI path for) onboarding a new app/domain.**
*(wave 2 #12 — Owen)*
The Environments page is a genuine, well-documented, domain-agnostic
self-service admin screen. Apps/domains have no equivalent — the only real
mechanism is a CI job (`ReconcileApps` in `ci.yml`), undocumented in-app,
discoverable only via a stray sentence on an unrelated page. Lowest lift:
document the CI-only path in-app, same honest-disclosure spirit as the
Environments page's "stored, not enforced" footnotes. Higher lift: build a
self-service equivalent.

**15. Add a distinct approver/reviewer field, separate from requester.**
*(wave 1 — Aisha; wave 2 #9 — Malik)*
Every promotion in this dataset is self-service (requester = approver).
Confirmed independently by an auditor persona in each wave. A real audit
gap if it's also true of the production schema, not just the seed data —
worth a quick check against the actual promotion model before scoping this
as a story.

## Explicitly not a story: API/webhook surface

Wave 2's Ravi investigated this directly (network-trace inspection, not a
guess): the UI is a server-rendered htmx app with zero XHR/JSON calls, by
design, not by omission. There's genuinely nothing to integrate against
today. If automation demand shows up for real, the actual finding to build
from is that promotion auth already runs on Keycloak realm roles
(`app-registry-promoter-<env>`) — that's the natural seam a future gRPC/API
surface would sit behind. Not worth a story until there's a concrete
automation use case driving it.

## Keep doing this

Two design choices were praised, unprompted, by a large fraction of all 30
personas, and are worth treating as the house style to extend rather than
one-off wins:

- **The promote form's mandatory dry-run gate** (near-unanimous across both
  waves — over half of all 30 personas called it out specifically, several
  independently landing on nearly identical language like "a real,
  considered piece of defensive UX"). It re-arms the instant any field is
  touched, so "Promote for real" can never fire against a form state it
  didn't just validate.
- **The Environments page's self-disclosing copy about which fields are
  real vs. decorative** ("stored, not enforced," "does not restrict who can
  promote") — the single most-praised specific design choice in either
  wave (6 personas in wave 2 alone), and directly actionable: apply the
  same pattern to #3 above (writeback/sync status) and #14 (onboarding)
  instead of inventing new UI language for either.

## Caveats

Same as both source docs: one seeded local dataset, one point in time, 30
simulated (not human) users. Treat every item above as a well-evidenced
hypothesis to triage, not a pre-approved spec — in particular, P0 should
run before any engineering time is spent on the assumption that finding #1
was real.
