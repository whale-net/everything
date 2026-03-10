# Templ Migration Progress

## Completed Tasks

### ✅ Task 1: Setup Templ Build Infrastructure
- Added templ and tailwind-merge-go to go.mod
- Created `tools/templ.bzl` macro for templ code generation
- Updated MODULE.bazel with templ dependencies
- Created test component and verified build system works
- **Files**: tools/templ.bzl, manmanv2/ui/components/ui/hello.templ

### ✅ Task 2: Create Base Component Library
- Created types/props.go with ButtonProps, BadgeProps, CardProps, AlertProps
- Implemented Button, Badge, Card, Alert components with variant system
- Created classes.go with tailwind-merge-go integration
- Created ComponentShowcase for testing
- **Files**: 
  - manmanv2/ui/types/props.go
  - manmanv2/ui/components/ui/button.templ
  - manmanv2/ui/components/ui/badge.templ
  - manmanv2/ui/components/ui/card.templ
  - manmanv2/ui/components/ui/alert.templ
  - manmanv2/ui/components/ui/classes.go
  - manmanv2/ui/components/ui/showcase.templ

### ✅ Task 3: Create Layout System
- Created Base layout with navigation and theme support
- Implemented Nav component with theme switcher (Alpine.js)
- Created Breadcrumbs component
- Created Hero component with gradient
- **Files**:
  - manmanv2/ui/components/layout/base.templ
  - manmanv2/ui/components/layout/nav.templ
  - manmanv2/ui/components/layout/breadcrumbs.templ
  - manmanv2/ui/components/layout/hero.templ

### ✅ Task 4: Create Form Components
- Created Input, Textarea, Select, Checkbox components
- Implemented FormField wrapper with error display
- Created FormActions for submit/cancel buttons
- **Files**:
  - manmanv2/ui/components/forms/inputs.templ
  - manmanv2/ui/components/forms/field.templ

## Remaining Tasks

### Task 5: Migrate Games Pages
- Create types/games.go with page data structs
- Create pages/games/list.templ
- Create pages/games/detail.templ
- Create pages/games/form.templ
- Refactor handlers_games.go to use templ

### Task 6: Create Domain Components
- Extract reusable patterns from games pages
- Create components/domain/game_card.templ
- Create components/domain/config_card.templ
- Create components/domain/deployment_table.templ

### Task 7: Migrate Servers Pages
- Create types/servers.go with ServerPageData, ServerDetailPageData
- Create pages/servers/list.templ (servers list page)
- Create pages/servers/detail.templ (server detail page)
- Refactor handlers_servers.go to use templ components
- Test server list and detail pages render correctly

### Task 8: Migrate Sessions Pages
- Create types/sessions.go with SessionPageData, SessionDetailPageData
- Create pages/sessions/list.templ (sessions list with filters)
- Create pages/sessions/detail.templ (complex session detail with logs, actions)
- Handle collapsible sections with Alpine.js (log viewer, action history)
- Refactor handlers_sessions.go to use templ
- Test session detail page with all interactive elements

### Task 9: Migrate Workshop Pages
- Create types/workshop.go with WorkshopPageData, LibraryDetailPageData, AddonDetailPageData
- Create pages/workshop/library.templ (workshop library list)
- Create pages/workshop/library_detail.templ (library detail page)
- Create pages/workshop/addon_detail.templ (addon detail page)
- Create pages/workshop/search.templ (workshop search page)
- Create pages/workshop/installations.templ (installed items page)
- Refactor handlers_workshop.go to use templ
- Test all workshop pages with search and installation flows

### Task 10: Migrate Config Detail Page
- Create types/configs.go with ConfigDetailPageData, BackupConfigData
- Create pages/configs/detail.templ with all sections:
  - Configuration details card
  - Deployments section (prominent with indigo border)
  - Advanced Configuration (collapsible: volumes, env vars)
  - Backup Configuration (collapsible per volume)
  - Danger Zone (collapsible)
- Implement Alpine.js state management for collapsible sections
- Create pages/configs/form.templ for create/edit
- Refactor handlers for config pages
- Test all collapsible sections and forms work correctly

### Task 11: Create Shared Utilities
- Create utils/render.go with helper functions:
  - `RenderComponent(w, ctx, component)` - Standard render helper
  - `RenderError(w, ctx, err)` - Error page rendering
  - `RenderNotFound(w, ctx)` - 404 page rendering
- Create utils/htmx.go for HTMX response helpers:
  - `HXRedirect(w, url)` - Set HX-Redirect header
  - `HXRefresh(w)` - Set HX-Refresh header
  - `HXTrigger(w, event)` - Set HX-Trigger header
- Create utils/validation.go for form validation:
  - `ValidateRequired(value, fieldName)` - Required field validation
  - `ValidateEmail(email)` - Email validation
  - `ValidationErrors` type for collecting errors
- Standardize error handling pattern across all handlers
- Update all handlers to use utility functions

### Task 12: Documentation and Cleanup
- Create ARCHITECTURE.md documenting:
  - Directory structure and organization
  - Component hierarchy and composition patterns
  - Props pattern and variant types
  - HTMX integration patterns
  - Alpine.js usage for interactivity
- Create COMPONENTS.md with component usage guide:
  - All UI components with examples
  - Layout components with examples
  - Form components with validation examples
  - Domain components with use cases
- Update BUILD.bazel files with clear organization and comments
- Remove old templates/ directory (after verifying all pages migrated)
- Remove templates.go (after verifying no dependencies)
- Create migration guide for future components
- Add README.md in manmanv2/ui/ with quick start guide
- Run final build and test to ensure everything works

## Workflow Established

1. Write `.templ` files in appropriate directory
2. Run `~/go/bin/templ generate` in that directory
3. Update BUILD.bazel with `templ_library()` rule
4. Build with `bazel build //path/to:target`

## Key Patterns

### Component with Props
```templ
templ Button(props ...types.ButtonProps) {
    {{ var p types.ButtonProps }}
    if len(props) > 0 {
        {{ p = props[0] }}
    }
    <button class={ buttonClasses(p) }>
        { children... }
    </button>
}
```

### Layout Composition
```templ
@layout.Base(data.Layout) {
    @layout.Hero("Page Title", "Subtitle")
    <div class="content">
        { children... }
    </div>
}
```

### Form Pattern
```templ
@forms.FormField("Name", "name", errorMsg) {
    @forms.Input("name", value, "Enter name")
}
```

## Next Steps

Continue with Task 5 to migrate the Games pages, which will establish the pattern for all other page migrations.
