package main

// Shared test doubles and helpers used by both the fast handler tests
// (handlers_auth_test.go, nfr18_conformance_test.go) and the Docker-backed
// integration tests (session_integration_test.go, //go:build integration).
// This file carries NO build tag deliberately, so it compiles into both
// go_test targets in BUILD.bazel (":ui_test" and ":ui_integration_test").

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// --- fakes --------------------------------------------------------------

// fakeLeafLabClient is a minimal stand-in for pb.LeafLabAPIClient covering
// GetHealth (handleHome's own probe) and ListBoards (handleHome delegates
// to handleBoards once healthy -- #1330 makes the boards screen the Phase
// 1 post-login landing route, so any test that drives an authenticated
// request through "/" now reaches ListBoards too).
type fakeLeafLabClient struct {
	pb.LeafLabAPIClient

	healthResp  *pb.GetHealthResponse
	healthErr   error
	healthCalls int

	// boardsResp defaults to an empty page (no boards, no error) when nil
	// and boardsErr is unset, so tests that only care about the health
	// gate don't have to stub ListBoards themselves.
	boardsResp  *pb.ListBoardsResponse
	boardsErr   error
	boardsCalls int
}

func (f *fakeLeafLabClient) GetHealth(ctx context.Context, in *pb.GetHealthRequest, opts ...grpc.CallOption) (*pb.GetHealthResponse, error) {
	f.healthCalls++
	if f.healthErr != nil {
		return nil, f.healthErr
	}
	return f.healthResp, nil
}

func (f *fakeLeafLabClient) ListBoards(ctx context.Context, in *pb.ListBoardsRequest, opts ...grpc.CallOption) (*pb.ListBoardsResponse, error) {
	f.boardsCalls++
	if f.boardsErr != nil {
		return nil, f.boardsErr
	}
	if f.boardsResp != nil {
		return f.boardsResp, nil
	}
	return &pb.ListBoardsResponse{}, nil
}

// devAuth returns an AuthModeNone authenticator: RequireAuth injects a
// fixed dev user without touching any session store, so it is the fast
// stand-in for "an authenticated request" in handler tests that aren't
// themselves testing session persistence (pattern:
// tools/app_registry/ui/handlers_promote_rollback_test.go's devUserAuth).
func devAuth(t *testing.T) *htmxauth.Authenticator {
	t.Helper()
	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{Mode: htmxauth.AuthModeNone})
	if err != nil {
		t.Fatalf("failed to construct AuthModeNone authenticator: %v", err)
	}
	return auth
}

// --- fake OIDC provider ---------------------------------------------------

// newFakeOIDCServer starts an in-process HTTP server that serves just
// enough OIDC discovery (a "/.well-known/openid-configuration" document
// naming this server's own authorization/jwks endpoints) for
// coreos/go-oidc's Provider discovery to succeed, plus a fake
// authorization endpoint that renders real login-form HTML -- standing in
// for Keycloak's hosted login page, since leaflab-ui itself never renders
// a login form (HandleLogin always redirects straight to the IdP; see
// libs/go/htmxauth/auth.go). No test in this repo has needed a fake OIDC
// server before now (grep confirms), so this is new, self-contained
// plumbing rather than a hoist of an existing helper.
func newFakeOIDCServer(t *testing.T) *httptest.Server {
	t.Helper()

	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"issuer": %q,
			"authorization_endpoint": %q,
			"token_endpoint": %q,
			"jwks_uri": %q
		}`, srv.URL, srv.URL+"/authorize", srv.URL+"/token", srv.URL+"/jwks")
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		// Stands in for the IdP's real hosted login page: real HTML,
		// containing a real <form>, never a blank body or JSON (FR13).
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<form id="kc-form-login" method="post" action="/login-actions/authenticate">
				<input type="text" name="username">
				<input type="password" name="password">
				<button type="submit">Sign in</button>
			</form>
		</body></html>`)
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"keys":[]}`)
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// oidcTestConfig returns a Config wired against a fake OIDC server (see
// newFakeOIDCServer), with no end_session_endpoint advertised -- matching
// most non-Keycloak-confirmed providers and exercising HandleLogout's
// local-only fallback path.
func oidcTestConfig(oidcSrv *httptest.Server) htmxauth.Config {
	return htmxauth.Config{
		Mode:             htmxauth.AuthModeOIDC,
		SessionSecret:    "test-secret-that-is-at-least-32-bytes-long",
		SessionName:      "leaflab_ui_session",
		OIDCIssuer:       oidcSrv.URL,
		OIDCClientID:     "leaflab-ui",
		OIDCClientSecret: "test-client-secret",
		OIDCRedirectURL:  oidcSrv.URL + "/auth/callback",
	}
}

// newOIDCTestAuthenticator builds a cookie-backed OIDC-mode Authenticator
// against a fake OIDC server -- used for the "unauthenticated"/"expired
// session" redirect-chain tests, which don't need DB-backed sessions.
func newOIDCTestAuthenticator(t *testing.T, oidcSrv *httptest.Server) *htmxauth.Authenticator {
	t.Helper()
	auth, err := htmxauth.NewAuthenticator(context.Background(), oidcTestConfig(oidcSrv))
	if err != nil {
		t.Fatalf("failed to construct OIDC-mode authenticator against fake server: %v", err)
	}
	return auth
}
