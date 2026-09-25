package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

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
		for _, want := range []string{"<html", "<nav>", "</html>"} {
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

	body := string(renderPage(capabilityTemplate, capabilityPageOf(doc, productID)))
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

	body := string(renderPage(decisionsTemplate, decisionsPageOf(doc, productID)))
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

	body := string(renderPage(personasTemplate, personasPageOf(product, personas, productID)))
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

	body := string(renderPage(nonGoalsTemplate, nonGoalsPageOf(product, nonGoals, productID)))
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

// TestProductPagesCrossLink pins the cross-nav: every per-product page links
// to the other four, so an operator who lands anywhere under
// /spec/products/{id} can reach decisions, personas, non-goals, and delivery
// (and back to the capability map) without retyping a URL.
func TestProductPagesCrossLink(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	product := store.Product{ID: productID, Name: "krill"}
	doc := slice.Document{Product: &slice.ProductEntity{Name: "krill"}}

	pages := map[string]string{
		"capability": string(renderPage(capabilityTemplate, capabilityPageOf(doc, productID))),
		"decisions":  string(renderPage(decisionsTemplate, decisionsPageOf(doc, productID))),
		"personas":   string(renderPage(personasTemplate, personasPageOf(product, nil, productID))),
		"non-goals":  string(renderPage(nonGoalsTemplate, nonGoalsPageOf(product, nil, productID))),
		"delivery":   string(renderPage(deliveryTemplate, deliveryPageOf(product, slice.DeliveryListing{}, nil, productID))),
	}
	links := []string{
		productPath(productID),
		decisionsPath(productID),
		personasPath(productID),
		nonGoalsPath(productID),
		deliveryPath(productID),
	}

	for name, body := range pages {
		for _, href := range links {
			if !strings.Contains(body, `href="`+href+`"`) {
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
	delivery := pages["delivery"]
	if !strings.Contains(delivery, `href="`+deliveryPath(productID)+`" aria-current="page"`) {
		t.Errorf("delivery page does not mark the Delivery cross-link as the current page")
	}
	for _, href := range []string{productPath(productID), decisionsPath(productID), personasPath(productID), nonGoalsPath(productID)} {
		if strings.Contains(delivery, `href="`+href+`" aria-current="page"`) {
			t.Errorf("delivery page wrongly marks %s as the current page", href)
		}
	}
}

func ptr(s string) *string { return &s }
