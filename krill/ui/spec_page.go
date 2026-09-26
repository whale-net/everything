// The spec browser: a product's capability map, its load-bearing
// decisions, its personas, and its non-goals, each rendered inside the
// shell chrome. Every page is scoped to one product at /spec/products/{id}
// and reads through app.spec (readclient.go) -- the same //krill/slice
// Querier and //krill/store readers the MCP tools get_product_slice /
// list_personas / list_non_goals call underneath, so a page and the
// matching tool always show the same current spec.
//
// All four pages render current revisions only: the reader's GetCurrent /
// ListCurrentByProduct methods never touch history, so a superseded
// revision is never surfaced.
//
// Every page body is a templ component under krill/ui/pages; this file
// keeps the handlers, the route spellings, and the pure view-model
// builders that the field-parity tests exercise without a database.
package main

import (
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
)

// specProductsPath lists the products an operator can browse into;
// specProductPath is the prefix every per-product page hangs off. The
// delivery/roadmap view a sibling task adds under /spec does not collide
// with these, which all live under the /spec/products/{id} prefix.
const (
	specProductsPath = specPath + "/products"
	specProductPath  = specPath + "/products/{id}"
)

// specProductID validates the {id} path value as a product id, writing a
// shell-rendered 400 and returning ok=false when it is not a UUID.
func specProductID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		renderSpecStatus(w, r, http.StatusBadRequest, pages.StatusPage{
			Title:    "Bad product id",
			Detail:   "The product id in the URL is not a UUID.",
			BackHref: specProductsPath,
			BackText: "Back to products",
		})
		return uuid.Nil, false
	}
	return id, true
}

// renderSpecError maps a spec read error to a page. store.ErrNotFound
// (an unknown or superseded product) is a 404 the operator can act on;
// anything else is a genuine read failure and is logged at ERROR before a
// 500. Both render inside the shell, not as a bare http.Error string.
func renderSpecError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		renderSpecStatus(w, r, http.StatusNotFound, pages.StatusPage{
			Title:    "Not found",
			Detail:   "No current product matches that id.",
			BackHref: specProductsPath,
			BackText: "Back to products",
		})
		return
	}
	logger.Error("spec read failed", "error", err)
	// Every spec page carries a Refresh button whose hx-get is this same
	// route, so this path is reachable by htmx -- and htmx does not swap
	// on a 500. A bare error page here would leave the operator clicking
	// Refresh with no feedback at all, which is the one thing they most
	// need to be told. Answer 200 with the message inline; the no-JS
	// browser still gets the full status-coded page.
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, pages.SpecInlineError("Could not load the spec. The spec store could not be read; see the logs."))
		return
	}
	renderSpecStatus(w, r, http.StatusInternalServerError, pages.StatusPage{
		Title:    "Could not load the spec",
		Detail:   "The spec store could not be read. See the logs.",
		BackHref: specProductsPath,
		BackText: "Back to products",
	})
}

// renderSpecStatus renders the spec area's error body through the shell
// with an explicit status, for the cases that have no data view of their
// own (a bad id, an unknown product, a failed store read, and the
// not-yet-wired cases that reuse it).
//
// The status necessarily lives here rather than in the component: templ
// components are body-writers with no status concept, so renderShellStatus
// keeps owning the response and this only supplies the body.
func renderSpecStatus(w http.ResponseWriter, r *http.Request, status int, page pages.StatusPage) {
	renderShellStatus(w, r, "Spec", specPath, pages.SpecStatus(page), status)
}

// renderSpecPage serves one spec page in both modes off its single
// route: an htmx request gets the page's own content region as a bare
// 200 fragment, and a browser gets that same component inside the shell
// chrome. The Refresh button each page carries re-requests its own path
// with HX-Request set, which is what lands on the fragment branch.
func renderSpecPage(w http.ResponseWriter, r *http.Request, title string, body templ.Component) {
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, body)
		return
	}
	renderShell(w, r, title, specPath, body)
}

// productHeaderOf builds the banner from a store product's own current
// row and the {id} it is browsed at.
func productHeaderOf(p store.Product) pages.ProductHeader {
	return pages.ProductHeader{Name: p.Name, Vision: p.Vision, Href: productPath(p.ID)}
}

// productHeaderOfEntity builds the same banner from a slice.Document's
// optional Product entity, for the pages that already have the document
// (the capability map and decisions) and do not need a second product
// read. A nil Product -- which get_product_slice does not produce on
// success, but the shared type allows -- yields a name-less banner rather
// than a panic.
func productHeaderOfEntity(p *slice.ProductEntity, id uuid.UUID) pages.ProductHeader {
	if p == nil {
		return pages.ProductHeader{Href: productPath(id)}
	}
	return pages.ProductHeader{Name: p.Name, Vision: p.Vision, Href: productPath(id)}
}

// productPath is the capability-map page for a product.
func productPath(id uuid.UUID) string {
	return specPath + "/products/" + id.String()
}

// decisionsPath, personasPath, nonGoalsPath are the other three per-product
// spec pages; deliveryPath is the delivery/roadmap view (delivery_page.go).
// All four hang off the /spec/products/{id} prefix, so the cross-nav and the
// route table agree on one spelling.
func decisionsPath(id uuid.UUID) string { return productPath(id) + "/decisions" }
func personasPath(id uuid.UUID) string  { return productPath(id) + "/personas" }
func nonGoalsPath(id uuid.UUID) string  { return productPath(id) + "/non-goals" }
func deliveryPath(id uuid.UUID) string  { return productPath(id) + "/delivery" }

// productNavFor builds the five per-product cross-links, marking the one
// matching current as active. The component that renders them is
// components.SubNav, shared with the delivery page so the two spell the
// cross-nav once.
//
// The links are plain <a> navigation rather than htmx swaps: moving
// between them is navigation, not a refresh of the current view.
func productNavFor(id uuid.UUID, current string) []components.NavLink {
	links := []components.NavLink{
		{Label: "Capability map", Href: productPath(id)},
		{Label: "Decisions", Href: decisionsPath(id)},
		{Label: "Personas", Href: personasPath(id)},
		{Label: "Non-goals", Href: nonGoalsPath(id)},
		{Label: "Delivery", Href: deliveryPath(id)},
	}
	for i := range links {
		links[i].Active = links[i].Href == current
	}
	return links
}

// -- product index --------------------------------------------------------

// handleSpecProducts lists the current products in the deployment's sole
// scope so an operator can pick one to browse. It mirrors the mcp
// list_products discovery shape, which also resolves the sole scope rather
// than asking the caller to name one.
func (app *App) handleSpecProducts(w http.ResponseWriter, r *http.Request) {
	products, err := app.spec.Products(r.Context())
	if err != nil {
		renderSpecError(w, r, err)
		return
	}

	items := make([]pages.ProductListItem, 0, len(products))
	for _, p := range products {
		items = append(items, pages.ProductListItem{
			Name:        p.Name,
			Vision:      p.Vision,
			CapabilityH: productPath(p.ID),
		})
	}
	renderSpecPage(w, r, "Products", pages.Products(pages.ProductsPage{
		Path:  specProductsPath,
		Items: items,
	}))
}

// -- capability map -------------------------------------------------------

// handleCapabilityMap renders a product's FeatureSets, Features, and
// Requirements (current revisions only) as a navigable capability map --
// get_product_slice's shape, nested by parent. FR 638a7e5f.
func (app *App) handleCapabilityMap(w http.ResponseWriter, r *http.Request) {
	productID, ok := specProductID(w, r)
	if !ok {
		return
	}

	doc, err := app.spec.ProductSlice(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}

	renderSpecPage(w, r, "Capability map", pages.CapabilityMap(capabilityPageOf(doc, productID)))
}

// capabilityPageOf assembles the capability map from a slice.Document,
// grouping the flat entities by parent id into FeatureSet -> Feature ->
// Requirement and copying every field get_product_slice returns. Pure, so
// the field-parity with the MCP wire is unit-testable without a database.
func capabilityPageOf(doc slice.Document, productID uuid.UUID) pages.CapabilityPage {
	page := pages.CapabilityPage{
		Product: productHeaderOfEntity(doc.Product, productID),
		Nav:     productNavFor(productID, productPath(productID)),
		Path:    productPath(productID),
	}

	// Index requirements and features by parent id so the component can
	// nest them without a second pass per level.
	reqsByFeature := map[uuid.UUID][]pages.CapabilityRequirement{}
	for _, rq := range doc.Requirements {
		reqsByFeature[rq.FeatureID] = append(reqsByFeature[rq.FeatureID], pages.CapabilityRequirement{
			ID:   rq.ID.String(),
			Kind: rq.Kind,
			Name: rq.Name,
			Body: deref(rq.Body),
		})
	}
	featsBySet := map[uuid.UUID][]pages.CapabilityFeature{}
	for _, f := range doc.Features {
		featsBySet[f.FeatureSetID] = append(featsBySet[f.FeatureSetID], pages.CapabilityFeature{
			ID:           f.ID.String(),
			Number:       f.DisplayNumber,
			Name:         f.Name,
			Description:  deref(f.Description),
			Requirements: reqsByFeature[f.ID],
		})
	}
	for _, fs := range doc.FeatureSets {
		page.FeatureSets = append(page.FeatureSets, pages.CapabilityFeatureSet{
			ID:          fs.ID.String(),
			Name:        fs.Name,
			Description: deref(fs.Description),
			Features:    featsBySet[fs.ID],
		})
	}
	return page
}

// -- load-bearing decisions -----------------------------------------------

// handleSpecDecisions lists a product's current LoadBearingDecisions with
// their full body text -- get_product_slice's decisions, unchanged (FR
// 6aa70e3a).
func (app *App) handleSpecDecisions(w http.ResponseWriter, r *http.Request) {
	productID, ok := specProductID(w, r)
	if !ok {
		return
	}

	doc, err := app.spec.ProductSlice(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}

	renderSpecPage(w, r, "Decisions", pages.Decisions(decisionsPageOf(doc, productID)))
}

// decisionsPageOf assembles the decisions list, copying each decision's
// full body and LBn exactly as get_product_slice returns them. Pure, so the
// full-body / field-parity contract is unit-testable without a database.
func decisionsPageOf(doc slice.Document, productID uuid.UUID) pages.DecisionsPage {
	page := pages.DecisionsPage{
		Product: productHeaderOfEntity(doc.Product, productID),
		Nav:     productNavFor(productID, decisionsPath(productID)),
		Path:    decisionsPath(productID),
	}
	for _, d := range doc.Decisions {
		page.Decisions = append(page.Decisions, pages.DecisionItem{
			ID:     d.ID.String(),
			Number: d.DisplayNumber,
			Name:   d.Name,
			Body:   deref(d.Body),
		})
	}
	return page
}

// -- personas -------------------------------------------------------------

// handleSpecPersonas lists a product's current Personas -- list_personas'
// shape (FR b4c1c77f).
func (app *App) handleSpecPersonas(w http.ResponseWriter, r *http.Request) {
	productID, ok := specProductID(w, r)
	if !ok {
		return
	}

	product, err := app.spec.Product(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}
	personas, err := app.spec.Personas(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}

	renderSpecPage(w, r, "Personas", pages.Personas(personasPageOf(product, personas, productID)))
}

// personasPageOf assembles the personas list, copying every field
// list_personas returns (id, name, description). Pure, so field-parity is
// unit-testable without a database.
func personasPageOf(product store.Product, personas []store.Persona, productID uuid.UUID) pages.PersonasPage {
	page := pages.PersonasPage{
		Product: productHeaderOf(product),
		Nav:     productNavFor(productID, personasPath(productID)),
		Path:    personasPath(productID),
	}
	for _, p := range personas {
		page.Personas = append(page.Personas, pages.PersonaItem{
			ID:          p.ID.String(),
			Name:        p.Name,
			Description: deref(p.Description),
		})
	}
	return page
}

// -- non-goals ------------------------------------------------------------

// nonGoalHeadings are the two kinds' display labels, in the order the page
// lists them (permanent first, then deferred).
var nonGoalHeadings = []struct{ kind, heading string }{
	{string(store.NonGoalKindPermanent), "Permanent non-goals"},
	{string(store.NonGoalKindDeferred), "Deferred, not foreclosed"},
}

// handleSpecNonGoals lists a product's current Non-Goals, both the
// permanent and deferred kinds -- list_non_goals' shape (FR b4c1c77f).
func (app *App) handleSpecNonGoals(w http.ResponseWriter, r *http.Request) {
	productID, ok := specProductID(w, r)
	if !ok {
		return
	}

	product, err := app.spec.Product(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}
	nonGoals, err := app.spec.NonGoals(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}

	renderSpecPage(w, r, "Non-goals", pages.NonGoals(nonGoalsPageOf(product, nonGoals, productID)))
}

// nonGoalsPageOf assembles the non-goals list, copying every field
// list_non_goals returns (id, kind, name, body) and bucketing by kind so
// the permanent vs deferred distinction is explicit. Pure, so field-parity
// is unit-testable without a database.
func nonGoalsPageOf(product store.Product, nonGoals []store.NonGoal, productID uuid.UUID) pages.NonGoalsPage {
	page := pages.NonGoalsPage{
		Product: productHeaderOf(product),
		Nav:     productNavFor(productID, nonGoalsPath(productID)),
		Path:    nonGoalsPath(productID),
	}
	// Only non-empty kinds become a group, so the "no non-goals" state
	// renders when both kinds are empty.
	byKind := map[string][]pages.NonGoalItem{}
	for _, n := range nonGoals {
		byKind[string(n.Kind)] = append(byKind[string(n.Kind)], pages.NonGoalItem{
			ID:   n.ID.String(),
			Kind: string(n.Kind),
			Name: n.Name,
			Body: deref(n.Body),
		})
	}
	for _, h := range nonGoalHeadings {
		if items := byKind[h.kind]; len(items) > 0 {
			page.Groups = append(page.Groups, pages.NonGoalGroup{
				Kind:     h.kind,
				Heading:  h.heading,
				NonGoals: items,
			})
		}
	}
	return page
}

// -- helpers --------------------------------------------------------------

// deref unwraps an optional text field, treating absent as empty so a
// component never has to nil-check a body or description.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
