// Shared request-driving helpers for product_test.go/featureset_test.go/
// feature_test.go/requirement_test.go/decision_test.go (issue #2490's
// Testing section): every entity create/attach handler must be exercised
// wrapped in handlers.RequireSession (gate.go), exactly as routes.go wires
// them, so a handler-level test also proves the write gate is actually in
// front of it -- not just that the handler works when called directly.
package handlers_test

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// uniqueViolationErr stands in for a real Postgres unique-constraint
// violation (SQLSTATE 23505) -- the shape types.go's isUniqueViolation
// detects via errors.As -- without needing a database. See
// krill/store/entities_integration_test.go's
// TestCreateProduct_DuplicateNameInScope_IsUniqueViolation for the real
// error this fixture stands in for.
var uniqueViolationErr error = &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}

// genericStoreErr stands in for a genuine, unclassified store failure --
// writeStoreError (types.go) must map this to a 500, never a 400 or 409.
var genericStoreErr = errors.New("connection reset by peer")

// mustInitSession mints a session on sessions with the given scope and
// subjects, failing the test on error.
func mustInitSession(t *testing.T, sessions store.SessionStore, scopeID uuid.UUID, acting, onBehalfOf store.Subject) store.SessionID {
	t.Helper()
	id, err := sessions.InitSession(t.Context(), scopeID, acting, onBehalfOf, nil)
	require.NoError(t, err)
	return id
}

// doGatedRequest wraps handler in handlers.RequireSession(sessions) (the
// exact wiring routes.go uses for every entity create/attach endpoint) and
// drives one POST request through it. sessionIDHeader is the raw
// X-Krill-Session-Id header value to send; pass "" to omit the header
// entirely (the "no session id" rejection case).
func doGatedRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)

	req := httptest.NewRequest(http.MethodPost, "/some/write/endpoint", bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	return rec
}

// newTestSession mints a fakeSessionStore-backed session where the caller
// acts for itself (acting == on_behalf_of), returning the store, the
// scope_id the session carries, and the session id as a header-ready
// string -- the common case every entity create test needs. See
// product_test.go's TestCreateProductHandler_ActingDiffersFromOnBehalfOf_
// BothRecordedDistinctly for the differing-subjects case this helper
// deliberately does not cover.
func newTestSession(t *testing.T) (*fakeSessionStore, uuid.UUID, string) {
	t.Helper()
	sessions := newFakeSessionStore()
	scopeID := uuid.New()
	self := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-1", Kind: store.SubjectKindService}
	sessionID := mustInitSession(t, sessions, scopeID, self, self)
	return sessions, scopeID, sessionID.String()
}

// idResponseBody mirrors types.go's IDResponse (exported as of issue #2547
// so krill/mcp/tools can reuse it directly) -- this package's tests live
// in handlers_test (external test package) so they decode the wire shape
// via their own local struct rather than depending on IDResponse's own
// field set staying test-compatible, exactly like session_test.go's
// initSessionResponse-shaped anonymous struct.
type idResponseBody struct {
	ID string `json:"id"`
}
