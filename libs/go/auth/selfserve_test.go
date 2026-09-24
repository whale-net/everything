package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// selfServeFakeStore is an in-memory CredentialStore for selfserve_test.go
// that, unlike verify_test.go's fakeCredentialStore, actually tracks
// per-identity List and id-scoped Revoke — the two behaviors this file's
// handlers depend on and fakeCredentialStore stubs out as no-ops.
type selfServeFakeStore struct {
	mu    sync.Mutex
	byID  map[uuid.UUID]Credential
	order []uuid.UUID // insertion order, most recent last
}

func newSelfServeFakeStore() *selfServeFakeStore {
	return &selfServeFakeStore{byID: make(map[uuid.UUID]Credential)}
}

var _ CredentialStore = (*selfServeFakeStore)(nil)

func (s *selfServeFakeStore) Mint(_ context.Context, identity string) (string, Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := "raw-token-" + uuid.NewString()
	cred := Credential{ID: uuid.New(), Identity: identity, TokenHash: hashToken(token)}
	s.byID[cred.ID] = cred
	s.order = append(s.order, cred.ID)
	return token, cred, nil
}

func (s *selfServeFakeStore) Verify(_ context.Context, _ string) (string, Credential, error) {
	return "", Credential{}, ErrInvalidCredential
}

func (s *selfServeFakeStore) Revoke(_ context.Context, id uuid.UUID, identity string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cred, ok := s.byID[id]
	if !ok || cred.Identity != identity || cred.RevokedAt != nil {
		return nil
	}
	now := cred.CreatedAt
	cred.RevokedAt = &now
	s.byID[id] = cred
	return nil
}

func (s *selfServeFakeStore) List(_ context.Context, identity string) ([]Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Credential
	for i := len(s.order) - 1; i >= 0; i-- {
		cred := s.byID[s.order[i]]
		if cred.Identity == identity {
			out = append(out, cred)
		}
	}
	return out, nil
}

// selfServeTestServer bundles an httptest.Server with both Mount and
// MountSelfServe registered, plus a mutable stubResolver.
type selfServeTestServer struct {
	srv      *httptest.Server
	resolver *stubResolver
	creds    *selfServeFakeStore
	client   *http.Client
}

func newSelfServeTestServer(t *testing.T) *selfServeTestServer {
	t.Helper()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resolver := &stubResolver{}
	creds := newSelfServeFakeStore()

	p, err := NewProvider(ProviderConfig{
		Issuer:      srv.URL,
		Resource:    srv.URL + testResourcePath,
		Resolver:    resolver,
		Credentials: creds,
	})
	require.NoError(t, err)
	p.Mount(mux)
	require.NoError(t, p.MountSelfServe(mux))

	return &selfServeTestServer{srv: srv, resolver: resolver, creds: creds, client: srv.Client()}
}

func (ts *selfServeTestServer) do(t *testing.T, method, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.srv.URL+path, nil)
	require.NoError(t, err)
	resp, err := ts.client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// ── POST /credentials (mint) ────────────────────────────────────────────

func TestSelfServeMint_ResolvedCaller_ReturnsTokenOnce(t *testing.T) {
	ts := newSelfServeTestServer(t)
	ts.resolver.identity, ts.resolver.ok = "person-1", true

	resp := ts.do(t, http.MethodPost, "/credentials")
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body mintResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.NotEmpty(t, body.Token)
	assert.NotEmpty(t, body.ID)
	assert.Empty(t, body.RevokedAt)

	// Minted for the resolved identity, not some other caller.
	list, err := ts.creds.List(context.Background(), "person-1")
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestSelfServeMint_UnresolvedCaller_Returns401(t *testing.T) {
	ts := newSelfServeTestServer(t)
	// resolver.ok defaults to false — no session established.

	resp := ts.do(t, http.MethodPost, "/credentials")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var body selfServeErrorBody
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "unauthenticated", body.Error)
}

func TestSelfServeMint_NoStoreCacheControl(t *testing.T) {
	ts := newSelfServeTestServer(t)
	ts.resolver.identity, ts.resolver.ok = "person-1", true

	resp := ts.do(t, http.MethodPost, "/credentials")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

// ── GET /credentials (list) ─────────────────────────────────────────────

func TestSelfServeList_OnlyReturnsCallersOwnCredentials(t *testing.T) {
	ts := newSelfServeTestServer(t)

	ts.resolver.identity, ts.resolver.ok = "person-1", true
	mint := ts.do(t, http.MethodPost, "/credentials")
	require.Equal(t, http.StatusCreated, mint.StatusCode)

	ts.resolver.identity, ts.resolver.ok = "person-2", true
	mint2 := ts.do(t, http.MethodPost, "/credentials")
	require.Equal(t, http.StatusCreated, mint2.StatusCode)

	ts.resolver.identity, ts.resolver.ok = "person-1", true
	resp := ts.do(t, http.MethodGet, "/credentials")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body listCredentialsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Credentials, 1)
}

func TestSelfServeList_NeverRendersTheRawToken(t *testing.T) {
	ts := newSelfServeTestServer(t)
	ts.resolver.identity, ts.resolver.ok = "person-1", true

	mint := ts.do(t, http.MethodPost, "/credentials")
	var minted mintResponse
	require.NoError(t, json.NewDecoder(mint.Body).Decode(&minted))

	resp := ts.do(t, http.MethodGet, "/credentials")
	// Read as raw text to assert the minted token never appears anywhere
	// in the list body, not just that a typed field is absent.
	buf, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.NotContains(t, string(buf), minted.Token)
}

func TestSelfServeList_UnresolvedCaller_Returns401(t *testing.T) {
	ts := newSelfServeTestServer(t)

	resp := ts.do(t, http.MethodGet, "/credentials")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// ── DELETE /credentials/{id} (revoke) ───────────────────────────────────

func TestSelfServeRevoke_OwnCredential_RemovesItFromList(t *testing.T) {
	ts := newSelfServeTestServer(t)
	ts.resolver.identity, ts.resolver.ok = "person-1", true

	mint := ts.do(t, http.MethodPost, "/credentials")
	var minted mintResponse
	require.NoError(t, json.NewDecoder(mint.Body).Decode(&minted))

	resp := ts.do(t, http.MethodDelete, "/credentials/"+minted.ID)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	list, err := ts.creds.List(context.Background(), "person-1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.NotNil(t, list[0].RevokedAt)
}

func TestSelfServeRevoke_NotOwnedCredential_StillReports204(t *testing.T) {
	ts := newSelfServeTestServer(t)

	ts.resolver.identity, ts.resolver.ok = "person-1", true
	mint := ts.do(t, http.MethodPost, "/credentials")
	var minted mintResponse
	require.NoError(t, json.NewDecoder(mint.Body).Decode(&minted))

	// person-2 tries to revoke person-1's credential — idempotent
	// contract means this still reports success without leaking that the
	// id exists under a different owner.
	ts.resolver.identity, ts.resolver.ok = "person-2", true
	resp := ts.do(t, http.MethodDelete, "/credentials/"+minted.ID)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	list, err := ts.creds.List(context.Background(), "person-1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Nil(t, list[0].RevokedAt) // untouched by the not-owned attempt
}

func TestSelfServeRevoke_MalformedID_Returns400(t *testing.T) {
	ts := newSelfServeTestServer(t)
	ts.resolver.identity, ts.resolver.ok = "person-1", true

	resp := ts.do(t, http.MethodDelete, "/credentials/not-a-uuid")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ── MountSelfServe validation ───────────────────────────────────────────

func TestMountSelfServe_NilResolver_ReturnsError(t *testing.T) {
	p := &Provider{cfg: ProviderConfig{Credentials: newSelfServeFakeStore()}}
	err := p.MountSelfServe(http.NewServeMux())
	assert.ErrorIs(t, err, ErrSelfServeRequiresResolver)
}
