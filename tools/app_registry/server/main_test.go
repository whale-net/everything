package main

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/grpcauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	registryauth "github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	"google.golang.org/grpc"
	"github.com/whale-net/everything/tools/app_registry/kinds"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// startTestServer builds the real registerServices() wiring behind a
// bufconn listener — no TCP port, no Postgres. This is the exact
// registration surface run() uses in production; only the transport and the
// (absent) DB pool differ, so a missing RegisterXxxServer call or a health
// server that never flips to SERVING would be caught here exactly as it
// would in the real binary. The auth interceptor runs in AuthModeNone with
// DevRoles set to every app-registry role, matching run()'s production
// wiring (see main.go) — every RPC call below is implicitly authenticated
// as a superuser, same as local Tilt.
func startTestServer(t *testing.T) *grpc.ClientConn {
	t.Helper()

	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:     grpcauth.AuthModeNone,
		DevRoles: registryauth.AllRoles(),
	})
	if err != nil {
		t.Fatalf("failed to create auth interceptors: %v", err)
	}

	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryInt),
		grpc.ChainStreamInterceptor(streamInt),
	)
	registerServices(grpcServer, fake.New(), nil, kinds.NewAppMetadataRegistry(), "", "", "", "", "", "")

	go func() {
		// Serve returns a non-nil error on Stop()/GracefulStop() too; the
		// test's own cleanup already knows the server is going down, so
		// there's nothing to assert on here.
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

// assertUnimplementedFromHandler fails the test unless err is a gRPC status
// error with code Unimplemented AND a message matching wantMsg.
//
// Code alone is not enough: grpc-go returns codes.Unimplemented with message
// "unknown service ..." for a service that was never registered at all, same
// code as our handlers' deliberate status.Error(codes.Unimplemented, "...").
// A dropped pb.RegisterXxxServer call would therefore still show
// codes.Unimplemented and pass a code-only check — this is what caught that
// exact regression when this test was first written; the message assertion
// is load-bearing, not decorative.
func assertUnimplementedFromHandler(t *testing.T, rpc, wantMsg string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an Unimplemented error, got nil", rpc)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("%s: expected a gRPC status error, got %v", rpc, err)
	}
	if st.Code() != codes.Unimplemented {
		t.Fatalf("%s: expected codes.Unimplemented, got %v (%v)", rpc, st.Code(), err)
	}
	if strings.Contains(st.Message(), "unknown service") || strings.Contains(st.Message(), "unknown method") {
		t.Fatalf("%s: got grpc's own %q — the service was never registered, not just unimplemented", rpc, st.Message())
	}
	if st.Message() != wantMsg {
		t.Fatalf("%s: expected message %q from the handler, got %q", rpc, wantMsg, st.Message())
	}
}

// TestRegisterServices_AllFourServicesReachable asserts every service from
// protos/api.proto is registered and reaches its handler. grpc-go answers an
// unregistered service with codes.Unimplemented too ("unknown service ..."),
// so code alone can't tell "never registered" apart from "registered but the
// RPC is genuinely unimplemented" — assertUnimplementedFromHandler checks
// the message text for exactly that reason. A dropped pb.RegisterXxxServer
// call in main.go fails this test.
//
// AppRegistry, ArtifactRegistry, EnvironmentRegistry, and PromotionRegistry
// are all real now (AR-2a, AR-3b, AR-3c respectively), so those subtests
// assert a real (empty-but-successful) response against the fake repository
// instead of Unimplemented — a dropped Register call would now surface as
// codes.Unavailable/Unimplemented-with-"unknown service" instead, still
// caught below.
func TestRegisterServices_AllFourServicesReachable(t *testing.T) {
	conn := startTestServer(t)
	ctx := context.Background()

	t.Run("AppRegistry", func(t *testing.T) {
		client := pb.NewAppRegistryClient(conn)
		resp, err := client.ListApps(ctx, &pb.ListAppsRequest{})
		if err != nil {
			t.Fatalf("AppRegistry.ListApps: expected success against the fake repository, got %v", err)
		}
		if len(resp.Apps) != 0 {
			t.Fatalf("AppRegistry.ListApps: expected no apps in a fresh fake registry, got %d", len(resp.Apps))
		}
	})

	t.Run("ArtifactRegistry", func(t *testing.T) {
		client := pb.NewArtifactRegistryClient(conn)
		resp, err := client.ListArtifacts(ctx, &pb.ListArtifactsRequest{})
		if err != nil {
			t.Fatalf("ArtifactRegistry.ListArtifacts: expected success against the fake repository, got %v", err)
		}
		if len(resp.Artifacts) != 0 {
			t.Fatalf("ArtifactRegistry.ListArtifacts: expected no artifacts in a fresh fake registry, got %d", len(resp.Artifacts))
		}
	})

	// AllocateVersion is real as of AR-5a (see PLAN.md's AR-5 status), but
	// still unreachable in production: every domain defaults to adoption
	// stage "observe" (no domain_adoption row), so an empty request reaches
	// the real handler and fails validation ("kind is required") rather
	// than codes.Unimplemented's "unknown service"/"not implemented" —
	// still enough to prove pb.RegisterArtifactRegistryServer wasn't
	// dropped, which is this whole test's job. See handlers/artifact_test.go
	// for AllocateVersion's actual business-logic coverage (gate, retry,
	// idempotency replay).
	t.Run("ArtifactRegistry_AllocateVersion_ReachesRealHandler", func(t *testing.T) {
		client := pb.NewArtifactRegistryClient(conn)
		_, err := client.AllocateVersion(ctx, &pb.AllocateVersionRequest{})
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("ArtifactRegistry.AllocateVersion: expected a gRPC status error, got %v", err)
		}
		if st.Code() != codes.InvalidArgument {
			t.Fatalf("ArtifactRegistry.AllocateVersion: expected codes.InvalidArgument, got %v (%v)", st.Code(), err)
		}
		if strings.Contains(st.Message(), "unknown service") || strings.Contains(st.Message(), "unknown method") {
			t.Fatalf("ArtifactRegistry.AllocateVersion: got grpc's own %q — the service was never registered", st.Message())
		}
	})

	t.Run("PromotionRegistry", func(t *testing.T) {
		client := pb.NewPromotionRegistryClient(conn)
		resp, err := client.ListPromotions(ctx, &pb.ListPromotionsRequest{})
		if err != nil {
			t.Fatalf("PromotionRegistry.ListPromotions: expected success against the fake repository, got %v", err)
		}
		if len(resp.Promotions) != 0 {
			t.Fatalf("PromotionRegistry.ListPromotions: expected no promotions in a fresh fake registry, got %d", len(resp.Promotions))
		}
	})

	t.Run("PromotionRegistry_Promote_StillReachable", func(t *testing.T) {
		client := pb.NewPromotionRegistryClient(conn)
		// "dev" matches DevRoles' app-registry-promoter-dev grant; a
		// PermissionDenied here would mean auth never ran, and a "not
		// registered" error would surface as Unimplemented/Unavailable —
		// NotFound (unknown environment, since the fake repo is empty) is
		// the only outcome proving the RPC actually reached the handler.
		_, err := client.Promote(ctx, &pb.PromoteRequest{EnvironmentKey: "dev", ArtifactId: "nonexistent", IdempotencyKey: "test"})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("PromotionRegistry.Promote: expected NotFound (unknown environment) against the fake repository, got %v", err)
		}
	})

	t.Run("EnvironmentRegistry", func(t *testing.T) {
		client := pb.NewEnvironmentRegistryClient(conn)
		resp, err := client.ListEnvironments(ctx, &pb.ListEnvironmentsRequest{})
		if err != nil {
			t.Fatalf("EnvironmentRegistry.ListEnvironments: expected success against the fake repository, got %v", err)
		}
		if len(resp.Environments) != 0 {
			t.Fatalf("EnvironmentRegistry.ListEnvironments: expected no environments in a fresh fake registry, got %d", len(resp.Environments))
		}
	})

	t.Run("EnvironmentRegistry_ArchiveEnvironment_StillReachable", func(t *testing.T) {
		client := pb.NewEnvironmentRegistryClient(conn)
		_, err := client.ArchiveEnvironment(ctx, &pb.ArchiveEnvironmentRequest{Key: "nonexistent", Reason: "test"})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("EnvironmentRegistry.ArchiveEnvironment: expected codes.NotFound against a fresh fake registry, got %v", err)
		}
	})
}

// TestRegisterServices_HealthCheckServing asserts the health server is wired
// up and reports SERVING, matching what the AR-1 exit criteria ("server
// starts... and answers reflection + health") requires.
func TestRegisterServices_HealthCheckServing(t *testing.T) {
	conn := startTestServer(t)

	healthClient := grpc_health_v1.NewHealthClient(conn)
	resp, err := healthClient.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("expected SERVING, got %v", resp.Status)
	}
}

// TestRegisterServices_FR72_MissingBucketPanics verifies FR-72's startup
// validation: if ANY S3 config is provided, BUCKET must be present, or the
// server panics at startup with an explicit error naming the missing value.
func TestRegisterServices_FR72_MissingBucketPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected a panic for missing RELEASE_TOOLS_S3_BUCKET, got none")
		} else if !strings.Contains(r.(string), "RELEASE_TOOLS_S3_BUCKET") {
			t.Fatalf("expected panic mentioning RELEASE_TOOLS_S3_BUCKET, got: %v", r)
		}
	}()

	grpcServer := grpc.NewServer()
	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:     grpcauth.AuthModeNone,
		DevRoles: registryauth.AllRoles(),
	})
	if err != nil {
		t.Fatalf("failed to create auth interceptors: %v", err)
	}
	grpcServer = grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryInt),
		grpc.ChainStreamInterceptor(streamInt),
	)

	registerServices(grpcServer, fake.New(), nil, kinds.NewAppMetadataRegistry(),
		"", // BUCKET (missing — should trigger panic)
		"https://s3.example.com/", "https://public.example.com/", "us-east-1", "key", "secret")
}

// TestRegisterServices_FR72_MissingEndpointPanics verifies FR-72's startup
// validation for the primary/internal S3 endpoint.
func TestRegisterServices_FR72_MissingEndpointPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected a panic for missing RELEASE_TOOLS_S3_ENDPOINT, got none")
		} else if !strings.Contains(r.(string), "RELEASE_TOOLS_S3_ENDPOINT") {
			t.Fatalf("expected panic mentioning RELEASE_TOOLS_S3_ENDPOINT, got: %v", r)
		}
	}()

	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:     grpcauth.AuthModeNone,
		DevRoles: registryauth.AllRoles(),
	})
	if err != nil {
		t.Fatalf("failed to create auth interceptors: %v", err)
	}
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryInt),
		grpc.ChainStreamInterceptor(streamInt),
	)

	registerServices(grpcServer, fake.New(), nil, kinds.NewAppMetadataRegistry(),
		"bucket", "", // ENDPOINT (missing — should trigger panic)
		"https://public.example.com/", "us-east-1", "key", "secret")
}

// TestRegisterServices_FR72_MissingPublicEndpointPanics verifies FR-72's
// startup validation for the public endpoint.
func TestRegisterServices_FR72_MissingPublicEndpointPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected a panic for missing RELEASE_TOOLS_S3_PUBLIC_ENDPOINT, got none")
		} else if !strings.Contains(r.(string), "RELEASE_TOOLS_S3_PUBLIC_ENDPOINT") {
			t.Fatalf("expected panic mentioning RELEASE_TOOLS_S3_PUBLIC_ENDPOINT, got: %v", r)
		}
	}()

	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:     grpcauth.AuthModeNone,
		DevRoles: registryauth.AllRoles(),
	})
	if err != nil {
		t.Fatalf("failed to create auth interceptors: %v", err)
	}
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryInt),
		grpc.ChainStreamInterceptor(streamInt),
	)

	registerServices(grpcServer, fake.New(), nil, kinds.NewAppMetadataRegistry(),
		"bucket", "https://s3.example.com/", "", // PUBLIC_ENDPOINT (missing — should trigger panic)
		"us-east-1", "key", "secret")
}

// TestRegisterServices_FR72_MissingRegionPanics verifies FR-72's startup
// validation for the region (needed for signing).
func TestRegisterServices_FR72_MissingRegionPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected a panic for missing RELEASE_TOOLS_S3_REGION, got none")
		} else if !strings.Contains(r.(string), "RELEASE_TOOLS_S3_REGION") {
			t.Fatalf("expected panic mentioning RELEASE_TOOLS_S3_REGION, got: %v", r)
		}
	}()

	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:     grpcauth.AuthModeNone,
		DevRoles: registryauth.AllRoles(),
	})
	if err != nil {
		t.Fatalf("failed to create auth interceptors: %v", err)
	}
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryInt),
		grpc.ChainStreamInterceptor(streamInt),
	)

	registerServices(grpcServer, fake.New(), nil, kinds.NewAppMetadataRegistry(),
		"bucket", "https://s3.example.com/", "https://public.example.com/",
		"", // REGION (missing — should trigger panic)
		"key", "secret")
}

// TestRegisterServices_FR72_MissingAccessKeyPanics verifies FR-72's startup
// validation for the access key credential (NFR-22's grant).
func TestRegisterServices_FR72_MissingAccessKeyPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected a panic for missing RELEASE_TOOLS_S3_ACCESS_KEY, got none")
		} else if !strings.Contains(r.(string), "RELEASE_TOOLS_S3_ACCESS_KEY") {
			t.Fatalf("expected panic mentioning RELEASE_TOOLS_S3_ACCESS_KEY, got: %v", r)
		}
	}()

	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:     grpcauth.AuthModeNone,
		DevRoles: registryauth.AllRoles(),
	})
	if err != nil {
		t.Fatalf("failed to create auth interceptors: %v", err)
	}
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryInt),
		grpc.ChainStreamInterceptor(streamInt),
	)

	registerServices(grpcServer, fake.New(), nil, kinds.NewAppMetadataRegistry(),
		"bucket", "https://s3.example.com/", "https://public.example.com/", "us-east-1",
		"", // ACCESS_KEY (missing — should trigger panic)
		"secret")
}

// TestRegisterServices_FR72_MissingSecretKeyPanics verifies FR-72's startup
// validation for the secret key credential.
func TestRegisterServices_FR72_MissingSecretKeyPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected a panic for missing RELEASE_TOOLS_S3_SECRET_KEY, got none")
		} else if !strings.Contains(r.(string), "RELEASE_TOOLS_S3_SECRET_KEY") {
			t.Fatalf("expected panic mentioning RELEASE_TOOLS_S3_SECRET_KEY, got: %v", r)
		}
	}()

	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:     grpcauth.AuthModeNone,
		DevRoles: registryauth.AllRoles(),
	})
	if err != nil {
		t.Fatalf("failed to create auth interceptors: %v", err)
	}
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryInt),
		grpc.ChainStreamInterceptor(streamInt),
	)

	registerServices(grpcServer, fake.New(), nil, kinds.NewAppMetadataRegistry(),
		"bucket", "https://s3.example.com/", "https://public.example.com/", "us-east-1", "key",
		"") // SECRET_KEY (missing — should trigger panic)
}

// TestRegisterServices_FR72_AllConfigPresentNosPanic verifies that when all
// four S3 config values are present, registerServices does not panic.
func TestRegisterServices_FR72_AllConfigPresentNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("expected no panic when all S3 config is present, got: %v", r)
		}
	}()

	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:     grpcauth.AuthModeNone,
		DevRoles: registryauth.AllRoles(),
	})
	if err != nil {
		t.Fatalf("failed to create auth interceptors: %v", err)
	}
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryInt),
		grpc.ChainStreamInterceptor(streamInt),
	)

	registerServices(grpcServer, fake.New(), nil, kinds.NewAppMetadataRegistry(),
		"bucket", "https://s3.example.com/", "https://public.example.com/", "us-east-1",
		"key", "secret")
}

// TestRegisterServices_FR72_NoConfigNoPanic verifies that when NO S3 config
// is provided (all empty strings), registerServices does not panic. This is
// the expected path for deployments not doing binary resolution.
func TestRegisterServices_FR72_NoConfigNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("expected no panic when no S3 config is present, got: %v", r)
		}
	}()

	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(context.Background(), grpcauth.ServerConfig{
		Mode:     grpcauth.AuthModeNone,
		DevRoles: registryauth.AllRoles(),
	})
	if err != nil {
		t.Fatalf("failed to create auth interceptors: %v", err)
	}
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryInt),
		grpc.ChainStreamInterceptor(streamInt),
	)

	registerServices(grpcServer, fake.New(), nil, kinds.NewAppMetadataRegistry(),
		"", "", "", "", "", "") // All empty — should not panic
}
