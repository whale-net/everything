package main

import (
	"net/http"

	"github.com/whale-net/everything/krill/ui/pages"
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

// homeLinks is the signed-in landing page's list: the nav's areas, one
// line each.
func homeLinks() []pages.AreaLink {
	links := make([]pages.AreaLink, 0, len(navAreas))
	for _, a := range navAreas {
		links = append(links, pages.AreaLink{Path: a.Path, Label: a.Label, Blurb: a.Blurb})
	}
	return links
}

// handleShellHome renders the landing page. The home page is not itself a
// nav area, so no link is marked active on it -- the header's "krill"
// brand is the way back here from anywhere in the shell.
func (app *App) handleShellHome(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Home", "/", pages.AreaIndex("Where to next", homeLinks()))
}

// The area handlers below own the shell's per-area roots. Each renders the
// chrome; the read and write surfaces under these prefixes are registered
// alongside these roots.

// opsIndexLinks is the ops console root's body: the four read views it
// owns, one link each (ops.go renders the views themselves).
var opsIndexLinks = []pages.AreaLink{
	{opsClaimedPath, "Claimed tasks", "every task that currently holds a claim."},
	{opsEscalatedPath, "Escalated tasks", "every task with an active escalation, and why."},
	{opsCancelledPath, "Cancelled tasks", "every cancelled (dead-lettered) task."},
	{opsNotesPath, "Open notes", "every note still in an open lifecycle status."},
}

// handleOps is the ops console root, linking its four read views.
func (app *App) handleOps(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Ops console", opsPath, pages.AreaIndex("Ops console", opsIndexLinks))
}

// handleDesign is the design-session browser root. It is the entry point
// into the read sub-pages registered under this prefix in mountShellRoutes
// (design_page.go): a product's session list and one session's
// revision-event log + open questions. The root itself just takes a product
// id and navigates to that product's session list.
func (app *App) handleDesign(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Design sessions", designPath, pages.DesignRoot())
}

// handleSpec is the spec + delivery browser root. It is a static landing
// (like the ops and design roots) that links into the store-backed spec
// pages under specProductsPath -- the product index, and per product the
// capability map, load-bearing decisions, personas, and non-goals (all in
// spec_page.go). The landing itself reads nothing, so the area root stays
// cheap.
func (app *App) handleSpec(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Spec & delivery", specPath, pages.AreaIndex("Spec & delivery", []pages.AreaLink{
		{Path: specProductsPath, Label: "Products", Blurb: "Browse a product's capability map, load-bearing decisions, personas, and non-goals."},
	}))
}
