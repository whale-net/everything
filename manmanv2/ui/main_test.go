package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFaviconRoute(t *testing.T) {
	mux := http.NewServeMux()
	app := &App{}
	app.setupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := w.Header().Get("Content-Type"); got != "image/x-icon" {
		t.Errorf("Content-Type = %q, want image/x-icon", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("Cache-Control = %q, want public, max-age=86400", got)
	}
	if len(w.Body.Bytes()) == 0 {
		t.Errorf("expected non-empty favicon body")
	}
}
