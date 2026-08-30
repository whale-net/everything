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

// openMethods are the three pre-existing RPCs NFR2 leaves unauthenticated
// through M1. wantFencedMethods are the three M1-added RPCs NFR2 requires to
// be fenced. Both are listed literally here -- deliberately NOT derived from
// authenticatedMethods in auth.go -- so that if a method is ever dropped
// from that map (accidentally left open), these tests still assert it must
// be fenced and go red, rather than silently shrinking their own coverage
// to match whatever the map currently says. Verified: temporarily removing
// GetSensorReadingHistory from authenticatedMethods while this list stayed
// derived from the map made the corresponding no-token test vanish instead
// of failing -- exactly the false-green this literal list prevents.
var openMethods = []string{
	"/leaflab.api.v1.LeafLabAPI/PushDeviceConfig",
	"/leaflab.api.v1.LeafLabAPI/GetDeviceConfig",
	"/leaflab.api.v1.LeafLabAPI/ListBoards",
}

var wantFencedMethods = []string{
	"/leaflab.api.v1.LeafLabAPI/ListBoardsWithState",
	"/leaflab.api.v1.LeafLabAPI/GetBoardDetail",
	"/leaflab.api.v1.LeafLabAPI/GetSensorReadingHistory",
}

// fencedMethods returns wantFencedMethods, after first asserting it is
// exactly the allowlist auth.go ships (authenticatedMethods) -- so a method
// added to or removed from either list without updating the other fails
// loudly here rather than the two silently drifting apart.
func fencedMethods(t *testing.T) []string {
	t.Helper()
	if len(authenticatedMethods) != len(wantFencedMethods) {
		t.Fatalf("authenticatedMethods has %d entries, wantFencedMethods has %d -- keep them in sync", len(authenticatedMethods), len(wantFencedMethods))
	}
	for _, m := range wantFencedMethods {
		if !authenticatedMethods[m] {
			t.Fatalf("expected %q to be in authenticatedMethods (auth.go), it is not -- NFR2 requires this RPC to be fenced", m)
		}
	}
	return wantFencedMethods
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
// -- fenced or not -- reaches its handler with no credential supplied.
func TestSelectiveUnary_AuthModeNone_EveryRPCSucceedsWithoutCredential(t *testing.T) {
	unaryAuth, _, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode: grpcauth.AuthModeNone,
	})
	if err != nil {
		t.Fatalf("constructing interceptors: %v", err)
	}
	interceptor := selectiveUnaryInterceptor(unaryAuth)

	for _, method := range append(append([]string{}, fencedMethods(t)...), openMethods...) {
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

// TestSelectiveUnary_OIDCMode_NoToken_FencedRPCsRejected proves the three
// M1-added RPCs reject an anonymous (no-token) call in OIDC mode --
// grpcauth's own interceptor lets that call proceed with no error and no
// claims; requireClaims (auth.go) is what turns it into Unauthenticated.
func TestSelectiveUnary_OIDCMode_NoToken_FencedRPCsRejected(t *testing.T) {
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

	for _, method := range fencedMethods(t) {
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

// TestSelectiveUnary_OIDCMode_NoToken_OpenRPCsUnaffected proves the operator's
// existing grpcurl config-push path keeps working: PushDeviceConfig,
// GetDeviceConfig, and ListBoards still reach their handlers with no
// credential, even once OIDC mode is turned on for the fenced RPCs.
func TestSelectiveUnary_OIDCMode_NoToken_OpenRPCsUnaffected(t *testing.T) {
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

	for _, method := range openMethods {
		t.Run(method, func(t *testing.T) {
			handler, reached := reachedHandler()
			_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: method}, handler)
			if err != nil {
				t.Fatalf("unexpected error for open RPC: %v", err)
			}
			if !*reached {
				t.Fatal("expected handler to be reached")
			}
		})
	}
}

// --- OIDC mode, wrong-audience token -------------------------------------

// TestSelectiveUnary_OIDCMode_WrongAudience_FencedRPCsRejected proves a
// token minted for a different client (e.g. the UI's client) is rejected at
// the fenced RPCs, exercising the real audience check inside go-oidc's
// verifier end-to-end against a live (fake) issuer.
func TestSelectiveUnary_OIDCMode_WrongAudience_FencedRPCsRejected(t *testing.T) {
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

	for _, method := range fencedMethods(t) {
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

// TestSelectiveUnary_OIDCMode_ValidToken_FencedRPCsReachHandlerWithClaims
// proves a token issued for the API's own audience reaches the fenced
// RPCs' handlers, with grpcauth.ClaimsFromContext yielding the caller's
// subject -- the "this is what an authenticated call looks like" case.
func TestSelectiveUnary_OIDCMode_ValidToken_FencedRPCsReachHandlerWithClaims(t *testing.T) {
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

	for _, method := range fencedMethods(t) {
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
// None of the M1 RPCs stream today, but selectiveStreamInterceptor and
// requireClaimsStream are shipped code (auth.go documents them as a
// drop-in for a future streaming RPC); these pin the same routing and
// rejection behavior as the unary tests above.

// TestSelectiveStream_OIDCMode_NoToken_FencedRejectedOpenUnaffected proves
// the stream interceptor applies the same allowlist as the unary one: a
// fenced method rejects an anonymous call, an open method does not.
func TestSelectiveStream_OIDCMode_NoToken_FencedRejectedOpenUnaffected(t *testing.T) {
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

	fenced := fencedMethods(t)[0]
	t.Run("fenced rejected", func(t *testing.T) {
		reached := false
		handler := func(srv interface{}, ss grpc.ServerStream) error {
			reached = true
			return nil
		}
		ss := &fakeServerStream{ctx: context.Background()}
		err := interceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: fenced}, handler)
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated, got %v", err)
		}
		if reached {
			t.Fatal("handler must not be reached without a credential")
		}
	})

	t.Run("open unaffected", func(t *testing.T) {
		reached := false
		handler := func(srv interface{}, ss grpc.ServerStream) error {
			reached = true
			return nil
		}
		ss := &fakeServerStream{ctx: context.Background()}
		err := interceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: openMethods[0]}, handler)
		if err != nil {
			t.Fatalf("unexpected error for open RPC: %v", err)
		}
		if !reached {
			t.Fatal("expected handler to be reached")
		}
	})
}
