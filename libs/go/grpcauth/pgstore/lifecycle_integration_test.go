//go:build integration

// lifecycle_integration_test.go is issue #2389: one continuous, ordered
// scenario driving the whole delegated-grant lifecycle (US1-US5) against a
// real pgstore.NewGrantStore (testcontainers Postgres via libs/go/dbtest)
// plus the fake Keycloak (internal/keycloakfake) -- proving the assembled
// primitive a consuming domain actually depends on, not just each mechanism
// in isolation. The per-task suites (pgstore_integration_test.go here, and
// grpcauth's own delegatedgrant_*_test.go) already prove each mechanism on
// its own; this file wires all of them together.
//
// The subtests below run in declared order and share one fake Keycloak
// server, one Postgres-backed store, and one *grpcauth.DelegatedGrantSource
// -- state built up by an earlier subtest (a consented grant, a rotated
// refresh token, a needs_reauth grant) is exactly what a later subtest
// exercises. Do not parallelize or reorder them.
//
// keycloakfake.Server's SetSubject/SetTokenMode/SetNextRefreshToken are
// global to the fake, not per-call -- every subtest that mints or refreshes
// a token sets these explicitly immediately before the call it governs,
// rather than relying on whatever an earlier subtest last left configured.
package pgstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/internal/keycloakfake"
)

// lifecycleSourceRef breaks the construction cycle between the
// pgstore-backed grpcauth.Store (which wants a pgstore.Revoker at
// construction, see StoreConfig.Revoker) and the *grpcauth.DelegatedGrantSource
// (which wants that same Store at construction, per DelegatedGrantConfig.Store):
// the store is built first with this ref as its Revoker, and the ref's src
// field is filled in once the real source exists. This mirrors how a
// consuming domain wires a single DelegatedGrantSource to double as both its
// Store's Revoker (best-effort RFC 7009, #2388) and its own
// consent/refresh/status accessor.
type lifecycleSourceRef struct {
	src *grpcauth.DelegatedGrantSource
}

func (r *lifecycleSourceRef) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	return r.src.RevokeRefreshToken(ctx, refreshToken)
}

// newLifecycleSource builds a *grpcauth.DelegatedGrantSource pointed at
// fake's endpoints verbatim (NFR4: never a live Keycloak), with store as its
// Store.
func newLifecycleSource(t *testing.T, fake *keycloakfake.Server, store grpcauth.Store) *grpcauth.DelegatedGrantSource {
	t.Helper()
	src, err := grpcauth.NewDelegatedGrantSource(context.Background(), grpcauth.DelegatedGrantConfig{
		Issuer:       fake.URL,
		ClientID:     "lifecycle-test-client",
		ClientSecret: "lifecycle-test-client-secret",
		RedirectURI:  "https://example.invalid/lifecycle-callback",
		Store:        store,
		Endpoints: grpcauth.Endpoints{
			Authorization: fake.URL + "/authorize",
			Token:         fake.URL + "/token",
			Revocation:    fake.URL + "/revoke",
		},
	})
	if err != nil {
		t.Fatalf("grpcauth.NewDelegatedGrantSource: %v", err)
	}
	return src
}

// consent drives BeginAuthorization -> a real (non-redirect-following) GET
// of the fake's /authorize -> CompleteAuthorization end to end for
// (subject, grant), against whatever fake.SetSubject/SetNextRefreshToken the
// caller has already configured. This is the browser leg (US1); everything
// after this function returns is non-interactive.
func consent(t *testing.T, src *grpcauth.DelegatedGrantSource, subject, grant string) error {
	t.Helper()
	authURL, pending, err := src.BeginAuthorization(context.Background(), subject, grant)
	if err != nil {
		t.Fatalf("BeginAuthorization(%s, %s): %v", subject, grant, err)
	}

	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := httpClient.Get(authURL)
	if err != nil {
		t.Fatalf("GET authURL: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("GET authURL status = %d, want %d", resp.StatusCode, http.StatusFound)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	code, state := loc.Query().Get("code"), loc.Query().Get("state")

	return src.CompleteAuthorization(context.Background(), pending, state, code)
}

// lifecycleAccessTokenClaims is the subset of an access token's claims these
// tests decode -- enough to assert FR4 (unmodified sub/roles) and NFR3
// (identity isolation).
type lifecycleAccessTokenClaims struct {
	Sub         string `json:"sub"`
	RealmAccess struct { //nolint:govet // field ordering matches Keycloak's own claim shape
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// decodeAccessTokenClaims reads sub/realm_access.roles out of a JWT's
// payload segment without verifying its signature -- these tests inspect a
// token grpcauth itself just obtained over an authenticated HTTP call to the
// fake, not an externally-supplied bearer token (mirrors grpcauth's own
// subjectFromAccessToken doc comment on why verification is unnecessary
// here).
func decodeAccessTokenClaims(t *testing.T, accessToken string) lifecycleAccessTokenClaims {
	t.Helper()
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		t.Fatalf("access token is not a JWT (want 3 dot-separated parts, got %d)", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	var claims lifecycleAccessTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal JWT claims: %v", err)
	}
	return claims
}

// rolesEqual compares two role slices exactly (same values, same order):
// FR4 requires realm roles pass through unmodified, not merely "a
// permutation of".
func rolesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestLifecycle_US1ThroughUS5_EndToEnd walks the whole delegated-grant
// lifecycle -- initial consent, non-interactive fire, refresh-token rotation
// across repeated firings, multi-grant and cross-subject isolation, a broken
// grant needing re-auth, transient-failure recovery, re-consent,
// self-service revoke, and admin revoke -- as one continuous scenario over a
// real pgstore.NewGrantStore and the fake Keycloak. See the file doc comment
// for why the subtests below must not be parallelized or reordered.
func TestLifecycle_US1ThroughUS5_EndToEnd(t *testing.T) {
	ctx := context.Background()

	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: grantSchema})

	ref := &lifecycleSourceRef{}
	store, err := NewGrantStore(ctx, StoreConfig{
		Pool:          db.Pool,
		EncryptionKey: testKey(),
		Revoker:       ref,
	})
	if err != nil {
		t.Fatalf("NewGrantStore: %v", err)
	}

	src := newLifecycleSource(t, fake, store)
	ref.src = src

	aliceRoles := []string{"scheduler-operator"}
	bobRoles := []string{"scheduler-operator", "billing-viewer"}

	// --- US1: initial consent (alice, sched-1) ------------------------------

	t.Run("US1_consent", func(t *testing.T) {
		fake.SetSubject("alice", aliceRoles)
		fake.SetNextRefreshToken("rt-alice-sched1-v1")

		if err := consent(t, src, "alice", "sched-1"); err != nil {
			t.Fatalf("consent(alice, sched-1): %v", err)
		}

		status, err := src.Status(ctx, "alice", "sched-1")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status != grpcauth.GrantStatusActive {
			t.Fatalf("Status = %q, want %q", status, grpcauth.GrantStatusActive)
		}

		var raw []byte
		if err := db.Pool.QueryRow(ctx,
			"SELECT token_material FROM grpcauth_delegated_grant WHERE subject = $1 AND grant_key = $2",
			"alice", "sched-1",
		).Scan(&raw); err != nil {
			t.Fatalf("raw select token_material: %v", err)
		}
		if bytes.Contains(raw, []byte("rt-alice-sched1-v1")) {
			t.Fatal("raw token_material column contains the plaintext refresh token (NFR2 violation)")
		}
	})

	// --- US3: non-interactive fire (alice, sched-1) -------------------------

	t.Run("US3_fire_returns_live_access_token", func(t *testing.T) {
		fake.SetSubject("alice", aliceRoles)
		fake.SetNextRefreshToken("rt-alice-sched1-v2")

		tok, err := src.TokenSource("alice", "sched-1").Token(ctx)
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		claims := decodeAccessTokenClaims(t, tok.AccessToken)
		if claims.Sub != "alice" {
			t.Fatalf("access token sub = %q, want %q", claims.Sub, "alice")
		}
		if !rolesEqual(claims.RealmAccess.Roles, aliceRoles) {
			t.Fatalf("access token realm roles = %v, want %v (FR4: unmodified)", claims.RealmAccess.Roles, aliceRoles)
		}
	})

	// --- US3 (continued): refresh-token rotation holds across repeated
	// firings, not just once (FR13). -----------------------------------------

	t.Run("US3_rotation_across_firings", func(t *testing.T) {
		fake.SetSubject("alice", aliceRoles)

		rotations := []string{"rt-alice-sched1-v3", "rt-alice-sched1-v4", "rt-alice-sched1-v5"}
		expectedSent := "rt-alice-sched1-v2" // left behind by US3_fire_returns_live_access_token
		for i, next := range rotations {
			fake.SetNextRefreshToken(next)
			if _, err := src.TokenSource("alice", "sched-1").Token(ctx); err != nil {
				t.Fatalf("Token (call %d): %v", i+1, err)
			}
			if got := fake.LastRefreshToken(); got != expectedSent {
				t.Fatalf("Token (call %d) sent refresh_token = %q, want %q (the previous call's rotated value)", i+1, got, expectedSent)
			}
			expectedSent = next
		}

		material, err := store.TokenMaterial(ctx, "alice", "sched-1")
		if err != nil {
			t.Fatalf("TokenMaterial: %v", err)
		}
		if want := rotations[len(rotations)-1]; material.RefreshToken != want {
			t.Fatalf("stored RefreshToken = %q, want the last rotated value %q", material.RefreshToken, want)
		}
	})

	// --- FR2: alice also consents for sched-2; both grants mint tokens
	// independently and neither's rotation disturbs the other. ---------------

	t.Run("FR2_multi_grant_independent", func(t *testing.T) {
		fake.SetSubject("alice", aliceRoles)
		fake.SetNextRefreshToken("rt-alice-sched2-v1")
		if err := consent(t, src, "alice", "sched-2"); err != nil {
			t.Fatalf("consent(alice, sched-2): %v", err)
		}

		before1, err := store.TokenMaterial(ctx, "alice", "sched-1")
		if err != nil {
			t.Fatalf("TokenMaterial(sched-1) before: %v", err)
		}

		fake.SetNextRefreshToken("rt-alice-sched2-v2")
		if _, err := src.TokenSource("alice", "sched-2").Token(ctx); err != nil {
			t.Fatalf("Token(sched-2): %v", err)
		}

		after1, err := store.TokenMaterial(ctx, "alice", "sched-1")
		if err != nil {
			t.Fatalf("TokenMaterial(sched-1) after: %v", err)
		}
		if after1.RefreshToken != before1.RefreshToken {
			t.Fatalf("firing sched-2 changed sched-1's stored material: before %q, after %q", before1.RefreshToken, after1.RefreshToken)
		}

		before2, err := store.TokenMaterial(ctx, "alice", "sched-2")
		if err != nil {
			t.Fatalf("TokenMaterial(sched-2) before: %v", err)
		}

		fake.SetNextRefreshToken("rt-alice-sched1-v6")
		if _, err := src.TokenSource("alice", "sched-1").Token(ctx); err != nil {
			t.Fatalf("Token(sched-1): %v", err)
		}

		after2, err := store.TokenMaterial(ctx, "alice", "sched-2")
		if err != nil {
			t.Fatalf("TokenMaterial(sched-2) after: %v", err)
		}
		if after2.RefreshToken != before2.RefreshToken {
			t.Fatalf("firing sched-1 changed sched-2's stored material: before %q, after %q", before2.RefreshToken, after2.RefreshToken)
		}
	})

	// --- NFR3: bob also consents for a grant also named "sched-1"; alice's
	// and bob's grants never cross. -------------------------------------------

	t.Run("NFR3_isolation_across_subjects", func(t *testing.T) {
		fake.SetSubject("bob", bobRoles)
		fake.SetNextRefreshToken("rt-bob-sched1-v1")
		if err := consent(t, src, "bob", "sched-1"); err != nil {
			t.Fatalf("consent(bob, sched-1): %v", err)
		}
		fake.SetNextRefreshToken("rt-bob-sched2-v1")
		if err := consent(t, src, "bob", "sched-2"); err != nil {
			t.Fatalf("consent(bob, sched-2): %v", err)
		}

		// Consenting bob's grants must not touch alice's material under the
		// same grant name.
		aliceMaterial, err := store.TokenMaterial(ctx, "alice", "sched-1")
		if err != nil {
			t.Fatalf("TokenMaterial(alice, sched-1): %v", err)
		}
		if want := "rt-alice-sched1-v6"; aliceMaterial.RefreshToken != want {
			t.Fatalf("alice's sched-1 material changed after bob consented for a grant of the same name: got %q, want %q", aliceMaterial.RefreshToken, want)
		}

		fake.SetSubject("alice", aliceRoles)
		fake.SetNextRefreshToken("rt-alice-sched1-v7")
		aliceTok, err := src.TokenSource("alice", "sched-1").Token(ctx)
		if err != nil {
			t.Fatalf("Token(alice, sched-1): %v", err)
		}
		aliceClaims := decodeAccessTokenClaims(t, aliceTok.AccessToken)
		if aliceClaims.Sub != "alice" {
			t.Fatalf("alice's token sub = %q, want %q", aliceClaims.Sub, "alice")
		}

		fake.SetSubject("bob", bobRoles)
		fake.SetNextRefreshToken("rt-bob-sched1-v2")
		bobTok, err := src.TokenSource("bob", "sched-1").Token(ctx)
		if err != nil {
			t.Fatalf("Token(bob, sched-1): %v", err)
		}
		bobClaims := decodeAccessTokenClaims(t, bobTok.AccessToken)
		if bobClaims.Sub != "bob" {
			t.Fatalf("bob's token sub = %q, want %q", bobClaims.Sub, "bob")
		}
		if !rolesEqual(bobClaims.RealmAccess.Roles, bobRoles) {
			t.Fatalf("bob's token roles = %v, want %v", bobClaims.RealmAccess.Roles, bobRoles)
		}
	})

	// --- US4: fake returns invalid_grant on refresh -> Token errors with
	// ErrGrantNeedsReauth, Status reports needs_reauth without a further
	// Keycloak call, and the grant row still exists (history retained). ------

	t.Run("US4_broken_grant_needs_reauth", func(t *testing.T) {
		fake.SetSubject("alice", aliceRoles)
		fake.SetTokenMode(keycloakfake.ModeInvalidGrant)
		t.Cleanup(func() { fake.SetTokenMode(keycloakfake.ModeSuccess) })

		_, err := src.TokenSource("alice", "sched-1").Token(ctx)
		if !errors.Is(err, grpcauth.ErrGrantNeedsReauth) {
			t.Fatalf("Token error = %v, want errors.Is match for ErrGrantNeedsReauth", err)
		}
		if grpcauth.IsTransient(err) {
			t.Fatalf("Token error = %v, must not also classify as transient", err)
		}

		tokenCallsBeforeStatus := fake.TokenCalls()

		status, err := src.Status(ctx, "alice", "sched-1")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status != grpcauth.GrantStatusNeedsReauth {
			t.Fatalf("Status = %q, want %q", status, grpcauth.GrantStatusNeedsReauth)
		}
		if got := fake.TokenCalls(); got != tokenCallsBeforeStatus {
			t.Fatalf("fake.TokenCalls() changed across a Status call: before %d, after %d (Status must never call Keycloak, FR10)", tokenCallsBeforeStatus, got)
		}
	})

	// --- NFR4: a transient 500 does not pause the schedule -- the very next
	// call succeeds. -----------------------------------------------------------

	t.Run("NFR4_transient_recovery", func(t *testing.T) {
		fake.SetSubject("alice", aliceRoles)
		fake.SetTokenMode(keycloakfake.ModeServerError)

		_, err := src.TokenSource("alice", "sched-2").Token(ctx)
		if !grpcauth.IsTransient(err) {
			t.Fatalf("Token error = %v, want IsTransient", err)
		}
		if errors.Is(err, grpcauth.ErrGrantNeedsReauth) || errors.Is(err, grpcauth.ErrGrantRevoked) {
			t.Fatalf("transient error must never also match a terminal sentinel: %v", err)
		}

		status, err := src.Status(ctx, "alice", "sched-2")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status != grpcauth.GrantStatusActive {
			t.Fatalf("Status after transient failure = %q, want %q (a blip must not pause the schedule)", status, grpcauth.GrantStatusActive)
		}

		fake.SetTokenMode(keycloakfake.ModeSuccess)
		fake.SetNextRefreshToken("rt-alice-sched2-v3")
		if _, err := src.TokenSource("alice", "sched-2").Token(ctx); err != nil {
			t.Fatalf("Token immediately after the transient failure: %v, want nil error", err)
		}
	})

	// --- US1/FR11: re-consent restores a needs_reauth grant under the same
	// key. -----------------------------------------------------------------

	t.Run("US1_FR11_reconsent", func(t *testing.T) {
		var rowCountBefore int
		if err := db.Pool.QueryRow(ctx,
			"SELECT count(*) FROM grpcauth_delegated_grant WHERE subject = $1", "alice",
		).Scan(&rowCountBefore); err != nil {
			t.Fatalf("count before: %v", err)
		}

		fake.SetSubject("alice", aliceRoles)
		fake.SetTokenMode(keycloakfake.ModeSuccess)
		fake.SetNextRefreshToken("rt-alice-sched1-reauth-v1")
		if err := consent(t, src, "alice", "sched-1"); err != nil {
			t.Fatalf("re-consent(alice, sched-1): %v", err)
		}

		status, err := src.Status(ctx, "alice", "sched-1")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status != grpcauth.GrantStatusActive {
			t.Fatalf("Status after re-consent = %q, want %q", status, grpcauth.GrantStatusActive)
		}

		var rowCountAfter int
		if err := db.Pool.QueryRow(ctx,
			"SELECT count(*) FROM grpcauth_delegated_grant WHERE subject = $1", "alice",
		).Scan(&rowCountAfter); err != nil {
			t.Fatalf("count after: %v", err)
		}
		if rowCountAfter != rowCountBefore {
			t.Fatalf("row count for alice changed across re-consent: before %d, after %d, want unchanged (same key reused)", rowCountBefore, rowCountAfter)
		}

		fake.SetNextRefreshToken("rt-alice-sched1-reauth-v2")
		if _, err := src.TokenSource("alice", "sched-1").Token(ctx); err != nil {
			t.Fatalf("Token after re-consent: %v, want nil error", err)
		}
	})

	// --- US2: self-service revoke ---------------------------------------------

	t.Run("US2_self_service_revoke", func(t *testing.T) {
		tokenCallsBefore := fake.TokenCalls()
		revokeCallsBefore := fake.RevokeCalls()

		if err := store.Revoke(ctx, "alice", "sched-1"); err != nil {
			t.Fatalf("Revoke(alice, sched-1): %v", err)
		}

		status, err := src.Status(ctx, "alice", "sched-1")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status != grpcauth.GrantStatusRevoked {
			t.Fatalf("Status = %q, want %q", status, grpcauth.GrantStatusRevoked)
		}

		if _, err := src.TokenSource("alice", "sched-1").Token(ctx); !errors.Is(err, grpcauth.ErrGrantRevoked) {
			t.Fatalf("Token after revoke: err = %v, want errors.Is match for ErrGrantRevoked", err)
		}
		if got := fake.TokenCalls(); got != tokenCallsBefore {
			t.Fatalf("fake.TokenCalls() changed after revoking: before %d, after %d (FR7 short-circuit: a revoked grant must make zero Keycloak token calls)", tokenCallsBefore, got)
		}
		if got := fake.RevokeCalls(); got != revokeCallsBefore+1 {
			t.Fatalf("fake.RevokeCalls() = %d, want %d (exactly one remote RFC 7009 call)", got, revokeCallsBefore+1)
		}

		sched2Status, err := src.Status(ctx, "alice", "sched-2")
		if err != nil {
			t.Fatalf("Status(alice, sched-2): %v", err)
		}
		if sched2Status != grpcauth.GrantStatusActive {
			t.Fatalf("Status(alice, sched-2) = %q, want %q (untouched by revoking sched-1)", sched2Status, grpcauth.GrantStatusActive)
		}
	})

	// --- US5: admin revoke, invoked with no relationship to bob whatsoever --
	// the Store interface carries no "acting as" identity parameter to begin
	// with. --------------------------------------------------------------------

	t.Run("US5_admin_revoke", func(t *testing.T) {
		adminCtx := context.Background() // deliberately not derived from any bob-authenticated context

		tokenCallsBefore := fake.TokenCalls()
		revokeCallsBefore := fake.RevokeCalls()

		if err := store.Revoke(adminCtx, "bob", "sched-1"); err != nil {
			t.Fatalf("Revoke(bob, sched-1): %v", err)
		}

		status, err := src.Status(ctx, "bob", "sched-1")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status != grpcauth.GrantStatusRevoked {
			t.Fatalf("Status = %q, want %q", status, grpcauth.GrantStatusRevoked)
		}

		if _, err := src.TokenSource("bob", "sched-1").Token(ctx); !errors.Is(err, grpcauth.ErrGrantRevoked) {
			t.Fatalf("Token after admin revoke: err = %v, want errors.Is match for ErrGrantRevoked", err)
		}
		if got := fake.TokenCalls(); got != tokenCallsBefore {
			t.Fatalf("fake.TokenCalls() changed after admin revoke: before %d, after %d", tokenCallsBefore, got)
		}
		if got := fake.RevokeCalls(); got != revokeCallsBefore+1 {
			t.Fatalf("fake.RevokeCalls() = %d, want %d", got, revokeCallsBefore+1)
		}

		bobSched2Status, err := src.Status(ctx, "bob", "sched-2")
		if err != nil {
			t.Fatalf("Status(bob, sched-2): %v", err)
		}
		if bobSched2Status != grpcauth.GrantStatusActive {
			t.Fatalf("Status(bob, sched-2) = %q, want %q (bob's other grant must be untouched)", bobSched2Status, grpcauth.GrantStatusActive)
		}

		aliceSched2Status, err := src.Status(ctx, "alice", "sched-2")
		if err != nil {
			t.Fatalf("Status(alice, sched-2): %v", err)
		}
		if aliceSched2Status != grpcauth.GrantStatusActive {
			t.Fatalf("Status(alice, sched-2) = %q, want %q (a different subject's grant must be untouched)", aliceSched2Status, grpcauth.GrantStatusActive)
		}
	})

	// --- FR7: after revoke, no exported call sequence yields a token for
	// that grant -- not TokenMaterial, not Token, and not a fresh source
	// built over the same store. ------------------------------------------------

	t.Run("FR7_completeness_after_revoke", func(t *testing.T) {
		for _, subject := range []string{"alice", "bob"} {
			t.Run(subject, func(t *testing.T) {
				if _, err := store.TokenMaterial(ctx, subject, "sched-1"); !errors.Is(err, grpcauth.ErrGrantRevoked) {
					t.Fatalf("store.TokenMaterial(%s, sched-1): err = %v, want errors.Is match for ErrGrantRevoked", subject, err)
				}
				if _, err := src.TokenSource(subject, "sched-1").Token(ctx); !errors.Is(err, grpcauth.ErrGrantRevoked) {
					t.Fatalf("src.TokenSource(%s, sched-1).Token: err = %v, want errors.Is match for ErrGrantRevoked", subject, err)
				}

				freshSrc := newLifecycleSource(t, fake, store)
				if _, err := freshSrc.TokenSource(subject, "sched-1").Token(ctx); !errors.Is(err, grpcauth.ErrGrantRevoked) {
					t.Fatalf("a fresh DelegatedGrantSource over the same store: TokenSource(%s, sched-1).Token: err = %v, want errors.Is match for ErrGrantRevoked", subject, err)
				}
			})
		}
	})
}
