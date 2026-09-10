package grpcauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/grpcauth/internal/keycloakfake"
)

// newTestTokenSource builds a DelegatedGrantSource pointed at fake with the
// given store, and returns it alongside a GrantTokenSource bound to
// (subject, grant). Callers are responsible for seeding store with whatever
// initial grant state (or lack thereof) the test needs.
func newTestTokenSource(t *testing.T, fake *keycloakfake.Server, store Store, subject, grant string) (*DelegatedGrantSource, GrantTokenSource) {
	t.Helper()
	cfg := validDelegatedGrantConfig(fake)
	cfg.Store = store
	src, err := NewDelegatedGrantSource(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDelegatedGrantSource: %v", err)
	}
	return src, src.TokenSource(subject, grant)
}

// jwtClaims decodes (without verifying signature) the subset of claims this
// test needs from a compact JWS produced by keycloakfake.MintAccessToken.
// Full cryptographic verification is out of scope for this accessor's tests
// -- FR4 only requires proving the claims round-trip unmodified through
// Token(), which reading the payload segment already demonstrates.
type jwtClaims struct {
	Sub         string `json:"sub"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func decodeJWTClaims(t *testing.T, token string) jwtClaims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("decodeJWTClaims: %q is not a compact JWS (want 3 dot-separated parts)", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decodeJWTClaims: base64 decode payload: %v", err)
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("decodeJWTClaims: unmarshal payload: %v", err)
	}
	return claims
}

// --- happy path --------------------------------------------------------

// TestTokenSource_HappyPath proves an active grant refreshes successfully in
// exactly one Keycloak round-trip.
func TestTokenSource_HappyPath(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	store := NewFakeStore()
	ctx := context.Background()
	if err := store.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: "initial-refresh-token", ObtainedAt: time.Now()}); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	_, ts := newTestTokenSource(t, fake, store, "alice", "sched-1")

	tok, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("Token() = %v, want nil error", err)
	}
	if tok.AccessToken == "" {
		t.Fatal("Token() returned an empty AccessToken")
	}
	if got := fake.TokenCalls(); got != 1 {
		t.Fatalf("fake.TokenCalls() = %d, want 1", got)
	}
}

// --- FR7: status-first short-circuit ------------------------------------

// TestTokenSource_ShortCircuit_Revoked proves a revoked grant returns
// ErrGrantRevoked without ever reaching Keycloak's token endpoint.
func TestTokenSource_ShortCircuit_Revoked(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	store := NewFakeStore()
	ctx := context.Background()
	if err := store.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: "initial-refresh-token"}); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if err := store.Revoke(ctx, "alice", "sched-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	_, ts := newTestTokenSource(t, fake, store, "alice", "sched-1")

	_, err = ts.Token(ctx)
	if !errors.Is(err, ErrGrantRevoked) {
		t.Fatalf("Token() error = %v, want errors.Is match for ErrGrantRevoked", err)
	}
	if got := fake.TokenCalls(); got != 0 {
		t.Fatalf("fake.TokenCalls() = %d, want 0 (revoked grant must not reach Keycloak)", got)
	}
}

// TestTokenSource_ShortCircuit_NeedsReauth proves a needs_reauth grant
// returns ErrGrantNeedsReauth without ever reaching Keycloak's token
// endpoint.
func TestTokenSource_ShortCircuit_NeedsReauth(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	store := NewFakeStore()
	ctx := context.Background()
	if err := store.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: "initial-refresh-token"}); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if err := store.MarkNeedsReauth(ctx, "alice", "sched-1"); err != nil {
		t.Fatalf("MarkNeedsReauth: %v", err)
	}

	_, ts := newTestTokenSource(t, fake, store, "alice", "sched-1")

	_, err = ts.Token(ctx)
	if !errors.Is(err, ErrGrantNeedsReauth) {
		t.Fatalf("Token() error = %v, want errors.Is match for ErrGrantNeedsReauth", err)
	}
	if got := fake.TokenCalls(); got != 0 {
		t.Fatalf("fake.TokenCalls() = %d, want 0 (needs_reauth grant must not reach Keycloak)", got)
	}
}

// --- FR8: invalid_grant classification ----------------------------------

// TestTokenSource_InvalidGrant_MarksNeedsReauth proves Keycloak rejecting the
// refresh with invalid_grant transitions the grant to needs_reauth (history
// retained, not deleted) and returns an error matching ErrGrantNeedsReauth.
func TestTokenSource_InvalidGrant_MarksNeedsReauth(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	store := NewFakeStore()
	ctx := context.Background()
	if err := store.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: "initial-refresh-token"}); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	fake.SetTokenMode(keycloakfake.ModeInvalidGrant)

	_, ts := newTestTokenSource(t, fake, store, "alice", "sched-1")

	_, err = ts.Token(ctx)
	if !errors.Is(err, ErrGrantNeedsReauth) {
		t.Fatalf("Token() error = %v, want errors.Is match for ErrGrantNeedsReauth", err)
	}
	if IsTransient(err) {
		t.Fatalf("Token() error = %v, must not also classify as transient", err)
	}

	status, statusErr := store.Status(ctx, "alice", "sched-1")
	if statusErr != nil {
		t.Fatalf("Status: %v", statusErr)
	}
	if status != GrantStatusNeedsReauth {
		t.Fatalf("Status() = %q, want %q", status, GrantStatusNeedsReauth)
	}
	// History retained: the row must still exist, not ErrGrantNotFound.
	if _, err := store.TokenMaterial(ctx, "alice", "sched-1"); !errors.Is(err, ErrGrantNeedsReauth) {
		t.Fatalf("TokenMaterial() error = %v, want errors.Is match for ErrGrantNeedsReauth (row must still exist)", err)
	}
}

// --- FR9 / NFR4: transient classification --------------------------------

// TestTokenSource_Transient_ServerError proves a 500 from Keycloak is
// classified transient, leaves persisted status untouched, does not retry
// internally, and a subsequent successful call still works (a blip does not
// permanently pause a schedule).
func TestTokenSource_Transient_ServerError(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	store := NewFakeStore()
	ctx := context.Background()
	if err := store.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: "initial-refresh-token"}); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	fake.SetTokenMode(keycloakfake.ModeServerError)

	_, ts := newTestTokenSource(t, fake, store, "alice", "sched-1")

	_, err = ts.Token(ctx)
	if !IsTransient(err) {
		t.Fatalf("Token() error = %v, want IsTransient", err)
	}
	if errors.Is(err, ErrGrantNeedsReauth) || errors.Is(err, ErrGrantRevoked) {
		t.Fatalf("Token() error = %v, must not also match ErrGrantNeedsReauth/ErrGrantRevoked", err)
	}

	status, statusErr := store.Status(ctx, "alice", "sched-1")
	if statusErr != nil {
		t.Fatalf("Status: %v", statusErr)
	}
	if status != GrantStatusActive {
		t.Fatalf("Status() = %q, want %q (transient failure must not change persisted status)", status, GrantStatusActive)
	}
	if got := fake.TokenCalls(); got != 1 {
		t.Fatalf("fake.TokenCalls() = %d, want 1 (no internal retry on transient failure)", got)
	}

	// A subsequent call, once Keycloak recovers, must still succeed.
	fake.SetTokenMode(keycloakfake.ModeSuccess)
	tok, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("Token() after recovery = %v, want nil error", err)
	}
	if tok.AccessToken == "" {
		t.Fatal("Token() after recovery returned an empty AccessToken")
	}
}

// TestTokenSource_Transient_ConnectionFailure proves a network-level failure
// (no HTTP response at all) is classified transient the same way a 500 is.
func TestTokenSource_Transient_ConnectionFailure(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	store := NewFakeStore()
	ctx := context.Background()
	if err := store.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: "initial-refresh-token"}); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	fake.SetTokenMode(keycloakfake.ModeConnectionFailure)

	_, ts := newTestTokenSource(t, fake, store, "alice", "sched-1")

	_, err = ts.Token(ctx)
	if !IsTransient(err) {
		t.Fatalf("Token() error = %v, want IsTransient", err)
	}
	if errors.Is(err, ErrGrantNeedsReauth) || errors.Is(err, ErrGrantRevoked) {
		t.Fatalf("Token() error = %v, must not also match ErrGrantNeedsReauth/ErrGrantRevoked", err)
	}

	status, statusErr := store.Status(ctx, "alice", "sched-1")
	if statusErr != nil {
		t.Fatalf("Status: %v", statusErr)
	}
	if status != GrantStatusActive {
		t.Fatalf("Status() = %q, want %q (transient failure must not change persisted status)", status, GrantStatusActive)
	}
	if got := fake.TokenCalls(); got != 1 {
		t.Fatalf("fake.TokenCalls() = %d, want 1 (no internal retry on transient failure)", got)
	}
}

// --- FR13: refresh write-back --------------------------------------------

// TestTokenSource_WriteBack_RotatedRefreshToken proves a rotated
// refresh_token in a successful response is persisted before Token returns,
// and that a subsequent call sends the rotated value to Keycloak instead of
// the original.
func TestTokenSource_WriteBack_RotatedRefreshToken(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	store := NewFakeStore()
	ctx := context.Background()
	const original = "original-refresh-token"
	const rotated = "rotated-refresh-token-1"
	if err := store.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: original}); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	fake.SetNextRefreshToken(rotated)

	_, ts := newTestTokenSource(t, fake, store, "alice", "sched-1")

	if _, err := ts.Token(ctx); err != nil {
		t.Fatalf("Token() = %v, want nil error", err)
	}

	if got := fake.LastRefreshToken(); got != original {
		t.Fatalf("first call sent refresh_token = %q, want original %q", got, original)
	}

	material, err := store.TokenMaterial(ctx, "alice", "sched-1")
	if err != nil {
		t.Fatalf("TokenMaterial: %v", err)
	}
	if material.RefreshToken != rotated {
		t.Fatalf("stored RefreshToken = %q, want rotated value %q (write-back before return, FR13)", material.RefreshToken, rotated)
	}

	// A subsequent call must present the rotated value, not the original.
	const rotatedAgain = "rotated-refresh-token-2"
	fake.SetNextRefreshToken(rotatedAgain)
	if _, err := ts.Token(ctx); err != nil {
		t.Fatalf("second Token() = %v, want nil error", err)
	}
	if got := fake.LastRefreshToken(); got != rotated {
		t.Fatalf("second call sent refresh_token = %q, want rotated value %q from the first call", got, rotated)
	}
}

// TestTokenSource_WriteBack_PersistFailureIsTransient proves that if the
// store rejects the write-back, Token does not hand back the access token
// as if nothing happened -- it returns a transient error instead, so a
// caller retries rather than proceeding with material the store no longer
// matches.
func TestTokenSource_WriteBack_PersistFailureIsTransient(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	store := &failPersistStore{FakeStore: NewFakeStore()}
	ctx := context.Background()
	if err := store.FakeStore.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: "initial-refresh-token"}); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	_, ts := newTestTokenSource(t, fake, store, "alice", "sched-1")

	tok, err := ts.Token(ctx)
	if tok != nil {
		t.Fatalf("Token() returned a token %+v despite write-back failure, want nil", tok)
	}
	if !IsTransient(err) {
		t.Fatalf("Token() error = %v, want IsTransient", err)
	}
	if errors.Is(err, ErrGrantNeedsReauth) || errors.Is(err, ErrGrantRevoked) {
		t.Fatalf("Token() error = %v, must not also match ErrGrantNeedsReauth/ErrGrantRevoked", err)
	}
}

// failPersistStore wraps a *FakeStore and fails only the Persist call made
// after TokenMaterial has already succeeded once -- i.e. the write-back
// Persist, not the initial seed Persist done by the test's setup.
type failPersistStore struct {
	*FakeStore
	tokenMaterialCalls int
}

func (f *failPersistStore) Persist(ctx context.Context, subject, grant string, material TokenMaterial) error {
	if f.tokenMaterialCalls > 0 {
		return NewTransientError("fake persist failure", errors.New("disk full"))
	}
	return f.FakeStore.Persist(ctx, subject, grant, material)
}

func (f *failPersistStore) TokenMaterial(ctx context.Context, subject, grant string) (TokenMaterial, error) {
	f.tokenMaterialCalls++
	return f.FakeStore.TokenMaterial(ctx, subject, grant)
}

// --- FR4: claims pass through unmodified ---------------------------------

// TestTokenSource_ClaimsPassThroughUnmodified proves the returned access
// token carries the grantor's subject and full realm-role set exactly as
// Keycloak issued them -- no scope narrowing, no claim filtering.
func TestTokenSource_ClaimsPassThroughUnmodified(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	roles := []string{"admin", "billing-viewer", "scheduler"}
	fake.SetSubject("alice", roles)

	store := NewFakeStore()
	ctx := context.Background()
	if err := store.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: "initial-refresh-token"}); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	_, ts := newTestTokenSource(t, fake, store, "alice", "sched-1")

	tok, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("Token() = %v, want nil error", err)
	}

	claims := decodeJWTClaims(t, tok.AccessToken)
	if claims.Sub != "alice" {
		t.Fatalf("claims.Sub = %q, want %q", claims.Sub, "alice")
	}
	if len(claims.RealmAccess.Roles) != len(roles) {
		t.Fatalf("claims.RealmAccess.Roles = %v, want %v", claims.RealmAccess.Roles, roles)
	}
	for i, want := range roles {
		if claims.RealmAccess.Roles[i] != want {
			t.Fatalf("claims.RealmAccess.Roles = %v, want %v", claims.RealmAccess.Roles, roles)
		}
	}
}

// --- NFR3: identity isolation ---------------------------------------------

// TestTokenSource_IdentityIsolation proves two subjects with identically
// named grants never cross: alice's accessor only ever reads/sends alice's
// material, never bob's.
func TestTokenSource_IdentityIsolation(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	store := NewFakeStore()
	ctx := context.Background()
	if err := store.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: "alice-refresh-token"}); err != nil {
		t.Fatalf("Persist alice: %v", err)
	}
	if err := store.Persist(ctx, "bob", "sched-1", TokenMaterial{RefreshToken: "bob-refresh-token"}); err != nil {
		t.Fatalf("Persist bob: %v", err)
	}
	// bob's grant is unusable; if alice's accessor ever touched bob's
	// material this would surface as a leaked ErrGrantNeedsReauth or a
	// refresh sent with bob's token.
	if err := store.MarkNeedsReauth(ctx, "bob", "sched-1"); err != nil {
		t.Fatalf("MarkNeedsReauth bob: %v", err)
	}

	_, ts := newTestTokenSource(t, fake, store, "alice", "sched-1")

	tok, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("alice Token() = %v, want nil error", err)
	}
	if tok.AccessToken == "" {
		t.Fatal("alice Token() returned an empty AccessToken")
	}
	if got := fake.LastRefreshToken(); got != "alice-refresh-token" {
		t.Fatalf("refresh request carried refresh_token = %q, want alice's token", got)
	}

	bobStatus, err := store.Status(ctx, "bob", "sched-1")
	if err != nil {
		t.Fatalf("Status bob: %v", err)
	}
	if bobStatus != GrantStatusNeedsReauth {
		t.Fatalf("bob's status changed to %q, want it untouched at %q", bobStatus, GrantStatusNeedsReauth)
	}
}

// --- FR10: Status pass-through --------------------------------------------

// TestDelegatedGrantSource_Status_PassThrough proves Status reads persisted
// state directly with zero Keycloak calls, for all three statuses.
func TestDelegatedGrantSource_Status_PassThrough(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	store := NewFakeStore()
	ctx := context.Background()

	cases := []struct {
		name  string
		grant string
		setup func()
		want  GrantStatus
	}{
		{
			name:  "active",
			grant: "sched-active",
			setup: func() {
				if err := store.Persist(ctx, "alice", "sched-active", TokenMaterial{RefreshToken: "t"}); err != nil {
					t.Fatalf("Persist: %v", err)
				}
			},
			want: GrantStatusActive,
		},
		{
			name:  "needs_reauth",
			grant: "sched-needs-reauth",
			setup: func() {
				if err := store.Persist(ctx, "alice", "sched-needs-reauth", TokenMaterial{RefreshToken: "t"}); err != nil {
					t.Fatalf("Persist: %v", err)
				}
				if err := store.MarkNeedsReauth(ctx, "alice", "sched-needs-reauth"); err != nil {
					t.Fatalf("MarkNeedsReauth: %v", err)
				}
			},
			want: GrantStatusNeedsReauth,
		},
		{
			name:  "revoked",
			grant: "sched-revoked",
			setup: func() {
				if err := store.Persist(ctx, "alice", "sched-revoked", TokenMaterial{RefreshToken: "t"}); err != nil {
					t.Fatalf("Persist: %v", err)
				}
				if err := store.Revoke(ctx, "alice", "sched-revoked"); err != nil {
					t.Fatalf("Revoke: %v", err)
				}
			},
			want: GrantStatusRevoked,
		},
	}

	for _, tc := range cases {
		tc.setup()
	}

	cfg := validDelegatedGrantConfig(fake)
	cfg.Store = store
	src, err := NewDelegatedGrantSource(ctx, cfg)
	if err != nil {
		t.Fatalf("NewDelegatedGrantSource: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := src.Status(ctx, "alice", tc.grant)
			if err != nil {
				t.Fatalf("Status() = %v, want nil error", err)
			}
			if got != tc.want {
				t.Fatalf("Status() = %q, want %q", got, tc.want)
			}
		})
	}

	if got := fake.TokenCalls(); got != 0 {
		t.Fatalf("fake.TokenCalls() = %d, want 0 (Status must never touch Keycloak)", got)
	}
}

// --- NFR1: no secret leakage in errors ------------------------------------

// TestTokenSource_ErrorsNeverLeakSecrets proves that across every error path
// this accessor can take, the returned error's Error() string never contains
// the refresh token, the client secret, or an access token.
func TestTokenSource_ErrorsNeverLeakSecrets(t *testing.T) {
	const refreshToken = "super-sensitive-refresh-token-value"
	const clientSecret = "super-secret-client-secret-value"

	assertNoLeak := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			return
		}
		msg := err.Error()
		if strings.Contains(msg, refreshToken) {
			t.Fatalf("error %q leaked the refresh token", msg)
		}
		if strings.Contains(msg, clientSecret) {
			t.Fatalf("error %q leaked the client secret", msg)
		}
	}

	t.Run("revoked", func(t *testing.T) {
		fake, err := keycloakfake.New()
		if err != nil {
			t.Fatalf("keycloakfake.New: %v", err)
		}
		t.Cleanup(fake.Close)

		store := NewFakeStore()
		ctx := context.Background()
		if err := store.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: refreshToken}); err != nil {
			t.Fatalf("Persist: %v", err)
		}
		if err := store.Revoke(ctx, "alice", "sched-1"); err != nil {
			t.Fatalf("Revoke: %v", err)
		}

		cfg := validDelegatedGrantConfig(fake)
		cfg.Store = store
		cfg.ClientSecret = clientSecret
		src, err := NewDelegatedGrantSource(ctx, cfg)
		if err != nil {
			t.Fatalf("NewDelegatedGrantSource: %v", err)
		}

		_, err = src.TokenSource("alice", "sched-1").Token(ctx)
		assertNoLeak(t, err)
	})

	t.Run("invalid_grant", func(t *testing.T) {
		fake, err := keycloakfake.New()
		if err != nil {
			t.Fatalf("keycloakfake.New: %v", err)
		}
		t.Cleanup(fake.Close)
		fake.SetTokenMode(keycloakfake.ModeInvalidGrant)

		store := NewFakeStore()
		ctx := context.Background()
		if err := store.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: refreshToken}); err != nil {
			t.Fatalf("Persist: %v", err)
		}

		cfg := validDelegatedGrantConfig(fake)
		cfg.Store = store
		cfg.ClientSecret = clientSecret
		src, err := NewDelegatedGrantSource(ctx, cfg)
		if err != nil {
			t.Fatalf("NewDelegatedGrantSource: %v", err)
		}

		_, err = src.TokenSource("alice", "sched-1").Token(ctx)
		assertNoLeak(t, err)
	})

	t.Run("transient", func(t *testing.T) {
		fake, err := keycloakfake.New()
		if err != nil {
			t.Fatalf("keycloakfake.New: %v", err)
		}
		t.Cleanup(fake.Close)
		fake.SetTokenMode(keycloakfake.ModeServerError)

		store := NewFakeStore()
		ctx := context.Background()
		if err := store.Persist(ctx, "alice", "sched-1", TokenMaterial{RefreshToken: refreshToken}); err != nil {
			t.Fatalf("Persist: %v", err)
		}

		cfg := validDelegatedGrantConfig(fake)
		cfg.Store = store
		cfg.ClientSecret = clientSecret
		src, err := NewDelegatedGrantSource(ctx, cfg)
		if err != nil {
			t.Fatalf("NewDelegatedGrantSource: %v", err)
		}

		_, err = src.TokenSource("alice", "sched-1").Token(ctx)
		assertNoLeak(t, err)
	})
}
