package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// productScopeMux mounts the real product-scoped routes against a reader
// holding the given products and no delivery listing. The scope, task and
// design-session stores are stubbed too, so the un-prefixed ops views
// mounted alongside and the Overview's blocking-questions tile can render
// without one.
//
// A container-addressed route (/milestones/{mid}) answers 404 against this
// fixture, because a milestone id resolves out of the product's own
// delivery listing and this one is empty. Callers whose subject is such a
// route want productScopeMuxWithListing, which hands it a listing holding
// the id under test.
func productScopeMux(t *testing.T, products ...store.Product) *http.ServeMux {
	t.Helper()
	return productScopeMuxWithListing(t, slice.DeliveryListing{}, products...)
}

// productScopeMuxWithListing mounts the same routes against a reader that
// answers Delivery with the given listing, for the routes whose URL names
// a container rather than a product alone.
func productScopeMuxWithListing(t *testing.T, listing slice.DeliveryListing, products ...store.Product) *http.ServeMux {
	t.Helper()
	app := newTestApp(t)
	app.spec = scopedProductsReader{specReadClient: &fakeSpecReader{listing: listing}, products: products}
	app.scopes = productScopeScopes{scope: store.Scope{ID: uuid.New()}}
	app.tasks = productScopeTasks{}
	app.designSessions = navStubDesignSessions{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// lastViewedCookie reads the last-viewed-product cookie off a response,
// failing the test when the handler did not write one. The cookie is the
// whole observable output of the un-prefixed resolution, so a test that
// cannot find it has nothing to assert on.
// productScopeScopes and productScopeTasks are the two stores the
// un-prefixed ops views read. They embed their interfaces and implement only
// what those views call, so a view reaching for anything else nil-panics
// rather than silently rendering a fabricated page.
type productScopeScopes struct {
	store.ScopeStore
	scope store.Scope
}

func (s productScopeScopes) GetSole(context.Context) (store.Scope, error) { return s.scope, nil }

type productScopeTasks struct {
	store.TaskStore
}

func (productScopeTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, nil
}

// CountEscalatedTasks is the chrome's Needs-attention badge read AND the
// Needs attention page's Escalated tab count, which every shell page renders
// and the ops URLs now redirect into. It is separate from the List method
// above on purpose: the badge's figure must never be capped at a page size,
// so the store gives it a dedicated count read rather than reusing the list.
func (productScopeTasks) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}

// The other three tabs' counts, read by the Needs attention page the ops
// URLs redirect into. Zero: this fixture's subject is which product a URL
// resolves to, not what any queue holds.
func (productScopeTasks) CountClaimedTasks(context.Context, store.ListClaimedTasksParams) (int, error) {
	return 0, nil
}

func (productScopeTasks) CountCancelledTasks(context.Context, store.ListCancelledTasksParams) (int, error) {
	return 0, nil
}

func (productScopeTasks) CountOpenNotes(context.Context, store.ListOpenNotesParams) (int, error) {
	return 0, nil
}

// SummarizeProductTaskProgress is the read the Overview's in-flight panel
// makes. It answers with no containers so the panel renders its empty
// state: these fixtures' subject is the product a URL resolves to, not the panel's rows.
func (productScopeTasks) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	return store.ProductTaskProgress{ProductID: params.ProductID, Containers: []store.ContainerTaskProgress{}}, nil
}

func (productScopeTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, nil
}

func (productScopeTasks) ListOpenNotes(context.Context, store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	return store.Page[store.OpenNoteRow]{}, nil
}

// The product-wide Tasks/Board read, which those two routes now serve.
// Both answer empty: this fixture's subject is which product a URL resolves
// to, not what any page lists.
func (productScopeTasks) ListProductTasks(context.Context, store.ListProductTasksParams) (store.Page[store.ProductTaskRow], error) {
	return store.Page[store.ProductTaskRow]{}, nil
}

func (productScopeTasks) CountProductTasks(context.Context, store.ListProductTasksParams) (int, error) {
	return 0, nil
}

func (productScopeTasks) ListCancelledTasks(context.Context, store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	return store.Page[store.CancelledTaskRow]{}, nil
}

// CountConsoleOverview is the Overview stat tiles' read, reached by every
// case here that renders the home. Zero figures, so these cases stay about
// which product a URL resolves to.
func (productScopeTasks) CountConsoleOverview(context.Context, store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	return store.ConsoleOverviewCounts{}, nil
}

func lastViewedCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		if c.Name == lastViewedProductCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie on the response", lastViewedProductCookie)
	return nil
}

// fetchWith issues one request carrying the given cookies, the way a
// browser returning to a legacy link would.
func fetchWith(t *testing.T, mux *http.ServeMux, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// followWith is fetchWith through any redirect, which is what a browser does
// with an un-prefixed link now: the ops URLs resolve a product and 302 into
// the product-scoped Needs attention page, and THAT page is what records the
// last-viewed product and renders the chrome. A test asserting on an
// un-prefixed page's product resolution therefore has to ask the page the
// operator actually lands on, or it reads the 302's empty body.
func followWith(t *testing.T, mux *http.ServeMux, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	const maxHops = 5
	for range maxHops {
		rec := fetchWith(t, mux, target, cookies...)
		if rec.Code != http.StatusFound && rec.Code != http.StatusMovedPermanently {
			return rec
		}
		next := rec.Header().Get("Location")
		if next == "" {
			t.Fatalf("%s redirected with no Location", target)
		}
		target = next
	}
	t.Fatalf("%s redirected more than %d times", target, maxHops)
	return nil
}

// A product-scoped URL resolves the product it names and renders inside
// the shell.
func TestProductScopedPlaceholderResolvesNamedProduct(t *testing.T) {
	pid := uuid.New()
	mux := productScopeMux(t, store.Product{ID: pid, Name: "krill"})

	rec := fetch(t, mux, productHref(pid, overviewSuffix))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "krill") {
		t.Errorf("page does not name the product it resolved: %s", body)
	}
	if !strings.Contains(body, "<html") {
		t.Error("placeholder did not render inside the shell chrome")
	}
}

// An out-of-scope pid must not render the placeholder at all: the
// resolver answers an in-shell 404 before any content is built.
func TestProductScopedOutOfScopeIsInShell404(t *testing.T) {
	inScope, outOfScope := uuid.New(), uuid.New()
	mux := productScopeMux(t, store.Product{ID: inScope, Name: "krill"})

	rec := fetch(t, mux, productHref(outOfScope, overviewSuffix))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "<html") {
		t.Errorf("404 is not rendered inside the shell chrome: %s", body)
	}
	if strings.Contains(body, "product-placeholder") {
		t.Error("out-of-scope product rendered the placeholder body")
	}
}

// A prefixed link resolves the product it names and records it, so the
// operator's next un-prefixed page lands where they were just working.
// This is the whole of the "copied link shows the same product" clause:
// the cookie written here is what makes the next legacy link agree.
func TestPrefixedURLRecordsTheProductItNamed(t *testing.T) {
	pid := uuid.New()
	mux := productScopeMux(t, store.Product{ID: pid, Name: "krill"})

	rec := fetch(t, mux, productHref(pid, milestonesSuffix))

	if got := lastViewedCookie(t, rec).Value; got != pid.String() {
		t.Errorf("last-viewed cookie = %s, want %s", got, pid)
	}
}

// The cookie is a hint and never an authority: a prefixed URL for product A
// renders A and records A even when the operator's cookie says B.
func TestCookieNeverOverridesAPrefixedURL(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	mux := productScopeMux(t,
		store.Product{ID: a, Name: "product A"},
		store.Product{ID: b, Name: "product B"},
	)

	rec := fetchWith(t, mux, productHref(a, overviewSuffix),
		&http.Cookie{Name: lastViewedProductCookie, Value: b.String()})

	if body := rec.Body.String(); !strings.Contains(body, "product A") {
		t.Errorf("prefixed URL for A rendered something else: %s", body)
	}
	if got := lastViewedCookie(t, rec).Value; got != a.String() {
		t.Errorf("last-viewed cookie = %s, want the URL's product %s", got, a)
	}
}

// An un-prefixed URL with no cookie resolves the first product in scope and
// says so in the cookie -- the operator never has to name one.
func TestUnprefixedURLResolvesFirstProductInScope(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	mux := productScopeMux(t,
		store.Product{ID: first, Name: "product A"},
		store.Product{ID: second, Name: "product B"},
	)

	for _, target := range []string{"/", opsEscalatedPath} {
		t.Run(target, func(t *testing.T) {
			// /ops/escalated is a redirect into the product-scoped Needs
			// attention page now, so the product it resolves is recorded by
			// the page the operator lands on -- which is what a browser
			// asking this question actually sees.
			rec := followWith(t, mux, target)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := lastViewedCookie(t, rec).Value; got != first.String() {
				t.Errorf("last-viewed cookie = %s, want the first product in scope %s", got, first)
			}
		})
	}
}

// A cookie naming a product that has left the scope falls back to the first
// product in scope. It is the ordinary state for a legacy link after a
// rename or a removal, and 404-ing it would break the link for good.
func TestStaleCookieFallsBackToFirstProductInScope(t *testing.T) {
	gone, current := uuid.New(), uuid.New()
	mux := productScopeMux(t, store.Product{ID: current, Name: "product B"})

	rec := fetchWith(t, mux, "/",
		&http.Cookie{Name: lastViewedProductCookie, Value: gone.String()})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := lastViewedCookie(t, rec).Value; got != current.String() {
		t.Errorf("last-viewed cookie = %s, want the fallback %s", got, current)
	}
}

// A cookie naming a product still in scope is honoured: this is what makes
// the operator's next legacy page stay on the product they were reading.
func TestInScopeCookieIsHonoured(t *testing.T) {
	first, last := uuid.New(), uuid.New()
	mux := productScopeMux(t,
		store.Product{ID: first, Name: "product A"},
		store.Product{ID: last, Name: "product B"},
	)

	rec := fetchWith(t, mux, "/",
		&http.Cookie{Name: lastViewedProductCookie, Value: last.String()})

	if got := lastViewedCookie(t, rec).Value; got != last.String() {
		t.Errorf("last-viewed cookie = %s, want the cookie's product %s", got, last)
	}
}

// A scope holding no product is an empty state, not a 404: the URL resolved
// fine and there is simply nothing behind it yet. It shows on /design, the
// one un-prefixed page that cannot render a body without a product.
func TestEmptyScopeRendersAnEmptyStateNotA404(t *testing.T) {
	mux := productScopeMux(t)

	rec := fetch(t, mux, designPath)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- an empty scope is not a broken link", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No products in this scope") {
		t.Errorf("empty scope did not render the empty state: %s", body)
	}
	if !strings.Contains(body, "<html") {
		t.Error("empty state did not render inside the shell chrome")
	}
	if got := rec.Header().Get("Set-Cookie"); strings.Contains(got, lastViewedProductCookie) {
		t.Error("an empty scope wrote a last-viewed cookie naming no product")
	}
}

// The credentials page must still mint a token when the scope holds no
// product -- blocking it would lock an operator out of the very tool they
// need to fix things.
func TestCredentialsPageStillRendersWithAnEmptyScope(t *testing.T) {
	app := newTestApp(t)
	app.spec = scopedProductsReader{specReadClient: &fakeSpecReader{}}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)

	rec := fetch(t, mux, credentialsPath)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "No products in this scope") {
		t.Errorf("credentials page was replaced by the empty state: %s", body)
	}
}

// No shell page asks the operator to type or paste a product id. The design
// root used to be the one that did; this pins that no page in the shell's
// own route table grew an id input back.
func TestNoShellPageAsksForATypedProductID(t *testing.T) {
	app := newTestApp(t)
	pid := uuid.New()
	app.spec = scopedProductsReader{specReadClient: &fakeSpecReader{
		products: nil,
		listing: slice.DeliveryListing{
			Milestones: []slice.MilestoneListingEntry{{ID: navMilestoneID, Name: "Shipped shell"}},
		},
	}, products: []store.Product{{ID: pid, Name: "krill"}}}
	// The console reads the walk visits, and the scope the chrome reads
	// its Needs-attention badge under.
	app.tasks = &navTasks{milestoneID: navMilestoneID}
	app.scopes = chromeScopes{}
	app.designSessions = navStubDesignSessions{}
	app.revisionEvents = navStubRevisionEvents{}
	app.credentials = &fakeCredentials{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)

	for _, path := range shellPagePaths(pid) {
		rec := fetchPage(t, mux, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", path, rec.Code)
			continue
		}
		if body := rec.Body.String(); strings.Contains(body, `name="product_id"`) {
			t.Errorf("GET %s asks the operator for a product id", path)
		}
	}
}

// The whole product-scoped route table, spelled out as literals rather
// than derived from the suffix constants: deleting a route (or a constant)
// would otherwise shrink the very table that checks it, and pass.
//
// The milestone-detail row is the acceptance case that matters most -- a
// copied link with a milestone id under it is exactly the shape a link
// pasted into chat takes, and it has to open on the product it names.
func TestEveryProductScopedRouteResolvesItsOwnProduct(t *testing.T) {
	pid := uuid.New()
	mid := uuid.New()
	// The {mid} row of the table is a container-addressed route, so the
	// fixture has to hold the id that URL names: a milestone resolves out
	// of the product's own delivery listing, and one that does not is a
	// 404 by design rather than a product-resolution failure.
	mux := productScopeMuxWithListing(t, slice.DeliveryListing{
		Milestones: []slice.MilestoneListingEntry{{ID: mid, Name: "M0 Scoped milestone"}},
	}, store.Product{ID: pid, Name: "krill"})

	for _, target := range []string{
		"/products/" + pid.String() + "/overview",
		"/products/" + pid.String() + "/needs-attention",
		"/products/" + pid.String() + "/tasks",
		"/products/" + pid.String() + "/board",
		"/products/" + pid.String() + "/milestones",
		"/products/" + pid.String() + "/milestones/" + mid.String(),
		"/products/" + pid.String() + "/milestones/" + mid.String() + "/status-history",
	} {
		t.Run(target, func(t *testing.T) {
			rec := fetch(t, mux, target)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if body := rec.Body.String(); !strings.Contains(body, "krill") {
				t.Errorf("page does not name the product the URL named: %s", body)
			}
			if got := lastViewedCookie(t, rec).Value; got != pid.String() {
				t.Errorf("last-viewed cookie = %s, want the URL's product %s", got, pid)
			}
		})
	}
}

// A copied milestone-detail link opens on the product it names, not on
// whatever the opener's cookie remembers. This is the {mid} branch of the
// URL table above stated as the clause it exists to satisfy.
func TestCopiedMilestoneDetailLinkOpensOnTheProductItNames(t *testing.T) {
	a, b, mid := uuid.New(), uuid.New(), uuid.New()
	mux := productScopeMuxWithListing(t, slice.DeliveryListing{
		Milestones: []slice.MilestoneListingEntry{{ID: mid, Name: "M0 Copied link's milestone"}},
	},
		store.Product{ID: a, Name: "product A"},
		store.Product{ID: b, Name: "product B"},
	)

	target := "/products/" + a.String() + "/milestones/" + mid.String()
	rec := fetchWith(t, mux, target,
		&http.Cookie{Name: lastViewedProductCookie, Value: b.String()})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "product A") {
		t.Errorf("copied milestone link did not render the linked product: %s", body)
	}
	// Scoped to the page body, not the whole document: the sidebar's
	// Product select lists every product in scope by design, so the
	// cookie's product appearing as an unselected option says nothing
	// about which product the page resolved.
	if page := productPageRegion(body); strings.Contains(page, "product B") {
		t.Errorf("copied milestone link rendered the cookie's product too: %s", page)
	}
	// The switcher marks the current product, so the cookie's product must
	// be the one left unselected.
	if strings.Contains(selectedOptionRegion(body), "product B") {
		t.Errorf("the switcher selected the cookie's product on a prefixed URL: %s", body)
	}
	if got := lastViewedCookie(t, rec).Value; got != a.String() {
		t.Errorf("last-viewed cookie = %s, want the linked product %s", got, a)
	}
}

// productPageRegion slices the routed page's own region out of a shell
// document, so an assertion about which product the page is about cannot
// be satisfied by the name of a product the sidebar happens to offer.
//
// It slices whichever region the page rendered. A product-scoped URL now
// lands on real content rather than the placeholder, so keying this to the
// placeholder's marker would silently return "" and make every assertion
// against it pass -- which is how a copied link resolving to the WRONG
// product would go unnoticed.
func productPageRegion(body string) string {
	for _, marker := range []string{
		`data-krill="product-placeholder"`,
		`data-krill="milestone-detail"`,
		// The status-history sub-page hangs under a milestone's detail URL.
		// A URL whose region this cannot find slices to "", and every
		// assertion made against that "" passes -- so each page the
		// product-scoped routes reach has to be named here or its own tests
		// assert nothing.
		`data-krill="milestone-status-history"`,
	} {
		start := strings.Index(body, marker)
		if start < 0 {
			continue
		}
		end := strings.Index(body[start:], "</section>")
		if end < 0 {
			return body[start:]
		}
		return body[start : start+end]
	}
	return ""
}

// selectedOptionRegion slices the switcher's selected option out of a page.
func selectedOptionRegion(body string) string {
	rest := body
	for {
		i := strings.Index(rest, "<option")
		if i < 0 {
			return ""
		}
		rest = rest[i:]
		end := strings.Index(rest, "</option>")
		if end < 0 {
			return ""
		}
		option := rest[:end]
		if strings.Contains(option, "selected") {
			return option
		}
		rest = rest[end:]
	}
}

// A milestone-detail link whose product has left the scope is a 404 in the
// chrome, not a page that renders the placeholder against a product the
// caller cannot see. The {mid} branch resolves {pid} before the id is ever
// looked at, so this holds for detail links exactly as it does for roots.
func TestOutOfScopeMilestoneDetailLinkIsInShell404(t *testing.T) {
	inScope, outOfScope := uuid.New(), uuid.New()
	mux := productScopeMux(t, store.Product{ID: inScope, Name: "krill"})

	rec := fetch(t, mux, "/products/"+outOfScope.String()+"/milestones/"+uuid.New().String())

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "<html") {
		t.Errorf("404 is not rendered inside the shell chrome: %s", body)
	}
	if strings.Contains(rec.Header().Get("Set-Cookie"), lastViewedProductCookie) {
		t.Error("a refused out-of-scope link recorded the product as last-viewed")
	}
}

// A pid that is not a UUID at all is refused the same way an unknown one
// is. It is not a crash, a bare error, or a 500 -- a link an operator
// followed lands inside the shell they can navigate back out of.
func TestMalformedProductIDIsInShell404(t *testing.T) {
	pid := uuid.New()
	mux := productScopeMux(t, store.Product{ID: pid, Name: "krill"})

	rec := fetch(t, mux, "/products/not-a-uuid/overview")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<html") {
		t.Errorf("404 is not rendered inside the shell chrome: %s", body)
	}
	if strings.Contains(body, "product-placeholder") {
		t.Error("a malformed pid rendered the placeholder body")
	}
}

// Every un-prefixed page the shell owns resolves the product server-side
// and records it. The list is the shell's own un-prefixed routes spelled
// out, because the point is that none of them was missed -- /ops/escalated
// and / are the acceptance cases, the rest are the same wiring.
func TestEveryUnprefixedPageResolvesARecordedProduct(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	mux := productScopeMux(t,
		store.Product{ID: first, Name: "product A"},
		store.Product{ID: second, Name: "product B"},
	)

	for _, target := range []string{
		"/",
		opsPath,
		opsClaimedPath,
		opsEscalatedPath,
		opsCancelledPath,
		opsNotesPath,
		credentialsPath,
	} {
		t.Run(target, func(t *testing.T) {
			rec := followWith(t, mux, target)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := lastViewedCookie(t, rec).Value; got != first.String() {
				t.Errorf("last-viewed cookie = %s, want the first product in scope %s", got, first)
			}
		})
	}
}

// An in-scope cookie is honoured on the legacy read views, not only on the
// home page: /ops/escalated is the acceptance case, and it is the one an
// operator arrives at from a bookmark.
func TestUnprefixedPageHonoursAnInScopeCookie(t *testing.T) {
	first, last := uuid.New(), uuid.New()
	mux := productScopeMux(t,
		store.Product{ID: first, Name: "product A"},
		store.Product{ID: last, Name: "product B"},
	)

	for _, target := range []string{"/", opsEscalatedPath} {
		t.Run(target, func(t *testing.T) {
			rec := followWith(t, mux, target,
				&http.Cookie{Name: lastViewedProductCookie, Value: last.String()})

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := lastViewedCookie(t, rec).Value; got != last.String() {
				t.Errorf("last-viewed cookie = %s, want the cookie's product %s", got, last)
			}
		})
	}
}

// A cookie naming a product that has left the scope never breaks the link
// it would have guided: the page falls back to the first product in scope
// and stays 200. This holds across the un-prefixed views, because a
// 404-ing one is exactly how a legacy bookmark rots.
func TestStaleCookieFallsBackOnEveryUnprefixedPage(t *testing.T) {
	gone, current := uuid.New(), uuid.New()
	mux := productScopeMux(t, store.Product{ID: current, Name: "product B"})

	for _, target := range []string{"/", opsEscalatedPath, credentialsPath} {
		t.Run(target, func(t *testing.T) {
			rec := followWith(t, mux, target,
				&http.Cookie{Name: lastViewedProductCookie, Value: gone.String()})

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 -- a stale cookie must not break a legacy link", rec.Code)
			}
			if got := lastViewedCookie(t, rec).Value; got != current.String() {
				t.Errorf("last-viewed cookie = %s, want the fallback %s", got, current)
			}
		})
	}
}

// A cookie whose value is not a UUID is a hint that cannot be honoured, so
// it falls back exactly as a stale one does rather than erroring the page.
func TestUnreadableCookieFallsBackToFirstProductInScope(t *testing.T) {
	current := uuid.New()
	mux := productScopeMux(t, store.Product{ID: current, Name: "product B"})

	rec := followWith(t, mux, opsEscalatedPath,
		&http.Cookie{Name: lastViewedProductCookie, Value: "not-a-uuid"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := lastViewedCookie(t, rec).Value; got != current.String() {
		t.Errorf("last-viewed cookie = %s, want the fallback %s", got, current)
	}
}

// A prefixed URL resolves its product when the product list cannot be
// read, and says so in the chrome at 500 rather than falling through to
// the un-prefixed resolution and rendering some other product. A link an
// operator followed must not quietly become a different link.
func TestPrefixedURLDoesNotFallBackWhenTheProductReadFails(t *testing.T) {
	pid := uuid.New()
	app := newTestApp(t)
	app.spec = scopedProductsReader{specReadClient: &fakeSpecReader{},
		productsErr: errors.New("product list unavailable")}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)

	rec := fetch(t, mux, productHref(pid, overviewSuffix))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<html") {
		t.Errorf("500 is not rendered inside the shell chrome: %s", body)
	}
	if strings.Contains(body, "product-placeholder") {
		t.Error("a failed product read rendered the placeholder body anyway")
	}
	if strings.Contains(rec.Header().Get("Set-Cookie"), lastViewedProductCookie) {
		t.Error("a failed product read recorded a last-viewed product")
	}
}

// An un-prefixed page is the opposite case, and the reason the two
// resolutions are separate functions: a failed product read must not take
// down a page whose body was already serviceable. The credentials page in
// particular must still mint a token.
func TestUnprefixedPageSurvivesAFailedProductRead(t *testing.T) {
	app := newTestApp(t)
	app.spec = scopedProductsReader{specReadClient: &fakeSpecReader{},
		productsErr: errors.New("product list unavailable")}
	app.scopes = productScopeScopes{scope: store.Scope{ID: uuid.New()}}
	app.tasks = productScopeTasks{}
	app.credentials = &fakeCredentials{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)

	for _, target := range []string{"/", opsEscalatedPath, credentialsPath} {
		t.Run(target, func(t *testing.T) {
			rec := fetch(t, mux, target)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 -- a failed product read must not take down %s", rec.Code, target)
			}
			if strings.Contains(rec.Header().Get("Set-Cookie"), lastViewedProductCookie) {
				t.Error("a failed product read recorded a last-viewed product")
			}
		})
	}
}

// An htmx fragment swap renders no chrome, so it must not write the
// cookie either: a mid-page swap is not a page view, and setting a cookie
// on one would make the last-viewed product depend on which pane the
// operator happened to page.
func TestHtmxFragmentSwapDoesNotRecordALastViewedProduct(t *testing.T) {
	first := uuid.New()
	mux := productScopeMux(t, store.Product{ID: first, Name: "product A"})

	// The Needs attention page, which is where an htmx swap now happens:
	// the pre-redesign ops URLs answer a redirect whatever header they
	// carry, so the page-view/swap distinction lives on the page itself.
	req := httptest.NewRequest(http.MethodGet, productHref(first, needsAttentionSuffix), nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Header().Get("Set-Cookie"), lastViewedProductCookie) {
		t.Error("an htmx fragment swap recorded a last-viewed product")
	}
}

// The cookie the prefixed routes write must actually work: a browser that
// follows a product-scoped link and then a legacy one stays on the product
// the link named. Asserted end to end over two requests carrying one
// cookie, because a cookie that is written but never honoured is the
// failure mode both halves of this file exist to rule out.
func TestAPrefixedLinkCarriesTheProductToTheNextLegacyPage(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	mux := productScopeMux(t,
		store.Product{ID: a, Name: "product A"},
		store.Product{ID: b, Name: "product B"},
	)

	linked := fetch(t, mux, productHref(a, milestonesSuffix))
	followed := followWith(t, mux, opsEscalatedPath, lastViewedCookie(t, linked))

	if got := lastViewedCookie(t, followed).Value; got != a.String() {
		t.Errorf("legacy page after a linked one resolved %s, want the linked product %s", got, a)
	}
}
