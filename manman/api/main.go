package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/whale-net/everything/manman/api/handlers"
	"github.com/whale-net/everything/manman/api/repository/postgres"
	pb "github.com/whale-net/everything/manman/protos"
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

	// Create gRPC server
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(10 * 1024 * 1024), // 10 MB
		grpc.MaxSendMsgSize(10 * 1024 * 1024), // 10 MB
	)

	// Register API server
	apiServer := handlers.NewAPIServer(repo)
	pb.RegisterManManAPIServer(grpcServer, apiServer)

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
