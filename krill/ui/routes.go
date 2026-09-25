package main

import (
	"html/template"
	"net/http"
)

// Route prefixes for the shell's own pages. Each area's sub-pages hang
// off its prefix, so navClass can mark the active link by prefix match.
const (
	opsPath         = "/ops"
	designPath      = "/design"
	specPath        = "/spec"

	// credentialsPath is the human-facing credential-widget page. It
	// deliberately lives outside the "/credentials" prefix:
	// app.mcpProvider.MountSelfServe already owns GET/POST /credentials
	// and DELETE /credentials/{id} as its JSON self-serve API
	// (libs/go/auth/selfserve.go), and ServeMux rejects a page registered
	// anywhere under that prefix -- the {id} wildcard outranks it and the
	// two would panic the binary at boot.
	credentialsPath = "/account/credentials"
)

// shellHome is the signed-in landing page: the shell's nav and one line
// per area. It replaces the old single static landing page (sign-in
// confirmation plus the credential widget) -- FR 85a8b33c.
func (app *App) handleShellHome(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Home", "/", shellHomeContent)
}

// shellHomeContent is static markup describing each nav area. Rendered
// into the shell as already-escaped template.HTML -- it carries no
// request data, so there is nothing here to escape.
var shellHomeContent = template.HTML(`<h2>Where to next</h2>
<ul>` + navListItems() + `</ul>`)

// navListItems builds the home page's one-line-per-area list from
// navAreas, so adding an area to the nav needs no second edit.
func navListItems() string {
	out := ""
	for _, area := range navAreas {
		out += `<li><a href="` + htmlEscape(area.Path) + `">` + htmlEscape(area.Label) + `</a> &mdash; ` + htmlEscape(area.Blurb) + `</li>`
	}
	return out
}

// The three area handlers below are the shell's structural skeleton: each
// renders the chrome and a placeholder body, and grows its real read
// surface in the Implementation lane. They are registered now so the nav
// links resolve end to end rather than 404ing from day one.

// handleOps is the ops console root (claimed / escalated / cancelled
// tasks, open notes).
func (app *App) handleOps(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Ops console", opsPath, areaPlaceholder(
		"No task views yet.",
		"Claimed, escalated, and cancelled task consoles land here.",
	))
}

// handleDesign is the design-session browser root.
func (app *App) handleDesign(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Design sessions", designPath, areaPlaceholder(
		"No design sessions yet.",
		"The session list, a session's revisions, and its open questions land here.",
	))
}

// handleSpec is the spec + delivery browser root.
func (app *App) handleSpec(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Spec & delivery", specPath, areaPlaceholder(
		"No spec entities yet.",
		"Products, feature sets, requirements, load-bearing decisions, and the delivery roadmap land here.",
	))
}

// areaPlaceholder is the shared "this area exists but has no content yet"
// body, so a freshly-scaffolded area still renders inside the shell
// rather than an empty page.
func areaPlaceholder(heading, detail string) template.HTML {
	return template.HTML(`<h2>` + htmlEscape(heading) + `</h2>
<p>` + htmlEscape(detail) + `</p>`)
}
