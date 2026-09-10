//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See the go_test target's gotags in BUILD.bazel for how to run it.
//
// Unlike libs/go/grpcauth/pgstore's and libs/go/grpcauth/grantindex's own
// integration tests (which stand up a self-contained copy of the schema
// contract they document), these tests apply this repo's *actual*
// migration 008_delegated_grant through //libs/go/migrate first -- the
// proof issue #2426's Testing section asks for: "this repo's migration
// really does satisfy pgstore's [and grantindex's] schema contract", not
// just that a hand-copied schema string does.
package schema_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantindex"
	"github.com/whale-net/everything/libs/go/grpcauth/pgstore"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
)

// testEncryptionKey returns a fixed, valid grpcauth.GrantKeySize-byte
// encryption key for use across this file's pgstore-backed tests.
func testEncryptionKey() []byte {
	key := make([]byte, grpcauth.GrantKeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

// migratedDB stands up a real Postgres database and applies every
// migration in this repo's schema (including 008_delegated_grant) through
// the real //libs/go/migrate runner -- not a hand-copied schema string.
func migratedDB(t *testing.T) (ctx context.Context, db *dbtest.Postgres) {
	t.Helper()
	ctx = context.Background()
	db = dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration, including 008_delegated_grant")

	return ctx, db
}

// TestMigration008_PgstoreRoundTrip proves migration 008's
// grpcauth_delegated_grant table really does satisfy
// libs/go/grpcauth/pgstore's schema contract: Persist -> TokenMaterial ->
// Status -> Revoke round-trips against it exactly like pgstore's own
// integration tests do against their self-contained schema copy.
func TestMigration008_PgstoreRoundTrip(t *testing.T) {
	ctx, db := migratedDB(t)

	store, err := pgstore.NewGrantStore(ctx, pgstore.StoreConfig{
		Pool:          db.Pool,
		EncryptionKey: testEncryptionKey(),
	})
	require.NoError(t, err, "NewGrantStore must accept migration 008's grpcauth_delegated_grant table shape")

	const subject = "https://keycloak.example/realms/whale-net|alice-sub"
	const grantKey = "manmanv2"

	material := grpcauth.TokenMaterial{RefreshToken: "rt-migration-008-round-trip", ObtainedAt: time.Now()}
	require.NoError(t, store.Persist(ctx, subject, grantKey, material))

	status, err := store.Status(ctx, subject, grantKey)
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusActive, status)

	got, err := store.TokenMaterial(ctx, subject, grantKey)
	require.NoError(t, err)
	assert.Equal(t, material.RefreshToken, got.RefreshToken)

	require.NoError(t, store.Revoke(ctx, subject, grantKey))

	status, err = store.Status(ctx, subject, grantKey)
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusRevoked, status)

	_, err = store.TokenMaterial(ctx, subject, grantKey)
	assert.ErrorIs(t, err, grpcauth.ErrGrantRevoked, "TokenMaterial after Revoke must report ErrGrantRevoked, not decrypt a revoked grant's material")
}

// TestMigration008_GrantIndexRoundTrip proves migration 008's
// grpcauth_grant_index table really does satisfy
// libs/go/grpcauth/grantindex's schema contract: Record -> ListBySubject
// round-trips against it.
func TestMigration008_GrantIndexRoundTrip(t *testing.T) {
	ctx, db := migratedDB(t)

	idx, err := grantindex.New(db.Pool, grantindex.Config{})
	require.NoError(t, err, "grantindex.New must accept migration 008's grpcauth_grant_index table shape")

	entry := grantindex.Entry{
		SubjectIss:        "https://keycloak.example/realms/whale-net",
		SubjectSub:        "bob-sub",
		Domain:            "audience_score_system",
		PreferredUsername: "bob",
	}
	require.NoError(t, idx.Record(ctx, entry))

	got, err := idx.ListBySubject(ctx, entry.SubjectIss, entry.SubjectSub)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, entry.Domain, got[0].Domain)
	assert.Equal(t, entry.PreferredUsername, got[0].PreferredUsername)
	assert.False(t, got[0].GrantedAt.IsZero())
}
