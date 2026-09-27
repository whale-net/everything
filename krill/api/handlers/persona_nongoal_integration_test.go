//go:build integration

// Real-Postgres coverage for the two spec-axis collection creates FR
// 7a3906a3 adds -- POST /personas and POST /non-goals. The unit tests
// (persona_test.go/nongoal_test.go) prove the handlers' validation and
// error mapping against fakes; this file proves the thing fakes cannot:
// that a caller holding a real gated krill session actually persists a row
// whose surrogate id is the one the response carried, that the returned
// ids are distinct per kind, that a real duplicate name is the same 409
// (backed by migration 002's real partial unique index, not a fabricated
// *pgconn.PgError), and that the same mux rejects an ungated POST with 401
// while writing nothing.
//
// The routes are mounted on a real http.ServeMux at the paths routes.go
// uses, so the method-and-path match is exercised too.
//
// Read-back: neither kind has an HTTP read route (GET /slices/... does not
// carry personas or non-goals -- see krill/ARCHITECTURE/29-open-items.md),
// so the returned id is read back through the store's own current-row read
// -- the same read MCP's list_personas/list_non_goals call. That is
// deliberately NOT a "the handler returned 201" check: the assertion is
// that the id in the response body is the id of a persisted row carrying
// the posted fields.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/api/handlers:persona_nongoal_integration_test --test_output=all
package handlers_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

func newPersonaNonGoalHTTPTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	pool, err := pgxpool.New(ctx, db.ConnString)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return store.New(pool), pool
}

// personaNonGoalTestSubject is the single self-acting subject every seed
// here records, mirroring taskClaimHTTPTestSubject's precedent.
func personaNonGoalTestSubject() store.Subject {
	return store.Subject{Iss: "https://issuer.example.com", Sub: "agent-1", Kind: store.SubjectKindService}
}

// personaNonGoalTestScope inserts a raw `scope` row -- ScopeStore itself
// exposes no Create.
func personaNonGoalTestScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "whale-net/persona-nongoal-http-test-"+uuid.NewString()).Scan(&scopeID))
	return scopeID
}

// personaNonGoalTestWorld stands up one Product plus a real session over
// it, and mounts POST /personas and POST /non-goals on a real mux exactly
// as api/routes.go does -- gate(handlers.CreateXxxHandler(...)) -- so every
// request below crosses the real session gate and the real store. Returns
// the store, that mux, the scope and Product ids to post under, and the
// session id to present.
func personaNonGoalTestWorld(t *testing.T, ctx context.Context) (*store.Store, *http.ServeMux, uuid.UUID, uuid.UUID, store.SessionID) {
	t.Helper()

	s, pool := newPersonaNonGoalHTTPTestStore(t)
	scopeID := personaNonGoalTestScope(t, ctx, pool)
	self := personaNonGoalTestSubject()

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	sessions := store.NewSessionStore(pool)
	sessionID, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)

	gate := handlers.RequireSession(sessions)
	mux := http.NewServeMux()
	mux.Handle("POST /personas", gate(handlers.CreatePersonaHandler(s.Personas())))
	mux.Handle("POST /non-goals", gate(handlers.CreateNonGoalHandler(s.NonGoals())))

	return s, mux, scopeID, product.ID, sessionID
}

// postJSON drives one POST through mux with the given session id header
// ("" omits it, the ungated case) and returns the recorder.
func postJSON(t *testing.T, mux *http.ServeMux, path, sessionIDHeader, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func createdID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp handlers.IDResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "body was %q", rec.Body.String())
	require.NotEmpty(t, resp.ID, "a 201 must carry a surrogate id (LB2)")
	return resp.ID
}

// TestCreatePersonaAndNonGoal_HTTP_PersistsRowsBehindRealSession is FR
// 7a3906a3's acceptance pin for the persona half: a real gated session
// POSTs a Persona, and the returned id reads back as a persisted row
// carrying exactly the posted fields under the session's scope.
func TestCreatePersonaAndNonGoal_HTTP_PersistsRowsBehindRealSession(t *testing.T) {
	ctx := context.Background()
	s, mux, scopeID, productID, sessionID := personaNonGoalTestWorld(t, ctx)

	rec := postJSON(t, mux, "/personas", sessionID.String(),
		`{"product_id": "`+productID.String()+`", "name": "Swarm Operator", "description": "runs the swarm"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	id, err := uuid.Parse(createdID(t, rec))
	require.NoError(t, err)

	persona, err := s.Personas().GetCurrentByID(ctx, id)
	require.NoError(t, err, "the id the response carried must be a readable current persona row")
	assert.Equal(t, scopeID, persona.ScopeID, "scope_id must come from the session, never the body (LB1)")
	assert.Equal(t, productID, persona.ProductID)
	assert.Equal(t, "Swarm Operator", persona.Name)
	require.NotNil(t, persona.Description)
	assert.Equal(t, "runs the swarm", *persona.Description)
}

// TestCreateNonGoal_HTTP_PersistsBothKinds is the same pin for the
// non-goal half, and additionally proves the `kind` discriminator survives
// the round trip: a `permanent` and a `deferred` Non-Goal are two distinct
// rows under one Product, not one row re-kinded.
func TestCreateNonGoal_HTTP_PersistsBothKinds(t *testing.T) {
	ctx := context.Background()
	s, mux, scopeID, productID, sessionID := personaNonGoalTestWorld(t, ctx)

	permanentRec := postJSON(t, mux, "/non-goals", sessionID.String(),
		`{"product_id": "`+productID.String()+`", "kind": "permanent", "name": "No second Product", "body": "one Product per repo"}`)
	require.Equal(t, http.StatusCreated, permanentRec.Code, permanentRec.Body.String())

	deferredRec := postJSON(t, mux, "/non-goals", sessionID.String(),
		`{"product_id": "`+productID.String()+`", "kind": "deferred", "name": "No second Slice", "body": "maybe later"}`)
	require.Equal(t, http.StatusCreated, deferredRec.Code, deferredRec.Body.String())

	permanentID, err := uuid.Parse(createdID(t, permanentRec))
	require.NoError(t, err)
	deferredID, err := uuid.Parse(createdID(t, deferredRec))
	require.NoError(t, err)
	assert.NotEqual(t, permanentID, deferredID, "each Non-Goal is returned with its OWN surrogate id")

	permanent, err := s.NonGoals().GetCurrentByID(ctx, permanentID)
	require.NoError(t, err)
	assert.Equal(t, store.NonGoalKindPermanent, permanent.Kind)
	assert.Equal(t, scopeID, permanent.ScopeID)
	assert.Equal(t, productID, permanent.ProductID)
	assert.Equal(t, "No second Product", permanent.Name)

	deferred, err := s.NonGoals().GetCurrentByID(ctx, deferredID)
	require.NoError(t, err)
	assert.Equal(t, store.NonGoalKindDeferred, deferred.Kind)
	assert.Equal(t, "No second Slice", deferred.Name)
}

// TestCreatePersonaAndNonGoal_HTTP_UngatedPost_Rejected is the other half
// of the acceptance pin (NFR6): the same mux rejects both POSTs carrying no
// session header, with a 401 that writes nothing -- not merely a 401 on a
// table that happens to be empty for another reason.
func TestCreatePersonaAndNonGoal_HTTP_UngatedPost_Rejected(t *testing.T) {
	ctx := context.Background()
	s, mux, _, productID, _ := personaNonGoalTestWorld(t, ctx)

	personaRec := postJSON(t, mux, "/personas", "",
		`{"product_id": "`+productID.String()+`", "name": "Swarm Operator"}`)
	assert.Equal(t, http.StatusUnauthorized, personaRec.Code, personaRec.Body.String())

	nonGoalRec := postJSON(t, mux, "/non-goals", "",
		`{"product_id": "`+productID.String()+`", "kind": "permanent", "name": "No second Product"}`)
	assert.Equal(t, http.StatusUnauthorized, nonGoalRec.Code, nonGoalRec.Body.String())

	personas, err := s.Personas().ListCurrentByProduct(ctx, productID)
	require.NoError(t, err)
	assert.Empty(t, personas, "a rejected ungated POST must write no persona row")

	nonGoals, err := s.NonGoals().ListCurrentByProduct(ctx, productID)
	require.NoError(t, err)
	assert.Empty(t, nonGoals, "a rejected ungated POST must write no non_goal row")
}

// TestCreatePersonaAndNonGoal_HTTP_DuplicateName_Returns409 proves the
// 409 the acceptance criteria name is the real one -- migration 002's
// partial unique index, not a fabricated *pgconn.PgError as the fakes
// stand in for: POSTing the same name twice under the same Product is
// refused with 409, and the first row is untouched.
func TestCreatePersonaAndNonGoal_HTTP_DuplicateName_Returns409(t *testing.T) {
	ctx := context.Background()
	s, mux, _, productID, sessionID := personaNonGoalTestWorld(t, ctx)

	first := postJSON(t, mux, "/personas", sessionID.String(),
		`{"product_id": "`+productID.String()+`", "name": "Swarm Operator"}`)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())

	second := postJSON(t, mux, "/personas", sessionID.String(),
		`{"product_id": "`+productID.String()+`", "name": "Swarm Operator"}`)
	assert.Equal(t, http.StatusConflict, second.Code, second.Body.String())

	firstNG := postJSON(t, mux, "/non-goals", sessionID.String(),
		`{"product_id": "`+productID.String()+`", "kind": "permanent", "name": "No second Product"}`)
	require.Equal(t, http.StatusCreated, firstNG.Code, firstNG.Body.String())

	// Same name, opposite kind: the index is on (scope, product,
	// lower(name)), not on kind, so this is still a conflict.
	secondNG := postJSON(t, mux, "/non-goals", sessionID.String(),
		`{"product_id": "`+productID.String()+`", "kind": "deferred", "name": "no second product"}`)
	assert.Equal(t, http.StatusConflict, secondNG.Code, secondNG.Body.String())

	personas, err := s.Personas().ListCurrentByProduct(ctx, productID)
	require.NoError(t, err)
	assert.Len(t, personas, 1, "the refused create must not have added a second row")

	nonGoals, err := s.NonGoals().ListCurrentByProduct(ctx, productID)
	require.NoError(t, err)
	assert.Len(t, nonGoals, 1, "the refused create must not have added a second row")
}

// TestCreatePersonaAndNonGoal_HTTP_InvalidNonGoalKind_WritesNothing proves
// the discriminator is enforced against the real store, not only against a
// fake: a third `kind` is a 400 and no row lands.
func TestCreatePersonaAndNonGoal_HTTP_InvalidNonGoalKind_WritesNothing(t *testing.T) {
	ctx := context.Background()
	s, mux, _, productID, sessionID := personaNonGoalTestWorld(t, ctx)

	rec := postJSON(t, mux, "/non-goals", sessionID.String(),
		`{"product_id": "`+productID.String()+`", "kind": "retired", "name": "No second Product"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	nonGoals, err := s.NonGoals().ListCurrentByProduct(ctx, productID)
	require.NoError(t, err)
	assert.Empty(t, nonGoals)
}

// TestCreatePersonaAndNonGoal_HTTP_UnknownProduct_Returns400 proves a
// missing (or cross-scope) parent is the 400 every other create returns,
// not a 500 from the store's parent-existence check.
func TestCreatePersonaAndNonGoal_HTTP_UnknownProduct_Returns400(t *testing.T) {
	ctx := context.Background()
	_, mux, _, _, sessionID := personaNonGoalTestWorld(t, ctx)

	personaRec := postJSON(t, mux, "/personas", sessionID.String(),
		`{"product_id": "`+uuid.NewString()+`", "name": "Swarm Operator"}`)
	assert.Equal(t, http.StatusBadRequest, personaRec.Code, personaRec.Body.String())

	nonGoalRec := postJSON(t, mux, "/non-goals", sessionID.String(),
		`{"product_id": "`+uuid.NewString()+`", "kind": "permanent", "name": "No second Product"}`)
	assert.Equal(t, http.StatusBadRequest, nonGoalRec.Code, nonGoalRec.Body.String())
}
