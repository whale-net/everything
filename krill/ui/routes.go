package main

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// Route prefixes for the legacy, un-prefixed areas; the nav marks the active item
// by prefix.
const (
	opsPath    = "/ops"
	designPath = "/design"
	specPath   = "/spec"

	// credentialsPath sits outside "/credentials" because MountSelfServe owns that
	// prefix's JSON API; its {id} wildcard would conflict and panic the mux at boot.
	credentialsPath = "/account/credentials"
)

// Product-scoped prefixes. The product lives in the path so a copied link opens
// the same product for whoever follows it.
const (
	productsPath      = "/products"
	productPathPrefix = productsPath + "/{pid}"

	overviewSuffix       = "/overview"
	needsAttentionSuffix = "/needs-attention"
	tasksSuffix          = "/tasks"
	boardSuffix          = "/board"
	milestonesSuffix     = "/milestones"
)

// productHref is the one place a link under productsPath is spelled, so pages
// never hand out a URL the mux does not serve.
func productHref(pid uuid.UUID, suffix string) string {
	return productsPath + "/" + pid.String() + suffix
}

// handleShellHome serves the Overview for the product "/" resolves (last-viewed
// cookie in scope, else first in scope). A failed or empty resolve renders the
// empty state; the nav key is the Overview item's own path.
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

// ── legacy URL continuity ───────────────────────────────────────────────────

// legacyURL is one legacy URL and how it resolves. Exactly one of Serve
// (render the existing page) or Successor (redirect) is set.
type legacyURL struct {
	// Pattern is the URL exactly as the mux spells it, including method and wildcards.
	Pattern string

	Serve func(app *App, w http.ResponseWriter, r *http.Request)

	// Successor builds the replacement URL; ok is false when no product resolves, and
	// the product index renders instead of a redirect to nowhere.
	Successor func(app *App, r *http.Request) (target string, ok bool)
}

// legacyURLs is every legacy URL that must keep resolving. An entry keeps
// serving until its replacement ships, then moves to Successor.
func legacyURLs() []legacyURL {
	return []legacyURL{
		// The ops console's read views retire into Needs attention's matching tabs.
		{Pattern: "/{$}", Serve: (*App).handleShellHome},
		{Pattern: opsPath, Successor: legacyNeedsAttentionSuccessor("")},
		{Pattern: opsClaimedPath, Successor: legacyNeedsAttentionSuccessor(needsAttentionTabClaimed)},
		{Pattern: opsEscalatedPath, Successor: legacyNeedsAttentionSuccessor(needsAttentionTabEscalated)},
		{Pattern: opsCancelledPath, Successor: legacyNeedsAttentionSuccessor(needsAttentionTabCancelled)},
		{Pattern: opsNotesPath, Successor: legacyNeedsAttentionSuccessor(needsAttentionTabNotes)},

		{Pattern: specPath, Serve: (*App).handleSpec},
		{Pattern: specProductsPath, Serve: (*App).handleSpecProducts},
		{Pattern: specProductPath, Serve: (*App).handleCapabilityMap},
		// The feature quick-look blade; under the product prefix so a shared link resolves
		// its product first.
		{Pattern: specProductPath + specFeatureSuffix, Serve: (*App).handleSpecFeature},
		{Pattern: specProductPath + "/decisions", Serve: (*App).handleSpecDecisions},
		{Pattern: specProductPath + "/personas", Serve: (*App).handleSpecPersonas},
		{Pattern: specProductPath + "/non-goals", Serve: (*App).handleSpecNonGoals},
		// Delivery redirects to the Milestones table, carrying the query.
		{Pattern: specProductPath + "/delivery", Successor: legacyDeliverySuccessor},
		// Per-container list and board redirect to the product-wide views scoped to the
		// same container.
		{Pattern: specProductPath + "/milestones/{mid}/tasks", Successor: legacyTaskSuccessor(tasksSuffix)},
		// The per-container task detail redirects to the product-scoped detail by tid alone.
		{Pattern: specProductPath + "/milestones/{mid}/tasks/{tid}", Successor: legacyTaskDetailSuccessor},
		{Pattern: specProductPath + "/milestones/{mid}/board", Successor: legacyTaskSuccessor(boardSuffix)},

		// The design-session browser. The root serves the resolved product's session list.
		{Pattern: designPath, Serve: (*App).handleDesign},
		{Pattern: "GET /design/products/{productID}/design-sessions", Serve: (*App).handleDesignSessionList},
		// The old session detail redirects to the product-scoped one, with the pid read
		// from the session row.
		{Pattern: "GET /design/design-sessions/{id}", Successor: legacyDesignSessionDetailSuccessor},
	}
}

// bind closes an App method expression over app, so legacyURLs stays plain data.
func bind(app *App, h func(*App, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h(app, w, r) }
}

// mountLegacyRoutes registers every legacy URL behind the sign-in gate.
func (app *App) mountLegacyRoutes(mux *http.ServeMux) {
	app.mountLegacyTable(mux, legacyURLs())
}

// mountLegacyTable registers one table of legacy URLs; taking it as a parameter
// lets tests mount a doctored copy.
func (app *App) mountLegacyTable(mux *http.ServeMux, table []legacyURL) {
	for _, l := range table {
		switch {
		case l.Successor != nil:
			mux.HandleFunc(l.Pattern, app.readerRoute(app.serveLegacy(l)))
		case l.Serve != nil:
			mux.HandleFunc(l.Pattern, app.readerRoute(bind(app, l.Serve)))
		default:
			// An entry with neither field is a programming error.
			panic("legacy URL " + l.Pattern + " names neither a page nor a successor")
		}
	}
}

// serveLegacy redirects to the successor with 302: not 301, so browsers do not
// cache the hop, and not 307/308, which would preserve a non-GET method and 405.
func (app *App) serveLegacy(l legacyURL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target, ok := l.Successor(app, r)
		if !ok {
			app.renderShell(w, r, "No products in this scope", specProductsPath,
				pages.NoProductsInScope())
			return
		}
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// legacyTaskSuccessor redirects a per-container tasks or board URL to the
// product-wide view scoped to that same container.
func legacyTaskSuccessor(suffix string) func(*App, *http.Request) (string, bool) {
	return func(app *App, r *http.Request) (string, bool) {
		// specProductPath's wildcard is {id}; the container is {mid}.
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

// legacyNeedsAttentionSuccessor redirects an ops console URL to the Needs attention
// tab for the un-prefixed product; "" is the default tab. It only reads, so the
// landing page records the last-viewed product.
func legacyNeedsAttentionSuccessor(tab string) func(*App, *http.Request) (string, bool) {
	return func(app *App, r *http.Request) (string, bool) {
		product, err := app.resolveProductForUnprefixed(r)
		if err != nil || product.ID == uuid.Nil {
			return "", false
		}
		return needsAttentionTabHref(product.ID, tab), true
	}
}

// legacyDeliverySuccessor redirects to the product's Milestones table, re-attaching
// the raw query so status filters and expansions survive without listing names here.
func legacyDeliverySuccessor(app *App, r *http.Request) (string, bool) {
	pid, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return "", false
	}
	target := productHref(pid, milestonesSuffix)
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	return target, true
}

// legacyTaskContainer resolves the container a legacy URL named, reading its kind
// from the delivery listing. An unreadable listing or unknown id falls back to a
// milestone scope, which the target page resolves or 404s itself.
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

// legacyDesignSessionDetailSuccessor redirects to the product-scoped session
// detail, reading the pid from the session row; an unreadable session renders the
// product index.
func legacyDesignSessionDetailSuccessor(app *App, r *http.Request) (string, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return "", false
	}
	ds, err := app.designSessions.GetByID(r.Context(), id)
	if err != nil {
		logger.Warn("legacy design session URL: could not resolve the session's product; rendering the product index",
			"design_session_id", id, "error", err)
		return "", false
	}
	return designSessionPath(ds.ProductID, id), true
}

// legacyTaskDetailSuccessor redirects to the product-scoped detail by tid alone,
// since that page resolves the task's container itself. A recognised ?tab= is
// carried through taskDetailTabOf so only known tabs survive.
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
	target := productTaskDetailPath(pid, tid)
	if tab := taskDetailTabOf(r); tab != pages.TaskTabOverview {
		target += "?tab=" + tab
	}
	return target, true
}

// handleDesign resolves a product server-side and serves its session list, so the
// operator never types an id.
func (app *App) handleDesign(w http.ResponseWriter, r *http.Request) {
	r, product, ok := app.resolveUnprefixedProduct(w, r)
	if !ok {
		return
	}
	// designPath as nav key: the sidebar item owns the product-scoped path.
	app.renderDesignSessionList(w, r, product.ID, designPath)
}

// handleSpec is a static landing linking into the spec pages; it reads nothing.
func (app *App) handleSpec(w http.ResponseWriter, r *http.Request) {
	app.renderShell(w, r, "Spec & delivery", specPath, pages.AreaIndex("Spec & delivery", []pages.AreaLink{
		{Path: specProductsPath, Label: "Products", Blurb: "Browse a product's capability map, load-bearing decisions, personas, and non-goals."},
	}))
}
