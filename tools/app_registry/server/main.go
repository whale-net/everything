// Command app-registry-api is the App Registry's gRPC server. AppRegistry and
// ArtifactRegistry are real as of AR-2a; EnvironmentRegistry is real as of
// AR-3b; PromotionRegistry is real as of AR-3c. See ../ARCHITECTURE.md and
// ../README.md.
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
	temporallib "github.com/whale-net/everything/libs/go/temporal"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	registryauth "github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/handlers"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/postgres"
	"github.com/whale-net/everything/tools/app_registry/kinds"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.temporal.io/sdk/client"
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

	// Issue #979/#983/#1101: ArtifactServer.ResolveBinaryURL's release-tools
	// S3 bucket. Credentials are read here (unlike the original NFR2 design)
	// because the bucket was never actually configured for anonymous/public
	// reads -- ResolveBinaryURL presigns with these instead. See ENV.md's
	// "CLI binary S3" section.
	releaseToolsS3Bucket := getEnv("RELEASE_TOOLS_S3_BUCKET", "")
	releaseToolsS3PublicEndpoint := getEnv("RELEASE_TOOLS_S3_PUBLIC_ENDPOINT", "")
	releaseToolsS3Region := getEnv("RELEASE_TOOLS_S3_REGION", "")
	releaseToolsS3AccessKey := getEnv("RELEASE_TOOLS_S3_ACCESS_KEY", "")
	releaseToolsS3SecretKey := getEnv("RELEASE_TOOLS_S3_SECRET_KEY", "")

	// Initialize database pool (reads PG_DATABASE_URL)
	log.Println("Connecting to database...")
	pool, err := db.NewPool(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer pool.Close()
	repo := postgres.NewRepository(pool)
	log.Println("Database connection established")

	// Load app metadata from the checkout (FR-36: central authoring site)
	// This loads all release_app declarations and makes them available to
	// lookupBinaryOwner and other metadata-based lookups.
	log.Println("Loading app metadata from checkout...")
	metadataRegistry := kinds.NewAppMetadataRegistry()
	checkoutRoot := getEnv("CHECKOUT_ROOT", ".")
	if err := metadataRegistry.LoadFromCheckout(ctx, checkoutRoot); err != nil {
		// Log but don't fail: if metadata files are unavailable, the registry
		// simply stays empty and lookupBinaryOwner falls back gracefully.
		log.Printf("Warning: failed to load app metadata from checkout: %v", err)
	}

	// Temporal client, so ReleaseServer.TriggerRelease (issue #889) can
	// start ReleaseWorkflow executions directly from this process -- see
	// worker/main.go's identical NewClient construction (the worker
	// process this client's workflow starts on; see release.TaskQueue).
	temporalClient, err := temporallib.NewClient(temporallib.ConfigFromEnv(), temporallib.NewLogger("app-registry-api"))
	if err != nil {
		return fmt.Errorf("failed to connect to temporal: %w", err)
	}
	defer temporalClient.Close()

	// Create auth interceptors. DevRoles matters only in AuthModeNone: it
	// makes the injected dev Claims carry every app-registry role, not
	// grpcauth's generic default of ["admin"] (which satisfies none of our
	// service-prefixed role checks — see server/auth). Without this, local
	// Tilt and any CI path running with GRPC_AUTH_MODE=none would fail every
	// write RPC's role check. See ENV.md.
	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(ctx, grpcauth.ServerConfig{
		Mode:      grpcauth.AuthMode(grpcAuthMode),
		IssuerURL: grpcOIDCIssuer,
		ClientID:  grpcOIDCClientID,
		DevRoles:  registryauth.AllRoles(),
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

	healthServer := registerServices(grpcServer, repo, temporalClient, metadataRegistry, releaseToolsS3Bucket, releaseToolsS3PublicEndpoint, releaseToolsS3Region, releaseToolsS3AccessKey, releaseToolsS3SecretKey)

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
// real database or gRPC auth setup required — see main_test.go, which passes
// a fake repository.Registry (and a nil temporalClient -- see
// handlers.ReleaseServer's temporal field doc comment).
func registerServices(grpcServer *grpc.Server, repo repository.Registry, temporalClient client.Client, metadataRegistry *kinds.AppMetadataRegistry, releaseToolsS3Bucket, releaseToolsS3PublicEndpoint, releaseToolsS3Region, releaseToolsS3AccessKey, releaseToolsS3SecretKey string) *health.Server {
	// AppRegistry and ArtifactRegistry are real as of AR-2a (AllocateVersion
	// stays Unimplemented — that's AR-5). EnvironmentRegistry is real as of
	// AR-3b. PromotionRegistry is real as of AR-3c. ReleaseRegistry is real
	// as of issue #889 (TriggerRelease starts ReleaseWorkflow via
	// temporalClient; #888 landed TriggerRelease/GetRelease's repository
	// half first).
	pb.RegisterAppRegistryServer(grpcServer, handlers.NewAppServer(repo))
	pb.RegisterArtifactRegistryServer(grpcServer, handlers.NewArtifactServer(repo, handlers.WithMetadataRegistry(metadataRegistry), handlers.WithReleaseToolsS3(releaseToolsS3Bucket, releaseToolsS3PublicEndpoint, releaseToolsS3Region, releaseToolsS3AccessKey, releaseToolsS3SecretKey)))
	pb.RegisterPromotionRegistryServer(grpcServer, handlers.NewPromotionServer(repo, temporalClient))
	pb.RegisterEnvironmentRegistryServer(grpcServer, handlers.NewEnvironmentServer(repo))
	pb.RegisterReleaseRegistryServer(grpcServer, handlers.NewReleaseServer(repo, temporalClient))

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
