//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See the go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest's README for how to run it.
//
// It covers the one /mcp/consent-flow guarantee handlers_consent_test.go
// cannot: the FR12 grant-bookkeeping *write* itself. grantindex.Index is a
// concrete, pgx-backed type with no in-memory fake (unlike grpcauth.Store,
// which ships grpcauth.FakeStore for exactly this reason -- see
// libs/go/grpcauth/grantindex/grantindex_integration_test.go's identical
// rationale for its own Postgres-backed tests).
//
// This file's go_test target (BUILD.bazel's ui_integration_test) also
// compiles handlers_consent_test.go into this same test binary, reusing
// its fakeGrantIdP/consentMux/newConsentTestApp-adjacent helpers and
// devUserEncodedSubject/testConsentOIDCIssuer constants rather than
// duplicating them -- deliberately not main_test.go too (its
// newTestMCPProvider/newTestOIDCAuthenticator/requestWasAuthBlocked are
// unrelated to anything either consent test file exercises, per
// consentMux's own doc comment).
//
// These tests deliberately configure fakeGrantIdP (handlers_consent_test.go)
// to return devUserEncodedSubject as the grant token's `sub` claim -- the
// grpcauth-contract-satisfying case handlers_consent_test.go's
// TestConsentRoundTrip_MatchingGrantSubject_PersistsActiveGrant already
// isolates -- rather than re-driving
// TestConsentRoundTrip_RealisticCrossClientSubject_MustSucceed's failing,
// realistic-subject case here too. That failure (subject mismatch against
// any real Keycloak realm) is an implementation defect independent of, and
// already fully documented by, the unit test; duplicating it here would
// only prevent this file from ever reaching the FR12 write path it exists
// to cover.
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantindex"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/whagent_net/delegatedgrant"
	"github.com/whale-net/everything/whagent_net/mcpidentity"
)

// grantIndexSchema is a self-contained copy of the schema contract
// documented in grantindex.go's package doc comment -- dbtest's own
// README asks integration tests to keep schema self-contained rather than
// importing another package's migrations, mirroring
// grantindex_integration_test.go's own grantIndexSchema constant.
const grantIndexSchema = `
	CREATE TABLE grpcauth_grant_index (
		subject_iss        TEXT        NOT NULL,
		subject_sub        TEXT        NOT NULL,
		domain              TEXT        NOT NULL,
		preferred_username  TEXT        NOT NULL,
		granted_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (subject_iss, subject_sub, domain)
	);
`

// newConsentIndexTestApp mirrors newConsentTestApp
// (handlers_consent_test.go) but wires a real, Postgres-backed
// *grantindex.Index as app.grant.Index instead of leaving it nil, so
// recordConsentBookkeeping's FR12 write actually reaches a table.
func newConsentIndexTestApp(t *testing.T, fake *fakeGrantIdP) (*App, *grantindex.Index) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: grantIndexSchema})
	idx, err := grantindex.New(db.Pool, grantindex.Config{})
	require.NoError(t, err)

	store := grpcauth.NewFakeStore()
	src, err := grpcauth.NewDelegatedGrantSource(ctx, grpcauth.DelegatedGrantConfig{
		ClientID:     "test-grant-client",
		ClientSecret: "test-grant-client-secret",
		RedirectURI:  "http://ui.example.test/mcp/consent/callback",
		Store:        store,
		Endpoints: grpcauth.Endpoints{
			Authorization: fake.URL + "/authorize",
			Token:         fake.URL + "/token",
		},
	})
	require.NoError(t, err)

	auth, err := htmxauth.NewAuthenticator(ctx, htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "test-secret-that-is-at-least-32-bytes-long",
		SessionName:   "test_consent_ui_session",
	})
	require.NoError(t, err)

	app := &App{
		auth:         auth,
		oidcIssuer:   testConsentOIDCIssuer,
		grant:        delegatedgrant.Components{Source: src, Store: store, Index: idx},
		consentStore: newConsentStore("test-secret-that-is-at-least-32-bytes-long"),
	}
	return app, idx
}

// driveFullConsent runs one complete POST /mcp/consent -> real /authorize
// hit -> GET /mcp/consent/callback round trip and returns the callback's
// response, failing the test unless it completed successfully (302 to
// return_to).
func driveFullConsent(t *testing.T, mux *http.ServeMux, domain string) *httptest.ResponseRecorder {
	t.Helper()

	confirmReq := httptest.NewRequest(http.MethodPost, "/mcp/consent", strings.NewReader(url.Values{
		"domain":    {domain},
		"return_to": {"/sessions/42"},
	}.Encode()))
	confirmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	confirmW := httptest.NewRecorder()
	mux.ServeHTTP(confirmW, confirmReq)
	require.Equal(t, http.StatusFound, confirmW.Code)
	cookie := consentCookie(t, confirmW)

	code, state := driveConsentAuthorize(t, confirmW.Header().Get("Location"))

	callbackReq := httptest.NewRequest(http.MethodGet, "/mcp/consent/callback?code="+code+"&state="+state, nil)
	callbackReq.AddCookie(cookie)
	callbackW := httptest.NewRecorder()
	mux.ServeHTTP(callbackW, callbackReq)
	require.Equal(t, http.StatusFound, callbackW.Code, "body: %s", callbackW.Body.String())
	return callbackW
}

// TestConsentCallback_SuccessfulConsent_RecordsExactlyOneGrantIndexRow is
// issue #2428's Testing section: "Successful consent writes exactly one
// grantindex row with the operator's preferred_username" -- and its
// sibling bullet, "The operator's (iss, sub) written by `ui` matches what
// whagent_net/mcpidentity round-trips" (verified directly below via
// mcpidentity.Decode against the row grantindex.ListBySubject reads back).
func TestConsentCallback_SuccessfulConsent_RecordsExactlyOneGrantIndexRow(t *testing.T) {
	fake := newFakeGrantIdP(t)
	fake.SetSubject(devUserEncodedSubject)
	app, idx := newConsentIndexTestApp(t, fake)
	mux := consentMux(app)

	driveFullConsent(t, mux, "audience_score_system")

	wantIss, wantSub, err := mcpidentity.Decode(devUserEncodedSubject)
	require.NoError(t, err)

	entries, err := idx.ListBySubject(context.Background(), wantIss, wantSub)
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly one grantindex row must be written on successful consent")

	entry := entries[0]
	assert.Equal(t, wantIss, entry.SubjectIss)
	assert.Equal(t, wantSub, entry.SubjectSub)
	assert.Equal(t, "audience_score_system", entry.Domain)
	assert.Equal(t, "developer", entry.PreferredUsername, "must capture the consenting operator's own preferred_username (AuthModeNone's fixed dev user)")
}

// TestConsentCallback_ReConsentForSameDomain_UpsertsWithoutDuplicateOrError
// is issue #2428's Testing section: "a second consent for the same domain
// does not duplicate or error" -- grantindex.Record's ON CONFLICT DO
// NOTHING contract (FR12), exercised end to end through the real HTTP
// handlers rather than calling Record directly.
func TestConsentCallback_ReConsentForSameDomain_UpsertsWithoutDuplicateOrError(t *testing.T) {
	fake := newFakeGrantIdP(t)
	fake.SetSubject(devUserEncodedSubject)
	app, idx := newConsentIndexTestApp(t, fake)
	mux := consentMux(app)

	driveFullConsent(t, mux, "audience_score_system")
	driveFullConsent(t, mux, "audience_score_system")

	wantIss, wantSub, err := mcpidentity.Decode(devUserEncodedSubject)
	require.NoError(t, err)

	entries, err := idx.ListBySubject(context.Background(), wantIss, wantSub)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "re-consenting for an already-recorded domain must not duplicate the grantindex row")
}

// TestConsentCallback_ConsentForTwoDomains_RecordsBothIndependently proves
// FR3's per-domain scoping holds for the bookkeeping index too: consenting
// for two different domains records two independent rows, not one shared
// or overwritten row.
func TestConsentCallback_ConsentForTwoDomains_RecordsBothIndependently(t *testing.T) {
	fake := newFakeGrantIdP(t)
	fake.SetSubject(devUserEncodedSubject)
	app, idx := newConsentIndexTestApp(t, fake)
	mux := consentMux(app)

	driveFullConsent(t, mux, "audience_score_system")
	driveFullConsent(t, mux, "manmanv2")

	wantIss, wantSub, err := mcpidentity.Decode(devUserEncodedSubject)
	require.NoError(t, err)

	entries, err := idx.ListBySubject(context.Background(), wantIss, wantSub)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	domains := map[string]bool{}
	for _, e := range entries {
		domains[e.Domain] = true
	}
	assert.True(t, domains["audience_score_system"])
	assert.True(t, domains["manmanv2"])
}
