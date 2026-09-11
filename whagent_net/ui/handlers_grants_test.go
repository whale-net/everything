package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantindex"
	"github.com/whale-net/everything/whagent_net/delegatedgrant"
)

// This file guards issue #2432's Implementation-phase Testing section:
// FR16's own-grants-only scoping, live (never-cached) status reads, FR17's
// single-(subject, grant)-pair revoke scoping (including a tampered
// request supplying another operator's subject), and NFR4/NFR7's
// audit-log/no-cache guarantees.
//
// testIssuer is the fixed app.oidcIssuer every test below uses -- mirrors
// handlers_session_test.go's isSessionOwner tests' own constant.
const testIssuer = "https://keycloak.example.com"

// discardLogger is a *slog.Logger that writes nowhere, for tests that
// exercise a code path requiring a logger but don't assert on its output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(new(bytes.Buffer), nil))
}

// fakeGrantIndex is a grantIndexLister backed by an in-memory slice,
// filtering ListBySubject exactly the way grantindex.Index's real SQL
// WHERE clause does (subject_iss = $1 AND subject_sub = $2) -- so a test
// seeding entries for two different (iss, sub) pairs proves buildGrantRows
// never leaks one operator's rows into another's result (FR16).
type fakeGrantIndex struct {
	entries []grantindex.Entry
}

func (f *fakeGrantIndex) ListBySubject(_ context.Context, subjectIss, subjectSub string) ([]grantindex.Entry, error) {
	var out []grantindex.Entry
	for _, e := range f.entries {
		if e.SubjectIss == subjectIss && e.SubjectSub == subjectSub {
			out = append(out, e)
		}
	}
	return out, nil
}

// TestHandleGrants_RendersPageStub is a route/render sanity check: the
// signed-in operator's own GET /grants renders the page shell. app.grant
// is zero-valued in this test (no Postgres available), so this exercises
// handleGrants' "delegated-grant management is not configured" degrade
// branch, not real listing -- see TestBuildGrantRows_* below for that.
func TestHandleGrants_RendersPageStub(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t)}
	wrapped := app.auth.RequireAuthFunc(app.handleGrants)

	req := httptest.NewRequest(http.MethodGet, "/grants", nil)
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "My grants")
}

// TestHandleGrantsRevoke_RequiresDomain is a scaffold-level sanity check:
// a POST with no domain field is rejected with 400 before any store call
// is made.
func TestHandleGrantsRevoke_RequiresDomain(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t)}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsRevoke)

	req := httptest.NewRequest(http.MethodPost, "/grants/revoke", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleGrantsRevoke_RedirectsOnValidDomain proves a POST for a
// domain the signed-in (dev-mode) operator actually holds a grant for
// revokes it and redirects back to /grants.
func TestHandleGrantsRevoke_RedirectsOnValidDomain(t *testing.T) {
	store := grpcauth.NewFakeStore()
	subjectKey, err := grantSubjectKey(testIssuer, "dev-user")
	require.NoError(t, err)
	require.NoError(t, store.Persist(context.Background(), subjectKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	app := &App{
		auth:       devModeAuthenticator(t),
		oidcIssuer: testIssuer,
		grant:      delegatedgrant.Components{Store: store},
	}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsRevoke)

	req := httptest.NewRequest(http.MethodPost, "/grants/revoke", strings.NewReader("domain=audience_score_system"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/grants", w.Header().Get("Location"))

	status, err := store.Status(context.Background(), subjectKey, "audience_score_system")
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusRevoked, status)
}

// TestHandleGrantsRevoke_TamperedSubjectIsIgnored is FR16/FR17's tamper
// test: the signed-in (dev-mode "dev-user") operator has no grant at all
// for "audience_score_system" -- only a *different* subject
// ("victim-sub") does. A request naming that domain but supplying
// "victim-sub" as a "subject"/"sub" form field must not touch victim's
// grant: the handler never reads a request-supplied subject at all, so
// the call is scoped to "dev-user" (who holds nothing there), which fails
// as an authorization-adjacent "not found" rather than ever reaching
// victim's row.
func TestHandleGrantsRevoke_TamperedSubjectIsIgnored(t *testing.T) {
	store := grpcauth.NewFakeStore()
	victimKey, err := grantSubjectKey(testIssuer, "victim-sub")
	require.NoError(t, err)
	require.NoError(t, store.Persist(context.Background(), victimKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	app := &App{
		auth:       devModeAuthenticator(t),
		oidcIssuer: testIssuer,
		grant:      delegatedgrant.Components{Store: store},
	}
	wrapped := app.auth.RequireAuthFunc(app.handleGrantsRevoke)

	form := "domain=audience_score_system&subject=victim-sub&sub=victim-sub"
	req := httptest.NewRequest(http.MethodPost, "/grants/revoke", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	wrapped(w, req)

	// Never a success: dev-user itself holds no such grant, so the
	// request-supplied "subject"/"sub" fields (silently ignored) cannot
	// be used to reach victim's grant through this handler.
	assert.NotEqual(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, http.StatusNotFound, w.Code)

	victimStatus, err := store.Status(context.Background(), victimKey, "audience_score_system")
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusActive, victimStatus, "victim's grant must be untouched by a tampered request")
}

// TestBuildGrantRows_ScopedToSubject is FR16's core scoping guarantee at
// the row-building layer: operator A's rows never include operator B's
// entries, even for the identical domain.
func TestBuildGrantRows_ScopedToSubject(t *testing.T) {
	store := grpcauth.NewFakeStore()
	ctx := context.Background()

	aKey, err := grantSubjectKey(testIssuer, "operator-a")
	require.NoError(t, err)
	bKey, err := grantSubjectKey(testIssuer, "operator-b")
	require.NoError(t, err)
	require.NoError(t, store.Persist(ctx, aKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))
	require.NoError(t, store.Persist(ctx, bKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	index := &fakeGrantIndex{entries: []grantindex.Entry{
		{SubjectIss: testIssuer, SubjectSub: "operator-a", Domain: "audience_score_system", PreferredUsername: "alice", GrantedAt: time.Now()},
		{SubjectIss: testIssuer, SubjectSub: "operator-b", Domain: "audience_score_system", PreferredUsername: "bob", GrantedAt: time.Now()},
	}}

	rowsA, err := buildGrantRows(ctx, index, store, testIssuer, "operator-a", discardLogger())
	require.NoError(t, err)
	require.Len(t, rowsA, 1)
	assert.Equal(t, "alice", rowsA[0].OperatorLabel)
	assert.Equal(t, "audience_score_system", rowsA[0].Domain)

	for _, row := range rowsA {
		assert.NotEqual(t, "bob", row.OperatorLabel, "operator A's rows must never include operator B's grant")
	}
}

// TestBuildGrantRows_StatusIsLiveNeverFromIndex proves status is read
// live from the store (never from the index, which has no status column
// at all, FR12): marking a grant needs_reauth in the store, with no index
// write of any kind, is reflected on the next buildGrantRows call.
func TestBuildGrantRows_StatusIsLiveNeverFromIndex(t *testing.T) {
	store := grpcauth.NewFakeStore()
	ctx := context.Background()

	subjectKey, err := grantSubjectKey(testIssuer, "operator-a")
	require.NoError(t, err)
	require.NoError(t, store.Persist(ctx, subjectKey, "manmanv2", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	index := &fakeGrantIndex{entries: []grantindex.Entry{
		{SubjectIss: testIssuer, SubjectSub: "operator-a", Domain: "manmanv2", PreferredUsername: "alice", GrantedAt: time.Now()},
	}}

	rows, err := buildGrantRows(ctx, index, store, testIssuer, "operator-a", discardLogger())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "active", rows[0].Status)

	// No index write happens here -- the index has no status column to
	// write to at all (grantindex's own "hard semantics" doc comment).
	require.NoError(t, store.MarkNeedsReauth(ctx, subjectKey, "manmanv2"))

	rows, err = buildGrantRows(ctx, index, store, testIssuer, "operator-a", discardLogger())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "needs_reauth", rows[0].Status)
}

// TestRevokeGrant_FR17ScopedToExactlyOnePair is the plan's own FR17
// example: operator A holds grants for audience_score_system and
// manmanv2, operator B holds one for audience_score_system. A revokes
// audience_score_system; A's manmanv2 grant and B's audience_score_system
// grant must both remain active.
func TestRevokeGrant_FR17ScopedToExactlyOnePair(t *testing.T) {
	store := grpcauth.NewFakeStore()
	ctx := context.Background()

	aKey, err := grantSubjectKey(testIssuer, "operator-a")
	require.NoError(t, err)
	bKey, err := grantSubjectKey(testIssuer, "operator-b")
	require.NoError(t, err)
	require.NoError(t, store.Persist(ctx, aKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))
	require.NoError(t, store.Persist(ctx, aKey, "manmanv2", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))
	require.NoError(t, store.Persist(ctx, bKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	require.NoError(t, revokeGrant(ctx, store, testIssuer, "operator-a", "audience_score_system", discardLogger()))

	aASSStatus, err := store.Status(ctx, aKey, "audience_score_system")
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusRevoked, aASSStatus)

	aManmanStatus, err := store.Status(ctx, aKey, "manmanv2")
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusActive, aManmanStatus, "revoking A's audience_score_system grant must not affect A's manmanv2 grant")

	bASSStatus, err := store.Status(ctx, bKey, "audience_score_system")
	require.NoError(t, err)
	assert.Equal(t, grpcauth.GrantStatusActive, bASSStatus, "revoking A's grant must not affect B's grant for the same domain")
}

// TestRevokeGrant_NFR7EffectiveWithoutRestart proves revocation is
// effective on the very next credential read in the same process: no
// cache exists (FR8), so grpcauth.Store.TokenMaterial -- the same call
// grpcauth.DelegatedGrantSource.TokenSource(...).Token(ctx) makes on
// every invocation -- fails immediately after revokeGrant returns.
func TestRevokeGrant_NFR7EffectiveWithoutRestart(t *testing.T) {
	store := grpcauth.NewFakeStore()
	ctx := context.Background()

	subjectKey, err := grantSubjectKey(testIssuer, "operator-a")
	require.NoError(t, err)
	require.NoError(t, store.Persist(ctx, subjectKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	// Prove the grant is usable before revoke, in the same process.
	_, err = store.TokenMaterial(ctx, subjectKey, "audience_score_system")
	require.NoError(t, err)

	require.NoError(t, revokeGrant(ctx, store, testIssuer, "operator-a", "audience_score_system", discardLogger()))

	_, err = store.TokenMaterial(ctx, subjectKey, "audience_score_system")
	require.ErrorIs(t, err, grpcauth.ErrGrantRevoked)
}

// TestRevokeGrant_LogsExactlyOneINFORecord is NFR4's audit-log guard: a
// completed revoke emits exactly one INFO record naming the revoker, the
// grant's subject, and the domain -- never WARNING/ERROR for a normal,
// successfully-completed revoke (AGENTS.md's logging-levels convention).
func TestRevokeGrant_LogsExactlyOneINFORecord(t *testing.T) {
	store := grpcauth.NewFakeStore()
	ctx := context.Background()

	subjectKey, err := grantSubjectKey(testIssuer, "operator-a")
	require.NoError(t, err)
	require.NoError(t, store.Persist(ctx, subjectKey, "audience_score_system", grpcauth.TokenMaterial{RefreshToken: "rt", ObtainedAt: time.Now()}))

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	require.NoError(t, revokeGrant(ctx, store, testIssuer, "operator-a", "audience_score_system", logger))

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	require.Len(t, lines, 1, "expected exactly one log record, got: %q", output)

	assert.Contains(t, output, "level=INFO")
	assert.Contains(t, output, "delegated grant revoked")
	assert.Contains(t, output, "operator-a")
	assert.Contains(t, output, "audience_score_system")
	assert.NotContains(t, output, "level=WARN")
	assert.NotContains(t, output, "level=ERROR")
}
