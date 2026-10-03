# krill/ui — the operator web UI

How this binary's pages are built: the two layers it runs, the render
seam every page goes through, the htmx conventions it follows, and the
constraints a new page has to respect.

Read this before adding a page, adding an htmx branch, or changing
anything under `krill/ui/`. The design language itself — the *why* behind
these rules — is `//libs/go/htmxui/ARCHITECTURE.md`; this file is how
krill applies it.

## The two layers on one binary

`ui` is two things at once, and the boundary between them is the first
thing to get right.

**The auth front door.** `krill/mcp`'s auth needs somewhere to send a
not-yet-signed-in caller, and that is this binary's `/login`. `ui` mounts
`auth.Provider`'s OAuth2 authorization-server endpoints, so MCP clients
can authorize against it. See
`../ARCHITECTURE/20-krill-ui-mcpauth-front-door.md`. **None of these are
shell pages and none may ever be wrapped in `components.Shell`:**

| Route | Why not a shell page |
|---|---|
| `/login`, `/auth/login`, `/auth/callback`, `/logout` | The sign-in flow itself; it has to render before there is a signed-in user to put in the chrome. |
| `/authorize`, `/token`, `/register`, both discovery documents | Reachable before any credential exists. |
| `POST`/`GET` `/credentials`, `DELETE` `/credentials/{id}` | `MountSelfServe`'s **JSON** self-serve API, not a page. The credentials *page* (`/account/credentials`) is htmx and talks to the same `auth.CredentialStore` directly (`credentials_page.go`), not to this API. |
| `/healthz` | JSON. |
| `/favicon.ico` | A static asset, unauthenticated on purpose. |

**The operator shell.** Everything registered in `mountShellRoutes` — the
home page, the product-scoped areas, and the pre-redesign URLs mounted
from `legacyURLs`. All of it is behind `app.auth.RequireAuthFunc`.

`credentialsPath` is `/account/credentials`, **not** `/credentials`, and
that is not an inconsistency: `MountSelfServe`'s `{id}` wildcard outranks
a page registered anywhere under `/credentials`, and the two would panic
the binary at boot. Do not "tidy" it.

## The render seam

Everything a page renders goes through one of three functions in
`templ_render.go`. Handlers do not write HTML themselves.

| Function | Use it for | Status |
|---|---|---|
| `app.renderShell(w, r, title, activePath, body)` | A full page in the shared chrome. | 200 |
| `app.renderShellStatus(..., status int)` | A full page that is a real 400 / 404 / 500 **rendered inside the chrome** rather than a bare `http.Error`. | the given code |
| `renderFragment(w, r, c)` | A bare component with no chrome — the `HX-Request` half of a one-route-two-modes branch. | always 200 |

**Why the status code lives in Go and not in a component.** templ
components are body-writers; they have no status concept. So the status
necessarily lives in the seam, which is exactly the capability the
pre-templ `renderShellStatus` already had. That is why the spec area's
bad-product-id page and the intervention-rejection page still work.

**The seam is the whole chrome.** `app.renderShellStatus` is a method
because it is not just a document wrapper any more: it resolves the
current product, builds the grouped sidebar, reads the Needs-attention
badge, and passes `app.productSwitcherData(r)` through. Every signed-in
page gets the drawer sidebar, the Product select, the toast host, and the
signed-in identity from this one call, and no page has per-page work to
get any of it.

Two consequences worth knowing before editing it:

- **`activePath` is a nav key, not always the URL.** A page passes its own
  `r.URL.Path` unless its URL is not one of the nav's own — the shell
  home renders the Overview, whose nav key is the product's overview URL.
- **The product is read off the path, not off the router.**
  `shellPathTargets` pulls `{pid}` and the container under it out of the
  URL, which is what lets a milestone-scoped page light Tasks and link at
  that milestone's own pages with no work of its own. A resolver's answer
  wins over the path; an un-prefixed page falls back to
  `rememberUnprefixedProduct`.

**Passing `nil` for the switcher compiles and ships a broken sidebar.**
That trailing argument exists only so routes kept compiling during the
cutover; there is no correct reason to pass nil.

## The Overview

`/` and `/products/{pid}/overview` serve the same page
(`overview_page.go`, `pages/overview.templ`): the product as its heading,
a status badge per container in flight, and the `Review N escalated
tasks` action. It is the shell's home, so it is the one page both the
legacy `/` and the product-scoped prefix reach.

**In flight means two of the eight statuses**, `in design` and `in
progress` (`milestoneInFlight`). `designed` and `planned` are up next and
`partially complete` is stalled — work that stopped, which is the one
status that most looks like progress and is not it. A product with
nothing in flight says so in a sentence rather than rendering an empty
panel. Milepebbles are listed beside their milestones, because a cut
milestone that is merely "designed" while its milepebbles are in progress
is a product being built.

The escalated count is read once and carried on the request
(`withEscalationBadge`), so the primary action and the sidebar's badge are
one read rather than two that could disagree. An unreadable count renders
no action and says nothing about escalation at all — "nothing is
escalated" would be a second unverified claim. The action points at the
console's escalated view, which is the Escalated tab Needs attention
serves today.

The four stat tiles (`overview_tiles.go`) sit under the header: Escalated,
Claimed, Open notes and Blocking questions, each a link to whatever it
counts. Every figure covers the current product across all its milestones —
a `ProductID` narrowing and no milestone one, since an escalation in another
milestone is still something this operator must see.

**Each sub-line comes from the same read as its own figure.**
`store.CountConsoleOverview` counts each queue and its sub-line over one
FROM/JOIN/WHERE, the sub-line being that clause with one conjunct added
(escalation time, lease expiry, note kind) — so "N leases expire within 10
min" cannot drift from the claimed count above it. The windows
(`OverviewRecentEscalationWindow`, `OverviewLeaseExpiryWindow`) are measured
from one explicitly-passed instant, so one render answers for one moment and
a test can hold the clock still.

The Escalated tile shows the sidebar badge's own figure rather than a
second read of the same rows: the badge, this tile and the unfiltered
Escalated tab are required to be one number, and two reads is how two
numbers happen. Each tile carries its own figures and its own failure, so a
read that fails costs that tile alone rather than the whole strip — and a
queue that could not be counted renders as a message, never as a `0`.

The Needs-attention panel and the in-flight panel are separate work and
render their slots on this page empty until they land.

**Why the body is buffered before `WriteHeader`.** `renderShellStatus`
renders the component into a buffer *first*, then commits the status. A
component that fails to render therefore leaves the response unwritten,
rather than committing a status and then truncating the body — which is
what the old code did.

**`buildHead` and the load-order trap.** `buildHead()` is krill's
`CustomHead`: the no-FOUC theme bootstrap, then Tailwind, then daisyUI,
then `htmxui.ThemesCSS` — in that order. `ThemesCSS` maps daisyUI's CSS
variables to krill's palette, so it **must** load after the daisyUI
stylesheet. `htmxbase` renders `CustomCSS` *before* `CustomHead`, so
`ThemesCSS` must go in `CustomHead`, never `CustomCSS`; loading it first
makes the palette override silently lose to daisyUI's defaults with no
error anywhere. `templ_render_test.go` guards the order — do not
"simplify" it out.

htmx core and Alpine are loaded by `htmxbase`'s own base layout, before
`CustomHead`, so anything appended to `buildHead` is already ordered after
core.

## The package split

| Package | Owns |
|---|---|
| `krill/ui` (package `main`) | Routing, the `App` struct, the write path, the render seam, `nav.go`'s grouped sidebar table and its active-path rule, `overview_page.go`'s Overview frame, `overview_tiles.go`'s stat-tile builders, and **every pure view-model builder**. |
| `krill/ui/components` | The chrome: `Shell`/`sidebar` (the workspace shell's drawer sidebar and grouped nav), `navLink`, `SubNav`, `ProductSwitcher` (the sidebar's product select), `ToastHost` (the one live region every mutation confirms through), and the `MilestoneStatusStyle` status vocabulary. |
| `krill/ui/pages` | Page bodies, one `.templ` per area, each declaring its own view-model struct. |

**Builders stay in `package main`; only the structs and the components
move to `pages`.** That split is deliberate: it keeps the existing
package-main tests compiling unchanged, and `spec_parity_test.go` — which
is the guard that a page and the matching MCP tool show the same spec —
reflects over the builders, so it must keep being able to call them
directly.

## Adding a page, end to end

1. **Path constant** in `routes.go`, inside the area's prefix. If it is a
   new top-level area, add a group to `navGroupTable` too.
2. **View-model struct** in the area's `.templ` file in `pages`, beside
   the component that renders it. Use exported field names.
3. **Pure builder** in the area's `.go` file in `package main`, returning
   `pages.X`. It reads through `app.spec` / `app.tasks` / `app.designSessions`.
4. **The component**, taking the view model. Compose `htmxui` primitives;
   do not hand-roll a badge, an alert, an empty state, or a confirm.
5. **The handler**, calling `app.renderShell`.
6. **Register it** in `mountShellRoutes` behind `app.auth.RequireAuthFunc`
   (or `app.operatorRoute` if it writes). If it is a **pre-redesign** URL,
   it belongs in `routes.go`'s `legacyURLs` table instead — see below.
7. **An `HX-Request` branch** if it is a list or a view that benefits —
   see below.
8. **A test** asserting the data reaches the page, and an entry in
   `nav_test.go`'s `requiredAreas` if it is a new area.

## Legacy URLs: the pre-redesign table (`legacyURLs`)

Every URL the operator UI facelift replaces is registered from one table,
`legacyURLs` in `routes.go`, mounted by `mountLegacyRoutes`. Each entry
names exactly one destination:

- **`Serve`** — the URL's existing page, rendered inside the shell at 200.
- **`Successor`** — where the URL goes once its redesigned page ships.
  Retired so far: the per-milestone **task list** and **board** (FR
  f41a352d) and the per-milestone **task detail** (FR 0c03eac1).

So the phase that replaces a page moves its URL from `Serve` to
`Successor` and changes nothing else. That is the point: a replaced page's
old link cannot go dark, because the URL was already accounted for in one
table rather than being a route registration someone has to remember to
redirect.

A `Successor` is a `func(*App, *http.Request) (target string, ok bool)`.
`ok` is false only when the ids in the old URL cannot be resolved to build
the target — an un-prefixed URL must always land somewhere, so that case
renders the product index rather than redirecting to nowhere. Redirects are
**302**, not 301: a pre-redesign URL stays a live link an operator may keep
following, and 301 lets a browser pin the old URL in its cache past the
page it now names.

**A successor decides whether it carries the old URL's scope.** The list
and board carry `{mid}` into a `scope=` / `container_id=` query, so a
bookmarked milestone URL still opens *that* milestone's work rather than
the whole product's. The detail does not: it carries the `tid` alone,
because the page it retires into resolves the task's own container from the
task. Reusing `legacyTaskSuccessor(suffix)` for the detail would land the
operator on a list, which is the wrong page for a link that named a task.

Do **not** redirect a URL to a page that has not shipped. Those render a
placeholder, so an operator following a working "what is escalated?" link
would land on a page saying nothing is there yet. The Overview is the one
redesigned page P1 ships, and it replaces no old URL: `/` and
`/products/{pid}/overview` are both its own addresses.

`legacy_urls_test.go` holds the acceptance. It walks every pre-redesign
URL the FR names — spelled out as literals, **not** derived from the
production table, since a test iterating the table it polices passes just
as well after an entry is deleted — asserts none 404s, and follows any
redirect to a page rendering the **full workspace chrome** at 200 (the
drawer shell, the grouped nav with its group headings, and the Product
select), not merely a 200 with an HTML body.

Four checks hold that walk to the contract, and each catches something the
others do not:

- **`TestPreRedesignURLsResolve`** — none 404s, and each renders the
  whole shell.
- **`TestPreRedesignURLsRenderNoReadFailure`** — none carries an error
  alert. A fixture that answers a read with a failure produces a
  well-chromed 200 whose body is an error, which a status-and-chrome check
  cannot see; its `_HasTeeth` case proves the check still fires.
- **`TestLegacyTableAndLiveRoutesAgree`** — the table and the live mux
  agree in **both** directions. A pattern the table names but the mux does
  not serve is a URL the FR believes is kept alive and nothing is; a
  pre-redesign URL resolving through a pattern the table does *not* name
  is one outside the retirement mechanism, so replacing its page would
  leave the old link serving the old page forever. Either half alone
  passes on the other's failure mode.
- **`TestLegacyRedirectUsesFoundAndNotMoved`** — mounts the real table
  with one real entry moved to `Successor` and asserts 302 (not 301), the
  `Location`, that the successor renders in-shell, and that no other entry
  broke. It goes through `mountLegacyTable` rather than a hand-wired
  probe mux, so it exercises the wiring a phase actually inherits.

Adding a URL to `legacyURLs` without adding it to the literal list fails
`TestLegacyTableAndLiveRoutesAgree`; so does registering a pre-redesign
route anywhere outside the table.

`mountLegacyRoutes` delegates to `mountLegacyTable(mux, table)` and
`mountShellRoutes` to `mountShellPages(mux)` purely so a test can mount a
doctored legacy table alongside the real shell pages.

## The `HX-Request` branch: one route, two modes

krill does **not** use a `/partials/` prefix. One route serves both:

```go
if r.Header.Get("HX-Request") != "" {
    renderFragment(w, r, pages.ClaimedResults(data))
    return
}
renderShell(w, r, "Claimed tasks", opsClaimedPath, pages.ClaimedPage(data))
```

The page composes the *same* fragment, so the two modes cannot drift
apart.

`HX-Redirect` is the one legitimate redirect on an htmx path, and only
for a **navigation to a different page** (cancel-confirm, open-session
success). It is *not* a way to avoid re-rendering a fragment you could
have re-rendered in place.

## The 200-re-render error rule

**Every outcome — success, refusal, or a state you could not read —
answers 200 with the fragment re-derived from freshly observed state, and
the error inline via `htmxui.Alert`.**

- Never 422. htmx does not swap on an error status, so a 4xx leaves the
  operator staring at an unchanged page with no explanation.
- Never an assumed "success" render. Re-read the state; a write that
  succeeded may have changed what the page should say.
- Never a redirect on a swap path (see the exception above).

When a *list* call fails, render the error in place of the list rather
than 500-ing the page.

**Every path an `hx-*` attribute can reach needs this branch — including
the ones that look unreachable.** A doubled form's no-JS branch and its
htmx branch share a handler, so it is easy to leave `http.Error` on a
transport-failure path and have it become silent the moment htmx is
driving. The dangerous ones are the failures an operator most needs to see:
api unreachable, session mint failed, the re-read behind a form
re-render failing. A 401/502 there means the operator's submit appears to
do nothing at all.

**Re-render the form, not a bare error, and keep what the operator
typed.** If the read behind the form also failed, the form can still be
handed back with its error set and the typed text and ticked ids intact —
`renderOpenFormFailure` / `renderAnswerFormFailure` do exactly this, with
a `degradedForm` closure for the case where the page body itself could not
be re-read. Losing someone's paragraph because a list call blipped is a
worse outcome than showing them a list that is missing its rows.

**Never render a read failure as an empty view.** An empty `Rows` renders
a confident, wrong "No claimed tasks." — indistinguishable from success.
Set `Error` on the fallback branch so the operator is told the view could
not be reloaded.

## The doubled-form rule

Every mutating form carries **`method` + `action` *and* `hx-post` +
`hx-target` + `hx-swap`**.

```templ
<form method="post" action={ templ.URL(c.Action) }
      hx-post={ templ.URL(c.Action) }
      hx-target="#ops-results" hx-swap="outerHTML">
```

The no-JS branch is the old behaviour verbatim — a 303 back to the
originating view on success, an in-shell error page on refusal. That is
why the existing `StatusSeeOther` + `Location` assertions in
`interventions_test.go` and `design_write_test.go` never had to change.
Keep it that way: a form that only works with JavaScript is a
regression, and this binary's whole point is that it is the thing that
works when nothing else does.

Destructive controls stay a plain `<a href=".../cancel/confirm">`, never a
form — cancel asks first.

## Polling

**Only where there is a genuine transient state.** Today that is exactly
one place: `/ops/claimed`, where a claim's lease can lapse and the row
should leave the view on its own.

The self-terminating pattern: the poll attributes are a **pure function of
observed state**, emitted only while the state is transient. Because the
endpoint's response *is* the same fragment, a settled view comes back
without them and the loop stops by itself — no client-side timer
bookkeeping. Model it on `deploymentRowPollAttrs` in
`manmanv2/ui/pages/deployment_row.templ`.

**The "near expiry" window must be a fraction of the lease, never the
lease.** `ClaimTask` sets `lease_expires_at` to `now + DefaultLeaseDuration`
and `HeartbeatTask` resets it to the same, so *every* row the store can
return satisfies `LeaseExpiresAt - now <= DefaultLeaseDuration`. A
whole-lease horizon therefore makes the predicate `len(rows) > 0`, and any
deployment with a claimed task — including a swarm that heartbeats
forever and never actually nears expiry — re-queries Postgres every three
seconds, indefinitely. `krill/ui/ops.go` uses
`DefaultLeaseDuration / 3`; `TestClaimedPollingHorizonIsAFractionOfTheLease`
pins it, feeding the predicate the states the store actually produces.

**A poll and a Refresh must re-request the operator's actual URI, not the
route constant.** An operator who has paged forward is on
`?page_size=&page_token=`; a refresh that drops those silently snaps them
back to page one. `opsSelfPath(r)` carries `r.URL.RequestURI()` onto the
view model for exactly this.

Everything else gets a **manual Refresh button** (`hx-get` = its own
path). A timer on a read-only spec page is pure cost, and it would swap
content out from under an operator who is reading it.

**A whole-content-region refresh must target a region id, not `this` —
and the served fragment must be exactly that region.** `hx-target="this"`
resolves to the *button*, so swapping a whole page body into it
duplicates the page. The spec and delivery pages give their content
region a stable id and target it.

The second half is the subtle one, and it has bitten this codebase once.
htmx `outerHTML` inserts **every top-level node of the response** into
the target. So if a page's handler serves a fragment with more than one
top-level element while `hx-target` names only one of them, each Refresh
click splices the extras in — the heading and button duplicate, once per
click, without bound. The converse is the reassuring half: a control
*inside* the target is not destroyed by a swap, because the response
carries a fresh one. So the rule is not "keep the button outside the
region"; it is **"the served fragment's root element is the swap
target."**

`hx-get` is the page's real path, carried on the view model as `Path` —
not a relative `"."` — so it is correct regardless of trailing-slash
handling. `pages/refresh_test.go` asserts the fragment shape directly
(top-level element count, and that its root carries the targeted id)
rather than reasoning about DOM containment.

**Byte-stability.** A polled fragment must produce identical bytes for
unchanged state, or every poll becomes a visible swap that destroys
scroll position and selection. So: absolute `time.RFC3339` only (never
"in 3m"), no generated ids, no nonces, no map iteration order. The same
rule SSE will need, from `//libs/go/htmxsse/README.md` — it applies
before SSE exists.

SSE itself is **not** wired up. It would need a RabbitMQ dependency krill
does not have (`../ENV.md`), and the hub must degrade to nil rather than
fail boot.

## The htmxui primitives — and when not to re-hand-roll them

Available, and the right answer for their own job:

`Shell` / `ShellData` · `UserMenu` · `ThemeSwitcher` / `Themes` / `ThemesCSS`
· `Button` / `ButtonVariant` / `ButtonSize` · `Badge` / `BadgeVariant` /
`BadgeSize` · `Card` · `Confirm` / `ConfirmProps` · `ContainerWide` ·
`Alert` / `AlertVariant` · `EmptyState`

**Do not hand-roll these:**

| Instead of | Use |
|---|---|
| A status pill with your own class names | `htmxui.Badge` + krill's `components.MilestoneStatusStyle` |
| `<div role="alert" class="alert alert-error">` | `htmxui.Alert` — it *derives* the ARIA role from the variant |
| `<p class="text-base-content/70">No X yet.</p>` | `htmxui.EmptyState` — the opacity is fixed there, not per call site |
| A confirm/danger-zone card | `htmxui.Confirm` (the `<form>` stays app-owned — see htmxui §9) |

**Rules that come with them:**

- **Empty means render nothing** (htmxui §4). An empty optional value
  omits its markup entirely — no empty `<div>`, no empty `<ul>`, no size
  class. `AreaIndex` and `EmptyState` both demonstrate this.
- **The `<form>` element is never rendered by htmxui** (§9). `Confirm` and
  `Alert` render structure and delegate the form to you, so one component
  serves a plain POST, an `hx-post`, and a multi-action form.
- **`attrs templ.Attributes` is the escape hatch** (§9). `hx-*`, `id`,
  `title`, `name`/`value` go through `attrs`; they never become a new
  named parameter.
- **Domain vocabulary stays krill's.** `MilestoneStatusStyle` lives in
  `krill/ui/components`, not in htmxui — htmxui does not know what
  "partially complete" means (§1).
- **Escalate to htmxui at 2–3 independent call sites, never at one** (§8).

## templ and Bazel: `go_srcs` is hand-maintained

`//tools:templ.bzl`'s `templ_library` globs `*.templ` and runs a genrule
per file. Generated `_templ.go` are **never checked in**.

`go_srcs` is a **literal, hand-maintained list**. Gazelle will not add a
new plain `.go` file to it, and the failure mode is a confusing
"undefined" rather than a helpful one. Adding a hand-written `.go` to
`components` or `pages` means editing `BUILD.bazel` by hand, in the same
change.

Prefer declaring a type or a small helper inside the `.templ` file
itself, the way `libs/go/htmxui/badge.templ` declares `BadgeVariant` and
`badgeClasses` — then `go_srcs` stays empty and there is nothing to keep
in sync. `krill/ui/components/status.go` is a `.go` rather than a
`.templ` precisely because it is a pure-Go mapper with no markup.

`libs/go/htmxui`'s own `templ_library` carries a `# keep` marker so
gazelle does not collapse it. The app-level ones do not.

## Product-scoped URLs

`product_scope.go` resolves which product a request is about. A page
reached under `/products/{pid}/...` names its product in the path, so a
copied link opens on the same product for whoever follows it. A page
reached without one — the legacy `/ops/*` routes, the pre-redesign task
and board URLs, `/`, and the credentials page — resolves one
server-side, so no shell page ever asks for a typed product id.

The two resolutions are separate functions on purpose:

- `resolveProductFromPath` treats the `{pid}` as authoritative. An
  unknown or out-of-scope one is an **in-shell 404** through
  `renderShellStatus`, never a bare `http.Error` — a link an operator
  followed has to land somewhere they can navigate back out of.
- `resolveProductForUnprefixed` never 404s. It takes the
  `krill_last_viewed_product` cookie when that product is still in scope,
  otherwise the first product in scope. A stale cookie is the ordinary
  case for a legacy link, and failing there would break every one of them.

Two wrappers sit on the un-prefixed resolution, and which one a page
calls is the design decision:

- **`resolveUnprefixedProduct`** is for a page that cannot render a body
  without a product — today only `/design`, whose whole job is to link to
  one product's session list. It writes the cookie and answers an empty
  scope with a designed empty state at 200, not a 404: the URL resolved
  fine, there is simply nothing behind it yet.
- **`rememberUnprefixedProduct`** is for a page whose body is the same
  whichever product is current — `/`, `/ops`, the ops read views, the
  credentials page. It writes the cookie and never writes a response, so
  neither a failed product read nor an empty scope can take down a page
  that was already serviceable. The credentials page in particular must
  still mint a token when the scope holds no product: blocking it would
  lock an operator out of the very tool they need to fix things.

The cookie is a **non-authoritative hint**: written on every page render
that resolved a product, and read only by the un-prefixed path — never by
a prefixed one. A value naming a product that has left the scope is
discarded on the way back in, so writing it from a page that did not
itself check scope costs nothing. A cookie naming product B can never
override a prefixed URL for product A.

A page that serves both modes writes the cookie only on the full-page
render: an htmx fragment swap is not a page view, and setting the cookie
on one would make the last-viewed product depend on which pane the
operator happened to page.

Both resolve against `app.spec.Products`, which lists the deployment's
sole scope — a browser cannot pick a scope. Adding a product-scoped page
means registering `productPathPrefix + <suffix>` in `mountShellRoutes`
and calling `resolveProductFromPath` first, so an out-of-scope link is
rejected before any content is built.

## Mutation success feedback: the toast host

`components/toast.templ` is the shell's **one** toast region (an
`aria-live="status"` div, `ToastHostID`), and `toast.go` is the mechanism
that fills it. `//libs/go/htmxui` has no Toast; this is krill-local
(htmxui §8 — escalate a primitive only at 2–3 independent call sites).

**Success takes one of two paths, and which one is decided by the
request, not by the handler.**

| Request | Mechanism | Why |
|---|---|---|
| htmx (`HX-Request`) | `renderFragment(w, r, withToast(msg, c))` — the toast rides along in the same response, out-of-band into the host | templ builds the markup, so the message is templ-escaped. `HX-Trigger` would hand the raw string to a JS template that would have to escape it again. |
| no-JS form post | `flashSuccess(w, msg)` before the 303, then `withFlashSuccess` in `renderShellStatus` renders it as a success alert | a redirect has no body. The cookie is one-shot (`MaxAge: 30`, expired on read) and `SameSite=Lax` — Strict would drop it on the cross-site-initiated navigation it exists for. |

**Out-of-band `beforeend`, never `outerHTML`.** The default OOB swap
replaces the host itself, taking the live region with it, so the second
toast of a session would land in a region that announces nothing.

**A refusal is never a toast.** `renderInterventionResults` takes
`message` and `toast` as separate arguments precisely for this: a refusal
rides inline in `message` (stays until read), a success in `toast`
(transient). A toast is the only record of an outcome only if the operator
never looks away, which a 5-second self-dismiss cannot promise.

**Empty means nothing, on both paths.** `withToast` returns the fragment
unchanged for a blank message and `flashSuccess` sets no cookie. A handler
that already records its outcome where the operator is looking — the
credential mint's one-time token block, the revoked row flipping to
"revoked" — names no message and correctly shows no toast.

**A redirect is the only thing that carries a flash**, so the design-session
forms (`design_write.go`) name a message on the same cookie: both their
success outcomes navigate (303 for no-JS, `HX-Redirect` for htmx) and
neither has a body to state the outcome in.

**The flash is only read by a full page load.** `withFlashSuccess` returns
the body untouched for an htmx request: a fragment renders no document, so
a prepended alert would be swapped into the middle of whatever target
asked for it, and consuming the cookie there would leave nothing to show.
For the same reason the cookie is expired only when one is actually
present, so an ordinary page load does not carry a `Set-Cookie` clearing a
cookie it never had.

`components.Shell` mounts the host on every page, outside `<main>` so a
swap of the page content cannot destroy the live region that is meant to
announce the swap.

Both resolvers return the request as well as the product, and put the
product on its context (`withCurrentProduct` / `currentProduct`). That is
how the chrome knows which product is current without resolving it a
second time — see the switcher below.

### The Product switcher

`components.ProductSwitcher` is the sidebar's product select, and
`product_switcher.go` is its view-model builder and its change handler.
It is the one control that moves an operator between products, and the
reason no shell page asks for a typed product id.

`productSwitcherData` reads the same scope the page already depends on
and renders **every** product, the current one marked — including when
the scope holds exactly one, because the select is also how an operator
tells which product they are in, and that is true most often when there
is only one. A scope the page cannot read yields no switcher at all
rather than an empty one: an empty select reads as "this deployment has
no products", which is the one thing a failed read does not mean.

The current product comes from the request context a resolver populated,
never from the cookie — a component that re-guessed it could disagree
with the page it is rendered beside.

`handleProductSwitch` (`GET /product-switch?product=<pid>&from=<path>`)
is where the change lands. Two rules make it safe:

- **The picked id is checked against the caller's scope.** An id the
  scope does not hold is an in-shell 404, so the control is never a way
  to reach a product this deployment does not serve.
- **`from` is only ever classified, never echoed.** `productAreaHref`
  reduces the page to a *shape* — its segments with every UUID replaced
  by `*` — looks the shape up in a table of areas, and rebuilds the
  target from the validated product id alone. So a milestone, task, or
  design-session id from the product being left cannot ride along under
  the new one, and a hand-edited `from` cannot become an open redirect.
  Every switch therefore lands on a **list** page, and the operator
  keeps their bearings: a switch from Decisions lands on the new
  product's Decisions, not on its overview.

Recognising ids by being UUIDs rather than by counting segments off a
prefix is deliberate: the shape says which area a page is, and an id is
exactly the part of a path that must not survive. A positional rule would
mean re-deciding for each new area which segment holds the id, and
getting that wrong leaks it.

A shell page is given the switcher through `workspaceShellData`, whose
last argument is the `*components.ProductSwitcherData`. That seam stays
pure: the switcher needs a scope read, and a builder that read one would
be a second, differently-filtered read that could disagree with the
page's own.

## Task views

There were two generations of task view. The per-container one is retired —
its list and board 302 into the product-wide pair (FR f41a352d) and its
detail into the product-scoped detail (FR 0c03eac1) — so what follows is
what it did, kept for the handlers and templates that are still built and
reachable only from tests.

`/spec/products/{id}/milestones/{mid}/tasks` (task_page.go,
`pages/tasks.templ`) was the read-only task list for a milestone or
milepebble, linked from each delivery-page row (which also links the
`.../board` route). The container is resolved through the product's own
delivery listing, so an id outside the product is an in-shell 404. A
milestone with milepebbles cut shows links to theirs, never aggregated
tasks. `taskStateBadges` derives the live / lease-expired / capped /
escalated / cancelled badges that the board and detail views reuse; rows
carry the observed claim id and lease expiry as `data-krill-*` attributes so
a later write can be claim-guarded.

<!-- BEGIN product-task-scope section (task 18e3f591) -->
### Product-wide Tasks and Board

`/products/{pid}/tasks` and `/products/{pid}/board`
(product_task_page.go, product_task_scope.go) are two views of **one
scope**, served by one handler with the view's own name. They sit on the
product-wide paged read (`store.ListProductTasks` /
`CountProductTasks`), which is why the query parameter names are the same
ones krill api's `GET /products/{id}/tasks` reads: one filter set has one
spelling, so the link an operator copies is the request the api answers.

**The scope control** (`pages/product_task_scope.templ`, one component
rendered identically by both views) is a plain GET form carrying the mode
and the selected ids:

| Mode | URL | What the control reveals |
|------|-----|-------------------------|
| All incomplete milestones (default) | `scope=incomplete` | nothing — the mode names no container |
| Milestone | `scope=milestone&container_id=<id>` | a milestone select |
| Milepebble | `scope=milepebble&milestone=<id>&container_id=<id>` | a milestone select **and** a select of that milestone's milepebbles |

The rules the control exists to keep:

- **A no-id mode picks the product's highest-position container**, which
  is the listing's *last* entry (`slice.ListProductDelivery` returns
  position-ASCENDING). The store's own read and the board's swimlane order
  take the highest position first, so the console does too.
- **The two selects in milepebble mode submit different parameters.** Only
  the milepebble is the read's container, so only it is `container_id`;
  the milestone submits `milestone`, a UI-only parameter that never
  reaches the store. A milepebble id alone cannot say which options the
  second select should offer, which is why the parent is in the URL at all.
  A named milepebble this product does not own is still a 404 — the
  membership check is independent of the parent. A named milepebble the
  product *does* own but that hangs under a **different** milestone
  resolves to the named milestone's first milepebble instead: that
  disagreeing pair is exactly what the control's own milestone change
  necessarily submits, because the form is a plain GET over two selects
  and a select cannot be emptied by choosing something else in it. Refusing
  it would 404 the one interaction `milestone` exists to enable, and htmx
  does not swap on a 4xx, so it would break silently. The named milestone
  is the more specific statement of intent; the no-id rule settles the
  milepebble one level down.
- **Two empty outcomes, kept apart.** A product with no milestones at all
  (`productTaskScopeNoContainers`) renders the modes and no select — there
  is nothing to put in one. Milepebble mode over a milestone that simply
  has nothing cut under it (`productTaskScopeNoMilepebbles`) keeps the
  milestone select and the named mode marked, because that select is
  exactly how the operator picks a cut one. Answering the second with the
  first is what used to leave the page with no way back to a usable scope.
- **Incomplete applies to the product-wide mode only.** It is judged per
  container by `store.IsIncompleteContainerStatus`, the same predicate the
  store's own query runs. A shipped or abandoned milestone is *excluded*
  from that mode and *offered* (marked "outside the all-incomplete scope")
  in the selects, because picking one explicitly shows its tasks whatever
  its status.

The form works with JavaScript off; htmx is layered on top
(`hx-get` onto the same path, `hx-target` the region's own anchor,
`hx-swap="outerHTML"`, `hx-push-url="true"`, `hx-trigger="change, submit"`).
The control lives *inside* the region, so a swap moves the answer and the
control that produced it together — the marked mode and selected option
are always the ones the new URL resolved to. The `lane` and `only_stuck`
filters ride along as hidden fields, so changing the scope does not
silently drop them. **No field asks the operator to type an id**: every
container is chosen from a select built from the product's own delivery
listing.

Failures split by request mode, as everywhere else here: a full page gets
the in-shell status page (404 for a container outside the product, 400 for
a malformed one), an htmx request gets a 200 carrying the sentence inline,
because htmx does not swap on a non-2xx. The last-viewed-product cookie is
written only for a real page view — a scope change is a swap inside a page
the operator is already on, not a navigation.

The region renders the scope it resolved to, the total behind it, and its
own answer: the Tasks view's table of rows, an empty state naming the active
filters, or an inline failure.

**The table pages, and never truncates silently.** Under the table the
Tasks view renders a footer reading `Showing X of Y tasks`, where Y is
`CountProductTasks`'s answer for the *same* `store.ListProductTasksParams`
the rows were read with (`readProductTasks` builds it once and hands the one
value to both) and X is the page the store returned. Neither number is
derived from the rows: a footer that counted what it had would read
"Showing 25 of 25" on the first of four pages, which is the failure FR
7bff09fe rules out. **Next is present exactly when `Page.NextToken` is** —
the store's own answer that a row remains, not a comparison of X against Y,
which gets an exactly-full final page wrong. **Previous is always disabled.**
The store's keyset paging is forward-only: a token names a position to
resume *after*, so nothing in this layer can name the position before the
page being shown. The control is rendered inert with a `title` saying so
rather than omitted (absent on some pages and present on others, it would
read as a rendering bug) and rather than pointed at a guessed page (which
would land the operator somewhere they did not ask for). The browser's Back
button is the way back, and it works because the pages are real URLs.

**A page move keeps every filter.** `productTaskPagePath` rebuilds the
link from `r.URL.Query()` with only `page_token` replaced, so scope mode,
`container_id`, the UI-only `milestone`, `lane`, `only_stuck` and
`page_size` all survive by construction rather than by a re-derivation that
could forget one. The risk this rules out is a parameter the rebuild drops,
which would silently change what the next page shows.

**A refused token is a fourth state, kept apart from the other three.**
`store.ErrTokenScopeMismatch`, `ErrTokenFilterMismatch` and
`ErrInvalidContinuationToken` are all the caller's own stale link — the
token is URL-carried, so this is a genuinely reachable case — and none of
them is a read failure. They render `product-tasks-page-error` plus a
recovery link, at 400 for a browser and 200 inline for htmx. Folding them
into `product-tasks-error` would answer "see the logs" to someone whose
database is fine, and answering them with rows would show tasks the URL does
not ask for. The recovery link **keeps** every filter and drops only the
token: a token goes stale when the scope or the filters *change*, which is
what the operator just did, so resetting them would discard the thing that
caused the refusal. A page that comes back empty while the count for the
same filters is non-zero is a fourth thing again — a position past the end
of a set that has since shrunk — and the empty state says so rather than
blaming the filters (see `productTaskPastEndDetailOf`). The footer renders
on the Tasks view only; the Board's own requirement about exceeding one
render is FR cf000440's, not this footer's.

**The rows are the read's order, never a re-sort.** The store's keyset
sort (milestone position descending, then id, then creation, then id) is
what a continuation token is bound to; a table that ordered its rows any
other way would leave the next page starting from a row this one did not
end on. `productTaskRowsOf` passes `page.Rows` straight through.

**A cut milestone aggregates.** `store.ProductTaskScopeMilestone` already
means the milestone *and* its milepebbles, so both kinds of row arrive
together; each names the milepebble it came from in its own column, and a
task scoped to the milestone directly leaves that cell empty rather than
inventing one. The empty state is therefore reachable only when the read
returned nothing — never while a cut milestone still has tasks, which is
what the pre-redesign list used to show.

**Lane and state are the shared badges**, through `TaskLaneBadge` /
`TaskBadges`, so the table cannot drift from the board or the detail. The
table issues no write: no form but the scope control's plain GET, no
`hx-post`/`hx-put`/`hx-delete`, and no lane move.

**Claim identity is all-or-nothing.** `data-krill-claim-id` and
`data-krill-lease-expires-at` are set together or not at all, and the
claimed / lease-expired badge follows the same rule: a claim whose expiry
the read did not report cannot be judged live or lapsed, so calling it
"Claimed" would contradict the claim id the row does or does not carry.

**Freshness is an instant, not a sentence.** The region renders
`data-krill-updated-at` with the read's absolute RFC3339 value and nothing
derived from it; `relativeAgeScript` (templ_render.go, in the document
head) turns it into "Updated N ago" and re-runs on `htmx:after:swap`. A
server-rendered relative string would be as old as the response and nothing
inside the region would reveal it. Without JavaScript the operator sees the
instant, which is why that is the element's server-side content.

**The row's detail link is product-scoped**: `/products/{pid}/tasks/{tid}`,
not the per-container form. This table is not scoped to a container, so the
per-container link would name the wrong milestone for every row but one.
That route resolves the task's own container and checks it against the
product's listing, and it is the only detail URL that serves: the
pre-redesign per-container one now 302s into it (see Task detail below).

The **Board** view (`product_board_page.go`, `pages/board.templ`) renders
one swimlane per milestone that has tasks (FR cf000440): five counted
columns per lane, a horizontal scroller inside the board rather than a grid
that spills across the page, and a header naming the container, its own
status badge and the progress read's "N of M done".

### Board cards

A card (FR f6b62cc7) shows the title linking to task detail, the milepebble
it came from when the milestone is cut, a state badge per live state,
"n of cap attempts", and — when it holds a claim — its lease. It sits only
in the column of its own lane, and carries the observed claim id and lease
expiry as `data-krill-*` attributes on the card element itself.

Two rules are load-bearing:

- **The lease is an absolute instant; the countdown is the client's.** The
  card renders `<time data-krill="task-lease" datetime="<RFC3339>">` whose
  text is that same instant. `leaseCountdownScript` — in the document head,
  never in a fragment — rewrites the text to "Lease in 18 min" or "Lease
  expired 6 min ago". A relative string rendered by the server would be as
  old as the response and would differ between two identical reads of
  unchanged state; the board is a fragment Refresh re-requests, so that
  difference would be visible (NFR 7b497d92). With JavaScript off, the
  absolute instant is what the operator sees.
- **A Done card with nothing outstanding is quiet.** It shows its title
  and its milepebble and nothing else — no state badge, no attempts, no
  lease — because a finished task badging "3 of 3" reads as work still to
  do. The carve-out is about the *absence* of live state, not the lane: a
  Done task that is escalated or cancelled still says so, or the board
  would hide work needing a human in the very lane that claims it is done.

State badges come from the one mapper (`components.TaskStateStyle`), the
same one the list and the detail use, over the same derivation the Tasks
table makes — so a lapsed lease reads `lease-expired` on every view and
never `claimed`. Claim identity is all-or-nothing: a row carrying a claim
id without a lease expiry would claim a lease the read did not report.

### The List/Board toggle

The two views are switched by a pair of links rendered **inside the shared
scope control** (`pages/product_task_scope.templ`), not beside either
region body. That placement is load-bearing: the Tasks region
(`pages.ProductTasks`) and the Board region (`pages.ProductBoard`) are two
separate templates behind one anchor, and the scope control is the one
component both of them render. A toggle declared in either view's own
template would be present on one view and silently absent from the other —
which is exactly the drift the shared scope control exists to prevent.
For the same reason the toggle is a sibling of the `<form>`, not a control
inside it: switching view is navigation, not a value the scope submits.

The toggle carries the request's **whole query** to the sibling view,
because switching view changes which rendering of the scope the operator
gets, not which scope they asked for. The sibling path is the request's own
path with its trailing segment swapped (`tasks` ↔ `board`), so the two
views stay one spelling wherever they are mounted.

**One parameter is deliberately dropped: `page_token`.** A continuation
token is a *position* in the keyset-paged read, not a filter. The two views
do not page the same way — the Board's own contract (FR cf000440) is to show
every task in scope, or to state "Showing X of Y" with a link to the filtered
list — so a token issued for the Tasks table's page 3 names a row offset that
means nothing on a board of swimlanes. Carrying it would land the operator on
a board showing an arbitrary third of the work with nothing saying so. It is
dropped, not refused: the destination starts at its own first page, which is
the honest answer. `page_size` *does* survive — it is a filter.

Because the toggle sits inside the region, an htmx scope swap brings it
back with the marking and hrefs the new URL resolved to. A toggle rendered
in the shell chrome would survive the swap while going stale, and nothing
would reveal it.

### The sidebar's Tasks and Board

Both nav items link at the product-wide pages (`/products/{pid}/tasks`,
`/products/{pid}/board`) and each marks active on its own view. They used to
fall back to the delivery page, or to whatever milestone the current URL
carried — which meant the same sidebar showed different links depending on
which page it was rendered from, and on a page with no container in the URL
neither item could build its own href at all.

`navTargets` therefore has **no milestone id any more**. It existed only so
those two items could build per-milestone hrefs, and it was a field the
chrome could only sometimes fill. Every sidebar href is now a function of
the product alone, so a request cannot produce a sidebar whose links
disagree with the page it is on.

Each item still **owns** the pre-redesign `/milestones/{mid}/tasks` subtree
as its `AltPath`. It was load-bearing while those URLs served their own
pages: that URL is a task view however it is reached, and an operator who
followed a bookmarked one was on a Tasks page and had to see the sidebar say
so. The href moved to the product-wide page; the active marking did not.
Every per-milestone URL is now a 302 — the **list and board** into these
very product-wide pages (FR f41a352d), the **detail** into the product-scoped
detail (FR 0c03eac1) — so an operator who followed a bookmark arrives on a
path `Path` already owns, and the sidebar marks Tasks from `Path`. The
`AltPath` is kept as the belt to that pair of braces rather than removed
with them: a render whose URL still carries a pre-redesign path is then
marked correctly too, and the wildcard costs nothing when nothing does.
<!-- END product-task-scope section -->

<!-- BEGIN task-detail section (task 9599fc1f) -->
### Task detail

`/products/{pid}/tasks/{tid}` (task_detail_page.go, `pages/task_detail.templ`)
is the task detail, and the only URL that serves it. `serveTaskDetail` is
the one read-and-render both detail handlers reach — only the container
check differs, and only the product-scoped handler is mounted.

**The pre-redesign per-container detail is retired.** Its
`legacyURLs` entry names `legacyTaskDetailSuccessor` rather than serving
(FR 0c03eac1), so a bookmarked
`/spec/products/{id}/milestones/{mid}/tasks/{tid}` 302s into
`productTaskDetailPath(pid, tid)` — the **tid alone, no container**. Unlike
the per-container list and the board, which retire into a view *scoped* to
the `{mid}` they named, the detail retires into the detail: the operator
following a bookmark came to read that task, and the product-scoped handler
resolves the task's own milestone from the task, so a redirect that pinned
the old URL's `{mid}` would strand any task that had since moved between
containers. A `{pid}`, `{mid}` or `{tid}` that does not parse has no such
target, and `serveLegacy` renders the product index rather than redirecting
nowhere.

The redirect is 302 for every method, because its target is registered
GET-only and 302 is the status a client may re-issue as a GET — see
`serveLegacy`. Its `Location` is built from the parsed UUID, so every
spelling of one task id redirects to the same canonical URL. No other
spelling of the per-container URL serves the page: the pattern was never a
subtree, so a trailing slash or a sub-page under it has always 404'd, and
`TestNoPerContainerTaskURLRendersTheDetail` pins that none of them starts
serving.

Two places still *link* the retired URL and so cost a hop: the product-wide
Board's task cards (`product_board_page.go`) and the detail page's own
"Depends on" links. Both resolve — that is what the redirect is for — so
repointing them is a separate change, not part of the retirement.

**The frame is built before its contents.** Top to bottom: a breadcrumb
(product → milestone → milepebble when the task sits on one → the task's
own title, the only crumb with no href), the title as the page's one `h1`
with the task's state badges beside it, then
`grid gap-6 lg:grid-cols-[minmax(0,1fr)_18rem]` — a main column and
`<aside data-krill="task-properties-rail">`. The rail and the tab strip
(Overview / Notes / Dependencies / Spec slice) are the later tasks that fill
those two regions; the region's shape is fixed first so they have a frame to
slot into. `page.Path` is set from `r.URL.Path` after the render, not from
`taskDetailPath`, so Refresh re-requests whichever URL actually served the
page.

Two values on this page must not be re-derived:

- **The lane step strip reads `task.lane_sequence`,** never
  `store.CanonicalLaneOrder` (store/task.go NFR5): a task created as
  Scaffold → Validation → Done has said which lanes it is on, and a strip
  built from the canonical order would show it passing through an
  Implementation lane it never had. Earlier lanes are `step-primary`
  (passed); the current one is `task-step-current` + `aria-current="step"`.
- **The breadcrumb's milepebble level** comes from `taskContainer.ParentID`
  / `ParentName`, which `resolveTaskContainer` sets. A task under a cut
  milestone always sits on one of its milepebbles, so "the container is a
  milepebble" and "the task sits on a cut milepebble" are one condition,
  decided from the container's own `Kind` rather than a second lookup.

It composes `GetTaskByID`, `ListDependencies`, `ListNotesForTask`, the
current claim row, the escalation event behind `task.CurrentEscalationID`
(`GetEscalationEventByID`, read **once** so the properties rail and the
Overview callout cannot disagree about why a task was escalated) and the
task's spec slice (`MilestoneDeliversSlice`, the same document `get_task`
embeds); no history query is added. A task id unknown, or one whose
milestone is not under the URL's product, is an in-shell 404. Dependencies
and notes each render an inline alert on a read failure; a failed
escalation read costs the page the reason and nothing else, since the
Escalated badge comes from the task row. The region carries
`data-krill-claim-id` / `data-krill-lease-expires-at` for later
claim-guarded writes; it has no form or `hx-post`.
<!-- END task-detail section -->

## Read gate

Reads stay in-process (no api hop), so every read page is mounted through
`readerRoute` (main.go): sign-in, then the same `RoleConfig.ResolvePersona`
(`KRILL_ROLE_OPERATOR` / `KRILL_ROLE_READER`) api uses. A signed-in user with
neither role gets 403. Under `AUTH_MODE=none` the synthetic dev user is
admitted as an operator, matching api's dev token. New read pages must use
`readerRoute`, never bare `RequireAuthFunc`.

## Write identity (LB4) — the constraint that is easy to break

**Identity is resolved server-side.** Every write resolves the operator's
real `(iss, sub)` through `operatorIdentity(ctx)` and reaches krill only
via `withKrillSession`. **The browser never supplies identity.**

So, when adding a write surface:

- A form carries only its own action's arguments. Never an operator
  field, an issuer, or a subject.
- No view-model may carry a `store.Subject` (or any other field a
  browser could have filled in). If a page can display a subject, it
  displays one the server read.
- A write route is mounted through `app.operatorRoute` — never a path
  that can render before `requireOperator` has run.
- `htmxui` never sees a `Subject` or an `htmxauth.UserInfo` at all:
  `ShellData.UserLabel` is a plain string, resolved in the seam. htmxui §1
  requires components to stay free of a specific auth dependency, which is
  also what lets an `AUTH_MODE=none` deployment render the chrome.

## Testing conventions

- **Assert the specific emitted claim, not a golden snapshot** (htmxui
  §14). `TestContainerWideRendersWideBreakpoint`, not a full-page diff.
- **Prefer a stable `data-krill="…"` hook over a cosmetic class** for
  anything a test must find. `class="breakdown"` was the old way and it
  broke on restyle; `data-krill="delivery-breakdown"` will not.
- **Never assert attribute serialisation order.** Assert `method="post"`,
  `action="…"`, and `hx-post="…"` as three separate checks. They happen
  to be emitted in source order today; that is not a contract.
- **Do not derive a test's expectations from the list it is checking.**
  `nav_test.go`'s `requiredAreas` and `requiredNavGroups` are spelled out
  as literals on purpose — a test iterating `navGroupTable` passes even if
  you delete an entry from it.
- Match an **exact class token**, not a substring: `hasClass(body,
  "btn-error")`, because `"btn"` is a substring of every other `btn-*`.

## Known exceptions

- **`hx-boost` is never used.** The theme bootstrap in `buildHead` must
  run on every page load.
- **There is no `/partials/` prefix**, by design — see the `HX-Request`
  section.

<!-- BEGIN task board section (kept separate from the task-list docs above) -->
## Task board

`/spec/products/{id}/milestones/{mid}/board` (`board_page.go`,
`pages/board.templ`) was the read-only five-lane board for a milestone or
milepebble. **It is retired** (FR f41a352d): the URL 302s into the
product-wide Board scoped to the `{mid}` it named, so the page this section
describes is unmounted. What it did: it reused the task list's read
(`ListTasksByMilestone`), route scoping, badges and cut-milestone
milepebble links. Columns are always Scaffold, Implementation, Testing,
Validation, Done (with counts, empty ones included); a task is a card only
in its current-lane column. Cards link to task detail and carry the observed
claim id and lease expiry as `data-krill-*` attributes. The region
(`BoardAnchor`) is the `HX-Request` fragment and the Refresh target; there
is no polling and no write control.
<!-- END task board section -->
