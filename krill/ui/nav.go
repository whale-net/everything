package main

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// navArea is one top-level destination in the shell's persistent nav.
// The three areas the shell exists to expose -- ops console,
// design-session browser, spec+delivery browser -- are FR 85a8b33c's
// contract; the credential widget is the page that predates the shell and
// stays reachable from it.
type navArea struct {
	// Path is this area's own route prefix on this binary.
	Path string

	// Label is the nav link's text.
	Label string

	// Blurb is the one-line description the shell's home page shows
	// beside the link.
	Blurb string
}

// navAreas is the shell's nav, in render order. Kept as package-level
// state rather than per-request data: the set is fixed at build time and
// every page renders the same nav.
var navAreas = []navArea{
	{
		Path:  opsPath,
		Label: "Ops console",
		Blurb: "Claimed, escalated, and cancelled tasks, and open notes.",
	},
	{
		Path:  designPath,
		Label: "Design sessions",
		Blurb: "Browse design sessions, their revisions, and open questions.",
	},
	{
		Path:  specPath,
		Label: "Spec & delivery",
		Blurb: "The spec entities, decisions, and milestone delivery status.",
	},
	{
		Path:  credentialsPath,
		Label: "Credentials",
		Blurb: "Mint a static bearer token for an MCP client.",
	},
}

// shellLayout wraps every signed-in page: the persistent nav, the
// signed-in identity, a sign-out link, and whatever the page handler
// rendered into Content. A page's Content is the already-rendered output
// of one of this package's own page templates -- never request data
// concatenated into markup; see the page templates in routes.go and
// credentials_page.go.
//
// The stylesheet is inlined rather than served from a static route: this
// binary has no asset pipeline, and a nav an operator cannot see the
// active state of is not a nav.
var shellLayout = template.Must(template.New("shell").Funcs(template.FuncMap{
	"navIsActive": navIsActive,
}).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} &middot; krill</title>
<style>
:root { color-scheme: light dark; }
body { font-family: system-ui, sans-serif; margin: 0; line-height: 1.5; }
header { border-bottom: 1px solid rgba(128,128,128,0.35); padding: 0.75rem 1.5rem; }
header h1 { font-size: 1.1rem; margin: 0 0 0.5rem; }
header h1 a { color: inherit; text-decoration: none; }
nav a { display: inline-block; margin-right: 0.75rem; padding: 0.25rem 0.5rem; border-radius: 4px; text-decoration: none; }
nav a.active { font-weight: 600; background: rgba(128,128,128,0.2); }
.identity { margin: 0.5rem 0 0; font-size: 0.9rem; opacity: 0.8; }
main { padding: 1.5rem; max-width: 60rem; }
</style>
</head>
<body>
<header>
  <h1><a href="/">krill</a></h1>
  <nav>
    {{range .Areas}}<a href="{{.Path}}"{{if navIsActive . $.ActivePath}} class="active" aria-current="page"{{end}}>{{.Label}}</a>
    {{end}}</nav>
  <p class="identity">{{if .User}}Signed in as {{.User}} &middot; {{end}}<a href="/logout">Sign out</a></p>
</header>
<main>
{{.Content}}
</main>
</body>
</html>`))

// navIsActive reports whether the page being rendered belongs to this nav
// area, so an operator can see where they are without reading the URL.
// Matched at path-segment boundaries rather than by raw prefix: an area
// root is a prefix of its sub-pages ("/ops" of "/ops/claimed"), but not
// of an unrelated sibling that merely starts with the same characters
// ("/opsarchive").
func navIsActive(area navArea, activePath string) bool {
	if activePath == area.Path {
		return true
	}
	return strings.HasPrefix(activePath, area.Path+"/")
}

// renderShell writes one signed-in page: the shell chrome plus the
// page's own rendered body. Every app route is mounted behind
// app.auth.RequireAuthFunc by setupRoutes, so the identity renderShell
// reads is always present.
func renderShell(w http.ResponseWriter, r *http.Request, title, activePath string, content template.HTML) {
	// Every app route is behind app.auth.RequireAuthFunc, so a user is
	// present in practice; the nil guard keeps the identity line from
	// rendering as a dangling "Signed in as ." if a route is ever mounted
	// without the gate.
	var username string
	if user := htmxauth.GetUser(r.Context()); user != nil {
		username = user.PreferredUsername
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := shellLayout.Execute(w, struct {
		Title      string
		ActivePath string
		User       string
		Areas      []navArea
		Content    template.HTML
	}{
		Title:      title,
		ActivePath: activePath,
		User:       username,
		Areas:      navAreas,
		Content:    content,
	})
	if err != nil {
		// shellLayout is parsed once at init and this package's data
		// never fails to execute against it, so an error here is a
		// programming mistake -- the same reasoning setupRoutes uses to
		// panic on MountSelfServe's error.
		panic(err)
	}
}
