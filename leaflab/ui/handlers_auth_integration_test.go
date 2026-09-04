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
// Schema here is a self-contained subset of the column sets migrations
// 013_ownership.up.sql (leaflab_user, board_owner_history, region/plant
// owner columns) and 001_initial_schema.up.sql (board, region, plant) create
// — see leaflab/migrate/migrations/013_ownership.up.sql and
// libs/go/dbtest's README for why integration tests keep schema
// self-contained rather than importing another package's migrations.
package main

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// leaflabUserOwnershipSchema mirrors just enough of migrations 001, 013, and
// 016 to prove upsertLeafLabUser's contracts: idempotency keyed on
// oidc_sub, that it never writes board_owner_history or any
// owner_leaflab_user_id column, and (016's leaflab_user_role) the FR10
// empty-database bootstrap grant.
const leaflabUserOwnershipSchema = `
	CREATE TABLE leaflab_user (
		leaflab_user_id     BIGSERIAL   PRIMARY KEY,
		oidc_sub            TEXT        NOT NULL UNIQUE,
		preferred_username  TEXT,
		email               TEXT,
		display_name        TEXT,
		created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE leaflab_user_role (
		leaflab_user_role_id BIGSERIAL   PRIMARY KEY,
		leaflab_user_id      BIGINT      NOT NULL REFERENCES leaflab_user(leaflab_user_id) ON DELETE CASCADE,
		role                 TEXT        NOT NULL,
		valid_from           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to             TIMESTAMPTZ
	);
	CREATE UNIQUE INDEX idx_leaflab_user_role_current
		ON leaflab_user_role(leaflab_user_id, role) WHERE valid_to IS NULL;

	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE board_owner_history (
		board_owner_history_id BIGSERIAL   PRIMARY KEY,
		board_id               BIGINT      NOT NULL REFERENCES board(board_id) ON DELETE CASCADE,
		leaflab_user_id        BIGINT      NOT NULL REFERENCES leaflab_user(leaflab_user_id),
		valid_from             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to               TIMESTAMPTZ
	);
	CREATE UNIQUE INDEX idx_board_owner_history_current
		ON board_owner_history(board_id) WHERE valid_to IS NULL;

	CREATE TABLE region (
		region_id             BIGSERIAL    PRIMARY KEY,
		name                  VARCHAR(255) NOT NULL,
		owner_leaflab_user_id BIGINT REFERENCES leaflab_user(leaflab_user_id)
	);

	CREATE TABLE plant (
		plant_id              BIGSERIAL    PRIMARY KEY,
		region_id             BIGINT       NOT NULL REFERENCES region(region_id) ON DELETE RESTRICT,
		name                  VARCHAR(128) NOT NULL,
		owner_leaflab_user_id BIGINT REFERENCES leaflab_user(leaflab_user_id)
	);
`

// TestUpsertLeafLabUser_FirstSignIn_InsertsRow covers the Testing section's
// "first call inserts one row" assertion.
func TestUpsertLeafLabUser_FirstSignIn_InsertsRow(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: leaflabUserOwnershipSchema})
	app := &App{pool: db.Pool}

	err := app.upsertLeafLabUser(ctx, &htmxauth.UserInfo{
		Sub:               "sub-first",
		PreferredUsername: "alice",
		Email:             "alice@example.com",
		Name:              "Alice Example",
	})
	require.NoError(t, err)

	assert.Equal(t, 1, countLeaflabUsers(t, ctx, db.Pool, "sub-first"))
}

// TestUpsertLeafLabUser_RepeatSignIn_UpdatesNotInserts covers the Testing
// section's "second call with the same sub updates and does not insert a
// second [row]" assertion, and is the test the issue's red/green note names
// directly: removing the ON CONFLICT clause from upsertLeafLabUser's INSERT
// must make this test fail with a unique-constraint violation on oidc_sub
// instead of the second sign-in's profile fields winning.
func TestUpsertLeafLabUser_RepeatSignIn_UpdatesNotInserts(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: leaflabUserOwnershipSchema})
	app := &App{pool: db.Pool}

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

	assert.Equal(t, 1, countLeaflabUsers(t, ctx, db.Pool, "sub-repeat"),
		"a second sign-in with the same sub must update the existing row, not insert a second one")

	var preferredUsername, email, displayName string
	err := db.Pool.QueryRow(ctx,
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
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: leaflabUserOwnershipSchema})
	app := &App{pool: db.Pool}

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
	assert.Equal(t, 1, countLeaflabUsers(t, ctx, db.Pool, "sub-concurrent"),
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
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: leaflabUserOwnershipSchema})
	app := &App{pool: db.Pool}

	_, err := db.Pool.Exec(ctx, `INSERT INTO board (device_id) VALUES ('leaflab-deadbeef')`)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `INSERT INTO region (name) VALUES ('Greenhouse')`)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `INSERT INTO plant (region_id, name) VALUES (1, 'Basil')`)
	require.NoError(t, err)

	require.NoError(t, app.upsertLeafLabUser(ctx, &htmxauth.UserInfo{
		Sub:               "sub-claims-nothing",
		PreferredUsername: "dave",
		Email:             "dave@example.com",
		Name:              "Dave Example",
	}))

	var boardOwnerHistoryCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM board_owner_history`).Scan(&boardOwnerHistoryCount))
	assert.Equal(t, 0, boardOwnerHistoryCount, "signing in must never write board_owner_history")

	var regionOwner, plantOwner *int64
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT owner_leaflab_user_id FROM region WHERE name = 'Greenhouse'`).Scan(&regionOwner))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT owner_leaflab_user_id FROM plant WHERE name = 'Basil'`).Scan(&plantOwner))
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
