# ManMan V2 Design System

**Professional + Gaming Aesthetic**  
A cohesive design language combining clean business-focused UI with vibrant, energetic accents.

---

## Color Palette

### Primary Colors

| Color | Usage | Tailwind Classes | Hex |
|-------|-------|------------------|-----|
| **Indigo** | Main actions, links, focus states | `indigo-600/700` | #4f46e5 / #4338ca |
| **Success (Green)** | Confirmations, success states | `green-600/700` | #16a34a / #15803d |
| **Danger (Red)** | Destructive actions, errors | `red-600/700` | #dc2626 / #b91c1c |
| **Warning (Yellow)** | Warnings, pending states | `yellow-500/600` | #eab308 / #ca8a04 |
| **Neutral (Slate)** | Secondary actions, text, borders | `slate-600/700/800` | #475569 / #334155 / #1e293b |

### Gradient (Hero Headers)

```css
background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
/* Tailwind: bg-gradient-to-br from-indigo-600 to-purple-600 */
```

---

## Component Patterns

### Navigation Bar

**Dark slate in all themes** (including light mode) — this is the "gaming
accent" against the otherwise business-like UI:

```html
<nav class="bg-slate-800 dark:bg-slate-900 shadow-lg sticky top-0 z-50">
```

- Link hover states use `bg-slate-700`
- Collapses to a dropdown menu on narrow viewports (a daisyUI
  `dropdown`/`dropdown-content` pair, the same focus-driven pattern the
  shared theme switcher uses — no JS toggle needed)
- Implementation: `Layout()` in `components/layout.templ`, built on the
  shared `htmxui.Shell` (`libs/go/htmxui`) — see "Future Direction:
  daisyUI" below for how this landed

---

### Hero Headers

**Usage**: Main section pages (Games, Servers, Sessions, Workshop)  
**Do NOT use on**: Detail pages (use breadcrumbs instead)

**Templ Component**: `@components.Hero(title, subtitle, actions)`

```templ
@components.Hero("Games", "Manage game configurations and deployments") {
    @components.Button(components.ButtonProps{
        Text: "+ Create Game",
        Variant: "primary",
        URL: "/games/new",
    })
}
```

**Raw HTML** (if not using component):
```html
<div class="bg-gradient-to-br from-indigo-600 to-purple-600 rounded-lg p-6 md:p-8 mb-6 shadow-lg">
    <div class="flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
        <div class="text-white">
            <h1 class="text-2xl md:text-3xl font-bold mb-2">Page Title</h1>
            <p class="text-indigo-100 text-sm">Page description</p>
        </div>
        <div class="flex gap-2">
            <button class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-white hover:bg-gray-50 text-indigo-600 font-semibold rounded-md transition-colors shadow-md">
                + Create New
            </button>
        </div>
    </div>
</div>
```

---

### Buttons

**Templ Component**: `@components.Button(components.ButtonProps{...})`

#### Primary (Indigo)
**Usage**: Create, Edit, View, Manage, Configure

```templ
@components.Button(components.ButtonProps{
    Text: "Primary Action",
    Variant: "primary",
    URL: "/action",
})
```

#### Success (Green)
**Usage**: Start, Deploy, Save, Confirm, Enable

```templ
@components.Button(components.ButtonProps{
    Text: "Start Session",
    Variant: "success",
    URL: "/start",
})
```

#### Danger (Red)
**Usage**: Delete, Stop, Force, Remove, Disable

```templ
@components.Button(components.ButtonProps{
    Text: "Delete",
    Variant: "danger",
    URL: "/delete",
})
```

#### Secondary (Slate)
**Usage**: Cancel, Back, Close, Dismiss

```templ
@components.Button(components.ButtonProps{
    Text: "Cancel",
    Variant: "secondary",
    URL: "/back",
})
```

**Raw HTML** (if not using component):
```html
<!-- Primary -->
<button class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-indigo-600 hover:bg-indigo-700 text-white font-medium rounded-md transition-colors">
    Primary Action
</button>

<!-- Success -->
<button class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-green-600 hover:bg-green-700 text-white font-medium rounded-md transition-colors">
    Start / Save
</button>

<!-- Danger -->
<button class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-red-600 hover:bg-red-700 text-white font-medium rounded-md transition-colors">
    Delete / Stop
</button>

<!-- Secondary -->
<button class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-slate-600 hover:bg-slate-700 text-white font-medium rounded-md transition-colors">
    Cancel / Back
</button>

<!-- Small Button -->
<button class="inline-flex items-center justify-center px-3 py-1.5 min-h-[36px] bg-indigo-600 hover:bg-indigo-700 text-white text-sm font-medium rounded-md transition-colors">
    Small Action
</button>
```

---

### Badges

**Templ Component**: `@components.Badge(status, text)`

#### Status Badge Colors

| Status | Color | Variant |
|--------|-------|---------|
| Active, Running, Online, Success | Green | `"success"` |
| Pending, Starting, Stopping, Warning | Yellow | `"warning"` |
| Error, Crashed, Failed, Danger | Red | `"danger"` |
| Info, Deployed, Primary | Indigo | `"primary"` |
| Inactive, Stopped, Offline, Secondary | Slate | `"secondary"` |

```templ
@components.Badge("running", "")        // Uses status text
@components.Badge("success", "Active")  // Custom text
```

**Raw HTML**:
```html
<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200">
    Active
</span>
```

---

### Danger Zones

**Usage**: Delete game, Delete config, Delete SGC, Stop session  
**Location**: Always at bottom of page  
**Pattern**: Red-bordered section with inline confirmation

```html
<div x-data="{ confirmDelete: false }" class="bg-white dark:bg-slate-800 rounded-lg shadow-md border-2 border-red-200 dark:border-red-900 overflow-hidden mt-8">
    <div class="bg-red-50 dark:bg-red-900/20 px-6 py-4 border-b-2 border-red-200 dark:border-red-900">
        <h3 class="text-lg font-semibold text-red-900 dark:text-red-200">⚠️ Danger Zone</h3>
    </div>
    <div class="p-6">
        <div class="mb-4">
            <h4 class="text-base font-semibold text-slate-900 dark:text-white mb-2">Delete Resource</h4>
            <p class="text-sm text-slate-600 dark:text-slate-400 mb-4">
                This will permanently delete the resource and all associated data. This action cannot be undone.
            </p>
        </div>
        <div x-show="!confirmDelete">
            <button @click="confirmDelete = true" class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-red-600 hover:bg-red-700 text-white font-medium rounded-md transition-colors">
                Delete Resource
            </button>
        </div>
        <div x-show="confirmDelete" class="flex items-center gap-3">
            <span class="text-sm font-medium text-slate-900 dark:text-white">Are you sure?</span>
            <form method="POST" action="/delete" class="inline">
                <button type="submit" class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-red-600 hover:bg-red-700 text-white font-medium rounded-md transition-colors">
                    Yes, Delete
                </button>
            </form>
            <button @click="confirmDelete = false" class="inline-flex items-center justify-center px-4 py-2 min-h-[44px] bg-slate-600 hover:bg-slate-700 text-white font-medium rounded-md transition-colors">
                Cancel
            </button>
        </div>
    </div>
</div>
```

**Requirements**:
- Use Alpine.js for inline confirmation (`x-data`, `x-show`, `@click`)
- Include clear consequence messaging
- State when actions cannot be undone
- Always use red color scheme

---

### Cards

#### Standard Card

```html
<div class="bg-white dark:bg-slate-800 rounded-lg shadow-md p-6 mb-6 border border-gray-200 dark:border-slate-700">
    <p class="text-slate-700 dark:text-slate-300">Card content</p>
</div>
```

#### Card with Header

```html
<div class="bg-white dark:bg-slate-800 rounded-lg shadow-md border border-gray-200 dark:border-slate-700 mb-6 overflow-hidden">
    <div class="flex justify-between items-center p-4 border-b border-gray-200 dark:border-slate-700 bg-gray-50 dark:bg-slate-900">
        <h2 class="text-lg font-semibold text-slate-900 dark:text-white">Card Title</h2>
        <button class="inline-flex items-center justify-center px-3 py-1.5 min-h-[36px] bg-indigo-600 hover:bg-indigo-700 text-white text-sm font-medium rounded-md transition-colors">
            Action
        </button>
    </div>
    <div class="p-6">
        <p class="text-slate-700 dark:text-slate-300">Card content</p>
    </div>
</div>
```

---

### Forms

#### Text Input

```html
<div class="mb-4">
    <label class="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-2">Label</label>
    <input type="text" class="w-full px-3 py-2 min-h-[44px] border border-gray-300 dark:border-slate-600 rounded-md bg-white dark:bg-slate-800 text-slate-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-indigo-500">
</div>
```

#### Select

```html
<div class="mb-4">
    <label class="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-2">Select</label>
    <select class="w-full px-3 py-2 min-h-[44px] border border-gray-300 dark:border-slate-600 rounded-md bg-white dark:bg-slate-800 text-slate-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-indigo-500">
        <option>Option 1</option>
    </select>
</div>
```

#### Textarea

```html
<div class="mb-4">
    <label class="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-2">Description</label>
    <textarea rows="3" class="w-full px-3 py-2 border border-gray-300 dark:border-slate-600 rounded-md bg-white dark:bg-slate-800 text-slate-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-indigo-500"></textarea>
</div>
```

---

### Tables

```html
<div class="bg-white dark:bg-slate-800 rounded-lg shadow-md border border-gray-200 dark:border-slate-700 overflow-hidden">
    <div class="overflow-x-auto">
        <table class="min-w-full divide-y divide-gray-200 dark:divide-slate-700">
            <thead class="bg-gray-50 dark:bg-slate-900">
                <tr>
                    <th scope="col" class="px-6 py-3 text-left text-xs font-medium text-slate-500 dark:text-slate-400 uppercase tracking-wider">Name</th>
                    <th scope="col" class="px-6 py-3 text-left text-xs font-medium text-slate-500 dark:text-slate-400 uppercase tracking-wider">Status</th>
                    <th scope="col" class="px-6 py-3 text-right text-xs font-medium text-slate-500 dark:text-slate-400 uppercase tracking-wider">Actions</th>
                </tr>
            </thead>
            <tbody class="bg-white dark:bg-slate-800 divide-y divide-gray-200 dark:divide-slate-700">
                <tr class="hover:bg-gray-50 dark:hover:bg-slate-700 transition-colors">
                    <td class="px-6 py-4 whitespace-nowrap text-sm font-medium text-slate-900 dark:text-white">Item Name</td>
                    <td class="px-6 py-4 whitespace-nowrap text-sm text-slate-700 dark:text-slate-300">Active</td>
                    <td class="px-6 py-4 whitespace-nowrap text-right text-sm">
                        <button class="inline-flex items-center justify-center px-3 py-1.5 min-h-[36px] bg-indigo-600 hover:bg-indigo-700 text-white text-sm font-medium rounded-md transition-colors">
                            View
                        </button>
                    </td>
                </tr>
            </tbody>
        </table>
    </div>
</div>
```

---

### Alerts / Banners

#### Info Banner

```html
<div class="mb-4 p-4 bg-indigo-50 dark:bg-indigo-900/20 border-l-4 border-indigo-500 dark:border-indigo-400 rounded">
    <p class="text-sm text-indigo-900 dark:text-indigo-200">Info message here</p>
</div>
```

#### Warning Banner

```html
<div class="mb-4 p-4 bg-yellow-50 dark:bg-yellow-900/20 border-l-4 border-yellow-400 rounded">
    <p class="text-sm text-yellow-900 dark:text-yellow-200">Warning message here</p>
</div>
```

#### Error Banner

```html
<div class="mb-4 p-4 bg-red-50 dark:bg-red-900/20 border-l-4 border-red-500 dark:border-red-400 rounded">
    <p class="text-sm text-red-900 dark:text-red-200">Error message here</p>
</div>
```

---

## Typography & Spacing

### Headings

- **H1**: `text-2xl md:text-3xl font-bold text-slate-900 dark:text-white`
- **H2**: `text-xl font-semibold text-slate-900 dark:text-white`
- **H3**: `text-lg font-semibold text-slate-900 dark:text-white`

### Body Text

- **Primary**: `text-slate-700 dark:text-slate-300`
- **Secondary**: `text-slate-600 dark:text-slate-400`
- **Muted**: `text-slate-500 dark:text-slate-500`

### Spacing Scale

Use Tailwind's spacing scale consistently:
- **4px**: `gap-1`, `p-1`, `m-1`
- **8px**: `gap-2`, `p-2`, `m-2`
- **12px**: `gap-3`, `p-3`, `m-3`
- **16px**: `gap-4`, `p-4`, `m-4`
- **24px**: `gap-6`, `p-6`, `m-6`
- **32px**: `gap-8`, `p-8`, `m-8`

---

## Touch Targets

### Minimum Sizes

- **Standard buttons/inputs**: `min-h-[44px]` (44px minimum)
- **Small buttons**: `min-h-[36px]` (36px minimum for inline actions)
- **Checkboxes/radios**: `w-4 h-4` (16px, acceptable for small controls)

### Mobile Considerations

- All interactive elements meet 44px minimum on mobile
- Forms stack vertically on mobile (`flex-col md:flex-row`)
- Tables scroll horizontally (`overflow-x-auto`)
- Navigation collapses to hamburger menu

---

## Theme Support

### Three Themes

| Theme | Chrome (navbar / page bg) | Page-body cards / surfaces |
|-------|---------------------------|-----------------------------|
| **Light** | daisyUI `[data-theme="light"]` (`libs/go/htmxui/themes.css`) | `bg-gray-100` (#f3f4f6) / White (#ffffff), unchanged |
| **Night** | daisyUI `[data-theme="night"]` | `dark:bg-slate-900` (#0f172a) / Slate-800 (#1e293b), unchanged |
| **OLED Night** | daisyUI `[data-theme="oled"]` — distinct near-black/pure-black palette, no longer identical to Night | Still `dark:bg-slate-900`-family (page-body markup migration to daisyUI is a follow-on effort) |

The shared, pinned `libs/go/htmxui/themes.css` (see `[data-theme="oled"]`
there) is the source of truth for OLED's actual pure-black values — it
replaces the standalone `manman-theme`-era CSS variables this section used
to describe.

### Mechanism

- Theming is `libs/go/htmxui`'s shared `ThemeSwitcher`
  (`theme_switcher.templ`), mounted by `Layout()` via `htmxui.Shell` — not
  a manmanv2-local component. It sets `data-theme` on `<html>` and
  persists the choice to `localStorage['htmxui-theme']`
  (`htmxui.ThemeSwitcherStorageKey`); `templ_render.go`'s bootstrap script
  migrates a previously-saved `localStorage['manman-theme']` value onto
  that key once, so existing operators' preferences aren't lost.
- Switching themes applies immediately via daisyUI's CSS variables under
  `data-theme` — **no `location.reload()`** (the pre-migration Tailwind
  CDN build needed a full reload to re-scan classes; the pinned daisyUI +
  `themes.css` pipeline does not).
- `manmanv2/ui/pages/*.templ` bodies still use raw Tailwind `dark:`
  utilities rather than daisyUI semantic classes (that migration is a
  follow-on issue); `templ_render.go` redefines Tailwind's `dark:` variant
  to key off `data-theme="night"|"oled"` instead of a `.dark` class, so
  those utilities keep responding to the shared theme switcher without
  any manmanv2-local class-toggling JS.

### Dark Mode Classes

All components use Tailwind's `dark:` variant:
- `bg-white dark:bg-slate-800`
- `text-slate-900 dark:text-white`
- `border-gray-200 dark:border-slate-700`

### Gradient Visibility

The indigo-purple gradient works in all three themes:
- **Light**: Full vibrancy
- **Night**: Slightly muted but visible
- **OLED**: High contrast against pure black

---

## Quick Reference

### When to Use Each Color

| Action Type | Color | Example |
|-------------|-------|---------|
| Create, Edit, View | Indigo | "Create Game", "Edit Config", "View Details" |
| Start, Deploy, Save | Green | "Start Session", "Deploy Config", "Save Changes" |
| Delete, Stop, Force | Red | "Delete Game", "Stop Session", "Force Restart" |
| Cancel, Back | Slate | "Cancel", "Back to List", "Close" |
| Warnings (badges only) | Yellow | "Pending", "Starting", "Stopping" |

### Component Selection

| Need | Use |
|------|-----|
| Main section page | Hero header with gradient |
| Detail page | Breadcrumbs (no hero) |
| Destructive action | Danger zone at bottom |
| List of items | Table with hover states |
| Form | Consistent input styling |
| Status indicator | Badge with appropriate color |
| Alert/notification | Banner with left border |

---

## Migration Checklist

The chrome and render pipeline (navbar, theme switching, `templ_render.go`'s
`<head>`) are already migrated onto daisyUI via `libs/go/htmxui` (#1007) —
see "Future Direction: daisyUI" below for what landed there. This checklist
covers the remaining work: bringing an individual page's **body** markup
onto daisyUI, built on `libs/go/htmxui`'s shared primitives, the same way
the chrome was migrated. Do not build a manmanv2-local component layer for
anything `libs/go/htmxui` already provides (Button/Badge/Card/Confirm) —
import and use those directly, the way `components/layout.templ` imports
`htmxui.Shell` and `htmxui.ThemeSwitcher` rather than re-implementing them.

When migrating a page to daisyUI:

- [ ] Replace this package's local `@components.Button`/`@components.Badge`
      (in `components/ui.templ`) with `@htmxui.Button`/`@htmxui.Badge`
      (`libs/go/htmxui`) where the shared component already covers the
      case; keep local components only for what `htmxui` doesn't provide
      (e.g. `@components.Hero`).
- [ ] Replace Tailwind `dark:` utility pairs (`bg-white dark:bg-slate-800`,
      etc.) with daisyUI semantic classes driven by `data-theme`
      (`bg-base-100`, `bg-base-200`, `text-base-content`, ...) — see
      `libs/go/htmxui/themes.css` for the full variable-to-color mapping
      per theme (light/night/oled).
- [ ] Replace `bg-indigo-600 hover:bg-indigo-700`-style buttons with
      `btn btn-primary`; `bg-slate-600 hover:bg-slate-700` with
      `btn btn-secondary`; destructive actions with `btn btn-error`.
- [ ] Add hero header to main section pages (use `@components.Hero`,
      unchanged — daisyUI has no hero-gradient equivalent in this design
      system's vocabulary).
- [ ] Move delete actions to danger zone at bottom.
- [ ] Update status badges to daisyUI badge classes (`badge badge-success`,
      `badge-warning`, `badge-error`, `badge-secondary`).
- [ ] Ensure all buttons meet the 44px minimum height (the shared
      `libs/go/htmxui/themes.css` already sets this for `.btn`; no local
      override needed).
- [ ] Test in all three themes (light/night/OLED) — OLED must render the
      distinct near-black `[data-theme="oled"]` palette, not look
      identical to Night.
- [ ] Verify mobile responsiveness.
- [ ] Use data attributes for passing dynamic data to JavaScript (NOT template expressions in `<script>` tags).

---

## JavaScript Integration

### Passing Dynamic Data to Scripts

**Problem**: Templ expressions `{ }` inside `<script>` tags are treated as **literal text**, not evaluated.

**Wrong**:
```templ
<script>
  const sessionId = { fmt.Sprintf("%d", data.Session.SessionId) };  // Outputs literal string!
</script>
```

**Correct**: Use HTML data attributes (which ARE evaluated), then read in JavaScript:
```templ
<div id="my-script" data-session-id={ fmt.Sprintf("%d", data.Session.SessionId) }></div>
<script>
  const sessionId = parseInt(document.getElementById('my-script').dataset.sessionId);
</script>
```

**Why**: Templ treats script content as raw strings to avoid breaking JavaScript syntax. Dynamic values must be injected via HTML attributes.

---

## Future Direction: daisyUI

**The go decision has been made: daisyUI is adopted, on a shared library,
not an app-local reimplementation.** Its first adopter was
`tools/app_registry/ui/` (App Registry admin UI, #629), which shipped with
daisyUI from the start rather than migrating onto it. Its components were
then extracted into `libs/go/htmxui` (#1002–#1004: Button/Badge/Card/
Confirm primitives, the shared `Shell`/`ShellData` chrome, and the
CSS-variable-based `ThemeSwitcher` — none of it manmanv2- or app-registry-
specific) precisely so a **second** UI could adopt daisyUI by importing
that library instead of re-deriving its own component set. See
`libs/go/htmxui/README.md` and `tools/app_registry/ui/`'s
`ARCHITECTURE.md`/`README.md` "Design system"/"Styling" sections for the
load-order constraint (NFR5) and value vocabulary that decision produced.

**`manmanv2/ui` is that second adopter (#1007 — this migration has
landed).** The render pipeline (`templ_render.go`) and chrome
(`components/layout.templ`'s `Layout()`, now built on `htmxui.Shell`) are
daisyUI-based, on `libs/go/htmxui`, with no manmanv2-local
reimplementation of the shared primitives:

- `templ_render.go` swapped the unpinned `cdn.tailwindcss.com` for the
  same pinned Tailwind browser build + daisyUI CDN pair
  `tools/app_registry/ui/templ_render.go` uses, followed by
  `htmxui.ThemesCSS` (`libs/go/htmxui/themes.css` — light/night/oled,
  primary=indigo, success=green, error=red, neutral=slate, including the
  pure-black OLED surfaces the pre-migration app lacked), in that exact
  order (NFR5).
- Theme switching is `htmxui.ThemeSwitcher`, not a manmanv2-local
  implementation: daisyUI themes are CSS variables under `data-theme`, so
  no re-scan is needed and the pre-migration theme-switch
  `location.reload()` is gone.
- `components/layout.templ`'s `Layout()` composes `htmxui.Shell` with
  manmanv2's own nav list, server selector, and logout link via Shell's
  app-owned slots (`Nav`, `HeaderRight`) and its breadcrumbs via the
  `Banner` slot — see `htmxui.ShellData`'s doc comment for that slot
  boundary (FR1a).

**What's still pending** (a follow-on issue, not #1007): individual page
**bodies** (`manmanv2/ui/pages/*.templ`) still use raw Tailwind `dark:`
utility classes rather than daisyUI semantic classes — see "Migration
Checklist" above for that remaining work. Semantic equivalents once a page
is migrated: `btn-primary`/`btn-success`/`btn-error`/`btn-secondary` for
the button variants (via `@htmxui.Button` where it covers the case),
`badge-soft badge-*` for the soft-tint status badges, `card bg-base-100`
for cards.

---

## Resources

- **Wireframes**: `manmanv2/ui/design/wireframes/` — clickable design mockups (assembled by `//tools/wireframe`; workflow in `.claude/skills/wireframe/SKILL.md`)
- **Component Library**: `manmanv2/ui/components/ui.templ`
- **Example Pages**: `manmanv2/ui/pages/*.templ`
- **README**: `manmanv2/ui/README.md` (includes critical gotchas)
- **Components Guide**: `manmanv2/ui/COMPONENTS.md`

---

**Last Updated**: August 2026  
**Version**: 2.2 (chrome + render pipeline migrated to daisyUI via `libs/go/htmxui`, #1007; page-body markup migration remains a follow-on effort)  
**Status**: Production Ready
