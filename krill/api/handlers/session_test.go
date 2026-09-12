// Unit tests for InitSessionHandler (session.go, issue #2489's Testing
// section): success mints a session id and records both subjects, and
// per-field validation failures reject with 400 before ever reaching the
// store. No Postgres dependency -- fakeSessionStore (fake_session_store_test.go)
// stands in for store.SessionStore.
package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

func doInit(t *testing.T, sessions *fakeSessionStore, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/init", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handlers.InitSessionHandler(sessions).ServeHTTP(rec, req)
	return rec
}

// TestInitSessionHandler_Success proves a well-formed request mints a
// session id distinct from the request's own whagent_session_id and
// records both subjects on the store exactly as sent (LB4).
func TestInitSessionHandler_Success(t *testing.T) {
	sessions := newFakeSessionStore()
	scopeID := uuid.New()
	whagentSessionID := uuid.NewString()

	body := `{
		"scope_id": "` + scopeID.String() + `",
		"acting": {"iss": "https://issuer.example.com", "sub": "agent-1", "kind": "service"},
		"on_behalf_of": {"iss": "https://issuer.example.com", "sub": "human-1", "kind": "human"},
		"whagent_session_id": "` + whagentSessionID + `"
	}`

	rec := doInit(t, sessions, body)
	require.Equal(t, http.StatusCreated, rec.Code)

	var resp struct {
		SessionID string `json:"session_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.SessionID)
	assert.NotEqual(t, whagentSessionID, resp.SessionID, "the minted krill session id must never equal the whagent session id")

	id, err := uuid.Parse(resp.SessionID)
	require.NoError(t, err)

	sess, err := sessions.GetSession(context.Background(), store.SessionID(id))
	require.NoError(t, err)
	assert.Equal(t, "agent-1", sess.Acting.Sub)
	assert.Equal(t, "human-1", sess.OnBehalfOf.Sub)
	require.NotNil(t, sess.WhagentSessionID)
	assert.Equal(t, whagentSessionID, *sess.WhagentSessionID)
}

// TestInitSessionHandler_SuccessWithoutWhagentClaim proves the human/OAuth2
// shape: omitting whagent_session_id entirely still succeeds.
func TestInitSessionHandler_SuccessWithoutWhagentClaim(t *testing.T) {
	sessions := newFakeSessionStore()
	scopeID := uuid.New()

	body := `{
		"scope_id": "` + scopeID.String() + `",
		"acting": {"iss": "https://issuer.example.com", "sub": "human-1", "kind": "human"},
		"on_behalf_of": {"iss": "https://issuer.example.com", "sub": "human-1", "kind": "human"}
	}`

	rec := doInit(t, sessions, body)
	require.Equal(t, http.StatusCreated, rec.Code)
}

// TestInitSessionHandler_ValidationFailures proves each documented rejection
// (parseSubject/session.go) returns 400 without ever calling InitSession.
func TestInitSessionHandler_ValidationFailures(t *testing.T) {
	scopeID := uuid.New().String()
	validActing := `{"iss": "https://issuer.example.com", "sub": "agent-1", "kind": "service"}`

	cases := map[string]string{
		"malformed json": `{not json`,
		"missing scope_id": `{
			"acting": ` + validActing + `,
			"on_behalf_of": ` + validActing + `
		}`,
		"invalid scope_id uuid": `{
			"scope_id": "not-a-uuid",
			"acting": ` + validActing + `,
			"on_behalf_of": ` + validActing + `
		}`,
		"missing acting.iss": `{
			"scope_id": "` + scopeID + `",
			"acting": {"sub": "agent-1", "kind": "service"},
			"on_behalf_of": ` + validActing + `
		}`,
		"missing acting.sub": `{
			"scope_id": "` + scopeID + `",
			"acting": {"iss": "https://issuer.example.com", "kind": "service"},
			"on_behalf_of": ` + validActing + `
		}`,
		"invalid acting.kind": `{
			"scope_id": "` + scopeID + `",
			"acting": {"iss": "https://issuer.example.com", "sub": "agent-1", "kind": "robot"},
			"on_behalf_of": ` + validActing + `
		}`,
		"missing on_behalf_of": `{
			"scope_id": "` + scopeID + `",
			"acting": ` + validActing + `,
			"on_behalf_of": {"iss": "https://issuer.example.com", "sub": "", "kind": "human"}
		}`,
		"unknown field": `{
			"scope_id": "` + scopeID + `",
			"acting": ` + validActing + `,
			"on_behalf_of": ` + validActing + `,
			"unexpected_field": true
		}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			sessions := newFakeSessionStore()
			rec := doInit(t, sessions, body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "expected 400 for case %q, got body %q", name, rec.Body.String())
			assert.Empty(t, sessions.sessions, "a validation failure must never reach InitSession")
		})
	}
}

// TestInitSessionHandler_MethodNotAllowed proves GET is rejected.
func TestInitSessionHandler_MethodNotAllowed(t *testing.T) {
	sessions := newFakeSessionStore()
	req := httptest.NewRequest(http.MethodGet, "/sessions/init", nil)
	rec := httptest.NewRecorder()
	handlers.InitSessionHandler(sessions).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
