package main

import (
	"bytes"
	"html/template"
	"net/http"

	"github.com/a-h/templ"
	"github.com/whale-net/everything/libs/go/htmxbase"
)

// RenderTempl renders a templ component wrapped in htmxbase layout
func RenderTempl(w http.ResponseWriter, r *http.Request, title string, component templ.Component) error {
	var buf bytes.Buffer
	if err := component.Render(r.Context(), &buf); err != nil {
		return err
	}

	themeInit := `<script src="https://cdn.tailwindcss.com"></script>
<script>
(function() {
    const theme = localStorage.getItem('manman-theme') || 
                  (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'night' : 'light');
    document.documentElement.setAttribute('data-theme', theme);
    if (theme === 'night' || theme === 'oled') {
        document.documentElement.classList.add('dark');
    }
})();
</script>`

	layoutData := htmxbase.LayoutData{
		Title:      title,
		CustomCSS:  manmanStyles,
		Content:    template.HTML(buf.String()),
		CustomHead: template.HTML(themeInit),
	}

	return htmxbase.Render(w, layoutData)
}
