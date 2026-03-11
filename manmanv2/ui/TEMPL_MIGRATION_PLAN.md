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

### Task 1: Create templ infrastructure ✅ COMPLETE
- [x] Create `manmanv2/ui/components/` directory
- [x] Create `manmanv2/ui/pages/` directory
- [x] Create `templ_render.go` with helper functions for rendering templ components to http.ResponseWriter
- [x] Update BUILD.bazel to add `templ_library` rules (keep existing go_library)
- [x] Add templ dependencies: `@com_github_a_h_templ//:templ`, `@com_github_a_h_templ//runtime`
- [x] Test: `bazel build //manmanv2/ui:ui_lib` succeeds with empty templ structure
- [x] Demo: Verify Bazel builds successfully

**Implementation Notes:**
- Created separate BUILD.bazel files in components/ and pages/ subdirectories
- Each templ_library has its own importpath based on package location
- `templ_render.go` provides simple wrapper: `func RenderTempl(w http.ResponseWriter, r *http.Request, component templ.Component) error`

---

### Task 2: Create base layout components ✅ COMPLETE
- [x] Create `components/layout.templ`
- [x] Extract navigation from `wrapper.html` to `Nav()` component
- [x] Create `NavItem(href, label, active)` component for navigation links
- [x] Extract theme switcher to `ThemeSwitcher()` component
- [x] Create `Layout(title, active, content)` component that wraps content with nav
- [x] Keep htmxbase integration for outer HTML structure (unchanged)
- [x] Test: Layout renders with correct theme classes and navigation
- [x] Demo: Render test page with navigation showing active states and theme switcher working

**Implementation Notes:**
- Preserved exact HTML structure from wrapper.html
- Kept all Alpine.js directives for mobile menu and theme switching
- Maintained server selector dropdown functionality
- Layout component wraps htmxbase.Base for outer HTML structure
- Added `buildTemplLayoutData()` helper in main.go for templ pages

---

### Task 3: Create core UI component library ✅ COMPLETE
- [x] Create `components/ui.templ`
- [x] Implement `Button(variant, size, text, attrs)` component
  - Variants: primary (indigo), success (green), danger (red), secondary (slate)
  - Sizes: default (44px min-height), small (36px min-height)
- [x] Implement `Badge(status, text)` component
  - Use same logic as `statusBadge()` helper from templates.go
- [x] Implement `Card(title, content)` component
  - Standard card with optional header
- [x] Implement `HeroHeader(title, subtitle, actions)` component
  - Gradient hero for section pages (games, servers, sessions, workshop)
- [x] Implement `Table(headers, rows)` component
  - Responsive table wrapper with proper Tailwind classes
- [x] Implement `FormInput(label, name, inputType, value, attrs)` component
  - Standard form input with label
- [x] Test: Each component renders with correct Tailwind classes in all three themes
- [x] Demo: Create sample page using all components, verify theme support

**Implementation Notes:**
- All components use pure Tailwind (no legacy CSS)
- Match exact styling from DESIGN_SYSTEM.md
- Preserve min-height requirements (44px standard, 36px small)
- Support dark mode with `dark:` variants
- Added helper components: DLItem, DLItemMono, DLItemCode, TableInline, EmptyState, Alert
- Added ButtonLink for anchor tags styled as buttons

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

### Task 15: Migrate home.html and dashboard partials ✅ IN PROGRESS
- [x] Create `pages/home.templ`
- [ ] Create `components/dashboard.templ`
- [ ] Implement `DashboardSummary(stats)` component
- [ ] Implement `DashboardSessions(sessions)` component
- [ ] Convert home page using dashboard components
- [x] Update `handlers_home.go` - `handleHome()` to use templ render
- [ ] Update dashboard API handlers to return templ partials
- [ ] Test: Dashboard displays correctly
- [ ] Test: HTMX updates work (summary stats, session list)
- [ ] Test: Polling intervals maintained
- [ ] Remove `templates/home.html` and dashboard partials only after all tests pass
- [ ] Demo: View dashboard, verify live updates work

**Implementation Notes:**
- Dashboard API endpoints may need updates to render templ partials
- Preserve exact HTMX polling behavior
- **Current Status**: Basic home page created and handler updated, needs dashboard partials for HTMX endpoints

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

**Phase 1**: ✅ COMPLETE (3/3 tasks)
**Phase 2**: ☐ Not Started  
**Phase 3**: ☐ Not Started  
**Phase 4**: ☐ Not Started  
**Phase 5**: ⏳ IN PROGRESS (Task 15 started - 1/3 tasks)
**Phase 6**: ☐ Not Started  

**Overall Progress**: 3.5/18 tasks completed (19%)

---

## Current Status Summary

**Completed:**
- ✅ Templ infrastructure set up with separate BUILD files for components/ and pages/
- ✅ Base layout components created (Nav, ThemeSwitcher, Breadcrumbs, Layout)
- ✅ Core UI component library created (Button, Badge, Card, HeroHeader, FormInput, Table, etc.)
- ✅ Home page migrated to templ (pages/home.templ)
- ✅ Handler updated (handlers_home.go uses RenderTempl)
- ✅ Bazel builds successfully
- ✅ Tilt deployed and manmanv2-ui is running

**In Progress:**
- ⏳ Task 15: Home page needs dashboard partials for HTMX endpoints (/api/dashboard-summary, /api/dashboard-sessions)

**Next Steps:**
1. Create dashboard.templ components for HTMX partials
2. Update dashboard API handlers to use templ
3. Test home page with live HTMX updates
4. Remove templates/home.html after verification
5. Continue with remaining pages (prioritize high legacy CSS pages per plan)

**Architecture Notes:**
- Components and pages are in separate packages with own BUILD files
- Layout component wraps htmxbase.Base for outer HTML structure
- Helper function `buildTemplLayoutData()` converts to components.LayoutData
- All templ files must be generated with `~/go/bin/templ generate` before Bazel build

---

## Notes & Lessons Learned

### 2026-03-11 - Phase 1 Complete

**What Worked:**
- Separate BUILD.bazel files for components/ and pages/ subdirectories
- templ_library macro works well with proper dependency setup
- Layout component successfully wraps htmxbase.Base for outer HTML
- Components can be imported across packages using full import paths

**Challenges:**
- Initial attempt to mix htmxbase (html/template) directly in templ components failed
- templ_library macro uses native.package_name() for importpath, requiring subdirectories for separate packages
- Must run `~/go/bin/templ generate` before Bazel builds to create _templ.go files
- Keyword escaping in templ: "for" in text must be written as `{ "for" }` to avoid parser confusion

**Architecture Decisions:**
- Keep htmxbase wrapper at page level (not in Layout component)
- Layout component provides nav + main content wrapper
- Pages call Layout component and pass children
- Helper function `buildTemplLayoutData()` bridges old LayoutData to components.LayoutData

**Next Session:**
- Start with simple pages (home, games, servers, sessions) to validate patterns
- Save complex pages (config_detail, workshop) for after patterns are proven
- Consider reordering migration to do Phase 4 (simple list pages) before Phase 2 (complex detail pages)

---

**Last Updated**: 2026-03-10  
**Next Review**: After Phase 1 completion
