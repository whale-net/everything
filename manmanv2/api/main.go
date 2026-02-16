package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	rmqlib "github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manmanv2/api/handlers"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
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

	// Get configuration from environment
	port := getEnv("PORT", "50051")
	dbHost := getEnv("DB_HOST", "localhost")
	dbPort := getEnv("DB_PORT", "5432")
	dbUser := getEnv("DB_USER", "postgres")
	dbPassword := getEnv("DB_PASSWORD", "")
	dbName := getEnv("DB_NAME", "manman")
	dbSSLMode := getEnv("DB_SSL_MODE", "disable")
	rabbitmqURL := getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	s3Bucket := getEnv("S3_BUCKET", "manman-logs")
	s3Region := getEnv("S3_REGION", "us-east-1")
	s3Endpoint := getEnv("S3_ENDPOINT", "")     // Optional: for S3-compatible storage (OVH, MinIO, etc.)
	s3AccessKey := getEnv("S3_ACCESS_KEY", "")   // Optional: for static credentials (MinIO, etc.)
	s3SecretKey := getEnv("S3_SECRET_KEY", "")   // Optional: for static credentials (MinIO, etc.)

	// Build connection string
	connString := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		dbHost, dbPort, dbUser, dbPassword, dbName, dbSSLMode,
	)

	// Initialize repository
	log.Println("Connecting to database...")
	repo, err := postgres.NewRepository(ctx, connString)
	if err != nil {
		return fmt.Errorf("failed to initialize repository: %w", err)
	}
	log.Println("Database connection established")

	// Initialize S3 client
	log.Println("Initializing S3 client...")
	s3Client, err := s3.NewClient(ctx, s3.Config{
		Bucket:    s3Bucket,
		Region:    s3Region,
		Endpoint:  s3Endpoint,
		AccessKey: s3AccessKey,
		SecretKey: s3SecretKey,
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

	// Create gRPC server
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(10 * 1024 * 1024), // 10 MB
		grpc.MaxSendMsgSize(10 * 1024 * 1024), // 10 MB
	)

	// Register API server
	apiServer := handlers.NewAPIServer(repo, s3Client, rmqConn)
	pb.RegisterManManAPIServer(grpcServer, apiServer)

	// Initialize workshop status handler for installation status updates
	log.Println("Setting up workshop status handler...")
	workshopStatusHandler, err := handlers.NewWorkshopStatusHandler(repo.WorkshopInstallations, rmqConn)
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
