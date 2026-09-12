//go:build integration

// Real-Postgres coverage for PointerArtifactStore (migration 005, issue
// #2496, FR20, C9) -- the audit trail of the one thin GitHub issue krill
// mints per Product. See store_integration_test.go's package doc for why
// this file only builds under the "integration" build tag.
//
// This is its own go_test target (not folded into store_integration_test)
// because it is the one PointerArtifactStore test that also needs
// //krill/slice to prove FR8's whole-product-slice retrieval half of this
// issue's Testing section -- a dependency no other store-package test
// target carries.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:pointer_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newPointerTestStore provisions an isolated, migrated Postgres database
// (krill's own real embedded schema through 005_pointer_artifact) and
// returns a ready *store.Store plus the underlying dbtest.Postgres.
func newPointerTestStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return store.New(db.Pool), db
}

func newPointerTestScope(t *testing.T, ctx context.Context, db *dbtest.Postgres, repoFullName string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, repoFullName).Scan(&id))
	return id
}

func testSubject(sub string) store.Subject {
	return store.Subject{Iss: "https://issuer.example.com", Sub: sub, Kind: store.SubjectKindService}
}

// TestPointerArtifactStore_Create_AttachesToProductAndMirrorsScope proves
// Create mints a pointer_artifact row against the given Product, records
// both LB4 subjects, and mirrors the issue number onto
// scope.pointer_issue_number (LB1) in the same transaction.
func TestPointerArtifactStore_Create_AttachesToProductAndMirrorsScope(t *testing.T) {
	ctx := context.Background()
	s, db := newPointerTestStore(t)
	scopeID := newPointerTestScope(t, ctx, db, "whale-net/pointer-create-test")
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record substrate")
	require.NoError(t, err)

	acting := testSubject("agent-1")
	onBehalfOf := testSubject("human-1")

	artifact, err := s.PointerArtifacts().Create(ctx, scopeID, product.ID, 42, "https://github.com/whale-net/everything/issues/42", acting, onBehalfOf)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, artifact.ID)
	assert.Equal(t, product.ID, artifact.ProductID)
	assert.Equal(t, scopeID, artifact.ScopeID)
	assert.Equal(t, "github_issue", artifact.Kind)
	assert.Equal(t, 42, artifact.IssueNumber)
	assert.Equal(t, "https://github.com/whale-net/everything/issues/42", artifact.IssueURL)
	assert.Equal(t, acting, artifact.CreatedByActing, "both LB4 subjects must be recorded")
	assert.Equal(t, onBehalfOf, artifact.CreatedByOnBehalfOf, "both LB4 subjects must be recorded")

	scope, err := s.Scopes().GetByID(ctx, scopeID)
	require.NoError(t, err)
	require.NotNil(t, scope.PointerIssueNumber, "scope.pointer_issue_number must be mirrored in the same transaction (LB1)")
	assert.Equal(t, 42, *scope.PointerIssueNumber)

	list, err := s.PointerArtifacts().ListByProduct(ctx, product.ID)
	require.NoError(t, err)
	require.Len(t, list, 1, "the pointer artifact must be readable back from the Product it was created for")
	assert.Equal(t, artifact.ID, list[0].ID)
}

// TestPointerArtifactStore_Create_UnknownProduct_ReturnsErrNotFound proves
// LB2 parentage: Create rejects a product_id with no current `product` row
// and inserts no row.
func TestPointerArtifactStore_Create_UnknownProduct_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newPointerTestStore(t)
	scopeID := newPointerTestScope(t, ctx, db, "whale-net/pointer-orphan-test")

	self := testSubject("agent-1")
	_, err := s.PointerArtifacts().Create(ctx, scopeID, uuid.New(), 1, "https://github.com/whale-net/everything/issues/1", self, self)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pointer_artifact`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected Create must insert no row")
}

// TestPointerArtifactStore_Create_SecondCallSameProduct_Rejected proves
// pointer_artifact_product_idx: at most one pointer issue is ever minted
// per Product.
func TestPointerArtifactStore_Create_SecondCallSameProduct_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newPointerTestStore(t)
	scopeID := newPointerTestScope(t, ctx, db, "whale-net/pointer-dup-test")
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)

	self := testSubject("agent-1")
	_, err = s.PointerArtifacts().Create(ctx, scopeID, product.ID, 1, "https://github.com/whale-net/everything/issues/1", self, self)
	require.NoError(t, err)

	_, err = s.PointerArtifacts().Create(ctx, scopeID, product.ID, 2, "https://github.com/whale-net/everything/issues/2", self, self)
	assert.Error(t, err, "a second pointer artifact for the same Product must be rejected (pointer_artifact_product_idx)")
}

// TestPointerArtifactStore_AppearsInWholeProductSlice_NotInAnotherScope is
// this issue's Testing section, verbatim: a created pointer artifact
// appears in FR8's whole-product slice for its own scope, and never in
// another scope's slice.
func TestPointerArtifactStore_AppearsInWholeProductSlice_NotInAnotherScope(t *testing.T) {
	ctx := context.Background()
	s, db := newPointerTestStore(t)

	scopeA := newPointerTestScope(t, ctx, db, "whale-net/pointer-slice-test-a")
	productA, err := s.Products().Create(ctx, scopeA, "Krill A", "")
	require.NoError(t, err)

	scopeB := newPointerTestScope(t, ctx, db, "whale-net/pointer-slice-test-b")
	productB, err := s.Products().Create(ctx, scopeB, "Krill B", "")
	require.NoError(t, err)

	self := testSubject("agent-1")
	artifactA, err := s.PointerArtifacts().Create(ctx, scopeA, productA.ID, 7, "https://github.com/whale-net/everything/issues/7", self, self)
	require.NoError(t, err)

	q := slice.NewQuerier(s)

	docA, err := q.GetProductSlice(ctx, productA.ID)
	require.NoError(t, err)
	require.Len(t, docA.PointerArtifacts, 1, "the pointer artifact must appear in its own Product's whole-product slice")
	assert.Equal(t, artifactA.ID, docA.PointerArtifacts[0].ID)
	assert.Equal(t, artifactA.IssueNumber, docA.PointerArtifacts[0].IssueNumber)
	assert.Equal(t, artifactA.IssueURL, docA.PointerArtifacts[0].IssueURL)

	docB, err := q.GetProductSlice(ctx, productB.ID)
	require.NoError(t, err)
	assert.Empty(t, docB.PointerArtifacts, "a pointer artifact created for one scope's Product must never appear in another scope's Product slice")
}
