// The spec browser: one tabbed page per product (capabilities, decisions, personas,
// non-goals), each tab its own URL. Reads go through the same readers as the MCP
// tools, current revisions only, so a page and its tool always agree.
package main

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/render"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
)

// specProductsPath lists products; specProductPath is every per-product page's prefix.
const (
	specProductsPath = specPath + "/products"
	specProductPath  = specPath + "/products/{id}"

	// Tab suffixes, spelled once so routes, path builders and specTabOf agree.
	decisionsSuffix = "/decisions"
	personasSuffix  = "/personas"
	nonGoalsSuffix  = "/non-goals"

	// specFeatureSuffix is the quick-look blade's suffix under a product.
	specFeatureSuffix = "/features/{fid}"
)

// specProductID parses {id} as a product id, rendering a 400 when it is not a UUID.
// A valid id becomes the last-viewed product; ids outside scope are discarded on read.
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

// renderSpecError maps a spec read error to an in-shell page: ErrNotFound is a 404,
// anything else is logged and a 500.
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
	// htmx does not swap a 500, so Refresh gets a 200 inline error instead.
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

// renderSpecStatus renders the spec error body through the shell with an explicit
// status; templ components cannot set status themselves.
func (app *App) renderSpecStatus(w http.ResponseWriter, r *http.Request, status int, page pages.StatusPage) {
	app.renderShellStatus(w, r, "Spec", r.URL.Path, pages.SpecStatus(page), status)
}

// renderSpecPage serves an htmx request the bare content region and a browser
// the same component inside the shell.
func (app *App) renderSpecPage(w http.ResponseWriter, r *http.Request, title string, body templ.Component) {
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, body)
		return
	}
	app.renderShell(w, r, title, r.URL.Path, body)
}

// renderSpecTabPage serves three modes, told apart by HX-Target: a tab swap gets
// strip and panel, a panel Refresh gets just the panel, a browser gets the full
// page. blade rides inside the swap region since a tab click replaces both.
func (app *App) renderSpecTabPage(w http.ResponseWriter, r *http.Request, title string, productID uuid.UUID, panel templ.Component, blade templ.Component) {
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
		Blade: blade,
	})
	if htmx {
		renderFragment(w, r, body)
		return
	}
	// An unreadable product list costs only the heading's name.
	productName := ""
	if products, err := app.scopeProducts(r.Context()); err == nil {
		for _, p := range products {
			if p.ID == productID {
				productName = p.Name
			}
		}
	}
	app.renderShell(w, r, title, r.URL.Path, pages.SpecPage(productName, body))
}

// specTabSwapRequested reports whether the htmx target is the tab swap region.
func specTabSwapRequested(r *http.Request) bool {
	return hxTargetID(r) == pages.SpecPanelAnchor
}

// specTabOf resolves the tab from the path; anything unrecognised is Capabilities.
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

// specTabsOf builds the tab strip with the request's tab marked active.
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

func productHeaderOf(p store.Product) pages.ProductHeader {
	return pages.ProductHeader{Name: p.Name, Vision: p.Vision, Href: productPath(p.ID)}
}

// productHeaderOfEntity builds the banner from a slice document's Product, avoiding
// a second read; a nil Product yields a name-less banner.
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

// Per-product spec page paths; deliveryPath is the delivery/roadmap view.
func decisionsPath(id uuid.UUID) string { return productPath(id) + decisionsSuffix }
func personasPath(id uuid.UUID) string  { return productPath(id) + personasSuffix }
func nonGoalsPath(id uuid.UUID) string  { return productPath(id) + nonGoalsSuffix }
func deliveryPath(id uuid.UUID) string  { return productPath(id) + "/delivery" }

// featureBladePath is a feature's quick-look URL, carrying the expansion state so
// Close returns to the section the operator came from.
func featureBladePath(productID, featureID uuid.UUID, section capabilityExpansion) string {
	return productPath(productID) + "/features/" + featureID.String() + section.query()
}

// productNavFor builds the per-product cross-links for the delivery page; the spec
// area itself uses the tab strip.
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

// handleSpecProducts lists the current products in the deployment's sole scope.
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

// handleCapabilityMap renders a product's current FeatureSets, Features and
// Requirements nested by parent, in get_product_slice's shape.
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

	// The Milestone column uses the same delivery read as the Milestones table; a
	// failed read costs only that column.
	listing, deliveryErr := app.capabilityMilestoneDelivery(r, productID)

	app.renderSpecTabPage(w, r, "Capability map", productID,
		pages.CapabilityMap(capabilityPageWithMilestonesOf(doc, productID, parseCapabilityExpansion(r), listing, deliveryErr)),
		pages.SpecBladeSlot(pages.SpecBladePage{}))
}

// capabilityMilestoneDelivery reads the listing for the Milestone column. Failure
// logs at WARNING because the page still renders.
func (app *App) capabilityMilestoneDelivery(r *http.Request, productID uuid.UUID) (slice.DeliveryListing, error) {
	listing, err := app.spec.Delivery(r.Context(), productID, nil)
	if err != nil {
		logger.Warn("spec milestone delivery read failed; the Milestone column cannot be read",
			"product", productID.String(), "error", err)
		return slice.DeliveryListing{}, err
	}
	return listing, nil
}

// capabilityPageOf is capabilityPageWithMilestonesOf with no delivery data, for the
// get_product_slice parity tests.
func capabilityPageOf(doc slice.Document, productID uuid.UUID, section capabilityExpansion) pages.CapabilityPage {
	return capabilityPageWithMilestonesOf(doc, productID, section, slice.DeliveryListing{}, nil)
}

// capabilityPageWithMilestonesOf builds the capability map plus the Milestone
// column from the delivery listing. A non-nil deliveryErr costs only that column.
func capabilityPageWithMilestonesOf(doc slice.Document, productID uuid.UUID, section capabilityExpansion, listing slice.DeliveryListing, deliveryErr error) pages.CapabilityPage {
	page := pages.CapabilityPage{
		Product: productHeaderOfEntity(doc.Product, productID),
		Path:    productPath(productID),
	}

	milestones := capabilityMilestoneIndexOf(listing, deliveryErr)

	// Resolve the default section before building features, since each quick-look href
	// carries the expansion and Close must return to the same section.
	section = resolvedCapabilitySection(doc, section)

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
			QuickLookHref:    featureBladePath(productID, f.ID, section),
		})
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

// -- the feature quick-look blade -----------------------------------------

// handleSpecFeature renders one feature's quick-look blade. htmx gets only the
// blade region so the page under it is untouched; a browser gets the whole Spec
// page with the blade open, so reloads and shared links match.
func (app *App) handleSpecFeature(w http.ResponseWriter, r *http.Request) {
	productID, ok := app.specProductID(w, r)
	if !ok {
		return
	}

	featureID, err := uuid.Parse(r.PathValue("fid"))
	if err != nil {
		app.renderSpecStatus(w, r, http.StatusBadRequest, pages.StatusPage{
			Title:    "Bad feature id",
			Detail:   "The feature id in the URL is not a UUID.",
			BackHref: productPath(productID),
			BackText: "Back to the capability map",
		})
		return
	}

	doc, err := app.spec.ProductSlice(r.Context(), productID)
	if err != nil {
		app.renderSpecError(w, r, pages.CapabilityMapAnchor, err)
		return
	}

	section := parseCapabilityExpansion(r)
	listing, deliveryErr := app.capabilityMilestoneDelivery(r, productID)

	blade, found := specBladePageOf(doc, productID, featureID, section, listing, deliveryErr)
	if !found {
		app.renderSpecStatus(w, r, http.StatusNotFound, pages.StatusPage{
			Title:    "Not found",
			Detail:   "No current feature of this product matches that id.",
			BackHref: productPath(productID) + section.query(),
			BackText: "Back to the capability map",
		})
		return
	}

	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, pages.SpecBladeSlot(blade))
		return
	}
	app.renderSpecTabPage(w, r, "Capability map", productID,
		pages.CapabilityMap(capabilityPageWithMilestonesOf(doc, productID, section, listing, deliveryErr)),
		pages.SpecBladeSlot(blade))
}

// specBladePageOf builds one feature's quick look, or reports the id names no
// current feature. Milestone and FRn/NFRn citations use the same derivations as
// the Capabilities table and rendered PRODUCT.md.
func specBladePageOf(doc slice.Document, productID, featureID uuid.UUID, section capabilityExpansion, listing slice.DeliveryListing, deliveryErr error) (pages.SpecBladePage, bool) {
	section = resolvedCapabilitySection(doc, section)

	var feature *slice.FeatureEntity
	for i := range doc.Features {
		if doc.Features[i].ID == featureID {
			feature = &doc.Features[i]
			break
		}
	}
	if feature == nil {
		return pages.SpecBladePage{}, false
	}

	citations := render.RequirementCitations(doc)
	blade := pages.SpecBladeFeature{
		ID:          feature.ID.String(),
		Number:      feature.DisplayNumber,
		Name:        feature.Name,
		Description: deref(feature.Description),
		Milestone:   capabilityMilestoneIndexOf(listing, deliveryErr).cell(featureID, requirementIDsOf(doc, featureID)),
	}

	// Slice order is the order RequirementCitations numbered them in; an unnumbered
	// requirement shows its kind alone.
	for _, rq := range doc.Requirements {
		if rq.FeatureID != featureID {
			continue
		}
		citation := citations[rq.ID]
		if citation == "" {
			citation = rq.Kind
		}
		blade.Requirements = append(blade.Requirements, pages.SpecBladeRequirement{
			ID:       rq.ID.String(),
			Citation: citation,
			Name:     rq.Name,
		})
	}

	return pages.SpecBladePage{
		Product:   productHeaderOfEntity(doc.Product, productID),
		Feature:   &blade,
		CloseHref: productPath(productID) + section.query(),
	}, true
}

// requirementIDsOf is one feature's requirement ids in slice order.
func requirementIDsOf(doc slice.Document, featureID uuid.UUID) []uuid.UUID {
	var ids []uuid.UUID
	for _, rq := range doc.Requirements {
		if rq.FeatureID == featureID {
			ids = append(ids, rq.ID)
		}
	}
	return ids
}

// -- the Milestone column ------------------------------------------------

// capabilityMilestoneUnreadMessage fills every Milestone cell when the listing
// could not be read; a blank cell would wrongly claim nothing delivers the feature.
const capabilityMilestoneUnreadMessage = "Milestone delivery could not be read. See the logs."

// milestoneBadgeSource is one milestone as the Milestone column needs it; the id
// lets cells count distinct milestones.
type milestoneBadgeSource struct {
	id     uuid.UUID
	name   string
	status string
}

// capabilityMilestoneIndex is the delivery listing indexed by delivered entity id.
type capabilityMilestoneIndex struct {
	byEntity map[uuid.UUID][]milestoneBadgeSource

	Unread bool
}

// capabilityMilestoneIndexOf indexes top-level milestones only: a milepebble
// delivers a subset of its parent, so counting it would inflate the count.
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

// cell derives a feature's Milestone cell: milestones delivering the feature
// itself decide it; otherwise the distinct milestones delivering its requirements;
// none is blank. A failed read outranks all three.
func (idx capabilityMilestoneIndex) cell(featureID uuid.UUID, requirementIDs []uuid.UUID) pages.CapabilityMilestone {
	if idx.Unread {
		return pages.CapabilityMilestone{Kind: pages.CapabilityMilestoneUnread, Message: capabilityMilestoneUnreadMessage}
	}
	if delivering := distinctMilestones(idx.byEntity[featureID]); len(delivering) > 0 {
		return idx.cellOf(delivering)
	}
	return idx.cellOf(distinctMilestonesAcross(idx.byEntity, requirementIDs))
}

// distinctMilestonesAcross unions several entities' delivering milestones, deduped
// by milestone id.
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

// cellOf turns delivering milestones into a cell: one names it, several count,
// none is blank.
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

// capabilityExpansionQueryParam names the open feature-set sections.
const capabilityExpansionQueryParam = "open"

// capabilityExpansionNone means "nothing open"; dropping the parameter would
// re-open the default first section instead.
const capabilityExpansionNone = "none"

// capabilityExpansion is the capability map's expansion state: the tab path and
// the set of open sections. Every href restates the whole set so no sibling drops.
// The zero value is the default (first section open).
type capabilityExpansion struct {
	// Path is the Capabilities tab's URL, the base of every expander href.
	Path string

	// Open is nil in the zero value, so withFirstOpen writes a fresh map.
	Open map[uuid.UUID]bool

	// Closed is the explicit "none" value, distinct from an absent parameter.
	Closed bool
}

// parseCapabilityExpansion reads the open sections. Absent or unparseable values
// are the default, never a 400; only capabilityExpansionNone keeps everything shut.
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
	// A value that parsed to nothing stays the default rather than "nothing open".
	return e
}

// isExpanded reports whether this section is open. Named ids are honoured exactly;
// one matching no section is not swapped for another.
func (e capabilityExpansion) isExpanded(id uuid.UUID) bool {
	if e.Closed {
		return false
	}
	return e.Open[id]
}

// withFirstOpen applies the default first-open section; it needs the feature sets,
// so the builder applies it rather than the parser.
func (e capabilityExpansion) withFirstOpen(first uuid.UUID) capabilityExpansion {
	if e.Closed || len(e.Open) > 0 || first == uuid.Nil {
		return e
	}
	// A fresh map: other builder calls may still read the receiver's.
	open := map[uuid.UUID]bool{first: true}
	e.Open = open
	return e
}

// resolvedCapabilitySection applies the default to an expansion. The map and the
// blade both use it so a blade's Close spells the default, not "nothing open".
func resolvedCapabilitySection(doc slice.Document, section capabilityExpansion) capabilityExpansion {
	if len(doc.FeatureSets) == 0 {
		return section
	}
	return section.withFirstOpen(doc.FeatureSets[0].ID)
}

// expandHref and collapseHref are the tab URL with this section opened or shut,
// keeping every other open section.
func (e capabilityExpansion) expandHref(id uuid.UUID) string {
	return e.href(withID(e.Open, id, true))
}

func (e capabilityExpansion) collapseHref(id uuid.UUID) string {
	return e.href(withID(e.Open, id, false))
}

// withID copies open with id set, leaving the receiver untouched.
func withID(open map[uuid.UUID]bool, id uuid.UUID, present bool) map[uuid.UUID]bool {
	out := make(map[uuid.UUID]bool, len(open)+1)
	for k, v := range open {
		out[k] = v
	}
	out[id] = present
	return out
}

// href is the tab URL with the open set, sorted so one state is one address.
func (e capabilityExpansion) href(open map[uuid.UUID]bool) string {
	return e.Path + expansionSuffix(open)
}

// query is the "?open=..." suffix for the blade's URL and Close, including the
// explicit "none" so Close does not reopen the default section.
func (e capabilityExpansion) query() string {
	return expansionSuffix(e.Open)
}

// expansionSuffix spells an open set as the parameter value; empty is "none".
func expansionSuffix(open map[uuid.UUID]bool) string {
	ids := make([]string, 0, len(open))
	for id, present := range open {
		if present {
			ids = append(ids, id.String())
		}
	}
	if len(ids) == 0 {
		return "?" + capabilityExpansionQueryParam + "=" + capabilityExpansionNone
	}
	sort.Strings(ids)
	return "?" + capabilityExpansionQueryParam + "=" + strings.Join(ids, ",")
}

// -- load-bearing decisions -----------------------------------------------

// handleSpecDecisions lists a product's current load-bearing decisions with full
// bodies.
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

	app.renderSpecTabPage(w, r, "Decisions", productID, pages.Decisions(decisionsPageOf(doc, productID)),
		pages.SpecBladeSlot(pages.SpecBladePage{}))
}

// decisionsPageOf copies each decision's body and LBn as get_product_slice returns them.
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

// handleSpecPersonas lists a product's current personas.
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

	app.renderSpecTabPage(w, r, "Personas", productID, pages.Personas(personasPageOf(product, personas, productID)),
		pages.SpecBladeSlot(pages.SpecBladePage{}))
}

// personasPageOf copies every field list_personas returns.
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

// nonGoalHeadings are the kinds' labels in page order.
var nonGoalHeadings = []struct{ kind, heading string }{
	{string(store.NonGoalKindPermanent), "Permanent non-goals"},
	{string(store.NonGoalKindDeferred), "Deferred, not foreclosed"},
}

// handleSpecNonGoals lists a product's current permanent and deferred non-goals.
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

	app.renderSpecTabPage(w, r, "Non-goals", productID, pages.NonGoals(nonGoalsPageOf(product, nonGoals, productID)),
		pages.SpecBladeSlot(pages.SpecBladePage{}))
}

// nonGoalsPageOf copies every field list_non_goals returns, bucketed by kind.
func nonGoalsPageOf(product store.Product, nonGoals []store.NonGoal, productID uuid.UUID) pages.NonGoalsPage {
	page := pages.NonGoalsPage{
		Product: productHeaderOf(product),
		Path:    nonGoalsPath(productID),
	}
	// Only non-empty kinds become a group, so the empty state renders when both are empty.
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

// deref unwraps an optional text field, treating nil as empty.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
