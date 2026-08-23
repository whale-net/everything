# App Registry UI — User Journey Transcripts, Wave 2 (2026-08-23)

Full per-persona narratives and interview answers behind
[USER_JOURNEYS_2026-08-23_WAVE2.md](USER_JOURNEYS_2026-08-23_WAVE2.md)'s synthesis. This is
the second cohort (personas 11-30) run the same session as the first ten — see that file's
methodology section for the shared-session/concurrency caveat that applies throughout. Each
section below is one independent simulated session (Playwright MCP driving a real browser
against `http://localhost:8090`, UI-only, no CLI/source access), written by the persona
in character.

---


**Persona:** Taylor Kim — brand-new intern, first day, non-technical
**Task:** Manager asked (over Slack): "Can you just check whether leaflab's worker service is actually live in prod right now? Don't worry about how, just poke around the App Registry tool, should be self-explanatory."

## Narrative

Okay, deep breath. First day, first "real" task, and it's phrased like it should be easy — "should be self-explanatory," she said. I opened http://localhost:8090 half-expecting a login screen or something scary, but it just dropped me straight onto a "Dashboard" with a purple banner that says "What's live, what drifted, what needs attention — across every environment." Fine, promising, that's basically what I need.

There's a red warning strip: "⚠ 1 drifted override(s) — an overridden image no longer matches its chart's pin." I have no idea what a "chart's pin" is, but I also don't think that's my task, so I filed it away as "not my problem right now" and moved on. There are three little cards for dev / stage / prod — prod says "5 promoted, Healthy." Okay, so "promoted" apparently means "live." Good, new vocab word learned.

Below that there's a "Recent promotions" table, and I actually see "leaflab-leaflab" in there with a prod row at v0.0.10! I got excited for a second — "hey, is that the answer?" — but then I remembered she said "worker service," not just "leaflab." So "leaflab-leaflab" is presumably the whole app/product, not specifically the worker part. I needed to actually find something called "worker."

I clicked "Apps" in the top nav. Nothing happened — the page just sat there still showing the Dashboard. I clicked it again. Still nothing. I got a little paranoid I was clicking the wrong spot, so I typed the URL directly instead (localhost:8090/apps) and that seemed to load — the browser tab title even changed to "Apps Catalog" — but the actual page content on screen was still the Dashboard?! I want to be clear this wasn't just me being slow: I checked twice. Eventually clicking on the literal word "Apps" via a slightly different approach did land me on a real "Apps Catalog" page with a search box, domain filter, and a big table of every app. So it did work eventually, just... flaky. Not a great start for an app whose whole selling point seems to be "trust me, I know what's live."

Now, the Apps Catalog. There's a domain dropdown: app-registry, friendly-computing-machine, leaflab, manman, manmanv2, tools. Good, "leaflab" is a real option. I filtered and found four leaflab things:
- **leaflab-leaflab** (marked "chart")
- **leaflab-leaflab-api**
- **leaflab-migrate**
- **leaflab-processor**

None of them is called "worker." Great. Exactly the kind of gap she warned me about — "you'll have to search/browse to even figure that out." So I tried the search box for "worker" across *everything*, no domain filter, just to see what comes up. It found **app-registry-worker**, **friendly-computing-machine-worker**, **manman-worker**, and **manman-worker-dal-api** — so lots of other product areas literally have a thing called "worker." Leaflab just... doesn't. That's a dead end I wasted a few minutes on.

Also, weirdly, when I was poking around the "Deployments" matrix page later I spotted charts named **demo-hello-worker** and **demo-demo-workers** — under what I think is a "demo" domain that isn't even in the Apps Catalog's domain dropdown. For one confusing second I wondered if THAT was what she meant, before realizing "demo" is obviously just sample/test data and has nothing to do with leaflab. Small thing, but it's exactly the kind of red herring that would trip up someone who doesn't already know the system.

So, back to leaflab. Of the four leaflab things, "leaflab-migrate" sounds like a one-time database migration job, not an ongoing "service." "leaflab-leaflab-api" sounds like the API, not a worker. That left **leaflab-processor** as my best guess — "processor" sounds like exactly the kind of background-grinding-away thing a non-technical person would call "the worker."

I clicked into leaflab-processor's page to check. And here's where I actually got worried I was about to give my manager wrong information: the Apps Catalog table I'd just come from showed leaflab-processor as **"not promoted"** in dev, stage, AND prod. That sounds like "not live anywhere," full stop. But the app's own detail page showed a totally different story — a "Current artifact by environment" table with real version numbers and digests: dev v0.0.7, stage v0.0.7, and **prod v0.0.6**, each tagged "Via chart." So which is it?? The catalog said no, the detail page said yes.

I dug one level further and opened the **leaflab-leaflab chart** page (leaflab-processor is "part of chart: leaflab-leaflab" — there's a little package-emoji link for that). That page confirmed: prod is running chart version v0.0.10, promoted 35 minutes ago, and that chart version's "currently declared composition" pins leaflab-processor at v0.0.6 in prod, status "Adopted." So it really is live — the "not promoted" label in the main Apps Catalog table apparently just means "this specific app was never promoted *directly, on its own*," not "this isn't running." Nobody tells you that distinction anywhere on the page. I only figured it out by cross-checking three different pages.

I also want to flag: I don't actually know for certain that "leaflab-processor" IS what she means by "worker." There's no description field, no tooltip, nothing that says what the app does in plain English. I inferred it from the name and by elimination. I'd tell her that in my answer rather than pretend I'm 100% sure.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
Honestly I expected something like a search bar where I type "leaflab" and it shows me a status page — green/red, live or not. I figured I'd start on a homepage and there'd be an obvious "search" or a list of products/teams. The actual Dashboard was in the right spirit (a "what's live" summary) but it summarizes everything at once rather than letting me jump straight to one team's stuff.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Landed on Dashboard → saw "leaflab-leaflab" in a recent-promotions table but wasn't sure that was "the worker" → clicked "Apps" in the nav and it didn't visibly navigate the first couple tries, had to retry / use the URL directly → got to Apps Catalog, filtered by leaflab domain → found 4 leaflab apps, none named "worker" → searched "worker" globally, got matches from three *other* domains, no leaflab match → nearly got confused by a "demo-hello-worker" chart I stumbled on later that turned out to be unrelated sample data → guessed "leaflab-processor" was the closest thing to a worker by process of elimination → its detail page said it's live in prod at v0.0.6 → but the Apps Catalog table I'd just come from said "not promoted" for that exact app in prod, which made me second-guess myself → cross-checked the parent chart page (leaflab-leaflab) which confirmed prod is on v0.0.10 and that version does pin leaflab-processor v0.0.6 as "Adopted." That's where I stopped, fairly confident but not fully certain about the name mapping.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Mostly yes — I'd tell her "the closest thing to a 'worker' in leaflab is called leaflab-processor, and yes, it's live in prod, currently version v0.0.6, as part of the leaflab-leaflab chart promoted about 35 minutes ago." What's missing: (1) nothing confirms that "processor" is actually what anyone internally calls "the worker" — that's my guess, not a fact I could verify in the UI, and (2) I hit a real contradiction between two pages about whether it was "promoted" at all, which is a scary thing to have to reconcile on day one.

**4. Rate navigability 1-5.**
2.5. The page layout itself (top nav, breadcrumbs, tables) is clean and I could always tell where I "was" once a page loaded. But actual clicking around was unreliable — nav links sometimes didn't seem to do anything, and one search action landed me on a completely unrelated app's version-history page that I never clicked toward. That's disorienting for anyone, let alone someone who doesn't know if that's expected behavior or a bug.

**5. Rate usefulness/task-success 1-5.**
4. Once I found the right page, the actual promotion/version data was detailed and genuinely answered my question (version number, environment, how long ago, what chart pinned it). I just had to work harder than "self-explanatory" implied to get there, and I had to resolve a contradiction between two pages to trust the answer.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes, big one: the Apps Catalog table said leaflab-processor was "not promoted" in prod, while its own detail page showed it live in prod at v0.0.6. Those can't both be straightforwardly true to a normal reader — I eventually decided "not promoted" in that table must mean "never promoted *by itself*, only ever comes along for the ride inside a chart," but nothing on screen says that. Also tripped on jargon throughout: "chart's pin," "Adopted" vs "Observed" (no idea what these mean, never explained), "Via chart," "drift," "override." None of these are defined anywhere I found.

**7. What's one thing you'd change immediately if you owned this UI?**
Add a one-line description/purpose field to each app ("leaflab-processor — background job processor for leaflab") so people who don't already know the codebase can match a plain-English ask like "the worker service" to the actual registered name without guessing. Second choice: fix whatever "not promoted" means on the catalog table so it doesn't contradict the app's own detail page.

**8. What's something you thought was genuinely cool or delightful?**
The automatic drift warning banner on the Dashboard ("1 drifted override(s)...") — I liked that the tool proactively surfaced a problem instead of making me go hunting for it. Also liked that every version number came with a copy button and a direct link to "view artifact" — felt thorough, like nothing was hidden behind five more clicks once you're on the right page.

**9. Anything you expected to exist that wasn't there?**
A plain-language description of what each app does, and some kind of "also known as" / alias search so nicknames like "the worker" map to real names. Also expected a global search bar in the header, not just a search box buried inside the Apps Catalog page specifically.

**10. One sentence recommendation — would you point a teammate at this UI, or tell them to ask an engineer instead?**
I'd point a teammate here for a quick prod-status check, but I'd tell them to double-check anything that looks contradictory (like the "not promoted" label) with an engineer before repeating it in a meeting.

---


**Persona:** Morgan Ellis — Product Designer (non-technical, no engineering background)
**Task:** Open-ended exploratory design critique of "App Registry" — asked to poke around freely and assess whether its UI patterns are worth reusing for a different internal dashboard.

## Narrative

I started at localhost:8090 with zero brief beyond "poke around and tell us what you think," so I did what I always do with an unfamiliar tool: look at the shell first. Top nav, seven items — Environments, Deployments, Apps, Builds, Reconcile Runs, Drift & Audit, Trigger Release — plus a "developer" label in the corner where I'd expect a user menu. Immediately I squinted at the top-left: there's clearly a wordmark there ("App Registry," I confirmed later by inspecting it), but it's rendered so close in value to the black nav bar that it's functionally invisible. I actually thought the logo slot was empty for the first few minutes. I checked the computed color myself — the text is a near-black gray on a near-black bar. That's not a nitpick, that's a contrast failure a first-year design student would catch in a portfolio review.

The Dashboard itself is the best-looking screen in the product: a nice indigo-to-purple gradient hero, three stat tiles (Environments / Drifted overrides / Currently promoted), three environment health cards (dev/stage/prod, each with a "Healthy" or "Drift" pill), and a "Recent promotions" table. It reads top-to-bottom like an actual dashboard should. I liked that it says outright, in a footnote, "Dashboard is read-only by design — every action here links out to the Deployments screen that owns it." That's a rare and good instinct: telling the user why a page can't do a thing, instead of leaving them to discover it.

Deployments is where the real product lives — a matrix, one row per app/chart, one column per environment (dev/stage/prod), each cell showing version@digest, a copy button, a provenance badge, and two icon actions: a purple up-arrow to promote, a magenta circular-arrow to roll back. Rows are collapsible `<details>` elements — clicking a chart row (e.g. `app-registry-app-registry`) expands to show the individual container images that live under that chart. That drill-down is genuinely well thought out information architecture; it mirrors how I'd expect someone to actually think about "this chart bundles these four images."

Then the badges started fighting each other. "Adopted" is a solid orange/amber pill — meaning, I learned from the Drift & Audit page, "a human recorded this, CI didn't observe it." Fine, that's a real distinction worth surfacing. But then on the Trigger Release page, the exact same orange is the color of the "Run dry run" button, and the "Trigger release" button — the one that actually kicks off a real build — is also that same orange, just a shade more saturated. And on an empty promote screen, "nothing to promote" is shown in a big orange banner too. By my count that's four unrelated meanings sharing one color: a provenance label, a preview action, a live commit action, and an empty state. If orange means everything, it means nothing — I couldn't glance at a button and know if it was safe or consequential.

The promote flow itself, once I dug into it, is actually one of the better-designed sequences in the app. Promoting `demo-hello-fastapi` to prod required a reason (with a genuinely helpful caveat explaining that the *server* only requires a reason above dev, but the *UI* requires it everywhere — I appreciated that honesty), and "Promote for real" was disabled until I ran a dry run against the exact same form state. Change any field, the gate re-arms. That's a real safety pattern, not just decoration. The dry-run response did have a typo, though — "Would promote to**v1.0.1** @ sha256:..." — a missing space between "to" and the version number. And the "Cancel" button next to it is bright hot-pink, which is a strange color for "never mind, take me back" — it's the same color family as the rollback icon in the matrix, so for a half-second I read it as a destructive action rather than a dismiss.

I went ahead and actually promoted (the brief said it's safe to try), and the confirmation screen was genuinely satisfying: a green "Promotion recorded" banner, a clean two-block receipt — "Current recorded state of this target" and "This submission's event" — with promotion ID, actor, reason, and timestamps. That's exactly the kind of "yes, this worked, here's your paper trail" moment I want after taking a real action.

Then I hit the Environments page and the tone shifted entirely. The column headers aren't just labels — "Requires approval" carries an inline caveat reading "stored, not enforced — Promote does not check it," and "Allowed principals" carries "does not restrict who can promote — the promoter role check is the only access control in force." There's a full paragraph below the table repeating this in more detail, plus a reference to "PromotionState.PENDING_APPROVAL" — an enum name, not a concept a design reviewer or a new hire should need to parse. I respect the honesty (I'd rather a tool admit a field is decorative than let me assume it's enforced), but this is developer-note voice bleeding straight into end-user copy, and the table itself runs wider than the viewport — Actions, Promoter role, and GitOps path scroll off to the right with zero visual hint that there's more to see.

The Drift & Audit page has the same tell: its drift-count blurb references "(same underlying Matrix, FR-9)" — an internal requirement ID — and its footer says "recording a new adoption is screen S2, delivered separately." Those are ticket references, not user-facing sentences. Reconcile Runs has a bright cyan banner (a color used nowhere else in the app) explaining, at length, that a "stale" sweep rejection is "expected, correct behaviour, not a bug." All three pages read like someone pasted engineering Slack messages directly into the UI.

Builds was the least designed screen of the bunch — a flat table of CI run IDs, most literally formatted `#adopted:<uuid>`, with em-dashes standing in for empty Commit/Started columns. It's clearly the seed data leaking through, but even so, there's no pagination, sorting control, or filter beyond a raw "Workflow run id..." lookup box, and the noise (dozens of near-identical adopted rows) buries the two or three rows that look like real CI runs.

And then Trigger Release, structurally the most complex page in the app (a nested checkbox tree of every domain → app → chart), doesn't have the purple gradient hero banner every other page opens with. It's just a plain black "Trigger release" heading on a gray background. It's the one page that visually forgot it belongs to the same product.

One more small thing that shook my confidence, though I can't be 100% sure it's the app's fault rather than my own browser session hiccuping: early on, navigating straight to /environments briefly rendered content that looked like the Apps Catalog with a leftover search term already typed in, and a moment later I found myself looking at an entirely different "Promotion Details" screen I hadn't clicked into. It resolved itself on a fresh navigation, but for about ten seconds I genuinely didn't trust that the URL bar and the screen agreed with each other.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
From "App Registry" I expected either a package/artifact catalog (like an npm or Docker registry browser) or a service catalog (a directory of "here are our apps and who owns them"). I expected to land on a search or list view. I did not expect promotion/rollback controls to be the primary interaction — that only became clear once I saw the Deployments matrix, which honestly is a more useful thing than what the name implies.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → Environments (briefly saw what looked like the wrong page render, re-navigated) → Deployments (expanded a chart row to see its images) → Drift & Audit → Apps Catalog → Builds → Reconcile Runs → Trigger Release (tried submitting empty, got a validation message, then selected a chart and walked the resolve → draft flow but stopped short of actually kicking off a build) → back to Deployments to run an actual promote (dry run → promote for real → confirmation receipt) → rollback screen (hit an empty state, "nothing to roll back to," since I'd picked a target with no prior promotion). The one real backtrack was that early Environments/Promotion-Details confusion — I re-navigated to be sure I was looking at what I thought I was looking at.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
My "task" was a design critique, not a lookup, so in that sense yes — I got a full read on the system's patterns. If I reframe the question as "could I, as a non-engineer, tell what's currently live in prod for a given app," the answer is also yes, and fairly quickly, via Deployments. What I couldn't get anywhere was a plain-language glossary of terms like "Adopted," "drift," "rank," or "reconcile" — I had to piece those together by reading dense inline captions meant for someone who already knows the domain.

**4. Rate navigability 1-5.**
4. Seven top-level items, none redundant, and the "→" cross-links (Apps → Reconcile runs, Reconcile Runs → Reconcile runs, Dashboard → Deployments) form a sensible thread through the app. I lose a point for the Environments table running off-screen with no scroll affordance and for that one disorienting navigation moment.

**5. Rate usefulness/task-success 1-5.**
4. For someone who already knows the vocabulary (promote, drift, adopted, reconcile), this genuinely answers "what's live where" faster than I'd expect from an internal tool. The promote flow's dry-run gate and receipt page are the strongest evidence the team cared about getting the real task right, even if the surrounding copy needs an editor.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Two moments. First, that early render where I wasn't sure whether I was on Environments, Apps, or some stray Promotion Details page — for a few seconds the URL and the content didn't feel synced. Second, the "Adopted" badge: it's orange, which every design system I've worked in treats as "caution," but it just means "a person typed this in instead of CI recording it." I kept mentally flagging Adopted rows as "something's wrong here" until I read the Drift & Audit subtitle and realized it's neutral metadata, not a warning.

**7. What's one thing you'd change immediately if you owned this UI?**
Fix the logo contrast and then do a five-minute color audit: pick one color for "informational/neutral" (provenance, empty states) and a different one for "this button does something consequential," and stop reusing orange for both. Right now the palette is doing double and triple duty everywhere, and it's the single fastest thing to fix with the highest trust payoff.

**8. What's something you thought was genuinely cool or delightful?**
The promote-to-prod safety gate — reason required, dry run mandatory before "Promote for real" unlocks, any field change re-arms it — paired with the confirmation receipt (promotion ID, actor, reason, timestamps, in two clearly separated blocks). That's a pattern I'd steal outright for our new dashboard's higher-stakes actions.

**9. Anything you expected to exist that wasn't there?**
A glossary or an "?" info affordance for the recurring jargon (Adopted, drift, rank, reconcile, watermark) — instead that information is smeared across page subtitles and footnotes in inconsistent depth. Also no visible pagination or filtering on Builds, which felt like the one screen that would fall over first under real production volume. And no legend anywhere explaining what the badge colors mean as a system.

**10. One sentence recommendation — would you point a teammate at this UI, or tell them to ask an engineer instead?**
I'd point a teammate here for "what's promoted where right now" (Deployments answers that fast and well), but for anything involving "why," "is this safe," or "what does this field actually do," I'd tell them to loop in an engineer, because the UI's honesty about its own gaps is admirable but reads like internal documentation, not a finished product.

---


**Persona:** Devon Park — Senior on-call SRE, expert
**Task:** 3am page says "friendly-computing-machine-bot-services is erroring in prod, is this a bad deploy?" — use App Registry to find out fast, before falling back to ArgoCD/kubectl.

## Narrative

06:xx, phone's buzzing, I'm annoyed. Paging says friendly-computing-machine-bot-services is erroring in prod. First question: did somebody just ship something bad? I pull up the App Registry dashboard at localhost:8090 because that's supposedly the "source of truth for what should be where."

Landing page is actually decent — it doesn't make me hunt. There's a yellow banner right at the top: "1 drifted override(s) — an overridden image no longer matches its chart's pin," plus a recent-promotions table. My eyes go straight to it. Good, drift banner is exactly what a 3am brain wants. But the drifted row turns out to be manmanv2-control-api/control-services, not my app. Ruled out in about 10 seconds — that's the tool doing its job.

Now I need my actual app. I go to Apps, filter/search for "friendly-computing-machine," find the chart (friendly-computing-machine-bot-services) and click in. This is where it goes sideways. The page for the chart throws a console error — a stylesheet request for "tailwindcss" 404s with a MIME-type mismatch — and the resulting page renders completely unstyled. It's still readable text so I push on: prod is on v0.1.2, dev and stage are on v0.1.3. Prod is trailing, which the org apparently considers normal (two release generations). But then I notice the timestamps: dev/stage got v0.1.3 about 34 minutes ago, and prod got v0.1.2 only ~20 minutes ago — that's *after* the newer version was already live elsewhere. That's not "prod hasn't caught up yet," that reads like somebody deliberately pushed the older build to prod recently. Possibly a rollback. The tool never says the word "rollback" anywhere on this page, though — I have to do the math on two timestamps myself, under a page with no CSS, at 3am. Not great.

Then I try to find out *why* — was this an intentional rollback because v0.1.3 broke something? I look for a history/audit trail on the chart page. Apps get a "Version history" link; this chart doesn't. I try clicking around, and this is where things get genuinely bad: clicking what looked like an expandable row on the Deployments matrix flung me to a totally different page (Apps catalog), and immediately after that, to a raw unstyled white screen with nothing but one line of backend stack trace — "Failed to load version history for app-registry-app-registry from app-registry-api: GetApp(...): rpc error: code = NotFound" — no header, no nav, no way back except manually retyping a URL. Then a direct nav to /builds silently rendered the Environments page's content under the Builds URL for a beat before I forced a second navigation. I do not trust any single click on this app right now — I've now seen three separate instances where the visible content didn't match the URL/nav item I clicked, unprompted, with no error toast or indication anything went wrong. If I were doing this for real and not re-navigating defensively, I could easily have acted on stale data without knowing it.

I check the Builds page hoping for "what shipped recently for this app" — it's just a flat, unfiltered wall of `#adopted:<uuid>` rows, all timestamped "37 minutes ago," all actor "dev-user," most with blank commit hashes. No app-name column, no filter for it. The only search box wants an exact run ID I don't have. Dead end.

I check Environments — confirms the tool has zero live health/cluster signal, by design (it says so explicitly: GitOps path, promoter roles, no health check). Fine, that's expected — ArgoCD's job, not this tool's. But it means the actual "is prod erroring" half of my question was never answerable here regardless.

Bottom line after ~5 minutes of real clicking: I have a plausible read (prod got rolled back to v0.1.2 after v0.1.3 landed elsewhere, both entries are human-"Adopted" not CI-"Observed" so nobody's even sure a pipeline validated them) but no confirmation, no rollback reason/audit note, and I burned real time fighting a broken stylesheet and a router that occasionally shows me the wrong page. I'm now going straight to `kubectl get pods -n prod` and ArgoCD.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a "what's live where, and did it just change" board — basically a promotion status page with a search bar, plus pod/rollout health baked in or linked. I expected to start at a dashboard or a search box where I type the app name and immediately see current-vs-previous version per environment. That's roughly what I got for the dashboard, so the starting point was right.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → saw drift banner (not my app, ruled out fast) → Apps catalog → searched/filtered to the chart → chart detail page (unstyled, console MIME error) → read prod=v0.1.2 vs dev/stage=v0.1.3 and noticed prod's timestamp was *newer* than dev/stage's, implying a deliberate rollback → tried to find rollback reasoning/history, chart pages have no "Version history" link (unlike apps) → clicked what looked like an expandable matrix row, got teleported to Apps catalog, then to a raw backend-error blank page for a completely different app → had to manually re-navigate several times → checked Builds for CI context, found an unsearchable wall of UUIDs with no app column → checked Environments, confirmed no health data exists in this tool at all. Backtracked at least three times because a click or nav landed me on a page I didn't ask for.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Partially. I got a strong circumstantial signal (prod is one version behind, and that version was (re-)promoted *after* the newer one already shipped elsewhere, which smells like a rollback), but nothing in the UI confirms it was a rollback, states a reason, or tells me whether prod is currently actually healthy. The tool answers "what's promoted" but not "was this deliberate" or "is it broken right now" — and I had to infer the interesting part from comparing two timestamps by hand.

**4. Rate navigability 1-5.**
2. The nav bar itself is fine and the IA (Environments/Deployments/Apps/Builds/Reconcile Runs/Drift & Audit) makes sense on paper. But I hit three separate cases where clicking a link or typing a URL did not render what it claimed to — silently. That's disqualifying for anything time-sensitive.

**5. Rate usefulness/task-success 1-5.**
2. The dashboard's drift banner and the per-env version breakdown on the chart page are genuinely the right shape of answer. But between the broken stylesheet, the missing history/reasoning for the chart's rollback-looking event, and a Builds page that's unsearchable by app, I couldn't close the loop. I'd have gotten a faster, more trustworthy answer from `kubectl` and ArgoCD directly.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Constantly, after the second unexpected page-swap. Once I saw a click on "Deployments" land me on an Apps catalog page with no error, I stopped trusting that any given screen was actually the one I asked for — I started re-navigating by full URL and re-snapshotting every time, which is not something you can do calmly at 3am. Also "Adopted" vs "Observed" as provenance labels took a second read to parse (human-entered vs CI-recorded) — not bad jargon, just not instantly obvious under pressure, and it mattered here since both of this app's live versions were human-"Adopted," meaning I can't even lean on "CI verified it."

**7. What's one thing you'd change immediately if you owned this UI?**
Fix the stylesheet 404 (relative path breaking on nested routes) — it's clearly the root cause of the layout falling apart and probably why clicks are landing on the wrong elements. Right after that: put a visible, explicit "rolled back from vX to vY by <actor> at <time>, reason: ..." event on the promotion/chart timeline instead of making me diff two timestamps to guess it happened.

**8. What's something you thought was genuinely cool or delightful?**
The dashboard's drift banner + per-app drift badges on the Deployments matrix. Being able to rule out "is this app drifted" in ten seconds, for free, without navigating anywhere, is exactly the kind of thing that's useful during an incident. If the rest of the app were as solid as that one feature, this would be a good tool.

**9. Anything you expected to exist that wasn't there?**
A rollback/promotion audit log with a human-readable reason and actor, visible right on the chart/app page. Also a way to search Builds by app name instead of only by exact run ID. And, even just a link out to the app's ArgoCD Application or a "view in ArgoCD" button — since this tool explicitly says it's not the deploy engine, it should hand me off to the thing that is.

**10. One sentence recommendation — would you point a teammate at this UI, or tell them to ask an engineer instead?**
Check the dashboard's drift banner here first since it's fast and reliable, but for anything beyond "is it drifted," go straight to ArgoCD/kubectl — this UI currently costs you more time than it saves once you're past the landing page.

---


**Persona:** Nadia Osei — Compliance/audit specialist, semi-technical
**Task:** Produce a handoff-ready record of every production promotion from the last "quarter" — who did it, when, why, and what changed — for an external auditor.

## Narrative

I started at `localhost:8090` expecting something like a change-management log — a list of "who deployed what to prod and why," maybe with a date filter and an export button, the way any compliance tool I've used before works. Instead I landed on a "Dashboard" that's clearly built for engineers: environment health tiles, a "1 drifted override(s)" warning banner, and a "Recent promotions" table that only shows the last ten events across *all* environments mixed together, with a note at the bottom that the dashboard is "read-only by design — every action here links out to the Deployments screen that owns it." Fine — not what I wanted, but I followed the trail.

**Deployments** turned out to be a live matrix: one row per app/chart, one column per dev/stage/prod, showing the currently-promoted version with little promote (⬆) and rollback (↺) icons. This is clearly the "what's live right now" view, not a history log. There's an "As of (UTC)" field at the top that I initially got excited about — I assumed it would let me time-travel back through the quarter — but I never got a chance to test it properly because clicking around the nav got flaky on me (more on that below).

I went hunting for anything literally called "audit" and found **Drift & Audit**. This looked promising — it has a whole section called "Adopted artifacts" with Owner/Kind/Version/Digest/Adopted-timestamp columns. But right under the section heading it says, in plain text: *"Recorded by a human via AdoptArtifact, not observed by CI. Reason is logged server-side; cross-reference server logs for the full 'why'."* That stopped me cold. The one screen in this whole app with "Audit" in its name is explicitly telling me the "why" isn't here — I'd need server log access, which I don't have and wasn't told exists as an option anywhere in the UI. Also: no "who" column here either, just "Adopted" timestamps.

I eventually found real per-promotion detail by opening individual apps from the Apps Catalog and reading their "Promotion history" section. For `manmanv2-host-manager` I found actual promote/rollback events with a "Requested by," "Requested at," "Action," and "Reason" — exactly the fields I need. Great, I thought, I can build my table this way, app by app.

Then I hit two walls:
1. Every single record I opened said "Requested by: **developer (you)**" and the identical boilerplate reason: *"seed_tilt_walkthrough.py: populate local registry for a UI walkthrough."* Not one entry had a different name, a ticket number, or an actual business reason. If this were real data I'd have to tell the auditor "one person did everything, for the same undocumented reason, every time" — which would fail the audit on its own.
2. When I checked the Apps Catalog for everything currently live in prod, four of the six prod-promoted things are **charts** (`app-registry-app-registry`, `friendly-computing-machine-bot-services`, `leaflab-leaflab`, `manmanv2-control-services`), not "apps." Chart detail pages have *no* Promotion History section at all — just a relative "promoted 36 minutes ago" with no actor, no reason, no link to a record. I even tried guessing a `/history` URL for one and got a raw 502 error with a gRPC "NotFound" message leaking straight into the page. So for two-thirds of what's actually running in production, there is no who/why trail exposed anywhere in this UI.

I looked everywhere for an export: no CSV button, no "Export," no print-friendly view, no "Download report" link on Deployments, Apps, Drift & Audit, or Builds. The closest thing to a workaround is manually opening each promoted entity's page, one at a time, and copy-pasting the "Promotion history" list text into a spreadsheet by hand — and that only works for the image-type entities; the chart-type ones simply have nothing to copy.

Along the way navigation itself felt unreliable — clicking the left-nav "Drift & Audit" link once dropped me on an unrelated chart detail page, and clicking into a different link once landed me on the Builds page instead. I couldn't tell if I'd mis-clicked or the app itself hiccuped, but for a compliance tool where I need to trust that I'm looking at the record I asked for, that's unsettling on its own.

One more thing I noticed on **Environments**: the "Requires approval" column has a footnote saying it's *"stored, not enforced — Promote does not check it,"* and "Allowed principals" is *"stored and read by nothing... does not restrict who can promote."* So even the guardrails that exist in the data model aren't actually wired up. For an auditor that's a real finding in itself, separate from my task.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a change log or release history tool — something like a ServiceNow change record or a deploy-log viewer, where I'd land on a filterable list of promotion events with date range, environment, and actor filters, and an obvious "Export" button. I expected to start on something literally called "History" or "Audit Log."

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → Deployments (current-state matrix, not history) → Drift & Audit (told me the "why" lives in server logs I don't have) → individual App pages via the Apps Catalog, reading each one's "Promotion history" list by hand → tried a chart's page and found no history section at all → guessed a `/history` URL for a chart and got a raw 502/gRPC error → checked Environments almost as an afterthought and found the approval/access-control fields are unenforced. I backtracked constantly because nothing aggregates across apps — I had to visit each promoted entity individually, and even then, four of six prod entries had nothing to show.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
No, not fully. I could hand the auditor a partial table for the one or two apps that are directly image-promoted (with real requester/timestamp/action/reason fields), but for the majority of what's actually live in prod — the chart-based apps — there is no who/when/why visible anywhere in the UI. And what data I could get all pointed to one generic actor and one boilerplate reason, which wouldn't survive audit scrutiny even if it were complete.

**4. Rate navigability 1-5.**
2. The top nav is simple and the labels are mostly sensible, but there's no breadcrumb path to "all promotion history," everything requires drilling into individual entities, and I had at least two clicks land me on completely unrelated pages.

**5. Rate usefulness/task-success 1-5.**
2. I could not produce a complete, trustworthy, handoff-ready record. I got fragments for a minority of apps and hard walls (no data, a 502 error, "check server logs") for the rest.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes, several. "Adopted" vs "Promoted" vs "Override" vs "Via chart" all appear as status badges without a glossary, and I was never fully sure which of those means "this is what's actually running." The promotion detail pages all showed "Committed: Not yet committed" and "Sync triggered at: Not yet triggered" — so is the promotion I'm looking at even live? The Environments page footnote admitting the approval gate isn't enforced also made me trust the whole system less — if the safety rails in the data aren't real, I can't assume anything else claimed by the UI is enforced either.

**7. What's one thing you'd change immediately if you owned this UI?**
Add one screen: a flat, filterable, exportable (CSV at minimum) log of every promotion event across every app and chart, environment, and date range, with requester/reason columns front and center — and make sure chart-type promotions get logged there too, not just direct image promotions.

**8. What's something you thought was genuinely cool or delightful?**
The digest copy-to-clipboard buttons (⧉) next to every version/digest pairing were a nice touch, and the per-app "Current artifact by environment" table that shows dev/stage/prod side by side was genuinely clear and easy to read once I found it.

**9. Anything you expected to exist that wasn't there?**
An audit/change log with a date range picker, an export button (CSV/PDF/print view), a real named-user login (everything just says "developer (you)"), and promotion history for chart-type entities, not just image-type ones.

**10. One sentence recommendation — would you point a teammate at this UI, or tell them to ask an engineer instead?**
Ask an engineer — and tell them to check the underlying database or server logs directly, because this UI can't currently produce a complete promotion record on its own.

---


**Persona:** Owen Bryant — Platform Engineering Team Lead
**Task:** Evaluate, using only the App Registry UI, whether the tool is general enough to onboard a brand-new, unrelated domain: what onboarding would take, whether environments/promotability/provenance are domain-agnostic or hardcoded, and whether any in-app docs/help explain onboarding.

## Narrative

I opened App Registry expecting something like a lightweight CMDB for deploy state — a table of "this app, this environment, this version" that some other team could plug their own app names into. First impression from the Dashboard: clean, read-only, and it says so explicitly ("Dashboard is read-only by design — every action here links out to the Deployments screen that owns it"). Good — that's the kind of self-aware label I want to see in a tool I'm about to recommend to another team.

I went hunting for the onboarding path along three fronts: an "Environments" concept, an "Apps"/"domain" concept, and any settings/admin/help surface.

**Environments** turned out to be the best-designed part of the tool for my purposes. There's a real `+ Add environment` form with Key, Display name, Rank, GitOps path, Allowed principals, and a Requires-approval checkbox. Crucially, the copy on that page is brutally honest about what's real: a big inline warning says "Requires approval is stored but not yet enforced — Promote does not check it today," and another says "Allowed principals... does not restrict who can promote — the app-registry-promoter-<key> realm role check... is the only access control in force." That's the opposite of the usual SaaS trick of implying more governance than exists — I respect it, and it directly tells me the real gate is a Keycloak realm role named `app-registry-promoter-<env-key>`, created and assigned outside this tool entirely. So: environments are genuinely domain-agnostic — dev/stage/prod are just three rows, shared globally, not owned by any one app. Good sign for "general enough."

Where it got murkier was **apps and domains**. The Apps Catalog has a "domain" filter dropdown pre-populated with app-registry, friendly-computing-machine, leaflab, manman, manmanv2, tools — but there is no "+ Add app" or "+ Add domain" button anywhere on that page, and no domain-management screen at all. A "domain" appears to be nothing more than a string prefix convention on app names, not a first-class entity you create.

So how do apps actually get into the system? I found the answer on the **Reconcile Runs** page, almost by accident: "The identity pipeline — ReconcileApps from ci.yml, on every push to main." That's the real answer to (a): a brand-new domain isn't onboarded by clicking anything in this UI — it's onboarded by getting your CI pipeline to run the same reconciliation job against your own chart/app definitions, presumably against some catalog file this repo already has that I never got to see (UI-only constraint bit me here). Builds page corroborated this — CI-recorded builds plus a bunch of "adopted:<uuid>" entries authored by "dev-user," which look like a one-time bulk-import/backfill mechanism for pre-existing deploy history rather than a repeatable onboarding flow a new team could self-serve.

The Trigger Release page has a nice full domain/app/chart checkbox tree, but again it only reflects apps that already exist — no way to add a new leaf from there either.

I looked everywhere for docs: no "?" icon, no "Help," no "Docs," no footer links, nothing behind the "developer" text in the top-right (that's just a role label, not a menu — I tried). Zero in-app onboarding documentation. If someone handed me this tool and said "onboard your new domain," I would have to go find a README or ask someone — the UI itself has no answer to (c).

One process note, not really about the product itself: navigation felt unusually twitchy during this session — deep links to `/environments`, `/environments/new`, and `/apps/<name>` sometimes landed correctly and sometimes bounced me to Dashboard, Deployments, Builds, or even a live Promote screen I hadn't touched, and once the page auto-navigated to a `/promote?...` URL after several seconds of me doing nothing at all. I also caught one console error where a nested route (`/apps/app-registry-api`) requested its stylesheet from a relative path (`/apps/tailwindcss`) instead of the site root, which the browser correctly refused as a bad MIME type — meaning a hard refresh two levels deep in the URL can silently load unstyled. I can't be 100% sure how much of the jumpiness was the app itself versus this being a shared local demo environment with other activity on it, but from where I sat, it read as instability, and that colored my trust in everything else I clicked.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a promotion-tracking / deploy-state ledger — "app X, version Y, in environment Z, promoted by whom, when." I expected to start on some kind of dashboard or app list, and I expected an obvious "register a new app" or "new project" action somewhere near the top, the way most internal catalog tools put creation front-and-center.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → Apps Catalog (looking for an add button — none) → Environments (found a real, well-documented admin CRUD screen with an Add Environment form) → tried to open an app's detail page and to reach /environments/new directly by URL, and got bounced to unrelated pages more than once before landing where I meant to → Builds → Reconcile Runs, where I finally found the sentence that explained how apps actually get created (CI's `ReconcileApps` job, not a UI form) → Trigger Release, to confirm the domain/app tree is read-only from this screen too → searched the header/footer for any help or docs link and found none.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Partially. I got a clear, satisfying answer that *environments* are domain-agnostic and self-service. I got a much less satisfying, indirectly-discovered answer that *apps/domains are not self-service at all* — they come from a CI reconciliation job pointed at some repo-side manifest I never got to see because I was UI-only. The UI never states this outright; I had to infer it from a stray sentence on the Reconcile Runs page. Nothing in-app tells you what the CI side actually needs to look like, so I could not fully answer "what would it take" — I'd have to go find whoever owns that CI job.

**4. Rate navigability 1-5.**
2. The information architecture itself (Environments / Deployments / Apps / Builds / Reconcile Runs / Drift & Audit / Trigger Release) is sensible once you know what each means, but I hit repeated, disorienting instances of landing on a page other than the one I navigated to — including once with no click at all. Even discounting that as possibly an artifact of a shared test instance, a nested-route CSS load bug that leaves a page unstyled on refresh is a real, findable issue.

**5. Rate usefulness/task-success 1-5.**
3. I did walk away with a real answer for my boss: "environments are generic, apps are not — onboarding is a CI/backend exercise, not a UI one." That's a usable conclusion. But it took real digging and inference rather than the tool telling me directly, and I never saw the actual mechanism (the manifest format ReconcileApps consumes), so I can't yet size the onboarding effort.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Two moments, in opposite directions. First, a moment of *increased* trust: the Environments page's blunt admissions ("stored but not enforced," "does not restrict who can promote") are exactly the kind of honesty I wish more internal tools had — I trust a tool more, not less, for telling me where its governance is fake. Second, a moment of *decreased* trust: "Adopted" as a badge on a version cell isn't explained anywhere I found — I inferred from context (and the "adopted:<uuid>" build IDs) that it means "backfilled/hand-entered provenance" as opposed to CI-observed, but a first-time viewer with less context than me could easily misread "Adopted" as some kind of approval status.

**7. What's one thing you'd change immediately if you owned this UI?**
Put a one-paragraph "How an app/domain gets registered" explainer directly on the Apps Catalog page — the same way the Environments page explains its own fields — instead of making me discover the CI-driven reconciliation mechanism by reading a stray sentence on an unrelated Reconcile Runs page.

**8. What's something you thought was genuinely cool or delightful?**
The self-critical inline copy on the Environments and Add Environment forms — telling me exactly which fields are decorative versus load-bearing, and naming the exact Keycloak realm role that actually gates a promotion. That level of "here's what's real" honesty is rare and I'd hold it up as a model for the rest of the app.

**9. Anything you expected to exist that wasn't there?**
An "Apps"/"Domains" admin screen mirroring the Environments one — with an explicit "+ Add app" or "+ Add domain" action, or at minimum a page documenting the manifest/CI contract a new domain needs to satisfy. Also any help/docs entry point at all — there is none in the chrome.

**10. One sentence recommendation — would you recommend your org adopt this tool as-is, or would you tell them to build/use something else?**
The environment/promotion model is genuinely generic and worth adopting, but I would not tell the other org to onboard yet — I'd first get someone to hand them the CI reconciliation contract (or get that turned into a real, documented, UI-driven "register a domain" flow) since right now that's a black box you have to go ask a person about.

---


**Persona:** Ravi Deshmukh — Automation Engineer (SRE/platform tooling background)
**Task:** Figure out, purely by exploring the App Registry UI (and its network traffic), whether there's a documented API, webhook, or notification mechanism to build a Slack bot against (post on every prod promotion, and/or auto-promote low-risk apps on a schedule) — or whether a CLI/gRPC client needs to be found separately.

## Narrative

I started at `localhost:8090` expecting either (a) a dashboard with a sidebar link to "API" or "Integrations," or (b) at minimum a JS single-page app throwing JSON around that I could reverse-engineer from the Network tab. What I got was a clean, server-rendered Dashboard: three environment tiles (dev/stage/prod), a "1 drifted override" warning banner, and a "Recent promotions" table. Nice looking, DaisyUI-styled, immediately legible. Bottom of the page: "Dashboard is read-only by design — every action here links out to the Deployments screen that owns it." Good sign for a human. Not yet a sign either way for me.

First thing I always do on an unfamiliar internal tool: check what's actually loading. I pulled the network log. Four third-party scripts: `htmx.org@1.9.10`, `alpinejs@3.x`, `@tailwindcss/browser`, and `daisyui.css`. No custom `app.js`, no `/api/*` XHR calls, nothing fetch-shaped at all on page load. That's the tell — this is classic htmx: the server renders full HTML (or HTML fragments) and the browser just swaps DOM. There is no JSON contract sitting behind these pages that I could point `curl` or a bot at and expect a clean response; I'd get back HTML, styled for a screen.

I went hunting the nav for anything resembling my task: `Environments`, `Deployments`, `Apps`, `Builds`, `Reconcile Runs`, `Drift & Audit`, `Trigger Release`. No `API`, no `Integrations`, no `Webhooks`, no `Settings`. Top right of the navbar just says "developer" in plain text — I assumed that was a user menu or account dropdown with maybe an API-token page under it. I checked the DOM directly: it's a static `<span>`, not a link, not a button, no dropdown. That's just telling me who I'm logged in as (or what role I'm wearing). Dead end.

I poked at the promote flow next, partly because it's the thing I'd actually need to script, and partly to see what shape the underlying action takes. Clicking the "⬆" promote arrow on the Deployments matrix doesn't fire a mutation — it navigates to `/promote?env=stage&kind=chart&owner=<name>`, a GET, which renders a confirmation form: pick a version from a dropdown, type a required "Reason," then a two-stage button pair — "Run dry run" (enabled) and "Promote for real" (disabled, and it says right there in the UI: disabled until a dry run has been run against exactly the form state below; changing any field re-arms the requirement). That's useful and slightly annoying at the same time: useful because it tells me the real backend enforces a dry-run-before-apply discipline I'd have to replicate in any automation, not just fire-and-forget a POST; annoying because there's no visible raw endpoint I can just hit — this is clearly built to be driven by a human clicking buttons in sequence, with client-side state gating the second button.

While poking around the Environments page (checking whether "requires approval" gating might block scripted low-risk auto-promotion), I found something genuinely important buried in a column-header tooltip: promotion is access-controlled via Keycloak realm roles named `app-registry-promoter-<env>` (e.g. `app-registry-promoter-prod`) — and, refreshingly honest, the table admits right there that "Requires approval" is a stored field the backend doesn't actually check yet, and "Allowed principals" is stored but read by nothing — the promoter role is the only real gate. That's a real finding for my task: if I build a bot, it needs a Keycloak service account carrying `app-registry-promoter-prod` (or whichever env), not some app-registry-specific API key.

I tried the obvious guesses directly in the address bar — `/api`, `/docs` — both quietly returned HTTP 200 and rendered... the Dashboard again. No 404, no "not found," no redirect banner. It just silently falls back to the home page content, which is a little disorienting: you can't tell the difference between "this route doesn't exist" and "you got sent home for some other reason." I ran a full-text scan of the rendered HTML on several pages (Dashboard, Trigger Release, Reconcile Runs) for "webhook," "API," "swagger," "openapi," "grpc," "integration," "notification," "slack," "token" — zero hits, every single time.

I checked Builds (CI run recording) and Drift & Audit hoping for something like an events feed I could poll — "what got promoted to prod in the last hour" is exactly the query my Slack bot needs. Both are real, useful, honest audit surfaces (adopted-vs-CI-observed provenance, drift between chart pin and override, promotion history) — but they're plain HTML tables with a manual "Filter"/"Look up" text box, no timestamp-range query params I could see reflected in the URL, no pagination cursor, no RSS/webhook/SSE. It's built for a human scanning a table, not a bot polling a feed.

One rough patch worth flagging honestly: navigation got flaky partway through my session. A couple of times, right after navigating to one URL (e.g., a specific build detail page), the very next snapshot showed me on an entirely different page (once I ended up on an app detail page I hadn't clicked into). I couldn't pin down why — possibly a stale reference in my tooling, possibly something about how the app handles back-to-back requests — but it did make me trust the screen less for a minute, since URL bar and rendered content briefly disagreed.

Net: I found real, useful information (tech stack, auth model, promote's dry-run discipline, the honest "not enforced yet" caveats), but not the thing I came for. There is no documented REST API, no webhook config screen, no notification settings, and no "Developer"/"Integrations" section anywhere in this UI. Given the brief told me this is gRPC-backed underneath, that tracks — the web UI is a thin, human-facing client on top of something I'd need to go find separately (proto defs, a CLI, or direct gRPC access), armed with a Keycloak service account holding the right `app-registry-promoter-*` role.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a promotion/release dashboard, and I expected to start by scanning the nav bar for something like "API," "Integrations," or a gear icon — that's usually where internal tools stash service-account/webhook config. Failing that, I expected to open the Network tab immediately and watch for JSON calls I could imitate with `curl`.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Landed on the Dashboard, checked the network log (four static libraries, no XHR — htmx tell). Scanned the nav for API/Integrations — not there. Clicked "developer" in the top right expecting an account menu — it's inert plain text, not a link, which stopped me cold for a second since it's the one thing in the header that looks like it should be interactive. Opened a Promote link to see the mutation shape — turned out to be a GET-rendered confirmation form with a two-stage dry-run/apply button pair, not a raw endpoint. Found the Keycloak `app-registry-promoter-<env>` role mentioned almost as an aside in an Environments column tooltip — that was the single most useful thing I found all session, and I nearly missed it because it wasn't where I was looking. Tried guessing `/api` and `/docs` directly — both silently served the Dashboard with a 200, no signal either way, which was its own small backtrack (had to double check via the network log that it really was just the Dashboard, not a real API index page). Checked Builds and Drift & Audit hoping for a pollable event log — got audit tables, not a feed. Hit some page-navigation flakiness near the end that cost me a couple of re-checks to make sure I trusted what I was looking at.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Partially. I got a confident "no" — there's no REST API, webhook config, or notification settings surfaced in this UI — which is itself an answer, just not the one I wanted. What's missing: any pointer to where the real interface lives (a proto file, a CLI doc, a "for programmatic access see X" note). The UI never tells you it's gRPC-backed or that a separate client exists; you only know that if someone tells you outside the tool.

**4. Rate navigability 1-5.**
4. For a human doing promotions and checking drift, the nav is tight and well-labeled. It loses a point only because there's zero signal, anywhere, pointing an integrator toward the "real" interface.

**5. Rate usefulness/task-success 1-5.**
2. I didn't find what I came for, but the session wasn't wasted — I now know the auth model (Keycloak promoter roles), the promote flow's dry-run contract, and that I should stop looking here and go find the gRPC/CLI story instead. That's useful downstream information, just not a working integration path.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Two moments. First, the promote form's copy — "disabled until a dry run has been run against exactly the form state below" — took a re-read to parse; it's precise but dense. Second, and more concerning as an automation engineer: the Environments table cheerfully tells you "Requires approval" and "Allowed principals" are stored fields that the backend doesn't actually enforce. That's honest, which I respect, but it means those UI affordances are decorative — if I'd trusted the screen at face value without reading the fine print, I'd have shipped an auto-promoter believing an approval gate existed when it doesn't.

**7. What's one thing you'd change immediately if you owned this UI?**
Add one link, anywhere in the header or footer: "Programmatic access / API reference." Even if it just points to a proto file or a README, it turns a 20-minute dead-end investigation into a 10-second click.

**8. What's something you thought was genuinely cool or delightful?**
The brutally honest inline caveats — "stored, not enforced," "does not restrict who can promote," "Not a cluster check," "No adopt control lives here." Most internal tools let stale/aspirational UI copy imply guarantees that don't exist. This one keeps correcting itself in place, which is exactly what I want from a system of record before I build automation against its stated behavior.

**9. Anything you expected to exist that wasn't there?**
An API-key or service-account management screen, a webhook/notification config panel, an OpenAPI/proto schema link, and any kind of pollable events feed (even just `?since=<timestamp>` on the promotion history) for the Slack bot's core use case.

**10. One sentence recommendation — would you point a teammate at this UI, or go straight for something else to automate against?**
Point them here first only to learn the auth model (Keycloak `app-registry-promoter-<env>` roles) and the promote-flow semantics (dry-run required), then go straight to the gRPC client/proto definitions or CLI — this web UI is not, and doesn't claim to be, an integration surface.

---


**Persona:** Ellis Moreau — Accessibility/QA specialist, semi-technical (evaluates for assistive-tech users, not a screen-reader user personally)
**Task:** Keyboard-only journey from the Dashboard, through the Deployments matrix, to a promote form, and as far as possible toward submitting it — never using a mouse click.

## Narrative

I landed on `localhost:8090` and the Dashboard rendered cleanly: a heading, a drift-warning banner, three environment tiles, a "Recent promotions" table. Good first impression — there's a clear `<main>` landmark and a real `<h1>`. Before touching anything else, I checked for a skip-to-content link, since that's the first thing a keyboard user wants on a page with a persistent top nav. There isn't one — the very first Tab stop is the "App Registry" logo/home link, then all seven nav items, then whatever is in the page body. On a page this shallow it's not fatal, but it's worth flagging on principle.

Second thing I checked: is the nav actually a `<nav>` landmark? It isn't — it's a plain `<ul>` of links with no `role="navigation"` and no `<header>`/banner wrapper either. A screen-reader user browsing by landmark won't find "navigation" in their landmarks list; they'll have to fall back to link-by-link browsing.

Then I hit the first real surprise. I tabbed onto "Deployments" and it had a focus ring — logo did too — so I assumed the rest of the primary nav would be consistent. It isn't. I focused each of the seven nav links directly and checked computed styles: the logo link gets `outline: solid 2px`, but every single item in the actual menu — Environments, Deployments, Apps, Builds, Reconcile Runs, Drift & Audit, Trigger Release — comes back `outline: none` and `box-shadow: none`. I took a screenshot with "Deployments" programmatically focused to be sure this wasn't a rendering artifact of my testing method: the screenshot confirms it — the link looks pixel-identical to its unfocused neighbors. If I were tabbing through this nav for real, with no other cue, I would have no idea which item I was on. This is a real gap, because the close button, the version dropdown, the reason field, and all three action buttons on the Promote form all get a clean, visible 2px focus ring — so the codebase clearly *has* a focus-style convention, it's just been reset or never applied for the nav `<a>` elements specifically.

I navigated into Deployments to find the promote form. The matrix itself reads reasonably well — one row per app/chart, one column per env, each cell showing version, an "Adopted"/drift badge, and icon actions. But two things stood out immediately. First, several rows (the "chart"-kind entities like `app-registry-app-registry`, `leaflab-leaflab`, `manmanv2-control-services`) are wrapped in a big block with `cursor: pointer` styling that clearly wants to be clicked — presumably to open a composition/drift detail view. I checked it in the DOM: it's a plain `<div>` with `tabindex="-1"` and no ARIA role. That's explicitly *excluded* from the tab order. There is no way to reach that row-level detail affordance with a keyboard at all — not even a stray Tab stop with a bad label, it's simply not there. Whatever that click does, it's mouse-only.

Second: the promote (⬆), rollback (↺), and copy-hash (⧉) controls are real `<a>`/`<button>` elements and ARE in the tab order — good — but their accessible name is literally just the glyph character itself. No "Promote to dev," no "Rollback demo-hello-fastapi in stage." In a matrix with a dozen rows × three envs, that's three dozen links that would all announce as "up arrow, link" to a screen reader with zero indication of which app or environment they belong to.

I opened a promote form directly (`/promote?env=dev&kind=chart&owner=app-registry-app-registry`) to check the form itself. It rendered: "Promote app-registry-app-registry → dev," a Version dropdown, a Reason field marked required, and Cancel / Run dry run / Promote for real buttons. I checked for `<label>` elements in the DOM — there are none, anywhere on the page. The Version `<select>` has no accessible name whatsoever (no label, no aria-label, no aria-labelledby) — a screen reader would announce it as an unlabeled combobox and read out the option text as its only clue. The Reason `<textarea>` fares slightly better only because its placeholder text ("e.g. verify sensor batch fix ahead of chart's next cut") gets used as a fallback accessible name by the browser — but that's a well-known anti-pattern, since placeholder text vanishes the moment you start typing and isn't a substitute for a real associated label.

I then did the real test: fresh page load, Tab from the top, through all nav links, past the close (✕), landed cleanly on the Version select, tabbed into the Reason textarea, typed "ver" character-by-character via the keyboard, tabbed past Cancel onto "Run dry run," and pressed Enter. It worked — the tab order through the form itself is completely sane and every field/button in it does show a focus ring. The dry-run result came back correctly in what reads as an `alert` region: "Dry run only — no write performed. This would be a no-op: the selected artifact is already current for this target." That's a genuinely good outcome — I got a real promote workflow operating end-to-end via keyboard only, no mouse.

But right after that submission, I checked where focus landed: `document.activeElement` was `<body>`. Nothing. The result banner appeared, but focus wasn't moved to it, and it wasn't kept on the button either — my tab position was simply lost, and I'd have to start over from the logo to get back down to "Promote for real" or to re-read the result via Tab. For a screen-reader user this is worse than it sounds: unless the alert role triggers an automatic announcement (plausible, but not guaranteed depending on how it's implemented), there's a real chance the outcome of the action goes completely unnoticed.

One more thing worth being honest about: several times during this session the page navigated to a completely different URL between my own actions — once jumping from the Dashboard to Environments after a single Tab press, once landing on an unrelated `/artifacts/...` detail page mid-sequence. I don't think this was caused by anything I did (focus programmatically stayed sane, and the *within-page* tab order was consistent every time I got a clean run). My best guess is this is a shared browser session also being driven by other concurrent test agents, not a single-user defect in the app — but I'm flagging it because if a real user experienced pages changing under them mid-task with no warning, that would be a five-alarm accessibility (and usability) problem regardless of cause.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected an internal ops dashboard — land on some kind of overview, then drill into a matrix of app × environment to promote or roll back a build. That's roughly what I got: Dashboard first, with clear links out to Environments/Deployments/Apps/etc. Reasonable, unsurprising information architecture.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Landed on Dashboard, Tab-checked for a skip link (none), tabbed the primary nav and found it has zero visible focus indicator on all seven items (confirmed via computed style *and* a screenshot with "Deployments" focused — it looks identical to unfocused neighbors). Went to Deployments, found that the clickable "detail" affordance on chart-type rows is a `tabindex="-1"` div — unreachable by keyboard, full stop, that's a hard stuck point, not a workaround-able one. The icon-only promote/rollback/copy controls (⬆ ↺ ⧉) are reachable but their accessible names are just the raw glyphs. Opened a promote form, found no `<label>` elements anywhere — the Version dropdown has no accessible name at all, the Reason field's name comes only from its placeholder. Did a clean Tab walk through the form, typed into Reason, tabbed to "Run dry run," pressed Enter — this worked and returned a real result. After that, focus was dropped to `<body>` with no indication of where to go next. Separately, the page jumped to unrelated URLs a few times between my own actions, which I believe was cross-session interference in this shared test browser rather than the app itself, but it did disrupt a couple of my Tab-order traces.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Mostly yes — I got from Dashboard to Deployments to an open promote form to a submitted dry run, entirely via keyboard, and the "happy path" itself is operable. What's missing is confidence that a *screen-reader* user could do the same: the unlabeled Version field, the glyph-only action names, and the unreachable row-detail affordance would all trip up someone who can't fall back on visual layout the way I effectively did by reading the accessibility snapshot.

**4. Rate navigability 1-5.**
3. The overall structure and tab order are logical and nothing is a true dead end on the core promote flow, but the missing nav focus indicator and the unreachable row-detail div are real, not cosmetic, gaps.

**5. Rate usefulness/task-success 1-5.**
4. I did complete the actual task (reach and operate a promote form, submit a dry run) via keyboard alone. Docking a point for the two findings above plus the post-submit focus loss.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
The "Requires approval" and "Allowed principals" columns on the Environments page carry inline caveats ("stored, not enforced," "does not restrict who can promote") — that's actually refreshingly honest copy, not a trust problem. The real trust wobble was after "Run dry run": the result appeared, but with focus silently reset to `<body>`, I couldn't tell without checking the DOM directly whether the action had actually completed or whether I'd lost my place mid-flow.

**7. What's one thing you'd change immediately if you owned this UI?**
Restore a visible `:focus-visible` style on the primary nav links — it's clearly already implemented correctly everywhere else on the page (logo, form fields, buttons all get a clean 2px outline), so this looks like one missed CSS rule, not a systemic redesign.

**8. What's something you thought was genuinely cool or delightful?**
The dry-run flow itself is a nice touch — getting a real, specific answer ("this would be a no-op, artifact's already current") before committing to a write is exactly the kind of safety net that makes an ops tool trustworthy, and it's fully keyboard-operable once you're in the form.

**9. Anything you expected to exist that wasn't there?**
A skip-to-content link, given there's a persistent seven-item nav on every single page. And I expected the row-level "click for detail" affordance on the Deployments matrix to be a real link or button — instead it's an inert div as far as keyboard/AT is concerned.

**10. One sentence recommendation — is this tool usable keyboard-only today, yes or no, and what's the single biggest blocker?**
Mostly yes for the core promote workflow, but the single biggest blocker is the invisible focus indicator on the entire primary navigation menu — combined with the unreachable row-detail divs on the Deployments matrix, a keyboard-only user has no reliable way to see where they are or to access every feature a mouse user gets.

---


**Persona:** Casey Nakamura — new engineer, second week on the team, semi-technical (comfortable with git/CLI, still ramping on team-specific tooling)
**Task:** Self-guided 20-minute tour of App Registry per onboarding doc ("mostly self-explanatory"), hunting for in-app help/docs/tooltips/glossary explaining jargon (chart vs image, promotability, drift, provenance, adopted vs observed), and coming away with a correct mental model with zero human help.

## Narrative

Onboarding doc says "spend 20 minutes exploring it, it's mostly self-explanatory," so I pulled up `localhost:8090` cold, no Slack pings to a teammate, just me and the browser.

**Dashboard first.** This is a good front door — three stat tiles (Environments: 3, Drifted overrides: 1, Currently promoted: 19), three environment cards (dev/stage/prod) each with a Healthy/Drift pill, and a "Recent promotions" table. There's a banner up top: "⚠️ 1 drifted override(s) — an overridden image no longer matches its chart's pin." I don't fully know what "override" or "chart's pin" means yet, but I can tell it's a warning and there's a link to go look at it. Nice touch at the bottom: "Dashboard is read-only by design — every action here links out to the Deployments screen that owns it." That's the kind of sentence that tells me someone actually thought about a confused new hire reading this page.

**Environments page.** This is where I started actually trusting the tool. The column headers aren't just "Requires approval" — they're "Requires approval *(stored, not enforced — Promote does not check it)*" and "Allowed principals *(does not restrict who can promote — the promoter role check is the only access control in force)*." There's a full paragraph at the bottom spelling out exactly which fields are real-but-inert and which one (`app-registry-promoter-<key>` realm role in Keycloak) is the only thing actually gating a promotion. I did not expect a tool to be this honest about its own unfinished bits. I poked "+ Add environment" too and the form repeats the same caveats field-by-field, plus a red alert about the Keycloak role not being visible/confirmable from this UI at all. That's the single most useful screen for building an accurate mental model, and ironically it's the one I'd have guessed was boring boilerplate.

**Deployments matrix.** This is clearly the "main" screen — one row per "promotable entity," one column per env, with version@digest, a copy button, an "Adopted" badge on some cells, "drift" on others, and ⬆ (promote) / ↺ (rollback) icons per cell. I could see the one drifted row highlighted (`manmanv2-control-services`, stage cell literally tagged "drift" in red-ish). Good — I can visually confirm what the dashboard warned me about. I also noticed a bunch of rows sitting at "not promoted" across all three environments (all the `demo-*` chart entries, most `manman-*` apps) — I initially wondered if the tool was broken, then realized "not promoted" is a legitimate, common state, it's not an error.

I did try clicking into a row to see if there was a governing "explain this row" panel, and at one point a click I made landed me on an unrelated "Add environment" screen, and another time on a "Promote app-registry-app-registry → dev" form I hadn't asked for — table stakes for a local dev environment I guess (maybe I fat-fingered something, or this build's routing is a little jumpy), but it was disorienting for a minute; I had to re-navigate from the nav bar each time to get my bearings back.

**Apps Catalog.** Subtitle: "Every registered app and chart, and what 'promotable' means for each." Good framing sentence — except it never actually defines "promotable" in a sentence I could point to; I had to infer it from the table itself. The "Deploy unit" column is where chart-vs-image jargon lives: most rows say "image (via chart)," a few say "chart," a couple say plain "image." Clicking into an "image (via chart)" row (`app-registry-api`) showed me the shape of it directly: badge "image (via chart)," a link "📦 part of chart: app-registry-app-registry," and per-env rows tagged "Via chart" instead of "Adopted." So now I get it: some images are promoted as a standalone unit, most are just riding along inside a chart's pin and aren't independently promotable. That's a real and important distinction and the UI does eventually show it, just not with a plain-English label anywhere ("image (via chart)" is doing a lot of work for three words).

**Drift & Audit.** This page is where the two biggest vocabulary words finally got spelled out for me. "Drifted overrides" section: "Overridden promotions that no longer match their chart's pin. Not a cluster check — the registry only knows about its own promotion + chart-pin records." Good, that also answers a question I was quietly holding (does this thing check the actual cluster? No — it's bookkeeping only, ArgoCD does the real deploying, though the UI never says the word "ArgoCD" anywhere I found). "Adopted artifacts" section: "Recorded by a human via AdoptArtifact, not observed by CI." That's "adopted" defined in one line. There's also an honest disclaimer: "No adopt control lives here — recording a new adoption is screen 52, delivered separately. This screen is read-side audit only" — a little inside-baseball (screen 52?) but at least it's not pretending a feature exists that doesn't.

**Artifact detail pages** are where "Adopted" vs "Observed" get the cleanest definitions in the whole app: literally a "Provenance" row reading *"Adopted — recorded after the fact by an admin, not observed by CI"* on one artifact, and *"Observed — recorded by CI, not adopted"* on another. Crisp, symmetric, no ambiguity. Problem: you only see this if you click three levels deep (Apps → app/chart → artifact). Nowhere on the Deployments matrix — where I'd actually be scanning "Adopted" badges all day — is that word explained. I had to go spelunking to find the definition of a word that's staring at me on the very first real screen.

**Reconcile Runs** explained itself well too: "The identity pipeline — ReconcileApps from ci.yml, on every push to main. Separate from Builds: this tracks app/chart identity, not published artifacts," plus a note that a sweep rejected as "stale" is expected/correct behavior, not a bug. **Builds** was similarly self-aware — a build detail page for a run with zero recorded artifacts told me plainly: "This registry cannot tell whether recording opt-in was off for every target, or whether recording failed silently" — refreshingly honest about its own blind spot instead of just showing an empty table and letting me guess.

**Chart detail page** (`manmanv2-control-services`) tied it together: per-env "Apps published in ... (stage)" tables, the drift case called out inline ("manmanv2-control-api overridden — drift"), and a "Currently declared composition" section with the line "What this chart's manifest says it composes right now — independent of what any promoted version actually pins above." That's the composition-vs-promoted-drift concept from the brief, and it's actually explained, just not glossary-style — you get it by reading a paragraph in context.

I searched the whole app for a help icon, "?" affordance, tooltip, or glossary link (used find-in-page for "help," "glossary," "docs," "learn more") — nothing, on any page I visited. The "developer" text in the top right isn't a menu either, it's just a static label — no settings, no profile, no help link behind it.

By the end of ~20 minutes I felt like I had a real, mostly-correct model of the system, but I built it by triangulating across five or six different detail pages, not because the app told me up front.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
From the onboarding doc's one-liner ("promotion source of truth") I expected something like a deploy dashboard — a grid of apps × environments showing current versions, plus buttons to push a new version forward. I expected to land on exactly that grid. Instead I landed on a Dashboard summary page first, which was actually a reasonable place to start — it gave me the headline numbers before dropping me into the detail.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → Environments → Deployments matrix → Apps Catalog → an app detail page → Drift & Audit → two artifact detail pages → Reconcile Runs → Builds → a build detail page → a chart detail page → the Promote form → Trigger Release. I backtracked twice: once when a click from the Deployments matrix unexpectedly dropped me on the "Add environment" form instead of an app detail view, and once when I ended up on a Promote form I hadn't clicked into. Both times I just used the top nav to reset and re-navigate — no real damage, just a couple of "wait, how did I get here" moments.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Mostly yes. I came away able to correctly explain: chart vs. image as deploy units, "via chart" images aren't independently promotable, drift means an override no longer matches the chart's current pin, adopted means a human backfilled it and observed means CI recorded it live, and this whole tool is bookkeeping — it doesn't touch the cluster itself. The one thing I never got a straight answer on: what actually consumes this data to deploy anything. The word "ArgoCD" never appeared anywhere in the UI text I found, even though my onboarding doc told me that's the real deploy mechanism. If I hadn't already been told that externally, I'd have finished this tour thinking App Registry itself deploys things.

**4. Rate navigability 1-5.**
3. The nav bar is simple and the seven top-level sections make sense once you've seen them all. But a couple of clicks took me somewhere I didn't intend (a promote form, an add-environment form) without a clear cause, which cost me re-orientation time. Deep information (like what "Adopted" actually means) is also buried three clicks down from where you'd most want it.

**5. Rate usefulness/task-success 1-5.**
4. Once I pieced it together, every screen was legitimately useful and non-redundant — Dashboard for the 30-second check, Deployments for the "what's live where" grid, Drift & Audit for the audit trail, Builds/Reconcile Runs for CI plumbing questions. I'd genuinely reach for this tool day-to-day.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes — the "Healthy" / "Drift" pill on the environment cards on the Dashboard. "Healthy" reads exactly like an ArgoCD/cluster health check to someone coming from a Kubernetes-adjacent team, but as far as I can tell from the rest of the app, it just means "no drifted overrides in this env" — a data-consistency check, not a liveness check. I'd genuinely worry a teammate might glance at three green "Healthy" pills and assume pods are up, when this tool has no idea whether anything is actually running.

**7. What's one thing you'd change immediately if you owned this UI?**
Put a one-line legend directly on the Deployments matrix — the screen everyone will look at first and most often — defining "Adopted" (human-recorded, not CI-observed) and "drift" (override no longer matches the chart's pin) right there, instead of making people discover those definitions on an artifact detail page three clicks deep.

**8. What's something you thought was genuinely cool or delightful?**
The Environments table's inline caveats — "stored, not enforced," "does not restrict who can promote," annotated right in the column header — and the matching alert on the Add Environment form warning that the Keycloak realm role can silently lock everyone out and this UI can't even see whether it exists. That's an unusually honest, almost self-deprecating level of transparency about which controls are real and which are decorative. I trust a tool more, not less, when it admits what it can't do.

**9. Anything you expected to exist that wasn't there?**
A glossary or "?" tooltip — I went looking specifically and found zero. Also no visible link from anywhere in the UI back to ArgoCD or to "here's what happens after you promote," which feels like a real gap given the tool explicitly calls itself a "source of truth" that something else acts on.

**10. One sentence recommendation — after 20 minutes of self-guided exploring, do you feel ready to use this tool for real, or would you still need to ask a teammate first?**
I'd feel comfortable reading the Deployments matrix and Drift & Audit page unsupervised starting today, but before I clicked "Promote for real" on anything I'd still want five minutes with a teammate to confirm what downstream system actually reacts to that click.

---


**Persona:** Vic Torres — Staff Engineer, deeply skeptical, treats every form as an attack surface
**Task:** Deliberately try to break App Registry's forms with edge-case input (empty required fields, malformed digests, extremely long text, double-submits, rapid dropdown switching, invalid direct URLs) and evaluate how gracefully the UI fails.

## Narrative

I started at `http://localhost:8090`, landed on the Dashboard, and immediately noted the tool is honest about its own scope: "Dashboard is read-only by design — every action here links out to the Deployments screen that owns it." Fine. I went hunting for the actual mutating forms: Trigger Release and the per-cell Promote/Rollback actions on Deployments.

Within the first few minutes of poking around, I ran into something bigger than any of the specific edge cases I came to test: **this environment is being hammered by concurrent automated traffic sharing my exact browser session.** Every single time I navigated to a form — Promote, Trigger Release, anything — and then made a second tool call (even a plain snapshot, even a same-millisecond `fetch()`), the tab had already been yanked to a completely different page: Apps Catalog, a random app detail page, a promotion-details page, Reconcile Runs, Builds, whatever. New tabs I opened myself to get a "clean" surface got hijacked just as fast — sometimes within the round-trip of a single tool call, before I could even read the DOM. By the end I had 20+ tabs open, almost none of which were where I'd left them. I want to be very explicit about this in my findings: I cannot tell whether some of what I saw (or failed to see) is this app's bug or just concurrent test sessions stepping on each other in a shared local Tilt environment. I'm flagging it rather than asserting it as a product defect.

Given that, I adapted my technique: instead of multi-step click-sequences (which kept getting stolen mid-flight), I used single atomic JS `evaluate()` calls that do everything — fill field, click, wait, read result — in one shot, and `fetch()` calls that don't depend on the visible tab surviving at all. That got me real, trustworthy signal on a few things, though not everything I set out to test.

**Empty Reason field.** On `/promote?env=dev&kind=chart&owner=demo-hello-fastapi`, the Reason textarea has a native HTML `required` attribute. Clicking "Promote for real" with it empty didn't submit anything, didn't navigate, and didn't add any custom error text to the page — it triggered the browser's own native validation ("Please fill out this field.") and blocked the click. Functional, but it's relying entirely on default browser chrome rather than an in-app, screen-reader-friendly, on-brand error message. If the user has form-validation extensions/autofill weirdness, or is on a browser that renders that bubble subtly, it's easy to miss why nothing happened.

**Stale dry-run defense (unexpectedly good).** On a promote form for an environment above dev (I landed on prod for `friendly-computing-machine-bot-services`), the page states outright: *"prod is above dev (rank 20). 'Promote for real' is a UI policy requirement: disabled until a dry run has been run against exactly the form state below — changing any field re-arms this requirement."* That's exactly the defense I'd want against the "rapidly switch the Version dropdown before the dry run completes" attack I came in planning to run — the button is disabled by construction until a dry run has validated the *current* exact form state, and any field change (including the dropdown) immediately re-locks it. I'll give real credit here: this is a deliberate, well-thought-through guard rail, and the copy explaining it is unusually transparent for an internal tool. The same text also disclosed a subtlety I wouldn't have known to ask about: *"the server itself only requires [a reason] above dev/rank 0"* — meaning the Reason-required rule is a UI-side policy for dev, layered on top of a looser server contract. Interesting to know, and honestly a little concerning: it means the UI is the only thing stopping a raw API caller from promoting to dev without a reason.

**Bad direct URLs.** This is where the app's error handling gets genuinely rough:
- `GET /apps/does-not-exist-xyz123` → **HTTP 502 Bad Gateway**, plaintext body, no app chrome at all: `Failed to load app does-not-exist-xyz123 from app-registry-api: GetApp("does-not-exist-xyz123"): rpc error: code = NotFound desc = not found`. That's a raw gRPC error message leaking straight through to the browser. Wrong status code too — a "not found" from the backend RPC should surface as a 404, not a 502 (502 usually means "I couldn't even reach my upstream," which is misleading here and would send an on-call engineer down the wrong path at 3am).
- `GET /promote?app=nonsense` and bare `GET /promote` (no query params at all) → **HTTP 400 Bad Request**, plaintext: `missing or invalid 'owner'/'kind'/'env' query parameters`. The message itself is specific and accurate, which I'll credit, but it's delivered as an unstyled raw text response outside the app shell — no nav bar, no "go back" link, nothing that tells a human they're still inside App Registry.

**What I couldn't finish, and why.** I made seven-plus attempts, across several minutes, to: (1) type a garbage digest string (`not-a-real-digest-!!!@@@`) into Trigger Release's "Digest (optional)" field and hit Resolve selection, (2) paste ~20,000 characters into the Reason field and submit, and (3) double-click "Promote for real" to check for a double-submit race. Every attempt got its tab stolen by concurrent traffic before or during the interaction — including one case where my own `browser_navigate` call itself apparently landed on a URL other than the one I requested. I could not get clean, attributable results for those three specific cases. I'm reporting that honestly rather than guessing.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected an internal deploy-tracking dashboard — something like a lightweight ArgoCD companion showing "what's promoted where" with a promote/rollback action per row. I expected to start on a landing page listing environments or apps, which is exactly what the Dashboard gave me. No surprises there.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → Trigger Release (inspected the form) → Deployments → Promote form for `demo-hello-fastapi`/dev → tried submitting with an empty Reason → tried a prod-rank promote to see the dry-run gating → tried bad URLs directly. I backtracked constantly, not because the app confused me, but because my browser tab kept getting stolen out from under me by what I have to assume is other automated traffic sharing this environment — I lost count of how many times I re-navigated to the same Promote form only to find myself on Apps Catalog or a random app page two seconds later. That ate most of my session and is the single biggest "usability" issue I hit today, caveat that it may not be the app's fault.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Partially. I confirmed the Reason field's required-ness (native validation, no custom error), confirmed the app has real backend validation for bad promote query params (400 with a specific message), and confirmed a genuinely good dry-run-staleness guard on above-dev promotions. I did not get clean answers on garbage-digest handling, true double-submit races, or rapid-dropdown-switch races — the environment didn't hold still long enough.

**4. Rate navigability 1-5.**
4. Nav bar is flat and sensible (Environments, Deployments, Apps, Builds, Reconcile Runs, Drift & Audit, Trigger Release), URLs are predictable and RESTful-ish, and pages cross-link well (chart → apps published in that chart, promotion → build → artifact). Docked one point because deep-linking into a broken state (bad app slug, malformed promote query) drops you into a bare unstyled error response with zero way back into the app except the browser back button.

**5. Rate usefulness/task-success 1-5.**
3, specifically for *my* task (edge-case/robustness probing) — not a knock on the app's core purpose, which looks solid from what I saw of it. I couldn't finish 3 of my 5 planned edge-case tests due to environment instability, so from a "did I get my job done" lens, no, not fully.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes, twice. First: the 502 on a bad app slug — a 502 usually means "upstream unreachable," but the body clearly says `NotFound`, which is a 404-shaped problem wearing a 500-series status code. That mismatch would make me suspect a real outage before I read the body. Second: the promote form's own copy, which I actually appreciated for being unusually candid — it says the UI requires Reason for every environment while "the server itself only requires it above dev/rank 0." That's good transparency, but it also means I now trust the UI *less* as the source of truth for what's actually enforced, since it's telling me outright that it enforces more than the API does. Anyone hitting the API directly (a script, a CI job) could promote to dev with a blank reason and it'd be accepted server-side.

**7. What's one thing you'd change immediately if you owned this UI?**
Wrap every backend error (400/404/502/whatever) in the same app shell with the nav bar intact, translate backend status codes honestly (NotFound → 404, not 502), and strip raw gRPC error strings before they reach the browser. Right now a mistyped URL dumps a bare, unstyled server error with implementation details in it — that's a rough first impression and a support headache waiting to happen.

**8. What's something you thought was genuinely cool or delightful?**
The dry-run-staleness guard on above-dev promotions, hands down. Disabling "Promote for real" until a dry run has run against the *exact* current form state, and re-arming (disabling again) on any field change, is precisely the right way to prevent someone from dry-running version A, switching the dropdown to version B, and fat-fingering a promote that was never actually validated. That's a real, considered piece of defensive UX design, not an accident.

**9. Anything you expected to exist that wasn't there (e.g. a specific validation message you expected but didn't get)?**
I expected a friendly branded "app not found" page for a bad `/apps/<slug>` URL — a 404 within the app shell, maybe with a search box or a link back to Apps Catalog. Instead I got a raw plaintext gRPC error and a 502. I also expected — but couldn't verify either way given my environment troubles — some client-side format check on the Digest field (e.g., "must be `sha256:` followed by 64 hex chars") before it's even sent to the server; the placeholder text (`sha256:...`) implies a format contract that I never got to confirm is actually enforced.

**10. One sentence recommendation on the overall robustness of this UI's input handling.**
What I could verify was decent-to-good — real backend validation with specific (if too-raw) messages, and a genuinely well-designed stale-dry-run guard — but the unstyled, wrongly-coded error responses for basic bad-URL cases are the kind of thing that erodes trust fast, and I'd want that fixed before I'd call this "hardened."

---


**Persona:** Ingrid Larsen — Internal Tooling Data Analyst (semi-technical: dashboards, spreadsheets, basic SQL; not a backend engineer)
**Task:** Build a number for "how often do we actually promote things, and how long does it typically take something to go from dev to prod?" — promotion frequency and dev→stage→prod lead time, fleet-wide if possible, or for a couple of specific apps otherwise — using only what the App Registry UI exposes.

## Narrative

I went in expecting something like a deploy dashboard with a history tab and maybe a chart. First stop, `localhost:8090`, lands on a "Dashboard" — good sign, that's usually where the overview lives. Top of the page: 3 environments, "1 drifted override," "19 currently promoted." There's a "Recent promotions" table right there with App / Env / Version / Since columns, and Since is a real ISO timestamp (`2026-08-23T07:16:05Z` etc.) — exactly the raw material I need. I jotted down the ten rows it showed.

Then I went looking for more — is there a per-app history, so I can actually measure dev→stage→prod gaps instead of eyeballing one shared table? I tried to get to "Apps" from the nav. This is where things got messy: navigating and even clicking sent me bouncing between a "Promote demo-hello-fastapi" form, random "Promotion Details" pages, "Trigger Release," and other screens I never clicked into myself. I want to flag this honestly: the prompt for this session told me other automated tests might be sharing this environment concurrently, and given I kept landing on tabs showing pages I didn't request (and the browser tab list kept accumulating tabs from what looked like a different in-progress promote flow), I'm treating this as concurrent traffic, not a confirmed app bug. But it genuinely slowed me down and once put me one click away from a live "Promote for real" button on a form I hadn't filled in — I backed out without submitting.

Once I forced fresh navigations by URL and grabbed the page immediately, I got clean data. The Apps Catalog (`/apps`) lists every registered app/chart, filterable by domain and deploy unit, with current version per environment or "not promoted." No timestamps here though — just current state.

The Deployments page (`/deployments`) is the promotion matrix — one row per promotable entity, one column per environment, current version + Adopted/drift badge + promote (⬆) and rollback (↺) action links. Again, only current state, no history.

To get real history I had to open individual entities. Two different page types, two different experiences:

- **Chart pages** (`/charts/leaflab-leaflab`, `/charts/app-registry-app-registry`) show "promoted X minutes/hours ago" per environment — **relative time only**, no absolute timestamp, and no history list at all. Below that, tables of "apps published in this chart version" per environment.
- **App/image pages** (`/apps/manmanv2-host-manager`) are richer: current version per env, a "Current artifact by environment" table linking to the CI build, AND an actual "Promotion history" list with absolute UTC timestamps, action (promote/rollback), requester, and reason, each linking to a "Promotion Details" page with the full request record (requested-at, action, reason) and a "Writeback" block (committed? sync triggered? sync status? health status?).

That Writeback block was an eye-opener and also where I stopped trusting my first read of the data: several promotion records I opened showed "Pending" with "Not yet committed" and "Not yet triggered" for the ArgoCD sync. So a "promotion" in this tool is a *recorded request*, not necessarily a *live change* — the environment's current-version cell might reflect something ArgoCD hasn't actually synced yet. That matters a lot for a lead-time calculation: am I measuring "time the promotion was requested" or "time it went live"? The UI only really gives me the former.

Then I tried to actually do the math. I picked three entities I had good data for:
- `leaflab-leaflab` (chart): dev promoted `2026-08-23T06:31:46Z`, stage `2026-08-23T06:31:46Z`, prod `2026-08-23T06:31:46Z` — **identical to the second.**
- `app-registry-app-registry` (chart): dev/stage/prod all "promoted 59 minutes ago" — same relative bucket, effectively simultaneous.
- `manmanv2-host-manager` (image): promotion history entries at `06:31:46Z` (×3) and `06:32:48Z` (×2), all within about a minute of each other.

I also checked the Builds list (`/builds`) hoping for a longer historical tail — nope, every fleet-related build was "57–58 minutes ago," all from the same seed run (`seed_tilt_walkthrough.py` / `seed-test`), with only a couple of unrelated "system-validator" entries at 2/7/8 hours ago that don't belong to any app I was tracking.

So here's the uncomfortable conclusion I'd have to bring back to my manager: every single timestamp I could find — across the Dashboard, the per-app Promotion History, and the Builds list — falls inside roughly the same one-hour window on the same day. Dev, stage, and prod for the same app were "promoted" at the exact same second in more than one case. That's not a real rollout cadence; that's the moment this local environment got seeded with a snapshot of "current state." There is no evidence anywhere in the UI of promotions actually spread across real days or weeks, so I cannot compute either "promotions per week" or "dev→stage→prod lead time" as a trustworthy number from what's on screen — the honest lead time I'd calculate would be ~0 minutes for everything, which I know is not real.

One extra thing I noticed and couldn't chase down given the time I had: the Deployments page has an "As of (UTC)" field with an Apply button, implying you can view the promotion matrix as of a past point in time. That's promising for point-in-time comparisons, but it's a one-timestamp lookup, not a range/trend tool, and I didn't get to fully test it given how much the concurrent navigation was eating my clock.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a deploy-tracking dashboard — something like a lightweight internal Backstage/Octopus Deploy hybrid — and I expected to land on an overview page with recent activity and maybe a trend chart, then drill into a specific app for its full history. Landing on "Dashboard" matched that expectation reasonably well.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → noted the "Recent promotions" table (real timestamps, only ~10 rows) → tried to get to Apps Catalog, got repeatedly bounced to a "Promote" form and random Promotion Details pages I hadn't clicked (concurrent-session interference, per the task's own warning — I backed out without submitting anything) → forced clean navigation by typing URLs directly → Apps Catalog (current state, no history) → Deployments matrix (current state, no history) → Chart detail pages (relative time only, no history list) → App/image detail page (absolute timestamps + real Promotion History + Promotion Details drill-down) → Builds list (confirmed everything clusters in one seed window) → Reconcile Runs (unrelated identity-pipeline concept, one row) → tried Environments, got hijacked again mid-navigation. The biggest backtrack was realizing partway through that chart-type entities and image-type entities have genuinely different detail pages with different data available — I had to remember which type each app was to know where to even look for history.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
No, not a trustworthy one. I got the current-state answer ("19 things promoted somewhere right now, 1 drifted") easily. But every promotion timestamp I could find — across three different screens — fell inside the same roughly one-hour window, with several dev/stage/prod triples showing the *identical* second. That tells me the local seed data represents a snapshot of "where things are now," not a simulated history spread over real time, so I can't compute either promotion frequency or dev→stage→prod lead time without producing a fake zero. What's missing: any promotion feed spanning more than an hour, and any indication of whether "promoted at" means "requested" or "actually live" (the Writeback/Pending status suggests those are different things).

**4. Rate navigability 1-5.**
2. Setting aside the concurrent-session chaos (which I'm not fully blaming the app for), the deeper issue is structural: there's no single place that aggregates promotion events across apps over time — I had to visit each app individually, and even then chart-type vs. image-type entities show different information. No breadcrumb from "Dashboard" summary numbers back to a filterable list.

**5. Rate usefulness/task-success 1-5.**
2. It answers "what's live right now" well. It does not support "how often" or "how long" questions at all with the data currently in the system — and I couldn't tell if that's a UI gap or a data gap, which itself is a problem.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes, twice. First: a promotion labeled "rollback" for `manmanv2-host-manager` showed "dev: v1.0.0 → v1.1.0" — going to a *higher* version number under a "rollback" label reads backwards to me; I wouldn't trust a naive "rollback = version went down" assumption when doing this analysis. Second: several promotion detail pages showed "Pending" with "Not yet committed" / "Not yet triggered" for the ArgoCD writeback — meaning the promotion I'm reading might not have actually taken effect in the cluster yet, but the app's "current version" cell doesn't visually distinguish "promoted-and-live" from "promoted-and-pending." As an analyst I'd want that flagged loudly before I count it as a completed promotion event.

**7. What's one thing you'd change immediately if you owned this UI?**
Add a fleet-wide, paginated/filterable promotion log (not just the Dashboard's fixed ~10-row "recent" table) with absolute timestamps, sortable by app/env/date, and a visible "actually synced" vs "pending" flag — that single screen would make the frequency/lead-time question answerable in five minutes instead of unanswerable.

**8. What's something you thought was genuinely cool or delightful?**
The per-app "Promotion history → Promotion Details" drill-down is genuinely well designed where it exists: requester, absolute timestamp, reason, and live ArgoCD sync/health status all in one place. If that same depth existed fleet-wide instead of per-app, this would be a great tool for exactly my use case.

**9. Anything you expected to exist that wasn't there?**
A trends/metrics/reports page. The nav has Environments, Deployments, Apps, Builds, Reconcile Runs, Drift & Audit, Trigger Release — no "Metrics," "Reports," or "History" tab, no chart of promotions-over-time, no CSV export, no aggregate "average lead time" stat anywhere despite the Dashboard clearly being built to surface aggregate numbers (it already computes "19 currently promoted" and "1 drifted").

**10. One sentence recommendation — could you build a real trend/metrics report from this UI's data today, or not?**
Not today — the UI's mechanics for showing history (per-app promotion logs with real timestamps) are there and well-built, but the actual data currently loaded has every promotion clustered in the same ~one-hour seeding window with no real historical spread, so any frequency or lead-time number I computed right now would be meaningless.

---


**Persona:** Malik Osei — Support/Customer-Success Engineer (semi-technical)
**Task:** Find the one seeded rollback event in App Registry, and answer: which app, what version it rolled back from/to, who did it, when, what reason was given, and whether there's an approval trail distinct from "who clicked the button" — building a confident answer for a customer escalation ticket.

## Narrative

The ticket just says "customer noticed something got rolled back in prod, wants to know who approved it and why." No app name. So step one is just going to be "go find the rollback in whatever tool tracks this."

I land on the Dashboard at localhost:8090. It's got a nice "what's live, what drifted" framing, three environment tiles (dev/stage/prod), and a "Recent promotions" table. I scan it for anything that says "rollback" — nothing. It's just current version-per-env rows with timestamps, no action-type column. Not what I need, but it does surface one useful thing: a "1 drifted override(s)" banner. Not my rollback, but I file it away.

Next I try "Drift & Audit" in the nav — "Audit" sounds exactly like what I want, a log of actions taken. It's not. It's two tables: "Drifted overrides" (one row, a stage/prod mismatch on manmanv2-control-api) and "Adopted artifacts" (a long list of things a human manually recorded rather than CI observing them). No promote/rollback/override event log at all. First backtrack.

I try "Deployments" — the promotion matrix. Every promoted cell has a little ⬆ and ↺ icon next to it. My first instinct is "oh, the ↺ must mark cells that were rolled back" — I actually go looking at which rows have it lit up differently before realizing every promoted cell has the same ↺, always. It's not a history marker at all, it's a live control that would let me trigger a brand-new rollback right now if I clicked it. That's a genuinely uncomfortable half-second — I almost clicked one just to "see what it does" before catching myself. Second backtrack, and a flag for later.

So: no cross-app activity feed exists. The only place history lives is inside each individual app's own page, under "Promotion history." That means checking apps one at a time. I open the Apps Catalog (30+ rows) and start clicking in.

Around this time the session gets weird — pages start navigating on their own to screens I didn't click (a "Trigger Release" form, a raw "Promote demo-hello-fastapi → dev" screen with a live "Promote for real" button sitting there, even a 404/502 probe against a fake app id), and new browser tabs keep appearing faster than I open them myself. I was warned other automated sessions might be poking the same app concurrently, so I treat this as that, not a bug in the tool itself — but as a real user this would have been alarming, watching an unattended tab surface a "Promote for real" button I never asked for.

I check `demo-hello-fastapi` first (its dashboard timestamp was suspiciously the newest of anything) — its promotion history is a live mess of "promote — developer (you)" entries with notes like "Design review poke-around — trying the promote flow end to end," clearly other test traffic hammering it in real time. Not my rollback, and honestly unnerving to look at as a support engineer trying to build a "here are the facts" answer — the ground is moving under me.

I check `manmanv2-control-api` next, since it's the one with drift — its promotion history shows an "override" action (that's what caused the stage/prod digest mismatch), not a rollback.

I finally check `manmanv2-host-manager` (an image, part of the manmanv2 domain) and there it is: one entry reading `rollback — developer (you)`, reason `"seed_tilt_walkthrough.py: populate local registry for a UI walkthrough"`, at `2026-08-23T06:32:48Z`. I click into the "details" link.

The detail page is clean: a heading "dev: v1.0.0 → v1.1.0", a Request block (Requested by: developer (you); Requested at: 2026-08-23T06:32:48Z; Action: rollback; Reason: the seed-script string), a Writeback block (Committed: Not yet committed; Sync triggered at: Not yet triggered; Current sync/health status: —), and a Sync history section that just says "No ArgoCD sync/health observations recorded yet."

To understand the "from/to," I also pull the sibling promotion right before it in the list (same exact timestamp, `bdff1a21…`), which turns out to be a `promote` action that moved dev from v1.1.0 down to v1.0.0. So the real sequence is: something (the seed script, standing in for "a bad promote") pushed dev down to v1.0.0, and immediately after, a rollback action put it back up to v1.1.0 — i.e., the rollback undid the bad promote and restored the prior version. That's the correct mental model for "rollback," just not what I expected from the version arrow alone.

Here's the catch for my actual ticket: **this rollback happened in `dev`, not `prod`.** I don't find any rollback action recorded against prod for this app or any other app I checked. Prod for this same app is sitting on the older v1.0.0 while dev/stage are on v1.1.0 — but that's just described elsewhere in the tool as the normal "two generations, prod trails" staged-rollout pattern, not the result of an explicit rollback action. So if my customer says "it got rolled back in prod," this tool's own audit trail doesn't actually back that claim up — the only rollback event that exists anywhere in this dataset never touched prod.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a single "Activity" or "Audit Log" screen — a chronological, filterable feed of every promote/rollback/override across all apps and environments, so I could search or filter for "action = rollback" and be done in one screen. I expected to start on the Dashboard, since that's usually where an ops tool puts "recent events."

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → no action-type info in "Recent promotions," just current state. Drift & Audit → expected an audit log, got drift + adopted-artifact tables instead, no rollback events (backtrack #1). Deployments matrix → mistook the ↺ icon for a "this was rolled back" history marker before realizing it's a live control to trigger a new rollback right now — nearly clicked it out of curiosity (backtrack #2). Apps Catalog → had to open individual apps one at a time since history only lives per-app. Checked demo-hello-fastapi (noise from concurrent test traffic, no rollback), manmanv2-control-api (found the drift-causing "override," not a rollback), then manmanv2-host-manager, where I finally found the one `rollback` entry and opened its details page.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Partially. I got hard facts: app = manmanv2-host-manager (image), rolled back in **dev** from v1.0.0 back to v1.1.0, actor recorded as "developer (you)," timestamp 2026-08-23T06:32:48Z, reason string = a seed-script description, not a real justification. What's missing for the actual customer question: nothing in the tool shows a rollback ever touching **prod** — so I can't confirm the premise of the ticket ("rolled back in prod") using this tool at all. And there's no approval trail beyond the single requester field.

**4. Rate navigability 1-5.**
2 — finding one event required manually opening roughly five individual app pages because there's no cross-app filter or search by action type; "Drift & Audit" is misleadingly named for what I actually needed.

**5. Rate usefulness/task-success 1-5.**
3 — once I found the record, the detail page itself was precise and useful (actor, timestamp, reason, writeback/sync status), but the underlying facts don't actually support the customer's "rolled back in prod" claim, so I still can't close the ticket confidently from this tool alone.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes, twice. First, the ↺ icon looks identical whether or not a rollback ever happened there — it's just a permanent action button, not a history indicator, which is a dangerous thing to misread in a hurry. Second, the action-type semantics surprised me: "promote" moved a version number *down* (v1.1.0→v1.0.0) and "rollback" then moved it back *up* (v1.0.0→v1.1.0) — I'd assumed "rollback" always meant "go to a lower/older number," but it actually means "undo the last change and restore the prior state," which isn't the same thing.

**7. What's one thing you'd change immediately if you owned this UI?**
Add one global, filterable Activity/Audit Log (by action type, app, domain, environment, actor) so a rollback anywhere in the system is a single search away instead of a per-app scavenger hunt.

**8. What's something you thought was genuinely cool or delightful?**
The promotion detail page's "Writeback" section — it doesn't just say what was requested, it also shows whether that request was ever actually committed and synced to ArgoCD (Committed / Sync triggered at / Current sync + health status). That's exactly the "did this really happen to the cluster, or just get recorded" cross-check a support engineer needs, and it's honestly telling me here that this one was never confirmed synced.

**9. Anything you expected to exist that wasn't there (e.g. a distinct approver field)?**
Yes — there's no "Approved by" or reviewer field anywhere, only "Requested by." Whoever requests a promote/rollback is the only actor modeled; there's no separate approval step, and no ticket/incident-reference field to tie the action to a change request.

**10. One sentence recommendation — could you confidently answer this customer's escalation using only this UI?**
Not fully — I can tell them exactly what one rollback record exists in the system (dev, not prod, by "developer," no real reason or approver logged), but since it never touched prod I'd need to escalate internally before telling the customer their prod issue is explained by anything I found here.

---


**Persona:** Fiona Grant — Release Manager, technical, coordinates multi-app "release trains"
**Task:** Pick 2-3 apps/charts across at least two domains that are ahead in stage vs prod (or dev vs stage), and promote them all to the next environment as a coordinated release, using whatever the UI provides — including testing whether "Trigger Release" supports bundling multiple apps into one action.

## Narrative

I started at localhost:8090 expecting some kind of release dashboard — a place that would tell me, at a glance, "here's what's waiting to ship." First surprise: hitting the root URL didn't reliably land me on a dashboard. Sometimes it did ("Dashboard" title), but the very first navigation actually resolved to `/drift-audit`, and a moment later to `/apps`. I want to flag upfront that this environment is clearly shared with other automated test traffic right now — my tabs kept getting redirected mid-task to pages I never clicked (a stray Promote form for "demo-hello-fastapi," a 404 probe, other promotion detail pages). I'm treating that churn as an artifact of concurrent testing, not a bug in the app itself, and I'll call out separately what I think is a *real* app finding versus noise from the shared session.

Once I got my bearings, the **Apps Catalog** (`/apps`) turned out to be exactly the report I wanted at the start: one row per app/chart, columns for dev/stage/prod, each cell showing version + Adopted/CI-observed provenance, with a "drift" flag where applicable. From that single table I could immediately spot my release-train candidates — several charts had stage ahead of prod:

- `friendly-computing-machine-bot-services` (chart) — stage v0.1.3, prod v0.1.2
- `leaflab-leaflab` (chart) — stage v0.0.11, prod v0.0.10
- `manmanv2-host-manager` (image) — stage v1.1.0, prod v1.0.0
- `manmanv2-control-services` (chart) — stage v0.2.22 *(flagged "drift")*, prod v0.2.21

I deliberately skipped `manmanv2-control-services`/`manmanv2-control-api` — clicking into it (accidentally, via the concurrent-traffic churn) confirmed it's the seeded drift case: stage's override doesn't match what the chart currently pins. Not something I'd bundle into a clean coordinated release without addressing the drift first — that's exactly the kind of judgment call this tool is surfacing correctly.

So I picked three apps across three domains: `friendly-computing-machine-bot-services`, `leaflab-leaflab`, and `manmanv2-host-manager`. Domain diversity, clean stage-ahead-of-prod gaps, no drift.

Next: could I promote all three as one action? I went to **Trigger Release** in the nav, half-expecting a "select multiple apps → ship" wizard given the name. What I found was a checkbox tree of every domain/app/chart in the registry, with a note: "checking a domain releases everything under it; checking one app or chart releases only that target." I selected multiple checkboxes and clicked "Resolve selection" — and it worked, producing a "Draft — N target(s)" table. This is genuinely a batch UI.

But here's the catch, and it's the single biggest finding of this whole exercise: **Trigger Release is not the same operation as Promote.** The resolved draft asked me to choose Auto-bump (+major/+minor/+patch) or an Explicit version, and a "build fresh" digest option per target. This is a **new-build-cutting** workflow — it kicks off CI builds (backed by a Temporal workflow, visible as `release-<hash>` on the resulting status page) for artifacts that don't exist yet. It is not a "move an already-built stage artifact to prod" action. When I resolved a 2-target draft (picking two `app-registry` images that were both un-promoted anywhere) and clicked "Trigger release," it did create one grouped record — `/releases/<uuid>` — showing both targets' queued build state under a single Release ID. So there IS a batch concept in this tool... just not for the thing I actually needed. It's for "cut fresh builds for N apps at once," not "promote N apps' existing artifacts together."

For my actual task — moving an *existing* stage artifact to prod — the only path is the per-app **Promote** form (`/promote?env=<env>&kind=<kind>&owner=<name>`), reachable from an app's detail page or from a cell in the **Deployments** matrix view. Each one is a fully separate transaction: pick version, write a mandatory Reason, run a "dry run" (which the UI *requires* before "Promote for real" un-disables — a nice safety rail, if a little fussy), then submit for real. I did this three times, once per app, writing the same cross-referencing reason text into each ("Release train: coordinated stage->prod promotion alongside X and Y — Fiona Grant") as my own manual paper trail, because the tool gave me no structural way to link them.

Each promotion got back its own independent Promotion ID (`19c3f268-…`, `0b57dc87-…`, `1563ccab-…`) with no parent/batch reference field anywhere on the confirmation screen. I confirmed on the Deployments matrix afterward that all three now show matching prod/stage/dev versions — the promotions succeeded — and `manmanv2-control-services` (the drift case) correctly remains untouched and still flagged.

So: I completed the release train. But it was three independent, unlinked actions wearing a shared reason string I typed by hand, not one auditable unit the system itself understands as a group.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected something dashboard-first — a landing page that says "here's what's out of sync across environments, here's what's waiting on a release train." I expected a "Releases" or "Promotions" concept to be front and center, and I expected to start by picking a set of apps and pinning a target environment, the way a release-orchestration tool usually opens.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Root URL → got bounced around by what I now believe was concurrent test traffic (not my doing) → landed on Apps Catalog, which turned out to be my best "what's ahead of what" view → picked 3 candidates across 3 domains, deliberately avoiding the one flagged with drift → went to Trigger Release expecting a promote-bundle tool → discovered it's actually a build-triggering tool (Auto-bump/Explicit version, "build fresh" digest) with its own batch/Release-ID concept, not what I needed → backtracked to the per-app Promote form → did three separate dry-run-then-promote cycles, one per app, hand-writing cross-references into each Reason field → verified on the Deployments matrix that all three landed and the drift case stayed untouched.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Partially. I got a definitive answer to "is there a batch grouping concept" — yes, but only for triggering new builds, not for promoting existing artifacts across environments. What was missing was the thing I actually wanted: a way to say "promote these three apps to prod together" and get back one auditable record for the set. I ended up faking that grouping myself in free-text reason fields.

**4. Rate navigability 1-5.**
3. The individual screens are clear and well-labeled once you're on them, and the Apps Catalog and Deployments matrix are genuinely good "figure out what to do" views. But the top-nav doesn't hint at the Trigger-Release-vs-Promote distinction, so I went down the wrong path first, and the shared-session churn (not the app's fault, but it happened) made multi-step flows fragile.

**5. Rate usefulness/task-success 1-5.**
3. I did complete all three promotions and verified them. But "usefulness" for my specific job — coordinating a release train — is capped by the fact that the tool has zero concept of a promotion batch; I had to synthesize that myself.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes — "Trigger Release" is a genuinely confusing name for a build-cutting action, given that "release" in my world means "ship this to an environment." I clicked into it fully expecting a promote-bundle wizard and it took real digging (dry-running the checkbox tree) to understand it does something adjacent-but-different. Also small trust wobble: the promotion confirmation screen says "Actor: dev-user" while the top-nav badge says "developer" — probably the same identity under the hood, but the mismatched label made me pause.

**7. What's one thing you'd change immediately if you owned this UI?**
Give Promote the same batch mechanic Trigger Release already has internally — let me check multiple app/chart targets, pick one destination environment, and submit once, getting back a single Release/Batch ID that every resulting Promotion record references. The plumbing for a multi-target draft screen with a resolve step clearly already exists; it's just wired to the wrong verb (build) instead of the one I need (promote).

**8. What's something you thought was genuinely cool or delightful?**
The mandatory "dry run before promote-for-real is enabled, and any field change re-arms the requirement" guardrail is a genuinely good safety pattern for a tool where mistakes ship to prod. And the Apps Catalog's single-table dev/stage/prod-with-drift-flag view is exactly the kind of "what needs my attention" surface a release manager wants — I'd screenshot that and use it in a standup.

**9. Anything you expected to exist that wasn't there (e.g. a "release" or "batch" grouping concept)?**
Yes, precisely that — a "release train" or batch object that groups N promotions (not N builds) as one unit, with one combined reason, one combined audit record, and ideally one combined rollback. Trigger Release has the shape of this (multi-select checkboxes, a resolved draft, a grouping ID backed by a workflow) but it's pointed at the build step, not the promote step.

**10. One sentence recommendation — is this tool ready for coordinated multi-app release trains, or strictly single-app-at-a-time today?**
Strictly single-app-at-a-time for actual environment promotion today — the batch machinery exists in the codebase (Trigger Release proves it), it's just not yet wired to the promote/environment-progression workflow that a release train actually needs.

---


**Persona:** Priyanka Shah — Senior SRE, expert
**Task:** Find the one deliberately-seeded drifted chart in App Registry and fully reconstruct the incident: which child image drifted, current vs. declared version/digest, when/who/why it was overridden, whether it's still live, and what remediation options the UI offers.

## Narrative

I started at `localhost:8090` expecting a landing page that would either dump me into a raw table of promotions or, if I was lucky, actually foreground the thing I care about most as an SRE: what's broken right now. It was the latter. The Dashboard opened with a `⚠️ 1 drifted override(s) — an overridden image no longer matches its chart's pin` banner and a direct "Review in Deployments" link, plus a per-environment health strip that marked `stage` as "Drift" while `dev` and `prod` read "Healthy." Good — the drift is already localized to one environment before I've clicked anything.

Rather than following the dashboard's own suggested link, I noticed a top-nav item called "Drift & Audit" and went straight there, since that's exactly the forensic-first framing I wanted. That page paid off immediately: a "Drifted overrides" table with exactly one row —

- **Env:** stage
- **App:** manmanv2-control-api
- **Promoted (override):** v0.2.19 @ `sha256:140f601fb320…`
- **Chart currently pins:** `sha256:a022f86945af…`
- **Since:** 2026-08-23T06:31:46Z

Below it sat a second table, "Adopted artifacts" — every artifact in the system that was recorded by a human via `AdoptArtifact` rather than observed by CI, with an explicit disclaimer: *"Reason is logged server-side; cross-reference server logs for the full 'why.'"* That's an honest admission of a UI gap I clocked immediately: the audit page itself won't fully answer "why," even though it's the page literally named "Drift & Audit." I made a mental note to see if the per-app promotion history gave me more than this summary did.

It did. Drilling into `/apps/manmanv2-control-api`, I got a clean per-environment breakdown (Latest v0.2.20 Adopted, dev v0.2.20 Via chart, stage v0.2.19 **Override · Drift**, prod v0.2.19 Via chart), a "Current artifact by environment" table that explicitly annotated the stage row with *"Chart pin says sha256:a022f86945af… (mismatch)"*, and — the payoff — a "Promotion history" section with exactly one entry:

> `override — developer (you), "seed_tilt_walkthrough.py: populate local registry for a UI walkthrough"` — status **Pending**, with a `details` link.

So: who (developer/seed script), when (2026-08-23T06:31:46Z), and a "why" that's honestly just seed-data provenance, not a real incident narrative — which is exactly what I'd expect from a local Tilt seed, and the UI was upfront about that rather than inventing a fake justification. Following the `details` link to the full Promotion Details record gave me three sections: **Request** (requested by developer (you), action=override, same reason string), **Writeback** (Committed: *Not yet committed*; Sync triggered at: *Not yet triggered*; current sync/health: *—*), and **Sync history** (*No ArgoCD sync/health observations recorded yet*). This was the most important nuance of the whole investigation: this override sits in App Registry's database as "Pending" and has apparently never been written back to the GitOps repo or synced by ArgoCD. If that's accurate, this "drift" may be entirely a bookkeeping artifact — the live cluster might still be running whatever the chart declares, never having received this override at all. As a forensic SRE, that distinction (registry-drift vs. cluster-drift) is the single most load-bearing fact in the whole incident, and the UI does surface it, just not with any glossing or alarm — I had to know to look at "Writeback" to catch it.

I spent a good chunk of time getting knocked around by what the prompt had warned me about: other concurrent test sessions sharing this browser context. Direct navigations to `/charts/manmanv2-control-services` kept landing me somewhere else entirely — the Dashboard, the Deployments matrix, a totally unrelated chart page (`app-registry-app-registry`), even a broken `/promote` 400 page from someone else's action. None of these were destinations I'd asked for, so I read them as the documented shared-environment artifact rather than a product bug, and eventually got a clean load by re-issuing the same navigation until it stuck.

Once I did get to the `manmanv2-control-services` chart page, it resolved my remaining questions cleanly. The chart's environment strip showed dev v0.2.22, stage v0.2.22 (annotated right there: "manmanv2-control-api overridden — drift"), prod v0.2.21. Its per-version "Apps published in vX (env)" tables told the real story: chart v0.2.22 (what's live in stage) pins `manmanv2-control-api` at **v0.2.20**; chart v0.2.21 (what's live in prod) pins it at **v0.2.19** — the exact version of the override. In other words: the override wasn't crazy when it was made — it matched some chart pin at some point — but stage's chart got bumped from v0.2.21 to v0.2.22 afterward (bumping the control-api pin from v0.2.19 to v0.2.20) while nobody re-promoted control-api to follow it, so the stale override now reads as drift. Prod, still on the older chart v0.2.21, shows no drift purely because it hasn't caught up to v0.2.22 yet — a coincidence of timing, not health.

The app's Version History page confirmed there are only ever two artifact versions on record for this app (v0.2.20, v0.2.19), both provenance "Adopted," both built from synthetic `adopted:<uuid>` pseudo-builds rather than real CI runs — consistent with local seed data, but a real gap for genuine forensics (no commit SHA, no CI job link, no PR reference on either artifact). The artifact detail page for v0.2.19 added one more corroborating fact via its "Pinned by" table: it's pinned by chart v0.2.21 only, not v0.2.22 — confirming the mismatch mechanically, not just via a status flag.

Finally, remediation. I opened the Promote flow for `manmanv2-control-api → stage` and got an explicit, well-written warning: *"This app reaches stage only inside its chart's pin (Via chart). Promoting it directly here will be recorded as an override and tracked as drift the moment the chart's pin moves."* The version dropdown defaulted to v0.2.20 (the chart's current pin) with v0.2.19 also selectable, required a Reason field, required an acknowledgment checkbox, and gated "Promote for real" behind a mandatory "Run dry run" first. That's a genuinely good piece of guardrail design. But it also confirmed something slightly deflating: there's no "clear override, resync to chart" action distinct from promoting — even picking the version that matches the chart today creates a new override record that will drift again the instant the chart's pin next moves. I checked Rollback too, hoping for a cleaner path; it reported *"Nothing to roll back to — no prior promotion of manmanv2-control-api in 'stage' to roll back to,"* because this override is the only stage promotion this app has ever had. So functionally, the only "fix" the UI offers is: re-run Promote, pick v0.2.20, accept that it's still technically an override, and accept it'll flag again next time the chart moves — there's no first-class "adopt the chart's version and stop tracking this as an override" affordance.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a promotion ledger — a place that tells me what build is live where, with drift as a secondary derived signal I'd have to go hunting for, probably buried in a diff view somewhere. I expected to start on some kind of app or environment list and have to click into an app before I saw anything about drift at all.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → drift banner told me instantly there was exactly one issue and which environment. I skipped the dashboard's own "Review in Deployments" link and went to the dedicated "Drift & Audit" nav item instead, which gave me the app name, both digests, and a timestamp in one table. From there: app detail page → promotion history entry → promotion detail page (request/writeback/sync sections) → chart detail page (composition per version, drift annotation) → version history page → artifact detail page ("pinned by") → build detail page → Promote form (remediation) → Rollback form (dead end). The only real friction was environmental, not navigational: five or six times a direct URL navigation to `/charts/manmanv2-control-services` landed me on a completely different page — Dashboard, Deployments, an unrelated chart, even someone else's broken `/promote` 400 — because other concurrent sessions were reusing the same browser tab. I had to retry the same navigation repeatedly until it stuck. That's flagged in my brief as expected shared-environment noise, not a product bug, and I'm treating it that way, but it did cost real time.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Yes, essentially completely. Child image: `manmanv2-control-api`. Current override: v0.2.19 @ `sha256:140f601fb320…`. Chart's current pin (v0.2.22, live in stage): v0.2.20 @ `sha256:a022f86945af…`. When: 2026-08-23T06:31:46Z. Who: "developer (you)," i.e., a local seed script (`seed_tilt_walkthrough.py`) — clearly not a real human decision, and the UI didn't pretend otherwise. Still drifted: yes, actively, confirmed at three separate layers (app page, chart page, drift-audit page all agree). The one gap: I cannot tell from the UI alone whether this override was ever actually written to the GitOps repo or applied by ArgoCD — the Writeback section says "Not yet committed" / "Not yet triggered," which strongly suggests it's a database-only, GitOps-never-happened override, but the UI doesn't state that conclusion outright; I had to infer it.

**4. Rate navigability 1-5.**
4. Every page I actually landed on (when not hijacked by a concurrent session) had a clear, predictable next link — chart ↔ app ↔ artifact ↔ build ↔ promotion, all cross-linked. I never had to guess a URL by hand except when retrying hijacked navigations.

**5. Rate usefulness/task-success 1-5.**
5. I reconstructed the full incident picture — what, where, when, who, why (as far as it's recorded), current status, and remediation options — entirely from the UI, with corroborating evidence from at least three independent pages (drift-audit summary, app promotion history, chart composition tables) that all agreed with each other.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Two moments. First: prod shows `manmanv2-control-api` at v0.2.19 tagged "Via chart" (not "Override"), the same version number as the drifted stage override — for a second I wondered if prod was drifted too and just not flagged. It isn't; prod's chart (v0.2.21) legitimately pins v0.2.19, so it's coincidence, not drift, but the visual similarity (same version string, different provenance tag) is a place I'd want a newer engineer to slow down and read carefully. Second: the Promotion Details "Pending" status next to "Writeback: Not yet committed" left me genuinely unsure whether this override is live in the cluster at all — the page gives me the raw facts but doesn't state the conclusion ("this override has not been applied to the live cluster") in plain language, which is exactly the kind of ambiguity that costs time during a real incident.

**7. What's one thing you'd change immediately if you owned this UI?**
Make the Promotion Details "Pending / not yet committed / not yet synced" state loud and unambiguous — ideally a banner that says outright "This override has not been written back to GitOps and may not be live in the cluster," rather than three separate neutral-looking fields I have to interpret myself. During a real incident, "is this actually deployed" is the first question, and right now the answer requires inference.

**8. What's something you thought was genuinely cool or delightful?**
The Promote form's guardrail design: it tells you in plain English that promoting this app directly will be recorded as an override and will drift the moment the chart's pin moves, requires a reason, requires an explicit "I understand" checkbox, and forces a dry run before "Promote for real" unlocks. That's exactly the kind of friction I want on a production-adjacent action, and it's written for a human, not just enforced silently.

**9. Anything you expected to exist that wasn't there?**
A first-class "resolve drift" or "re-sync to chart" action that's distinct from "promote again as a new override." Right now the only way to un-drift this app is to run Promote, pick the chart's version, and accept that you've just created another override entry that will drift again on the next chart bump — there's no way to say "stop treating this as an override, go back to tracking the chart." I also expected — and didn't find — any link from the Promotion Details page out to an actual ArgoCD Application URL, which would have let me cross-check cluster reality directly instead of inferring it from "Writeback: not yet committed."

**10. One sentence recommendation on how well this UI supports deep drift forensics specifically.**
For reconstructing the who/what/when/where of a drift, this UI is excellent and unusually thorough — cross-linked chart/app/artifact/promotion pages all corroborate each other — but it stops just short of full incident support because it never explicitly tells you whether the drifted state is actually live in the cluster or just recorded in the registry's database.

---


**Persona:** Theo Baptiste — junior engineer, semi-technical
**Task:** At a 390x844 mobile viewport, get from the landing page to any app's current prod status, and separately try to open (not necessarily complete) a promote form. Note anywhere the layout breaks, overflows horizontally, has unreadably small tap targets, or where a table/matrix doesn't work at this width.

## Narrative

Okay, teammate pinged me on Slack asking "hey is the host-manager thing actually live in prod," and I'm on the train with one thumb free, so let's see what this "App Registry" tool actually looks like on a phone. I pull it up at http://localhost:8090.

First impression: the nav. It's not a hamburger menu, it's just... the whole nav, unrolled, vertically, as a plain list — Environments, Deployments, Apps, Builds, Reconcile Runs, Drift & Audit, Trigger Release — stacked one under another, eating close to 230px of vertical real estate before I've seen one pixel of actual content. And behind it, ghosted almost invisibly, is the "App Registry" wordmark, like it's sitting under the black nav bar instead of beside it. I had to squint to even confirm it said "App Registry." That's a rough opener on a phone.

I scroll down past the nav and land on a Dashboard — decent looking gradient header, three stat cards (Environments: 3, Drifted overrides: 1, Currently promoted: 19), then three env cards (dev/stage/prod) each showing "N promoted" and a Healthy/Drift pill, then a "Recent promotions" table. This part is actually fine — the cards stack in a single readable column. But I notice something nagging: there's a thin horizontal scrollbar down at the bottom of the visible screen, and when I full-page screenshot it, there's a huge dead zone of white space to the right of the actual content, like the whole page is secretly ~700px wide and my 390px phone is just showing the left slice of it. That's on literally every page I visited — Dashboard, Apps, Deployments, Builds, the Promote form. Something in the layout isn't actually constraining to the viewport, even where the visible content looks fine.

I tap into "Apps" to find host-manager's prod status. This is where it falls apart: the Apps Catalog is a wide table — App, Domain, Deploy unit, dev, stage, prod — six columns, and at 390px I can maybe see three of them before the rest gets clipped off the right edge, cut off mid-cell to "not e..." style truncation in one spot. The exact columns I need — dev/stage/prod — are the ones pushed off-screen. To read one row I'd need to scroll right, lose the app name off the left, scroll back... not what you want to do standing on a train.

(Small aside: I got redirected around a bunch mid-session — apparently other people/bots are poking at the same local instance right now, so my screen kept jumping to pages I didn't tap. Wild, but noted, not blaming the app for that part specifically.)

Eventually I land — after a few of those surprise jumps — on an actual single-app page for manmanv2-control-api. And this page is genuinely good on mobile: dev/stage/prod shown as three stacked cards, big bold version numbers, a colored "Via chart" / "Override · Drift" pill under each. I can see prod is v0.2.19 without any horizontal scrolling at all. So — task one, done, eventually, just not via the path (Apps Catalog table) that seemed like the obvious one.

Task two: open a promote form. There's no "Promote" button sitting on the app detail page itself, which surprised me. I had to go back into the Deployments matrix (another wide table, "one row per entity, one column per env," same overflow problem) and find a tiny "⬆" glyph — one character, no label — sitting right next to an equally tiny "↺" rollback glyph, packed into the corner of a table cell. Those two icons are genuinely hard to tell apart and hard to aim at with a thumb. I tapped it (well, navigated straight to the URL after a couple of failed clicks from page-jumps) and got the actual Promote form.

And credit where due — the Promote form itself, once open, is one of the better screens in this whole test. Version dropdown, a required "Reason" textarea with a plain-English explanation of why it's required, a clear warning banner ("prod is above dev (rank 20)... disabled until a dry run has been run"), and three stacked buttons: Cancel / Run dry run / Promote for real, with "Promote for real" greyed out until I run a dry run first. That's a legitimately good safety pattern, and it reads fine at 390px — nothing overlapping, nothing clipped.

So: a tool with two very different mobile personalities. The single-item detail pages and the Promote modal are basically fine. The list/matrix views that are supposed to be your entry point — Apps Catalog, Deployments — are not.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I figured it'd be some kind of dashboard — a place that tells you what version is running where. I expected to land on a home page with maybe a search bar or an app picker up top, since that's the fastest way to answer "what's live in prod for app X" on a phone.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Landed on Dashboard (had to scroll past a huge stacked nav first). Tapped into Apps Catalog looking for host-manager's prod status — got stuck immediately because the table's dev/stage/prod columns were clipped off-screen. Bounced through Deployments (same wide-table problem) and eventually reached a single-app detail page where the prod version was finally clearly visible as a card. For the promote form, there was no "Promote" button on the app page itself, so I had to go back into the Deployments matrix and find a tiny unlabeled up-arrow icon buried in a table cell, which I could barely tell apart from the rollback icon next to it. Got there in the end.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Yes, eventually — manmanv2-control-api was v0.2.19 in prod, "via chart." But I got there through a single-app detail page, not through the "browse all apps" table that seemed like the intended front door. If I hadn't stumbled onto that per-app page, I'd have been stuck scrolling a chopped-off table.

**4. Rate navigability 1-5.**
2. The nav takes up almost a full screen's worth of height with no collapse/hamburger, and the two main "browse everything" views (Apps Catalog, Deployments) are tables that just don't fit a phone.

**5. Rate usefulness/task-success 1-5.**
3. Both my tasks technically succeeded — I found a prod version and opened a working promote form — but neither succeeded via the obvious path. It took detours to get there.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
A couple of things. That phantom horizontal-scroll whitespace on every page made me wonder if content was missing or still loading — turns out it's just there, always, even on pages that look complete. And terms like "Adopted," "Via chart," "Override · Drift," "provenance," "chart pin" aren't things I'd know cold as a junior — there was a helpful yellow explainer box on the Environments page that clarified some of it ("requires_approval is a real field but the gate isn't built yet"), which I appreciated, but I wouldn't have found that box if I hadn't happened to land there.

**7. What's one thing you'd change immediately if you owned this UI (mobile-specific)?**
Collapse the top nav into a hamburger/drawer on small screens, and turn the Apps Catalog / Deployments matrix into the same stacked-card pattern the single-app page already uses instead of a wide table — that pattern clearly already exists in the codebase, it's just not applied to the multi-app views.

**8. What's something you thought was genuinely cool or delightful (mobile-specific, if anything)?**
The Promote form's dry-run gate — "Promote for real" is greyed out until you've run a dry run against the exact current form state, and it re-locks if you touch anything. On a small screen where it's easy to fat-finger the wrong field, that's a genuinely reassuring safety net, and the form itself laid out cleanly without me having to scroll sideways at all.

**9. Anything you expected to exist that wasn't there?**
A "Promote" action directly on the app's own detail page — instead I had to route back through the Deployments matrix and hunt for a one-character icon. Also expected the app search box on Apps Catalog to be more front-and-center as the mobile-first way to jump straight to one app, rather than scrolling a six-column table to find it.

**10. One sentence recommendation — is this tool usable on a phone today, yes or no?**
No — the individual app and promote screens are fine, but the nav and the two primary "see everything at once" table/matrix views are not usable on a phone as they stand today.

---


**Persona:** Sunny Park — curious hobbyist, non-technical, no business task
**Task:** Stumbled onto the "App Registry" tool's URL from a coworker's screen share; poked around purely out of curiosity to figure out what it is, who it's for, whether there's any in-app explanation, and whether I could explain it to a friend afterward.

## Narrative

Okay, so I typed in localhost:8090 not really knowing what I was about to see. First thing that loads is a page titled "Dashboard" with a big heading "App Registry" in the top left, and a sentence right under the page title that actually helped a lot: *"What's live, what drifted, what needs attention — across every environment."* Cool, so already I'm guessing: this thing tracks software running in different places (they call them "environments" — dev, stage, prod, shown right there as three little cards). There's also a "developer" label in the top right corner that I assumed was a "logged in as" indicator, but I clicked it and nothing happened — not a real button, just text sitting there. A little odd but not a big deal.

There's a scary-looking orange banner: "⚠️ 1 drifted override(s) — an overridden image no longer matches its chart's pin." I have genuinely no idea what an "override" or a "chart's pin" is at this point, but the tone is clear: something doesn't match something else and I should care. There's a "Review in Deployments" link, so I made a mental note and kept looking around first.

Below that: three environment cards (dev/stage/prod) each showing how many things are "promoted" and whether they're "Healthy" or have "Drift." Then a "Recent promotions" table — rows like "manmanv2-host-manager, prod, v1.1.0 @ sha256:074a2c8eed98…, 2026-08-23T07:33:47Z." The app names are clearly internal codenames (manmanv2, leaflab, friendly-computing-machine — that last one made me smile, it sounds like a joke name someone kept). At the very bottom, in small print: "Dashboard is read-only by design — every action here links out to the Deployments screen that owns it." That's a genuinely thoughtful touch — it's basically telling me "don't worry, you can't break anything from here."

The top nav has seven sections: Environments, Deployments, Apps, Builds, Reconcile Runs, Drift & Audit, Trigger Release. I went through all of them.

**Environments** — a table of dev/stage/prod with a description: "The environments apps can be deployed to, such as staging and production. Adding or archiving one is rare and restricted to admins." That sentence alone told me more than anything else on the page. But then the table itself got deeply technical fast — columns like "Requires approval" and "Promoter role," with little tooltip-style footnotes admitting things like "stored, not enforced — Promote does not check it" and "denied until it exists in Keycloak and is assigned." I don't know what Keycloak is, but I could tell this is an admin config screen for engineers, not for me.

**Deployments** — this is the "wow, that's a big spreadsheet" screen. Rows are apps/charts (with names I don't recognize, like demo-hello-fastapi, tools-app-registry), columns are dev/stage/prod, and each cell shows a version number plus a scary long hash, plus tiny up-arrow and circular-arrow icons I assume mean "promote" and "roll back." Lots of rows just say "not promoted," which I gather means it's registered but nothing's been shipped there yet.

**Apps** — a searchable catalog of every app/chart, grouped by "domain" (app-registry, friendly-computing-machine, leaflab, manman, manmanv2, tools). Clicking into one app (manmanv2-control-api) showed me a nice little breakdown: "Latest / dev / stage / prod" version chips, and one of them literally said "Override · Drift" in an alarming little tag. There was also a "Promotion history" list with an entry that said "override — developer (you), 'seed_tilt_walkthrough.py: populate local registry for a UI walkthrough'" — which was a fun little peek behind the curtain that this whole dataset is fake/seeded for a demo. Kind of delightful, actually, like finding a prop label on a movie set.

**Builds** — "CI run recording status — find out exactly what did and didn't finish." A long table of build/run IDs, most literally named like "#adopted:5211ee84-...". Very much internal engineering plumbing — I clicked into one and got redirected somewhere else entirely (see below).

**Reconcile Runs** — one single row, and a nice plain-English warning box: "A sweep rejected by the reconcile watermark as stale is expected, correct behaviour, not a bug." I appreciated that they anticipated someone going "wait is this broken?" and answered it directly on the page.

**Drift & Audit** — my favorite page by far, because it's the one place that actually explains its own jargon in the description text: "Overridden promotions that no longer match their chart's pin, and every artifact a human asserted rather than CI observed." So a "drift" is when someone manually overrides what version is running somewhere, and later the official recipe (the "chart") moves on without it — so now the override is out of sync. And "adopted" apparently means a human told the system "this version is what's running" instead of the system discovering it automatically from a build pipeline. Once I read this page, a lot of the little "Adopted" and "drift" tags scattered everywhere finally clicked into place.

**Trigger Release** — a giant tree of checkboxes for every domain/app/chart, with a note: "checking a domain releases everything under it; checking one app or chart releases only that target," plus an optional field to pin an exact digest instead of building fresh. This is clearly "kick off a CI build/release from the UI," which is a genuinely powerful, developer-facing action. I did not check any boxes or click "Resolve selection" — this isn't my system to release things on, and I have no idea what I'd be shipping.

I also, out of curiosity, followed a promote link (accidentally, via the shared-session weirdness described below) into an actual **Promote** form, and it was nicely cautious: a checkbox you must tick ("I understand this is an override and will be flagged as drift..."), a required "Reason" field, and a "Promote for real" button that stays disabled until you've run a "dry run" first. That's a genuinely good safety pattern — you can't fat-finger a production promotion.

One important thing to flag: I was told ahead of time that other automated test sessions might be sharing this exact browser, and that turned out to be very true. Multiple times I navigated to one page (like /environments or /deployments) and by the time I took a snapshot, I'd already been swept somewhere completely different — a Promotion Details page, a Chart page, even a bare unstyled error page at /promote that just said "missing or invalid 'owner'/'kind'/'env' query parameters" in plain text with no navigation back. I can't tell if that raw error page is a real product rough edge or just an artifact of someone else's test hitting a bad URL — but even if it's the latter, a bare, unstyled 400 page with no way back to the app is the kind of thing that would make me panic if it happened to me for real.

Last thing: I specifically went looking for a README, "What is this," or Help link anywhere in the app. There's nothing in the nav. I tried guessing URLs like /help and /about directly — both just quietly redirected back to the Dashboard (looks like the app has a catch-all route that shows Dashboard for anything unrecognized, rather than a real 404). So: no built-in explanation of the tool exists anywhere inside it. Everything I figured out, I figured out by reading page subtitles and hovering/reading little footnote-style captions under table headers, which — to be fair — were more helpful and more honest than I expected from something this technical.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
Honestly, no idea — the name "App Registry" made me think of an app store or a phone's list of installed apps. I expected to land on some kind of list of apps and click into one to "see" it. I did not expect a full internal deployment-tracking dashboard with drift detection and CI triggers.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → Environments → Deployments → Apps (catalog) → into one app's detail page → Builds → into one build (got swept elsewhere) → Reconcile Runs → Drift & Audit → Trigger Release → tried /help and /about directly, both bounced to Dashboard. I didn't get "stuck" on any one screen exactly, but I got repeatedly and involuntarily teleported between pages mid-exploration — clearly other test sessions sharing the browser — which made it hard to build a mental map of "where am I" at several points. I never actually submitted a promote or rollback, only viewed the forms.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Mostly yes, by the end. I can now explain it in plain terms (see Q10), but it took reading the Drift & Audit page's description text specifically — if I'd skipped that page I'd have been stuck guessing what "drift" and "Adopted" meant for much longer. There's no single "what is this tool" page anywhere, so I had to reverse-engineer the concept from scattered hints across five different screens.

**4. Rate navigability 1-5.**
4. The top nav is short, consistently present, and every page has a clear one-line description under its heading, which is unusually good practice. Losing my page mid-navigation repeatedly (shared session) hurt the experience but isn't really the app's fault.

**5. Rate usefulness/task-success 1-5.**
N/A for me really, since I had no task — but if I force myself to rate "did I walk away understanding the tool," I'd say 4. The self-documenting subtitles and footnotes did a lot of heavy lifting.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes — "chart," "pin," "override," "Adopted," "reconcile watermark," "promoter role," "Keycloak." None of these are explained on first use; you only learn them by happening to land on the Drift & Audit or Environments page, which spell a few of them out. Also the little "developer" text in the top corner looked clickable (cursor implied it might be) but did nothing when I clicked it — I assumed it was a user/account menu and it wasn't.

**7. What's one thing you'd change immediately if you owned this UI?**
Add one real "What is this?" page (even three sentences) linked from the nav or the App Registry logo, explaining: this tracks what version of each app/chart is supposed to be running in dev/stage/prod, ArgoCD is what actually deploys it, and this is just the source-of-truth ledger. Everything else on the site would make far more sense read *after* that framing instead of readers having to assemble it themselves from six different table captions.

**8. What's something you thought was genuinely cool or delightful?**
Two things. One: the app literally tracks itself — I found "app-registry-app-registry" as an entry in its own catalog, which felt like a fun bit of self-reference. Two: the Promote form's safety design — you must tick an "I understand this will be flagged as drift" box, write a mandatory reason, and run a dry run before the real submit button even unlocks. That's a thoughtful guardrail for something that could otherwise be a scary "click and it's live" button.

**9. Anything you expected to exist that wasn't there?**
A help/about/README page, as covered — I checked /help and /about directly and both silently fell back to the Dashboard rather than a real 404, so there's no signal anywhere that "this route doesn't exist" versus "this route wasn't found, have the homepage instead." I also expected clicking the "developer" text top-right to do something (log out, switch user, show profile) and it didn't.

**10. One sentence recommendation — could you explain what this tool is/does to a friend after this session, yes or no?**
Yes: it's an internal engineering dashboard that keeps track of exactly which version of each piece of a company's software is supposed to be running in each environment (dev/testing/production), flags when someone's manual override has drifted out of sync with the official recipe, and lets engineers safely promote or roll back versions and kick off new builds — but you'd need to already be an engineer to actually use it, since the tool itself never explains its own vocabulary up front.

---


**Persona:** Grace Liu — Engineering Director (multi-domain, semi-technical, was an IC years ago)
**Task:** Get a fleet-wide health snapshot (how many domains/apps are drifted or behind org-wide, which domains are healthiest/riskiest) using only the summary-level screens — Dashboard, Apps Catalog, Deployments matrix — under a 15-minute time budget, without drilling into individual apps unless forced to.

## Narrative

Okay, fifteen minutes, let's go. I land on `/` and it's actually titled "Dashboard" — good, that's where I'd have gone anyway if nobody told me. Subhead says "What's live, what drifted, what needs attention — across every environment." That's literally my Slack message to leadership, so I'm hopeful.

First thing I see is a banner: "⚠ 1 drifted override(s) — an overridden image no longer matches its chart's pin." Fine, one thing to know about. Below that, three stat tiles: Environments (3), Drifted overrides (1), Currently promoted (all envs) (19). Then three cards — dev/stage/prod — each with a promoted count and a Healthy/Drift badge. Stage is the only one flagged "Drift." That's a clean, fast read. Fifteen seconds in, I already have a headline: "one drift, in staging, everything else green." I could say that out loud in a meeting.

But — and this nagged at me immediately — this is sliced by *environment*, not by *domain*. My board update isn't "how's staging doing," it's "which of our product areas (manmanv2, leaflab, app-registry, friendly-computing-machine, manman) are healthy." Nowhere on this dashboard does it say "5 domains, 4 healthy, 1 at risk." So headline number: half-answered. I know the fleet-wide drift count. I don't know which *domain* owns the risk without reading the recent-promotions table by eye and pattern-matching app-name prefixes.

I scanned the "Recent promotions" table hoping for a shortcut — it's just the last 10 promotion events, most recent first, no domain column, no aggregation. I can see "manmanv2-host-manager," "leaflab-leaflab," "friendly-computing-machine-bot-services" scroll by, which tells me those domains are active, but it's an activity log, not a health rollup.

So I went to Apps Catalog, since the nav promised "Every registered app and chart, and what 'promotable' means for each." There's a domain dropdown — app-registry, friendly-computing-machine, leaflab, manman, manmanv2, tools — great, exactly the org units I think in. But when I select one, it doesn't summarize; it just filters the same giant flat table down to that domain's rows, and the table itself is dense: App / Domain / Deploy unit / dev / stage / prod, one row per *component*, not per domain. For app-registry alone there are five rows (the chart plus four "image (via chart)" sub-apps that all say "not promoted" — which reads exactly like it's NOT deployed, when actually it's deployed via the chart row above it). That's a landmine for someone skimming: at a glance it looks like four things are broken in app-registry, when actually only the chart row matters and the rest is normal. I had to stop and remind myself "oh right, those are sub-components, not separately-shipped."

Honestly, at this point I got a stray navigation — the page jumped to a chart detail screen I hadn't clicked into, then a moment later to a `/promote` error page. Nothing I did. I'm told other test sessions share this browser, so I'm not docking the product for it, but in a live meeting-prep scenario that kind of surprise jump would have cost me real minutes and a mini heart-attack ("did I just click promote?!").

I went to Deployments next, billed as "one row per promotable entity, one column per environment." This is the most information-dense screen and probably closest to what a platform engineer needs, but for my purposes it's actually a downgrade from the Dashboard: no domain column at all, entities sorted alphabetically (demo-* stuff mixed in right next to real product domains), and — this is the part that actually worried me — I can see with my own eyes that `app-registry-app-registry` is on v0.0.35 in dev/stage but v0.0.34 in prod, and `manmanv2-control-services` is v0.2.22 in dev/stage but v0.2.21 in prod. That's "prod is behind" — exactly the kind of thing I came here to count — but the UI doesn't flag it as a state at all. There's no "behind" badge, no color, nothing. Only the one true drift case (manmanv2-control-api, stage, override mismatch) gets a badge. I would have to eyeball every row's three version strings and diff them myself to find "behind" cases. Under time pressure, I will miss some of these.

I clicked into Drift & Audit for one gut-check, since it seemed like the closest thing to an audit trail. It confirmed the same "1" drifted-override number as the dashboard (nice, that's consistent) and named exactly the case: `manmanv2-control-api` in stage, promoted v0.2.19 vs. chart now pinning a different digest. Good, that one thing I can say with total confidence. But the rest of that page is a long chronological list of "who manually adopted what," not a health rollup either.

I peeked at Environments too, out of curiosity — that turned out to be admin config (approval flags, GitOps paths, promoter roles), completely irrelevant to what I needed, and it even has little inline footnotes admitting some of its own fields aren't enforced yet. Not a place an exec should ever need to go, and thankfully I wasn't forced to.

By minute twelve I stopped digging. My honest, defensible headline: fleet-wide, 19 things are currently promoted across 3 environments, 1 confirmed drift (manmanv2, staging), all three environments read "Healthy" except stage. Domain-by-domain risk ranking is not something this tool handed me directly — I inferred "manmanv2 is today's flagged domain" only because both the drift case and the one behind-prod case I noticed happened to be manmanv2 apps, and I can't be fully sure I didn't miss a similar case in leaflab or manman just from eyeballing the matrix.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a "deploy status board" — something like a status page per product/domain, ideally with a single fleet health number or a red/yellow/green grid I could screenshot into a slide. I expected to land on a dashboard and expected it to already be sliced by team/domain, since that's the unit I manage in, not by environment.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard first (good instinct, it's literally the landing page) — got a clean environment-level headline in seconds. Then Apps Catalog to try to get a domain-level view — found the domain filter but it only filters a dense per-component table, doesn't summarize. Got knocked sideways by two stray navigations (a chart page and a `/promote` 400 error) I didn't trigger — lost maybe a minute reorienting. Went to Deployments hoping for the "matrix at a glance" promised in the nav label — found real detail (versions per env) but had to manually eyeball version-string diffs to spot "prod is behind," and there's no domain column there either. Checked Drift & Audit to confirm the one drift case — got a clean, specific confirmation. Peeked at Environments, immediately recognized it as admin config and backed out.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Partially. I can confidently say "1 drifted override, in staging, in manmanv2" and "19 things promoted across 3 healthy-ish environments." What I couldn't get with confidence was a domain-by-domain breakdown ("app-registry: clean, leaflab: clean, manman: not even promoted anywhere, manmanv2: 1 drift + 1 stale-prod, friendly-computing-machine: clean") — I had to infer most of that by pattern-matching app-name prefixes across two different tables myself, and I'm not fully confident I didn't miss something in manman or leaflab since nothing surfaces "behind" as a labeled state.

**4. Rate navigability 1-5.**
3. The top nav is short and sensibly named, and the Dashboard genuinely is the right landing page. But there's no path from "I think in domains" to "show me domains," and I got derailed twice by navigation I didn't initiate.

**5. Rate usefulness/task-success 1-5.**
3. Good enough for the one true drift case and the environment-level snapshot — I'd say those two sentences out loud with confidence. Not good enough for "which domains are healthiest/riskiest," which was the actual ask, because that requires domain-level rollup that doesn't exist anywhere at the summary layer.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Yes — twice. First, "not promoted" on sub-component rows in the Apps Catalog (app-registry-api, app-registry-migration, etc.) reads exactly like "broken/not deployed," when it actually just means "this piece ships bundled inside its chart, don't track it separately." Second, the version-skew I spotted myself in the Deployments matrix (prod trailing dev/stage by one patch version) isn't labeled as anything — it's not called "behind," it's not colored, it's just... two different version strings sitting next to each other, and I only noticed because I was looking for it. If I'd been moving faster I'd have missed it entirely and reported "everything's fine" when it isn't quite.

**7. What's one thing you'd change immediately if you owned this UI?**
Add a domain rollup to the Dashboard — one row or tile per domain (app-registry, leaflab, manman, manmanv2, friendly-computing-machine, tools) with a count of drifted/behind items and a Healthy/Drift/Behind badge, the same pattern already used for the dev/stage/prod cards. That's a small addition given the per-env cards already prove the team knows how to build this pattern — it just needs to also cut by domain, not only by environment.

**8. What's something you thought was genuinely cool or delightful?**
The Dashboard's honesty about its own limits — the closing line "Dashboard is read-only by design — every action here links out to the Deployments screen that owns it" told me immediately I wasn't going to accidentally fat-finger a promotion from a summary screen. And the drift count being identical and cross-referenced between the Dashboard badge and the Drift & Audit page (down to the same app/env) told me the underlying numbers are internally consistent, which is exactly the kind of trust signal I need before I repeat a number to my boss.

**9. Anything you expected to exist that wasn't there (e.g. a single fleet-wide health score/number)?**
Yes, exactly that — a single fleet-wide health score or percentage ("94% of promotable entities are in expected state") and, more importantly for my job specifically, a per-domain breakdown. Right now "health" is scoped only to environment (dev/stage/prod), never to domain/team, even though "domain" is clearly a first-class concept elsewhere (it's a filter field, a column in Apps Catalog). Also no explicit "prod is N versions behind stage" indicator anywhere — I had to compute that myself from raw version strings.

**10. One sentence recommendation — could you walk into that leadership meeting confident in what you learned, yes or no?**
Yes, but only for a narrow claim ("we have one known drift, in manmanv2 staging, everything else looks environment-healthy") — I would not walk in confident ranking domains by risk, because that number doesn't exist on the summary screens and I'd effectively be guessing from app-name prefixes.

---


**Persona:** Baxter Wells — Platform/chart maintainer, owns the `manmanv2-control-services` shared Helm chart
**Task:** Before bumping a shared dependency in `manmanv2-control-services`, determine the chart's current blast radius — which child app images it pins, at what versions, across dev/stage/prod, whether any of those are currently drifted from an override — and separately check whether the UI offers a reverse index (given one child image, list every chart that includes it).

## Narrative

I start at `http://localhost:8090` cold. Landing page is a "Dashboard" — clean, and it immediately tells me something useful without me asking: a banner reading "⚠️ 1 drifted override(s) — an overridden image no longer matches its chart's pin," plus a per-environment health strip (dev: Healthy, stage: Drift, prod: Healthy). Good first impression — drift is surfaced before I even go looking for it.

I want to find my chart. There's no "Charts" item in the top nav (Environments / Deployments / Apps / Builds / Reconcile Runs / Drift & Audit / Trigger Release), so I go to Apps, which turns out to be the actual catalog of both apps and charts together, filterable by domain and by "deploy unit" (chart / image / none). I filter mentally by scanning and find `manmanv2-control-services` — domain `manmanv2`, deploy unit `chart`, with per-env versions: dev v0.2.22 (Adopted), stage v0.2.22 (Adopted, flagged "drift"), prod v0.2.21 (Adopted). That's my chart, and it's exactly the "multiple child images, currently drifted" example I was told to look for.

Trying to click into it is where things got weird — and this is where I hit the "shared browser" behavior I was warned about. Four separate attempts to open the chart page (typed URL twice, one click) bounced me instead to Deployments, then Dashboard, then Drift & Audit, then Environments — pages I never asked for. As a real engineer hitting this cold, I'd have assumed for a minute that clicking a chart row was broken or redirecting somewhere nonsensical. It resolved on retry, so I'm treating it as the documented multi-agent artifact rather than a real navigation bug, but it did cost me several round-trips and, in the moment, genuinely eroded my confidence that the app was behaving deterministically.

Once I landed on `/charts/manmanv2-control-services` it was worth the wait. The page shows, per environment, the promoted chart version and an inline callout: stage explicitly says "manmanv2-control-api overridden — drift" right next to its version. Below that, three separate tables — "Apps published in manmanv2-control-services v0.2.22 (dev)", "...v0.2.22 (stage)", "...v0.2.21 (prod)" — each listing exactly what that chart version pins: app name, version, digest, provenance. My chart currently composes 5 child apps: `manmanv2-control-api`, `manmanv2-control-migration`, `manmanv2-event-processor`, `manmanv2-log-processor`, `manmanv2-manmanv2-ui`. Dev/stage (both on chart v0.2.22) pin control-api v0.2.20 and ui v0.2.20; the other three are v0.2.19 across the board. Prod, still on the older chart v0.2.21, pins v0.2.19 for everything including ui. There's also a separate "Currently declared composition (chart_app)" section at the bottom — explicitly labeled as "what the manifest says it composes right now, independent of what any promoted version actually pins" — which is precisely the distinction I need before a bump: today's declared shape vs. historical promoted pins. That's a genuinely well-designed feature for exactly my use case.

For the drift detail, the dedicated Drift & Audit page was even more precise than the chart page: one row, "stage / manmanv2-control-api / Promoted (override): v0.2.19 sha256:140f601fb320… / Chart currently pins: sha256:a022f86945af… ". Cross-referencing the Adopted-artifacts table below it, that pinned digest is control-api v0.2.20. So: someone manually promoted control-api v0.2.19 directly into stage, bypassing the chart, and it's now one version behind what the chart declares. Total blast radius of drift for my chart: exactly one image, one environment, one version-step. Confirmed a third time on the app's own page (`/apps/manmanv2-control-api`), which shows a per-env table with the stage row tagged "Override · Drift" and an explicit "Chart pin says sha256:a022f86945af… (mismatch)" annotation. Three independent screens agreeing on the same fact — I trust it.

Last task: the reverse lookup. On the artifact detail page for control-api's v0.2.20 digest, there's a section titled "Pinned by" with the subtitle "Charts that pin this app's image — an image artifact is contained, it doesn't contain." — a table of (Chart, Chart version) pairs. For this digest it listed just `manmanv2-control-services v0.2.22`. To make sure it wasn't just echoing the one obvious link, I checked a second digest — `manmanv2-control-migration`'s v0.2.19, which I knew from the chart tables was pinned by both chart v0.2.21 (prod) and v0.2.22 (dev/stage) — and the Pinned-by table correctly returned two rows, one per chart version. So the mechanism is real and aggregates properly; it's just scoped to one specific digest at a time, not "every chart that has ever included any version of this app." To get a full historical picture for an app I'd have to walk its version history and check each digest's Pinned-by table individually — there's no single "all charts including app X, ever" rollup.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a registry/catalog tool — something like an internal CMDB for "what build is where." I expected to start by searching for my chart by name and landing on a page that shows its declared composition and current deploy state side by side. That's roughly what happened, though I expected a top-level "Charts" nav entry, not charts buried as rows inside a combined Apps/Charts catalog table.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → Apps Catalog (found my chart's row, saw drift flagged) → tried to open the chart's detail page and got redirected to Deployments, then Dashboard again, then Drift & Audit (unintended, but useful — it happened to have the drift record I needed), then Environments (also unintended) — four failed attempts before the fifth navigation actually landed on `/charts/manmanv2-control-services`. From there it was smooth: chart page → app page (`manmanv2-control-api`) → artifact page (checked "Pinned by" twice, on two different digests) to verify the reverse index. No dead ends once I was past the redirect issue.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Yes, fully. My chart pins 5 apps; I know the exact version+digest each promoted chart version pins per environment; I know precisely one child image (control-api, in stage) is drifted and by how much (one version behind); and I confirmed a working reverse "which charts pin this digest" index, with the caveat that it's per-digest, not per-app-across-all-history.

**4. Rate navigability 1-5.**
3. The information architecture, once you're on the right page, is excellent — clear tables, inline drift annotations, cross-linking between chart/app/artifact. But there's no charts index or search, chart pages are one click removed from any obvious global entry point, and I hit repeated stray redirects trying to get there (attributed to the shared test environment, but still real friction from where I was sitting).

**5. Rate usefulness/task-success 1-5.**
4. I left with a complete, cross-confirmed, actionable blast-radius picture and verified the reverse lookup works. Dinged one point because the reverse index requires digest-by-digest checking rather than a single "show me every chart, past or present, that has ever included app X" view — for a chart owner doing due diligence across a long-lived shared dependency, that's a real gap.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Briefly — prod pins control-api at v0.2.19 and is *not* flagged as drift, while stage also shows v0.2.19 and *is* flagged. At first glance that looks inconsistent. It resolved once I understood drift is relative to what the *currently-live chart version in that environment* declares, not some absolute "latest" — prod is still on chart v0.2.21, which itself declares v0.2.19, so prod is correctly un-drifted; stage is on v0.2.22, which declares v0.2.20, so its v0.2.19 override is drifted. The word "Adopted" is also overloaded — it's used both as a provenance tag (human-recorded vs. CI-observed) and appears alongside promotion-state badges, which took a second read to disentangle.

**7. What's one thing you'd change immediately if you owned this UI?**
Add a top-level "Charts" nav item with its own searchable index page, instead of making chart rows something you have to spot inside the combined Apps Catalog table.

**8. What's something you thought was genuinely cool or delightful?**
The inline "Chart pin says sha256:a022f86945af… (mismatch)" annotation directly on the drifted table cell on the app page — it hands you the exact answer to "what should this be instead" without making you cross-reference two screens yourself. The "Currently declared composition (chart_app)" section, explicitly separated from "what any promoted version actually pinned," is also a genuinely sharp distinction for exactly the pre-bump reasoning I came here to do.

**9. Anything you expected to exist that wasn't there (e.g. a true reverse "which charts include this image" lookup)?**
Partially — the reverse lookup exists and works correctly (verified it aggregates multiple chart versions for a shared digest), but it's scoped to one artifact digest at a time. A true "every chart, across every version, that has ever pinned app X" rollup at the app level (not just the digest level) wasn't there.

**10. One sentence recommendation on whether this UI is good enough for real pre-bump blast-radius analysis.**
Yes for the forward question (what does my chart pin, where, and is it drifted — that's thorough and cross-confirmed across three screens), and good-enough-with-caveats for the reverse question (works, but only one digest at a time, so exhaustive cross-chart sharing analysis takes extra manual digging).

---


**Persona:** Noor Aziz — Incident Commander, expert, mid-incident
**Task:** Alert says "elevated error rate across manmanv2 services in prod, unclear scope." Determine, as fast as possible, whether any manmanv2 app/chart is currently drifted or was recently promoted/changed in prod, and name exactly which ones.

## Narrative

War room's live, three people staring at me waiting for scope. I've got maybe a minute of goodwill before I bail to `kubectl` and ArgoCD directly. I open `http://localhost:8090` cold — no bookmark, no idea what this tool even looks like.

First thing that renders is a **Dashboard**. Good sign — that's exactly where I'd want to land. Title says "What's live, what drifted, what needs attention — across every environment." That's my exact question, phrased almost word for word. I don't even have to think about where to click first.

Top of the page, there's a big orange alert banner: **"⚠️ 1 drifted override(s) — an overridden image no longer matches its chart's pin"** with a link straight to Deployments. Below it, three environment cards: dev (6 promoted, Healthy), stage (7 promoted · 1 drifted, Drift), prod (6 promoted, Healthy). So at a glance: prod shows "Healthy" as a whole, and the one drift is sitting in stage. That's already a partial answer — no drift currently active in prod overall — but I need the manmanv2-specific breakdown, not the environment-wide rollup.

Then I see a "Recent promotions" table, sorted newest-first, no filter applied. Top row: **manmanv2-host-manager, prod, v1.1.0, 2026-08-23T07:33:47Z**. That's the single most recent promotion event in the whole system, and it's a manmanv2 app going to prod. My pulse ticks up. I check the clock (`Date.now()` in-browser reads 07:42:25Z) — that promotion landed about **9 minutes before I'm looking at this screen**. That is exactly the kind of thing an IC needs to know in the first 60 seconds.

I click through to **Deployments** to get the full matrix and confirm scope properly instead of eyeballing a rollup card. This is the payoff screen: one row per promotable entity, one column per env, with per-cell "Adopted" and "drift" badges. I scan for `manmanv2*` rows:

- **manmanv2-control-services** (chart): row-level "drift" badge is showing. Dev = v0.2.22 (Adopted, no drift). **Stage = v0.2.22 (Adopted, drift flagged)**. Prod = v0.2.21 (Adopted, no drift flag). So: drifted, but the drift is in **stage**, not prod. Prod is one version behind stage/dev, which is normal trailing-release behavior, not drift.
- **manmanv2-host-manager** (image): v1.1.0 in dev, stage, AND prod, all "Adopted," no drift badge anywhere. This is the one that was promoted to prod 9 minutes ago per the dashboard's recent-promotions table.
- I don't see a separate `manmanv2-control-api` row on this matrix — I saw it referenced as an app elsewhere (probably rolled up under manmanv2-control-services as a chart component), but I don't chase that thread; scope, not root cause, is my job right now.

I tried to click into the app detail page for manmanv2-host-manager to see who/what triggered that prod promotion (human vs CI, in case that's a fast root-cause tell for the room) — and the browser jumped me to `/environments` instead, a page I never clicked toward. Re-navigating to `/deployments` directly got me back to where I was. This is the "shared local browser, multiple test agents" artifact I was warned about, not something I'm going to hang a product complaint on — but mid-incident, one unexplained navigation like that is enough to make me stop trusting the tab and go double-check the URL bar every time, which costs seconds I don't have.

At this point I have my answer and I'm not going to keep digging for flavor. Total: dashboard load → 1 click (Deployments) → done, roughly 45–60 seconds including the stray-navigation hiccup, 2 real clicks plus 2 direct URL navigations.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
Honestly I expected an internal tool like this to dump me on some generic list of "apps" and force me to hunt for a prod+manmanv2 filter, or worse, a raw table with no rollup at all. I was pleasantly surprised it opened straight to a dashboard framed around "what drifted, what needs attention."

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Loaded `localhost:8090` → read the dashboard alert banner + env health cards + recent-promotions table (all still on the landing page, no click needed) → clicked "Deployments" from the nav → scanned the matrix for manmanv2 rows → tried to click into the host-manager app page for provenance and got redirected to `/environments` instead (shared-browser cross-talk) → re-navigated directly to `/deployments` to confirm my read. The only real backtrack was that stray redirect, and it wasn't the app's fault as far as I can tell.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Yes. manmanv2-control-services is drifted in **stage**, not prod (prod is just one version behind, which is expected trailing-release lag). manmanv2-host-manager was promoted to prod at 07:33:47Z — about 9 minutes before I checked — with no drift flag. So: no active drift in prod for manmanv2, but there IS a very recent prod change on manmanv2-host-manager, which is exactly the "recent change" hypothesis I'm supposed to chase down with the room right now. The one thing I didn't get and would've liked in under 10 more seconds: who/what triggered that host-manager promotion (human vs CI-observed) — the redirect ate my one attempt at that.

**4. Rate navigability 1-5.**
4. The dashboard-first design with a direct "Review in Deployments" link off the drift alert is genuinely well-aimed at exactly my use case. Docked one point for the stray navigation and for having no direct "filter to manmanv2 only" control on the matrix — I had to eyeball-scan a full table.

**5. Rate usefulness/task-success 1-5.**
5. I got a scoped, confident, correct-looking answer inside a minute without leaving the browser, which is the bar for "keep this open during an incident."

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Two things. First, "Adopted" as a badge — I initially read it as some kind of health/status label before realizing (from the environments page footnote I glanced at) it's about provenance (human-adopted vs CI-observed), not incident status. Second, the environment-card "Healthy" label for prod on the dashboard is doing double duty — it means "no drift detected," not "no recent changes" — and I almost stopped there before checking recent-promotions and finding the 9-minutes-ago prod change. If I'd trusted "Healthy" as the full picture I'd have told the room the wrong thing.

**7. What's one thing you'd change immediately if you owned this UI?**
A one-click "domain = manmanv2" filter/pill on the Deployments matrix and dashboard, so I'm not pattern-matching row prefixes by eye under pressure. Also surface "last promotion age" as a relative time ("9 min ago") next to the raw ISO timestamp — I had to open devtools-adjacent tooling (evaluate `Date.now()`) to do the subtraction myself, which nobody mid-incident should have to do.

**8. What's something you thought was genuinely cool or delightful?**
The drift alert banner linking straight to the exact screen that resolves it ("Review in Deployments") — that's a real "don't make me think" touch, and it's rare in internal tools.

**9. Anything you expected to exist that wasn't there (e.g. a "filter to just this domain, just prod, just recent changes" one-click view)?**
Yes, exactly that — no domain filter, no env-only filter, no "changed in the last N hours" view. I got away with eyeballing a ~15-row matrix this time; at real scale (dozens of services per domain) that scan doesn't stay under 60 seconds.

**10. One sentence recommendation — is this fast enough to keep open during a live incident, or would you bail to kubectl/ArgoCD?**
I'd keep this tab open for the initial "what changed and where" scope call — it beat my 60-second budget — but I'm still opening ArgoCD/kubectl in the next tab over to confirm actual pod/rollout state, because this tool tells me what *should* be where, not what's actually running right now.

---


**Persona:** Lena Kowalski — Platform Engineer (senior IC, heavy prior ArgoCD UI experience)
**Task:** First-encounter usability playtest of "App Registry" at http://localhost:8090, explicitly comparing its mental model, terminology, and workflows against ArgoCD's UI — looking for overlap that causes confusion, additive value, and whether a team with ArgoCD already open would get anything incremental from this.

## Narrative

I typed `localhost:8090` in expecting... honestly, I wasn't sure. Something ArgoCD-adjacent, given the name "App Registry" and the framing. My prior at the last job: ArgoCD's UI is an app tree — you land on a grid of Application tiles, each with a Sync Status pill and a Health Status pill, and you drill into a tile to get the resource tree, the diff, and the sync/rollback history. So my first move was to just look at what the landing page put in front of me.

It redirected me to `/deployments`, titled "Deployments," and the shape immediately clicked as *ArgoCD-tile-grid-adjacent but rotated 90 degrees*: instead of one tile per Application, this is a matrix — one row per "promotable entity" (a chart or an image), one column per environment (dev/stage/prod). Each cell shows a version, a digest, a badge, and two inline actions: ⬆ promote, ↺ rollback. That's a genuinely different and, I'll say up front, useful shape for the specific question "what's live where" across many services — ArgoCD's tile grid doesn't answer that at a glance across environments, because in a typical ArgoCD setup each environment is a *separate* Application (or even a separate ArgoCD instance), so you'd need three tabs open side by side to get what this one page gives me. Good start.

But then I hit the first badge that worried me: a banner at the top said "⚠️ Drift detected — an overridden image no longer matches its chart's pin in at least one row/environment below," and down in the `manmanv2-control-services` row, both the dev and stage cells had a small "drift" tag next to "Adopted." If I were skimming this with one eye on a real incident, "drift" is a loaded word for me — in ArgoCD, drift means the live cluster state has diverged from what's declared in Git, i.e., someone `kubectl edit`'d a Deployment or a controller mutated something ArgoCD didn't put there. My gut reaction seeing "drift" on a promotion tool is "something in the cluster doesn't match Git, go look at the OutOfSync diff." That is not what's happening here.

I dug into the Drift & Audit page (`/drift-audit`) to check my assumption, and to the tool's credit, it says this almost verbatim: *"Not a cluster check — the registry only knows about its own promotion + chart-pin records."* So App Registry's "drift" = an environment's promoted override (a human said "put v0.2.19 here instead of what the chart says") no longer matches what the chart's current manifest pins for that app. It's a **registry-internal bookkeeping drift**, not a cluster-state drift. I appreciated that the page proactively disclaims it, but the badge itself, sitting in the exact visual slot where ArgoCD would put a Sync Status pill, is going to fool someone on a bad day who doesn't click through to read the fine print. Same word, same visual weight, different axis entirely.

I clicked into the app page for `manmanv2-control-api` (the one flagged) to see the promotion-level view. Good page — "Latest / dev / stage / prod" summary strip, per-environment current-artifact table with an explicit "Chart pin says sha256:a022f86945af… (mismatch)" caption on the stage row. That's a genuinely clear, well-labeled explanation of the *specific* mismatch, better than the terse "drift" badge alone. Then I noticed the promotion history entry underneath said "override — developer (you)... **Pending**" with a timestamp over an hour old. I clicked "details" to see what "Pending" meant.

That promotion-details page is where App Registry's relationship to ArgoCD got most interesting — and most honest. It has a "Writeback" section with fields: Committed (Not yet committed), Sync triggered at (Not yet triggered), **Current sync status** (—), **Current health status** (—), and a "Sync history" section saying "No ArgoCD sync/health observations recorded yet." Those last two field names are lifted straight from ArgoCD's own vocabulary — Sync Status and Health Status are literally ArgoCD's two headline pills. So this isn't an accidental collision like "drift" was; this is App Registry *intentionally* reaching into ArgoCD's actual sync/health signal and trying to surface it inline on the promotion record, presumably once the GitOps writeback commits and ArgoCD picks it up. In this seed data it was all empty, so I couldn't see it populated, but architecturally that answers my core question well: the *intent* is for this page to save you the tab-switch to ArgoCD to confirm a promotion actually synced and went healthy — not to replace ArgoCD's own diff/health view, just to echo the outcome back into the promotion record for audit purposes. That's a legitimately additive idea if it's wired up. Right now, on this Pending promotion, it left me with a genuine "is this stuck, or just slow, or did the demo seed data intentionally leave it half-finished?" — there's no explicit stuck/blocked indicator the way ArgoCD gives you a spinning refresh icon or an explicit OutOfSync/Progressing state machine.

I checked Environments next (`/environments`) expecting something like ArgoCD AppProjects/RBAC. It's a flat admin table: dev/stage/prod with Rank, "Requires approval," "Allowed principals," GitOps path, Promoter role. And again, refreshingly, the column headers self-disclose their own limitations: "Requires approval — stored, not enforced, Promote does not check it," "Allowed principals — does not restrict who can promote, the promoter role check is the only access control in force." I have never seen a UI so aggressively honest about which of its own fields are decorative. As a platform engineer that's oddly trustworthy — usually I have to go read the source to learn that a checkbox does nothing — but it's also a flag that this thing is mid-build, not a finished product yet.

Reconcile Runs (`/reconcile-runs`) turned out to be unrelated to ArgoCD's reconciliation loop despite the name overlap — it's "the identity pipeline... this tracks app/chart identity, not published artifacts," i.e., a CI job (`ci.yml`) that walks the repo and tells the registry what apps/charts exist. Different axis again from ArgoCD reconciliation (which continuously reconciles live cluster state against Git). Third instance of vocabulary overlap with a materially different referent.

Builds and Trigger Release were unambiguously *not* ArgoCD territory — CI run recording, and a full release-trigger UI with a per-domain/per-app checkbox tree that kicks off a Temporal workflow to build and version-bump things. I actually opened a stale Release Status page left over from someone else's session (`/releases/d1a27da9-...`) and it showed a **Failed** state with a raw exception dumped straight into the "Error" table cell — a Go error string, `resolve plan: activity error (type: ResolvePlan...) workspace root not found...`. That's clearly a real backend failure surfaced verbatim, not massaged for end users — a rough edge, but also proof this tool does something ArgoCD flatly does not: it drives the *build* side of the pipeline, not just the deploy side.

I tried the Promote form's dry-run gate (`/promote?env=stage&kind=chart&owner=manmanv2-control-services`) — nice detail, "Promote for real" is disabled until you run a dry run against the exact current form state, and any field change re-arms the requirement. Conceptually parallel to ArgoCD's "diff before sync," which I like. But when I clicked "Run dry run" with the Reason field still empty, nothing visibly happened — no diff output, no toast, no inline validation telling me the reason was required first. That's the first moment all session I didn't trust what the screen was telling me — not because a label lied, but because *nothing* told me anything, and I couldn't tell if it silently failed or silently succeeded.

One more thing worth logging: several times mid-session my navigation jumped to a page I hadn't clicked (once landing on a `Chart: manmanv2-control-services` detail page after I'd clicked "Drift & Audit," another time landing on an `Artifact:` detail page after clicking "Builds"). Per the framing I was given, this is expected in a shared local environment with other concurrent test agents driving the same browser context, not something I'm treating as a confirmed single-user bug — but it did cost me a couple of re-navigations by direct URL to get back on track.

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
Given the name and the framing — "records which build/artifact is promoted to which environment, plus promote/rollback and drift detection" — I expected a promotion ledger sitting *above* ArgoCD in the pipeline: something that tracks intent (what should be where) while ArgoCD tracks and enforces reality (what is where). I expected to land on some kind of app list or environment overview, and I expected at least one screen to directly reference or link out to ArgoCD, since the two tools are clearly meant to be used together.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Landed on the root, got redirected to `/deployments` (the promotion matrix). Read the drift banner, drilled into the flagged `manmanv2-control-services` chart page, then the affected app (`manmanv2-control-api`), then a specific Promotion Details page to chase down a "Pending" status. Backed out to `/drift-audit` to check my assumption about what "drift" meant here (correct instinct — it's a different axis than ArgoCD drift). Checked `/environments` for an RBAC/AppProject equivalent, `/reconcile-runs` (name overlap but unrelated concept), `/builds`, `/releases/trigger`, and a stale `/releases/{id}` status page. Tried the Promote form's dry-run button and got no visible feedback, which was the one spot I genuinely backtracked on, unsure if I'd done something wrong. Twice the page navigated somewhere I hadn't clicked, mid-session, which cost a couple of stray glances before I re-oriented by URL — consistent with the shared-browser-context caveat I was given going in, not something I could pin on the product itself.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Mostly yes. I came in wanting to know whether this duplicates or complements ArgoCD, and the Promotion Details page's "Current sync status / Current health status / Sync history" fields (empty here, but clearly designed to be filled from ArgoCD's own signal once writeback lands) answered that architecturally: it's meant to *echo* ArgoCD's outcome into the audit trail, not replace ArgoCD's own diff/health UI. What I didn't get: a populated example of that sync/health data, since every record I found was seeded pre-writeback (Not yet committed / Not yet triggered). I also never got clarity on why the one "Pending" promotion I found had been sitting for over an hour with no visible next step or stuck-indicator.

**4. Rate navigability 1-5.**
4. The top nav is small, flat, and consistent (Environments / Deployments / Apps / Builds / Reconcile Runs / Drift & Audit / Trigger Release), and cross-linking between chart pages, app pages, artifact pages, promotion pages, and build pages is genuinely dense and correct — I could always click through to the next relevant record. Docked a point for the dry-run button giving zero feedback, and for a couple of pages (Reconcile Runs, Builds) whose names strongly suggest ArgoCD-adjacent concepts but turn out to mean something else, which costs orientation time on first visit.

**5. Rate usefulness/task-success 1-5.**
4. As a cross-environment promotion ledger with real provenance tracking (CI-observed vs. human-adopted artifacts), a declared-vs-promoted composition diff for charts, and a CI-trigger/release-orchestration layer, this does real work ArgoCD has no opinion on at all. I'm knocking off a point because the one workflow state I actually needed to resolve (a stuck Pending promotion) didn't give me an actionable next step, and the dry-run silent-no-op dented my confidence in the promote flow specifically.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you (specifically: any ArgoCD-adjacent term that means something different here)?**
Three real ones. (a) "drift" — in ArgoCD this is live-cluster-vs-Git; here it's promoted-override-vs-chart-pin, a purely registry-internal check the page itself calls out as "not a cluster check." Same word, same alarm-red visual treatment, different meaning — the single riskiest overlap in the whole app. (b) "Reconcile Runs" — sounds like ArgoCD's continuous reconciliation loop; it's actually a periodic CI job that updates the registry's knowledge of *which apps/charts exist in the repo*, unrelated to deployment state. (c) The Promote form's silent dry-run click — not a jargon problem, a trust problem: I clicked the button, the reason field got focus, and nothing else happened, with no error telling me why.

**7. What's one thing you'd change immediately if you owned this UI?**
Rename or visually distinguish the registry-level "drift" badge from anything that reads as cluster/ArgoCD drift — even just "override drift" or "pin mismatch" instead of bare "drift," plus a persistent one-line disclaimer near the badge itself (not just on the dedicated audit page) so it can't be misread in the two seconds someone glances at the matrix during an incident.

**8. What's something you thought was genuinely cool or delightful?**
The self-disclosing column headers on the Environments page — "Requires approval (stored, not enforced — Promote does not check it)," "Allowed principals (does not restrict who can promote)." I have never seen a tool admit its own dead fields directly in the UI instead of making me go read the source or get burned by them. It's unusual honesty and it built trust in everything else on the page.

**9. Anything you expected to exist that wasn't there (relative to what ArgoCD offers)?**
A resource-level diff/manifest view — ArgoCD's bread and butter is showing you the actual YAML diff between live and desired state, resource by resource. App Registry never shows me a manifest at all; it operates one level up, at "which version/digest is promoted," and simply hands off to ArgoCD (once wired) for the rest. That's a legitimate scope boundary, not a gap, but I went in half-expecting some manifest surface and there genuinely isn't one — nor should there be, on reflection. I also expected, and didn't find, a direct outbound link from a Promotion Details page to the corresponding ArgoCD Application — that would have been the single highest-value addition for someone who already lives in both tools.

**10. One sentence recommendation — additive alongside ArgoCD, confusingly redundant with it, or somewhere in between?**
Solidly additive — it answers a question ArgoCD structurally can't (cross-environment promotion intent, provenance, and CI/release orchestration in one ledger) — but it borrows just enough of ArgoCD's own vocabulary ("drift," "sync status," "health status") on visually loaded badges that a team running both needs to actively teach new hires which "drift" they're looking at before it causes a bad-day misread.

---


**Persona:** Marcus Iyer — Power user / platform engineer, uses App Registry many times a day
**Task:** Hunt for power-user affordances (keyboard shortcuts, bulk operations, sortable columns, URL deep-linking, favorites/recents) in the App Registry UI at http://localhost:8090, and actually complete a promotion to verify the real workflow speed.

## Narrative

I start at `localhost:8090` expecting a dashboard, and I get one — clean enough. Top nav: Environments, Deployments, Apps, Builds, Reconcile Runs, Drift & Audit, Trigger Release. Dashboard says flat out "Dashboard is read-only by design — every action here links out to the Deployments screen that owns it." Fine, that's an honest signal. It tells me where the real work happens.

First thing I do on any new internal tool: mash the shortcuts I use everywhere else. `?` for a shortcuts overlay — nothing, page just silently sits there (and once appeared to navigate me elsewhere, more on that below). `Ctrl+K` for a command palette — nothing, no dialog, focus stays on `<body>`. `/` to jump into search — nothing, focus doesn't move. I confirmed this properly by dispatching the keydown events directly in JS and checking `document.activeElement` and for any `[role=dialog]` before/after — genuinely nothing intercepts these keys anywhere in the app. Same for `j`/`k` list nav and `g`-then-letter go-to — dispatched the events, checked the URL and focus, no change. There is no keyboard layer in this app at all. Everything is mouse-and-click.

Side note on methodology: this environment is shared with a bunch of other automated test sessions hitting the same browser context, and it showed — my page jumped out from under me repeatedly (an innocent `?` keypress landed me on `/apps`, then `/environments`; a `Ctrl+K` attempt landed me on an artifact detail page; a filtered `/apps?domain=leaflab` request got hijacked mid-flight to a `/promote` URL someone else was on). I re-ran the keyboard tests using direct JS event dispatch specifically so I could get a clean, uncontaminated read on whether the app itself has key handlers — it doesn't. That part I'm confident about independent of the noisy environment.

Next: the Deployments screen, which is clearly the actual product — "one row per promotable entity, one column per environment. Promote or roll back an app directly from its cell." This is the money screen. Each cell has a version, a digest with a copy button, an "Adopted" badge, and two tiny icon links: ⬆ (promote) and ↺ (rollback). I immediately look for a checkbox column — there isn't one. No row selection, no "select all," no bulk-promote-this-version-to-stage-across-12-apps action. If I need to promote five apps to stage after a release train, I am clicking ⬆ five separate times, filling in a reason five separate times, and running a dry run five separate times. That's rough at scale.

I do like that the ⬆/↺ links are real hrefs: `/promote?env=stage&kind=chart&owner=manmanv2-control-services`. That means the matrix cells are deep-linkable and I could bookmark or script around specific promote targets even without a bulk UI. Similarly the Apps Catalog page has a working filter that actually rewrites the URL — I typed "manman" into the search box, and typing alone did nothing (no live filter, it's not reactive), but clicking "Filter" navigated to `/apps?q=manman&domain=&unit=` and the table correctly narrowed from 32 rows to 15. That's a real, bookmarkable filter state, which is more than a lot of internal tools bother with. Good instinct from whoever built this, even if it needs a keystroke shortcut to reach it faster.

I checked the column headers on both the Apps Catalog and Deployments tables for sortability — computed `cursor` style was `auto` on every `<th>`, no button/link inside any of them. Not sortable. Confirmed via the actual DOM, not just eyeballing it.

I checked whether the "developer" text in the top-right corner is a user menu (looking for anything like recents, favorites, saved views) — it's a bare `<span>` with no click handler, no role, cursor `auto`. Not a menu. There is nothing resembling recently-viewed apps or a favoriting/starring mechanism anywhere I found — not on the Apps Catalog, not on individual app or chart detail pages, not in a sidebar.

I went looking at Builds next, since a 30+ row CI-run table is exactly the kind of place a power user wants sort/filter. It has a single "Workflow run id…" lookup box requiring an exact ID plus a "Look up" button — no fuzzy search, no column filter, no sort, and no visible pagination on what's already a long list. If I only remember the app name and roughly when a build ran, this box does nothing for me.

Drift & Audit was the one place I actively hoped for bulk action — a page literally titled "Drifted overrides" listing entities whose promoted image no longer matches their chart's pin sounds like a natural candidate for "resolve all" or multi-select re-sync. It's read-only: a table of drifted rows and a separate table of "Adopted artifacts" (things a human asserted rather than CI observed), each linking out to per-entity detail pages. No bulk resolve, no checkboxes there either. The page even says outright: "No adopt control lives here... This screen is read-side audit only."

Finally I ran an actual promotion end-to-end to time the real workflow: `manmanv2`/`app-registry-app-registry` chart, prod, v0.0.34 → v0.0.35. Landing on the promote form via its deep link auto-selected the newest version and auto-focused the Reason field — nice touch. But the flow itself is: fill Reason (required for anything above dev, "becomes part of the permanent history") → click "Run dry run" (this is a hard gate — "Promote for real" stays disabled until a dry run has been run against exactly this form state, and touching any field re-arms the gate) → review the dry-run result inline → click "Promote for real" → land on a confirmation page with the full promotion record (ID, valid-from timestamp, what it superseded, actor, reason). It worked, it's honest about what happened, and the audit trail it hands back is genuinely good. But it's two full round-trips per single target per single environment, with zero ability to fan that out to multiple apps or multiple environments at once, and zero keyboard path to it (had to mouse to two separate buttons).

## Interview Answers

**1. Before you clicked anything: what did you expect this tool to do, and where did you expect to start?**
I expected a dashboard-first "what's live where" view — a status board — with a fast path from there into the actual promote/rollback actions I do dozens of times a day. I expected to land somewhere with a command palette or at least a searchable jump-to, because that's table stakes for anything I'm going to live in.

**2. Walk through what you actually did, step by step — where did you get stuck or backtrack?**
Dashboard → tried `?`, `Ctrl+K`, `/`, `j`/`k`, `g`+letter (all dead, confirmed via direct event dispatch) → Deployments matrix (found the real promote/rollback UI, no checkboxes) → Apps Catalog (found working URL-param filtering, no live-as-you-type search, no sortable headers) → Builds (ID-only lookup, no sort/filter on a 30+ row table) → Drift & Audit (read-only, no bulk resolve) → ran a full promote (dev-user reason → dry run → promote for real → confirmation). No real backtracking beyond the shared-environment noise knocking me onto other pages mid-task, which I had to route around by re-navigating and re-verifying state with JS rather than trusting the click flow blindly.

**3. Did you get the actual answer you came for? If not, what was missing or unclear?**
Yes — I got a definitive, evidence-backed answer: there is no keyboard shortcut layer, no command palette, no bulk-select/bulk-promote anywhere, no sortable columns, no favorites/recents. The one genuine power-user affordance that does exist is URL-based deep-linking (both the filter bar and the per-cell promote/rollback links produce real, bookmarkable URLs), which I wasn't sure I'd find and was glad to confirm.

**4. Rate navigability 1-5.**
3. The seven top-level sections are clear and the matrix layout on Deployments is genuinely well thought out for reading state at a glance. But there's no fast lane between them — every hop is a mouse click through the nav bar, there's no search-everything, and long tables (Builds, Adopted artifacts) have no in-page way to narrow down besides an exact-ID box.

**5. Rate usefulness/task-success 1-5.**
4. I completed a real promotion, start to finish, and the data model behind it (dry-run gating, mandatory reason above dev, full audit record with supersession info) is more rigorous than most internal tools bother with. It answers "what's live where" clearly. It loses a point purely on throughput — one entity, one environment, per trip, always.

**6. Was there any moment you didn't trust what the screen was telling you, or where labels/jargon confused you?**
Not from the app's own copy — actually the opposite, it over-explains itself in a way I appreciated (e.g., the Environments page footnote spelling out that "Requires approval" is stored but not enforced, and "Allowed principals" is stored but reads by nothing). What I didn't trust was the browser state itself, because of the shared-session interference — pages kept jumping to URLs I hadn't clicked. I want to be fair to the product here: that's a test-environment artifact, not something I'm pinning on the app.

**7. What's one thing you'd change immediately if you owned this UI?**
Multi-select checkboxes on the Deployments matrix with a bulk "promote selected to env X" action that still runs the same dry-run-then-commit gate, just batched. Release trains touch five to ten apps at once for me; doing that one ⬆-click and one typed reason at a time is the single biggest tax this tool imposes on my day.

**8. What's something you thought was genuinely cool or delightful?**
The dry-run gate that re-arms itself the instant you touch any field, so "Promote for real" can never fire against a form state it didn't just validate. That's a small, unglamorous guardrail that actually prevents a real class of mistake (promoting the wrong version because you tweaked something after the dry run). Also the confirmation screen's "Superseded" line telling you exactly what it replaced and until when — that's the kind of audit detail I'd otherwise have to go dig for.

**9. Anything you expected to exist that wasn't there (which specific power-user affordance is most missed)?**
A command palette (Ctrl+K) is the single biggest miss — with seven top-level sections and dozens of apps/charts, "type app name, hit enter, land on it" would save me more time than anything else on this list. Bulk multi-select promote is a close second.

**10. One sentence recommendation — is this a tool you could live in all day efficiently, or does the lack of power-user affordances make it a "occasional visit" tool only?**
The underlying data model and the promote/audit workflow are solid enough that I'd trust it, but with zero keyboard shortcuts and no bulk actions it's currently an "open it, do one thing, close it" tool rather than something I could live in all day.

---

