package main

import (
	"bytes"
	"html/template"
	"net/http"
)

// Route prefixes for the shell's own pages. Each area's sub-pages hang
// off its prefix, so navIsActive can mark the active link by prefix.
const (
	opsPath    = "/ops"
	designPath = "/design"
	specPath   = "/spec"

	// credentialsPath is the human-facing credential-widget page. It
	// deliberately lives outside the "/credentials" prefix:
	// app.mcpProvider.MountSelfServe already owns GET/POST /credentials
	// and DELETE /credentials/{id} as its JSON self-serve API
	// (libs/go/auth/selfserve.go), and ServeMux rejects a page registered
	// anywhere under that prefix -- the {id} wildcard outranks it and the
	// two would panic the binary at boot.
	credentialsPath = "/account/credentials"
)

// renderPage renders one of this package's page templates to the HTML
// the shell then embeds. A page body is always rendered from a template
// this file owns, never assembled by concatenating strings into markup --
// that is what lets the shell treat Content as trusted template output.
func renderPage(t *template.Template, data any) template.HTML {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		// Every page template is parsed once at init and every value
		// passed here is this package's own, so this is a programming
		// mistake, not a runtime condition.
		panic(err)
	}
	return template.HTML(buf.String()) //nolint:gosec // this package's own rendered template, not request data
}

// shellHomeTemplate is the signed-in landing page: the nav's areas, one
// line each. It replaces the old single static landing page (sign-in
// confirmation plus the credential widget) -- FR 85a8b33c.
var shellHomeTemplate = template.Must(template.New("home").Parse(`<h2>Where to next</h2>
<ul>
{{range .}}<li><a href="{{.Path}}">{{.Label}}</a> &mdash; {{.Blurb}}</li>
{{end}}</ul>`))

// handleShellHome renders the landing page. The home page is not itself a
// nav area, so no link is marked active on it -- the header's "krill"
// brand is the way back here from anywhere in the shell.
func (app *App) handleShellHome(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Home", "/", renderPage(shellHomeTemplate, navAreas))
}

// areaTemplate is the shared body for an area that has a route but no
// content of its own yet, so a freshly-added area still renders inside
// the shell rather than as a bare or missing page.
var areaTemplate = template.Must(template.New("area").Parse(`<h2>{{.Heading}}</h2>
<p>{{.Detail}}</p>`))

// The three area handlers below own the shell's per-area roots. Each
// renders the chrome and a placeholder body; the read and write surfaces
// under these prefixes are separate tasks, which register their sub-pages
// alongside these roots.

type areaPage struct {
	Heading string
	Detail  string
}

// handleOps is the ops console root (claimed / escalated / cancelled
// tasks, open notes).
func (app *App) handleOps(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Ops console", opsPath, renderPage(areaTemplate, areaPage{
		Heading: "No task views yet.",
		Detail:  "Claimed, escalated, and cancelled task consoles land here.",
	}))
}

// handleDesign is the design-session browser root.
func (app *App) handleDesign(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Design sessions", designPath, renderPage(areaTemplate, areaPage{
		Heading: "No design sessions yet.",
		Detail:  "The session list, a session's revisions, and its open questions land here.",
	}))
}

// handleSpec is the spec + delivery browser root.
func (app *App) handleSpec(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Spec & delivery", specPath, renderPage(areaTemplate, areaPage{
		Heading: "No spec entities yet.",
		Detail:  "Products, feature sets, requirements, load-bearing decisions, and the delivery roadmap land here.",
	}))
}
