// Unit tests for InitSessionHandler: identity and scope come from the
// verified caller and the deployment, never the request body.
package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/caller"
	"github.com/whale-net/everything/krill/store"
)

type soleScope struct {
	id  uuid.UUID
	err error
}

func (s soleScope) GetByID(context.Context, uuid.UUID) (store.Scope, error) {
	return store.Scope{ID: s.id}, s.err
}
func (s soleScope) GetSole(context.Context) (store.Scope, error) {
	return store.Scope{ID: s.id}, s.err
}

func verified(ok bool) handlers.IdentityFunc {
	return func(*http.Request) (caller.Identity, bool) {
		return caller.Identity{
			Acting:           caller.Subject{Iss: "https://kc", Sub: "agent-1", Kind: "service"},
			OnBehalfOf:       caller.Subject{Iss: "https://kc", Sub: "human-1", Kind: "human"},
			WhagentSessionID: "ws-1",
		}, ok
	}
}

func doInit(sessions *fakeSessionStore, scopes soleScope, id handlers.IdentityFunc, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/sessions/init", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handlers.InitSessionHandler(sessions, scopes, id).ServeHTTP(rec, req)
	return rec
}

func TestInitSessionHandler_DerivesIdentityAndScope(t *testing.T) {
	sessions := newFakeSessionStore()
	scope := uuid.New()
	// A body asserting a different identity and scope must have no effect.
	body := `{"scope_id":"` + uuid.NewString() + `","acting":{"iss":"evil","sub":"root","kind":"human"}}`
	rec := doInit(sessions, soleScope{id: scope}, verified(true), body)
	require.Equal(t, http.StatusCreated, rec.Code)

	var resp handlers.InitSessionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, scope.String(), resp.ScopeID)
	sid, err := uuid.Parse(resp.SessionID)
	require.NoError(t, err)
	sess, err := sessions.GetSession(context.Background(), store.SessionID(sid))
	require.NoError(t, err)
	assert.Equal(t, scope, sess.ScopeID)
	assert.Equal(t, "agent-1", sess.Acting.Sub)
	assert.Equal(t, "human-1", sess.OnBehalfOf.Sub)
	require.NotNil(t, sess.WhagentSessionID)
	assert.Equal(t, "ws-1", *sess.WhagentSessionID)
}

func TestInitSessionHandler_NoVerifiedCaller(t *testing.T) {
	sessions := newFakeSessionStore()
	rec := doInit(sessions, soleScope{id: uuid.New()}, verified(false), `{}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, sessions.sessions)
}

func TestInitSessionHandler_ScopeUnresolvable(t *testing.T) {
	sessions := newFakeSessionStore()
	rec := doInit(sessions, soleScope{err: errors.New("many")}, verified(true), `{}`)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Empty(t, sessions.sessions)
}

func TestInitSessionHandler_StoreError(t *testing.T) {
	sessions := newFakeSessionStore()
	sessions.initErr = errors.New("db")
	rec := doInit(sessions, soleScope{id: uuid.New()}, verified(true), `{}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestInitSessionHandler_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/sessions/init", nil)
	rec := httptest.NewRecorder()
	handlers.InitSessionHandler(newFakeSessionStore(), soleScope{}, verified(true)).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
