package main

import (
	"context"
	"net/http"
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
// holding the given products.
func productScopeMux(t *testing.T, products ...store.Product) *http.ServeMux {
	t.Helper()
	app := newTestApp(t)
	app.spec = scopedProductsReader{fakeSpecReader: &fakeSpecReader{}, products: products}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
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
