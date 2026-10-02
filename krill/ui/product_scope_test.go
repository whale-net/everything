package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// scopedProductsReader is a fakeSpecReader whose Products call answers
// with a fixed list, so the resolver's in-scope decision comes from the
// test rather than a database. Every other read is inherited from
// fakeSpecReader, which already satisfies the interface.
type scopedProductsReader struct {
	*fakeSpecReader
	products []store.Product
}

func (r scopedProductsReader) Products(context.Context) ([]store.Product, error) {
	return r.products, nil
}

// productScopeMux mounts the real product-scoped routes against a reader
// holding the given products. The scope and task stores are stubbed too, so
// the un-prefixed ops views mounted alongside can render without one.
func productScopeMux(t *testing.T, products ...store.Product) *http.ServeMux {
	t.Helper()
	app := newTestApp(t)
	app.spec = scopedProductsReader{fakeSpecReader: &fakeSpecReader{}, products: products}
	app.scopes = productScopeScopes{scope: store.Scope{ID: uuid.New()}}
	app.tasks = productScopeTasks{}
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

func (productScopeTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, nil
}

func (productScopeTasks) ListOpenNotes(context.Context, store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	return store.Page[store.OpenNoteRow]{}, nil
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

// A product-scoped URL resolves the product it names and renders inside
// the shell. The rest of the acceptance matrix (cookie precedence,
// fallback, every sub-path) is the Testing phase's job.
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
			rec := fetch(t, mux, target)

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
	app.spec = scopedProductsReader{fakeSpecReader: &fakeSpecReader{}}
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
	app.spec = scopedProductsReader{fakeSpecReader: &fakeSpecReader{},
		products: []store.Product{{ID: uuid.New(), Name: "krill"}}}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)

	for _, a := range navAreas {
		rec := fetch(t, mux, a.Path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", a.Path, rec.Code)
			continue
		}
		if body := rec.Body.String(); strings.Contains(body, `name="product_id"`) {
			t.Errorf("GET %s asks the operator for a product id", a.Path)
		}
	}
}
