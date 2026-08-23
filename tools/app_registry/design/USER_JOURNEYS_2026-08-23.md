# App Registry UI — User Journeys (2026-08-23)

Point-in-time usability findings from 10 simulated user sessions against the
App Registry web UI (`app-registry-ui`), run locally under `tilt up`. This
is a snapshot, not a living doc — it reflects the UI as it existed on
2026-08-23, and will drift out of date as screens change. Read it as input
to prioritization, not as a current-state reference; see
[TOC.md](TOC.md) for docs that stay current.

**Don't re-run this exercise by hand to "check" a finding** — the
underlying seed data is disposable and regenerated per-session. Read
[USER_JOURNEYS_2026-08-23_TRANSCRIPTS.md](USER_JOURNEYS_2026-08-23_TRANSCRIPTS.md)
for the full per-persona narrative and evidence behind each finding below.

## Methodology

Ten personas, spanning non-technical (PM) through expert (staff SRE), each
given a distinct realistic task and a shared set of 10 interview questions
(navigability, task success, trust/jargon, "what would you change," "what
was cool," CLI-vs-UI preference). Each ran as an independent agent driving
a real Chromium browser via Playwright MCP against `http://localhost:8090`
— UI-only, no CLI/grpcurl/source access — against a local Tilt instance
seeded by
[`scripts/seed_tilt_walkthrough.py`](../scripts/seed_tilt_walkthrough.py)
(real domain names — manmanv2, leaflab, friendly-computing-machine,
app-registry itself — with two release generations per product so prod
trails stage/dev, one deliberate drift override, one rollback, and a mix of
ADOPTED/OBSERVED provenance). Personas ran with no shared context and no
knowledge of the seed script's contents, so their reactions are a genuine
first-encounter read, not a scripted walkthrough.

| # | Persona | Role / technical level | Task |
|---|---|---|---|
| 1 | Priya Raman | Staff SRE, expert, on-call | Paged "stage is showing drift, is prod safe?" — investigate |
| 2 | Marcus Webb | Backend engineer, technical, week 2 | Find prod's version of an app + who/when it got there |
| 3 | Dana Okafor | Eng manager, semi-technical | 10-second "what's behind in prod, fleet-wide" read for a meeting |
| 4 | Vik Chen | Staff engineer, expert, skeptical | Try to catch the UI's story not adding up |
| 5 | Jordan Alvarez | PM, non-technical | Confirm a claim ("bot fix shipped to dev, not prod") without an engineer |
| 6 | Sam Okonkwo | Release engineer, technical, hands-on | Actually promote stage→prod, then trial rollback |
| 7 | Aisha Bello | Security/compliance auditor, semi-technical | Audit who-promoted-what + ADOPTED vs OBSERVED provenance |
| 8 | Leo Ferreira | QA/support engineer, semi-technical | Support ticket: "what changed and when" |
| 9 | Riley Tran | Moderately technical, no assigned task | Click every nav item, first impressions |
| 10 | Chen Liu | SRE, expert, daily CLI user | Does the UI cover the CLI, or is it a shadow of it? |

Full reports: [USER_JOURNEYS_2026-08-23_TRANSCRIPTS.md](USER_JOURNEYS_2026-08-23_TRANSCRIPTS.md).

## Scorecard

| Persona | Navigability /5 | Usefulness /5 | UI over CLI? |
|---|---|---|---|
| Priya (SRE) | 4 | 5 | Yes |
| Marcus (new hire) | 3 | 4 | Yes, with caveat |
| Dana (EM) | 2 | 3 | Mixed — technical teammate might beat it via CLI |
| Vik (skeptic) | 4 | 2 | No — orient here, verify in ArgoCD |
| Jordan (PM) | 3 | 4 | Yes |
| Sam (release eng) | 3 | 3 | Yes, once nav-hijack is fixed |
| Aisha (auditor) | 4 | 4 | Yes |
| Leo (support) | 4 | 3 | Yes for versions, no for "did it actually ship" |
| Riley (tourist) | 4 | 4 | Yes |
| Chen (CLI power user) | 4 | 3 | Yes for status checks, no for Trigger Release/Promote until stable |
| **Average** | **3.5** | **3.5** | **4 yes / 4 conditional / 2 no** |

Read the averages loosely — this is 10 opinions, not a statistically
powered survey. The pattern underneath the numbers matters more than the
numbers: the UI's *information architecture and copywriting* score well
across the board; the two things dragging every borderline score down are
one reliability bug and one data-consistency bug, both cross-cutting (next
section).

## Cross-cutting findings

Ranked by how many independent personas hit the same thing, since that's a
much stronger signal than any one report.

### 1. Navigation gets hijacked mid-task (7/10 personas hit some form of this)

Marcus, Dana, Vik, Jordan, Sam, Leo, Aisha, and Chen — eight, actually —
each independently reported the browser landing on an unrelated page
without clicking there: a nav link to `/environments` opening a random
app/chart detail page instead (Dana, twice), a direct link to
`/drift-audit` silently loading `/deployments` (Aisha), a promote/rollback
icon click on one app landing on a *different* app's promotion detail page
mid-form-fill (Sam, three separate times, once mid-keystroke), and stale
element references generally (Chen, Marcus).

This is the single most damaging finding in the batch, because it lands
hardest exactly where it's most dangerous: Sam (release engineer, actively
promoting to prod) and Chen (triggering a release) both hit it while
performing a real write action, not while idly browsing. A tool whose core
job is unambiguous promote/rollback cannot also risk swapping the
in-progress form out from under the user. Some reports guessed at a
mechanism (a live-activity feed force-navigating the current view, or
dev-server hot-reload noise) but none confirmed one — **this needs an
engineering investigation, not a UI copy fix**, and should be the top
priority coming out of this exercise. See Priya's, Marcus's, Sam's, and
Chen's transcripts for the clearest reproductions.

### 2. Apps Catalog disagrees with the app's own detail page (4/10 personas)

Marcus, Dana, Vik, and Jordan each found that the Apps Catalog
(`/apps`) shows an app's dev/prod cell as **"not promoted"** for an app
that's actually live there via its parent chart — while that same app's
own detail page correctly resolves and shows the chart-inherited version.
Dana's report captures the stakes best: her gut read of a catalog full of
"not promoted" cells was "nothing is deployed anywhere," which she
almost said out loud in a leadership meeting before catching it. This
reads as a real bug in the catalog's per-cell "promoted" computation (it
appears to only count direct/explicit promotions of that exact deploy
unit, not versions inherited through a composing chart) rather than a
labeling issue — the two screens are stating contradictory facts about the
same app/environment pair.

### 3. "Adopted"/"live" badges vs. "Pending, not yet committed" promotion status (4/10 personas)

Priya, Vik, Aisha, and Leo each drilled into a promotion's own detail page
and found it marked **Pending — Not yet committed — Sync triggered at: Not
yet triggered — No ArgoCD sync/health observations recorded** for a
version that every summary screen (Dashboard, Deployments matrix, Apps
Catalog, app detail) confidently badges as the current, live "Adopted"
state. Vik: *"If this was never committed to the GitOps repo, ArgoCD never
saw it — the tool may be advertising a drift that never happened."*

Unlike finding #1, this one has a plausible **seed-data explanation**
worth ruling out before treating it as a product bug: the local Tilt
`app-registry-worker` drains the writeback outbox and calls a real git
provider, and a promotion made against a demo/local registry may
legitimately never complete that write in this environment. If that's the
whole story, the finding is really "the UI doesn't visibly distinguish a
promotion whose writeback hasn't landed yet from one that has, anywhere
except the one detail page four clicks deep" — which is still worth
fixing (surface commit/sync status on the summary screens, not just the
per-promotion detail page), just less alarming than "the badges lie."
**Needs one round of engineering triage to separate the seed-environment
explanation from a real gap**, but either way, the fix is the same: propagate
sync/commit status upward.

### 4. Promote/rollback history direction is backwards (Leo, 1/10 — flag for verification)

Leo reconstructing `manmanv2-host-manager`'s dev timeline found a promotion
history entry **labeled "promote"** showing `v1.1.0 → v1.0.0` (a
downgrade) and one **labeled "rollback"** showing `v1.0.0 → v1.1.0` (an
upgrade) — the opposite of what those words mean in plain English. Only
one persona hit this (it requires drilling into individual promotion
detail pages rather than just the summary table), but it's specific enough
and important enough — a support engineer reconstructing an incident
timeline would report the wrong direction of change to a customer — that
it deserves an engineering look even at n=1. It may simply be a case of
`rollback` doing exactly what it's documented to do (re-promote whichever
version was previously current, which can be numerically higher *or*
lower) with a label that reads as "always go down" to anyone who hasn't
read the CLI docs — worth a plain before→after version display instead of
relying on the promote/rollback verb alone.

### 5. Smaller, single-report findings worth a look

- **Build detail 502** (Leo) — a build's linked build-detail page returned
  a 502 Bad Gateway. Could be a real bug or a local-Tilt-only artifact of
  the synthetic `adopted:<uuid>` build rows the seed script creates for
  every adopted artifact; worth a quick repro check either way.
- **Tailwind CSS console error on nested routes** (Vik, Riley) —
  `Refused to apply style ... MIME type` on `/charts/<name>` pages; Riley's
  diagnosis (a relative stylesheet link resolving to `/charts/tailwindcss`
  instead of `/tailwindcss`) looks right and is a one-line path fix.
- **"Trigger Release" is the one screen with no orienting sentence**
  (Riley) — every other screen opens with a plain-English scope statement;
  this is the one screen most likely to be confused with "promote," and
  it's the one missing that sentence.
- **"screen 52" internal ticket reference in shipped copy** (Aisha, Riley)
  — Drift & Audit's footer leaks internal design-doc numbering into
  user-facing text.
- **No approver/reviewer field on promotions** (Aisha) — every promotion
  in this dataset is self-service (requester = approver), which is a
  legitimate audit gap regardless of seed data, if that's also true of the
  real schema.
- **Builds screen has no filtering** (Chen) — a flat, unfilterable ~35-row
  table with only an exact-run-id lookup; fine for a sanity check, not
  enough for "find one build in a specific domain."

## What's working well

Worth stating explicitly, since it's easy for a findings doc to read as
all-negative: **every persona's information architecture expectations were
met on the first try** (Dashboard → Deployments/Apps → detail pages is a
guessable, consistent structure), and a specific design choice showed up,
unprompted, as the standout positive in *multiple independent reports*:

- **The promote form's mandatory dry run.** Priya, Riley, and Sam all
  called this out unprompted as the single most trust-building interaction
  in the app — type a reason, run a dry run, get an instant, specific,
  plain-English verdict ("this would be a no-op") before anything real
  happens. Riley: *"that's the kind of safety rail that makes me willing
  to eventually click 'Promote for real' on a tool I'd never used
  before."*
- **The rollback form's before/after comparison** ("Currently live: vX"
  vs. "Will roll back to: vY," with a confirm button that names the target
  version in its own label) — Sam: *"the button itself is the
  confirmation."*
- **Honest, plain-language scoping disclaimers, everywhere.** The Drift
  page's "Not a cluster check — the registry only knows about its own
  promotion + chart-pin records," the Dashboard's "read-only by design,"
  the Reconcile Runs page explaining why a stale-rejected sweep correctly
  writes nothing, and the Builds page's "Indeterminate vs. Healthy"
  recording-health distinction were each singled out by a different
  persona as unusually trustworthy copywriting for an internal tool.
- **The artifact page's Provenance line** ("Adopted — recorded after the
  fact by an admin, not observed by CI") — called out by both Marcus and
  Aisha as exactly the right level of bluntness for, respectively, a
  new-hire not wanting to over-trust a build trail and an auditor needing
  a drop-in finding sentence.

## Caveats on this data

- This is one seeded local dataset, one point in time, and 10 simulated
  (not human) users — treat it as a strong set of hypotheses to verify,
  not a final verdict.
- Several findings (in particular #3 and the build 502) may be artifacts
  of the local Tilt writeback path lacking real git/CI credentials rather
  than product bugs — see each finding's note above.
- No persona tested the OIDC-authenticated / role-gated path (local Tilt
  runs `AUTH_MODE=none`); every persona was implicitly "developer" with
  every role. FR-13/FR-14 role-gating UX (see
  [ui/ARCHITECTURE.md](../ui/ARCHITECTURE.md)) is entirely unvalidated by
  this exercise.
