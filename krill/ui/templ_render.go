package main

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/htmxbase"
	"github.com/whale-net/everything/libs/go/htmxui"
)

// buildHead is krill's htmxbase CustomHead: the no-FOUC theme bootstrap,
// then the pinned Tailwind browser build, then the daisyUI stylesheet,
// then htmxui's ThemesCSS -- in that order.
//
// Load-order trap (htmxui ARCHITECTURE §10): ThemesCSS must load *after*
// the daisyUI stylesheet. htmxbase renders CustomCSS before CustomHead,
// so ThemesCSS must go in CustomHead, never CustomCSS -- loading it
// first makes the palette override silently lose to daisyUI's defaults
// with no error anywhere. templ_render_test.go guards the order.
//
// htmx core (1.9.10) and Alpine (3.x) are loaded by htmxbase's own base
// layout, before CustomHead, so any htmx extension script appended here
// is already ordered after core.
//
// No SSE extension in this PR: krill's ops console uses a
// self-terminating poll instead, and wiring //libs/go/htmxsse would mean
// a RabbitMQ dependency krill does not have today.
func buildHead() string {
	return fmt.Sprintf(`<script>
(function(){var KEY=%q;var t=null;try{t=localStorage.getItem(KEY);}catch(e){t=null;}
if(!t){t=window.matchMedia('(prefers-color-scheme: dark)').matches?'night':'light';}
document.documentElement.setAttribute('data-theme',t);})();
</script>
<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4.3.3/dist/index.global.js"></script>
<style type="text/tailwindcss">
@import "tailwindcss";
</style>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui@5.6.18/daisyui.css">
<style>%s</style>`, htmxui.ThemeSwitcherStorageKey, htmxui.ThemesCSS)
}

// renderShell writes one signed-in page: the shared chrome plus body, at
// HTTP 200.
//
// body is a templ.Component rather than pre-rendered HTML: templ has no
// "content" concept, so the caller composes the page and the seam owns
// the document around it. Every app route is mounted behind
// app.auth.RequireAuthFunc by mountShellRoutes, so the identity read here
// is always present.
func renderShell(w http.ResponseWriter, r *http.Request, title, activePath string, body templ.Component) {
	renderShellStatus(w, r, title, activePath, body, http.StatusOK)
}

// renderShellStatus is renderShell with an explicit status code, so a
// page that renders a real 404/400/500 body still does so inside the
// chrome rather than as a bare http.Error string.
//
// The status necessarily lives here rather than in a component: templ
// components are body-writers with no status concept, which is exactly
// the capability the old renderShellStatus already provided. spec_page.go
// and interventions.go depend on it.
//
// The component is rendered into a buffer *before* WriteHeader, so a
// component that fails to render leaves the response unwritten rather
// than committing a status and then truncating the body.
func renderShellStatus(w http.ResponseWriter, r *http.Request, title, activePath string, body templ.Component, status int) {
	// A plain string, never an htmxauth.UserInfo: htmxui §1 requires
	// components stay free of a specific auth dependency, so no auth type
	// may reach one.
	var userLabel string
	if u := htmxauth.GetUser(r.Context()); u != nil {
		userLabel = u.PreferredUsername
	}

	page := layoutWithBody(activePath, title, userLabel, body)

	var buf bytes.Buffer
	if err := page.Render(r.Context(), &buf); err != nil {
		// Every component is compiled by templ and every value passed
		// here is this package's own, so this is a programming mistake,
		// not a runtime condition.
		panic(err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := htmxbase.Render(w, htmxbase.LayoutData{
		Title:       title,
		TitleSuffix: "krill",
		Content:     template.HTML(buf.String()), //nolint:gosec // this package's own rendered component
		CustomHead:  template.HTML(buildHead()),  //nolint:gosec // fixed pinned CDN markup plus the embedded stylesheet
	}); err != nil {
		panic(err)
	}
}

// renderFragment writes a bare component at HTTP 200, with no chrome and
// no document layout -- the HX-Request half of a one-route-two-modes
// branch.
//
// The status is always 200: an htmx swap target's HTTP status is not
// surfaced to the operator, so a failure has to ride inside the fragment
// rather than in a status code. See the agent guide's error rule.
func renderFragment(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := c.Render(r.Context(), w); err != nil {
		logger.Error("failed to render fragment", "error", err)
	}
}

// layoutWithBody composes the chrome around a page body. templ passes a
// component's children through the context rather than as a parameter, so
// calling components.Layout from Go means re-attaching the body to the
// context inside a ComponentFunc.
func layoutWithBody(activePath, title, userLabel string, body templ.Component) templ.Component {
	data := components.LayoutData{
		Title:     title,
		UserLabel: userLabel,
		Nav:       navLinks(activePath),
	}
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return components.Layout(data).Render(templ.WithChildren(ctx, body), w)
	})
}

// mustRenderComponent renders c to a string, panicking on failure.
//
// Production goes through renderShell/renderFragment. Tests use this: the
// question they ask is "does this data reach the page", so a plain string
// is the right shape and keeps ~40 call sites free of error plumbing.
// Every component is compiled by templ and every value is this package's
// own, so failure here is a programming mistake, not a runtime condition.
func mustRenderComponent(c templ.Component) string {
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		panic(err)
	}
	return buf.String()
}
