package main

import (
	"net/http"
)

// noRedirectWriter is a non-redirecting ResponseWriter shim that wraps an
// http.ResponseWriter and prevents redirect responses from being sent. It is
// used to wrap the SSE handler so that auth failures never emit a 3xx status
// or Location header (which would violate FR28's "no redirect, ever" constraint
// for streaming responses).
//
// Four load-bearing properties:
// (i)   WriteHeader intercepts 3xx and converts to 401, deleting Location
// (ii)  Suppresses the body that follows a converted redirect
// (iii) Delegates Flush() — a missing Flush() breaks FR2's flush contract
//       and breaks recorder-based tests by buffering
// (iv)  Implements Unwrap() — ResponseController methods like SetWriteDeadline
//       resolve only through Unwrap (FR17 prototype for lifting into htmxauth)
type noRedirectWriter struct {
	w              http.ResponseWriter
	headerWritten  bool
	redirected     bool // tracks if we converted a redirect to 401
}

// newNoRedirectWriter creates a new shim wrapping the given ResponseWriter.
func newNoRedirectWriter(w http.ResponseWriter) *noRedirectWriter {
	return &noRedirectWriter{w: w}
}

// Header returns the header map from the wrapped writer.
func (nrw *noRedirectWriter) Header() http.Header {
	return nrw.w.Header()
}

// WriteHeader intercepts the status code. On a 3xx response:
// (i)  Delete the Location header from the map
// (ii) Write 401 instead of the redirect status
// Otherwise write the status as-is.
func (nrw *noRedirectWriter) WriteHeader(statusCode int) {
	if nrw.headerWritten {
		return
	}
	nrw.headerWritten = true

	// Check if this is a redirect response (3xx)
	if statusCode >= 300 && statusCode < 400 {
		// (i) Delete Location from the header map
		nrw.w.Header().Del("Location")
		// (ii) Delete Content-Type (http.Redirect writes an <a> document)
		nrw.w.Header().Del("Content-Type")
		// Convert to 401 and mark as redirected so Write() suppresses body
		nrw.redirected = true
		nrw.w.WriteHeader(http.StatusUnauthorized)
		return
	}

	nrw.w.WriteHeader(statusCode)
}

// Write writes data to the wrapped writer. If a redirect was converted to 401,
// suppress the body (property ii) — convert will delete Content-Type along with
// Location, so any body written here is orphaned.
func (nrw *noRedirectWriter) Write(b []byte) (int, error) {
	// Ensure WriteHeader has been called
	if !nrw.headerWritten {
		nrw.WriteHeader(http.StatusOK)
	}

	if nrw.redirected {
		// (ii) Suppress body after converted redirect
		return len(b), nil // pretend we wrote it, but discard
	}

	return nrw.w.Write(b)
}

// Flush delegates to the wrapped writer's Flusher (property iii).
// (iii) Implement and delegate Flush(). A wrapping ResponseWriter that drops
// Flush() breaks FR2's flush contract and breaks recorder-based tests by buffering.
func (nrw *noRedirectWriter) Flush() {
	if flusher, ok := nrw.w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter (property iv).
// (iv) Implement Unwrap() http.ResponseWriter. ResponseController methods like
// SetWriteDeadline resolve only through Unwrap.
func (nrw *noRedirectWriter) Unwrap() http.ResponseWriter {
	return nrw.w
}
