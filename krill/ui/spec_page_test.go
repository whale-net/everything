package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func (specStubReader) Products(context.Context) ([]store.Product, error) { return nil, nil }

// specModeMux mounts the four per-product spec handlers against the stub
// reader, at the paths routes.go really registers.
func specModeMux() *http.ServeMux {
	app := &App{spec: specStubReader{}}
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
func TestSpecBadProductIDIsBadRequest(t *testing.T) {
	mux := newTestMux(t)

	for _, path := range []string{
		specProductPath,
		specProductPath + "/decisions",
		specProductPath + "/personas",
		specProductPath + "/non-goals",
		specProductPath + "/delivery",
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

	body := mustRenderComponent(pages.CapabilityMap(capabilityPageOf(doc, productID)))
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
	for _, want := range []string{"One document type", "LB1", dID.String(), fullBody} {
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

// subnavAnchor returns the <a> tag the product sub-nav emits for href, or
// "" when no such link is present. Callers then ask whether that one tag
// carries aria-current, rather than pattern-matching a href/attribute
// adjacency: components.SubNav puts its styling class between the two, and
// htmxui ARCHITECTURE §14 asks tests to assert the behavioural claim
// (which link is current) rather than the presentational markup.
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

// TestProductPagesCrossLink pins the cross-nav: every per-product page links
// to the other four, so an operator who lands anywhere under
// /spec/products/{id} can reach decisions, personas, non-goals, and delivery
// (and back to the capability map) without retyping a URL.
func TestProductPagesCrossLink(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	product := store.Product{ID: productID, Name: "krill"}
	doc := slice.Document{Product: &slice.ProductEntity{Name: "krill"}}

	bodies := map[string]string{
		"capability": mustRenderComponent(pages.CapabilityMap(capabilityPageOf(doc, productID))),
		"decisions":  mustRenderComponent(pages.Decisions(decisionsPageOf(doc, productID))),
		"personas":   mustRenderComponent(pages.Personas(personasPageOf(product, nil, productID))),
		"non-goals":  mustRenderComponent(pages.NonGoals(nonGoalsPageOf(product, nil, productID))),
		"delivery":   renderDeliveryCrossNavPage(product, productID),
	}
	links := []string{
		productPath(productID),
		decisionsPath(productID),
		personasPath(productID),
		nonGoalsPath(productID),
		deliveryPath(productID),
	}

	for name, body := range bodies {
		for _, href := range links {
			if subnavAnchor(body, href) == "" {
				t.Errorf("%s page missing cross-link to %s", name, href)
			}
		}
		// Exactly one link is the current page.
		if got := strings.Count(body, `aria-current="page"`); got != 1 {
			t.Errorf("%s page has %d active cross-links, want 1", name, got)
		}
	}

	// On the delivery page, Delivery is the active link (aria-current), and
	// the other four are present but not marked current.
	delivery := bodies["delivery"]
	if !strings.Contains(subnavAnchor(delivery, deliveryPath(productID)), `aria-current="page"`) {
		t.Errorf("delivery page does not mark the Delivery cross-link as the current page")
	}
	for _, href := range []string{productPath(productID), decisionsPath(productID), personasPath(productID), nonGoalsPath(productID)} {
		if strings.Contains(subnavAnchor(delivery, href), `aria-current="page"`) {
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
		productPath(productID):   mustRenderComponent(pages.CapabilityMap(capabilityPageOf(doc, productID))),
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

// TestSpecSubNavIsPlainNavigation pins that the per-product cross-nav
// stays plain <a> links rather than htmx swaps: moving between them is
// navigation, not a refresh of the current view.
func TestSpecSubNavIsPlainNavigation(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	body := mustRenderComponent(pages.Decisions(decisionsPageOf(slice.Document{
		Product: &slice.ProductEntity{Name: "krill"},
	}, productID)))

	idx := strings.Index(body, `data-krill="product-nav"`)
	if idx < 0 {
		t.Fatalf("the per-product cross-nav did not render as the shared sub-nav region; got: %s", body)
	}
	nav := body[idx:]
	if end := strings.Index(nav, `data-krill="primary-nav"`); end >= 0 {
		nav = nav[:end]
	}
	if strings.Contains(nav, "hx-") {
		t.Errorf("the per-product cross-nav carries hx-* attributes; cross-links are navigation, not swaps")
	}
}

func ptr(s string) *string { return &s }
