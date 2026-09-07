//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs it.
// See the go_test target's gotags in BUILD.bazel and libs/go/dbtest/README.md
// for how to run it.
//
// These tests exercise FR2's leaflab_user upsert-on-sign-in
// (upsertLeafLabUser in handlers_auth.go) against a real Postgres: the
// insert-then-update idempotency, the ON CONFLICT clause's role in making
// concurrent sign-ins from the same person race-safe, and the "signing in
// claims nothing" guarantee (LB1/NFR5/NFR6) that no ownership row or column
// is ever touched by this path.
//
// Schema setup runs the real migrations via newLeafLabTestPool
// (testdb_integration_test.go in this package) -- the same embed.FS
// //leaflab/migrate applies in production.
package main

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// TestUpsertLeafLabUser_FirstSignIn_InsertsRow covers the Testing section's
// "first call inserts one row" assertion.
func TestUpsertLeafLabUser_FirstSignIn_InsertsRow(t *testing.T) {
	ctx := context.Background()
	db := newLeafLabTestPool(t)
	app := &App{pool: db}

	err := app.upsertLeafLabUser(ctx, &htmxauth.UserInfo{
		Sub:               "sub-first",
		PreferredUsername: "alice",
		Email:             "alice@example.com",
		Name:              "Alice Example",
	})
	require.NoError(t, err)

	assert.Equal(t, 1, countLeaflabUsers(t, ctx, db, "sub-first"))
}

// TestUpsertLeafLabUser_RepeatSignIn_UpdatesNotInserts covers the Testing
// section's "second call with the same sub updates and does not insert a
// second [row]" assertion, and is the test the issue's red/green note names
// directly: removing the ON CONFLICT clause from upsertLeafLabUser's INSERT
// must make this test fail with a unique-constraint violation on oidc_sub
// instead of the second sign-in's profile fields winning.
func TestUpsertLeafLabUser_RepeatSignIn_UpdatesNotInserts(t *testing.T) {
	ctx := context.Background()
	db := newLeafLabTestPool(t)
	app := &App{pool: db}

	require.NoError(t, app.upsertLeafLabUser(ctx, &htmxauth.UserInfo{
		Sub:               "sub-repeat",
		PreferredUsername: "bob",
		Email:             "bob@example.com",
		Name:              "Bob Example",
	}))
	require.NoError(t, app.upsertLeafLabUser(ctx, &htmxauth.UserInfo{
		Sub:               "sub-repeat",
		PreferredUsername: "bob2",
		Email:             "bob2@example.com",
		Name:              "Bob Two",
	}))

	assert.Equal(t, 1, countLeaflabUsers(t, ctx, db, "sub-repeat"),
		"a second sign-in with the same sub must update the existing row, not insert a second one")

	var preferredUsername, email, displayName string
	err := db.QueryRow(ctx,
		`SELECT preferred_username, email, display_name FROM leaflab_user WHERE oidc_sub = $1`,
		"sub-repeat").Scan(&preferredUsername, &email, &displayName)
	require.NoError(t, err)
	assert.Equal(t, "bob2", preferredUsername, "re-sign-in must refresh preferred_username")
	assert.Equal(t, "bob2@example.com", email, "re-sign-in must refresh email")
	assert.Equal(t, "Bob Two", displayName, "re-sign-in must refresh display_name")
}

// TestUpsertLeafLabUser_ConcurrentSignIns_YieldsExactlyOneRow covers the
// Testing section's "two concurrent calls with the same sub still yield
// exactly one row" assertion — the ON CONFLICT clause's actual reason for
// existing (a plain "check then insert" would race two connections into
// two rows).
func TestUpsertLeafLabUser_ConcurrentSignIns_YieldsExactlyOneRow(t *testing.T) {
	ctx := context.Background()
	db := newLeafLabTestPool(t)
	app := &App{pool: db}

	const concurrency = 10
	var wg sync.WaitGroup
	errs := make([]error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = app.upsertLeafLabUser(ctx, &htmxauth.UserInfo{
				Sub:               "sub-concurrent",
				PreferredUsername: "carol",
				Email:             "carol@example.com",
				Name:              "Carol Example",
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "concurrent upsert %d must not error", i)
	}
	assert.Equal(t, 1, countLeaflabUsers(t, ctx, db, "sub-concurrent"),
		"concurrent sign-ins from the same sub must never race two rows into existence")
}

// TestUpsertLeafLabUser_ClaimsNothing covers the Testing section's "after
// sign-in, board_owner_history is still empty and every
// owner_leaflab_user_id is still NULL" assertion (FR2's "signing in claims
// nothing", LB1/NFR5/NFR6): pre-populates a board, a region, and a plant —
// all unowned, as every board/region/plant is in M1 — then upserts a
// leaflab_user and asserts none of those rows or tables were touched.
func TestUpsertLeafLabUser_ClaimsNothing(t *testing.T) {
	ctx := context.Background()
	db := newLeafLabTestPool(t)
	app := &App{pool: db}

	_, err := db.Exec(ctx, `INSERT INTO board (device_id) VALUES ('leaflab-deadbeef')`)
	require.NoError(t, err)
	var regionID int64
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO region (name) VALUES ('Greenhouse') RETURNING region_id`).Scan(&regionID))
	var plantTypeID int64
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO plant_type (common_name) VALUES ('Basil plant') RETURNING plant_type_id`).Scan(&plantTypeID))
	_, err = db.Exec(ctx,
		`INSERT INTO plant (region_id, plant_type_id, name) VALUES ($1, $2, 'Basil')`, regionID, plantTypeID)
	require.NoError(t, err)

	require.NoError(t, app.upsertLeafLabUser(ctx, &htmxauth.UserInfo{
		Sub:               "sub-claims-nothing",
		PreferredUsername: "dave",
		Email:             "dave@example.com",
		Name:              "Dave Example",
	}))

	var boardOwnerHistoryCount int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM board_owner_history`).Scan(&boardOwnerHistoryCount))
	assert.Equal(t, 0, boardOwnerHistoryCount, "signing in must never write board_owner_history")

	var regionOwner, plantOwner *int64
	require.NoError(t, db.QueryRow(ctx, `SELECT owner_leaflab_user_id FROM region WHERE name = 'Greenhouse'`).Scan(&regionOwner))
	require.NoError(t, db.QueryRow(ctx, `SELECT owner_leaflab_user_id FROM plant WHERE name = 'Basil'`).Scan(&plantOwner))
	assert.Nil(t, regionOwner, "signing in must never assign region.owner_leaflab_user_id")
	assert.Nil(t, plantOwner, "signing in must never assign plant.owner_leaflab_user_id")
}

func countLeaflabUsers(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sub string) int {
	t.Helper()
	var count int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM leaflab_user WHERE oidc_sub = $1`, sub).Scan(&count)
	require.NoError(t, err)
	return count
}

// ─── #1775: FR10 empty-database bootstrap (maybeBootstrapAdmin) ────────────

// countOpenAdminGrants returns the number of open (valid_to IS NULL)
// 'admin' rows in leaflab_user_role across all users.
func countOpenAdminGrants(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM leaflab_user_role WHERE role = 'admin' AND valid_to IS NULL`).Scan(&count)
	require.NoError(t, err)
	return count
}

// hasOpenAdminGrant reports whether oidcSub's leaflab_user holds an open
// 'admin' grant.
func hasOpenAdminGrant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, oidcSub string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM leaflab_user_role r
			JOIN leaflab_user u ON u.leaflab_user_id = r.leaflab_user_id
			WHERE u.oidc_sub = $1 AND r.role = 'admin' AND r.valid_to IS NULL
		)
	`, oidcSub).Scan(&exists)
	require.NoError(t, err)
	return exists
}

// TestUpsertLeafLabUser_EmptyDatabase_FirstSignInBecomesAdmin_SecondDoesNot
// is Testing criterion 10: on a database with zero leaflab_user rows, the
// first sign-in creates the user and grants 'admin'; a second sign-in by a
// different subject creates an ordinary user with no grant.
func TestUpsertLeafLabUser_EmptyDatabase_FirstSignInBecomesAdmin_SecondDoesNot(t *testing.T) {
	ctx := context.Background()
	db := newLeafLabTestPool(t)
	app := &App{pool: db}

	require.NoError(t, app.upsertLeafLabUser(ctx, &htmxauth.UserInfo{
		Sub:               "sub-bootstrap-first",
		PreferredUsername: "first",
	}))
	assert.True(t, hasOpenAdminGrant(t, ctx, db, "sub-bootstrap-first"),
		"the first-ever sign-in on an empty database must be granted admin")
	assert.Equal(t, 1, countOpenAdminGrants(t, ctx, db))

	require.NoError(t, app.upsertLeafLabUser(ctx, &htmxauth.UserInfo{
		Sub:               "sub-bootstrap-second",
		PreferredUsername: "second",
	}))
	assert.False(t, hasOpenAdminGrant(t, ctx, db, "sub-bootstrap-second"),
		"a second, later first-time sign-in must not also become admin")
	assert.Equal(t, 1, countOpenAdminGrants(t, ctx, db),
		"exactly one open admin grant must exist after the second user's first sign-in")
}

// TestUpsertLeafLabUser_ConcurrentFirstSignIns_ExactlyOneAdminGrant is
// Testing criterion 11: two simultaneous first sign-ins by different
// subjects on an empty database must result in exactly one open 'admin'
// grant -- this is the test the issue's red/green note names directly (see
// this file's doc comment and maybeBootstrapAdmin's own doc comment in
// handlers_auth.go): replacing the transactional
// pg_advisory_xact_lock-guarded bootstrap with a bare read-then-write must
// make this test flaky/red under concurrency.
func TestUpsertLeafLabUser_ConcurrentFirstSignIns_ExactlyOneAdminGrant(t *testing.T) {
	ctx := context.Background()
	db := newLeafLabTestPool(t)
	app := &App{pool: db}

	const concurrency = 10
	var wg sync.WaitGroup
	errs := make([]error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = app.upsertLeafLabUser(ctx, &htmxauth.UserInfo{
				Sub:               fmt.Sprintf("sub-concurrent-first-%d", i),
				PreferredUsername: fmt.Sprintf("user%d", i),
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "concurrent first sign-in %d must not error", i)
	}

	assert.Equal(t, concurrency, func() int {
		var count int
		require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM leaflab_user`).Scan(&count))
		return count
	}(), "every concurrent first sign-in must still create its own leaflab_user row")

	assert.Equal(t, 1, countOpenAdminGrants(t, ctx, db),
		"exactly one of the concurrent first sign-ins must win the bootstrap admin grant")
}

// TestUpsertLeafLabUser_NonEmptyDatabase_ExistingAdminMeansNoGrantOnLaterFirstSignIn
// is Testing criterion 12: migration 016 having already granted admin to an
// existing user (simulated here by seeding an open admin grant directly,
// mirroring what that migration's INSERT leaves behind) means a later
// first-time sign-in gets no grant of its own.
func TestUpsertLeafLabUser_NonEmptyDatabase_ExistingAdminMeansNoGrantOnLaterFirstSignIn(t *testing.T) {
	ctx := context.Background()
	db := newLeafLabTestPool(t)
	app := &App{pool: db}

	// Simulate migration 016's earliest-user seed: a pre-existing
	// leaflab_user with an open admin grant, before any sign-in through
	// upsertLeafLabUser ever runs.
	var earliestUserID int64
	require.NoError(t, db.QueryRow(ctx,
		`INSERT INTO leaflab_user (oidc_sub) VALUES ('sub-migration-seeded') RETURNING leaflab_user_id`,
	).Scan(&earliestUserID))
	_, err := db.Exec(ctx,
		`INSERT INTO leaflab_user_role (leaflab_user_id, role) VALUES ($1, 'admin')`, earliestUserID)
	require.NoError(t, err)
	require.Equal(t, 1, countOpenAdminGrants(t, ctx, db))

	require.NoError(t, app.upsertLeafLabUser(ctx, &htmxauth.UserInfo{
		Sub:               "sub-later-first-sign-in",
		PreferredUsername: "later",
	}))

	assert.False(t, hasOpenAdminGrant(t, ctx, db, "sub-later-first-sign-in"),
		"a first-time sign-in must not be granted admin once migration 016 already seeded one")
	assert.Equal(t, 1, countOpenAdminGrants(t, ctx, db),
		"the migration-seeded grant must remain the only open admin grant")
}
