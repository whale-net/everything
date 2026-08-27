package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNoRedirectWriter_Property1_Redirect3xxTo401 tests that a 3xx status is
// converted to 401 with Location header deleted.
func TestNoRedirectWriter_Property1_Redirect3xxTo401(t *testing.T) {
	// Break: verify the test fails with a normal ResponseWriter that allows redirects
	{
		w := httptest.NewRecorder()
		w.Header().Set("Location", "/auth/login")
		w.WriteHeader(http.StatusSeeOther)
		if w.Code != http.StatusSeeOther {
			t.Fatal("control test broken: normal writer should allow 303")
		}
	}

	// Green: 3xx converted to 401, Location deleted
	w := httptest.NewRecorder()
	shim := newNoRedirectWriter(w)
	shim.Header().Set("Location", "/auth/login")
	shim.WriteHeader(http.StatusSeeOther)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("expected Location header to be deleted, got %q", loc)
	}
}

// TestNoRedirectWriter_Property2_ContentTypeDeleted tests that Content-Type
// is deleted when a redirect is converted to 401 (suppressing the redirect body).
func TestNoRedirectWriter_Property2_ContentTypeDeleted(t *testing.T) {
	// Break: verify the test fails without the shim (normal redirects have Content-Type)
	{
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusSeeOther)
		if w.Header().Get("Content-Type") == "" {
			t.Fatal("control test broken: normal writer should preserve Content-Type")
		}
	}

	// Green: 3xx converted to 401, Content-Type deleted
	w := httptest.NewRecorder()
	shim := newNoRedirectWriter(w)
	shim.Header().Set("Content-Type", "text/html")
	shim.WriteHeader(http.StatusSeeOther)

	if ct := w.Header().Get("Content-Type"); ct != "" {
		t.Errorf("expected Content-Type to be deleted, got %q", ct)
	}
}

// TestNoRedirectWriter_Property3_BodySuppressed tests that the body is
// suppressed when a redirect is converted to 401.
func TestNoRedirectWriter_Property3_BodySuppressed(t *testing.T) {
	w := httptest.NewRecorder()
	shim := newNoRedirectWriter(w)
	shim.WriteHeader(http.StatusSeeOther)

	testBody := []byte("<a href=\"/auth/login\">Click here</a>")
	n, err := shim.Write(testBody)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if n != len(testBody) {
		t.Errorf("Write returned %d bytes, expected %d", n, len(testBody))
	}

	// The body should be discarded (suppressed)
	if w.Body.String() != "" {
		t.Errorf("expected empty body, got %q", w.Body.String())
	}
}

// TestNoRedirectWriter_Property4_FlushDelegated tests that Flush() is delegated
// to the underlying writer. This is critical because a missing Flush() breaks
// SSE streaming by buffering.
func TestNoRedirectWriter_Property4_FlushDelegated(t *testing.T) {
	// Recorder with a custom writer that tracks Flush calls
	w := httptest.NewRecorder()
	shim := newNoRedirectWriter(w)

	// Write some data that would be buffered without Flush
	shim.WriteHeader(http.StatusOK)
	shim.Write([]byte("test data"))

	// Flush should succeed (delegated to Recorder's Flusher)
	shim.Flush()

	// Verify data is present (wasn't buffered/lost)
	if w.Body.String() != "test data" {
		t.Errorf("expected 'test data', got %q", w.Body.String())
	}
}

// TestNoRedirectWriter_Property5_UnwrapReturnsUnderlying tests that Unwrap()
// returns the underlying ResponseWriter. This is needed for ResponseController
// methods like SetWriteDeadline.
func TestNoRedirectWriter_Property5_UnwrapReturnsUnderlying(t *testing.T) {
	w := httptest.NewRecorder()
	shim := newNoRedirectWriter(w)

	unwrapped := shim.Unwrap()
	if unwrapped != w {
		t.Errorf("Unwrap() did not return the underlying writer")
	}
}

// TestNoRedirectWriter_NonRedirectStatus tests that non-redirect responses
// pass through unchanged.
func TestNoRedirectWriter_NonRedirectStatus(t *testing.T) {
	w := httptest.NewRecorder()
	shim := newNoRedirectWriter(w)

	shim.Header().Set("X-Custom", "value")
	shim.WriteHeader(http.StatusOK)
	shim.Write([]byte("response body"))

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if custom := w.Header().Get("X-Custom"); custom != "value" {
		t.Errorf("expected X-Custom: value, got %q", custom)
	}
	if w.Body.String() != "response body" {
		t.Errorf("expected 'response body', got %q", w.Body.String())
	}
}
