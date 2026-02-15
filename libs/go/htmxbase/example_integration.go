package htmxbase

import (
	"bytes"
	"html/template"
	"net/http"
)

// Example: Simple handler using htmxbase
func ExampleSimpleHandler(w http.ResponseWriter, r *http.Request) {
	data := LayoutData{
		Title:   "Home",
		Content: "<h1>Welcome to my HTMX app</h1>",
	}

	Render(w, data)
}

// Example: With custom CSS
func ExampleStyledHandler(w http.ResponseWriter, r *http.Request) {
	customStyles := `
		body {
			font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
			background: #f5f5f5;
			margin: 0;
			padding: 20px;
		}
		.container {
			max-width: 1200px;
			margin: 0 auto;
		}
	`

	data := LayoutData{
		Title:       "Dashboard",
		TitleSuffix: "MyApp",
		CustomCSS:   template.CSS(customStyles),
		Content:     template.HTML("<div class='container'><h1>Dashboard</h1></div>"),
	}

	Render(w, data)
}

// Example: Integration with existing Go templates
func ExampleWithGoTemplates(w http.ResponseWriter, r *http.Request, templates *template.Template) error {
	// Your existing template data
	contentData := struct {
		Name    string
		Message string
	}{
		Name:    "Alex",
		Message: "Hello from Go templates!",
	}

	// Render your content template to a buffer
	var contentBuf bytes.Buffer
	if err := templates.ExecuteTemplate(&contentBuf, "my_content.html", contentData); err != nil {
		return err
	}

	// Wrap in htmxbase layout
	layoutData := LayoutData{
		Title:   "Content Page",
		Content: template.HTML(contentBuf.String()),
	}

	return Render(w, layoutData)
}

// Example: Using Alpine.js and HTMX together
func ExampleAlpineAndHTMX(w http.ResponseWriter, r *http.Request) {
	content := `
<div x-data="{ open: false }">
    <!-- Alpine.js dropdown -->
    <button @click="open = !open" class="btn">
        Toggle Menu
    </button>

    <div x-show="open" x-transition>
        <!-- HTMX dynamic content -->
        <div hx-get="/api/menu-items" hx-trigger="load">
            Loading menu...
        </div>
    </div>
</div>

<!-- HTMX polling -->
<div hx-get="/api/status" hx-trigger="every 5s">
    Checking status...
</div>
`

	data := LayoutData{
		Title:   "Interactive Page",
		Content: template.HTML(content),
	}

	Render(w, data)
}

// Example: Helper function for consistent app layout
func RenderAppPage(w http.ResponseWriter, title string, content template.HTML, appName string) error {
	data := LayoutData{
		Title:       title,
		TitleSuffix: appName,
		CustomCSS: template.CSS(`
			* { margin: 0; padding: 0; box-sizing: border-box; }
			body { font-family: system-ui, sans-serif; background: #f5f5f5; }
			.container { max-width: 1200px; margin: 0 auto; padding: 20px; }
		`),
		Content: content,
	}

	return Render(w, data)
}
