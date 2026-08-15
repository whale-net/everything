package main

import (
	"bytes"
	"html/template"
	"net/http"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/libs/go/htmxbase"
)

// RenderTempl renders a templ component wrapped in the App Registry layout.
// The layout loads daisyUI from CDN first, then themes.css (inlined via CustomHead)
// so theme variables override daisyUI's built-in defaults.
func RenderTempl(w http.ResponseWriter, r *http.Request, title string, component templ.Component) error {
	var buf bytes.Buffer
	if err := component.Render(r.Context(), &buf); err != nil {
		return err
	}

	layoutData := htmxbase.LayoutData{
		Title:      title,
		CustomHead: template.HTML(themeCSS), // themes.css MUST come after daisyUI; CustomHead renders last.
		Content:    template.HTML(buf.String()),
	}

	return htmxbase.Render(w, layoutData)
}
