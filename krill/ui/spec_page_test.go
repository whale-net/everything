package main

import (
	"net/http"
	"strings"
	"testing"
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
