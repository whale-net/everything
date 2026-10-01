# Workspace shell (proposed)

The sidebar layout for apps with many destinations or a deep object tree:
krill and audience_score_system are the intended first adopters. It is
**proposed, not yet proven in an app**:

- `htmxui.Shell` is top-navbar plus a `max-w` main region, so this layout
  can't be expressed through Shell's slots. The first adopter writes it as an
  app-local `layout.templ`.
- It becomes an htmxui primitive only once a second app needs the same shape
  (htmxui `ARCHITECTURE.md` §8).
- The wireframe kit's `_shell.html` is top-nav too. Prove this layout in a
  wireframe with its own `_shell.html` before building it in templ.

## Structure

```
┌────────────┬──────────────────────────────────────────────┐
│ ▣ krill    │ Product › M3 › Task 42        [search] ◐ 👤 │ top bar
│ [Product ▾]├──────────────────────────────────────────────┤
│            │ Task title            [Open] [Claim] [⋯]     │ header
│ WORK       │ Overview  Activity  Notes                    │ tabs
│  Tasks     ├────────────────────────────────┬─────────────┤
│  Milestones│ main column                    │ Status      │
│ DESIGN     │                                │ Lane        │ properties
│  Sessions  │                                │ Owner       │ rail (lg+)
│  Spec      │                                │ Updated     │
│ ADMIN      │                                │             │
│  Creds     │                                │             │
└────────────┴────────────────────────────────┴─────────────┘
```

(Sidebar section titles render in sentence case. The capitals above are
ASCII only.)

## Reference markup

daisyUI 5 `drawer`: the sidebar is pinned open at `lg` and slides over as an
overlay below `lg`. No JS is needed.

```templ
templ WorkspaceLayout(d WorkspaceData) {
	<div class="drawer lg:drawer-open min-h-screen bg-base-200">
		<input id="app-drawer" type="checkbox" class="drawer-toggle"/>
		<div class="drawer-content flex flex-col min-w-0">
			<header class="navbar sticky top-0 z-30 gap-2 bg-base-100 border-b border-base-300">
				<label for="app-drawer" class="btn btn-ghost btn-square lg:hidden" aria-label="Open navigation">
					@icons.Menu()
				</label>
				<div class="flex-1 min-w-0">
					@d.Breadcrumbs
				</div>
				<div class="flex-none flex items-center gap-2">
					if d.Search != nil {
						@d.Search
					}
					@htmxui.ThemeSwitcher(htmxui.Themes)
					@htmxui.UserMenu(d.User)
				</div>
			</header>
			<main id="main" class="flex-1 p-4 lg:p-6">
				{ children... }
			</main>
		</div>
		<div class="drawer-side z-40">
			<label for="app-drawer" class="drawer-overlay" aria-label="Close navigation"></label>
			<aside class="flex min-h-full w-64 flex-col bg-neutral text-neutral-content">
				<a href="/" class="btn btn-ghost m-2 justify-start text-lg">{ d.BrandLabel }</a>
				if d.ContextSwitcher != nil {
					<div class="px-3 pb-2">
						@d.ContextSwitcher
					</div>
				}
				@d.Nav
			</aside>
		</div>
	</div>
}
```

`d.Nav` is a `menu` with section titles and an active item:

```html
<ul class="menu w-full gap-1">
  <li class="menu-title">Work</li>
  <li><a href="/tasks" class="menu-active"><!-- icon --> Tasks</a></li>
  <li><a href="/milestones"><!-- icon --> Milestones</a></li>
  <li class="menu-title">Design</li>
  <li><a href="/sessions"><!-- icon --> Sessions</a></li>
</ul>
```

## Detail page with properties rail

```html
<div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_18rem]">
  <section class="min-w-0 space-y-6"><!-- tabs + main content --></section>
  <aside class="space-y-4">
    <div class="card bg-base-100 border border-base-300">
      <dl class="card-body gap-3 text-sm"><!-- label/value rows, inline-editable --></dl>
    </div>
  </aside>
</div>
```

## Requirements carried over from Shell

- Keep the same `<head>` pipeline: `htmxbase`, then a `CustomHead` that
  loads Tailwind, then daisyUI, then `htmxui.ThemesCSS`, **in that order**.
  If `ThemesCSS` loads first, the palette silently loses to daisyUI's
  defaults (htmxui `ARCHITECTURE.md` §10).
- Mount `htmxui.ThemeSwitcher(htmxui.Themes)` and `htmxui.UserMenu`. Don't
  reimplement either one.
- The sidebar uses `bg-neutral`, matching Shell's dark navbar in every theme.

## Open follow-ups

- A desktop collapse-to-icons rail (persisted in `localStorage`). Leave it
  out until the basic layout has been used for real.
- A keyboard jump-to (`/` or ⌘K) backed by an `hx-get` search endpoint.
