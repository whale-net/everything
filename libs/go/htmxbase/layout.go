package htmxbase

import (
	"html/template"
	"io"
	"net/http"
)

// BaseLayoutTemplate provides the base HTML structure with HTMX and Alpine.js
const BaseLayoutTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}}{{if .TitleSuffix}} - {{.TitleSuffix}}{{end}}</title>
    <link rel="icon" href="{{if .FaviconURL}}{{.FaviconURL}}{{else}}/favicon.ico{{end}}">

    <!-- HTMX -->
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>

    <!-- Alpine.js -->
    <script defer src="https://cdn.jsdelivr.net/npm/alpinejs@3.x.x/dist/cdn.min.js"></script>

    {{if .CustomCSS}}
    <style>
        {{.CustomCSS}}
    </style>
    {{end}}

    {{if .CustomHead}}
    {{.CustomHead}}
    {{end}}
</head>
<body{{if .BodyAttrs}} {{.BodyAttrs}}{{end}}>
    {{.Content}}

    {{if .CustomScripts}}
    <script>
        {{.CustomScripts}}
    </script>
    {{end}}
</body>
</html>
`

// LayoutData contains data for rendering the base layout
type LayoutData struct {
	Title         string
	TitleSuffix   string
	FaviconURL    string
	CustomCSS     template.CSS
	CustomHead    template.HTML
	Content       template.HTML
	CustomScripts template.JS
	BodyAttrs     template.HTMLAttr
}

// Render renders the base layout with the provided data
func Render(w io.Writer, data LayoutData) error {
	tmpl, err := template.New("base").Parse(BaseLayoutTemplate)
	if err != nil {
		return err
	}
	return tmpl.Execute(w, data)
}

// MustRender renders the base layout and panics on error (useful for initialization)
func MustRender(w io.Writer, data LayoutData) {
	if err := Render(w, data); err != nil {
		panic(err)
	}
}

// FaviconHandler returns an http.HandlerFunc that serves favicon binary data with proper headers.
// It sets Content-Type (defaulting to "image/x-icon") and Cache-Control headers,
// and supports GET and HEAD requests.
func FaviconHandler(data []byte, contentType ...string) http.HandlerFunc {
	ct := "image/x-icon"
	if len(contentType) > 0 && contentType[0] != "" {
		ct = contentType[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	}
}
