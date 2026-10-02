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

// errSwitcherScope is the injected scope-read failure.
var errSwitcherScope = errors.New("product list read failed")

// switcherScopeApp builds an App whose scope holds exactly the products
// given, so the switcher's in-scope decision comes from the test.
func switcherScopeApp(t *testing.T, products ...store.Product) *App {
	t.Helper()
	app := newTestApp(t)
	app.spec = scopedProductsReader{specReadClient: &fakeSpecReader{}, products: products}
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
	body := renderSwitcher(t, switcherScopeApp(t, only), "/products/"+only.ID.String()+"/overview", only.ID)

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
	), "/products/"+a.String()+"/overview", a)

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
	app.spec = scopedProductsReader{specReadClient: &fakeSpecReader{}, productsErr: errSwitcherScope}

	body := renderSwitcher(t, app, "/products/"+uuid.NewString()+"/overview", uuid.New())
	if strings.Contains(body, "product-switcher") {
		t.Errorf("an unreadable scope still rendered a switcher: %s", body)
	}
	if !strings.Contains(body, "<html") && !strings.Contains(body, "workspace-shell") {
		t.Errorf("the shell did not render around the missing switcher: %s", body)
	}
}

// A read that fails and a scope that is genuinely empty are different
// situations, and neither may reach the operator as a select they can
// read: "this deployment has no products" is a claim, and it is false
// both when the read broke and when there is a product the read missed.
// The control is absent in both cases and the surrounding chrome is
// untouched either way -- the failure costs the control, not the page.
func TestSwitcherDistinguishesAnUnreadableScopeFromAnEmptyOne(t *testing.T) {
	failed := switcherScopeApp(t)
	failed.spec = scopedProductsReader{specReadClient: &fakeSpecReader{}, productsErr: errSwitcherScope}

	empty := switcherScopeApp(t)
	empty.spec = scopedProductsReader{specReadClient: &fakeSpecReader{}, products: nil}

	from := "/products/" + uuid.NewString() + "/overview"
	for name, body := range map[string]string{
		"failed read": renderSwitcher(t, failed, from, uuid.New()),
		"empty scope": renderSwitcher(t, empty, from, uuid.New()),
	} {
		if strings.Contains(body, "<select") || strings.Contains(body, "<option") {
			t.Errorf("a %s rendered a select an operator could read as products existing", name)
		}
		if !strings.Contains(body, `data-krill="workspace-shell"`) {
			t.Errorf("a %s did not render the shell around the missing switcher", name)
		}
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
		"&from=/products/"+here.String()+"/milestones")

	if rec.Code != http.StatusFound {
		t.Fatalf("switching product: status = %d, want 302 (body %s)", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("Location"), "/products/"+there.String()+"/milestones"; got != want {
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
			from: "/products/" + here.String() + "/milestones/" + milestone.String(),
			want: "/products/" + there.String() + "/milestones",
		},
		{
			name: "a task detail under the spec prefixes",
			from: "/spec/products/" + here.String() + "/milestones/" + milestone.String() + "/tasks/" + task.String(),
			want: "/products/" + there.String() + "/tasks",
		},
		{
			name: "a milestone's board",
			from: "/spec/products/" + here.String() + "/milestones/" + milestone.String() + "/board",
			want: "/products/" + there.String() + "/board",
		},
		{
			name: "a design session detail",
			from: "/design/design-sessions/" + task.String(),
			want: "/design/products/" + there.String() + "/design-sessions",
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

// The same property, stated over the router rather than over the
// switcher's own table. Every product-scoped page this binary serves is
// listed below, each checked against mux.Handler first so the corpus
// cannot drift into fiction, and each switched away from: none may carry
// an id across.
//
// Checking the corpus against the router is what keeps this from being
// self-referential. The cases above are the switcher's cases -- if a page
// is missing from productAreaHref, a list written from that same table
// would not notice, because both come from one author's reading. Here a
// path that stops being a route fails the test instead of quietly
// passing, and a route the switcher's table never learned about is
// covered the day it lands.
func TestSwitcherLandsOnAListForEveryPageTheRouterServes(t *testing.T) {
	here, there, mid, tid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	mux := http.NewServeMux()
	switcherScopeApp(t,
		store.Product{ID: here, Name: "krill"},
		store.Product{ID: there, Name: "whagent_net"},
	).mountShellRoutes(mux)

	// Every product-scoped page the mux registers, spelled out. The ids
	// are distinct so a leak of any one of them is caught, and every
	// expectation below is a literal -- nothing here is built by the
	// href helpers the switcher itself calls.
	for _, from := range []string{
		"/products/" + here.String() + "/overview",
		"/products/" + here.String() + "/needs-attention",
		"/products/" + here.String() + "/tasks",
		"/products/" + here.String() + "/board",
		"/products/" + here.String() + "/milestones",
		"/products/" + here.String() + "/milestones/" + mid.String(),
		"/spec/products/" + here.String(),
		"/spec/products/" + here.String() + "/decisions",
		"/spec/products/" + here.String() + "/personas",
		"/spec/products/" + here.String() + "/non-goals",
		"/spec/products/" + here.String() + "/delivery",
		"/spec/products/" + here.String() + "/milestones/" + mid.String() + "/tasks",
		"/spec/products/" + here.String() + "/milestones/" + mid.String() + "/tasks/" + tid.String(),
		"/spec/products/" + here.String() + "/milestones/" + mid.String() + "/board",
		"/design/products/" + here.String() + "/design-sessions",
		"/design/design-sessions/" + tid.String(),
	} {
		// The corpus is verified against the router before it is trusted:
		// a path here that no longer resolves is a stale case, and a
		// stale case passing is exactly the self-reference this test
		// exists to rule out.
		if _, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, from, nil)); pattern == "" || pattern == "/" {
			t.Errorf("the corpus lists %s, which this router does not serve; the case is stale", from)
			continue
		}

		rec := fetch(t, mux, productSwitchPath+"?product="+there.String()+"&from="+from)
		if rec.Code != http.StatusFound {
			t.Errorf("switching from %s: status = %d, want 302", from, rec.Code)
			continue
		}
		got := rec.Header().Get("Location")
		for name, id := range map[string]string{
			"the product being left": here.String(),
			"the milestone":          mid.String(),
			"the task or session":    tid.String(),
		} {
			if strings.Contains(got, id) {
				t.Errorf("switching from %s landed on %s, which carries %s (%s)", from, got, name, id)
			}
		}
		if !strings.Contains(got, there.String()) {
			t.Errorf("switching from %s landed on %s, which does not name the product that was picked", from, got)
		}
		// The landing page is a list the router serves, not merely a
		// well-formed path: a switch that pointed at a 404 would be a
		// worse outcome than keeping the operator in place.
		if _, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, got, nil)); pattern == "" || pattern == "/" {
			t.Errorf("switching from %s landed on %s, which this router does not serve", from, got)
		}
	}
}

// Each shell area switches to its counterpart under the new product, so
// an operator keeps their bearings across the switch rather than being
// dropped at the overview whatever they were reading.
func TestSwitcherKeepsTheOperatorInTheirArea(t *testing.T) {
	here, there := uuid.New(), uuid.New()
	spec := "/spec/products/" + here.String()
	mux := http.NewServeMux()
	switcherScopeApp(t,
		store.Product{ID: here, Name: "krill"},
		store.Product{ID: there, Name: "whagent_net"},
	).mountShellRoutes(mux)

	for _, tc := range []struct{ from, want string }{
		{"/products/" + here.String() + "/needs-attention", "/products/" + there.String() + "/needs-attention"},
		{"/products/" + here.String() + "/board", "/products/" + there.String() + "/board"},
		{spec + "/decisions", "/spec/products/" + there.String() + "/decisions"},
		{spec + "/personas", "/spec/products/" + there.String() + "/personas"},
		{spec + "/non-goals", "/spec/products/" + there.String() + "/non-goals"},
		{spec + "/delivery", "/spec/products/" + there.String() + "/delivery"},
		{spec, "/spec/products/" + there.String()},
		{"/design/products/" + here.String() + "/design-sessions", "/design/products/" + there.String() + "/design-sessions"},
		// A page that names no product-scoped area -- the shell home, a
		// legacy view -- lands on the new product's overview, which is
		// the one list page every deployment has.
		{"/", "/products/" + there.String() + "/overview"},
		{"/ops/escalated", "/products/" + there.String() + "/overview"},
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

	req := httptest.NewRequest(http.MethodGet, "/products/"+product.ID.String()+"/overview", nil)
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

// The switcher's two halves are wired to each other: the "from" the form
// carries is the page it was rendered on, and that page is what the
// handler classifies. A switcher that rendered the action but a stale or
// blank "from" would land every operator on the overview regardless of
// which area they were reading, which is a bug the two halves' own tests
// cannot see -- each drives its side with a hand-written path.
func TestSwitcherFormCarriesThePageItIsRendering(t *testing.T) {
	here := uuid.New()
	mid := uuid.New()
	on := "/spec/products/" + here.String() + "/milestones/" + mid.String() + "/board"

	body := renderSwitcher(t, switcherScopeApp(t, store.Product{ID: here, Name: "krill"}), on, here)

	if !strings.Contains(body, `action="`+productSwitchPath+`"`) {
		t.Errorf("the switcher does not post to %s: %s", productSwitchPath, body)
	}
	if !strings.Contains(body, `name="from" value="`+on+`"`) {
		t.Errorf("the switcher does not carry the page it is rendering as %q", on)
	}
}

// productAreaShapeWildcardsEveryID proves the classification's premise
// directly: an id segment is recognised by being a UUID, wherever it sits
// in the path, and a non-id segment is never mistaken for one.
//
// This is the load-bearing claim behind "a new area cannot get the
// id-leak wrong" -- the property the next test exercises through the
// handler. It is asserted on the shape rather than through the handler
// because the shape is where the rule lives; a positional classifier
// could pass every handler-level case in this file and still fail here.
func TestProductAreaShapeWildcardsEveryIDSegment(t *testing.T) {
	pid, mid, tid := uuid.New(), uuid.New(), uuid.New()

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{
			name: "ids in every position of an area this build knows",
			path: "/products/" + pid.String() + "/milestones/" + mid.String() + "/tasks/" + tid.String(),
			want: "products/*/milestones/*/tasks/*",
		},
		{
			name: "an area the table has no case for, with an id under it",
			path: "/products/" + pid.String() + "/widgets/" + mid.String(),
			want: "products/*/widgets/*",
		},
		{
			name: "an area reached by a prefix this build has never heard of",
			path: "/brand-new-root/" + pid.String() + "/inspections/" + mid.String() + "/runs/" + tid.String(),
			want: "brand-new-root/*/inspections/*/runs/*",
		},
		{
			name: "a segment that merely looks id-like but is not one",
			path: "/products/" + pid.String() + "/milestones/upcoming",
			want: "products/*/milestones/upcoming",
		},
		{
			name: "a non-canonical UUID, which is still an id",
			path: "/products/urn:uuid:" + pid.String() + "/widgets/" + mid.String(),
			want: "products/*/widgets/*",
		},
		{
			name: "a path with no ids at all is left alone",
			path: "/ops/escalated",
			want: "ops/escalated",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(productAreaShape(tc.path), "/"); got != tc.want {
				t.Errorf("productAreaShape(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// A new area added later cannot get the id-leak wrong, because no area
// has to be taught where its id is: every id is dropped before the
// lookup, and a shape the table has no case for falls back to the new
// product's overview rather than echoing anything from the old page.
//
// The "from" paths here are shapes this binary has never seen -- areas
// named in no case of productAreaHref, some under roots it does not
// serve at all -- because that is the case the design is for. An area
// that ships and gets a case keeps its bearings; one that has not gets
// the overview. Neither carries an id.
func TestSwitcherDropsTheIDsFromAnAreaTheCodeHasNeverSeen(t *testing.T) {
	here, there, mid, tid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	mux := http.NewServeMux()
	switcherScopeApp(t,
		store.Product{ID: here, Name: "krill"},
		store.Product{ID: there, Name: "whagent_net"},
	).mountShellRoutes(mux)

	for _, from := range []string{
		// An area under a prefix the router serves that the switch table
		// has no case for.
		"/products/" + here.String() + "/widgets/" + mid.String(),
		// An area under a prefix the router does not serve at all, which
		// is what a page that exists on no build yet looks like.
		"/brand-new-root/" + here.String() + "/inspections/" + mid.String() + "/runs/" + tid.String(),
		// An area whose id segment is not a UUID, so the shape cannot
		// wildcard it -- the case a positional classifier gets wrong.
		"/products/" + here.String() + "/widgets/upcoming",
		// A page that names a product at all but no area beneath it.
		"/products/" + here.String() + "/" + mid.String(),
	} {
		rec := fetch(t, mux, productSwitchPath+"?product="+there.String()+"&from="+from)
		if rec.Code != http.StatusFound {
			t.Fatalf("switching from %s: status = %d, want 302 (body %s)", from, rec.Code, rec.Body.String())
		}
		got := rec.Header().Get("Location")

		for _, id := range []string{here.String(), mid.String(), tid.String()} {
			if strings.Contains(got, id) {
				t.Errorf("switching from the unseen area %s landed on %s, which carries %s", from, got, id)
			}
		}
		// Whatever the shape, the target is a page this binary serves.
		// A fallthrough to the new product's overview is the only thing a
		// shape with no case may produce; anything else means the default
		// stopped being a real URL.
		if fetched := fetch(t, mux, got); fetched.Code != http.StatusOK {
			t.Errorf("switching from %s landed on %s, which does not serve (status %d)", from, got, fetched.Code)
		}
	}
}

// A hand-edited "from" cannot turn the switch into an open redirect:
// the handler classifies the value it is given and rebuilds the target
// from the validated product id alone, so the only thing a forged path
// can influence is which area is chosen -- never the host it points at.
func TestSwitcherCannotBeForgedIntoAnOpenRedirect(t *testing.T) {
	here, there := uuid.New(), uuid.New()
	mux := http.NewServeMux()
	switcherScopeApp(t,
		store.Product{ID: here, Name: "krill"},
		store.Product{ID: there, Name: "whagent_net"},
	).mountShellRoutes(mux)

	for _, from := range []string{
		"https://evil.example.com/steal",
		"//evil.example.com/steal",
		"/\\evil.example.com",
		"//evil.example.com/spec/products/" + here.String() + "/decisions",
	} {
		rec := fetch(t, mux, productSwitchPath+"?product="+there.String()+"&from="+from)
		if rec.Code != http.StatusFound {
			t.Fatalf("switching from %q: status = %d, want 302", from, rec.Code)
		}
		got := rec.Header().Get("Location")
		if !strings.HasPrefix(got, "/") || strings.HasPrefix(got, "//") {
			t.Errorf("a forged from (%q) produced the off-site target %q", from, got)
			continue
		}
		if strings.Contains(got, "evil.example.com") {
			t.Errorf("a forged from (%q) produced the off-site target %q", from, got)
		}
	}
}
