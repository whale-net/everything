package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// testValidToken/testClaims stand in for a validated OIDC token/Claims pair.
// fakeBearerAuthUnary/fakeBearerAuthStream below mimic grpcauth's own
// authenticate() "proceed anonymously if no Authorization header, otherwise
// verify" contract without a real IdP -- grpcauth's actual token
// verification is covered by libs/go/grpcauth's own tests
// (interceptors_test.go). What this file tests is what leaflab/api itself
// owns: auth.go's enforcement interceptor, logging_interceptor.go's
// correlation-id/subject logging, and GetHealth's allowlist bypass, all
// wired together exactly as buildServer/run() wire them in production.
const testValidToken = "test-valid-token-should-never-appear-in-logs-or-errors" //nolint:gosec // test fixture, not a real credential

var testClaims = &grpcauth.Claims{Subject: "test-subject", Roles: []string{RoleAdmin}}

func fakeBearerAuthUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		token, ok := bearerToken(ctx)
		if !ok {
			// No credential presented: proceed anonymously, matching
			// grpcauth.authenticate()'s AuthModeOIDC behavior for a missing
			// Authorization header -- auth.go's enforcement interceptor
			// (later in the chain) is what actually rejects this for any
			// non-allowlisted method.
			return handler(ctx, req)
		}
		if token != testValidToken {
			return nil, status.Error(codes.Unauthenticated, "invalid token")
		}
		return handler(grpcauth.ContextWithClaims(ctx, testClaims), req)
	}
}

func fakeBearerAuthStream() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		token, ok := bearerToken(ss.Context())
		if !ok {
			return handler(srv, ss)
		}
		if token != testValidToken {
			return status.Error(codes.Unauthenticated, "invalid token")
		}
		return handler(srv, &wrappedTestStream{ServerStream: ss, ctx: grpcauth.ContextWithClaims(ss.Context(), testClaims)})
	}
}

func bearerToken(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	vals := md.Get("authorization")
	if len(vals) == 0 || vals[0] == "" {
		return "", false
	}
	return strings.TrimPrefix(vals[0], "Bearer "), true
}

type wrappedTestStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedTestStream) Context() context.Context { return w.ctx }

// stubAPIServer is a minimal pb.LeafLabAPIServer that returns a canned
// success for every RPC. Used instead of the real LeafLabAPIServer (which
// needs a live Postgres/RabbitMQ, see response_contract_integration_test.go)
// so these tests isolate the auth/logging middleware's behavior -- exactly
// this task's scope -- from business logic covered elsewhere (repository
// tests, response_contract_integration_test.go, server_test.go's GetHealth
// unit tests).
type stubAPIServer struct {
	pb.UnimplementedLeafLabAPIServer
}

func (stubAPIServer) PushDeviceConfig(ctx context.Context, req *pb.PushDeviceConfigRequest) (*pb.PushDeviceConfigResponse, error) {
	return &pb.PushDeviceConfigResponse{Version: 1}, nil
}

func (stubAPIServer) GetDeviceConfig(ctx context.Context, req *pb.GetDeviceConfigRequest) (*pb.GetDeviceConfigResponse, error) {
	return &pb.GetDeviceConfigResponse{Found: false}, nil
}

func (stubAPIServer) ListBoards(ctx context.Context, req *pb.ListBoardsRequest) (*pb.ListBoardsResponse, error) {
	return &pb.ListBoardsResponse{}, nil
}

func (stubAPIServer) RewireSensor(ctx context.Context, req *pb.RewireSensorRequest) (*pb.RewireSensorResponse, error) {
	return &pb.RewireSensorResponse{}, nil
}

func (stubAPIServer) GetHealth(ctx context.Context, req *pb.GetHealthRequest) (*pb.GetHealthResponse, error) {
	return &pb.GetHealthResponse{Status: pb.HealthStatus_HEALTH_UP}, nil
}

// GetConfigStatus/ListConfigHistory/GetConfigVersion (FR34/FR35, #1369)
// are explicitly stubbed here, like every other RPC above, rather than
// left to the embedded pb.UnimplementedLeafLabAPIServer's promoted
// methods: those have a pointer receiver (*UnimplementedLeafLabAPIServer),
// which this value-typed stubAPIServer{} cannot satisfy through promotion
// alone.
func (stubAPIServer) GetConfigStatus(ctx context.Context, req *pb.GetConfigStatusRequest) (*pb.GetConfigStatusResponse, error) {
	return &pb.GetConfigStatusResponse{}, nil
}

func (stubAPIServer) ListConfigHistory(ctx context.Context, req *pb.ListConfigHistoryRequest) (*pb.ListConfigHistoryResponse, error) {
	return &pb.ListConfigHistoryResponse{}, nil
}

func (stubAPIServer) GetConfigVersion(ctx context.Context, req *pb.GetConfigVersionRequest) (*pb.GetConfigVersionResponse, error) {
	return &pb.GetConfigVersionResponse{}, nil
}

// GetResendAvailability/ResendDeviceConfig (FR42, Phase 4) are stubbed
// here for the same reason as GetConfigStatus/ListConfigHistory/
// GetConfigVersion above -- Scaffold only adds them to the proto; real
// behavior lands in the Implementation-phase task.
func (stubAPIServer) GetResendAvailability(ctx context.Context, req *pb.GetResendAvailabilityRequest) (*pb.GetResendAvailabilityResponse, error) {
	return &pb.GetResendAvailabilityResponse{}, nil
}

func (stubAPIServer) ResendDeviceConfig(ctx context.Context, req *pb.ResendDeviceConfigRequest) (*pb.ResendDeviceConfigResponse, error) {
	return &pb.ResendDeviceConfigResponse{}, nil
}

// The six admin/fleet-health RPCs below (FR10, FR12 activation, FR79) have
// no business logic yet -- Scaffold only adds them to the proto (see
// api.proto's "Admin" and "Fleet health listing" sections) -- so these
// canned stubs exist purely so stubAPIServer, a value type embedding
// pb.UnimplementedLeafLabAPIServer (whose promoted methods have pointer
// receivers), keeps satisfying pb.LeafLabAPIServer. Real behavior lands in
// the Implementation-phase task that wires these into server.go.
func (stubAPIServer) ResolveToHousehold(ctx context.Context, req *pb.ResolveToHouseholdRequest) (*pb.ResolveToHouseholdResponse, error) {
	return &pb.ResolveToHouseholdResponse{}, nil
}

func (stubAPIServer) ListFleetHealth(ctx context.Context, req *pb.ListFleetHealthRequest) (*pb.ListFleetHealthResponse, error) {
	return &pb.ListFleetHealthResponse{}, nil
}

func (stubAPIServer) Elevate(ctx context.Context, req *pb.ElevateRequest) (*pb.ElevateResponse, error) {
	return &pb.ElevateResponse{}, nil
}

func (stubAPIServer) RenewElevation(ctx context.Context, req *pb.RenewElevationRequest) (*pb.RenewElevationResponse, error) {
	return &pb.RenewElevationResponse{}, nil
}

func (stubAPIServer) EndElevation(ctx context.Context, req *pb.EndElevationRequest) (*pb.EndElevationResponse, error) {
	return &pb.EndElevationResponse{}, nil
}

func (stubAPIServer) GetElevationStatus(ctx context.Context, req *pb.GetElevationStatusRequest) (*pb.GetElevationStatusResponse, error) {
	return &pb.GetElevationStatusResponse{}, nil
}

// startTestServer builds the exact production interceptor chain
// (buildServer, shared with run()) behind a bufconn listener, backed by
// stubAPIServer and fakeBearerAuthUnary/Stream in place of
// Postgres/RabbitMQ and a real OIDC verifier respectively.
func startTestServer(t *testing.T, logger *slog.Logger) *grpc.ClientConn {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	grpcServer := buildServer(fakeBearerAuthUnary(), fakeBearerAuthStream(), logger, stubAPIServer{}, false)

	go func() {
		// Serve returns a non-nil error on Stop() too; cleanup already
		// knows the server is going down, nothing to assert here.
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

func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func assertUnauthenticated(t *testing.T, rpc string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s with no credential returned nil error, want Unauthenticated", rpc)
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("%s: got code %v, want Unauthenticated: %v", rpc, status.Code(err), err)
	}
}

// TestRPCs_NoCredential_Unauthenticated covers the Testing section's first
// bullet: PushDeviceConfig, GetDeviceConfig and ListBoards each return
// Unauthenticated with no credential.
func TestRPCs_NoCredential_Unauthenticated(t *testing.T) {
	conn := startTestServer(t, discardTestLogger())
	client := pb.NewLeafLabAPIClient(conn)
	ctx := context.Background()

	t.Run("PushDeviceConfig", func(t *testing.T) {
		_, err := client.PushDeviceConfig(ctx, &pb.PushDeviceConfigRequest{DeviceId: "board-1"})
		assertUnauthenticated(t, "PushDeviceConfig", err)
	})
	t.Run("GetDeviceConfig", func(t *testing.T) {
		_, err := client.GetDeviceConfig(ctx, &pb.GetDeviceConfigRequest{DeviceId: "board-1"})
		assertUnauthenticated(t, "GetDeviceConfig", err)
	})
	t.Run("ListBoards", func(t *testing.T) {
		_, err := client.ListBoards(ctx, &pb.ListBoardsRequest{})
		assertUnauthenticated(t, "ListBoards", err)
	})
}

// TestRPCs_ValidCredential_Succeeds covers the Testing section's first
// bullet's second half: each of the three RPCs succeeds with a valid
// credential (i.e. the enforcement interceptor lets an authenticated
// request reach the handler).
func TestRPCs_ValidCredential_Succeeds(t *testing.T) {
	conn := startTestServer(t, discardTestLogger())
	client := pb.NewLeafLabAPIClient(conn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+testValidToken)

	t.Run("PushDeviceConfig", func(t *testing.T) {
		resp, err := client.PushDeviceConfig(ctx, &pb.PushDeviceConfigRequest{DeviceId: "board-1"})
		if err != nil {
			t.Fatalf("expected success with a valid credential, got %v", err)
		}
		if resp.Version != 1 {
			t.Errorf("Version = %d, want 1 (stub response)", resp.Version)
		}
	})
	t.Run("GetDeviceConfig", func(t *testing.T) {
		if _, err := client.GetDeviceConfig(ctx, &pb.GetDeviceConfigRequest{DeviceId: "board-1"}); err != nil {
			t.Fatalf("expected success with a valid credential, got %v", err)
		}
	})
	t.Run("ListBoards", func(t *testing.T) {
		if _, err := client.ListBoards(ctx, &pb.ListBoardsRequest{}); err != nil {
			t.Fatalf("expected success with a valid credential, got %v", err)
		}
	})
}

// TestGetHealth_NoCredential_Succeeds_OverTheWire proves FR63.2's allowlist
// bypass end-to-end through the real interceptor chain (not just
// auth.go's interceptor in isolation -- see auth_test.go -- or
// LeafLabAPIServer.GetHealth in isolation -- see server_test.go).
func TestGetHealth_NoCredential_Succeeds_OverTheWire(t *testing.T) {
	conn := startTestServer(t, discardTestLogger())
	client := pb.NewLeafLabAPIClient(conn)

	resp, err := client.GetHealth(context.Background(), &pb.GetHealthRequest{})
	if err != nil {
		t.Fatalf("GetHealth with no credential returned an error, want success: %v", err)
	}
	if resp.Status != pb.HealthStatus_HEALTH_UP {
		t.Errorf("Status = %v, want HEALTH_UP (stub response)", resp.Status)
	}
}

// TestCredentialLeak_TokenNeverInLogsOrErrors is NFR13's "credentials are
// never logged, returned, or included in an error detail" checked two ways:
// a valid token presented on a successful call must not appear in captured
// logs, and an invalid token presented on a rejected call must not appear
// in the returned error or in captured logs.
func TestCredentialLeak_TokenNeverInLogsOrErrors(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	conn := startTestServer(t, logger)
	client := pb.NewLeafLabAPIClient(conn)

	authCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+testValidToken)
	if _, err := client.GetDeviceConfig(authCtx, &pb.GetDeviceConfigRequest{DeviceId: "board-1"}); err != nil {
		t.Fatalf("authenticated call failed: %v", err)
	}
	if strings.Contains(logBuf.String(), testValidToken) {
		t.Errorf("log output contains the raw valid token: %s", logBuf.String())
	}

	const wrongToken = "wrong-token-marker-should-never-leak" //nolint:gosec // test fixture
	badCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+wrongToken)
	_, err := client.GetDeviceConfig(badCtx, &pb.GetDeviceConfigRequest{DeviceId: "board-1"})
	assertUnauthenticated(t, "GetDeviceConfig", err)
	if strings.Contains(err.Error(), wrongToken) {
		t.Errorf("returned error leaks the presented token: %v", err)
	}
	if strings.Contains(logBuf.String(), wrongToken) {
		t.Errorf("log output contains the rejected token: %s", logBuf.String())
	}
}

// TestLogs_CarrySubjectAndCorrelationID is NFR12's interim audit record:
// an authenticated call's log line carries the acting subject and a
// request correlation id.
func TestLogs_CarrySubjectAndCorrelationID(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	conn := startTestServer(t, logger)
	client := pb.NewLeafLabAPIClient(conn)

	authCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+testValidToken)
	if _, err := client.GetDeviceConfig(authCtx, &pb.GetDeviceConfigRequest{DeviceId: "board-1"}); err != nil {
		t.Fatalf("authenticated call failed: %v", err)
	}

	var entry map[string]any
	if err := json.NewDecoder(&logBuf).Decode(&entry); err != nil {
		t.Fatalf("failed to decode log line %q: %v", logBuf.String(), err)
	}
	if entry["subject"] != testClaims.Subject {
		t.Errorf("subject = %v, want %q", entry["subject"], testClaims.Subject)
	}
	cid, _ := entry["correlation_id"].(string)
	if cid == "" {
		t.Errorf("correlation_id missing or empty: %v", entry["correlation_id"])
	}
	const wantMethod = "/leaflab.api.v1.LeafLabAPI/GetDeviceConfig"
	if entry["method"] != wantMethod {
		t.Errorf("method = %v, want %q", entry["method"], wantMethod)
	}
}

func hasReflectionService(s *grpc.Server) bool {
	for name := range s.GetServiceInfo() {
		if strings.Contains(strings.ToLower(name), "reflection") {
			return true
		}
	}
	return false
}

func noopUnary(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	return handler(ctx, req)
}

func noopStream(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return handler(srv, ss)
}

// TestBuildServer_ReflectionRegisteredOnlyInDevMode asserts FR11.1's
// "server reflection is disabled outside development" directly on the
// production wiring, without dialing Postgres/RabbitMQ.
func TestBuildServer_ReflectionRegisteredOnlyInDevMode(t *testing.T) {
	prod := buildServer(noopUnary, noopStream, discardTestLogger(), stubAPIServer{}, false)
	if hasReflectionService(prod) {
		t.Errorf("reflection registered with devMode=false, want not registered (FR11): %v", prod.GetServiceInfo())
	}

	dev := buildServer(noopUnary, noopStream, discardTestLogger(), stubAPIServer{}, true)
	if !hasReflectionService(dev) {
		t.Errorf("reflection not registered with devMode=true, want registered: %v", dev.GetServiceInfo())
	}
}

// TestValidateAuthBootConfig covers FR11.1's boot-time refusal directly:
// AuthModeNone is refused unless DevMode is explicitly true; every other
// combination boots.
func TestValidateAuthBootConfig(t *testing.T) {
	cases := []struct {
		name    string
		mode    grpcauth.AuthMode
		devMode bool
		wantErr bool
	}{
		{"AuthModeNone outside dev mode is refused", grpcauth.AuthModeNone, false, true},
		{"AuthModeNone in dev mode is allowed", grpcauth.AuthModeNone, true, false},
		{"AuthModeOIDC outside dev mode is allowed", grpcauth.AuthModeOIDC, false, false},
		{"AuthModeOIDC in dev mode is allowed", grpcauth.AuthModeOIDC, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAuthBootConfig(tc.mode, tc.devMode)
			if tc.wantErr && err == nil {
				t.Fatal("expected a boot-time refusal error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestRun_RefusesAuthModeNoneOutsideDevMode exercises run() itself (not
// just validateAuthBootConfig in isolation) to prove the refusal actually
// happens on the boot path, before any dependency is dialed: PG_DATABASE_URL
// and RABBITMQ_URL are left empty/default here, and OTEL_SDK_DISABLED is
// set so logging.Configure's EnableOTLP:true never attempts to reach an
// exporter -- if the refusal did not fire before db.NewPool/rmq.NewConnection,
// this test would hang or fail on those dependencies instead of returning
// the expected config error.
func TestRun_RefusesAuthModeNoneOutsideDevMode(t *testing.T) {
	t.Setenv("LEAFLAB_API_AUTH_MODE", "none")
	t.Setenv("LEAFLAB_API_DEV_MODE", "false")
	t.Setenv("OTEL_SDK_DISABLED", "true")

	err := run()
	if err == nil {
		t.Fatal("run() succeeded, want a boot-time refusal (FR11.1)")
	}
	if !strings.Contains(err.Error(), "LEAFLAB_API_DEV_MODE=true") {
		t.Errorf("error = %v, want a message referencing the dev-mode requirement", err)
	}
}
