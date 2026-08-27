package main

// Covers this issue's (#1335) Testing section, on the API side:
//   - a principal not on the allowlist is refused on every RPC except
//     GetHealth
//   - a principal on the allowlist gets through
//   - empty/missing configuration refuses everyone (fail-closed), asserted
//     explicitly
//   - the gate is applied at the interceptor, so a newly added RPC
//     inherits it (proven by exercising an arbitrary method name that
//     exists nowhere else in this codebase)
//
// Unit-level tests exercise NewExposureUnaryInterceptor/
// NewExposureStreamInterceptor directly (reusing alwaysOKUnary/
// alwaysOKStream/fakeServerStream from auth_test.go, the same pattern that
// file uses for auth.go's enforcement interceptor). Full-chain tests go
// through buildServer behind a bufconn listener (reusing
// stubAPIServer/fakeBearerAuthUnary/fakeBearerAuthStream/testValidToken/
// testClaims from main_test.go/auth_test.go) to prove the gate is wired
// into the real production interceptor chain, not just correct in
// isolation.

import (
	"context"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// --- ParseExposureAllowlist -------------------------------------------

func TestParseExposureAllowlist(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]struct{}
	}{
		{"empty string admits nobody", "", map[string]struct{}{}},
		{"whitespace-only admits nobody", "   ", map[string]struct{}{}},
		{"single entry", "alice", map[string]struct{}{"alice": {}}},
		{"multiple entries", "alice,bob", map[string]struct{}{"alice": {}, "bob": {}}},
		{"whitespace around entries is trimmed", " alice , bob ", map[string]struct{}{"alice": {}, "bob": {}}},
		{"blank entries between commas are dropped", "alice,,bob,", map[string]struct{}{"alice": {}, "bob": {}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseExposureAllowlist(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseExposureAllowlist(%q) = %v, want %v", tc.raw, got, tc.want)
			}
			if got == nil {
				t.Errorf("ParseExposureAllowlist(%q) returned a nil map, want a non-nil empty map (callers must treat empty as fail-closed, not as an uninitialized value)", tc.raw)
			}
		})
	}
}

func TestLoadExposureAllowlistFromEnv_MissingVar_RefusesEveryone(t *testing.T) {
	// Deliberately not calling t.Setenv: LEAFLAB_API_EXPOSURE_ALLOWLIST is
	// unset, matching a fresh environment that never configured it.
	got := LoadExposureAllowlistFromEnv()
	if len(got) != 0 {
		t.Fatalf("LoadExposureAllowlistFromEnv() with unset env var = %v, want empty (fail-closed)", got)
	}
}

// --- exposureAllows / exposureRefusal -----------------------------------

func TestExposureAllows(t *testing.T) {
	allowlist := map[string]struct{}{"insider": {}}

	t.Run("no claims in context", func(t *testing.T) {
		if exposureAllows(context.Background(), allowlist) {
			t.Error("expected exposureAllows to return false with no Claims in context")
		}
	})
	t.Run("claims present, not on allowlist", func(t *testing.T) {
		ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "outsider"})
		if exposureAllows(ctx, allowlist) {
			t.Error("expected exposureAllows to return false for a non-allowlisted subject")
		}
	})
	t.Run("claims present, on allowlist", func(t *testing.T) {
		ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "insider"})
		if !exposureAllows(ctx, allowlist) {
			t.Error("expected exposureAllows to return true for an allowlisted subject")
		}
	})
	t.Run("empty allowlist refuses even a real subject", func(t *testing.T) {
		ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "insider"})
		if exposureAllows(ctx, map[string]struct{}{}) {
			t.Error("expected exposureAllows to return false with an empty allowlist (fail-closed)")
		}
	})
	t.Run("nil allowlist refuses even a real subject", func(t *testing.T) {
		ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "insider"})
		if exposureAllows(ctx, nil) {
			t.Error("expected exposureAllows to return false with a nil allowlist (fail-closed)")
		}
	})
}

// TestExposureRefusal_PlainWording is FR59.2 applied to this specific
// gate: the reason a refused caller sees names no internal mechanism
// (allowlist, environment variable, etc).
func TestExposureRefusal_PlainWording(t *testing.T) {
	err := exposureRefusal()
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("exposureRefusal() error carries no Failure detail: %v", err)
	}
	if detail.Class != string(contract.FailurePermissionDenied) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailurePermissionDenied)
	}
	if detail.Reason != "This isn't open yet." {
		t.Errorf("Reason = %q, want the plain FR59.2 sentence", detail.Reason)
	}
	for _, leaked := range []string{"allowlist", "env", "ENV", "LEAFLAB_API_EXPOSURE", "config"} {
		if strings.Contains(strings.ToLower(detail.Reason), strings.ToLower(leaked)) {
			t.Errorf("Reason %q leaks internal mechanism detail %q", detail.Reason, leaked)
		}
	}
}

// --- unit-level interceptor tests ---------------------------------------
//
// arbitraryFutureMethod stands in for "a newly added RPC": it names no
// method that exists anywhere else in this codebase or its proto. Using it
// (rather than one of the three existing RPC names) proves the gate is
// wired generically off info.FullMethod -- refusing anything not in
// anonymousMethods -- rather than special-cased to today's three RPCs, so
// a new RPC added later inherits it automatically as long as it isn't
// added to anonymousMethods.
const arbitraryFutureMethod = "/leaflab.api.v1.LeafLabAPI/SomeFutureRPCNotYetWritten"

func TestExposureUnary_NotAllowlisted_Refused(t *testing.T) {
	interceptor := NewExposureUnaryInterceptor(map[string]struct{}{"insider": {}})
	info := &grpc.UnaryServerInfo{FullMethod: arbitraryFutureMethod}
	ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "outsider"})

	var reached bool
	_, err := interceptor(ctx, nil, info, alwaysOKUnary(&reached))

	if reached {
		t.Fatal("handler was reached despite a non-allowlisted subject")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("got code %v, want PermissionDenied: %v", status.Code(err), err)
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailurePermissionDenied) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailurePermissionDenied)
	}
}

func TestExposureUnary_Allowlisted_Reaches(t *testing.T) {
	interceptor := NewExposureUnaryInterceptor(map[string]struct{}{"insider": {}})
	info := &grpc.UnaryServerInfo{FullMethod: arbitraryFutureMethod}
	ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "insider"})

	var reached bool
	resp, err := interceptor(ctx, nil, info, alwaysOKUnary(&reached))

	if !reached {
		t.Fatal("handler was not reached despite an allowlisted subject")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Errorf("resp = %v, want %q", resp, "ok")
	}
}

func TestExposureUnary_EmptyAllowlist_RefusesEveryone(t *testing.T) {
	interceptor := NewExposureUnaryInterceptor(map[string]struct{}{})
	info := &grpc.UnaryServerInfo{FullMethod: arbitraryFutureMethod}
	ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "insider"})

	var reached bool
	_, err := interceptor(ctx, nil, info, alwaysOKUnary(&reached))

	if reached {
		t.Fatal("handler was reached despite an empty allowlist -- fail-closed requires refusal")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("got code %v, want PermissionDenied: %v", status.Code(err), err)
	}
}

func TestExposureUnary_NilAllowlist_RefusesEveryone(t *testing.T) {
	// A nil map is what LoadExposureAllowlistFromEnv-adjacent code could
	// pass if wiring were ever simplified to skip ParseExposureAllowlist's
	// always-non-nil guarantee; asserted directly here so that guarantee
	// isn't the only thing standing between a missing config and an
	// accidental "everyone in" bug.
	interceptor := NewExposureUnaryInterceptor(nil)
	info := &grpc.UnaryServerInfo{FullMethod: arbitraryFutureMethod}
	ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "insider"})

	var reached bool
	_, err := interceptor(ctx, nil, info, alwaysOKUnary(&reached))

	if reached {
		t.Fatal("handler was reached despite a nil allowlist -- fail-closed requires refusal")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("got code %v, want PermissionDenied: %v", status.Code(err), err)
	}
}

func TestExposureUnary_GetHealth_BypassesEvenWithoutClaims(t *testing.T) {
	interceptor := NewExposureUnaryInterceptor(map[string]struct{}{})
	info := &grpc.UnaryServerInfo{FullMethod: healthFullMethod}

	var reached bool
	_, err := interceptor(context.Background(), nil, info, alwaysOKUnary(&reached))

	if !reached {
		t.Fatal("GetHealth handler was not reached despite the anonymous-methods bypass")
	}
	if err != nil {
		t.Fatalf("unexpected error for GetHealth: %v", err)
	}
}

func TestExposureStream_NotAllowlisted_Refused(t *testing.T) {
	interceptor := NewExposureStreamInterceptor(map[string]struct{}{"insider": {}})
	info := &grpc.StreamServerInfo{FullMethod: arbitraryFutureMethod}
	ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "outsider"})
	ss := &fakeServerStream{ctx: ctx}

	var reached bool
	err := interceptor(nil, ss, info, alwaysOKStream(&reached))

	if reached {
		t.Fatal("stream handler was reached despite a non-allowlisted subject")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("got code %v, want PermissionDenied: %v", status.Code(err), err)
	}
}

func TestExposureStream_Allowlisted_Reaches(t *testing.T) {
	interceptor := NewExposureStreamInterceptor(map[string]struct{}{"insider": {}})
	info := &grpc.StreamServerInfo{FullMethod: arbitraryFutureMethod}
	ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "insider"})
	ss := &fakeServerStream{ctx: ctx}

	var reached bool
	if err := interceptor(nil, ss, info, alwaysOKStream(&reached)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reached {
		t.Fatal("stream handler was not reached despite an allowlisted subject")
	}
}

func TestExposureStream_GetHealth_BypassesEvenWithoutClaims(t *testing.T) {
	interceptor := NewExposureStreamInterceptor(map[string]struct{}{})
	info := &grpc.StreamServerInfo{FullMethod: healthFullMethod}
	ss := &fakeServerStream{ctx: context.Background()}

	var reached bool
	if err := interceptor(nil, ss, info, alwaysOKStream(&reached)); err != nil {
		t.Fatalf("unexpected error for GetHealth: %v", err)
	}
	if !reached {
		t.Fatal("GetHealth stream handler was not reached despite the anonymous-methods bypass")
	}
}

// --- full-chain (buildServer + bufconn) tests ----------------------------

// startTestServerWithAllowlist mirrors main_test.go's startTestServer, but
// takes an explicit exposure allowlist instead of the fixed
// testExposureAllowlist every other test file in this package uses -- this
// file is what actually varies that allowlist to exercise A30's gate over
// the wire.
func startTestServerWithAllowlist(t *testing.T, allowlist map[string]struct{}) *grpc.ClientConn {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	grpcServer := buildServer(fakeBearerAuthUnary(), fakeBearerAuthStream(), discardTestLogger(), stubAPIServer{}, false, allowlist)

	go func() {
		_ = grpcServer.Serve(lis)
	}()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn server: %v", err)
	}

	t.Cleanup(func() {
		conn.Close() //nolint:errcheck
		grpcServer.Stop()
	})

	return conn
}

// TestExposureGate_NotAllowlisted_RefusedOnEveryRPCExceptGetHealth is the
// Testing section's first bullet, over the wire through the real
// production interceptor chain: an authenticated principal (valid token,
// so auth.go's enforcement interceptor lets it through) who is not on the
// exposure allowlist is refused on every RPC except GetHealth.
func TestExposureGate_NotAllowlisted_RefusedOnEveryRPCExceptGetHealth(t *testing.T) {
	// testClaims.Subject ("test-subject") deliberately absent from this
	// allowlist.
	conn := startTestServerWithAllowlist(t, map[string]struct{}{"someone-else": {}})
	client := pb.NewLeafLabAPIClient(conn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+testValidToken)

	assertPermissionDenied := func(t *testing.T, rpc string, err error) {
		t.Helper()
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s: got code %v, want PermissionDenied: %v", rpc, status.Code(err), err)
		}
	}

	t.Run("PushDeviceConfig", func(t *testing.T) {
		_, err := client.PushDeviceConfig(ctx, &pb.PushDeviceConfigRequest{DeviceId: "board-1"})
		assertPermissionDenied(t, "PushDeviceConfig", err)
	})
	t.Run("GetDeviceConfig", func(t *testing.T) {
		_, err := client.GetDeviceConfig(ctx, &pb.GetDeviceConfigRequest{DeviceId: "board-1"})
		assertPermissionDenied(t, "GetDeviceConfig", err)
	})
	t.Run("ListBoards", func(t *testing.T) {
		_, err := client.ListBoards(ctx, &pb.ListBoardsRequest{})
		assertPermissionDenied(t, "ListBoards", err)
	})
	t.Run("GetHealth still succeeds", func(t *testing.T) {
		resp, err := client.GetHealth(ctx, &pb.GetHealthRequest{})
		if err != nil {
			t.Fatalf("GetHealth for a non-allowlisted (but authenticated) caller returned an error, want success (FR63.2 bypass): %v", err)
		}
		if resp.Status != pb.HealthStatus_HEALTH_UP {
			t.Errorf("Status = %v, want HEALTH_UP (stub response)", resp.Status)
		}
	})
}

// TestExposureGate_Allowlisted_Succeeds is the Testing section's second
// bullet, over the wire: an allowlisted, authenticated principal reaches
// the handler. (TestRPCs_ValidCredential_Succeeds in main_test.go already
// exercises this same path via the fixed testExposureAllowlist every other
// test in this package uses; this test makes the allowlist an explicit,
// local variable instead, so it reads as this issue's own assertion
// rather than incidental coverage.)
func TestExposureGate_Allowlisted_Succeeds(t *testing.T) {
	conn := startTestServerWithAllowlist(t, map[string]struct{}{testClaims.Subject: {}})
	client := pb.NewLeafLabAPIClient(conn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+testValidToken)

	if _, err := client.ListBoards(ctx, &pb.ListBoardsRequest{}); err != nil {
		t.Fatalf("expected success for an allowlisted principal, got %v", err)
	}
}

// TestExposureGate_EmptyMissingConfig_RefusesEveryone is the Testing
// section's third bullet, asserted explicitly and over the wire: an empty
// allowlist -- exactly what LoadExposureAllowlistFromEnv returns for a
// missing/empty LEAFLAB_API_EXPOSURE_ALLOWLIST -- refuses even an
// otherwise-valid, authenticated caller. This is the fail-closed case A30
// requires: missing configuration must mean nobody is admitted, not
// everybody.
func TestExposureGate_EmptyMissingConfig_RefusesEveryone(t *testing.T) {
	emptyAllowlist := ParseExposureAllowlist("") // exactly what an unset env var produces
	conn := startTestServerWithAllowlist(t, emptyAllowlist)
	client := pb.NewLeafLabAPIClient(conn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+testValidToken)

	_, err := client.ListBoards(ctx, &pb.ListBoardsRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("got code %v, want PermissionDenied -- empty/missing exposure config must refuse everyone, not admit everyone: %v", status.Code(err), err)
	}

	// GetHealth's anonymous bypass is unaffected by the exposure
	// allowlist's emptiness -- it never consults the allowlist at all.
	if _, err := client.GetHealth(context.Background(), &pb.GetHealthRequest{}); err != nil {
		t.Fatalf("GetHealth with an empty exposure allowlist returned an error, want success: %v", err)
	}
}
