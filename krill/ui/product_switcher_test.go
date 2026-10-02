// The sidebar's Product switcher (FR c4bd4bf8). The FR's own acceptance
// is a switcher's, so the cases here are the two halves of it stated as
// questions: does a scope holding one product still get a select, and
// does picking another land on the same area's list page rather than
// carrying the old page's id across.
//
// The chrome itself is mounted on no route yet (the cutover task owns
// that seam), so the render half drives the shell data and the component
// directly instead of scraping a route, and the redirect half drives the
// real handler through the real mux.
package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
)

// switcherScopeApp builds an App whose scope holds exactly the products
// given, so the switcher's in-scope decision comes from the test.
func switcherScopeApp(t *testing.T, products ...store.Product) *App {
	t.Helper()
	app := newTestApp(t)
	app.spec = scopedProductsReader{fakeSpecReader: &fakeSpecReader{}, products: products}
	app.scopes = productScopeScopes{scope: store.Scope{ID: uuid.New()}}
	return app
}

// renderSwitcher renders the sidebar's select through the same seam a
// shell page would, and returns the HTML.
func renderSwitcher(t *testing.T, app *App, from string, current uuid.UUID) string {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, from, nil)
	req = withCurrentProduct(req, store.Product{ID: current})

	data := workspaceShellData(
		navTargets{Product: current},
		from,
		"Krill",
		"developer",
		app.productSwitcherData(req),
	)
	return mustRenderComponent(components.Shell(data))
}

// A scope holding exactly one product still gets a select, with that
// product marked. The wireframe's two-option rendering must not become a
// rule that a one-product deployment silently opts out of: the select is
// how an operator tells which product they are in, and that is true most
// often when there is only one.
func TestSwitcherRendersWithASingleProductInScope(t *testing.T) {
	only := store.Product{ID: uuid.New(), Name: "krill"}
	body := renderSwitcher(t, switcherScopeApp(t, only), productHref(only.ID, overviewSuffix), only.ID)

	if !strings.Contains(body, `data-krill="product-switcher"`) {
		t.Errorf("no switcher in the sidebar: %s", body)
	}
	if !strings.Contains(body, `value="`+only.ID.String()+`" selected`) {
		t.Errorf("the only product in scope is not the selected option: %s", body)
	}
	if !strings.Contains(body, ">krill<") {
		t.Errorf("the switcher does not offer the product by name: %s", body)
	}
}

// Every product in scope is offered, not just the current one -- a
// select that listed one product would render identically whether the
// scope held one or twenty and would be wrong in twenty cases.
func TestSwitcherListsEveryProductInScope(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	body := renderSwitcher(t, switcherScopeApp(t,
		store.Product{ID: a, Name: "krill"},
		store.Product{ID: b, Name: "whagent_net"},
		store.Product{ID: c, Name: "friendly_computing_machine"},
	), productHref(a, overviewSuffix), a)

	for _, p := range []struct {
		id   uuid.UUID
		name string
	}{{a, "krill"}, {b, "whagent_net"}, {c, "friendly_computing_machine"}} {
		if !strings.Contains(body, `value="`+p.id.String()+`"`) {
			t.Errorf("product %s (%s) is not offered by the switcher", p.id, p.name)
		}
	}
	if got := strings.Count(body, "<option"); got != 3 {
		t.Errorf("switcher offers %d options, want one per product in scope (3)", got)
	}
	if !strings.Contains(body, `value="`+a.String()+`" selected`) {
		t.Errorf("the current product is not the selected option: %s", body)
	}
}

// A scope the page cannot read yields no select rather than an empty
// one: an empty select would read as "this deployment has no products",
// which is the one thing a failed read does not mean.
func TestSwitcherOmitsItselfWhenTheScopeCannotBeRead(t *testing.T) {
	app := switcherScopeApp(t)
	app.spec = scopedProductsReader{fakeSpecReader: &fakeSpecReader{}, productsErr: errSwitcherScope}

	body := renderSwitcher(t, app, "/products/"+uuid.NewString()+overviewSuffix, uuid.New())
	if strings.Contains(body, "product-switcher") {
		t.Errorf("an unreadable scope still rendered a switcher: %s", body)
	}
	if !strings.Contains(body, "<html") && !strings.Contains(body, "workspace-shell") {
		t.Errorf("the shell did not render around the missing switcher: %s", body)
	}
}

// The switcher's change handler is driven end to end against a scope of
// two products: picking the second lands on that product's area root.
func TestSwitcherChangeLandsOnTheNewProductsAreaRoot(t *testing.T) {
	here, there := uuid.New(), uuid.New()
	mux := http.NewServeMux()
	switcherScopeApp(t,
		store.Product{ID: here, Name: "krill"},
		store.Product{ID: there, Name: "whagent_net"},
	).mountShellRoutes(mux)

	rec := fetch(t, mux, productSwitchPath+"?product="+there.String()+
		"&from="+productHref(here, milestonesSuffix))

	if rec.Code != http.StatusFound {
		t.Fatalf("switching product: status = %d, want 302 (body %s)", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("Location"), productHref(there, milestonesSuffix); got != want {
		t.Errorf("switched to %s, want %s", got, want)
	}
}

// A detail id from the product being left must not survive the switch: a
// milestone or task id from one product means nothing under another, so
// every detail page switches to its own list.
func TestSwitcherNeverCarriesADetailIdAcrossProducts(t *testing.T) {
	here, there, milestone, task := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	mux := http.NewServeMux()
	switcherScopeApp(t,
		store.Product{ID: here, Name: "krill"},
		store.Product{ID: there, Name: "whagent_net"},
	).mountShellRoutes(mux)

	for _, tc := range []struct {
		name string
		from string
		want string
	}{
		{
			name: "a milestone detail under the new prefixes",
			from: productHref(here, milestonesSuffix) + "/" + milestone.String(),
			want: productHref(there, milestonesSuffix),
		},
		{
			name: "a task detail under the spec prefixes",
			from: productPath(here) + "/milestones/" + milestone.String() + "/tasks/" + task.String(),
			want: productHref(there, tasksSuffix),
		},
		{
			name: "a milestone's board",
			from: productPath(here) + "/milestones/" + milestone.String() + "/board",
			want: productHref(there, boardSuffix),
		},
		{
			name: "a design session detail",
			from: designSessionPath(task),
			want: designProductSessionsPath(there),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := fetch(t, mux, productSwitchPath+"?product="+there.String()+"&from="+tc.from)
			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302 (body %s)", rec.Code, rec.Body.String())
			}
			got := rec.Header().Get("Location")
			if got != tc.want {
				t.Errorf("switched to %s, want %s", got, tc.want)
			}
			for _, id := range []string{milestone.String(), task.String(), here.String()} {
				if strings.Contains(got, id) {
					t.Errorf("switch target %s carries an id from the product being left (%s)", got, id)
				}
			}
		})
	}
}

// Each shell area switches to its counterpart under the new product, so
// an operator keeps their bearings across the switch rather than being
// dropped at the overview whatever they were reading.
func TestSwitcherKeepsTheOperatorInTheirArea(t *testing.T) {
	here, there := uuid.New(), uuid.New()
	spec := productPath(here)
	mux := http.NewServeMux()
	switcherScopeApp(t,
		store.Product{ID: here, Name: "krill"},
		store.Product{ID: there, Name: "whagent_net"},
	).mountShellRoutes(mux)

	for _, tc := range []struct{ from, want string }{
		{productHref(here, needsAttentionSuffix), productHref(there, needsAttentionSuffix)},
		{productHref(here, boardSuffix), productHref(there, boardSuffix)},
		{spec + "/decisions", decisionsPath(there)},
		{spec + "/personas", personasPath(there)},
		{spec + "/non-goals", nonGoalsPath(there)},
		{spec + "/delivery", deliveryPath(there)},
		{spec, productPath(there)},
		{designProductSessionsPath(here), designProductSessionsPath(there)},
		// A page that names no product-scoped area -- the shell home, a
		// legacy view -- lands on the new product's overview, which is
		// the one list page every deployment has.
		{"/", productHref(there, overviewSuffix)},
		{opsEscalatedPath, productHref(there, overviewSuffix)},
	} {
		rec := fetch(t, mux, productSwitchPath+"?product="+there.String()+"&from="+tc.from)
		if rec.Code != http.StatusFound {
			t.Fatalf("switching from %s: status = %d, want 302", tc.from, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != tc.want {
			t.Errorf("switching from %s landed on %s, want %s", tc.from, got, tc.want)
		}
	}
}

// A pick naming a product outside the caller's scope is refused in-shell
// rather than obeyed: the switcher must not become a way to reach a
// product this deployment does not serve.
func TestSwitcherRefusesAProductOutOfScope(t *testing.T) {
	inScope, outOfScope := uuid.New(), uuid.New()
	mux := http.NewServeMux()
	switcherScopeApp(t, store.Product{ID: inScope, Name: "krill"}).mountShellRoutes(mux)

	rec := fetch(t, mux, productSwitchPath+"?product="+outOfScope.String()+"&from=/")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Header().Get("Location"), outOfScope.String()) {
		t.Error("an out-of-scope pick was redirected to anyway")
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("the refusal is not rendered inside the shell chrome: %s", rec.Body.String())
	}
}

// A malformed pick is a 400 in-shell, on the same reasoning: neither is
// a redirect and neither names a product.
func TestSwitcherRefusesAMalformedProductID(t *testing.T) {
	mux := http.NewServeMux()
	switcherScopeApp(t, store.Product{ID: uuid.New(), Name: "krill"}).mountShellRoutes(mux)

	rec := fetch(t, mux, productSwitchPath+"?product=not-a-uuid&from=/")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Errorf("a malformed pick was redirected to %s", got)
	}
}

// The pick becomes the last-viewed product, so the next un-prefixed page
// the operator opens follows them to the product they just chose rather
// than snapping back.
func TestSwitcherRecordsThePickAsLastViewed(t *testing.T) {
	here, there := uuid.New(), uuid.New()
	mux := http.NewServeMux()
	switcherScopeApp(t,
		store.Product{ID: here, Name: "krill"},
		store.Product{ID: there, Name: "whagent_net"},
	).mountShellRoutes(mux)

	rec := fetch(t, mux, productSwitchPath+"?product="+there.String()+"&from=/")
	if c := lastViewedCookie(t, rec); c.Value != there.String() {
		t.Errorf("last-viewed product = %s, want the product that was picked (%s)", c.Value, there)
	}
}

// The switcher is the only way an operator names a product: no shell
// page may offer a free-text field for a product id, which is what
// FR c4bd4bf8 means by replacing typed product ids.
func TestShellOffersNoFreeTextProductIDField(t *testing.T) {
	product := store.Product{ID: uuid.New(), Name: "krill"}
	app := switcherScopeApp(t, product)

	req := httptest.NewRequest(http.MethodGet, productHref(product.ID, overviewSuffix), nil)
	req = withCurrentProduct(req, product)
	data := workspaceShellData(navTargets{Product: product.ID}, req.URL.Path, "Krill", "developer",
		app.productSwitcherData(req))
	body := mustRenderComponent(components.Shell(data))

	for _, tag := range []string{`<input type="text"`, `<input class="input`, `name="product_id"`} {
		if strings.Contains(body, tag) {
			t.Errorf("the shell offers a free-text product field (%s)", tag)
		}
	}
	// The one product-named input the shell has is the switcher's own
	// hidden "from", which names a page this binary built -- never a
	// product the operator could edit into an arbitrary one.
	for _, marker := range []string{`type="text"`, `type="number"`} {
		if strings.Contains(body, marker) {
			t.Errorf("the shell renders a %s field", marker)
		}
	}
}

// errSwitcherScope is the injected scope-read failure.
var errSwitcherScope = errors.New("product list read failed")
