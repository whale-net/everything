//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs it.
// See the go_test target's gotags in BUILD.bazel and libs/go/dbtest/README.md
// for how to run it.
//
// These tests exercise exactly what auth_test.go's pure-Go tests cannot:
// real PostgreSQL preflight failure/success against a real ui_sessions
// table, real search_path resolution (FR-58's "unqualified probe" guarantee),
// and a real JSONB persist -> reload cycle for the nil/empty Roles
// distinction (FR-54).
//
// Schema here is a self-contained copy of the column set this package's own
// bundled migration creates (see migrations/900001_ui_sessions.up.sql) —
// dbtest's own README asks integration tests to keep schema self-contained
// rather than importing another package's migrations.
//
// oidc.IDToken has no public constructor for injecting test claims outside
// a full OIDC discovery+verify round trip, so these tests exercise the real
// production SetUserInfo/GetUserInfo *SQL* by inserting rows with the exact
// INSERT statement SetUserInfo runs (same columns, same JSON shape produced
// by parseRealmRoles + userInfoRecord), and then reading them back through
// the real, unmodified GetUserInfo method. That covers the DB round trip
// this file is here to prove without needing a real ID token.
package htmxauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
)

const uiSessionsSchema = `
	CREATE TABLE ui_sessions (
		session_id       TEXT        PRIMARY KEY,
		user_info        JSONB       NOT NULL DEFAULT '{}',
		access_token     TEXT        NOT NULL,
		refresh_token    TEXT        NOT NULL,
		token_expires_at TIMESTAMPTZ NOT NULL,
		created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at       TIMESTAMPTZ NOT NULL
	);
`

// ── FR-58: boot-time preflight ─────────────────────────────────────────────

func TestPreflight_MissingTable_FailsNamingTableAndMigration(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		// No Schema — ui_sessions does not exist.
	})

	store, err := NewDBSessionManager(ctx, db.Pool, "test-secret", "test_session")

	require.Error(t, err, "preflight must fail when ui_sessions does not exist")
	assert.Nil(t, store, "on preflight failure, no DBSessionManager must be handed back — "+
		"requesting DB sessions must never silently degrade to cookie sessions")
	assert.Contains(t, err.Error(), ui_sessionsTable, "error must name the table")
	assert.Contains(t, err.Error(), ui_sessionsMigration, "error must name the owning migration file")
}

func TestPreflight_PresentTable_BootProceeds(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: uiSessionsSchema,
	})

	store, err := NewDBSessionManager(ctx, db.Pool, "test-secret", "test_session")

	require.NoError(t, err)
	require.NotNil(t, store)
}

// TestPreflight_TableOutsideSearchPath_Fails proves the probe uses the
// unqualified table name: a ui_sessions table that exists only in a schema
// not on the connection's search_path must fail preflight exactly like a
// missing table, catching a search_path misconfiguration that a
// schema-qualified probe would hide.
func TestPreflight_TableOutsideSearchPath_Fails(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: `
			CREATE SCHEMA other_schema;
			CREATE TABLE other_schema.ui_sessions (
				session_id TEXT PRIMARY KEY
			);
		`,
		// Deliberately no ui_sessions table on the default search_path
		// ("$user", public) — only inside other_schema, which nothing
		// puts on this role's search_path.
	})

	store, err := NewDBSessionManager(ctx, db.Pool, "test-secret", "test_session")

	require.Error(t, err, "a table outside search_path must fail preflight just like a missing table")
	assert.Nil(t, store)
}

// ── FR-54 x persistence: JSONB round trip through real Postgres ───────────

func TestPersistReload_RolesRoundTripThroughRealDB(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: uiSessionsSchema,
	})

	store, err := NewDBSessionManager(ctx, db.Pool, "test-secret", "test_session")
	require.NoError(t, err)

	cases := []struct {
		name       string
		claims     map[string]interface{}
		wantNil    bool
		wantValues []string
	}{
		{
			name:    "absent claim persists and reloads as nil",
			claims:  map[string]interface{}{"sub": "u-absent"},
			wantNil: true,
		},
		{
			name: "present-empty claim persists and reloads as non-nil empty",
			claims: map[string]interface{}{
				"sub":          "u-empty",
				"realm_access": map[string]interface{}{"roles": []interface{}{}},
			},
			wantNil:    false,
			wantValues: []string{},
		},
		{
			name: "present roles persist and reload identically",
			claims: map[string]interface{}{
				"sub":          "u-roles",
				"realm_access": map[string]interface{}{"roles": []interface{}{"admin", "viewer"}},
			},
			wantNil:    false,
			wantValues: []string{"admin", "viewer"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roles, parseErr := parseRealmRoles(tc.claims)
			require.NoError(t, parseErr)

			sessionID := insertSessionRow(t, ctx, store, tc.claims["sub"].(string), roles)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(&http.Cookie{Name: "test_session", Value: sessionID})

			got, err := store.GetUserInfo(req)
			require.NoError(t, err, "a freshly persisted session must gate identically to a fresh login")

			if tc.wantNil {
				assert.Nil(t, got.Roles, "absent roles must reload as nil, not empty")
			} else {
				assert.NotNil(t, got.Roles)
				assert.Equal(t, tc.wantValues, got.Roles)
			}
		})
	}
}

// TestPersistReload_PreExistingRow_NoRolesKey_DeserialisesAsAbsent proves
// that a session row written before roles existed (no "roles" key at all in
// the JSONB) reloads through the real production GetUserInfo as Roles==nil,
// not an empty slice. This is the specific regression this issue exists to
// catch: asserting len(rec.Roles)==0 here would pass for both "absent" and
// "empty" and hide the bug; only a nil check distinguishes them.
func TestPersistReload_PreExistingRow_NoRolesKey_DeserialisesAsAbsent(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: uiSessionsSchema,
	})

	store, err := NewDBSessionManager(ctx, db.Pool, "test-secret", "test_session")
	require.NoError(t, err)

	// A pre-existing row: user_info JSON has no "roles" key at all,
	// simulating a session created before this library gained role support.
	legacyUserInfo := `{"sub":"u-legacy","preferred_username":"legacy","name":"Legacy User","email":"legacy@example.com"}`
	sessionID := "legacy-session-id"
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO ui_sessions
			(session_id, user_info, access_token, refresh_token, token_expires_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sessionID, legacyUserInfo, "access-tok", "enc-refresh-tok", time.Now().Add(time.Hour), time.Now().Add(24*time.Hour))
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "test_session", Value: sessionID})

	got, err := store.GetUserInfo(req)
	require.NoError(t, err)

	// CRITICAL: must use a nil check, never len(got.Roles) == 0 — both
	// absent and present-empty have length 0, so a length check cannot
	// tell them apart and would silently hide this regression.
	assert.Nil(t, got.Roles, "pre-existing row with no roles key must deserialise Roles as nil, not empty")
}

// insertSessionRow inserts a session row using exactly the columns and
// shape SetUserInfo's own INSERT statement writes (see db_session.go),
// standing in for a real OIDC login since oidc.IDToken cannot be
// constructed with injected test claims outside a full verify flow.
func insertSessionRow(t *testing.T, ctx context.Context, s *DBSessionManager, sub string, roles []string) string {
	t.Helper()

	rec := userInfoRecord{
		Sub:   sub,
		Roles: roles,
	}
	userInfoJSON, err := json.Marshal(rec)
	require.NoError(t, err)

	sessionID, err := generateSessionID()
	require.NoError(t, err)

	_, err = s.pool.Exec(ctx, `
		INSERT INTO ui_sessions
			(session_id, user_info, access_token, refresh_token, token_expires_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sessionID, userInfoJSON, "access-tok", "enc-refresh-tok", time.Now().Add(time.Hour), time.Now().Add(24*time.Hour))
	require.NoError(t, err)

	return sessionID
}
