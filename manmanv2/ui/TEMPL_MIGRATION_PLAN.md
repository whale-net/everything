# Templ Migration Plan - ManManV2 UI

**Status**: Not Started  
**Last Updated**: 2026-03-10  
**Migration Strategy**: Incremental, page-by-page with conservative component extraction

---

## ⚠️ CRITICAL: Preserve Existing Functionality

**This migration MUST maintain 100% of existing functionality. Do NOT:**
- Invent new features or functionality
- Remove existing features
- Change behavior of existing handlers
- Modify data structures or API contracts
- Alter HTMX interactions or Alpine.js behavior
- Change routing or URL patterns

**This migration SHOULD:**
- Convert HTML templates to templ 1:1
- Replace legacy CSS with Tailwind equivalents
- Extract common patterns to reusable components
- Delete old HTML files only after templ replacement is verified working
- Update handlers minimally (only rendering logic changes)

---

## Overview

### Problem Statement
Migrate //manmanv2/ui from Go html/template to templ incrementally (one page at a time) while consolidating common code patterns and eliminating CSS duplication. The migration must maintain all existing functionality.

### Current State
- **25 HTML templates**: 19 pages + 6 partials
- **CSS Duplication**: Mix of legacy CSS (`.card`, `.btn`, `.badge`, `.stat-card`, `.session-panel`) and modern Tailwind
- **Template System**: Go `html/template` with `embed.FS` loading
- **Design System**: Well-documented Tailwind-first approach in DESIGN_SYSTEM.md
- **Tooling**: templ_library macro ready in //tools/templ.bzl

### Target Architecture
```
manmanv2/ui/
├── templates/           # Legacy HTML (gradually deleted)
├── components/          # NEW: Templ components
│   ├── layout.templ     # Wrapper, nav, theme switcher
│   ├── ui.templ         # Buttons, badges, cards, forms, tables
│   ├── breadcrumbs.templ
│   └── dashboard.templ  # Dashboard-specific components
├── pages/              # NEW: Templ pages
│   ├── config_detail.templ
│   ├── workshop_*.templ
│   └── ...
├── templates.go        # Legacy template loader (gradually simplified)
└── templ_render.go     # NEW: Templ rendering helpers
```

### Migration Order
Start with pages needing most cleanup (high legacy CSS usage), then medium, then simple pages.

---

## Phase 1: Foundation & Tooling

### Task 1: Create templ infrastructure
- [ ] Create `manmanv2/ui/components/` directory
- [ ] Create `manmanv2/ui/pages/` directory
- [ ] Create `templ_render.go` with helper functions for rendering templ components to http.ResponseWriter
- [ ] Update BUILD.bazel to add `templ_library` rules (keep existing go_library)
- [ ] Add templ dependencies: `@com_github_a_h_templ//:templ`, `@com_github_a_h_templ//runtime`
- [ ] Test: `bazel build //manmanv2/ui:ui_lib` succeeds with empty templ structure
- [ ] Demo: Verify Bazel builds successfully

**Implementation Notes:**
- Keep existing `go_library` and `embedsrcs` intact
- Add separate `templ_library` targets for components and pages
- `templ_render.go` should provide simple wrapper: `func RenderTempl(w http.ResponseWriter, r *http.Request, component templ.Component) error`

---

### Task 2: Create base layout components
- [ ] Create `components/layout.templ`
- [ ] Extract navigation from `wrapper.html` to `Nav()` component
- [ ] Create `NavItem(href, label, active)` component for navigation links
- [ ] Extract theme switcher to `ThemeSwitcher()` component
- [ ] Create `Layout(title, active, content)` component that wraps content with nav
- [ ] Keep htmxbase integration for outer HTML structure (unchanged)
- [ ] Test: Layout renders with correct theme classes and navigation
- [ ] Demo: Render test page with navigation showing active states and theme switcher working

**Implementation Notes:**
- Preserve exact HTML structure from wrapper.html
- Keep all Alpine.js directives for mobile menu and theme switching
- Maintain server selector dropdown functionality
- Do NOT change any JavaScript or HTMX behavior

---

### Task 3: Create core UI component library
- [ ] Create `components/ui.templ`
- [ ] Implement `Button(variant, size, text, attrs)` component
  - Variants: primary (indigo), success (green), danger (red), secondary (slate)
  - Sizes: default (44px min-height), small (36px min-height)
- [ ] Implement `Badge(status, text)` component
  - Use same logic as `statusBadge()` helper from templates.go
- [ ] Implement `Card(title, content)` component
  - Standard card with optional header
- [ ] Implement `HeroHeader(title, subtitle, actions)` component
  - Gradient hero for section pages (games, servers, sessions, workshop)
- [ ] Implement `Table(headers, rows)` component
  - Responsive table wrapper with proper Tailwind classes
- [ ] Implement `FormInput(label, name, inputType, value, attrs)` component
  - Standard form input with label
- [ ] Test: Each component renders with correct Tailwind classes in all three themes
- [ ] Demo: Create sample page using all components, verify theme support

**Implementation Notes:**
- All components use pure Tailwind (no legacy CSS)
- Match exact styling from DESIGN_SYSTEM.md
- Preserve min-height requirements (44px standard, 36px small)
- Support dark mode with `dark:` variants

---

## Phase 2: High-Priority Pages (Most Legacy CSS)

### Task 4: Migrate config_detail.html
- [ ] Create `pages/config_detail.templ`
- [ ] Convert all `.card`, `.card-header`, `.card-title` to `Card()` component
- [ ] Create `components/breadcrumbs.templ` with `Breadcrumbs(items)` component
- [ ] Convert breadcrumbs section using new component
- [ ] Convert Alpine.js danger zone (preserve exact Alpine directives)
- [ ] Convert all buttons using `Button()` component
- [ ] Convert all badges using `Badge()` component
- [ ] Update `handlers_games.go` - `handleConfigDetail()` to use templ render
- [ ] Test: Config detail page displays correctly, all sections render
- [ ] Test: Edit button toggles edit mode (Alpine.js)
- [ ] Test: Danger zone confirmation works
- [ ] Test: Delete action works
- [ ] Remove `templates/config_detail.html` only after all tests pass
- [ ] Demo: Navigate to config detail, verify all functionality preserved

**Implementation Notes:**
- Preserve exact data structure passed to template
- Keep all Alpine.js `x-data`, `x-show`, `@click` directives unchanged
- Maintain all form actions and HTMX attributes
- Do NOT change handler logic beyond rendering method

---

### Task 5: Migrate session_detail.html
- [ ] Create `pages/session_detail.templ`
- [ ] Convert session info cards using `Card()` component
- [ ] Create `SessionStatus(status)` component for status display
- [ ] Create `PortBadge(port, protocol)` component for port display
- [ ] Convert all sections to use new components
- [ ] Update `handlers_sessions.go` - `handleSessionDetail()` to use templ render
- [ ] Test: Session detail shows all info correctly
- [ ] Test: HTMX polling updates work (verify hx-get, hx-trigger attributes preserved)
- [ ] Test: Action buttons work (start, stop, restart)
- [ ] Remove `templates/session_detail.html` only after all tests pass
- [ ] Demo: View active session, verify real-time updates continue working

**Implementation Notes:**
- Preserve all HTMX attributes exactly
- Keep polling intervals unchanged
- Maintain action button behavior

---

### Task 6: Migrate sgc_detail.html
- [ ] Create `pages/sgc_detail.templ`
- [ ] Convert SGC info display using `Card()` component
- [ ] Create `SessionList(sessions)` component (reusable for sessions page)
- [ ] Convert session list section
- [ ] Update `handlers_sgc.go` - `handleSGCDetail()` to use templ render
- [ ] Test: SGC detail displays all information
- [ ] Test: Session list renders correctly
- [ ] Test: Links to sessions work
- [ ] Remove `templates/sgc_detail.html` only after all tests pass
- [ ] Demo: View SGC with multiple sessions

**Implementation Notes:**
- `SessionList()` component should be reusable for Task 13
- Preserve session panel styling and hover effects

---

### Task 7: Migrate server_detail.html
- [ ] Create `pages/server_detail.templ`
- [ ] Convert server info cards using `Card()` component
- [ ] Convert all sections to use new components
- [ ] Update `handlers_servers.go` - `handleServerDetail()` to use templ render
- [ ] Test: Server detail displays all information
- [ ] Test: Server actions work (if any)
- [ ] Remove `templates/server_detail.html` only after all tests pass
- [ ] Demo: View server details, verify all data displays correctly

---

## Phase 3: Workshop Pages (Medium Complexity)

### Task 8: Migrate workshop_search.html
- [ ] Create `pages/workshop_search.templ`
- [ ] Convert filter form using `FormInput()` components
- [ ] Preserve Alpine.js `x-data` for client-side filtering (exact same logic)
- [ ] Create `WorkshopResultCard(item)` component for search results
- [ ] Convert results display using new component
- [ ] Update `handlers_workshop.go` - `handleWorkshopSearch()` to use templ render
- [ ] Test: Search form submits correctly
- [ ] Test: Client-side filters work (Alpine.js)
- [ ] Test: Results display correctly
- [ ] Remove `templates/workshop_search.html` only after all tests pass
- [ ] Demo: Search for workshop items, apply filters, verify results

**Implementation Notes:**
- Keep Alpine.js filtering logic identical
- Preserve form submission behavior
- Maintain HTMX attributes for dynamic loading

---

### Task 9: Migrate workshop_library.html
- [ ] Create `pages/workshop_library.templ`
- [ ] Create `WorkshopLibraryCard(library)` component
- [ ] Convert library display using new component
- [ ] Convert action buttons using `Button()` component
- [ ] Update `handlers_workshop.go` - `handleWorkshopLibrary()` to use templ render
- [ ] Test: Library displays correctly
- [ ] Test: Add/remove actions work
- [ ] Test: HTMX updates work
- [ ] Remove `templates/workshop_library.html` only after all tests pass
- [ ] Demo: View workshop library, test actions

---

### Task 10: Migrate workshop detail pages
- [ ] Create `pages/workshop_addon_detail.templ`
- [ ] Create `pages/workshop_library_detail.templ`
- [ ] Create `pages/workshop_installations.templ`
- [ ] Create shared `WorkshopDetailCard(item)` component
- [ ] Convert all three pages using shared component
- [ ] Update handlers in `handlers_workshop.go`:
  - `handleWorkshopAddonDetail()`
  - `handleWorkshopLibraryDetail()`
  - `handleWorkshopInstallations()`
- [ ] Test: All workshop detail pages render correctly
- [ ] Test: Installation actions work
- [ ] Test: Remove/reset actions work
- [ ] Remove old HTML files only after all tests pass
- [ ] Demo: Navigate through complete workshop flow

**Implementation Notes:**
- Preserve all workshop-specific functionality
- Keep installation state management unchanged
- Maintain HTMX partial updates

---

## Phase 4: List Pages (Low Complexity)

### Task 11: Migrate games.html
- [ ] Create `pages/games.templ`
- [ ] Use `HeroHeader()` component for page header
- [ ] Use `Table()` component for games list
- [ ] Create `GameRow(game)` component for table rows
- [ ] Convert empty state display
- [ ] Update `handlers_games.go` - `handleGames()` to use templ render
- [ ] Test: Games list displays correctly
- [ ] Test: Create button navigates to form
- [ ] Test: View buttons navigate to detail pages
- [ ] Test: Empty state displays when no games
- [ ] Remove `templates/games.html` only after all tests pass
- [ ] Demo: View games list with data and empty state

---

### Task 12: Migrate servers.html
- [ ] Create `pages/servers.templ`
- [ ] Use `HeroHeader()` and `Table()` components
- [ ] Create `ServerRow(server)` component
- [ ] Convert empty state display
- [ ] Update `handlers_servers.go` - `handleServers()` to use templ render
- [ ] Test: Servers list displays correctly
- [ ] Test: All links work
- [ ] Test: Empty state displays when no servers
- [ ] Remove `templates/servers.html` only after all tests pass
- [ ] Demo: View servers list

---

### Task 13: Migrate sessions.html
- [ ] Create `pages/sessions.templ`
- [ ] Reuse `SessionList()` component from Task 6
- [ ] Use `HeroHeader()` component
- [ ] Convert empty state display
- [ ] Update `handlers_sessions.go` - `handleSessions()` to use templ render
- [ ] Test: Sessions list displays correctly
- [ ] Test: HTMX polling updates work
- [ ] Test: Links to session details work
- [ ] Remove `templates/sessions.html` only after all tests pass
- [ ] Demo: View sessions list, verify real-time updates

**Implementation Notes:**
- Verify `SessionList()` component works in both contexts (SGC detail and sessions list)

---

## Phase 5: Forms & Remaining Pages

### Task 14: Migrate form pages
- [ ] Create `pages/game_form.templ`
- [ ] Create `pages/config_form.templ`
- [ ] Use `FormInput()` components throughout
- [ ] Convert form validation (preserve existing validation)
- [ ] Update handlers in `handlers_games.go`:
  - `handleGameForm()`
  - `handleConfigForm()`
- [ ] Test: Game create form works
- [ ] Test: Game edit form works
- [ ] Test: Config create form works
- [ ] Test: Config edit form works
- [ ] Test: Form validation works
- [ ] Test: Cancel buttons work
- [ ] Remove old HTML files only after all tests pass
- [ ] Demo: Create and edit game and config

**Implementation Notes:**
- Preserve form validation logic exactly
- Keep all form field names unchanged
- Maintain POST action URLs

---

### Task 15: Migrate home.html and dashboard partials
- [ ] Create `pages/home.templ`
- [ ] Create `components/dashboard.templ`
- [ ] Implement `DashboardSummary(stats)` component
- [ ] Implement `DashboardSessions(sessions)` component
- [ ] Convert home page using dashboard components
- [ ] Update `handlers_home.go` - `handleHome()` to use templ render
- [ ] Update dashboard API handlers to return templ partials
- [ ] Test: Dashboard displays correctly
- [ ] Test: HTMX updates work (summary stats, session list)
- [ ] Test: Polling intervals maintained
- [ ] Remove `templates/home.html` and dashboard partials only after all tests pass
- [ ] Demo: View dashboard, verify live updates work

**Implementation Notes:**
- Dashboard API endpoints may need updates to render templ partials
- Preserve exact HTMX polling behavior

---

### Task 16: Migrate remaining pages
- [ ] Create `pages/actions_manage.templ`
- [ ] Create `pages/config_strategies_docs.templ`
- [ ] Convert using existing components
- [ ] Update handlers in `handlers_actions.go` and `handlers_games.go`
- [ ] Test: Actions management page works
- [ ] Test: Config strategies docs display correctly
- [ ] Remove old HTML files only after all tests pass
- [ ] Demo: Navigate through all remaining pages

---

## Phase 6: Cleanup & Documentation

### Task 17: Remove legacy template system
- [ ] Verify all pages migrated (no .html files in templates/ except backup files)
- [ ] Delete `templates.go` (html/template loader)
- [ ] Remove `manmanStyles` CSS constant
- [ ] Remove `templateFS` embed directive
- [ ] Remove unused template helper functions (keep only those used by templ)
- [ ] Update BUILD.bazel to remove `embedsrcs = glob(["templates/**/*.html"])`
- [ ] Remove `templates/` directory entirely
- [ ] Test: `bazel build //manmanv2/ui:ui_lib` succeeds
- [ ] Test: `bazel run //manmanv2/ui:manmanv2-ui` starts successfully
- [ ] Demo: Full smoke test of all pages in running application

**Implementation Notes:**
- Do this task LAST, only after all pages migrated
- Keep helper functions that are still useful (formatTime, timeAgo, etc.)
- May need to move some helpers to templ_render.go

---

### Task 18: Update documentation
- [ ] Update `manmanv2/ui/README.md` with templ architecture
- [ ] Create `components/README.md` documenting all components
- [ ] Update DESIGN_SYSTEM.md to reference templ components instead of HTML
- [ ] Add migration completion notes to this file
- [ ] Document any lessons learned
- [ ] Test: Documentation is accurate and complete
- [ ] Demo: Review documentation with team

---

## Alternative Approach: Comprehensive Component Library First

**If you decide to build all components upfront (Option A from planning):**

### Phase 0: Build Complete Component Library
- [ ] Create comprehensive component library before any page migration
- [ ] Components to build:
  - Layout: `Layout()`, `Nav()`, `NavItem()`, `ThemeSwitcher()`
  - Buttons: `Button()`, `ButtonGroup()`, `IconButton()`
  - Cards: `Card()`, `CardHeader()`, `CardSection()`, `StatCard()`
  - Badges: `Badge()`, `StatusBadge()`, `TagBadge()`
  - Forms: `FormInput()`, `FormSelect()`, `FormTextarea()`, `FormGroup()`
  - Tables: `Table()`, `TableHeader()`, `TableRow()`, `TableCell()`
  - Alerts: `Alert()`, `Banner()`, `Toast()`
  - Modals: `Modal()`, `DangerZone()`
  - Lists: `SessionPanel()`, `SessionList()`, `WorkshopCard()`
- [ ] Create comprehensive test suite for all components
- [ ] Document each component with usage examples
- [ ] Then proceed with page migrations (Tasks 4-16)

**Trade-offs:**
- **Pros**: Faster page migrations, consistent components from start, easier to maintain consistency
- **Cons**: More upfront work, may build unused components, harder to validate components without real usage
- **Gaps Identified**: Need to build ~30+ components upfront vs ~10-15 with conservative approach

---

## Migration Patterns

### Handler Update Pattern
```go
// Before (html/template)
func (app *App) handleConfigDetail(w http.ResponseWriter, r *http.Request) {
    // ... fetch data ...
    if err := renderPage(w, "config_detail_content", data, layoutData); err != nil {
        log.Printf("Error: %v", err)
        http.Error(w, "Internal server error", http.StatusInternalServerError)
    }
}

// After (templ)
func (app *App) handleConfigDetail(w http.ResponseWriter, r *http.Request) {
    // ... fetch data (UNCHANGED) ...
    if err := pages.ConfigDetail(data).Render(r.Context(), w); err != nil {
        log.Printf("Error: %v", err)
        http.Error(w, "Internal server error", http.StatusInternalServerError)
    }
}
```

### CSS Conversion Pattern
```html
<!-- Before: Legacy CSS -->
<div class="card">
    <div class="card-header">
        <div class="card-title">Title</div>
    </div>
    <div>Content here</div>
</div>

<!-- After: Templ + Tailwind -->
@Card("Title") {
    <p class="text-slate-700 dark:text-slate-300">Content here</p>
}
```

### BUILD.bazel Pattern
```starlark
load("//tools:templ.bzl", "templ_library")

# Add these incrementally as you create templ files
templ_library(
    name = "components",
    srcs = glob(["components/*.templ"]),
    deps = ["//libs/go/htmxbase"],
)

templ_library(
    name = "pages",
    srcs = glob(["pages/*.templ"]),
    deps = [
        ":components",
        "//manmanv2/protos:manmanpb",
    ],
)

go_library(
    name = "ui_lib",
    srcs = [
        "grpc_client.go",
        "handlers_*.go",
        "main.go",
        "templ_render.go",  # NEW
        # Remove templates.go in Task 17
    ],
    embedsrcs = glob(["templates/**/*.html"]),  # Remove in Task 17
    deps = [
        ":components",  # NEW
        ":pages",       # NEW
        "//libs/go/htmxbase",
        "//manmanv2/protos:manmanpb",
        "@com_github_a_h_templ//:templ",  # NEW
    ],
)
```

---

## Success Criteria

- [ ] All 25 HTML templates migrated to templ
- [ ] Zero legacy CSS classes (`.card`, `.btn`, etc.) remaining
- [ ] All handlers updated to use templ rendering
- [ ] `templates/` directory deleted
- [ ] `templates.go` removed
- [ ] **All existing functionality preserved (CRITICAL)**
- [ ] Full test coverage maintained
- [ ] All three themes (light/night/OLED) work correctly
- [ ] HTMX functionality preserved
- [ ] Alpine.js interactions work
- [ ] Mobile responsiveness maintained
- [ ] No new features added
- [ ] No existing features removed

---

## Progress Tracking

**Phase 1**: ☐ Not Started  
**Phase 2**: ☐ Not Started  
**Phase 3**: ☐ Not Started  
**Phase 4**: ☐ Not Started  
**Phase 5**: ☐ Not Started  
**Phase 6**: ☐ Not Started  

**Overall Progress**: 0/18 tasks completed (0%)

---

## Notes & Lessons Learned

_Add notes here as you progress through the migration_

---

**Last Updated**: 2026-03-10  
**Next Review**: After Phase 1 completion
