# ManManV2 UI

Type-safe, component-based UI built with Go + templ + HTMX + Tailwind CSS.

## Quick Start

### Development Workflow

1. **Write templ files**:
   ```bash
   # Create component
   vim components/ui/mycomponent.templ
   ```

2. **Generate Go code**:
   ```bash
   cd components/ui
   ~/go/bin/templ generate
   ```

3. **Build with Bazel**:
   ```bash
   bazel build //manmanv2/ui/...
   ```

### Creating a New Page

1. **Define types** in `types/`:
   ```go
   type MyPageData struct {
       Layout LayoutData
       Items  []*manmanpb.Item
   }
   ```

2. **Create template** in `pages/`:
   ```templ
   package mypage
   
   templ List(data types.MyPageData) {
       @layout.Base(data.Layout) {
           @layout.Hero("Title", "Subtitle")
           <!-- content -->
       }
   }
   ```

3. **Generate and build**:
   ```bash
   cd pages/mypage
   ~/go/bin/templ generate
   bazel build //manmanv2/ui/pages/mypage:mypage
   ```

## Documentation

- **[ARCHITECTURE.md](ARCHITECTURE.md)** - System architecture and patterns
- **[COMPONENTS.md](COMPONENTS.md)** - Component usage guide
- **[TEMPL_MIGRATION.md](TEMPL_MIGRATION.md)** - Migration progress

## Features

- Type-safe templates with compile-time checks
- Component reusability with Props pattern
- HTMX-first architecture for dynamic interactions
- Dark mode support (light/night/oled themes)
- Tailwind CSS with tailwind-merge-go
- Alpine.js for client-side state

## Critical Gotchas

### JavaScript and Template Expressions

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

## Build System

Uses custom `templ_library` Bazel macro:
- Accepts `.templ` files and optional `.go` files
- Automatically includes templ dependencies
- Generates `_templ.go` files via `templ generate`
