//go:build integration

// Real-Postgres coverage for ClaimTaskHandler's full success path
// (task_claim.go, issue #2722's Testing section) -- the branches
// task_claim_test.go's fakes cannot reach, since work.Assembler wraps a
// concrete *slice.Querier over a real *store.Store, not an interface a
// fake can stand in for (mirrors task_payload_integration_test.go's own
// doc comment on the identical limitation): a genuine 200 carrying a real
// minted lease (FR3/FR5), and the claim response's document shape matching
// a subsequent GET /tasks/{id} exactly (FR4/FR10's single-type check,
// LB7/NFR4) -- the one scenario in this issue's Testing section that needs
// two real HTTP round trips against the same persisted state to prove.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/api/handlers:task_claim_integration_test --test_output=all
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

func newTaskClaimHTTPTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
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

// taskClaimHTTPTestScope inserts a raw `scope` row, mirroring
// taskPayloadHTTPTestScope's own precedent (ScopeStore itself exposes no
// Create).
func taskClaimHTTPTestScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "whale-net/task-claim-http-test-"+uuid.NewString()).Scan(&scopeID))
	return scopeID
}

func taskClaimHTTPTestSubject() store.Subject {
	return store.Subject{Iss: "https://issuer.example.com", Sub: "agent-1", Kind: store.SubjectKindService}
}

// seedTaskClaimHTTPWorld seeds a Product -> FeatureSet -> Feature, a
// milestone that delivers the Feature, a milepebble cut from it that also
// delivers the Feature, and one task scoped to that milepebble with no
// declared dependencies (vacuously "all dependencies Done") -- the minimum
// fixture ClaimTaskHandler's success path needs, mirroring
// seedTaskPayloadHTTPWorld's own precedent.
func seedTaskClaimHTTPWorld(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID, self store.Subject) (taskID, featureID uuid.UUID) {
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

// TestClaimTaskHandler_Success_ReturnsPayloadWithLease is issue #2722's
// Testing section item 1 exercised through the real HTTP handler: claiming
// an unclaimed task with all dependencies Done succeeds and returns the
// real assembled payload -- the slice portion carries the milepebble's own
// delivered Feature, and the TaskView carries the fresh lease/claim state
// (FR3/FR5/FR10), never a hand-built claim response (NFR4/LB7).
func TestClaimTaskHandler_Success_ReturnsPayloadWithLease(t *testing.T) {
	ctx := context.Background()
	s, pool := newTaskClaimHTTPTestStore(t)
	scopeID := taskClaimHTTPTestScope(t, ctx, pool)
	self := taskClaimHTTPTestSubject()
	taskID, featureID := seedTaskClaimHTTPWorld(t, ctx, s, scopeID, self)

	sessions := store.NewSessionStore(pool)
	sessionID, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	gated := handlers.RequireSession(sessions)(handlers.ClaimTaskHandler(s.Tasks(), assembler))
	mux := http.NewServeMux()
	mux.Handle("POST /tasks/{id}/claim", gated)

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID.String()+"/claim", nil)
	req.Header.Set("X-Krill-Session-Id", sessionID.String())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload work.Payload
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	assert.Equal(t, taskID, payload.Task.ID)
	require.Len(t, payload.Slice.Features, 1)
	assert.Equal(t, featureID, payload.Slice.Features[0].ID)

	require.NotNil(t, payload.Task.CurrentClaim, "a successful claim must populate the payload's lease state")
	assert.Equal(t, uuid.UUID(sessionID), payload.Task.CurrentClaim.SessionID)
	assert.False(t, payload.Task.CurrentClaim.Released)
	assert.Nil(t, payload.Task.CurrentClaim.ReleaseReason)
	assert.True(t, payload.Task.CurrentClaim.LeaseExpiresAt.After(payload.Task.CurrentClaim.ClaimedAt), "the lease must expire after it was claimed")
	assert.Equal(t, 1, payload.Task.AttemptNumber, "a successful claim must record exactly one attempt")
}

// TestClaimTaskHandler_ClaimResponseMatchesSubsequentGet is issue #2722's
// Testing section item 9 (FR4/FR10 single-type check): the claim response
// and a subsequent GET /tasks/{id} return the exact same document -- one
// payload type, never a claim-specific response shape (NFR4/LB7).
func TestClaimTaskHandler_ClaimResponseMatchesSubsequentGet(t *testing.T) {
	ctx := context.Background()
	s, pool := newTaskClaimHTTPTestStore(t)
	scopeID := taskClaimHTTPTestScope(t, ctx, pool)
	self := taskClaimHTTPTestSubject()
	taskID, _ := seedTaskClaimHTTPWorld(t, ctx, s, scopeID, self)

	sessions := store.NewSessionStore(pool)
	sessionID, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	mux := http.NewServeMux()
	mux.Handle("POST /tasks/{id}/claim", handlers.RequireSession(sessions)(handlers.ClaimTaskHandler(s.Tasks(), assembler)))
	mux.HandleFunc("GET /tasks/{id}", handlers.GetTaskPayloadHandler(s.Tasks(), assembler))

	claimReq := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID.String()+"/claim", nil)
	claimReq.Header.Set("X-Krill-Session-Id", sessionID.String())
	claimRec := httptest.NewRecorder()
	mux.ServeHTTP(claimRec, claimReq)
	require.Equal(t, http.StatusOK, claimRec.Code, claimRec.Body.String())

	getReq := httptest.NewRequest(http.MethodGet, "/tasks/"+taskID.String(), nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())

	var claimPayload, getPayload work.Payload
	require.NoError(t, json.Unmarshal(claimRec.Body.Bytes(), &claimPayload))
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &getPayload))

	assert.Equal(t, claimPayload, getPayload, "the claim response and a subsequent GET /tasks/{id} must return the exact same document")
}
