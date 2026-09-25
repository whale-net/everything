package main

import (
	"html/template"
	"net/http"

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
// rendered into Content. Content is a template.HTML the page handlers
// build, never raw request data -- see the navClass/content contract in
// renderShell.
var shellLayout = template.Must(template.New("shell").Funcs(template.FuncMap{
	"navClass": navClass,
}).Parse(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>{{.Title}} &middot; krill</title></head>
<body>
<header>
  <h1>krill</h1>
  <nav>
    {{range .Areas}}<a href="{{.Path}}" class="{{navClass . $.ActivePath}}">{{.Label}}</a> {{end}}
  </nav>
  <p>Signed in as {{.User}} &middot; <a href="/logout">Sign out</a></p>
</header>
<main>
{{.Content}}
</main>
</body>
</html>`))

// navClass marks the nav link for the page currently being rendered, so
// an operator can see where they are without reading the URL. Matched on
// path prefix rather than equality because the three area roots are
// prefixes of their sub-pages.
func navClass(area navArea, activePath string) string {
	if activePath == area.Path || len(activePath) > len(area.Path) && activePath[:len(area.Path)] == area.Path {
		return "active"
	}
	return ""
}

// renderShell writes one signed-in page: the shell chrome plus the
// caller's own already-escaped content. Every app route is mounted
// behind app.auth.RequireAuthFunc by setupRoutes, so the identity
// renderShell reads is always present.
func renderShell(w http.ResponseWriter, r *http.Request, title, activePath string, content template.HTML) {
	user := htmxauth.GetUser(r.Context())
	username := ""
	if user != nil {
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
		// The template is parsed once at init and the data is this
		// package's own, so an error here is a programming mistake.
		panic(err)
	}
}
