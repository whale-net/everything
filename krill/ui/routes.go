package main

import (
	"net/http"

	"github.com/google/uuid"

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

// The product-scoped prefixes (FR c4bd4bf8). The current product is
// carried in the path rather than in a cookie or a query parameter, so a
// copied link opens on the same product for whoever follows it. Each
// area's sub-pages hang off productsPath, mirroring the legacy areas
// above, which stay registered until the cutover task retires them.
const (
	productsPath      = "/products"
	productPathPrefix = productsPath + "/{pid}"

	// The sub-paths a product-scoped page hangs off. overview is the
	// shell home; the rest are the areas the shell's nav reaches.
	overviewSuffix       = "/overview"
	needsAttentionSuffix = "/needs-attention"
	tasksSuffix          = "/tasks"
	boardSuffix          = "/board"
	milestonesSuffix     = "/milestones"
)

// productHref builds the product-scoped href for a sub-path suffix -- the
// one place a link under productsPath is spelled, so a page can never
// hand out a URL the mux does not serve.
func productHref(pid uuid.UUID, suffix string) string {
	return productsPath + "/" + pid.String() + suffix
}

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
//
// "/" names no product, so it resolves one (the last-viewed cookie when
// still in scope, else the first in scope) purely to record it: the body
// here is the area list, which is the same whichever product is current.
func (app *App) handleShellHome(w http.ResponseWriter, r *http.Request) {
	app.rememberUnprefixedProduct(w, r)
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
	app.rememberUnprefixedProduct(w, r)
	renderShell(w, r, "Ops console", opsPath, pages.AreaIndex("Ops console", opsIndexLinks))
}

// handleDesign is the design-session browser root. It is the entry point
// into the read sub-pages registered under this prefix in mountShellRoutes
// (design_page.go): a product's session list and one session's
// revision-event log + open questions. "/design" names no product, so the
// root resolves one server-side and links to that product's session list
// rather than asking the operator for an id.
func (app *App) handleDesign(w http.ResponseWriter, r *http.Request) {
	product, ok := app.resolveUnprefixedProduct(w, r)
	if !ok {
		return
	}
	renderShell(w, r, "Design sessions", designPath,
		pages.DesignRoot(product.Name, designProductSessionsPath(product.ID)))
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

// handleProductPlaceholder serves every product-scoped sub-path until the
// area's own page ships. It resolves the product the URL names -- so the
// prefixes, the in-shell 404, and the last-viewed cookie are all live and
// testable from this point -- and renders a body that names the product
// and links onward, rather than the area's real content.
func (app *App) handleProductPlaceholder(w http.ResponseWriter, r *http.Request) {
	product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	setLastViewedProductCookie(w, product.ID)

	renderShell(w, r, product.Name, productHref(product.ID, overviewSuffix),
		pages.ProductPlaceholder(pages.ProductPlaceholderData{
			Product: productHeaderOf(product),
			Area:    r.PathValue("area"),
		}))
}
