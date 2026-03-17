package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	rmqlib "github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/libs/go/s3"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"github.com/whale-net/everything/manmanv2/api/handlers"
	workshophandler "github.com/whale-net/everything/manmanv2/api/handlers/workshop"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	"github.com/whale-net/everything/manmanv2/api/steam"
	"github.com/whale-net/everything/manmanv2/api/workshop"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
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
		ServiceName:   "control-api",
		Domain:        "manmanv2",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	defer logging.Shutdown(ctx) //nolint:errcheck

	// Get configuration from environment
	port := getEnv("PORT", "50051")
	rabbitmqURL := getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	s3Bucket := getEnv("S3_BUCKET", "manman-logs")
	s3Region := getEnv("S3_REGION", "us-east-1")
	s3Endpoint := getEnv("S3_ENDPOINT", "")             // Optional: for S3-compatible storage (OVH, MinIO, etc.)
	s3PublicEndpoint := getEnv("S3_PUBLIC_ENDPOINT", "") // Optional: public-facing endpoint for pre-signed URLs
	s3AccessKey := getEnv("S3_ACCESS_KEY", "")           // Optional: for static credentials (MinIO, etc.)
	s3SecretKey := getEnv("S3_SECRET_KEY", "")           // Optional: for static credentials (MinIO, etc.)
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
	log.Println("Database connection established")

	// Initialize S3 client
	log.Println("Initializing S3 client...")
	s3Client, err := s3.NewClient(ctx, s3.Config{
		Bucket:         s3Bucket,
		Region:         s3Region,
		Endpoint:       s3Endpoint,
		PublicEndpoint: s3PublicEndpoint,
		AccessKey:      s3AccessKey,
		SecretKey:      s3SecretKey,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize S3 client: %w", err)
	}

	if s3Endpoint != "" {
		log.Printf("S3 client initialized (bucket: %s, region: %s, endpoint: %s)", s3Bucket, s3Region, s3Endpoint)
	} else {
		log.Printf("S3 client initialized (bucket: %s, region: %s)", s3Bucket, s3Region)
	}

	// Initialize RabbitMQ connection
	log.Println("Connecting to RabbitMQ...")
	rmqConn, err := rmqlib.NewConnectionFromURL(rabbitmqURL)
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	defer rmqConn.Close()
	log.Println("RabbitMQ connection established")

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
		grpc.MaxRecvMsgSize(10 * 1024 * 1024), // 10 MB
		grpc.MaxSendMsgSize(10 * 1024 * 1024), // 10 MB
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(unaryInt),
		grpc.ChainStreamInterceptor(streamInt),
	)

	// Register Workshop service early so workshopManager is available for APIServer
	steamAPIKey := getEnv("STEAM_API_KEY", "")
	steamClient := steam.NewSteamWorkshopClient(steamAPIKey, 30*time.Second)
	rmqPublisher, err := rmqlib.NewPublisher(rmqConn)
	if err != nil {
		return fmt.Errorf("failed to create RMQ publisher: %w", err)
	}
	defer rmqPublisher.Close()

	workshopManager := workshop.NewWorkshopManager(
		repo.WorkshopAddons,
		repo.WorkshopInstallations,
		repo.WorkshopLibraries,
		repo.ServerGameConfigs,
		repo.Games,
		repo.GameConfigs,
		repo.GameConfigVolumes,
		repo.AddonPathPresets,
		repo.Sessions,
		steamClient,
		rmqPublisher,
	)

	// Register API server
	apiServer := handlers.NewAPIServer(repo, s3Client, rmqConn, workshopManager)
	pb.RegisterManManAPIServer(grpcServer, apiServer)

	// Register Workshop service
	workshopHandler := workshophandler.NewWorkshopServiceHandler(
		repo.WorkshopAddons,
		repo.WorkshopInstallations,
		repo.WorkshopLibraries,
		repo.ServerGameConfigs,
		repo.AddonPathPresets,
		workshopManager,
	)
	pb.RegisterWorkshopServiceServer(grpcServer, workshopHandler)

	// Initialize workshop status handler for installation status updates
	log.Println("Setting up workshop status handler...")
	workshopStatusHandler, err := workshophandler.NewWorkshopStatusHandler(repo.WorkshopInstallations, rmqConn)
	if err != nil {
		return fmt.Errorf("failed to create workshop status handler: %w", err)
	}
	defer workshopStatusHandler.Close()

	// Start workshop status consumer in background
	go func() {
		if err := workshopStatusHandler.Start(ctx); err != nil {
			log.Printf("Warning: Workshop status handler stopped: %v", err)
		}
	}()
	log.Println("Workshop status handler started")

	// Register reflection service (for grpcurl, debugging)
	reflection.Register(grpcServer)

	// Start listening
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %s: %w", port, err)
	}

	log.Printf("ManManV2 API server listening on :%s", port)

	// Handle graceful shutdown
	done := make(chan error, 1)
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh

		log.Println("Shutting down gracefully...")
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

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
