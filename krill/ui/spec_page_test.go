package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// specStubReader is an in-memory specReadClient whose every read succeeds
// with empty data, so a handler renders its empty state. The one-route-
// two-modes test below is about WHICH body a request is served, not about
// what is in it; the populated paths are covered against the pure
// builders instead.
type specStubReader struct{}

func (specStubReader) ProductSlice(context.Context, uuid.UUID) (slice.Document, error) {
	return slice.Document{}, nil
}

func (specStubReader) Personas(context.Context, uuid.UUID) ([]store.Persona, error) {
	return nil, nil
}

func (specStubReader) NonGoals(context.Context, uuid.UUID) ([]store.NonGoal, error) {
	return nil, nil
}

func (specStubReader) Delivery(context.Context, uuid.UUID, []store.MilestoneStatus) (slice.DeliveryListing, error) {
	return slice.DeliveryListing{}, nil
}

func (specStubReader) DeliveryBreakdown(context.Context, uuid.UUID) (slice.Document, slice.Document, error) {
	return slice.Document{}, slice.Document{}, nil
}

func (specStubReader) Product(context.Context, uuid.UUID) (store.Product, error) {
	return store.Product{}, nil
}

// StatusHistory answers with no transitions: this stub's subject is which
// body a route serves, never a container's status register.
func (specStubReader) StatusHistory(context.Context, uuid.UUID) ([]store.MilestoneStatusEvent, error) {
	return nil, nil
}

func (specStubReader) Products(context.Context) ([]store.Product, error) { return nil, nil }

// specModeMux mounts the four per-product spec handlers against the stub
// reader, at the paths routes.go really registers.
func specModeMux() *http.ServeMux {
	// scopes and tasks are what the chrome reads for its Needs-attention
	// badge on every page it renders.
	app := &App{spec: specStubReader{}, scopes: chromeScopes{}, tasks: chromeTaskCounter{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+specProductPath, app.handleCapabilityMap)
	mux.HandleFunc("GET "+specProductPath+"/decisions", app.handleSpecDecisions)
	mux.HandleFunc("GET "+specProductPath+"/personas", app.handleSpecPersonas)
	mux.HandleFunc("GET "+specProductPath+"/non-goals", app.handleSpecNonGoals)
	mux.HandleFunc("GET "+specProductsPath, app.handleSpecProducts)
	return mux
}

// TestSpecLandingLinksToProductBrowser pins the spec area root: it is a
// static landing that links into the store-backed product index, so an
// operator has a path from the nav to a product's pages without the
// landing itself needing a database.
func TestSpecLandingLinksToProductBrowser(t *testing.T) {
	body := fetch(t, newTestMux(t), specPath).Body.String()

	if !strings.Contains(body, `href="`+specProductsPath+`"`) {
		t.Errorf("GET %s does not link to the product browser %s", specPath, specProductsPath)
	}
}

// TestSpecRoutesDoNotCollide registers the full shell -- including the new
// spec sub-routes -- on the same mux as the self-serve credential API,
// the same boot-time collision guard TestShellRoutesDoNotCollideWithSelfServe
// applies. A spec page that collided with a wildcard route would panic the
// binary at startup, and this exercises the real registrations rather than
// a copy.
func TestSpecRoutesDoNotCollide(t *testing.T) {
	mux := http.NewServeMux()
	app := newTestApp(t)
	mountSelfServeStubs(mux)
	app.mountShellRoutes(mux)

	// Registering above would already have panicked on a collision. Confirm
	// the spec landing and the self-serve API each still answer on their own
	// paths.
	if rec := fetch(t, mux, specPath); rec.Code != http.StatusOK {
		t.Errorf("GET %s = %d, want 200", specPath, rec.Code)
	}
	if rec := fetch(t, mux, "GET /credentials"); rec.Code != http.StatusOK {
		t.Errorf("self-serve API GET /credentials = %d, want 200", rec.Code)
	}
}

// TestSpecBadProductIDIsBadRequest checks the {id} path-value guard: a
// non-UUID product id is rejected with a shell-rendered 400 before any
// store read, so the page never shows a bare http.Error string.
//
// The retired /delivery URL is absent from this list deliberately. It no
// longer has a handler with a path-value guard -- it is a Successor, and
// serveLegacy's contract is that an un- prefixed URL always LANDS somewhere,
// so a successor that cannot resolve a product renders the product index
// rather than refusing. See TestLegacyDeliveryRedirectsToMilestones for the
// redirect itself.
func TestSpecBadProductIDIsBadRequest(t *testing.T) {
	mux := newTestMux(t)

	for _, path := range []string{
		specProductPath,
		specProductPath + "/decisions",
		specProductPath + "/personas",
		specProductPath + "/non-goals",
	} {
		target := strings.Replace(path, "{id}", "not-a-uuid", 1)
		rec := fetch(t, mux, target)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400 (non-UUID product id)", target, rec.Code)
		}
		body := rec.Body.String()
		// The shell chrome still renders, so the rejection is a page.
		for _, want := range []string{"<html", "<nav", "</html>"} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s missing %q from the shell chrome", target, want)
			}
		}
	}
}

// The tests below pin field-parity between each view and the MCP tool it
// mirrors: the same fields get_product_slice / list_personas / list_non_goals
// return must appear on the rendered page. They exercise the pure view-model
// builders + templates directly, so the contract is covered without a live
// Postgres.

func mustID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("uuid.Parse(%q): %v", s, err)
	}
	return id
}

// TestCapabilityMapFieldParity pins that the capability map renders every
// field get_product_slice returns for a FeatureSet, Feature, and Requirement:
// the surrogate id, name, description, the Feature's Cn, and the
// Requirement's kind + body. FR 638a7e5f.
func TestCapabilityMapFieldParity(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	fsID := mustID(t, "22222222-2222-2222-2222-222222222222")
	featID := mustID(t, "33333333-3333-3333-3333-333333333333")
	frID := mustID(t, "44444444-4444-4444-4444-444444444444")
	nfrID := mustID(t, "55555555-5555-5555-5555-555555555555")

	doc := slice.Document{
		Product: &slice.ProductEntity{Name: "krill", Vision: "the substrate"},
		FeatureSets: []slice.FeatureSetEntity{{
			EntityRef:   slice.EntityRef{ID: fsID},
			Name:        "Spec axis",
			Description: ptr("feature sets about the spec"),
		}},
		Features: []slice.FeatureEntity{{
			EntityRef:     slice.EntityRef{ID: featID},
			FeatureSetID:  fsID,
			Name:          "Scoped slice",
			Description:   ptr("one query, four granularities"),
			DisplayNumber: 7,
		}},
		Requirements: []slice.RequirementEntity{
			{EntityRef: slice.EntityRef{ID: frID}, FeatureID: featID, Kind: "FR", Name: "product granularity", Body: ptr("returns every child")},
			{EntityRef: slice.EntityRef{ID: nfrID}, FeatureID: featID, Kind: "NFR", Name: "stays cheap", Body: nil},
		},
	}

	body := mustRenderComponent(pages.CapabilityMap(capabilityPageOf(doc, productID, capabilityExpansion{Path: productPath(productID)})))
	got := body
	for _, want := range []string{
		"krill", "the substrate", // product name + vision
		"Spec axis", "feature sets about the spec", fsID.String(), // featureset fields
		"C7", "Scoped slice", "one query, four granularities", featID.String(), // feature fields
		"FR", "product granularity", "returns every child", frID.String(), // FR fields
		"NFR", "stays cheap", nfrID.String(), // NFR fields
	} {
		if !strings.Contains(got, want) {
			t.Errorf("capability map missing %q", want)
		}
	}
}

// TestDecisionsRenderFullBodyAndFields pins FR 6aa70e3a: a decision renders
// its name, its LBn, its surrogate id, and its complete body -- not a
// truncated one and not a link-only reference.
func TestDecisionsRenderFullBodyAndFields(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	dID := mustID(t, "66666666-6666-6666-6666-666666666666")
	fullBody := "LB1 is load-bearing because the whole write gate depends on it. " +
		strings.Repeat("extra detail. ", 40)

	doc := slice.Document{
		Product:   &slice.ProductEntity{Name: "krill"},
		Decisions: []slice.DecisionEntity{{EntityRef: slice.EntityRef{ID: dID}, Name: "One document type", Body: ptr(fullBody), DisplayNumber: 1}},
	}

	body := mustRenderComponent(pages.Decisions(decisionsPageOf(doc, productID)))
	// fullBody's trailing space is checked separately with TrimSpace: Body is
	// markdown-rendered (renderMarkdown), and CommonMark trims a paragraph's
	// trailing whitespace -- invisible in the rendered HTML, but it would
	// otherwise make this exact-substring check fail on a cosmetic difference
	// that isn't the "not truncated" regression this test exists to catch.
	for _, want := range []string{"One document type", "LB1", dID.String(), strings.TrimSpace(fullBody)} {
		if !strings.Contains(body, want) {
			t.Errorf("decisions page missing %q", want)
		}
	}
}

// TestPersonasFieldParity pins that each persona renders every field
// list_personas returns: id, name, description. FR b4c1c77f.
func TestPersonasFieldParity(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	pID := mustID(t, "77777777-7777-7777-7777-777777777777")
	product := store.Product{ID: productID, Name: "krill", Vision: "the substrate"}
	personas := []store.Persona{{ID: pID, Name: "Operator", Description: ptr("runs the deployment")}}

	body := mustRenderComponent(pages.Personas(personasPageOf(product, personas, productID)))
	for _, want := range []string{"Operator", "runs the deployment", pID.String()} {
		if !strings.Contains(body, want) {
			t.Errorf("personas page missing %q", want)
		}
	}
}

// TestNonGoalsFieldParityAndKinds pins that non-goals render every field
// list_non_goals returns (id, kind, name, body) and that the permanent vs
// deferred kinds are visibly distinguished. FR b4c1c77f.
func TestNonGoalsFieldParityAndKinds(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	permID := mustID(t, "88888888-8888-8888-8888-888888888888")
	defID := mustID(t, "99999999-9999-9999-9999-999999999999")
	product := store.Product{ID: productID, Name: "krill"}
	nonGoals := []store.NonGoal{
		{ID: permID, Kind: store.NonGoalKindPermanent, Name: "No web editing", Body: ptr("write-only, by design")},
		{ID: defID, Kind: store.NonGoalKindDeferred, Name: "History in the UI", Body: nil},
	}

	body := mustRenderComponent(pages.NonGoals(nonGoalsPageOf(product, nonGoals, productID)))
	for _, want := range []string{
		"Permanent non-goals", "Deferred, not foreclosed", // kinds distinguished
		"No web editing", "write-only, by design", permID.String(),
		"History in the UI", defID.String(),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("non-goals page missing %q", want)
		}
	}
}

// The three non-Capabilities tabs' EMPTY state: each tab shows a designed
// EmptyState when its read succeeded with nothing in it, and shows the
// error path -- never the empty state -- when the read failed (FR
// fe0ebe94). The two are claims about different things: "this product has
// no decisions" is a fact about the product, and a store that could not be
// read is not entitled to make it.
//
// Both branches go through the real handlers, so this covers the wiring as
// well as the three components.

// failingListSpecReader fails the Personas and NonGoals reads while the
// product read SUCCEEDS.
//
// It exists because failingSpecReader (spec_capabilities_test.go) fails
// Product too, which is what the Personas/Non-goals handlers read FIRST --
// so against that reader their error path after the product read is never
// reached, and a handler that degraded a failed LIST read to an empty state
// would go unnoticed. Between the two readers both failure points on those
// two tabs are exercised.
type failingListSpecReader struct{ specStubReader }

func (failingListSpecReader) Personas(context.Context, uuid.UUID) ([]store.Persona, error) {
	return nil, errors.New("the spec store is unavailable")
}

func (failingListSpecReader) NonGoals(context.Context, uuid.UUID) ([]store.NonGoal, error) {
	return nil, errors.New("the spec store is unavailable")
}

// specTabFailureMux mounts the three non-Capabilities tab handlers at the
// paths routes.go really registers, against the reader every tab read
// fails.
func specTabFailureMux(reader specReadClient) *http.ServeMux {
	app := &App{spec: reader, scopes: chromeScopes{}, tasks: chromeTaskCounter{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+specProductPath+"/decisions", app.handleSpecDecisions)
	mux.HandleFunc("GET "+specProductPath+"/personas", app.handleSpecPersonas)
	mux.HandleFunc("GET "+specProductPath+"/non-goals", app.handleSpecNonGoals)
	return mux
}

// specTabEmptyStates pairs each of the three tabs with its own path and
// its own designed empty-state copy, so the assertion below cannot pass by
// finding one tab's empty state on another tab's page.
//
// readers are the failures each tab must survive: the Decisions tab reads
// the product slice, so the ProductSlice failure applies to it; the other
// two read the product first and then a list, so BOTH failures apply --
// the first one the handler hits and the one after it.
var specTabEmptyStates = []struct {
	suffix   string
	empty    string
	readers []specReadClient
}{
	{"/decisions", "No load-bearing decisions yet.", []specReadClient{failingSpecReader{}}},
	{"/personas", "No personas yet.", []specReadClient{failingSpecReader{}, failingListSpecReader{}}},
	{"/non-goals", "No non-goals yet.", []specReadClient{failingSpecReader{}, failingListSpecReader{}}},
}

func TestNonCapabilitiesTabsShowEmptyStateOnlyOnASuccessfulEmptyRead(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")

	for _, tab := range specTabEmptyStates {
		path := productPath(productID) + tab.suffix

		t.Run(tab.suffix+" renders the designed empty state on a successful empty read", func(t *testing.T) {
			rec := fetch(t, specModeMux(), path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", path, rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tab.empty) {
				t.Errorf("a read that succeeded and found nothing did not render %q", tab.empty)
			}
			// The designed empty state, not a bare sentence: htmxui's
			// EmptyState renders a card with its own description line.
			if !strings.Contains(body, "card bg-base-100") {
				t.Errorf("GET %s did not render the designed EmptyState component, only flat text", path)
			}
		})

		for i, reader := range tab.readers {
			t.Run(tab.suffix+" never renders the empty state on a failed read", func(t *testing.T) {
				rec := fetch(t, specTabFailureMux(reader), path)
				if rec.Code != http.StatusInternalServerError {
					t.Errorf("GET %s with failing reader %d = %d, want 500", path, i, rec.Code)
				}
				body := rec.Body.String()
				if strings.Contains(body, tab.empty) {
					t.Errorf("GET %s with failing reader %d rendered %q; that is a claim about the product nobody could make",
						path, i, tab.empty)
				}
				if !strings.Contains(body, "Could not load the spec") {
					t.Errorf("GET %s with failing reader %d did not render the alert path", path, i)
				}
			})
		}
	}
}

// TestNonCapabilitiesTabsCarryNoAttentionContent pins the read-only
// invariant over the three panels this task owns (FR df5bffd1's, applied
// to the whole page body). It renders each panel with real content --
// decisions, personas, and both kinds of non-goal -- because a panel with
// nothing in it cannot carry attention content whatever it renders.
func TestNonCapabilitiesTabsCarryNoAttentionContent(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	product := store.Product{ID: productID, Name: "krill"}

	panels := map[string]string{
		"decisions": mustRenderComponent(pages.Decisions(decisionsPageOf(slice.Document{
			Product: &slice.ProductEntity{Name: "krill"},
			Decisions: []slice.DecisionEntity{{
				EntityRef: slice.EntityRef{ID: mustID(t, "66666666-6666-6666-6666-666666666666")},
				Name:      "One document type", Body: ptr("one type, not two"),
			}},
		}, productID))),
		"personas": mustRenderComponent(pages.Personas(personasPageOf(product,
			[]store.Persona{{ID: mustID(t, "77777777-7777-7777-7777-777777777777"), Name: "Operator", Description: ptr("runs it")}},
			productID))),
		"non-goals": mustRenderComponent(pages.NonGoals(nonGoalsPageOf(product, []store.NonGoal{
			{ID: mustID(t, "88888888-8888-8888-8888-888888888888"), Kind: store.NonGoalKindPermanent, Name: "No web editing", Body: ptr("write-only")},
			{ID: mustID(t, "99999999-9999-9999-9999-999999999999"), Kind: store.NonGoalKindDeferred, Name: "History in the UI", Body: ptr("later")},
		}, productID))),
	}

	for tab, body := range panels {
		for _, forbidden := range []string{"<form", "hx-post", "hx-put", "hx-delete"} {
			if strings.Contains(strings.ToLower(body), forbidden) {
				t.Errorf("%s tab: the spec panel is read-only but carries %q", tab, forbidden)
			}
		}
		// The non-goal kind badge is the one NEW badge on these tabs, so
		// the attention-content check has to hold across it too: it is a
		// scope fact, not something needing anybody's action.
		for _, forbidden := range []string{"Needs attention", "escalated", "Escalate", "badge-error", "badge-warning"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s tab: the spec panel carries attention content (%q)", tab, forbidden)
			}
		}
	}
}

// subnavAnchor returns the <a> tag the product sub-nav emits for href, or
// "" when no such link is present. Callers then ask whether that one tag
// carries aria-current, rather than pattern-matching a href/attribute
// adjacency: components.SubNav puts its styling class between the two, and
// htmxui ARCHITECTURE §14 asks tests to assert the behavioural claim
// (which link is current) rather than the presentational markup.
//
// It now has one caller: the delivery page, the last area that carries a
// cross-nav. The spec area's cross-links are the tab strip's tabs, marked
// with aria-selected rather than aria-current.
func subnavAnchor(html, href string) string {
	idx := strings.Index(html, `href="`+href+`"`)
	if idx < 0 {
		return ""
	}
	rest := html[idx:]
	end := strings.Index(rest, ">")
	if end < 0 {
		return ""
	}
	return rest[:end+1]
}

// renderDeliveryCrossNavPage renders the delivery page purely for its
// cross-nav. Delivery is a sibling area with its own templ component, so
// this is the one place in this file that reaches outside the spec pages.
func renderDeliveryCrossNavPage(product store.Product, productID uuid.UUID) string {
	return mustRenderComponent(pages.Delivery(deliveryPageOf(product, slice.DeliveryListing{}, nil, productID)))
}

// TestSpecTabStripCrossLinks pins the promise the spec area's cross-nav
// used to make: an operator who lands on any of the four spec URLs can
// reach the other three without retyping a URL. The tab strip now carries
// it, one real href per tab, at the very paths the pages are served at.
func TestSpecTabStripCrossLinks(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	product := store.Product{ID: productID, Name: "krill"}
	doc := slice.Document{Product: &slice.ProductEntity{Name: "krill"}}

	panels := map[string]templ.Component{
		pages.SpecTabCapabilities: pages.CapabilityMap(capabilityPageOf(doc, productID, capabilityExpansion{Path: productPath(productID)})),
		pages.SpecTabDecisions:    pages.Decisions(decisionsPageOf(doc, productID)),
		pages.SpecTabPersonas:     pages.Personas(personasPageOf(product, nil, productID)),
		pages.SpecTabNonGoals:     pages.NonGoals(nonGoalsPageOf(product, nil, productID)),
	}
	links := []string{
		productPath(productID),
		decisionsPath(productID),
		personasPath(productID),
		nonGoalsPath(productID),
	}

	for tab, panel := range panels {
		body := mustRenderComponent(pages.SpecTabs(pages.SpecTabsPage{
			Tabs:  specTabsOf(productID, tab),
			Tab:   tab,
			Panel: panel,
		}))

		for _, href := range links {
			if subnavAnchor(body, href) == "" {
				t.Errorf("%s tab missing a link to %s", tab, href)
			}
		}
		// Exactly one tab is marked current.
		if got := strings.Count(body, `aria-selected="true"`); got != 1 {
			t.Errorf("%s tab marks %d tabs active, want 1", tab, got)
		}
	}

	// Delivery is not one of the four tabs: it is its own area, reached
	// from the sidebar, and the strip must not have inherited it.
	body := mustRenderComponent(pages.SpecTabs(pages.SpecTabsPage{
		Tabs:  specTabsOf(productID, pages.SpecTabCapabilities),
		Tab:   pages.SpecTabCapabilities,
		Panel: panels[pages.SpecTabCapabilities],
	}))
	if subnavAnchor(body, deliveryPath(productID)) != "" {
		t.Errorf("the spec tab strip carries a link to %s; delivery is its own area", deliveryPath(productID))
	}
}

// TestDeliveryKeepsItsCrossNav pins that removing the cross-nav from the
// spec area did not take it out of delivery, the one area that still has
// one.
func TestDeliveryKeepsItsCrossNav(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	body := renderDeliveryCrossNavPage(store.Product{ID: productID, Name: "krill"}, productID)

	links := []string{
		productPath(productID),
		decisionsPath(productID),
		personasPath(productID),
		nonGoalsPath(productID),
		deliveryPath(productID),
	}
	for _, href := range links {
		if subnavAnchor(body, href) == "" {
			t.Errorf("delivery page missing cross-link to %s", href)
		}
	}
	// Exactly one link is the current page.
	if got := strings.Count(body, `aria-current="page"`); got != 1 {
		t.Errorf("delivery page has %d active cross-links, want 1", got)
	}
	if !strings.Contains(subnavAnchor(body, deliveryPath(productID)), `aria-current="page"`) {
		t.Errorf("delivery page does not mark the Delivery cross-link as the current page")
	}
	for _, href := range links[:4] {
		if strings.Contains(subnavAnchor(body, href), `aria-current="page"`) {
			t.Errorf("delivery page wrongly marks %s as the current page", href)
		}
	}
}

// TestSpecPagesServeBothModesOffOneRoute pins the htmx branch each spec
// GET handler now carries: an HX-Request gets the page's own content
// region as a bare 200 fragment with no chrome, and the same route without
// that header gets the same component inside the shell.
func TestSpecPagesServeBothModesOffOneRoute(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	paths := map[string]string{
		productPath(productID):   `<section id="` + pages.CapabilityMapAnchor + `"`,
		decisionsPath(productID): `<section id="` + pages.DecisionsAnchor + `"`,
		personasPath(productID):  `<section id="` + pages.PersonasAnchor + `"`,
		nonGoalsPath(productID):  `<section id="` + pages.NonGoalsAnchor + `"`,
		specProductsPath:         `<section id="` + pages.ProductsAnchor + `"`,
	}

	for path, want := range paths {
		mux := specModeMux()

		// The fragment: 200, content region only, no document chrome.
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("HX-Request", "true")
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("GET %s (HX-Request) = %d, want 200", path, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, want) {
			t.Errorf("GET %s (HX-Request) did not return the page's own content region %q", path, want)
		}
		if body := rec.Body.String(); strings.Contains(body, "<html") {
			t.Errorf("GET %s (HX-Request) returned the document chrome; a fragment must be bare", path)
		}

		// The same route without the header still serves the full shell.
		full := fetch(t, specModeMux(), path)
		if full.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, full.Code)
		}
		if body := full.Body.String(); !strings.Contains(body, want) {
			t.Errorf("GET %s did not serve the same content region inside the shell", path)
		}
	}
}

// TestSpecPagesCarryAManualRefresh pins the refresh affordance: each spec
// page re-requests its own path, and none of them polls on a timer -- a
// timer would swap the spec out from under an operator mid-read.
func TestSpecPagesCarryAManualRefresh(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	product := store.Product{ID: productID, Name: "krill"}
	doc := slice.Document{Product: &slice.ProductEntity{Name: "krill"}}

	bodies := map[string]string{
		productPath(productID):   mustRenderComponent(pages.CapabilityMap(capabilityPageOf(doc, productID, capabilityExpansion{Path: productPath(productID)}))),
		decisionsPath(productID): mustRenderComponent(pages.Decisions(decisionsPageOf(doc, productID))),
		personasPath(productID):  mustRenderComponent(pages.Personas(personasPageOf(product, nil, productID))),
		nonGoalsPath(productID):  mustRenderComponent(pages.NonGoals(nonGoalsPageOf(product, nil, productID))),
	}

	for path, body := range bodies {
		if !strings.Contains(body, `hx-get="`+path+`"`) {
			t.Errorf("%s page has no refresh re-requesting its own path", path)
		}
		if !strings.Contains(body, `hx-swap="outerHTML"`) {
			t.Errorf("%s page refresh does not replace the content region", path)
		}
		// No timer: a poll would swap a spec out from under a reader.
		if strings.Contains(body, "hx-trigger") || strings.Contains(body, "every ") {
			t.Errorf("%s page refreshes on a timer; these pages are read, not watched", path)
		}
	}
}

// TestSpecTabStripIsRealNavigation pins that every tab is a REAL link to
// its own path and not only an htmx request: with JavaScript off, a tab
// has to be a plain navigation that still lands on the right tab.
func TestSpecTabStripIsRealNavigation(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	body := mustRenderComponent(pages.SpecTabs(pages.SpecTabsPage{
		Tabs:  specTabsOf(productID, pages.SpecTabDecisions),
		Tab:   pages.SpecTabDecisions,
		Panel: pages.Decisions(decisionsPageOf(slice.Document{
			Product: &slice.ProductEntity{Name: "krill"},
		}, productID)),
	}))

	idx := strings.Index(body, `data-krill="spec-tabs"`)
	if idx < 0 {
		t.Fatalf("the tab strip did not render; got: %s", body)
	}
	strip := body[idx:]
	if end := strings.Index(strip, `data-krill="spec-panel-body"`); end >= 0 {
		strip = strip[:end]
	}
	for _, href := range []string{
		productPath(productID),
		decisionsPath(productID),
		personasPath(productID),
		nonGoalsPath(productID),
	} {
		tag := subnavAnchor(strip, href)
		if tag == "" {
			t.Errorf("the tab strip has no tab linking to %s", href)
			continue
		}
		if !strings.Contains(tag, `hx-get="`+href+`"`) {
			t.Errorf("tab %s does not hx-get its own href %s", href, href)
		}
	}
}

func ptr(s string) *string { return &s }
