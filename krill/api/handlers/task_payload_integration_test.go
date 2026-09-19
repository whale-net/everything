//go:build integration

// Real-Postgres coverage for GetTaskPayloadHandler's full success path
// (task_payload.go, issue #2721's Testing section) -- the one branch
// task_payload_test.go's fakes cannot reach, since work.Assembler wraps a
// concrete *slice.Querier over a real *store.Store, not an interface a
// fake can stand in for. Mirrors krill/work/payload_integration_test.go's
// own dbtest/migration/seeding harness rather than duplicating it in
// spirit (a smaller, HTTP-focused fixture here, since the slice-content
// assertions themselves are krill/work's own job).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/api/handlers:task_payload_integration_test --test_output=all
package handlers_test

import (
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
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

func newTaskPayloadHTTPTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
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

// taskPayloadHTTPTestScope inserts a raw `scope` row -- ScopeStore itself
// exposes no Create (scopes are minted by the importer path, not this
// package's own tests), mirroring every other integration test's own
// createScope helper (e.g. krill/slice/query_integration_test.go's).
func taskPayloadHTTPTestScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "whale-net/task-payload-http-test-"+uuid.NewString()).Scan(&scopeID))
	return scopeID
}

func taskPayloadHTTPTestSubject() store.Subject {
	return store.Subject{Iss: "https://issuer.example.com", Sub: "agent-1", Kind: store.SubjectKindService}
}

// seedTaskPayloadHTTPWorld seeds a Product -> FeatureSet -> Feature, a
// milestone that delivers the Feature, a milepebble cut from it that also
// delivers the Feature, and one task scoped to that milepebble -- the
// minimum fixture GetTaskPayloadHandler's success path needs.
func seedTaskPayloadHTTPWorld(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID, self store.Subject) (taskID, featureID uuid.UUID) {
	t.Helper()

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship it", nil, self, self)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, feature.ID, self, self))

	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "MP1", "a slice of M1", self, self)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, milepebble.ID, feature.ID, self, self))

	task, err := s.Tasks().CreateTask(ctx, store.CreateTaskParams{
		ScopeID:      scopeID,
		MilestoneID:  milepebble.ID,
		Title:        "do the thing",
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation, store.LaneTesting, store.LaneValidation, store.LaneDone},
		StartingLane: store.LaneScaffold,
		Acting:       self,
		OnBehalfOf:   self,
	})
	require.NoError(t, err)

	return task.ID, feature.ID
}

// TestGetTaskPayloadHandler_Success_NoSessionHeader_ReturnsRealDocument is
// issue #2721's Testing section: GET /tasks/{id} succeeds with no session
// header at all (FR10/NFR6) and returns the real assembled payload
// document -- the milepebble's own delivered Feature is present in the
// embedded slice.
//
// This is the "unclaimed" case named in this issue's Testing section --
// there is no claim/lease state on Payload/TaskView yet (that lands with
// #2722), so every task this milestone's Assemble can produce today is,
// definitionally, "unclaimed." The "claimed by someone else" and
// "previously abandoned" cases from the same bullet cannot be exercised
// until #2722 (claim/lease) and #2726 (abandon) exist; whichever of those
// tasks lands next should extend this test with those two states rather
// than re-deriving this fixture.
func TestGetTaskPayloadHandler_Success_NoSessionHeader_ReturnsRealDocument(t *testing.T) {
	ctx := context.Background()
	s, pool := newTaskPayloadHTTPTestStore(t)
	scopeID := taskPayloadHTTPTestScope(t, ctx, pool)
	self := taskPayloadHTTPTestSubject()
	taskID, featureID := seedTaskPayloadHTTPWorld(t, ctx, s, scopeID, self)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks/{id}", handlers.GetTaskPayloadHandler(s.Tasks(), assembler))

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+taskID.String(), nil)
	require.Empty(t, req.Header.Get("X-Krill-Session-Id"), "this read must succeed with no session header (FR10/NFR6)")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload work.Payload
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	assert.Equal(t, taskID, payload.Task.ID)
	require.Len(t, payload.Slice.Features, 1)
	assert.Equal(t, featureID, payload.Slice.Features[0].ID)
	assert.NotEmpty(t, payload.Slice.SchemaVersion)
}

// TestGetTaskPayloadHandler_RealTaskResolves_UnknownIDStill404 is issue
// #2721's Testing section's "unknown task id fails loud and named" bullet,
// exercised end to end through the real HTTP handler and a real store
// (task_payload_test.go's own fake-backed coverage of the same shape
// proves the error-mapping wiring; this proves it holds against a real
// Postgres-backed Assemble too). Two scopes are seeded side by side --
// never just one -- so a positive 200 here isn't proof of an empty
// database being vacuously fine.
//
// This is not itself NFR1's cross-scope proof: GetTaskPayloadHandler
// resolves {id}'s scope from the task row itself (task_payload.go's own
// doc comment -- it has no session to compare against), so a task id can
// never be handed a foreign scope from outside an HTTP request the way a
// scope-qualified read can. NFR1's actual guarantee -- Assemble rejecting
// a taskID/scopeID pair that mismatch -- is krill/work/payload_test.go's
// TestAssemble_CrossScopeTaskID_NotFound.
func TestGetTaskPayloadHandler_RealTaskResolves_UnknownIDStill404(t *testing.T) {
	ctx := context.Background()
	s, pool := newTaskPayloadHTTPTestStore(t)
	self := taskPayloadHTTPTestSubject()

	scopeA := taskPayloadHTTPTestScope(t, ctx, pool)
	taskID, _ := seedTaskPayloadHTTPWorld(t, ctx, s, scopeA, self)

	scopeB := taskPayloadHTTPTestScope(t, ctx, pool)
	_, _ = seedTaskPayloadHTTPWorld(t, ctx, s, scopeB, self)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks/{id}", handlers.GetTaskPayloadHandler(s.Tasks(), assembler))

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+taskID.String(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String(), "a real task id must still resolve correctly")

	req = httptest.NewRequest(http.MethodGet, "/tasks/"+uuid.New().String(), nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String(), "an unknown task id must fail loud as 404, never 500 or an empty 200")
}
