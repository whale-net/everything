// The spec browser: ONE tabbed page per product, whose four tabs are
// that product's capability map, load-bearing decisions, personas, and
// non-goals. Each tab is its own URL -- /spec/products/{id} and its three
// suffix paths -- and the sidebar's four Spec links point at exactly
// those, so switching tabs is navigation and the four keep resolving
// (FR df5bffd1).
//
// The page is scoped to one product and reads through app.spec
// (readclient.go) -- the same //krill/slice Querier and //krill/store
// readers the MCP tools get_product_slice / list_personas / list_non_goals
// call underneath, so a page and the matching tool always show the same
// current spec.
//
// All four tabs render current revisions only: the reader's GetCurrent /
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
	"sort"
	"strings"

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

	// The three tabbed spec URLs' own suffixes. They are spelled once so
	// the route table, the path builders below and specTabOf cannot
	// disagree about which address is which tab.
	decisionsSuffix = "/decisions"
	personasSuffix  = "/personas"
	nonGoalsSuffix  = "/non-goals"
)

// specProductID validates the {id} path value as a product id, writing a
// shell-rendered 400 and returning ok=false when it is not a UUID.
//
// A valid id also becomes the last-viewed product, so the operator's next
// un-prefixed page lands on the product they were just reading. The cookie
// is a hint -- an id from another scope is discarded on the way back in --
// so writing it here costs these pages nothing they would not have paid.
func (app *App) specProductID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		app.renderSpecStatus(w, r, http.StatusBadRequest, pages.StatusPage{
			Title:    "Bad product id",
			Detail:   "The product id in the URL is not a UUID.",
			BackHref: specProductsPath,
			BackText: "Back to products",
		})
		return uuid.Nil, false
	}
	setLastViewedProductCookie(w, id)
	return id, true
}

// renderSpecError maps a spec read error to a page. store.ErrNotFound
// (an unknown or superseded product) is a 404 the operator can act on;
// anything else is a genuine read failure and is logged at ERROR before a
// 500. Both render inside the shell, not as a bare http.Error string.
func (app *App) renderSpecError(w http.ResponseWriter, r *http.Request, anchor string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		app.renderSpecStatus(w, r, http.StatusNotFound, pages.StatusPage{
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
		renderFragment(w, r, pages.SpecInlineError(anchor, "Could not load the spec. The spec store could not be read; see the logs."))
		return
	}
	app.renderSpecStatus(w, r, http.StatusInternalServerError, pages.StatusPage{
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
func (app *App) renderSpecStatus(w http.ResponseWriter, r *http.Request, status int, page pages.StatusPage) {
	app.renderShellStatus(w, r, "Spec", r.URL.Path, pages.SpecStatus(page), status)
}

// renderSpecPage serves one spec page in both modes off its single
// route: an htmx request gets the page's own content region as a bare
// 200 fragment, and a browser gets that same component inside the shell
// chrome. The Refresh button each page carries re-requests its own path
// with HX-Request set, which is what lands on the fragment branch.
func (app *App) renderSpecPage(w http.ResponseWriter, r *http.Request, title string, body templ.Component) {
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, body)
		return
	}
	app.renderShell(w, r, title, r.URL.Path, body)
}

// renderSpecTabPage serves the Spec page in three modes off its four tab
// routes, told apart by HX-Target because each replaces a different region
// of the same page:
//
//   - a tab names the swap region and gets it back whole -- strip and
//     panel together -- so the active marking travels with the panel;
//   - the panel's own Refresh button names the panel's content region and
//     gets just that region. It re-requests the tab's own path, so a
//     Refresh stays on the tab the operator is on;
//   - anything else is a browser request, and gets the page in the shell.
//
// Deciding on anything else -- the tab suffix, say -- would make a Refresh
// taken on a non-Capabilities tab serve a bare swap region for htmx to
// splice in beside the page.
func (app *App) renderSpecTabPage(w http.ResponseWriter, r *http.Request, title string, productID uuid.UUID, panel templ.Component) {
	htmx := r.Header.Get("HX-Request") != ""
	if htmx && !specTabSwapRequested(r) {
		renderFragment(w, r, panel)
		return
	}
	tab := specTabOf(r)
	body := pages.SpecTabs(pages.SpecTabsPage{
		Tabs:  specTabsOf(productID, tab),
		Tab:   tab,
		Panel: panel,
	})
	if htmx {
		renderFragment(w, r, body)
		return
	}
	app.renderShell(w, r, title, r.URL.Path, body)
}

// specTabSwapRequested reports whether this htmx request asked for the
// swap region itself.
//
// htmx sends the resolved target's id in HX-Target, so the target is the
// request's own statement of which region it is replacing: a tab names the
// swap region, the panel's Refresh button names the panel's content
// region.
func specTabSwapRequested(r *http.Request) bool {
	return r.Header.Get("HX-Target") == pages.SpecPanelAnchor
}

// specTabOf resolves which of the four spec tabs a request is for, from
// the URL itself -- the tab IS the address, not a parameter beside it.
//
// A path naming no tab is Capabilities, and so is one naming a tab this
// page does not have: the tab is read off whatever path arrived, so a
// hand-edited or stale link reaches here as readily as a copied one, and
// Capabilities is the tab every spec path can render.
func specTabOf(r *http.Request) string {
	switch {
	case strings.HasSuffix(r.URL.Path, decisionsSuffix):
		return pages.SpecTabDecisions
	case strings.HasSuffix(r.URL.Path, personasSuffix):
		return pages.SpecTabPersonas
	case strings.HasSuffix(r.URL.Path, nonGoalsSuffix):
		return pages.SpecTabNonGoals
	default:
		return pages.SpecTabCapabilities
	}
}

// specTabsOf builds the strip: the four tabs, each at the real path its
// own page is served at, with the one this request resolved to marked
// active. The labels are the sidebar's own Spec link labels, so the two
// spell the same four destinations.
func specTabsOf(productID uuid.UUID, current string) []pages.SpecTab {
	tabs := []pages.SpecTab{
		{Key: pages.SpecTabCapabilities, Label: "Capabilities", Href: productPath(productID)},
		{Key: pages.SpecTabDecisions, Label: "Decisions", Href: decisionsPath(productID)},
		{Key: pages.SpecTabPersonas, Label: "Personas", Href: personasPath(productID)},
		{Key: pages.SpecTabNonGoals, Label: "Non-goals", Href: nonGoalsPath(productID)},
	}
	for i := range tabs {
		tabs[i].Active = tabs[i].Key == current
	}
	return tabs
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
// All four hang off the /spec/products/{id} prefix, so the route table and
// the tab strip agree on one spelling.
func decisionsPath(id uuid.UUID) string { return productPath(id) + decisionsSuffix }
func personasPath(id uuid.UUID) string  { return productPath(id) + personasSuffix }
func nonGoalsPath(id uuid.UUID) string  { return productPath(id) + nonGoalsSuffix }
func deliveryPath(id uuid.UUID) string  { return productPath(id) + "/delivery" }

// productNavFor builds the five per-product cross-links, marking the one
// matching current as active. The component that renders them is
// components.SubNav.
//
// It survives for the delivery page alone: the spec area's own four links
// are the tab strip's tabs (specTabsOf), which reach three of these paths
// and no longer cross into Delivery.
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
		app.renderSpecError(w, r, pages.ProductsAnchor, err)
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
	app.renderSpecPage(w, r, "Products", pages.Products(pages.ProductsPage{
		Path:  specProductsPath,
		Items: items,
	}))
}

// -- capability map -------------------------------------------------------

// handleCapabilityMap renders a product's FeatureSets, Features, and
// Requirements (current revisions only) as a navigable capability map --
// get_product_slice's shape, nested by parent. FR 638a7e5f.
func (app *App) handleCapabilityMap(w http.ResponseWriter, r *http.Request) {
	productID, ok := app.specProductID(w, r)
	if !ok {
		return
	}

	doc, err := app.spec.ProductSlice(r.Context(), productID)
	if err != nil {
		app.renderSpecError(w, r, pages.CapabilityMapAnchor, err)
		return
	}

	// The Milestone column reads the product-wide delivery listing through
	// the same seam the Milestones table does (app.spec.Delivery, the
	// list_product_delivery querier underneath), so the milestone a spec
	// row names is the milestone the roadmap shows. A failed read costs the
	// column only -- the capability map itself has already been read
	// successfully by this point, so it still renders.
	listing, deliveryErr := app.capabilityMilestoneDelivery(r, productID)

	app.renderSpecTabPage(w, r, "Capability map", productID,
		pages.CapabilityMap(capabilityPageWithMilestonesOf(doc, productID, parseCapabilityExpansion(r), listing, deliveryErr)))
}

// capabilityMilestoneDelivery reads the delivery listing the Milestone
// column is built from, returning the listing and any error rather than
// failing the page.
//
// It is logged at WARNING, not ERROR: the read failed but the page still
// renders, with every cell saying what could not be read (AGENTS.md's
// logging levels -- ERROR is for an operation that cannot continue).
func (app *App) capabilityMilestoneDelivery(r *http.Request, productID uuid.UUID) (slice.DeliveryListing, error) {
	// A nil status filter means "all", mirroring the querier's contract.
	listing, err := app.spec.Delivery(r.Context(), productID, nil)
	if err != nil {
		logger.Warn("spec milestone delivery read failed; the Milestone column cannot be read",
			"product", productID.String(), "error", err)
		return slice.DeliveryListing{}, err
	}
	return listing, nil
}

// capabilityPageOf assembles the capability map from a slice.Document,
// grouping the flat entities by parent id into FeatureSet -> Feature ->
// Requirement and copying every field get_product_slice returns. Pure, so
// the field-parity with the MCP wire is unit-testable without a database.
//
// section is this request's expansion state, which the URL supplies -- see
// capabilityExpansion. It travels in rather than being read from the
// request here so the whole view model stays a pure function of its inputs.
//
// It carries no delivery data, so every Milestone cell renders blank. The
// handler builds the page through capabilityPageWithMilestonesOf, which is
// the one that can answer them; this form exists for the parity tests,
// which are about the get_product_slice read and have no delivery fixture.
func capabilityPageOf(doc slice.Document, productID uuid.UUID, section capabilityExpansion) pages.CapabilityPage {
	return capabilityPageWithMilestonesOf(doc, productID, section, slice.DeliveryListing{}, nil)
}

// capabilityPageWithMilestonesOf is capabilityPageOf plus the Milestone
// column's derivation (FR 18afc5a8), from the product-wide delivery listing
// -- the same read the Milestones table makes, so the two pages can never
// disagree about what delivers a feature.
//
// deliveryErr is the failed read's error, kept as an error rather than a
// bool because the caller's log line is worth having beside it. A non-nil
// one costs the Milestone column only: every cell states that delivery
// could not be read, and the rest of the map still renders.
func capabilityPageWithMilestonesOf(doc slice.Document, productID uuid.UUID, section capabilityExpansion, listing slice.DeliveryListing, deliveryErr error) pages.CapabilityPage {
	page := pages.CapabilityPage{
		Product: productHeaderOfEntity(doc.Product, productID),
		Path:    productPath(productID),
	}

	milestones := capabilityMilestoneIndexOf(listing, deliveryErr)

	// Index requirements and features by parent id so the component can
	// nest them without a second pass per level.
	reqsByFeature := map[uuid.UUID][]pages.CapabilityRequirement{}
	reqIDsByFeature := map[uuid.UUID][]uuid.UUID{}
	for _, rq := range doc.Requirements {
		reqsByFeature[rq.FeatureID] = append(reqsByFeature[rq.FeatureID], pages.CapabilityRequirement{
			ID:   rq.ID.String(),
			Kind: rq.Kind,
			Name: rq.Name,
			Body: deref(rq.Body),
		})
		reqIDsByFeature[rq.FeatureID] = append(reqIDsByFeature[rq.FeatureID], rq.ID)
	}
	featsBySet := map[uuid.UUID][]pages.CapabilityFeature{}
	for _, f := range doc.Features {
		featsBySet[f.FeatureSetID] = append(featsBySet[f.FeatureSetID], pages.CapabilityFeature{
			ID:               f.ID.String(),
			Number:           f.DisplayNumber,
			Name:             f.Name,
			Description:      deref(f.Description),
			RequirementCount: len(reqsByFeature[f.ID]),
			Requirements:     reqsByFeature[f.ID],
			Milestone:        milestones.cell(f.ID, reqIDsByFeature[f.ID]),
		})
	}
	// The first feature set is the default open one (FR 18afc5a8). Resolving
	// it here, where the page's own feature sets are in hand, is what keeps
	// "the default" an answer about THIS page rather than a bare flag the
	// href builders would have to re-derive per section.
	if len(doc.FeatureSets) > 0 {
		section = section.withFirstOpen(doc.FeatureSets[0].ID)
	}

	for _, fs := range doc.FeatureSets {
		page.FeatureSets = append(page.FeatureSets, pages.CapabilityFeatureSet{
			ID:           fs.ID.String(),
			Name:         fs.Name,
			Description:  deref(fs.Description),
			Features:     featsBySet[fs.ID],
			FeatureCount: len(featsBySet[fs.ID]),
			Expanded:     section.isExpanded(fs.ID),
			ExpandHref:   section.expandHref(fs.ID),
			CollapseHref: section.collapseHref(fs.ID),
		})
	}
	return page
}

// -- the Milestone column ------------------------------------------------

// capabilityMilestoneUnreadMessage is what every Milestone cell says when
// the delivery listing could not be read.
//
// It is not empty and it is not a blank cell: a blank cell asserts that
// nothing delivers the feature, which is a claim about delivery that a
// failed read cannot support.
const capabilityMilestoneUnreadMessage = "Milestone delivery could not be read. See the logs."

// milestoneBadgeSource is one milestone as the Milestone column needs it:
// its id (to count DISTINCT milestones rather than distinct associations),
// its name (what a single-milestone cell shows) and its status (the word
// components.MilestoneStatusStyle colours the badge by).
type milestoneBadgeSource struct {
	id     uuid.UUID
	name   string
	status string
}

// capabilityMilestoneIndex is the delivery listing indexed by the entities
// it delivers, so one feature's Milestone cell is a map read rather than a
// pass over every milestone.
type capabilityMilestoneIndex struct {
	byEntity map[uuid.UUID][]milestoneBadgeSource

	// Unread records that the listing itself could not be read, which
	// every cell reports instead of answering.
	Unread bool
}

// capabilityMilestoneIndexOf indexes listing by delivered entity id.
//
// Only TOP-LEVEL milestones are indexed (listing.Milestones), never a
// milestone's milepebbles: a milepebble's Delivers is a subset of its
// parent's, so counting milepebbles would read as several milestones
// delivering one feature where the cut has exactly one.
//
// A milestone delivering both a feature AND one of its requirements is
// indexed once for that feature's cell, so "N" counts milestones and not
// associations.
func capabilityMilestoneIndexOf(listing slice.DeliveryListing, err error) capabilityMilestoneIndex {
	idx := capabilityMilestoneIndex{byEntity: map[uuid.UUID][]milestoneBadgeSource{}, Unread: err != nil}
	if idx.Unread {
		return idx
	}
	for _, m := range listing.Milestones {
		source := milestoneBadgeSource{id: m.ID, name: m.Name, status: string(m.Status)}
		for _, f := range m.Delivers.Features {
			idx.byEntity[f.ID] = append(idx.byEntity[f.ID], source)
		}
		for _, r := range m.Delivers.Requirements {
			idx.byEntity[r.ID] = append(idx.byEntity[r.ID], source)
		}
	}
	return idx
}

// cell is one feature's Milestone cell, derived in the order the
// requirement spells out (FR 18afc5a8):
//
//  1. exactly one milestone delivering the FEATURE itself names it;
//  2. none does, so the DISTINCT milestones delivering its REQUIREMENTS --
//     exactly one names it, several give the neutral "N milestones" badge;
//  3. none at all leaves the cell blank.
//
// A failed listing read outranks all three: every cell says what could not
// be read rather than answering a question nobody could answer.
//
// The requirement does not spell out "several milestones deliver the
// FEATURE itself". It takes the same neutral count badge as case 2's
// several: there is no single milestone to name, and naming one arbitrarily
// would be a worse answer than counting. That choice is stated in the PR
// description rather than left buried here.
func (idx capabilityMilestoneIndex) cell(featureID uuid.UUID, requirementIDs []uuid.UUID) pages.CapabilityMilestone {
	if idx.Unread {
		return pages.CapabilityMilestone{Kind: pages.CapabilityMilestoneUnread, Message: capabilityMilestoneUnreadMessage}
	}
	// A milestone delivering the feature itself decides the cell outright,
	// at whatever count -- the requirement's requirements are never consulted
	// once the feature itself is delivered. Falling through to them on the
	// several case would report a milestone count about something else.
	if delivering := distinctMilestones(idx.byEntity[featureID]); len(delivering) > 0 {
		return idx.cellOf(delivering)
	}
	return idx.cellOf(distinctMilestonesAcross(idx.byEntity, requirementIDs))
}

// distinctMilestonesAcross unions the delivering milestones of several
// entities, deduplicated by milestone id: two of one feature's requirements
// delivered by one milestone is still ONE milestone, and a count that read
// it as two would be a fact about the listing, not about delivery.
func distinctMilestonesAcross(byEntity map[uuid.UUID][]milestoneBadgeSource, entityIDs []uuid.UUID) []milestoneBadgeSource {
	var out []milestoneBadgeSource
	seen := map[uuid.UUID]bool{}
	for _, id := range entityIDs {
		for _, m := range byEntity[id] {
			if !seen[m.id] {
				seen[m.id] = true
				out = append(out, m)
			}
		}
	}
	return out
}

// distinctMilestones deduplicates one entity's delivering milestones by id,
// for the same reason distinctMilestonesAcross does.
func distinctMilestones(sources []milestoneBadgeSource) []milestoneBadgeSource {
	var out []milestoneBadgeSource
	seen := map[uuid.UUID]bool{}
	for _, m := range sources {
		if !seen[m.id] {
			seen[m.id] = true
			out = append(out, m)
		}
	}
	return out
}

// cellOf turns a set of delivering milestones into a cell: one names it,
// several count, none is blank. The name and status come from the listing;
// nothing here is re-derived from the other read.
func (idx capabilityMilestoneIndex) cellOf(sources []milestoneBadgeSource) pages.CapabilityMilestone {
	switch len(sources) {
	case 0:
		return pages.CapabilityMilestone{Kind: pages.CapabilityMilestoneNone}
	case 1:
		return pages.CapabilityMilestone{
			Kind:   pages.CapabilityMilestoneNamed,
			Name:   sources[0].name,
			Status: sources[0].status,
			Count:  1,
		}
	default:
		return pages.CapabilityMilestone{Kind: pages.CapabilityMilestoneSeveral, Count: len(sources)}
	}
}

// capabilityExpansionQueryParam names the one feature-set section the
// capability map shows open. It is the Capabilities tab's own equivalent of
// the milestones page's expand parameter (milestones_page.go).
const capabilityExpansionQueryParam = "open"

// capabilityExpansionNone is the parameter's explicit "no section is open"
// value. It exists because the page's DEFAULT is the first section open,
// so simply dropping the parameter would re-open that section rather than
// close anything -- a Collapse control that rendered the state it was
// pressed from is worse than no control at all.
const capabilityExpansionNone = "none"

// capabilityExpansion is the capability map's section-expansion state: the
// page's own path, and the set of feature sets this request shows open.
//
// It is one value rather than two arguments for the same reason
// milestoneExpansion is: path and expansion travel together into every
// expander href, and a caller able to pass one from a different request
// would build a link that silently drops the other.
//
// It is a SET rather than one id because two sections can be open at once:
// comparing one section against another is the reason to open a second, and
// an expander that closed the first to open the second would make that
// comparison impossible. It follows the milestones page in spelling --
// every expand/collapse href re-states the whole set -- so an expander can
// never drop a sibling the operator had open.
//
// The ZERO value is the default state (first section open), which is what
// a caller that never mentions expansion gets: the default is the ordinary
// case, so it should not need spelling out.
type capabilityExpansion struct {
	// Path is the Capabilities tab's own URL, the base every expander href
	// is built on -- so a link can never point at another tab than the one
	// the operator is on.
	Path string

	// Open is the set of feature-set ids shown open. It is nil in the zero
	// value, which is why withFirstOpen writes through a fresh map.
	Open map[uuid.UUID]bool

	// Closed is the URL's explicit capabilityExpansionNone: the operator
	// collapsed whatever was open and wants it to STAY collapsed. It is
	// separate from an empty Open because that value also means "the URL
	// said nothing", which is the default.
	Closed bool
}

// parseCapabilityExpansion reads the open sections off the request.
//
// An absent, empty or unparseable value is the DEFAULT rather than an
// error: a hand-edited or stale open value is a reason to show the ordinary
// page, never a 400 and never a page whose every section is collapsed.
// capabilityExpansionNone is the one value read as an instruction to keep
// everything shut.
func parseCapabilityExpansion(r *http.Request) capabilityExpansion {
	e := capabilityExpansion{Path: r.URL.Path}
	raw := r.URL.Query().Get(capabilityExpansionQueryParam)
	if raw == capabilityExpansionNone {
		e.Closed = true
		return e
	}
	e.Open = map[uuid.UUID]bool{}
	for _, part := range strings.Split(raw, ",") {
		if id, err := uuid.Parse(strings.TrimSpace(part)); err == nil {
			e.Open[id] = true
		}
	}
	// A value that parsed to nothing usable stays the DEFAULT, not "nothing
	// open": "?open=not-a-uuid" is a typo, and answering it with a fully
	// collapsed map would look like a broken page.
	return e
}

// isExpanded reports whether this section is open.
//
// The first section is open when the URL said nothing (FR 18afc5a8). That
// default is deliberately NOT "nothing is open": a capability map with every
// section collapsed is an operator's first click, not a view of the
// product. A URL that named sections is honoured exactly -- an id matching
// no section here means a link shared from another product, and silently
// substituting a section would show them a capability map they did not ask
// for.
func (e capabilityExpansion) isExpanded(id uuid.UUID) bool {
	if e.Closed {
		return false
	}
	return e.Open[id]
}

// withFirstOpen is this expansion with the page's first feature set open,
// which is what the default state renders from. It is applied by the
// builder, where the feature sets are known -- parseCapabilityExpansion
// cannot resolve "the first" without them.
func (e capabilityExpansion) withFirstOpen(first uuid.UUID) capabilityExpansion {
	if e.Closed || len(e.Open) > 0 || first == uuid.Nil {
		return e
	}
	// A fresh map, never the receiver's: withFirstOpen must not mutate an
	// expansion some other builder call is still reading.
	open := map[uuid.UUID]bool{first: true}
	e.Open = open
	return e
}

// expandHref is the tab's URL with this section open alongside whatever is
// already open; collapseHref is the same URL with this section shut. Both
// are spelled out on every section rather than assumed, so the rendered
// state and the link an operator presses cannot disagree.
func (e capabilityExpansion) expandHref(id uuid.UUID) string {
	return e.href(withID(e.Open, id, true))
}

func (e capabilityExpansion) collapseHref(id uuid.UUID) string {
	return e.href(withID(e.Open, id, false))
}

// withID copies open with id set to present, leaving the receiver
// untouched: the href for one section must not disturb the state the other
// sections' hrefs are built from.
func withID(open map[uuid.UUID]bool, id uuid.UUID, present bool) map[uuid.UUID]bool {
	out := make(map[uuid.UUID]bool, len(open)+1)
	for k, v := range open {
		out[k] = v
	}
	out[id] = present
	return out
}

// href assembles the tab's own URL with the open set, in a STABLE order
// (sorted by id) so the same set always yields the same address -- an
// expander's URL that reordered itself between renders would push a
// different history entry for the same state.
//
// An empty set spells out capabilityExpansionNone rather than dropping the
// parameter, because the bare path is the DEFAULT (first section open); see
// capabilityExpansionNone.
func (e capabilityExpansion) href(open map[uuid.UUID]bool) string {
	ids := make([]string, 0, len(open))
	for id, present := range open {
		if present {
			ids = append(ids, id.String())
		}
	}
	if len(ids) == 0 {
		return e.Path + "?" + capabilityExpansionQueryParam + "=" + capabilityExpansionNone
	}
	sort.Strings(ids)
	return e.Path + "?" + capabilityExpansionQueryParam + "=" + strings.Join(ids, ",")
}

// -- load-bearing decisions -----------------------------------------------

// handleSpecDecisions lists a product's current LoadBearingDecisions with
// their full body text -- get_product_slice's decisions, unchanged (FR
// 6aa70e3a).
func (app *App) handleSpecDecisions(w http.ResponseWriter, r *http.Request) {
	productID, ok := app.specProductID(w, r)
	if !ok {
		return
	}

	doc, err := app.spec.ProductSlice(r.Context(), productID)
	if err != nil {
		app.renderSpecError(w, r, pages.DecisionsAnchor, err)
		return
	}

	app.renderSpecTabPage(w, r, "Decisions", productID, pages.Decisions(decisionsPageOf(doc, productID)))
}

// decisionsPageOf assembles the decisions list, copying each decision's
// full body and LBn exactly as get_product_slice returns them. Pure, so the
// full-body / field-parity contract is unit-testable without a database.
func decisionsPageOf(doc slice.Document, productID uuid.UUID) pages.DecisionsPage {
	page := pages.DecisionsPage{
		Product: productHeaderOfEntity(doc.Product, productID),
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
	productID, ok := app.specProductID(w, r)
	if !ok {
		return
	}

	product, err := app.spec.Product(r.Context(), productID)
	if err != nil {
		app.renderSpecError(w, r, pages.PersonasAnchor, err)
		return
	}
	personas, err := app.spec.Personas(r.Context(), productID)
	if err != nil {
		app.renderSpecError(w, r, pages.PersonasAnchor, err)
		return
	}

	app.renderSpecTabPage(w, r, "Personas", productID, pages.Personas(personasPageOf(product, personas, productID)))
}

// personasPageOf assembles the personas list, copying every field
// list_personas returns (id, name, description). Pure, so field-parity is
// unit-testable without a database.
func personasPageOf(product store.Product, personas []store.Persona, productID uuid.UUID) pages.PersonasPage {
	page := pages.PersonasPage{
		Product: productHeaderOf(product),
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
	productID, ok := app.specProductID(w, r)
	if !ok {
		return
	}

	product, err := app.spec.Product(r.Context(), productID)
	if err != nil {
		app.renderSpecError(w, r, pages.NonGoalsAnchor, err)
		return
	}
	nonGoals, err := app.spec.NonGoals(r.Context(), productID)
	if err != nil {
		app.renderSpecError(w, r, pages.NonGoalsAnchor, err)
		return
	}

	app.renderSpecTabPage(w, r, "Non-goals", productID, pages.NonGoals(nonGoalsPageOf(product, nonGoals, productID)))
}

// nonGoalsPageOf assembles the non-goals list, copying every field
// list_non_goals returns (id, kind, name, body) and bucketing by kind so
// the permanent vs deferred distinction is explicit. Pure, so field-parity
// is unit-testable without a database.
func nonGoalsPageOf(product store.Product, nonGoals []store.NonGoal, productID uuid.UUID) pages.NonGoalsPage {
	page := pages.NonGoalsPage{
		Product: productHeaderOf(product),
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
