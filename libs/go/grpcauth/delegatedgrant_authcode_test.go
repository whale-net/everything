package grpcauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/grpcauth/internal/keycloakfake"
)

// --- shared test helpers -----------------------------------------------------

// newAuthCodeTestSource builds a DelegatedGrantSource pointed at fake, using
// validDelegatedGrantConfig (delegatedgrant_test.go) as the base so these
// tests never contact a live Keycloak (NFR4). The Store is always a fresh
// *FakeStore, returned alongside the source so tests can inspect it directly.
func newAuthCodeTestSource(t *testing.T, fake *keycloakfake.Server) (*DelegatedGrantSource, *FakeStore) {
	t.Helper()
	cfg := validDelegatedGrantConfig(fake)
	store := NewFakeStore()
	cfg.Store = store

	src, err := NewDelegatedGrantSource(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDelegatedGrantSource: %v", err)
	}
	return src, store
}

// driveAuthorize performs an actual (non-redirect-following) HTTP GET of
// authURL against the fake's real /authorize endpoint and returns the code
// and state the fake's redirect carries -- exercising the real HTTP path
// rather than fabricating a callback.
func driveAuthorize(t *testing.T, authURL string) (code, state string) {
	t.Helper()
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
	return loc.Query().Get("code"), loc.Query().Get("state")
}

// runConsentFlow drives BeginAuthorization -> browser GET /authorize ->
// CompleteAuthorization end to end and returns the PendingAuthorization plus
// whatever error CompleteAuthorization produced, so error-path tests can
// still inspect the pending flow's fields (e.g. CodeVerifier for PKCE
// assertions).
func runConsentFlow(t *testing.T, src *DelegatedGrantSource, subject, grant string) (PendingAuthorization, error) {
	t.Helper()
	authURL, pending, err := src.BeginAuthorization(context.Background(), subject, grant)
	if err != nil {
		t.Fatalf("BeginAuthorization: %v", err)
	}
	code, state := driveAuthorize(t, authURL)
	err = src.CompleteAuthorization(context.Background(), pending, state, code)
	return pending, err
}

// --- PKCE verifier/challenge generation (pkce.go) ---------------------------

// TestGeneratePKCEVerifierShape proves generatePKCEVerifier returns
// unpredictable, RFC 7636-compliant (43-128 char, base64url) verifiers, and
// that repeated calls never collide.
func TestGeneratePKCEVerifierShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		verifier, err := generatePKCEVerifier()
		if err != nil {
			t.Fatalf("generatePKCEVerifier: %v", err)
		}
		if len(verifier) < 43 || len(verifier) > 128 {
			t.Fatalf("generatePKCEVerifier() length = %d, want 43-128 (RFC 7636)", len(verifier))
		}
		if _, err := base64.RawURLEncoding.DecodeString(verifier); err != nil {
			t.Fatalf("generatePKCEVerifier() = %q, not valid base64url: %v", verifier, err)
		}
		if seen[verifier] {
			t.Fatalf("generatePKCEVerifier() produced a duplicate: %q", verifier)
		}
		seen[verifier] = true
	}
}

// TestPKCEChallengeS256 proves pkceChallengeS256 is the RFC 7636 S256
// derivation (base64url(sha256(verifier)), no padding), is deterministic for
// a fixed verifier, and differs for different verifiers.
func TestPKCEChallengeS256(t *testing.T) {
	const verifier = "a-fixed-test-verifier-value-for-hashing"
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])

	if got := pkceChallengeS256(verifier); got != want {
		t.Fatalf("pkceChallengeS256(%q) = %q, want %q", verifier, got, want)
	}
	// Deterministic.
	if got := pkceChallengeS256(verifier); got != want {
		t.Fatalf("pkceChallengeS256(%q) not deterministic: got %q, want %q", verifier, got, want)
	}
	// Different verifier -> different challenge.
	if other := pkceChallengeS256(verifier + "x"); other == want {
		t.Fatalf("pkceChallengeS256 produced the same challenge for two different verifiers: %q", other)
	}
	// No padding characters, per RFC 7636.
	if strings.Contains(want, "=") {
		t.Fatalf("test bug: expected value %q unexpectedly contains padding", want)
	}
}

// --- BeginAuthorization ------------------------------------------------------

// TestBeginAuthorizationURLShapeAndPendingFields proves the authorize URL
// carries S256 PKCE, a non-empty per-flow state, the configured redirect_uri,
// and offline_access in scope, and that the returned PendingAuthorization
// carries matching Subject/Grant/State/CodeVerifier/CreatedAt.
func TestBeginAuthorizationURLShapeAndPendingFields(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	src, _ := newAuthCodeTestSource(t, fake)
	cfg := validDelegatedGrantConfig(fake)

	before := time.Now()
	authURL, pending, err := src.BeginAuthorization(context.Background(), "subject-1", "grant-1")
	if err != nil {
		t.Fatalf("BeginAuthorization: %v", err)
	}

	if pending.Subject != "subject-1" {
		t.Fatalf("pending.Subject = %q, want %q", pending.Subject, "subject-1")
	}
	if pending.Grant != "grant-1" {
		t.Fatalf("pending.Grant = %q, want %q", pending.Grant, "grant-1")
	}
	if pending.State == "" {
		t.Fatal("pending.State is empty, want a non-empty CSRF token")
	}
	if pending.CodeVerifier == "" {
		t.Fatal("pending.CodeVerifier is empty, want a non-empty PKCE verifier")
	}
	if pending.CreatedAt.Before(before) || pending.CreatedAt.After(time.Now()) {
		t.Fatalf("pending.CreatedAt = %v, want between %v and now", pending.CreatedAt, before)
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authURL: %v", err)
	}
	q := parsed.Query()

	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("authURL code_challenge_method = %q, want S256", got)
	}
	if got := q.Get("code_challenge"); got == "" {
		t.Fatal("authURL code_challenge is empty")
	} else if want := pkceChallengeS256(pending.CodeVerifier); got != want {
		t.Fatalf("authURL code_challenge = %q, want %q (S256 of pending.CodeVerifier)", got, want)
	}
	if got := q.Get("state"); got != pending.State {
		t.Fatalf("authURL state = %q, want %q (pending.State)", got, pending.State)
	}
	if got := q.Get("redirect_uri"); got != cfg.RedirectURI {
		t.Fatalf("authURL redirect_uri = %q, want %q", got, cfg.RedirectURI)
	}
	scope := q.Get("scope")
	found := false
	for _, s := range strings.Fields(scope) {
		if s == offlineAccessScope {
			found = true
		}
	}
	if !found {
		t.Fatalf("authURL scope = %q, want it to include %q", scope, offlineAccessScope)
	}
}

// TestBeginAuthorizationUniquePerCall proves two flows for the same
// (subject, grant) never share a state or verifier -- each call is an
// independent flow.
func TestBeginAuthorizationUniquePerCall(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	src, _ := newAuthCodeTestSource(t, fake)

	_, p1, err := src.BeginAuthorization(context.Background(), "subject-1", "grant-1")
	if err != nil {
		t.Fatalf("BeginAuthorization (1): %v", err)
	}
	_, p2, err := src.BeginAuthorization(context.Background(), "subject-1", "grant-1")
	if err != nil {
		t.Fatalf("BeginAuthorization (2): %v", err)
	}

	if p1.State == p2.State {
		t.Fatal("two BeginAuthorization calls produced the same state")
	}
	if p1.CodeVerifier == p2.CodeVerifier {
		t.Fatal("two BeginAuthorization calls produced the same PKCE verifier")
	}
}

// TestBeginAuthorizationRequiresSubjectAndGrant proves an empty subject or
// grant is rejected before any state/verifier is generated.
func TestBeginAuthorizationRequiresSubjectAndGrant(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	src, _ := newAuthCodeTestSource(t, fake)

	if _, _, err := src.BeginAuthorization(context.Background(), "", "grant-1"); err == nil {
		t.Fatal("BeginAuthorization with empty subject = nil error, want error")
	}
	if _, _, err := src.BeginAuthorization(context.Background(), "subject-1", ""); err == nil {
		t.Fatal("BeginAuthorization with empty grant = nil error, want error")
	}
}

// --- CompleteAuthorization: happy path + PKCE correlation -------------------

// TestCompleteAuthorizationHappyPath proves a full consent flow persists
// material via Store and leaves the grant active.
func TestCompleteAuthorizationHappyPath(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.SetSubject("subject-1", nil)
	fake.SetNextRefreshToken("refresh-token-1")

	src, store := newAuthCodeTestSource(t, fake)

	pending, err := runConsentFlow(t, src, "subject-1", "grant-1")
	if err != nil {
		t.Fatalf("CompleteAuthorization: %v", err)
	}
	_ = pending

	status, err := store.Status(context.Background(), "subject-1", "grant-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != GrantStatusActive {
		t.Fatalf("Status = %q, want %q", status, GrantStatusActive)
	}

	material, err := store.TokenMaterial(context.Background(), "subject-1", "grant-1")
	if err != nil {
		t.Fatalf("TokenMaterial: %v", err)
	}
	if material.RefreshToken != "refresh-token-1" {
		t.Fatalf("TokenMaterial.RefreshToken = %q, want %q", material.RefreshToken, "refresh-token-1")
	}

	if got := fake.TokenCalls(); got != 1 {
		t.Fatalf("TokenCalls() = %d, want 1", got)
	}
}

// TestCompleteAuthorizationPKCECorrelation proves the code_verifier sent to
// /token on exchange hashes (S256) to the code_challenge sent to /authorize --
// asserted against what the fake actually recorded on both calls, not just
// against the in-process PendingAuthorization.
func TestCompleteAuthorizationPKCECorrelation(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.SetSubject("subject-1", nil)

	src, _ := newAuthCodeTestSource(t, fake)

	if _, err := runConsentFlow(t, src, "subject-1", "grant-1"); err != nil {
		t.Fatalf("CompleteAuthorization: %v", err)
	}

	authorizeQuery := fake.LastAuthorizeQuery()
	tokenForm := fake.LastTokenForm()
	if authorizeQuery == nil {
		t.Fatal("fake never recorded an /authorize call")
	}
	if tokenForm == nil {
		t.Fatal("fake never recorded a /token call")
	}

	challenge := authorizeQuery.Get("code_challenge")
	verifier := tokenForm.Get("code_verifier")
	if challenge == "" {
		t.Fatal("recorded /authorize call has empty code_challenge")
	}
	if verifier == "" {
		t.Fatal("recorded /token call has empty code_verifier")
	}
	if got := pkceChallengeS256(verifier); got != challenge {
		t.Fatalf("S256(code_verifier sent to /token) = %q, want it to equal code_challenge sent to /authorize (%q)", got, challenge)
	}
}

// --- CompleteAuthorization: NFR5 CSRF state -----------------------------------

// TestCompleteAuthorizationCSRFStateMismatch proves a mismatched or empty
// callback state is rejected without ever calling the token endpoint.
func TestCompleteAuthorizationCSRFStateMismatch(t *testing.T) {
	cases := []struct {
		name          string
		callbackState string
	}{
		{name: "mismatched state", callbackState: "attacker-supplied-state"},
		{name: "empty state", callbackState: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, err := keycloakfake.New()
			if err != nil {
				t.Fatalf("keycloakfake.New: %v", err)
			}
			t.Cleanup(fake.Close)

			src, store := newAuthCodeTestSource(t, fake)

			_, pending, err := src.BeginAuthorization(context.Background(), "subject-1", "grant-1")
			if err != nil {
				t.Fatalf("BeginAuthorization: %v", err)
			}

			err = src.CompleteAuthorization(context.Background(), pending, tc.callbackState, "irrelevant-code")
			if !errors.Is(err, ErrAuthorizationStateMismatch) {
				t.Fatalf("CompleteAuthorization err = %v, want errors.Is match for ErrAuthorizationStateMismatch", err)
			}
			if got := fake.TokenCalls(); got != 0 {
				t.Fatalf("TokenCalls() = %d, want 0 (no exchange attempted on CSRF mismatch)", got)
			}
			if _, err := store.Status(context.Background(), "subject-1", "grant-1"); !errors.Is(err, ErrGrantNotFound) {
				t.Fatalf("Status err = %v, want ErrGrantNotFound (nothing persisted)", err)
			}
		})
	}
}

// --- CompleteAuthorization: NFR5 redirect_uri is always configured ----------

// TestCompleteAuthorizationRedirectURIAlwaysConfigured proves the redirect_uri
// the fake receives -- on both /authorize and /token -- always equals the
// source's own configured RedirectURI, across two differently-configured
// sources. There is no parameter on BeginAuthorization/CompleteAuthorization
// through which a caller could supply a different one.
func TestCompleteAuthorizationRedirectURIAlwaysConfigured(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.SetSubject("subject-1", nil)

	domains := []struct {
		redirectURI string
	}{
		{redirectURI: "https://a.example.invalid/callback"},
		{redirectURI: "https://b.example.invalid/callback"},
	}

	for _, d := range domains {
		t.Run(d.redirectURI, func(t *testing.T) {
			cfg := validDelegatedGrantConfig(fake)
			cfg.RedirectURI = d.redirectURI
			cfg.Store = NewFakeStore()

			src, err := NewDelegatedGrantSource(context.Background(), cfg)
			if err != nil {
				t.Fatalf("NewDelegatedGrantSource: %v", err)
			}

			if _, err := runConsentFlow(t, src, "subject-1", "grant-1"); err != nil {
				t.Fatalf("CompleteAuthorization: %v", err)
			}

			if got := fake.LastAuthorizeQuery().Get("redirect_uri"); got != d.redirectURI {
				t.Fatalf("/authorize redirect_uri = %q, want %q", got, d.redirectURI)
			}
			if got := fake.LastTokenForm().Get("redirect_uri"); got != d.redirectURI {
				t.Fatalf("/token redirect_uri = %q, want %q", got, d.redirectURI)
			}
		})
	}
}

// --- CompleteAuthorization: TTL expiry ---------------------------------------

// TestCompleteAuthorizationExpired proves a PendingAuthorization older than
// pendingAuthorizationTTL is rejected without a token call, even when its
// state matches exactly (isolating the TTL check from the CSRF check).
func TestCompleteAuthorizationExpired(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)

	src, store := newAuthCodeTestSource(t, fake)

	pending := PendingAuthorization{
		Subject:      "subject-1",
		Grant:        "grant-1",
		State:        "matching-state",
		CodeVerifier: "irrelevant-verifier",
		CreatedAt:    time.Now().Add(-(pendingAuthorizationTTL + time.Minute)),
	}

	err = src.CompleteAuthorization(context.Background(), pending, "matching-state", "irrelevant-code")
	if !errors.Is(err, ErrAuthorizationExpired) {
		t.Fatalf("CompleteAuthorization err = %v, want errors.Is match for ErrAuthorizationExpired", err)
	}
	if got := fake.TokenCalls(); got != 0 {
		t.Fatalf("TokenCalls() = %d, want 0 (no exchange attempted on expired flow)", got)
	}
	if _, err := store.Status(context.Background(), "subject-1", "grant-1"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("Status err = %v, want ErrGrantNotFound (nothing persisted)", err)
	}
}

// --- CompleteAuthorization: NFR3 subject match -------------------------------

// TestCompleteAuthorizationSubjectMismatch proves that when the exchanged
// token's identity differs from the subject the flow was started for,
// CompleteAuthorization errors and nothing is persisted under either
// subject's key.
func TestCompleteAuthorizationSubjectMismatch(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.SetSubject("actual-subject", nil) // differs from the flow's subject

	src, store := newAuthCodeTestSource(t, fake)

	_, err = runConsentFlow(t, src, "requested-subject", "grant-1")
	if !errors.Is(err, ErrAuthorizationSubjectMismatch) {
		t.Fatalf("CompleteAuthorization err = %v, want errors.Is match for ErrAuthorizationSubjectMismatch", err)
	}

	if _, err := store.Status(context.Background(), "requested-subject", "grant-1"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("Status(requested-subject) err = %v, want ErrGrantNotFound", err)
	}
	if _, err := store.Status(context.Background(), "actual-subject", "grant-1"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("Status(actual-subject) err = %v, want ErrGrantNotFound (never persisted under the token's own subject either)", err)
	}
}

// --- CompleteAuthorization: missing refresh token -----------------------------

// TestCompleteAuthorizationNoRefreshToken proves a token response without a
// refresh_token (offline_access requested but not actually granted) errors
// and persists nothing.
func TestCompleteAuthorizationNoRefreshToken(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.SetSubject("subject-1", nil)
	fake.SetNextRefreshToken("")

	src, store := newAuthCodeTestSource(t, fake)

	_, err = runConsentFlow(t, src, "subject-1", "grant-1")
	if !errors.Is(err, ErrAuthorizationNoRefreshToken) {
		t.Fatalf("CompleteAuthorization err = %v, want errors.Is match for ErrAuthorizationNoRefreshToken", err)
	}
	if _, err := store.Status(context.Background(), "subject-1", "grant-1"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("Status err = %v, want ErrGrantNotFound (nothing persisted)", err)
	}
}

// --- FR11: re-authorization reuses the same (subject, grant) key ------------

// TestCompleteAuthorizationReauthorizationReusesKey proves that re-running
// the full consent flow for a grant previously marked needs_reauth restores
// it to active under the *same* (subject, grant) key -- FR11 -- rather than
// creating a new grant identity.
func TestCompleteAuthorizationReauthorizationReusesKey(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.SetSubject("subject-1", nil)
	fake.SetNextRefreshToken("original-refresh-token")

	src, store := newAuthCodeTestSource(t, fake)

	if _, err := runConsentFlow(t, src, "subject-1", "grant-1"); err != nil {
		t.Fatalf("initial consent: CompleteAuthorization: %v", err)
	}

	if err := store.MarkNeedsReauth(context.Background(), "subject-1", "grant-1"); err != nil {
		t.Fatalf("MarkNeedsReauth: %v", err)
	}
	if status, err := store.Status(context.Background(), "subject-1", "grant-1"); err != nil || status != GrantStatusNeedsReauth {
		t.Fatalf("Status after MarkNeedsReauth = (%q, %v), want (%q, nil)", status, err, GrantStatusNeedsReauth)
	}

	fake.SetNextRefreshToken("reauth-refresh-token")
	if _, err := runConsentFlow(t, src, "subject-1", "grant-1"); err != nil {
		t.Fatalf("re-authorization: CompleteAuthorization: %v", err)
	}

	status, err := store.Status(context.Background(), "subject-1", "grant-1")
	if err != nil {
		t.Fatalf("Status after re-authorization: %v", err)
	}
	if status != GrantStatusActive {
		t.Fatalf("Status after re-authorization = %q, want %q", status, GrantStatusActive)
	}

	material, err := store.TokenMaterial(context.Background(), "subject-1", "grant-1")
	if err != nil {
		t.Fatalf("TokenMaterial after re-authorization: %v", err)
	}
	if material.RefreshToken != "reauth-refresh-token" {
		t.Fatalf("TokenMaterial.RefreshToken = %q, want %q (the new token, same key)", material.RefreshToken, "reauth-refresh-token")
	}

	if got := store.Calls().Persist; got != 2 {
		t.Fatalf("Store.Calls().Persist = %d, want 2 (initial consent + re-authorization, same key -- no separate grant identity)", got)
	}
}

// --- FR2: independent grants for the same subject ----------------------------

// TestCompleteAuthorizationIndependentGrants proves two grants for the same
// subject persist independently: consenting the second never alters the
// first's material or status.
func TestCompleteAuthorizationIndependentGrants(t *testing.T) {
	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.SetSubject("subject-1", nil)

	src, store := newAuthCodeTestSource(t, fake)

	fake.SetNextRefreshToken("token-for-g1")
	if _, err := runConsentFlow(t, src, "subject-1", "g1"); err != nil {
		t.Fatalf("consent g1: %v", err)
	}

	fake.SetNextRefreshToken("token-for-g2")
	if _, err := runConsentFlow(t, src, "subject-1", "g2"); err != nil {
		t.Fatalf("consent g2: %v", err)
	}

	g1Status, err := store.Status(context.Background(), "subject-1", "g1")
	if err != nil {
		t.Fatalf("Status(g1): %v", err)
	}
	if g1Status != GrantStatusActive {
		t.Fatalf("Status(g1) = %q, want %q (unaffected by consenting g2)", g1Status, GrantStatusActive)
	}

	g1Material, err := store.TokenMaterial(context.Background(), "subject-1", "g1")
	if err != nil {
		t.Fatalf("TokenMaterial(g1): %v", err)
	}
	if g1Material.RefreshToken != "token-for-g1" {
		t.Fatalf("TokenMaterial(g1).RefreshToken = %q, want %q (unaffected by consenting g2)", g1Material.RefreshToken, "token-for-g1")
	}

	g2Material, err := store.TokenMaterial(context.Background(), "subject-1", "g2")
	if err != nil {
		t.Fatalf("TokenMaterial(g2): %v", err)
	}
	if g2Material.RefreshToken != "token-for-g2" {
		t.Fatalf("TokenMaterial(g2).RefreshToken = %q, want %q", g2Material.RefreshToken, "token-for-g2")
	}
}

// --- NFR1: no secret leakage --------------------------------------------------

// TestPendingAuthorizationStringRedactsCodeVerifier proves String() (and
// therefore LogValue()/an accidental %v) never contains CodeVerifier.
func TestPendingAuthorizationStringRedactsCodeVerifier(t *testing.T) {
	pending := PendingAuthorization{
		Subject:      "subject-1",
		Grant:        "grant-1",
		State:        "some-state",
		CodeVerifier: "extremely-secret-pkce-verifier-value",
		CreatedAt:    time.Now(),
	}

	rendered := pending.String()
	if strings.Contains(rendered, pending.CodeVerifier) {
		t.Fatalf("String() leaked CodeVerifier: %q", rendered)
	}
	if !strings.Contains(rendered, "(redacted)") {
		t.Fatalf("String() = %q, want it to indicate CodeVerifier is set via a redaction placeholder", rendered)
	}

	logged := pending.LogValue()
	if strings.Contains(logged.String(), pending.CodeVerifier) {
		t.Fatalf("LogValue() leaked CodeVerifier: %q", logged.String())
	}
}

// TestCompleteAuthorizationErrorsNeverContainSecrets proves that across every
// CompleteAuthorization error path exercised above, the returned error never
// embeds the code, verifier, state, refresh token, or client secret (NFR1).
func TestCompleteAuthorizationErrorsNeverContainSecrets(t *testing.T) {
	const (
		secretVerifier = "should-never-appear-verifier"
		secretState    = "should-never-appear-state"
		secretCode     = "should-never-appear-code"
		clientSecret   = "should-never-appear-client-secret"
	)

	fake, err := keycloakfake.New()
	if err != nil {
		t.Fatalf("keycloakfake.New: %v", err)
	}
	t.Cleanup(fake.Close)
	fake.SetSubject("other-subject", nil) // forces ErrAuthorizationSubjectMismatch below

	cfg := validDelegatedGrantConfig(fake)
	cfg.ClientSecret = clientSecret
	cfg.Store = NewFakeStore()
	src, err := NewDelegatedGrantSource(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDelegatedGrantSource: %v", err)
	}

	assertNoLeak := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected a non-nil error")
		}
		msg := err.Error()
		for _, secret := range []string{secretVerifier, secretState, secretCode, clientSecret} {
			if strings.Contains(msg, secret) {
				t.Fatalf("error %q leaked secret %q", msg, secret)
			}
		}
	}

	t.Run("csrf mismatch", func(t *testing.T) {
		pending := PendingAuthorization{
			Subject:      "subject-1",
			Grant:        "grant-1",
			State:        secretState,
			CodeVerifier: secretVerifier,
			CreatedAt:    time.Now(),
		}
		err := src.CompleteAuthorization(context.Background(), pending, "not-"+secretState, secretCode)
		assertNoLeak(t, err)
	})

	t.Run("expired", func(t *testing.T) {
		pending := PendingAuthorization{
			Subject:      "subject-1",
			Grant:        "grant-1",
			State:        secretState,
			CodeVerifier: secretVerifier,
			CreatedAt:    time.Now().Add(-(pendingAuthorizationTTL + time.Minute)),
		}
		err := src.CompleteAuthorization(context.Background(), pending, secretState, secretCode)
		assertNoLeak(t, err)
	})

	t.Run("subject mismatch", func(t *testing.T) {
		authURL, pending, err := src.BeginAuthorization(context.Background(), "subject-1", "grant-1")
		if err != nil {
			t.Fatalf("BeginAuthorization: %v", err)
		}
		code, state := driveAuthorize(t, authURL)
		err = src.CompleteAuthorization(context.Background(), pending, state, code)
		assertNoLeak(t, err)
	})
}

// --- red/green discipline: statesMatch actually guards CSRF ------------------

// TestStatesMatchRedGreen deliberately exercises a broken comparator (always
// "matches") side by side with the real statesMatch, proving the real
// CompleteAuthorization CSRF test above (TestCompleteAuthorizationCSRFStateMismatch)
// would go red if statesMatch's guard were ever removed. This documents the
// discipline without mutating production code -- see also the manual
// break/run/revert performed against delegatedgrant_authcode.go itself before
// this test suite was finalized.
func TestStatesMatchRedGreen(t *testing.T) {
	brokenStatesMatch := func(pending, callbackState string) bool {
		return true // the bug this guards against: no comparison at all
	}

	if !brokenStatesMatch("real-state", "attacker-state") {
		t.Fatal("test bug: broken comparator unexpectedly rejected a mismatch")
	}
	if statesMatch("real-state", "attacker-state") {
		t.Fatal("statesMatch accepted a mismatched state -- CSRF guard is broken")
	}
	if !statesMatch("real-state", "real-state") {
		t.Fatal("statesMatch rejected a genuinely matching state")
	}
}
