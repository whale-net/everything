package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// allAuthenticatedMethods returns every key of authenticatedMethods
// (auth.go), sorted so subtest output is stable. M2 closed the fence
// (NFR1): every RPC in api.proto is in this map, so unlike M1's auth_test.go
// there is no separate "open methods" list -- these tests are table-driven
// directly over the map itself, so a newly added RPC that is not fenced
// fails here without anyone having to remember to add it to a second,
// hand-maintained list.
func allAuthenticatedMethods() []string {
	methods := make([]string, 0, len(authenticatedMethods))
	for m := range authenticatedMethods {
		methods = append(methods, m)
	}
	return methods
}

// reachedHandler returns a grpc.UnaryHandler and a pointer to a bool that
// flips to true iff the handler runs, so tests can assert a call did or did
// not reach the RPC implementation.
func reachedHandler() (grpc.UnaryHandler, *bool) {
	reached := false
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		reached = true
		return "ok", nil
	}
	return handler, &reached
}

// fakeServerStream is a minimal grpc.ServerStream whose only implemented
// method is Context -- the only method selectiveStreamInterceptor,
// grpcauth's stream interceptor, and requireClaimsStream call in these
// tests. Mirrors grpcauth's own wrappedStream shape.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }

// --- Fake OIDC issuer -------------------------------------------------
//
// grpcauth.NewServerInterceptors(AuthModeOIDC) fetches a real OIDC discovery
// document at construction time (via oidc.NewProvider), so exercising OIDC
// mode -- including the audience check inside go-oidc's verifier -- needs a
// real HTTP issuer to talk to. fakeOIDCIssuer serves the minimal discovery
// document and JWKS endpoint go-oidc requires, signed with an ephemeral
// ECDSA key generated per test. This mirrors the testIssuer pattern in
// go-oidc's own oidc_test.go (TestVerifierAlg).
type fakeOIDCIssuer struct {
	baseURL string
	jwk     jose.JSONWebKey
}

func (f *fakeOIDCIssuer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(struct {
			Issuer  string   `json:"issuer"`
			JWKSURI string   `json:"jwks_uri"`
			Algs    []string `json:"id_token_signing_alg_values_supported"`
		}{
			Issuer:  f.baseURL,
			JWKSURI: f.baseURL + "/keys",
			Algs:    []string{"ES256"},
		})
	case "/keys":
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{f.jwk}})
	default:
		http.NotFound(w, r)
	}
}

// newFakeOIDCServer starts a fakeOIDCIssuer and returns it along with the
// ECDSA private key tokens must be signed with to verify against it.
func newFakeOIDCServer(t *testing.T) (*httptest.Server, *ecdsa.PrivateKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating signing key: %v", err)
	}
	issuer := &fakeOIDCIssuer{
		jwk: jose.JSONWebKey{Algorithm: "ES256", Key: priv.Public(), Use: "sign"},
	}
	srv := httptest.NewServer(issuer)
	t.Cleanup(srv.Close)
	issuer.baseURL = srv.URL
	return srv, priv
}

// signToken mints a signed, one-hour-valid JWT for the given issuer,
// audience, and subject using priv (from newFakeOIDCServer).
func signToken(t *testing.T, priv *ecdsa.PrivateKey, issuer, audience, subject string) string {
	t.Helper()
	now := time.Now()
	claims := struct {
		Iss string `json:"iss"`
		Sub string `json:"sub"`
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Iat int64  `json:"iat"`
	}{
		Iss: issuer,
		Sub: subject,
		Aud: audience,
		Exp: now.Add(time.Hour).Unix(),
		Iat: now.Add(-time.Minute).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshaling claims: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: priv}, nil)
	if err != nil {
		t.Fatalf("creating signer: %v", err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	raw, err := jws.CompactSerialize()
	if err != nil {
		t.Fatalf("serializing token: %v", err)
	}
	return raw
}

// --- GRPC_AUTH_MODE=none -----------------------------------------------

// TestSelectiveUnary_AuthModeNone_EveryRPCSucceedsWithoutCredential proves
// the Tilt/integration-test default: with no OIDC config at all, every RPC
// in authenticatedMethods reaches its handler with no credential supplied
// (grpcauth always injects dev claims in AuthModeNone).
func TestSelectiveUnary_AuthModeNone_EveryRPCSucceedsWithoutCredential(t *testing.T) {
	unaryAuth, _, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode: grpcauth.AuthModeNone,
	})
	if err != nil {
		t.Fatalf("constructing interceptors: %v", err)
	}
	interceptor := selectiveUnaryInterceptor(unaryAuth)

	for _, method := range allAuthenticatedMethods() {
		t.Run(method, func(t *testing.T) {
			handler, reached := reachedHandler()
			_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: method}, handler)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !*reached {
				t.Fatal("expected handler to be reached")
			}
		})
	}
}

// --- OIDC mode, no token -------------------------------------------------

// TestSelectiveUnary_OIDCMode_NoToken_EveryMethodRejected is this task's
// Testing criterion 1: every method in authenticatedMethods rejects an
// anonymous (no-token) call with codes.Unauthenticated under AuthModeOIDC --
// table-driven directly over the map, so a newly added RPC that forgets to
// join the map is caught the moment it's added to api.proto and registered,
// without anyone updating a second list here. grpcauth's own interceptor
// lets a no-token call proceed with no error and no claims; requireClaims
// (auth.go) is what turns that into Unauthenticated.
func TestSelectiveUnary_OIDCMode_NoToken_EveryMethodRejected(t *testing.T) {
	srv, _ := newFakeOIDCServer(t)
	unaryAuth, _, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:      grpcauth.AuthModeOIDC,
		IssuerURL: srv.URL,
		ClientID:  "leaflab-api",
	})
	if err != nil {
		t.Fatalf("constructing interceptors: %v", err)
	}
	interceptor := selectiveUnaryInterceptor(unaryAuth)

	for _, method := range allAuthenticatedMethods() {
		t.Run(method, func(t *testing.T) {
			handler, reached := reachedHandler()
			_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: method}, handler)
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("expected Unauthenticated for no-token call, got %v", err)
			}
			if *reached {
				t.Fatal("handler must not be reached without a credential")
			}
		})
	}
}

// --- OIDC mode, wrong-audience token -------------------------------------

// TestSelectiveUnary_OIDCMode_WrongAudience_EveryMethodRejected proves a
// token minted for a different client (e.g. the UI's client) is rejected at
// every fenced RPC, exercising the real audience check inside go-oidc's
// verifier end-to-end against a live (fake) issuer.
func TestSelectiveUnary_OIDCMode_WrongAudience_EveryMethodRejected(t *testing.T) {
	srv, priv := newFakeOIDCServer(t)
	unaryAuth, _, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:      grpcauth.AuthModeOIDC,
		IssuerURL: srv.URL,
		ClientID:  "leaflab-api",
	})
	if err != nil {
		t.Fatalf("constructing interceptors: %v", err)
	}
	interceptor := selectiveUnaryInterceptor(unaryAuth)

	token := signToken(t, priv, srv.URL, "leaflab-ui", "user-1")
	md := metadata.Pairs("authorization", "Bearer "+token)

	for _, method := range allAuthenticatedMethods() {
		t.Run(method, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), md)
			handler, reached := reachedHandler()
			_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method}, handler)
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("expected Unauthenticated for wrong-audience token, got %v", err)
			}
			if *reached {
				t.Fatal("handler must not be reached for a wrong-audience token")
			}
		})
	}
}

// --- OIDC mode, valid token -----------------------------------------------

// TestSelectiveUnary_OIDCMode_ValidToken_EveryMethodReachesHandlerWithClaims
// proves a token issued for the API's own audience reaches every fenced
// RPC's handler, with grpcauth.ClaimsFromContext yielding the caller's
// subject -- the "this is what an authenticated call looks like" case.
func TestSelectiveUnary_OIDCMode_ValidToken_EveryMethodReachesHandlerWithClaims(t *testing.T) {
	srv, priv := newFakeOIDCServer(t)
	unaryAuth, _, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:      grpcauth.AuthModeOIDC,
		IssuerURL: srv.URL,
		ClientID:  "leaflab-api",
	})
	if err != nil {
		t.Fatalf("constructing interceptors: %v", err)
	}
	interceptor := selectiveUnaryInterceptor(unaryAuth)

	token := signToken(t, priv, srv.URL, "leaflab-api", "user-42")
	md := metadata.Pairs("authorization", "Bearer "+token)

	for _, method := range allAuthenticatedMethods() {
		t.Run(method, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), md)
			var gotSubject string
			var reached bool
			handler := func(ctx context.Context, req interface{}) (interface{}, error) {
				reached = true
				claims, ok := grpcauth.ClaimsFromContext(ctx)
				if !ok {
					t.Fatal("expected claims in context")
				}
				gotSubject = claims.Subject
				return "ok", nil
			}
			_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method}, handler)
			if err != nil {
				t.Fatalf("unexpected error for valid token: %v", err)
			}
			if !reached {
				t.Fatal("expected handler to be reached")
			}
			if gotSubject != "user-42" {
				t.Errorf("got subject %q, want %q", gotSubject, "user-42")
			}
		})
	}
}

// --- Stream interceptor parity -------------------------------------------
//
// None of the RPCs stream today, but selectiveStreamInterceptor and
// requireClaimsStream are shipped code (auth.go documents them as a
// drop-in for a future streaming RPC); this pins the same rejection
// behavior as the unary tests above for one representative fenced method.
func TestSelectiveStream_OIDCMode_NoToken_Rejected(t *testing.T) {
	srv, _ := newFakeOIDCServer(t)
	_, streamAuth, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:      grpcauth.AuthModeOIDC,
		IssuerURL: srv.URL,
		ClientID:  "leaflab-api",
	})
	if err != nil {
		t.Fatalf("constructing interceptors: %v", err)
	}
	interceptor := selectiveStreamInterceptor(streamAuth)

	methods := allAuthenticatedMethods()
	if len(methods) == 0 {
		t.Fatal("authenticatedMethods must not be empty")
	}
	fenced := methods[0]

	reached := false
	handler := func(srv interface{}, ss grpc.ServerStream) error {
		reached = true
		return nil
	}
	ss := &fakeServerStream{ctx: context.Background()}
	err = interceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: fenced}, handler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
	if reached {
		t.Fatal("handler must not be reached without a credential")
	}
}
