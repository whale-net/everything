package main

import (
	"context"
	"net"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// startTestServer builds the real registerServices() wiring behind a
// bufconn listener — no TCP port, no Postgres, no gRPC auth. This is the
// exact registration surface run() uses in production; only the transport
// and the (absent) DB pool differ, so a missing RegisterXxxServer call or a
// health server that never flips to SERVING would be caught here exactly as
// it would in the real binary.
func startTestServer(t *testing.T) *grpc.ClientConn {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	registerServices(grpcServer)

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
func TestRegisterServices_AllFourServicesReachable(t *testing.T) {
	conn := startTestServer(t)
	ctx := context.Background()

	t.Run("AppRegistry", func(t *testing.T) {
		client := pb.NewAppRegistryClient(conn)
		_, err := client.ListApps(ctx, &pb.ListAppsRequest{})
		assertUnimplementedFromHandler(t, "AppRegistry.ListApps", "ListApps not implemented", err)
	})

	t.Run("ArtifactRegistry", func(t *testing.T) {
		client := pb.NewArtifactRegistryClient(conn)
		_, err := client.ListArtifacts(ctx, &pb.ListArtifactsRequest{})
		assertUnimplementedFromHandler(t, "ArtifactRegistry.ListArtifacts", "ListArtifacts not implemented", err)
	})

	t.Run("PromotionRegistry", func(t *testing.T) {
		client := pb.NewPromotionRegistryClient(conn)
		_, err := client.ListPromotions(ctx, &pb.ListPromotionsRequest{})
		assertUnimplementedFromHandler(t, "PromotionRegistry.ListPromotions", "ListPromotions not implemented", err)
	})

	t.Run("EnvironmentRegistry", func(t *testing.T) {
		client := pb.NewEnvironmentRegistryClient(conn)
		_, err := client.ListEnvironments(ctx, &pb.ListEnvironmentsRequest{})
		assertUnimplementedFromHandler(t, "EnvironmentRegistry.ListEnvironments", "ListEnvironments not implemented", err)
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
