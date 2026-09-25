package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The streamable-HTTP transport's `GET <mount>` listening stream is a
// long-lived hang: it writes server->client notifications for as long as
// the session lives. net/http's WriteTimeout is an absolute deadline from
// the moment the request header was read, so a non-zero value makes every
// one of those writes fail once the deadline passes -- the handler keeps
// running and the client sees a stream that has silently gone deaf.
func TestNewHTTPServer_NoWriteTimeout(t *testing.T) {
	srv := newHTTPServer(":0", http.NotFoundHandler())
	assert.Zero(t, srv.WriteTimeout, "WriteTimeout breaks the long-lived SSE listening stream")
	assert.NotZero(t, srv.ReadTimeout, "request body reads should still be bounded")
}

// Same hazard, demonstrated rather than asserted -- this is why the guard
// above isn't cargo-cult. A short WriteTimeout is enough to break a
// listening stream, and the breakage is invisible from the handler's
// point of view: the handler is still running, it just can't write.
func TestWriteTimeoutBreaksListeningStream(t *testing.T) {
	const writeTimeout = 200 * time.Millisecond
	writes := make(chan error, 1)

	// Stands in for the transport's listening stream: open, then notify
	// long after the write deadline has passed. The Flush matters -- the
	// SDK writes each SSE event and flushes it (mcp.writeEvent), and
	// without the flush a small event would just sit in the bufio buffer
	// and be written out later, after the deadline is reset.
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rc := http.NewResponseController(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_ = rc.Flush()
		time.Sleep(3 * writeTimeout)
		_, err := fmt.Fprintln(w, "data: notification")
		if err == nil {
			// ResponseController.Flush surfaces the underlying write error,
			// which http.Flusher.Flush cannot (it returns nothing).
			err = rc.Flush()
		}
		writes <- err
	})

	ts := httptest.NewUnstartedServer(handler)
	ts.Config.WriteTimeout = writeTimeout
	ts.Start()
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL) //nolint:noctx // test-only request
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	select {
	case err := <-writes:
		assert.Error(t, err, "write past the WriteTimeout deadline must fail, deafening the stream")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the listening stream's write")
	}
}
