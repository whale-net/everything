// Command app-registry-api is the App Registry's gRPC server. AR-1 wires up
// the four services from protos/api.proto with no business logic — every RPC
// returns codes.Unimplemented. See ../ARCHITECTURE.md and ../README.md.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/handlers"
	"github.com/whale-net/everything/tools/app_registry/server/repository/postgres"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("Fatal error: %v", err)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logging.Configure(logging.Config{
		ServiceName:   "app-registry-api",
		Domain:        "app-registry",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	defer logging.Shutdown(ctx) //nolint:errcheck

	port := getEnv("PORT", "50051")
	grpcAuthMode := getEnv("GRPC_AUTH_MODE", "none")
	grpcOIDCIssuer := getEnv("GRPC_OIDC_ISSUER", "")
	grpcOIDCClientID := getEnv("GRPC_OIDC_CLIENT_ID", "")

	// Initialize database pool (reads PG_DATABASE_URL)
	log.Println("Connecting to database...")
	pool, err := db.NewPool(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer pool.Close()
	repo := postgres.NewRepository(pool)
	_ = repo // wired into handlers starting AR-2
	log.Println("Database connection established")

	// Create auth interceptors
	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(ctx, grpcauth.ServerConfig{
		Mode:      grpcauth.AuthMode(grpcAuthMode),
		IssuerURL: grpcOIDCIssuer,
		ClientID:  grpcOIDCClientID,
	})
	if err != nil {
		return fmt.Errorf("failed to create auth interceptors: %w", err)
	}

	// Create gRPC server
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(10*1024*1024), // 10 MB
		grpc.MaxSendMsgSize(10*1024*1024), // 10 MB
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(unaryInt),
		grpc.ChainStreamInterceptor(streamInt),
	)

	healthServer := registerServices(grpcServer)

	// Start listening
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %s: %w", port, err)
	}

	log.Printf("App Registry API listening on :%s", port)

	// Handle graceful shutdown
	done := make(chan error, 1)
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh

		log.Println("Shutting down gracefully...")
		healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
		grpcServer.GracefulStop()
		done <- nil
	}()

	// Start serving
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			done <- fmt.Errorf("grpc server error: %w", err)
		}
	}()

	return <-done
}

// registerServices registers all four App Registry services plus the health
// and reflection services on grpcServer, and returns the health server so
// the caller can flip it to NOT_SERVING on shutdown. Split out from run() so
// it can be exercised directly against a bufconn listener in tests, with no
// database or gRPC auth setup required — see main_test.go.
func registerServices(grpcServer *grpc.Server) *health.Server {
	// Register all four App Registry services. Every RPC returns
	// codes.Unimplemented in AR-1 — see handlers/*.go.
	pb.RegisterAppRegistryServer(grpcServer, handlers.NewAppServer())
	pb.RegisterArtifactRegistryServer(grpcServer, handlers.NewArtifactServer())
	pb.RegisterPromotionRegistryServer(grpcServer, handlers.NewPromotionServer())
	pb.RegisterEnvironmentRegistryServer(grpcServer, handlers.NewEnvironmentServer())

	// Health check — reports SERVING once the process is up. AR-1 has no
	// downstream dependency to gate on beyond the DB pool, which is already
	// required to reach this point in run().
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)

	// Register reflection service (for grpcurl, debugging)
	reflection.Register(grpcServer)

	return healthServer
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
