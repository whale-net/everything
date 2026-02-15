# htmxbase

A shared base layout library for HTMX applications that provides a consistent HTML structure with HTMX and Alpine.js globally available.

## Features

- **HTMX included**: Version 1.9.10 loaded from unpkg CDN
- **Alpine.js included**: Latest 3.x version loaded from jsDelivr CDN
- **Customizable**: Support for custom CSS, scripts, and head content
- **Type-safe**: Go template-based with proper type definitions

## Usage

### Basic Example

```go
package main

import (
    "net/http"
    "github.com/whale-net/everything/libs/go/htmxbase"
)

func handler(w http.ResponseWriter, r *http.Request) {
    data := htmxbase.LayoutData{
        Title:   "My Page",
        Content: "<h1>Hello World</h1>",
    }

    htmxbase.Render(w, data)
}
```

### With Custom Styling

```go
data := htmxbase.LayoutData{
    Title:       "Styled Page",
    TitleSuffix: "MyApp",
    CustomCSS: `
        body {
            font-family: sans-serif;
            margin: 0;
            padding: 20px;
        }
    `,
    Content: "<div class='container'>Content here</div>",
}

htmxbase.Render(w, data)
```

### With Alpine.js and HTMX

```go
content := `
<div x-data="{ count: 0 }">
    <button @click="count++">Clicked <span x-text="count"></span> times</button>
</div>

<div hx-get="/api/data" hx-trigger="load">
    Loading...
</div>
`

data := htmxbase.LayoutData{
    Title:   "Interactive Page",
    Content: template.HTML(content),
}

htmxbase.Render(w, data)
```

### Integration with Existing Templates

You can use this as a wrapper around your existing template rendering:

```go
func renderWithLayout(w http.ResponseWriter, contentTemplate string, contentData interface{}) error {
    // Render your content template to a buffer
    var buf bytes.Buffer
    if err := templates.ExecuteTemplate(&buf, contentTemplate, contentData); err != nil {
        return err
    }

    // Wrap in base layout
    layoutData := htmxbase.LayoutData{
        Title:   "My App",
        Content: template.HTML(buf.String()),
    }

    return htmxbase.Render(w, layoutData)
}
```

## LayoutData Fields

- **Title** (string): The page title
- **TitleSuffix** (string, optional): Appended to title with " - " separator
- **CustomCSS** (template.CSS, optional): Custom CSS to inject in head
- **CustomHead** (template.HTML, optional): Additional HTML for the head section
- **Content** (template.HTML): The main page content
- **CustomScripts** (template.JS, optional): Custom JavaScript to inject before closing body tag
- **BodyAttrs** (template.HTMLAttr, optional): Additional attributes for the body tag (e.g., `x-data="{}"`)

## CDN Sources

- **HTMX**: https://unpkg.com/htmx.org@1.9.10
- **Alpine.js**: https://cdn.jsdelivr.net/npm/alpinejs@3.x.x/dist/cdn.min.js

Both are loaded from reliable CDNs with integrity checks handled by the CDN providers.
