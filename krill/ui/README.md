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
shell pages and none may ever be wrapped in `components.Layout`:**

| Route | Why not a shell page |
|---|---|
| `/login`, `/auth/login`, `/auth/callback`, `/logout` | The sign-in flow itself; it has to render before there is a signed-in user to put in the chrome. |
| `/authorize`, `/token`, `/register`, both discovery documents | Reachable before any credential exists. |
| `POST`/`GET` `/credentials`, `DELETE` `/credentials/{id}` | `MountSelfServe`'s **JSON** self-serve API, not a page. |
| `/healthz` | JSON. |
| `/favicon.ico` | A static asset, unauthenticated on purpose. |

**The operator shell.** Everything registered in `mountShellRoutes` — the
home page, the four nav areas, and their sub-pages. All of it is behind
`app.auth.RequireAuthFunc`.

`credentialsPath` is `/account/credentials`, **not** `/credentials`, and
that is not an inconsistency: `MountSelfServe`'s `{id}` wildcard outranks
a page registered anywhere under `/credentials`, and the two would panic
the binary at boot. Do not "tidy" it.

## The render seam

Everything a page renders goes through one of three functions in
`templ_render.go`. Handlers do not write HTML themselves.

| Function | Use it for | Status |
|---|---|---|
| `renderShell(w, r, title, activePath, body)` | A full page in the shared chrome. | 200 |
| `renderShellStatus(..., status int)` | A full page that is a real 400 / 404 / 500 **rendered inside the chrome** rather than a bare `http.Error`. | the given code |
| `renderFragment(w, r, c)` | A bare component with no chrome — the `HX-Request` half of a one-route-two-modes branch. | always 200 |

**Why the status code lives in Go and not in a component.** templ
components are body-writers; they have no status concept. So the status
necessarily lives in the seam, which is exactly the capability the
pre-templ `renderShellStatus` already had. That is why the spec area's
bad-product-id page and the intervention-rejection page still work.

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
| `krill/ui` (package `main`) | Routing, the `App` struct, the write path, the render seam, `nav.go`'s area table and active-path rule, and **every pure view-model builder**. |
| `krill/ui/components` | The chrome: `Layout` (a wrapper around `htmxui.Shell`), `nav`, `navLink`, `SubNav`, and the `MilestoneStatusStyle` status vocabulary. |
| `krill/ui/pages` | Page bodies, one `.templ` per area, each declaring its own view-model struct. |

**Builders stay in `package main`; only the structs and the components
move to `pages`.** That split is deliberate: it keeps the existing
package-main tests compiling unchanged, and `spec_parity_test.go` — which
is the guard that a page and the matching MCP tool show the same spec —
reflects over the builders, so it must keep being able to call them
directly.

## Adding a page, end to end

1. **Path constant** in `routes.go`, inside the area's prefix. If it is a
   new top-level area, add a `navArea` to `navAreas` too.
2. **View-model struct** in the area's `.templ` file in `pages`, beside
   the component that renders it. Use exported field names.
3. **Pure builder** in the area's `.go` file in `package main`, returning
   `pages.X`. It reads through `app.spec` / `app.tasks` / `app.designSessions`.
4. **The component**, taking the view model. Compose `htmxui` primitives;
   do not hand-roll a badge, an alert, an empty state, or a confirm.
5. **The handler**, calling `renderShell`.
6. **Register it** in `mountShellRoutes` behind `app.auth.RequireAuthFunc`
   (or `app.operatorRoute` if it writes).
7. **An `HX-Request` branch** if it is a list or a view that benefits —
   see below.
8. **A test** asserting the data reaches the page, and an entry in
   `nav_test.go`'s `requiredAreas` if it is a new area.

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
  `nav_test.go`'s `requiredAreas` is spelled out as literals on purpose —
  a test iterating `navAreas` passes even if you delete an entry from it.
- Match an **exact class token**, not a substring: `hasClass(body,
  "btn-error")`, because `"btn"` is a substring of every other `btn-*`.

## Known exceptions

- **The credential widget's script is not htmx**, on purpose — see
  `pages/credentials.templ` for the reasoning. It is a `const` injected
  with `templ.Raw` because templ escapes Go expressions inside `<script>`,
  and the widget's script builds `'<tr><td ...>'` markup. Two tests in
  `pages/credentials_test.go` guard both facts.
- **`hx-boost` is never used.** The theme bootstrap in `buildHead` must
  run on every page load.
- **There is no `/partials/` prefix**, by design — see the `HX-Request`
  section.
