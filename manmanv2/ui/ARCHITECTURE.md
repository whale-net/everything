# ManManV2 UI Architecture

## Overview

The ManManV2 UI is built with Go + templ + HTMX + Tailwind CSS, providing a type-safe, component-based architecture for server-side rendered web applications.

## Technology Stack

- **templ**: Type-safe Go templating with compile-time checks
- **HTMX**: Dynamic interactions without JavaScript frameworks
- **Tailwind CSS**: Utility-first styling with dark mode support
- **Alpine.js**: Minimal client-side state (collapsible sections, theme switching)
- **Bazel**: Build system with templ code generation

## Directory Structure

```
manmanv2/ui/
├── tools/templ.bzl              # Bazel macro for templ
├── types/                       # Type definitions
│   ├── props.go                # Component props (Button, Badge, Card, Alert)
│   ├── page_data.go            # LayoutData
│   ├── configs.go              # Config page data
│   ├── games.go                # Games page data
│   ├── servers.go              # Servers page data
│   ├── sessions.go             # Sessions page data
│   └── workshop.go             # Workshop page data
├── components/
│   ├── ui/                     # Base UI components
│   ├── layout/                 # Layout components
│   ├── forms/                  # Form components
│   └── domain/                 # Domain-specific components
├── pages/                      # Page templates
│   ├── configs/
│   ├── games/
│   ├── servers/
│   ├── sessions/
│   └── workshop/
└── utils/                      # Utilities
    ├── render.go               # Component rendering
    ├── htmx.go                 # HTMX helpers
    └── validation.go           # Form validation
```

## Component Patterns

### Props Pattern
```go
type ButtonProps struct {
    Variant  ButtonVariant
    Size     ButtonSize
    Class    string
    Disabled bool
}
```

### Component with Children
```templ
templ Button(props ...types.ButtonProps) {
    <button class={ buttonClasses(props[0]) }>
        { children... }
    </button>
}
```

### Page Pattern
```templ
templ List(data types.PageData) {
    @layout.Base(data.Layout) {
        @layout.Hero("Title", "Subtitle")
        <!-- content -->
    }
}
```

## HTMX Integration

Components are designed as swappable sections:
- Use `hx-get`, `hx-post` for dynamic updates
- Target specific elements with `hx-target`
- Swap strategies: `innerHTML`, `outerHTML`, `beforeend`

## Build Workflow

1. Write `.templ` files
2. Generate: `~/go/bin/templ generate`
3. Build: `bazel build //manmanv2/ui/...`

## Dark Mode

Three themes supported:
- `light`: Default light theme
- `night`: Dark theme
- `oled`: Pure black for OLED displays

Theme switching via Alpine.js in nav component.
