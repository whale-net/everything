package htmxbase

import (
	"html/template"
	"io"
)

// BaseLayoutTemplate provides the base HTML structure with HTMX and Alpine.js
const BaseLayoutTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}}{{if .TitleSuffix}} - {{.TitleSuffix}}{{end}}</title>

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
