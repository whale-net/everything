package youtube

// Shared test infrastructure for this package's test files: fixture
// loading (testdata/, checked into the repo per this task's Testing
// section), an httptest-backed Client constructor, and the two
// oauth2.TokenSource doubles used across errors_test.go, client_test.go,
// schedule_test.go, and metrics_test.go.
//
// No test in this package (or fake/) ever makes a live network call: every
// Client under test here is either built with WithHTTPClient pointed at an
// httptest.Server (newTestServer, newTestServerErr), or -- for the
// oauth2.RetrieveError wiring test in client_test.go -- built with a
// TokenSource whose Token() fails before golang.org/x/oauth2's Transport
// ever dials out (see client.go's "HTTP transport" doc comment).

import (
	"embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

//go:embed testdata
var testdataFS embed.FS

// mustLoadFixture reads testdata/name, failing the test immediately if it's
// missing -- every fixture used below is checked into testdata/.
func mustLoadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := testdataFS.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return b
}

// staticTokenSource is a trivial always-succeeds oauth2.TokenSource used by
// every test that exercises WithHTTPClient's override: the token itself is
// never inspected by the httptest server, since the request never leaves
// the process's own httptest.Server.
type staticTokenSource struct{}

func (staticTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "test-access-token"}, nil
}

// erroringTokenSource is used by the oauth2.RetrieveError wiring test in
// client_test.go, which deliberately does NOT use WithHTTPClient: it relies
// on golang.org/x/oauth2's Transport.RoundTrip calling Source.Token() and
// returning its error before attempting to dial any network connection, so
// this stays a zero-network-risk test despite not using an httptest server.
type erroringTokenSource struct{ err error }

func (e erroringTokenSource) Token() (*oauth2.Token, error) {
	return nil, e.err
}

// newTestServer starts an httptest.Server whose handlers are supplied by
// routes (URL path -> handler), and returns a Client wired to it via
// WithHTTPClient plus the package-internal withServiceOptions test seam
// (option.WithEndpoint) -- see client.go's WithHTTPClient doc comment. The
// server is closed via t.Cleanup.
func newTestServer(t *testing.T, routes map[string]http.HandlerFunc) Client {
	t.Helper()
	mux := http.NewServeMux()
	for path, h := range routes {
		mux.HandleFunc(path, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return New(staticTokenSource{},
		WithHTTPClient(srv.Client()),
		withServiceOptions(option.WithEndpoint(srv.URL)),
	)
}

// writeJSONFixture writes name's fixture bytes as a 200 JSON response body.
func writeJSONFixture(t *testing.T, w http.ResponseWriter, name string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(mustLoadFixture(t, name)); err != nil {
		t.Fatalf("write fixture response: %v", err)
	}
}

// writeAPIError writes a googleapi.Error-shaped JSON error envelope --
// {"error": {"code", "message", "errors": [{"reason","message"}]}} -- the
// exact shape googleapi.CheckResponseWithBody parses back into a
// *googleapi.Error, so classify (errors.go) sees the same structured error
// it would from a real Google API failure. reason populates
// Errors[0].Reason (the field hasQuotaReason inspects first); pass "" to
// omit it and exercise the message-substring fallback instead.
func writeAPIError(w http.ResponseWriter, status int, reason, message string) {
	type errorItem struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	type errorBody struct {
		Code    int         `json:"code"`
		Message string      `json:"message"`
		Errors  []errorItem `json:"errors,omitempty"`
	}
	type envelope struct {
		Error errorBody `json:"error"`
	}
	body := envelope{Error: errorBody{Code: status, Message: message}}
	if reason != "" {
		body.Error.Errors = []errorItem{{Reason: reason, Message: message}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
