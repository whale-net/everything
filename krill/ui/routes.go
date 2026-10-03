package main

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// Route prefixes for the shell's own pages. Each area's sub-pages hang
// off its prefix, so the nav can mark the active item by prefix. The
// first three are the legacy, un-prefixed areas; the product-scoped
// prefixes below are where every link in the sidebar now points.
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

// handleShellHome renders the landing page, which is the Overview for
// whichever product this deployment resolves.
//
// "/" names no product, so it resolves one (the last-viewed cookie when
// still in scope, else the first in scope) and then serves exactly the
// page /products/{pid}/overview serves -- one Overview, two URLs.
//
// A product read that fails does not take the landing page down with it:
// an un-prefixed page whose body was already serviceable must stay
// serviceable (product_scope.go's own rule). With no product resolved the
// page has nothing to summarise, so it says so and points at the product
// index, which is the one place a missing product can be looked up.
//
// It passes the product's own overview URL as the nav key rather than its
// own path, because that is the path the sidebar's Overview item owns.
func (app *App) handleShellHome(w http.ResponseWriter, r *http.Request) {
	product, err := app.resolveProductForUnprefixed(r)
	if err != nil {
		logger.Warn("could not resolve a product for the landing page", "error", err)
		app.renderShell(w, r, "Products", specProductsPath,
			pages.NoProductsInScope())
		return
	}
	if product.ID == uuid.Nil {
		app.renderShell(w, r, "No products in this scope", specProductsPath,
			pages.NoProductsInScope())
		return
	}
	setLastViewedProductCookie(w, product.ID)
	app.renderOverview(w, withCurrentProduct(r, product), product)
}

// ── legacy URL continuity (FR 2544224c) ─────────────────────────────────────

// legacyURL is one pre-redesign URL and how it resolves during the
// operator UI facelift. It is the unit later phases retire URLs with.
//
// Exactly one of Serve and Successor is set. Serve is the URL's existing
// page, rendered inside the shell at 200. Successor is where the URL goes
// once its redesigned page has shipped; leaving it nil keeps the old page
// serving, which is what every entry does until its successor lands.
// Naming the successor is the whole cutover for that URL, and it is why a
// replaced page's old link can never go dark: the URL is already accounted
// for here, so replacing the page and repointing the link is one edit in
// one file rather than a route registration someone has to remember.
type legacyURL struct {
	// Pattern is the pre-redesign URL exactly as the mux spells it,
	// including any method prefix and id wildcards.
	Pattern string

	// Serve renders the URL's existing page while Successor is nil.
	Serve func(app *App, w http.ResponseWriter, r *http.Request)

	// Successor builds the redesigned page's URL for this request. ok is
	// false when no product could be resolved to build it, which is the
	// one case a legacy URL cannot redirect on: an un-prefixed URL must
	// always land somewhere, so an empty scope renders the product index
	// rather than answering with a redirect to nowhere.
	Successor func(app *App, r *http.Request) (target string, ok bool)
}

// legacyURLs is every pre-redesign URL the operator UI facelift must keep
// resolving, in one table the next phases extend rather than a set of
// ad-hoc handlers.
//
// An entry serves its existing page while its replacement has not shipped,
// and that is deliberate rather than unfinished: a redesigned page that has
// not landed renders a placeholder, so redirecting to one would send an
// operator who followed a working link -- "what is escalated?" -- to a page
// saying nothing is there yet. Once the replacement ships, the entry moves
// from Serve to Successor and the old URL redirects into it. The per-
// milestone task list and board are the first to move, because the
// product-wide Tasks and Board have replaced them (FR f41a352d).
func legacyURLs() []legacyURL {
	return []legacyURL{
		// The ops console. "/" is named by c4bd4bf8 among the un-prefixed
		// URLs; it renders the Overview at every phase of the facelift.
		{Pattern: "/{$}", Serve: (*App).handleShellHome},
		{Pattern: opsPath, Serve: (*App).handleOps},
		{Pattern: opsClaimedPath, Serve: (*App).handleClaimedTasks},
		{Pattern: opsEscalatedPath, Serve: (*App).handleEscalatedTasks},
		{Pattern: opsCancelledPath, Serve: (*App).handleCancelledTasks},
		{Pattern: opsNotesPath, Serve: (*App).handleOpenNotes},

		// The spec and delivery browser.
		{Pattern: specPath, Serve: (*App).handleSpec},
		{Pattern: specProductsPath, Serve: (*App).handleSpecProducts},
		{Pattern: specProductPath, Serve: (*App).handleCapabilityMap},
		{Pattern: specProductPath + "/decisions", Serve: (*App).handleSpecDecisions},
		{Pattern: specProductPath + "/personas", Serve: (*App).handleSpecPersonas},
		{Pattern: specProductPath + "/non-goals", Serve: (*App).handleSpecNonGoals},
		{Pattern: specProductPath + "/delivery", Serve: (*App).handleSpecDelivery},
		// The per-container list and board have been replaced by the
		// product-wide Tasks and Board, scoped to the container this URL
		// named (FR f41a352d). Both retire the same way: a 302 into the
		// new view carrying that container, so a bookmarked milestone URL
		// still opens that milestone's work.
		{Pattern: specProductPath + "/milestones/{mid}/tasks", Successor: legacyTaskSuccessor(tasksSuffix)},
		// The per-container task DETAIL retires too (FR 0c03eac1), but
		// into the product-scoped detail rather than a list: the successor
		// carries the tid alone, because that URL resolves the task's own
		// container from the task rather than from the path.
		{Pattern: specProductPath + "/milestones/{mid}/tasks/{tid}", Successor: legacyTaskDetailSuccessor},
		{Pattern: specProductPath + "/milestones/{mid}/board", Successor: legacyTaskSuccessor(boardSuffix)},

		// The design-session browser.
		{Pattern: designPath, Serve: (*App).handleDesign},
		{Pattern: "GET /design/products/{productID}/design-sessions", Serve: (*App).handleDesignSessionList},
		{Pattern: "GET /design/design-sessions/{id}", Serve: (*App).handleDesignSessionDetail},
	}
}

// bind closes an App method over the receiver, turning a method expression
// into the handler the mux registers. legacyURLs stores method expressions
// rather than bound handlers so the table stays plain data that does not
// need an App to build.
func bind(app *App, h func(*App, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h(app, w, r) }
}

// mountLegacyRoutes registers every pre-redesign URL from legacyURLs,
// behind the sign-in gate, redirecting to its successor when one is named
// and otherwise serving its existing page inside the shell.
//
// The registrations live in one table so that "no pre-redesign URL 404s"
// is checkable rather than assumed: the acceptance test walks this same
// table, so a URL dropped from it fails a test instead of quietly 404ing
// for an operator who had it bookmarked.
func (app *App) mountLegacyRoutes(mux *http.ServeMux) {
	app.mountLegacyTable(mux, legacyURLs())
}

// mountLegacyTable registers one table of pre-redesign URLs, and is
// mountLegacyRoutes with the table as an argument.
//
// Every entry currently serves, so no live route takes the Successor
// branch and a test cannot reach it through the production registrations.
// Taking the table as a parameter lets one mount a doctored copy and
// drive the branch a phase gets the moment it names a successor.
func (app *App) mountLegacyTable(mux *http.ServeMux, table []legacyURL) {
	for _, l := range table {
		switch {
		case l.Successor != nil:
			mux.HandleFunc(l.Pattern, app.readerRoute(app.serveLegacy(l)))
		case l.Serve != nil:
			mux.HandleFunc(l.Pattern, app.readerRoute(bind(app, l.Serve)))
		default:
			// A programming mistake rather than a runtime condition: an
			// entry with neither field would register a route that serves
			// nothing, which is the one outcome this table exists to make
			// impossible.
			panic("legacy URL " + l.Pattern + " names neither a page nor a successor")
		}
	}
}

// serveLegacy is a legacy URL's redirect-to-successor handler. The status
// is 302 rather than 301: a pre-redesign URL is a live link an operator
// may keep following, and 302 is the one that does not let a browser pin
// the old URL in its cache past the page it now names.
func (app *App) serveLegacy(l legacyURL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target, ok := l.Successor(app, r)
		if !ok {
			// An un-prefixed URL must always land somewhere, so a scope
			// with no product to resolve renders the product index rather
			// than a redirect with nowhere to go.
			app.renderShell(w, r, "No products in this scope", specProductsPath,
				pages.NoProductsInScope())
			return
		}
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// legacyTaskSuccessor is the successor for a pre-redesign per-container
// tasks or board URL: the product-wide view of the same area, scoped to the
// container the old URL named.
//
// So a bookmarked milestone URL keeps opening that milestone's work rather
// than the whole product's -- the redirect carries the scope, it does not
// merely change the page. A per-container task DETAIL is not one of these
// (see legacyTaskDetailSuccessor): it retires into the detail, not a list.
func legacyTaskSuccessor(suffix string) func(*App, *http.Request) (string, bool) {
	return func(app *App, r *http.Request) (string, bool) {
		// specProductPath's own wildcard is {id}, and the container is
		// {mid}: the two the legacy pattern declares.
		pid, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			return "", false
		}
		container, ok := legacyTaskContainer(app, r, pid)
		if !ok {
			return "", false
		}
		return productTaskContainerHref(pid, suffix, container), true
	}
}

// legacyTaskContainer is the container a pre-redesign per-container URL
// named, in the form the product-wide scope query needs: its id, and which
// of the two single-container modes it belongs to.
//
// The kind is read from the product's own delivery listing rather than
// assumed, because the legacy URL served a milepebble's tasks at the same
// path a milestone's used and only the id says which. A listing that cannot
// be read, or an id it does not carry, answers as a milestone: the scope
// query is then one the product-wide page resolves or refuses in its own
// right -- an in-shell 404 for an id this product does not own -- which is
// a better answer than a redirect with nowhere honest to go.
func legacyTaskContainer(app *App, r *http.Request, pid uuid.UUID) (taskContainer, bool) {
	mid, err := uuid.Parse(r.PathValue("mid"))
	if err != nil {
		return taskContainer{}, false
	}
	asMilestone := taskContainer{ID: mid, Kind: string(store.MilestoneKindMilestone)}

	listing, err := app.spec.Delivery(r.Context(), pid, nil)
	if err != nil {
		logger.Warn("legacy task URL: delivery listing read failed; redirecting as a milestone scope",
			"product", pid.String(), "container", mid.String(), "error", err)
		return asMilestone, true
	}
	container, found := resolveTaskContainer(listing, mid)
	if !found {
		return asMilestone, true
	}
	return container, true
}

// legacyTaskDetailSuccessor is the successor for the pre-redesign
// per-container task detail: the product-scoped detail, carrying the tid
// alone (FR 0c03eac1).
//
// Unlike the list and the board, this is not scoped to the {mid} the old
// URL named. The product-scoped detail resolves the task's own container
// from the task itself, so the target needs no container and a task that
// moved between the old URL's milestone and another still lands on it --
// which is the point: an operator following an old link reads the task.
//
// The {mid} is still parsed, because an unparseable one is not a URL this
// table kept alive, and serveLegacy renders the product index rather than
// a redirect nowhere.
func legacyTaskDetailSuccessor(app *App, r *http.Request) (string, bool) {
	pid, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return "", false
	}
	tid, err := uuid.Parse(r.PathValue("tid"))
	if err != nil {
		return "", false
	}
	if _, err := uuid.Parse(r.PathValue("mid")); err != nil {
		return "", false
	}
	return productTaskDetailPath(pid, tid), true
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
	r, _ = app.rememberUnprefixedProduct(w, r)
	app.renderShell(w, r, "Ops console", opsPath, pages.AreaIndex("Ops console", opsIndexLinks))
}

// handleDesign is the design-session browser root. It is the entry point
// into the read sub-pages registered under this prefix in mountShellRoutes
// (design_page.go): a product's session list and one session's
// revision-event log + open questions. "/design" names no product, so the
// root resolves one server-side and links to that product's session list
// rather than asking the operator for an id.
func (app *App) handleDesign(w http.ResponseWriter, r *http.Request) {
	r, product, ok := app.resolveUnprefixedProduct(w, r)
	if !ok {
		return
	}
	app.renderShell(w, r, "Design sessions", designPath,
		pages.DesignRoot(product.Name, designProductSessionsPath(product.ID)))
}

// handleSpec is the spec + delivery browser root. It is a static landing
// (like the ops and design roots) that links into the store-backed spec
// pages under specProductsPath -- the product index, and per product the
// capability map, load-bearing decisions, personas, and non-goals (all in
// spec_page.go). The landing itself reads nothing, so the area root stays
// cheap.
func (app *App) handleSpec(w http.ResponseWriter, r *http.Request) {
	app.renderShell(w, r, "Spec & delivery", specPath, pages.AreaIndex("Spec & delivery", []pages.AreaLink{
		{Path: specProductsPath, Label: "Products", Blurb: "Browse a product's capability map, load-bearing decisions, personas, and non-goals."},
	}))
}

// handleProductPlaceholder serves every product-scoped sub-path whose own
// page has not shipped. It resolves the product the URL names -- so the
// prefixes, the in-shell 404, and the last-viewed cookie are all live and
// testable from this point -- and renders a body that names the product
// and links onward, rather than the area's real content.
func (app *App) handleProductPlaceholder(w http.ResponseWriter, r *http.Request) {
	r, product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	setLastViewedProductCookie(w, product.ID)

	app.renderShell(w, r, product.Name, r.URL.Path,
		pages.ProductPlaceholder(pages.ProductPlaceholderData{
			Product: productHeaderOf(product),
			Area:    r.PathValue("area"),
		}))
}
