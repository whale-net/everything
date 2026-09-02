package main

import (
	"net/http"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/audience_score_system/web/components"
)

// renderTempl renders a templ component wrapped in the shared htmxbase
// layout. Thin delegate to components.Render, kept here only so main.go's
// own handlers (e.g. handleHome) don't need to import web/components just
// for this call -- web/invite's handlers, which live outside package main,
// call components.Render directly instead. See components.Render's doc
// comment for the load-order trap and CDN pinning this wraps.
func renderTempl(w http.ResponseWriter, r *http.Request, title string, component templ.Component) error {
	return components.Render(w, r, title, component)
}
