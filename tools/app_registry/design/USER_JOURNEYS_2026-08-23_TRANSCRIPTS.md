# App Registry UI — User Journey Transcripts (2026-08-23)

Full per-persona narratives and interview answers behind
[USER_JOURNEYS_2026-08-23.md](USER_JOURNEYS_2026-08-23.md)'s synthesis. Each section
below is one independent simulated session (Playwright MCP driving a real browser
against `http://localhost:8090`, UI-only, no CLI/source access), written by the
persona in character.

---

# Playtest Report: App Registry

**Persona:** Priya Raman, Staff SRE, 8 years experience, on-call this week
**Task:** Paged with "stage is showing drift, is prod safe?" — investigate and decide next steps.

## Narrative

I hit `localhost:8090` cold, no login prompt (dev bypass, noted and moved on). Landing page is a Dashboard, and it immediately did the one thing I actually wanted: a red banner reading "1 drifted override(s) — an overridden image no longer matches its chart's pin," plus per-environment cards showing `dev: Healthy`, `stage: Drift`, `prod: Healthy`. That's a 5-second triage right there — good.

I clicked through to Deployments (the matrix). It confirmed the same single row flagged `drift`: `manmanv2-control-services`, stage column only. Dev and prod on that row showed no drift badge. So far, so good — but the matrix alone doesn't say *what* is drifted, just that the row/env combo is.

I went to the chart detail page (`/charts/manmanv2-control-services`) and found the real sentence: "manmanv2-control-api overridden — drift" under stage. So the chart itself is fine; one *app inside* the chart bundle has a manual override that's stale.

Drift & Audit gave me the punchline in a proper table: stage / `manmanv2-control-api` — promoted (override) is `v0.2.19 @ sha256:140f...`, chart currently pins `sha256:a022f8...`. One row, no ambiguity.

Then I went to the app's own page (`/apps/manmanv2-control-api`) and got the full picture: dev is `v0.2.20 via chart` (no override, matches), stage is `v0.2.19, Override · Drift` with an explicit "Chart pin says sha256:a022f8... (mismatch)" note, and **prod is `v0.2.19 via chart`** — no override at all, just whatever the prod chart pin says. Prod was never touched by this override; it's not "safe by luck," it's structurally not exposed to this problem.

Clicking into the promotion's own detail page was the real find: this override's `Requested by: developer (you)`, `Reason: "seed_tilt_walkthrough.py: populate local registry for a UI walkthrough"`, status `Pending`, `Committed: Not yet committed`, `Sync triggered at: Not yet triggered`. In other words — in this seeded demo — the "drift" isn't even a real write that reached GitOps/ArgoCD. In a real incident this is exactly the field I'd need to know whether to panic.

One navigation snag: URLs occasionally didn't match where I asked to go (I typed `/environments` and `/reconcile-runs` and landed on other detail pages instead) — I initially suspected a routing bug, then attributed it to the browser session being reused across parallel test runs, not the app itself. Worth flagging as ambiguous, not a confirmed app bug.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a status board — "what's live where" — and I expected to start wherever incidents start: a health/overview page or an explicit Drift page, since that's literally the alert I got paged on. I did not expect a "Trigger Release" nav item to be adjacent to read-only investigation tools — that's a "don't touch during an incident until you know what you're looking at" landmine sitting one click away.

**2. Walk through what you actually did, step by step — where did you get stuck?**
Dashboard → Deployments matrix → chart detail page → Drift & Audit → app detail page → promotion detail page. That's five hops to get from "stage has drift" to "here's the exact override, here's why, and here's proof it never synced." The only real friction was navigation reliability (see narrative) — clicking a nav link didn't always land where I expected, though I believe that's a shared-session artifact from parallel testing rather than the app itself.

**3. Did you get the actual answer you came for?**
Yes, fully. Prod is unaffected — it's not running an override at all, just the chart's own pin, and it's on a different (older, stable) chart version than stage. Stage's drift is one specific app (`manmanv2-control-api`) whose manually-recorded override predates the chart's latest pin, and — bonus — that override was never actually committed/synced, so I'd treat it as registry bookkeeping noise until proven otherwise, not a live-cluster emergency.

**4. Rate navigability 1-5.**
4/5. Every screen I needed exists and the breadcrumbs are decent, but getting the *full* story required visiting five different pages that all show fragments of the same fact (chart page, matrix, drift-audit, app page, promotion page). A single "click the drift badge → see everything" flow would save real minutes at 3am.

**5. Rate usefulness/task-success 1-5.**
5/5. Every piece of data I needed to answer "is prod safe" was actually in the tool, including the underrated but crucial writeback/sync status on the Promotion Details page. That field alone is what separates "is this real" from "is this a phantom record."

**6. Any moment you didn't trust the screen, or jargon confused you?**
"Adopted" as a badge threw me for a second — I initially read it as "this version has been rolled out / is live," but it actually means "a human typed this in via AdoptArtifact, CI didn't observe it." That's an important trust signal (human-asserted vs. CI-verified) buried in a word that sounds like the opposite of suspicious. I'd rename it something like "Manually recorded" or add a tooltip, because during an incident "Adopted" reads as reassuring when it should read as "verify this."

**7. What's one thing you'd change immediately if you owned this UI?**
Put the drifted-override's sync/writeback status (committed? synced? Pending?) directly on the Deployments matrix cell or at least the Drift & Audit row, not three clicks deeper on a per-promotion detail page. That's the single fact that tells you whether to escalate or shrug, and right now it's the hardest one to find.

**8. Something genuinely cool or delightful?**
The chart detail page's "Currently declared composition" vs "Apps published in vX.Y.Z" split is smart — it visibly separates "what the manifest says today" from "what a specific already-promoted version actually pinned," which is exactly the kind of distinction that prevents wrong assumptions during a chart bump. And the drift table's phrasing — "Not a cluster check — the registry only knows about its own promotion + chart-pin records" — is the single most honest, useful disclaimer I've seen in an internal tool. It tells me exactly how much to trust the page.

**9. Anything you expected to exist that wasn't there?**
A direct stage-vs-prod diff view (pick two envs, see every app's version delta in one table) — the task briefing called it "Environment Diff" but what I found under "Environments" is actually environment *config* (rank, GitOps path, promoter role), not a diff. The Deployments matrix functions as a diff if you squint across three columns, but a dedicated "compare stage → prod" view would have made question 1 ("what's actually different") a 10-second glance instead of a table scan.

**10. Would you recommend a teammate use this UI, or CLI/grpcurl?**
UI, without hesitation — the drill-down from dashboard alert to root-cause override to sync status is faster here than I could get from grpcurl-ing raw promotion records, and the plain-English mismatch statements ("Chart pin says sha256:... (mismatch)") save real time over reading digests by eye.

---

# Playtest Report: App Registry

**Persona:** Marcus Webb, Backend Software Engineer, Week 2
**Task:** Find the current prod version of `manmanv2-control-api`, and determine whether I could trace who put it there and when.

---

## Narrative

I hadn't used this tool before, so I started at the root URL (`localhost:8090`), which landed on a "Dashboard." It had a "Recent promotions" table and env health tiles (dev/stage/prod), which was a reasonable orientation — I could immediately tell this is a promotion-tracking system, not a deploy system. The dashboard even had a line at the bottom: "Dashboard is read-only by design — every action here links out to the Deployments screen that owns it," which was a nice, unprompted clarification of scope.

I went to **Apps** in the nav, since the task said to try the catalog. I searched visually (no need to filter) and found `manmanv2-control-api`. The catalog table showed columns for dev/stage/prod, and it displayed: dev = "not promoted", stage = "v0.2.19 Adopted", prod = "not promoted". That immediately worried me — my tech lead's question presupposes there IS a prod version. Did I have the wrong app?

I clicked into the app's detail page and got a very different picture: prod actually shows **v0.2.19**, labeled "Via chart" (not "not promoted"). So the catalog's prod cell was flat-out wrong, or at least used a different definition of "promoted" than I expected — it turns out the catalog only counts direct/explicit promotions of that exact deploy unit, while the app detail page also resolves the version inherited from its parent chart's pin. Nothing on the catalog page told me that distinction existed; I only found out by cross-referencing the two pages.

From the app detail page I clicked "view artifact" for the prod row and landed on the artifact detail page. This is where I found the actual answer to the "who/when" half of the question: **Provenance: "Adopted — recorded after the fact by an admin, not observed by CI."** The "Built from" field pointed to a build labeled `#adopted:ae842a82-...` rather than a real CI build ID. In other words, there is no real build/author trail for this specific artifact — it was manually backfilled into the registry, not tracked live.

I also opened the one "Promotion history" entry visible on the app page — it was a single seed-script event ("seed_tilt_walkthrough.py: populate local registry for a UI walkthrough") dated today, clearly demo/seed data rather than real history, so I couldn't tell from it who actually promoted `v0.2.19` to prod in a "real" sense.

I additionally poked at the chart page (`manmanv2-control-services`) since the app is delivered "via chart," and at the Environments page, which had refreshingly honest inline caveats (e.g., "Requires approval... stored, not enforced — Promote does not check it").

One recurring headache: several times, after clicking a link, a follow-up snapshot/screenshot showed I'd been bounced to an unrelated page (once to `/environments`, once to `about:blank`) with no action from me. Re-navigating fixed it. I can't tell if that's a real app bug (e.g., some background polling/redirect) or a client-rendering hiccup, but it happened enough times that it dented my confidence in "the URL bar tells me where I really am."

---

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected something like a "current state" board — one screen per app, or one row per app showing what's live in each environment, plus a history/audit trail, similar to a deploy dashboard or a lightweight CMDB. I expected to start by searching for the app by name, probably from a top-level "Apps" or "Services" list, which is exactly where "Apps" in the nav took me. That part matched my mental model fine.

**2. Walk through what you actually did, step by step — where (if anywhere) did you get stuck, confused, or have to backtrack?**
Dashboard → Apps catalog → searched for manmanv2-control-api → got confused because the catalog said prod was "not promoted" → clicked into the app detail page → saw prod actually = v0.2.19 ("Via chart") → clicked "view artifact" on the prod row → got the provenance answer ("Adopted... not observed by CI") → checked the one promotion-history entry (seed data) → poked at the parent chart page and Environments page for more context. The backtrack was entirely caused by the catalog-vs-detail-page mismatch described above — I had to re-verify my facts on a second page before I trusted the first one.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Half yes, half "and that's itself the answer." I got a clean, confident answer to "what version is in prod" — v0.2.19. For "who put it there and when," the honest answer I'd give my tech lead is: "I can't, not really — this specific artifact is marked Adopted, meaning someone hand-entered it after the fact, and there's no real build or promotion author behind it, just today's seed script." That's a legitimate finding, but it took real digging (catalog → app page → artifact page) to surface it, and nothing surfaced it proactively — I had to know to click "view artifact" and then read a small provenance line.

**4. Rate navigability 1-5.**
**3/5.** The nav bar itself is simple and the URLs are predictable (`/apps/:name`, `/artifacts/:digest`), which I liked. But the catalog table and the app detail page disagree about "promoted" state for the same app/env, with no visible legend explaining why, and a couple of times the page seemed to silently navigate somewhere I didn't click. That's real friction for someone who doesn't already know the tool's internal model.

**5. Rate usefulness/task-success 1-5.**
**4/5.** Once I found the artifact page, the "Provenance: Adopted, not observed by CI" line was exactly the kind of blunt, useful signal I needed — it directly answers "could you find who/when," and the answer is a defensible "no, and here's proof why not." That's genuinely useful for a real incident. I'm docking a point because getting there required knowing to cross-reference three separate pages rather than the catalog page just being right the first time.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes — two moments. First, "not promoted" in the Apps Catalog for prod, when the app detail page for the same app/env said v0.2.19 was live. Those can't both be true under a plain-English reading of "not promoted," and there's no tooltip explaining the catalog only tracks direct overrides vs. chart-inherited versions. Second, "Adopted" as a status badge appears in like five different places (catalog cells, app detail badges, artifact provenance) and means slightly different things each time (a promotion event vs. an artifact's origin) — I had to infer the distinction rather than being told it.

**7. What's one thing you'd change immediately if you owned this UI?**
Make the Apps Catalog's per-env cell show the *effective* running version always (resolving through the chart, like the app detail page does), with a small icon distinguishing "explicit override" from "inherited from chart pin" — instead of collapsing chart-inherited versions down to a flatly wrong-looking "not promoted."

**8. What's something you thought was genuinely cool, clever, or delightful — even a small detail?**
The artifact page's blunt "Adopted — recorded after the fact by an admin, not observed by CI" line. Most internal tools would just show a build ID and let you assume it's real; this one is upfront that the provenance trail is fake/backfilled, which is exactly the kind of honesty that saves you from a false sense of confidence during an incident.

**9. Anything you expected to exist that wasn't there?**
A real "who promoted this to prod and when" audit entry tied to the actual chart promotion (not just the one image override event from the seed script). Also, on the artifact page, I expected a link to the actual CI run / commit / PR that produced the image — there was a "Built from" field, but for this artifact it just pointed at another "adopted" placeholder, a dead end.

**10. One sentence: would you recommend a teammate use this UI, or would they be better off with the CLI/grpcurl?**
For "what's running where" it's genuinely faster than any CLI would be — I'd point a teammate here first, just with a heads-up to double check the Apps Catalog against the app's own detail page before trusting a "not promoted" cell.

---

# Playtest Report — Dana Okafor, Engineering Manager

**Task:** Get a fast, confident "is anything behind in production right now, across the whole fleet" read for a leadership sync in 10 minutes. No fixing, just a status read.

## Narrative

I opened `localhost:8090` expecting a dashboard — a landing page that would just tell me, in plain English or a big colored number, whether prod was healthy. Instead it dropped me on "Apps Catalog," a giant table of every app/chart by domain with dev/stage/prod columns. Most rows said "not promoted" in all three columns, which I initially misread as "broken everywhere" until I realized it just means those are sub-images that ride along with a parent chart, not things promoted individually. That's a full minute of confusion before I figured out I should be looking at the *chart* rows, not the *app* rows, to get real version numbers.

I went to "Deployments" next since the nav suggested it, and that page was actually much better for my purposes — one row per promotable thing, one column per env, with real version numbers and a red "Drift detected" banner up top. By eyeballing it I could tell: app-registry, friendly-computing-machine-bot-services, leaflab, manmanv2-control-services, and manmanv2-host-manager are all sitting one version behind stage/dev in prod. That's a real, usable answer — but I had to build it myself by reading ~10 rows of tiny SHA-truncated text and comparing version strings across columns. There's no "3 apps behind in prod" summary anywhere.

I tried "Environments" next, expecting an env-level rollup (like "prod: 12/15 healthy"). Every time I navigated there, it silently landed me on an unrelated app or chart detail page instead — once on `manmanv2-control-services`, once on `manmanv2-control-api` — with a console MIME-type error underneath. That link appears to be flat-out broken. I gave up on it.

"Drift & Audit" was legible and reassuring: exactly one drift, and it's in stage, not prod. Its own subtext mentions "the dashboard's drifted-override badge" — implying a dashboard view exists somewhere that I never found in the nav.

## Interview Answers

**1. Expectations before clicking anything:**
I expected a dashboard homepage — an at-a-glance summary, ideally with red/yellow/green per environment or per app, and I expected to start there since the root URL and the nav both suggest "Dashboard" is the entry point (the browser tab literally said "Dashboard"). I did not expect the landing page to be a raw data table.

**2. What I actually did, step by step:**
Loaded root → landed on "Apps Catalog" (not a dashboard despite the tab title) → got confused by rows full of "not promoted" that turned out to be a red herring → went to "Deployments," which was the real answer source → manually scanned version strings across dev/stage/prod for ~10 rows to spot which ones were behind → tried "Environments" expecting an env-level rollup and got redirected to random unrelated detail pages every time, twice → gave up on that link → checked "Drift & Audit" for a sanity check, which confirmed only one (non-prod) drift.

**3. Did I get the answer I came for?**
Mostly, yes, but I had to assemble it myself. I can now say in the meeting: "Five things are a version behind in prod — app-registry, leaflab, the FCM bot services chart, manmanv2-control-services, and manmanv2-host-manager — nothing looks broken or drifted in prod itself, one drift exists but it's in stage." That's a real answer, but the tool made me do the comparison work by hand instead of just telling me. I'm not 100% confident I didn't miss a row.

**4. Navigability: 2/5.**
The nav bar itself is fine and consistent, but the two links I most wanted — the implied "Dashboard" and "Environments" — either don't clearly exist or are broken. I bounced through three pages before finding the one that actually had my answer, and a genuinely broken link cost me real time.

**5. Usefulness/task success: 3/5.**
The raw data to answer my question is in there (on the Deployments page), and it's accurate as far as I can tell. But it required manual per-row comparison across three columns for ~30 rows, which is exactly the kind of "digging" I don't have patience for before a meeting. A manager-facing tool should not require me to eyeball-diff version strings.

**6. Trust/confusion moments:**
Big one: the wall of "not promoted" cells on the Apps Catalog page. My gut reaction was "oh no, nothing is deployed anywhere," which would have been a terrible thing to almost say out loud in a leadership meeting. It took extra digging to realize those rows are just child images of a chart and the chart row is the one that matters. Also, "Adopted" as a status label is jargon-y — I don't know if that means "healthy" or just "recorded" until I read the Drift & Audit page's fine print (turns out it means "manually asserted, not observed by CI" — that's an important distinction I would have missed).

**7. One thing I'd change immediately:**
Add a real landing dashboard with one number per environment: "prod: 5 behind, 1 drifted" (or green if clean), computed for me — not a table I have to scan by hand.

**8. Something delightful:**
The "Drift & Audit" page's drift count directly says "Not a cluster check — the registry only knows about its own promotion + chart-pin records" — that kind of plain-language caveat about what the number does and doesn't mean is exactly the trustworthy, un-jargony framing I wish the whole app had.

**9. Expected but missing:**
A dashboard/summary landing page (the browser tab is literally titled "Dashboard" but the page isn't one). An env-level health rollup. Any single "N apps behind in prod" count — the Drift & Audit page hints one exists ("the dashboard's drifted-override badge") but I never found it.

**10. Recommend to a teammate?**
For a quick prod status check, I'd point a teammate at the Deployments page and tell them to squint at the columns themselves — not because it's pleasant, but because it beats the CLI/grpcurl route for someone like me; a more technical teammate comfortable with the CLI might actually get a faster, more precise answer that way today.

---

# Playtest Report — Vik Chen, Staff Engineer

**Task**: Evaluate whether App Registry's UI is trustworthy enough to use during an incident, or whether it's faster/safer to keep trusting ArgoCD + `git log` + GHCR directly. Cross-check "currently promoted" claims across Dashboard, Deployments matrix, app detail pages, and Drift & Audit.

## Narrative

Started at the Dashboard (`http://localhost:8090/`). It immediately flagged "1 drifted override(s)" in `stage`, which is a good, honest first impression — it's not hiding the one messy thing in the demo data. I followed the trail: Dashboard → Deployments matrix → found the flagged row (`manmanv2-control-services` chart, stage cell has a `drift` badge) → drilled into the chart detail page → drilled into the specific app it names (`manmanv2-control-api`) → drilled into that promotion's own detail page. Along the way I also swung through the Apps Catalog to check the same app from a third angle.

Three things didn't survive the cross-check:

1. **The "drift" is presented as live-cluster fact, but the promotion's own record says it was never applied.** The Drift & Audit page and Deployments matrix both confidently list `manmanv2-control-api`'s stage override (v0.2.19) as "Adopted" and flag it as drifted against the chart's v0.2.20 pin. But that promotion's own detail page (reached via "details" on the app's promotion history) shows: Committed = "Not yet committed", Sync triggered at = "Not yet triggered", Current sync status = "—", and "No ArgoCD sync/health observations recorded yet." Status: **Pending**. If this was never committed to the GitOps repo, ArgoCD never saw it — the tool may be advertising a drift that never happened in the actual cluster. None of the headline screens (Dashboard badge, matrix cell, app detail's summary strip) hint at this; you only find it four clicks deep.

2. **Apps Catalog silently disagrees with the app's own detail page.** The Apps Catalog table shows `manmanv2-control-api` as "not promoted" for dev and prod. But that same app's detail page shows dev = v0.2.20 "Via chart" and prod = v0.2.19 "Via chart" — i.e., actually live via chart composition, not absent. Someone scanning the catalog to answer "what's running in prod" would wrongly conclude nothing is.

3. **Deep-linking is flaky.** Direct navigation to `/promotions/<uuid>` and to `/apps` twice rendered completely unrelated page content (once showed the Environments page, once a stale Artifact page) under the correct URL/title metadata, with no error. Clicking the same links through the UI worked fine both times. A pasted permalink during an incident is not safe to trust on first load.

Also noticed one console error (`MIME type` failure loading Tailwind CSS from a relative path on a nested route) — cosmetic, but it's the kind of paper cut that makes me trust the surrounding code less by association.

## Interview

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a thin, mostly-read-only status board — "what image/chart is live where" — and I expected to start at whatever screen answers "is prod healthy right now," which turned out correctly to be the Dashboard. That part matched my mental model fine.

**2. Walk through what you actually did, step by step — where did you get stuck?**
Dashboard → Deployments matrix → Chart detail → App detail → Promotion detail → Apps Catalog (see narrative). I didn't get stuck on *finding* things — the breadcrumbs and cross-links are genuinely good. I got stuck trying to reconcile what each screen was claiming as "current." I had to open a specific promotion record to learn that "Adopted"/drifted in the UI's vocabulary doesn't necessarily mean "committed and synced by ArgoCD" — that's not something you'd guess from the badge alone.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Partially. I could tell *something* was flagged, but I could not get a confident answer to "is stage actually serving v0.2.19 right now" from the UI alone — the one screen with that ground truth (sync status) says it was never triggered, which contradicts the confident "Adopted" badges everywhere else. In an incident I would not stop here; I'd go check ArgoCD directly.

**4. Rate navigability 1-5.**
4/5. Breadcrumbs, cross-links (chart → app, app → chart, app → artifact → chart), and consistent nav made it easy to *follow a thread*. Docked a point for the deep-link/hard-navigation flakiness — a UI that renders the wrong page silently under the right URL is a navigability bug, not just a trust bug.

**5. Rate usefulness/task-success 1-5.**
2.5/5, rounding to 2. It surfaces the *existence* of a drift and lets you get to a promotion record, which is more than raw `git log` gives you for free. But because the top-level screens overstate confidence (calling something "Adopted" that was never committed/synced) and the catalog undercounts things that actually are live, I'd have to re-verify everything against ArgoCD anyway — at which point the tool saved me clicks but not trust.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes, repeatedly. "Adopted" is overloaded — it means both "a human manually recorded this artifact" (provenance sense, as seen on the artifact page: "recorded after the fact by an admin, not observed by CI") *and* is used as the generic "this is the current promoted state" pill on the matrix/catalog, even for entries that were never committed to GitOps. Those are two very different confidence levels wearing the same badge. "Pending" on the promotion detail page also isn't surfaced anywhere upstream — you'd never know to ask.

**7. What's one thing you'd change immediately if you owned this UI?**
Propagate the sync/commit status (Pending / Not yet committed) up into the Dashboard alert, the Deployments matrix cell, and the Apps Catalog row — anywhere the word "Adopted" or "drift" appears. If a promotion hasn't been committed and synced, every screen that shows it should say so, not just the one detail page four clicks deep.

**8. What's something you thought was genuinely cool or delightful?**
The "Currently declared composition (chart_app)" table on the chart page, with the explicit disclaimer "independent of what any promoted version actually pins above" — that's exactly the kind of SCD2-aware, honest labeling I want from a tool dealing with temporal/versioned data. Whoever wrote that copy understands the domain. I wish the rest of the UI had that same rigor.

**9. Anything you expected to exist that wasn't there?**
A per-environment "last verified against ArgoCD" timestamp on the Dashboard itself — something cheap that would have told me up front not to trust the drift badge at face value. Also expected the Deployments matrix to show a row per app (not just per chart) so I could see `manmanv2-control-api` next to its siblings without drilling into the chart.

**10. One sentence: would you recommend a teammate use this UI, or would they be better off with the CLI/grpcurl?**
Use it to get oriented and find *where* to look, then verify anything that matters against ArgoCD directly — I would not let a teammate page based on this tool's badges alone.

---

# Playtest Report — Jordan Alvarez, Product Manager

**Task:** Confirm, without pinging an engineer, whether it's true that "the bot fixes shipped to dev, not prod yet" for "friendly-computing-machine."

## What I did

I opened the tool cold at localhost:8090. The Dashboard loaded first, and honestly it was more helpful than I expected — right there in a "Recent promotions" table I spotted "friendly-computing-machine-bot-services" three times: once for dev, once for stage, once for prod, each with a version number and a timestamp. I could squint and tell dev/stage said "v0.1.3" and prod said "v0.1.2," but I wasn't 100% sure that table was the "current state" versus just a log of recent events, so I didn't trust it as my final answer.

I clicked into "Apps" to look up the bot by name properly. That page ("Apps Catalog") had a real search box and a domain filter, which was reassuring — like a spreadsheet I could filter. I found two rows for "friendly-computing-machine-bot-services": one row said "chart" with version numbers and an "Adopted" tag per environment (dev v0.1.3, stage v0.1.3, prod v0.1.2), and a separate row for "friendly-computing-machine-bot" said "image (via chart)" and "not promoted" across all three environments. That second row worried me — is the bot itself not deployed anywhere?

I then landed on "Deployments," which turned out to be the clearest screen: one row per app, one column per environment, color-coded "Adopted" badges with the version number right in the cell. That's where I finally felt confident: dev = v0.1.3, stage = v0.1.3, prod = v0.1.2. Dev and stage match each other and prod is a version behind.

One odd thing: partway through, the page in my browser jumped on its own — twice — to screens I hadn't clicked into ("Promotion Details" for a totally different app, then "Builds," then "Reconcile Runs"), without me doing anything. I had to re-navigate back each time. I'm noting it because it happened to me as a user, even though I can't tell you why.

## Answers

**1. Expectations going in:** I expected something like a deploy dashboard — pick an app, see a simple "dev: X, stage: Y, prod: Z" readout, maybe with dates. I expected to start by searching for the app by name, like a search bar on a homepage.

**2. Walkthrough / where I got stuck:** Dashboard → (uncertain if the "recent promotions" table was live state or a log) → Apps Catalog (used the domain filter) → confused by two rows for the bot, one "chart" with real versions and one "image (via chart)" saying "not promoted" for all three environments → Deployments matrix, which finally gave me a confident answer. I also got knocked off course twice by the page navigating itself to unrelated screens (a promotion detail page, then Builds, then Reconcile Runs) with no click from me — I had to backtrack via the URL bar each time, which was disorienting since I'd have sworn I hadn't touched anything.

**3. Did I get my answer?** Yes, eventually, from the Deployments matrix: dev = v0.1.3, stage = v0.1.3, prod = v0.1.2. So my engineer was right — it's out in dev (and stage too, which nobody mentioned to me) but prod is still one version behind. What was missing: nothing told me in plain English "this IS the fix" — I have to trust that the newer version number equals "the bot fixes." Nothing labeled which version contained which fix.

**4. Navigability: 3/5.** Once I knew "Deployments" was the answer screen, it was fine. But nothing on the Dashboard or nav bar told me up front which of six menu items ("Environments," "Deployments," "Apps," "Builds," "Reconcile Runs," "Drift & Audit") was the one to check a specific app's status. I had to try two before landing on the right one. The uncommanded page-jumping also hurt this badly — if the screen can change on its own, I can't trust that clicking around gets me anywhere.

**5. Usefulness / task success: 4/5.** I did get the answer, and the Deployments matrix is genuinely good — a version number and a colored badge per app per environment is exactly the shape of "confirm this without an engineer." Docked a point because it took three screens to get there and because of the "not promoted" row for the bot's image that I still don't understand.

**6. Trust / jargon moments:** Several. "Adopted" — adopted by whom, from where? I guessed it means "this is the officially tracked version," but nothing explained it. "Drift detected" with a red banner scared me at first — I thought something was broken, but it turned out to be about a totally different app (manmanv2), not mine, and it just sat there as a page-wide warning regardless of what I was looking at. "chart" vs "image (via chart)" as two separate rows for what I'd call "the same app" was the most confusing single thing — I don't know why the bot needs two entries or why one is always "not promoted." And, again, the screen navigating itself without my input made me stop trusting that what I was looking at was what I'd asked for.

**7. One thing I'd change immediately:** Put a real search bar on the Dashboard (or make the nav bar's landing behavior smarter) so I can type "friendly-computing-machine" from the very first screen and land directly on its status, instead of guessing which of six nav items has it.

**8. Something delightful:** The colored version badges on the Deployments matrix (v0.1.3 in dev/stage vs v0.1.2 in prod, side by side in the same row) made the "is it in prod yet" comparison genuinely easy once I found it — that's exactly the kind of "no jargon needed" visual I was hoping for.

**9. Expected but missing:** A plain-language answer to "is X in prod" — like a single sentence or a simple yes/no per environment — instead of me having to compare version strings myself. Also no changelog or "what's in this version" link, so I still don't actually know if v0.1.3 contains "the bot fixes" my engineer mentioned — I'm inferring it from the version number being newer, not confirming it.

**10. CLI recommendation:** I'd tell a non-technical teammate to use this UI, not a CLI — once you find the Deployments page it's readable without engineering vocabulary, which a command line never would be for me.

---

# Playtest Report — App Registry

**Persona:** Sam Okonkwo, Release Engineer
**Task:** Find `friendly-computing-machine-bot-services`, compare stage vs prod, promote stage's version to prod if ahead, then trial the rollback flow for UX evaluation.

## Narrative

I started at the dashboard (`localhost:8090`), which conveniently already showed me a "Recent promotions" table on load — I could see `friendly-computing-machine-bot-services` at `v0.1.3` on dev and stage, and `v0.1.2` on prod, without clicking anything. I double-checked on the chart's own detail page (`/charts/friendly-computing-machine-bot-services`), which confirmed the same per-environment versions plus a breakdown of exactly which app images each chart version pins — nice for knowing what I'd actually be shipping.

Confirmed: stage (`v0.1.3`) was ahead of prod (`v0.1.2`). I went to the Deployments matrix, which is clearly the tool's real command center — one row per app/chart, one column per env, with inline ⬆ (promote) and ↺ (rollback) icons per cell. I clicked ⬆ on the prod cell.

The promote form was excellent: title says exactly "Promote friendly-computing-machine-bot-services → prod," the version dropdown defaulted to the newer v0.1.3, and a reason field was required. A banner explained a mandatory dry run: "Promote for real" stays disabled until I run a dry run against the *exact* current form state — and it re-arms if I touch anything after. I ran the dry run, got a clear preview ("Would promote to v0.1.3 … (supersedes v0.1.2)"), then hit "Promote for real." Confirmation was thorough: promotion ID, env, target, version+digest, what it superseded, actor, reason, and UTC timestamp. Back on the matrix, all three envs now correctly showed v0.1.3.

Then rollback, purely to check the UX. The rollback form was even better in one way: "Currently live: v0.1.3" vs "Will roll back to: v0.1.2" shown side by side before I touch anything, plus a button labeled "Confirm rollback to v0.1.2" (not just "Confirm") — impossible to be unsure what's about to happen. No dry-run step here, just reason + confirm. It recorded cleanly with the same detailed confirmation panel.

**Where I got stuck:** repeatedly, and badly. Direct URL navigation to `/promote?...` and clicking the ⬆ icon both intermittently got yanked mid-action to a completely unrelated `/promotions/{random-uuid}` detail page for a different app/env — once mid-way through typing a reason, before I'd typed anything. This happened three separate times across different apps' seed promotions, never the one I was working on. I eventually worked around it by re-navigating to `/deployments` fresh and clicking through immediately without pausing. This felt exactly like the app has some live-activity push (SSE/websocket) that force-navigates the current view to whatever promotion just landed in the feed, regardless of what the user is doing. For a release engineer, that's not a cosmetic bug — that's "I was about to promote to prod and the app dragged me to a stranger's rollback of a different service."

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a registry of "what's deployed where" with a promote/rollback action per app per environment, and I expected to start from a search or an app list — type the chart name, land on its page, see three env badges. That's roughly what happened; the dashboard's recent-promotions table was a bonus I didn't expect but liked.

**2. Walk through what you actually did, step by step — where did you get stuck?**
Dashboard → Apps search (via URL, since click refs to nav links kept going stale) → chart detail page to confirm versions → Deployments matrix → promote (dry run → confirm) → rollback (compare → confirm). The stuck point wasn't the forms themselves — it was that clicking the promote/rollback icon, or even hard-loading the promote URL, sometimes got hijacked mid-flight to a random unrelated promotion's detail page. I had to retry the "navigate to Deployments, then immediately click" sequence three times before one stuck long enough to fill the form.

**3. Did you get the actual answer you came for?**
Yes. Stage was ahead of prod (v0.1.3 vs v0.1.2), I promoted prod to v0.1.3, verified it landed everywhere, then rolled prod back to v0.1.2 to test that flow. Both actions are recorded with full detail (promotion ID, reason, actor, before/after version+digest, timestamp).

**4. Navigability: 3/5.**
Once you know the shape (Apps → chart page → Deployments matrix), it's fine and even pleasant — search-then-drill-down works. But nothing on the dashboard or Apps list tells you *where the actual promote/rollback buttons live* until you find the Deployments matrix; I only found it because the dashboard explicitly said "every action links out to the Deployments screen." A first-time user could hunt for a while.

**5. Usefulness/task-success: 3/5.**
The actual promote and rollback screens are genuinely well-built — clear, hard to misfire, good confirmations. I'm knocking two points off entirely for the random-navigation-hijack behavior I hit three times. A tool whose core job is "let a release engineer promote/rollback fast and without ambiguity" cannot also risk yanking you off the form you're mid-filling to some other team's history.

**6. Any moment you didn't trust the screen, or jargon confused you?**
Twice: (a) landing on an unrelated `/promotions/{uuid}` page after clicking prod's promote icon for *my* chart — for a second I genuinely thought I'd fat-fingered a rollback on `manmanv2-host-manager`. Had to check "Requested at" timestamps to confirm it wasn't something I'd triggered. (b) The promote form's banner text is dense ("UI policy requirement... disabled until a dry run has been run against exactly the form state below — changing any field re-arms this requirement") — accurate and I appreciate the honesty about UI-policy-vs-server-requirement, but it took me a second read to parse. A release engineer under pressure at 2am wants six words, not a paragraph.

**7. One thing you'd change immediately if you owned this UI?**
Kill whatever live-update mechanism is force-navigating users off the page they're on. At minimum, a toast/badge for "new activity" is fine; silently replacing my in-progress form with someone else's promotion detail page is not.

**8. Something genuinely cool or delightful?**
The rollback screen's "Currently live: vX" vs "Will roll back to: vY" side-by-side comparison, combined with a confirm button that names the target version in its own label ("Confirm rollback to v0.1.2"). That's exactly the kind of "hard to get wrong by accident" design I want from a promote/rollback tool — no separate confirm dialog needed, the button itself is the confirmation.

**9. Anything you expected to exist that wasn't there?**
A diff or changelog between v0.1.2 and v0.1.3 (which app images actually changed) would've been useful right on the promote screen — I had to cross-reference the chart detail page's "Apps published" tables myself to see that four of five sub-apps bumped. Also no visible "who else is looking at / mid-editing this promotion" indicator, which would help explain (or prevent) the hijacking behavior I hit.

**10. Would you recommend a teammate use this UI, or the CLI/grpcurl?**
The UI, once the navigation-hijack issue is fixed — the promote/rollback forms themselves are clearer and safer than I'd trust myself to be typing flags into a CLI at 2am.

---

# Playtest Report — App Registry

**Persona:** Aisha Bello, Security/Compliance Auditor (semi-technical)
**Task:** Audit who promoted what to production recently, verify build provenance (Observed vs. Adopted) for everything currently running in prod, and find drift/manual-override records.

## Narrative

I started at the dashboard (localhost:8090), which immediately gave me a "1 drifted override(s)" banner and a promoted-artifact count — a reasonable landing page for someone doing a health check. From there I went to **Deployments**, the app's promotion matrix (rows = apps/charts, columns = dev/stage/prod). Every currently-promoted cell carries a small "Adopted" or blank badge next to its version, so I could scan the prod column at a glance. What jumped out immediately: every single prod-running entity I looked at — app-registry, friendly-computing-machine-bot-services, leaflab-leaflab, manmanv2-control-services, manmanv2-host-manager — was tagged **Adopted**, not Observed.

I drilled into manmanv2-host-manager's app page, which has a clean "Promotion history" list showing who (developer/you), when, and a free-text "Reason." In this dataset every reason was the same seed-script string, so I couldn't judge the tool's handling of real reason text, but the field clearly exists and is populated per-event with a "details" drill-through to a full Promotion Details page (requester, timestamp, action, reason, plus an ArgoCD writeback/sync section).

I then opened the artifact page for the prod image directly. It has an explicit **Provenance** row: "Adopted — recorded after the fact by an admin, not observed by CI." That's exactly the sentence an auditor wants to see — unambiguous, no jargon.

The best find was the dedicated **Drift & Audit** page. It has two purpose-built tables: "Drifted overrides" (found the same 1 drift as the dashboard — a stage override of manman-control-api no longer matching the chart's pin) and "Adopted artifacts" — a full, filterable ledger of every artifact anyone hand-registered instead of CI. Cross-checking against **Builds**, I confirmed nearly every current build record is a synthetic `#adopted:<uuid>` entry credited to "dev-user," not a real CI run with a commit SHA — so the badges are trustworthy, not cosmetic.

Two things undercut my confidence. First, a Promotion Details page for the artifact currently marked "live" in prod showed status **Pending**, "Not yet committed," and "No ArgoCD sync/health observations recorded yet" — directly contradicting the app page calling it the current prod version. Second, navigating straight to /drift-audit once silently bounced me back to /deployments, and a stray background navigation fired after I'd moved on to another page — I couldn't tell if that was my own misclick or the app.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a promotion/audit ledger — something like a change-log per environment — and I expected to start either on a dashboard or on a per-app history page. That's roughly where I landed, which was reassuring.

**2. Walk through what you actually did, step by step — where did you get stuck?**
Dashboard → Deployments matrix → app detail (promotion history) → artifact detail (provenance) → promotion detail (status) → Drift & Audit → Builds. The one real snag: a direct link to /drift-audit didn't load the page the first time (silently landed on Deployments instead), and separately, a click I made seemed to fire late and dropped me on an unrelated Promotion Details page. Neither was fatal — a reload fixed it — but I wouldn't want to explain "just reload it" to my compliance committee.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Yes, largely. I could confirm every prod-running artifact I checked is Adopted, see the full list on Drift & Audit, and see the one active drift. What I couldn't fully resolve is the "Pending" promotion status contradicting the app page's "this is current" claim — I don't know which one is the source of truth for "is this actually what's running," and that's the single most important question for an audit.

**4. Rate navigability 1-5.**
4/5. Once I knew the five nav items, everything I needed was one or two clicks away, and the Drift & Audit page in particular is clearly built for exactly this task. Docked a point for the flaky direct-link/redirect behavior.

**5. Rate usefulness/task-success 1-5.**
4/5. It answered "who promoted what, when, adopted vs. observed, and where's the drift" cleanly and in one dedicated screen. It falls short of 5 because the Pending/"not yet committed" vs. "this is the current version" conflict on the promotion record left me without a confident answer to "is this actually live," which is the whole point of an audit trail.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes — the Promotion Details "Pending" status with "Not yet committed" and "No ArgoCD sync/health observations recorded yet," sitting right next to an app page that calls the same version the current prod artifact. I also noticed the Drift & Audit page's footer text says "recording a new adoption is screen 52, delivered separately" — that's an internal ticket/screen number leaking into a page real users will read, which reads unprofessional and confusing to a non-engineer.

**7. What's one thing you'd change immediately if you owned this UI?**
Reconcile the "Pending" promotion status against the "current artifact" label — either show ArgoCD's actual last-sync result on the app page, or explain in one line why a promotion can be "Pending / not yet committed" while simultaneously being shown as live. Right now those two screens tell two different stories about the same deployment.

**8. What's something you thought was genuinely cool, clever, or delightful?**
The artifact Provenance sentence — "Adopted — recorded after the fact by an admin, not observed by CI" — is exactly the plain-English framing I need to drop straight into a finding without translation. And the Drift & Audit page's "Not a cluster check" disclaimer under Drifted overrides was a nice, honest scoping note that told me exactly what the number does and doesn't mean.

**9. Anything you expected to exist that wasn't there?**
An approver/reviewer field on promotions — everything I saw was self-service (requester = approver = "developer (you)"), with no second-person sign-off. For a prod promotion audit, "who approved this, not just who requested it" is usually the first question I'd be asked. I also didn't see a way to filter promotion history by date range across all apps at once — I had to go app-by-app.

**10. One sentence: would you recommend a teammate use this UI, or would they be better off with the CLI/grpcurl?**
For this exact audit — provenance and drift across prod — the UI is genuinely faster and clearer than I'd expect a CLI dump to be, so yes, I'd point a teammate here first.

---

# Persona Playtest Report

**Persona:** Leo Ferreira — QA / Support Engineer (semi-technical, ticket-driven)
**App under test:** App Registry (localhost:8090)
**Task:** Support ticket — "Customer says something in manmanv2-host-manager is broken in an environment, and swears it was working before. Find out what changed and when."

## Narrative

I went in expecting a "current state + history" screen, like a deploy dashboard — pick the app, see what's live per environment, see what changed and when. First stop was `/apps`, then I found `manmanv2-host-manager` and jumped to its app detail page. That page was actually pretty good: a row of environment cards (Latest/dev/stage/prod) plus a "Current artifact by environment" table plus a "Promotion history" list, all on one page. No hunting required to get oriented.

The state itself: dev and stage are both on v1.1.0, prod is still on v1.0.0. That alone answers "did something change" for prod — no, prod's never been bumped past v1.0.0, so if the complaint is about prod, nothing changed recently there. But the promotion history told a more interesting story for dev. I clicked into the detail page for each of the 5 history entries (via their "details" links / permalink URLs) and reconstructed:

- 06:31:46 — dev promoted to v1.1.0 (initial)
- 06:31:46 — stage promoted to v1.1.0 (initial)
- 06:31:46 — prod promoted to v1.0.0 (initial)
- 06:32:48 — an entry **labeled "promote"** that actually shows `dev: v1.1.0 → v1.0.0` (a downgrade)
- 06:32:48 — an entry **labeled "rollback"** that actually shows `dev: v1.0.0 → v1.1.0` (an upgrade, undoing the downgrade)

That's backwards from what the words mean in plain English, and it tripped me up hard — I had to open both detail pages and stare at the before/after versions to figure out the "rollback" was actually the one that put dev back on the newer version, not the older one. If I were working this ticket for real I'd have reported the wrong direction of change to the customer on first read.

Also: every one of these promotion detail pages says "Committed: Not yet committed" and "Sync triggered at: Not yet triggered" with a "Pending" status badge — but the app's own summary table confidently shows dev/stage/prod as "Adopted" with hard version numbers. So which is it — did this actually go out, or is it stuck? The page doesn't reconcile that for me.

Trying to check the CI build behind the v1.1.0 image (there's a "build 2c8c5ae8…" link right on the app page) got me a **502 Bad Gateway**. Screenshot: `leo-build-502-error.png`. Also, while clicking around, several direct navigations to `/promotions/<id>` links intermittently bounced me to unrelated pages (Deployments, Drift & Audit, Reconcile Runs) with no explanation — I never got a clean root cause on that, just noting it made the app feel unstable/flaky mid-investigation.

Screenshots saved: `leo-app-detail-manmanv2-host-manager.png`, `leo-promotion-92a2c338.png`, `leo-build-502-error.png`.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a "what's deployed where, right now" dashboard, and I expected to start by searching/typing the app name to land on one page showing all three environments side by side plus a change log. That's basically what I got by going to Apps → manmanv2-host-manager, so my mental model matched reality on the first try.

**2. Walk through what you actually did, step by step — where did you get stuck?**
Apps catalog → found manmanv2-host-manager → app detail page (environment cards + current-artifact table + promotion history, all in one place, good) → clicked into each of the 5 promotion history "details" links to get real timestamps and before/after versions → tried the linked build ID and hit a 502. I got stuck specifically trying to figure out which promotion event was the "bad" one, because the action label (promote/rollback) didn't match the direction of the version change shown right next to it.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Mostly yes for dev/stage — I can tell you dev went 1.1.0 → 1.0.0 → 1.1.0 within about a minute (06:31:46 to 06:32:48), and prod has stayed on 1.0.0 the whole time. What's missing: I can't tell if that dev flip-flop ever actually deployed anywhere real. Every promotion record says "not yet committed / not yet triggered," which is a scary thing to hand back to a customer as "here's what changed" if I'm not sure it's live.

**4. Rate navigability 1-5.**
4. The app detail page is genuinely the right one-stop page for this kind of question, and Apps → app name got me there in two clicks. Docking a point because direct links to promotion detail pages weren't reliable — some threw me onto an unrelated page instead of the promotion I asked for.

**5. Rate usefulness/task-success 1-5.**
3. I can answer "what changed and when" for the version numbers themselves, which is most of the ticket. I can't confidently answer "was it actually deployed" because of the Adopted-vs-Pending/Not-committed contradiction, and I can't pull the underlying build/CI info because that link 502'd.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes, twice. First: "promote" moving a version DOWN and "rollback" moving it UP, in the same minute, for the same app/env — that's the opposite of what those words mean to me. Second: the summary table says "Adopted" with a live version, but the promotion record behind it says "Not yet committed" / "Pending" — I don't know which one to believe is the ground truth.

**7. What's one thing you'd change immediately if you owned this UI?**
Fix the promote/rollback labeling so the label always matches the direction of the version change (or better: just always show "v1.1.0 → v1.0.0" style before/after and drop the ambiguous verb, or add a tooltip explaining what "rollback" means here if it's not simply "go to a lower version").

**8. What's something you thought was genuinely cool, clever, or delightful?**
The app detail page laying out Latest/dev/stage/prod as parallel cards right at the top, with a copy-digest button next to every artifact hash — that's exactly the kind of thing that saves a support engineer from fat-fingering a sha256 into a ticket.

**9. Anything you expected to exist that wasn't there?**
A plain-English "what changed" diff or changelog per version bump (commit list, PR link, release notes) — I have version numbers and digests but nothing telling me what actually changed in v1.1.0 that might explain the customer's complaint. Also expected the promotion detail page's "Sync history" to actually have entries given the versions clearly show as live elsewhere — it just says "No ArgoCD sync/health observations recorded yet," which is a dead end for the "is this actually deployed" question.

**10. One sentence: would you recommend a teammate use this UI, or would they be better off with the CLI/grpcurl?**
For a quick "what version is where and when did it change" check I'd point a teammate at this UI, but for anything where they need to trust that a deploy actually shipped, they'll need to cross-check ArgoCD directly because this tool's own "committed/synced" fields don't back up what its summary view claims.

---

# Playtest Report — Riley Tran (fresh-eyes tourist)

**Persona:** Riley Tran, moderately technical (comfortable with web apps, not a backend engineer). First time hearing about this tool. No assigned task — just clicking through everything and reacting.

**App:** App Registry, running at http://localhost:8090

---

## Narrative — what I actually did

### Dashboard (`/`)
Landed here cold. It's immediately legible: three big env cards (dev/stage/prod) each showing "N promoted" and a Healthy/Drift badge, a warning banner up top ("1 drifted override(s) — an overridden image no longer matches its chart's pin"), and a "Recent promotions" table. The very last line on the page is a one-liner that did more for my understanding than anything else: *"Dashboard is read-only by design — every action here links out to the Deployments screen that owns it."* That single sentence told me the whole information architecture in one shot: this page is a summary, Deployments is where you act.

### Environments (`/environments`)
A plain table of dev/stage/prod with Key, Display name, Rank, "Requires approval," "Allowed principals," GitOps path, and Promoter role. Column headers have hover-tooltips, and two of them are surprisingly candid: "Requires approval" is annotated **"stored, not enforced — Promote does not check it,"** and "Allowed principals" is annotated **"does not restrict who can promote — the promoter role check is the only access control in force."** There's also a footnote paragraph repeating this in plain English. I appreciated the honesty, but as someone brand new to the tool, my first reaction was "wait, so this field just... does nothing right now?" That's a genuinely confusing thing to put in front of a first-time user, even if it's transparent.

### Deployments (`/deployments`)
This is clearly the heart of the app — the paragraph under the title says it outright: "One row per promotable entity, one column per environment. Promote or roll back an app directly from its cell." It's a matrix: rows are apps/charts, columns are dev/stage/prod, each cell shows version@digest (with a copy button), an "Adopted" badge, and ⬆ (promote) / ↺ (rollback) icons. Rows with "not promoted" cells are just as common as populated ones — this is clearly a seeded demo catalog with a lot of apps that were never pushed anywhere.

I found the drifted row (`manmanv2-control-services`, flagged "drift" in the stage column) and clicked it — turns out the whole row is an expandable `<details>` disclosure, not a link. Expanding it revealed the individual child images under that chart (`manmanv2-control-api`, `-migration`, `-event-processor`, etc.), and the drifted one showed an "override…" link instead of the usual promote/rollback icons. That's a nice touch — it tells you exactly which sub-component broke pin and gives you a path to fix it, without you having to go dig through the chart's manifest.

There's also an "As of (UTC)" time-travel field at the top I didn't fully explore, but its presence hints at real audit-trail depth.

### Apps Catalog (`/apps`)
A searchable/filterable table (search box, domain dropdown, deploy-unit dropdown) listing every app and chart with its per-env status. I clicked into `app-registry-app-registry` (the chart) and got a detail page broken into three sections — "Apps published in v0.0.35 (dev)," same for stage, same for prod (v0.0.34) — each listing the child app images that version pins, with digests linking to an artifact page. Below that, a separate "Currently declared composition" table explicitly labeled as independent from what's actually promoted. That distinction (declared-now vs. pinned-then) is exactly the kind of thing that would be confusing without the label, and they labeled it.

Small delight: this chart, `app-registry-app-registry`, is App Registry tracking itself — the tool dogfoods its own registry. Cute detail once you notice it.

One real bug: opening this chart detail page threw a console error — `Refused to apply style from 'http://localhost:8090/charts/tailwindcss' because its MIME type ('text/plain') is not a supported stylesheet MIME type`. Looks like a relative stylesheet link that doesn't survive being loaded from a `/charts/<name>` route (resolves to `/charts/tailwindcss` instead of `/tailwindcss`). The page still rendered fine visually (presumably cached from the shell), but it's a real console error on a real path.

### Builds (`/builds`)
"CI run recording status — find out exactly what did and didn't finish." A long table of workflow runs (mostly `#adopted:<uuid>` entries from dev-user, a couple of real `seed-*` runs, a couple from `system-validator`). Footer note: "Recording health, artifact counts, and domain are not shown here — Build carries no artifact data. Open a run to see its recording health." I opened a couple of runs by accident while chasing stale element refs, and got two different states: one run showed "App Registry recording health: Indeterminate" with a banner explaining the tool genuinely can't tell whether recording was off or failed silently ("resolve it by checking the run itself" with a link out to GitHub Actions); another showed "Healthy" with one artifact row. That Indeterminate-vs-Healthy distinction, spelled out honestly instead of just saying "no data," was a good moment of trust-building.

### Reconcile Runs (`/reconcile-runs`)
Short and clear: "The identity pipeline — ReconcileApps from ci.yml, on every push to main. Separate from Builds: this tracks app/chart identity, not published artifacts." One row in the table (37 apps seen, 12 charts seen, 7 hours ago). A callout explains that a sweep rejected as "stale" is expected/correct behavior, not a bug, and writes nothing — so you won't see a row for it. I liked that they preempted the "why don't I see my sweep" confusion before I could even have it.

### Drift & Audit (`/drift-audit`)
Two sections: "Drifted overrides" (the same one row I'd already found via the Deployments matrix — good consistency, the counts and the identity of the row matched exactly), and "Adopted artifacts," a long log of every artifact a human asserted rather than CI observed, with owner/kind/version/digest/timestamp. Footer: "No adopt control lives here — recording a new adoption is screen 52, delivered separately. This screen is read-side audit only." That "screen 52" reference is clearly internal spec numbering that leaked into user-facing copy — harmless but a little odd to see as an end user.

### Trigger Release (`/releases/trigger`)
This one didn't match my mental model at all going in. I expected "Trigger Release" to mean "push a promoted version out," but it's actually the opposite end of the pipeline — a big checkbox tree of every domain/app/chart (app-registry, demo, friendly-computing-machine, leaflab, manman, manmanv2, tools, each with children) to select what to build via CI, plus an optional "Digest (optional)" field to pin to a pre-existing digest instead of building fresh, and a "Resolve selection" button. There's no explanatory paragraph at the top of this page the way every other screen has one — it's the one screen where I was genuinely unsure what "Resolve selection" was going to do before I'd click it (I didn't test it, since it looked like it might actually kick off a real CI run). I now understand this is upstream of the App Registry's normal job (recording artifacts once CI/GitHub Actions runs), but a first-timer lands here and immediately confuses it with "promote."

### Promote form (drilled in from Deployments)
Clicking a promote (⬆) icon opens a focused form: Version dropdown (with digest previews), a required Reason field with an excellent tooltip — *"required for every environment (UI policy; the server itself only requires it above dev/rank 0), becomes part of the permanent history"* — and three actions: Cancel, **Run dry run**, **Promote for real**. I typed a reason and ran a dry run (did not promote for real). It came back instantly: "Dry run only — no write performed. This would be a no-op: the selected artifact is already current for this target." That's exactly the kind of safety rail and plain-language feedback I want before I'd trust myself to click the real button. This was the single most reassuring interaction in the whole app.

I also tried promoting an app with no published artifacts (`demo-demo-all-types`) and got a clean guard message instead of a broken form: "demo-demo-all-types has no published artifacts of this kind yet — nothing to promote."

---

## Answers to the interview questions

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
From the brief ("records which artifact is promoted to each environment, lets you promote/rollback, ArgoCD does the actual deploying") I expected something like a spreadsheet-crossed-with-dashboard: a grid of apps × environments showing current versions, with buttons to bump a version up an environment. I expected to start on a dashboard or a big matrix page. That's almost exactly what I got — Dashboard first, Deployments as the real matrix — so my mental model was basically right going in.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → Environments → Deployments (expanded the drift row) → Apps Catalog → into a chart detail page → Builds → Reconcile Runs → Drift & Audit → Trigger Release → back into a Promote form to run a dry run. I got stuck exactly once, on "Trigger Release" — I sat there for a second not sure if "Resolve selection" was going to kick off a real build, and backed off rather than clicking it. I also had some tool-level flakiness (stale element references making me land on pages I didn't click into, e.g. a couple of Build detail pages) but that's an artifact of my own browser-automation session, not the app.

**3. Did you get the actual answer you came for? Clear mental model?**
Yes. By the end I had a clean mental model: **Builds/Reconcile Runs write facts in from CI** (what got built, what apps/charts exist) → **Deployments is the control surface** (what's promoted where, promote/rollback/dry-run) → **Drift & Audit and Dashboard are read-only reflections** of that same state at different granularities → **Trigger Release is upstream, kicking off CI itself, not a promotion action.** That last one only clicked for me after visiting the page directly; I would not have guessed it from the nav label alone.

**4. Navigability: 4/5.**
Seven items, always visible, active state, consistent order — I never lost my place. Docked one point because "Trigger Release" as a label sits in the same nav bar as "Deployments" and reads like a sibling action to promoting, when it's actually a different pipeline stage entirely (kicks off CI rather than moving an artifact). A newcomer will click it expecting "release my promoted app" and get a build-selection checkbox tree instead.

**5. Usefulness/task-success: 4/5.**
If someone told me "go promote v0.0.35 of X to stage," I could do it confidently — the matrix, the promote form, the dry run, the reason field, all walk you there without needing a manual. I'd dock a point only because a couple of read-side screens (Environments' unenforced fields, Trigger Release's blank slate) would leave a first-timer unsure whether they're looking at a fully-built feature or a half-finished one.

**6. Any moment you didn't trust the screen, or jargon confused you?**
Not distrust exactly — the opposite, actually: the app is unusually forthcoming about its own limitations (the Environments tooltips, the Builds "Indeterminate" health state, the Reconcile "stale sweep" callout). The one thing that read as jargon-not-meant-for-me was Drift & Audit's footer mentioning "screen 52" — that's clearly an internal design-doc reference that slipped into shipped copy.

**7. One thing you'd change immediately if you owned this UI?**
Add one sentence at the top of Trigger Release, the same way every other screen has one, explaining "this kicks off a CI build for the selected targets — it does not promote or deploy anything." Every other page in this app has exactly that kind of orienting sentence; this is the one place it's missing, and it's the page most likely to be confused with the app's core "promote" action.

**8. Something genuinely cool or delightful, even small?**
The promote form's dry run. Typing a reason, hitting "Run dry run," and getting back an instant, specific, plain-English verdict ("This would be a no-op: the selected artifact is already current for this target") before I'd committed to anything real — that's the kind of safety rail that makes we willing to eventually click "Promote for real" on a tool I'd never used before. Close second: the expandable chart row in Deployments that reveals exactly which child image drifted and gives you an "override…" link right there.

**9. Anything you expected to exist that wasn't there?**
A few things: no visible way to see "who promoted what" as a person (the audit trail shows timestamps and reasons but I didn't spot an actor/user column on promotions the way Builds shows an Actor column for CI runs); no obvious rollback history/diff view (rollback is a single icon, I didn't see what it rolls back to before clicking); and no in-app link from the Drift & Audit "drifted override" row straight to a promote-to-fix action — you have to go back to Deployments and expand the row yourself.

**10. Would you recommend a teammate use this UI, or the CLI/grpcurl?**
The UI, without hesitation — the matrix view, drift highlighting, and the dry-run safety net give you a picture (and a safety check) that a raw CLI call just can't, especially for anyone who isn't going to memorize the promote-with-flags incantation.

---

# Persona Playtest Report

**Persona:** Chen Liu — SRE, daily `app-registry` CLI user, automation-first, skeptical of UIs
**Task:** Evaluate the App Registry web UI's CLI-facing/CI-facing screens (Builds, Reconcile Runs, Trigger Release) plus a spot-check of Promote/Diff, at http://localhost:8090

## Narrative

I went in assuming this would be a read-only status board over the same tables the CLI hits, and started at the nav bar rather than the Dashboard, since that's the fastest way to see the app's shape. The Dashboard turned out to be a decent landing page — env health tiles, drift banner, recent promotions — and it says outright "Dashboard is read-only by design," which is the kind of honest labeling I appreciate.

Builds was the first real test. It's a flat table (Run / Commit / Actor / Started / Recorded) with a workflow-run-id lookup box up top — basically `builds get <id>` without needing to remember the exact ID format. But it's a wall of ~35 rows, mostly synthetic `adopted:<uuid>` entries with blank commit/started columns, and there's no status column, no filter by app/domain/outcome, no pagination control I could find. Clicking into a run (`/builds/<id>`) gets you a real payoff though: "App Registry recording health: Indeterminate/Healthy" plus a per-artifact table with State (Published, etc.) — that's a synthesized judgment call the raw CLI output makes me eyeball myself. Genuinely useful, not just a shadow.

Reconcile Runs was thinner: one row, no drill-down, no link into what actually changed identity-wise, and a static disclaimer explaining why stale-rejected sweeps don't show rows. Fine as a sanity check, useless for anything beyond "did the last sweep happen."

Trigger Release is the most interesting screen — a checkbox tree of every domain/app/chart plus an optional digest pin, feeding a "Resolve selection" step. It's the one place the UI does something CI-facing that isn't just displaying rows; browsing an org-wide app tree by checkbox beats me trying to remember exact target strings for a CLI flag. I ran into real instability trying to interact with it and the Promote screen — refs and page state kept invalidating faster than I could act on them, and once I hit a stretch where the tab went to `about:blank` and a screenshot call failed outright. A reload fixed it, and I can't rule out this being local dev-server hot-reload noise rather than a production bug, but a tool for triggering releases needs to not do that, ever.

Promote's dry-run flow, once I got a stable run at it, was clean: pick version, reason (required, with a footnote clarifying it's UI policy not server policy — nice bit of honesty), Run dry run vs Promote for real. Dry run correctly told me "no-op, already current" instead of pretending to do work.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a read-only mirror of `apps list`/`promote status` — something to sanity-check state without memorizing flags, nothing I'd actually mutate through. I started at the nav bar, not the Dashboard, because I wanted to see the full page inventory before trusting any one screen.

**2. Walk through what you actually did, step by step — where did you get stuck?**
Dashboard → Builds (list, then a no-artifact run, then a healthy run) → Reconcile Runs → Trigger Release → got stuck hard trying to check a checkbox and hit "Resolve selection": element refs kept going stale between snapshot and click, one click landed on a completely different page than I clicked on (a "chart detail" page instead of the checkbox), and at one point the tab went blank and a screenshot call errored out. I recovered by reloading and slowing down — click one thing, snapshot, click the next thing immediately after, no gaps. Once I did that, checking a box and running Promote's dry-run worked cleanly.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Mostly yes. I confirmed Builds and Reconcile Runs are read-only shadows of their CLI counterparts (list/status only, no export, no bulk actions, no way to re-trigger or annotate a run), and that Trigger Release and Promote are the two screens that actually do something beyond mirroring. What I didn't get: a dedicated Diff screen. I searched for "diff" on the pages I visited and found nothing — the closest equivalent is Promote's dry-run alert, which is fine for a single target but isn't `promote diff` across an environment.

**4. Rate navigability 1-5.**
4/5. Seven flat nav items, no nesting, everything is one click from anywhere, and page headers explain scope in a sentence ("Separate from Builds: this tracks app/chart identity, not published artifacts" on Reconcile Runs is exactly the kind of line that saves me from filing a confused ticket). Docked a point because Builds has no way to filter/search past the single run-id lookup, so finding one build in a specific app/domain means scrolling a long unsorted table.

**5. Rate usefulness/task-success 1-5.**
3/5. For read-only status checks it's a fine shortcut over typing `builds list` and squinting at JSON. For anything CI-facing at scale — filtering builds by app, checking why a reconcile sweep didn't pick something up, doing a multi-target diff before a release — it doesn't cover what I'd reach for the CLI to do, and Trigger Release/Promote (the two screens with real teeth) were the ones that got flaky under me, which is the worst place for that to happen.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes, twice. "App Registry recording health: Indeterminate" on a run with zero artifacts reads exactly like an incident status, and I had to read the alert text carefully to realize it means "opt-in might have been off," not "something broke." And the instability itself — refs going stale, a click landing on an unrelated chart-detail page, a blank tab — made me not trust that "Resolve selection" would actually resolve what I'd checked, not something stale from a prior render.

**7. What's one thing you'd change immediately if you owned this UI?**
Add filters (app, domain, outcome/recording-health) and a status column to the Builds table — right now it's an undifferentiated list of ~35 rows and the only navigation aid is an exact-ID lookup box.

**8. What's something you thought was genuinely cool or delightful?**
The Reason field on Promote explicitly noting "required for every environment (UI policy; the server itself only requires it above dev/rank 0)" — that's the kind of CLI-vs-UI-behavior-diff callout I'd normally have to go read source to find. And the dry-run alert correctly identifying a no-op instead of just running a fake diff.

**9. Anything you expected to exist that wasn't there?**
A standalone `diff` view — comparing what's promoted across environments for one app/chart side by side, beyond the single-target dry-run alert. Also no visible `rollback history` or `promote history` screen distinct from the Deployments matrix — I didn't find a dedicated audit trail of past promotions beyond "Recent promotions" on the Dashboard (which only shows a handful, no pagination).

**10. One sentence recommendation.**
For a quick status check I'd point a teammate at the UI, but for anything that touches Trigger Release or Promote I'd tell them to keep the CLI in their other terminal tab until this thing stops losing state mid-click.

---

