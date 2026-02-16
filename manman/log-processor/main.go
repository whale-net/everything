package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	"github.com/whale-net/everything/manmanv2/log-processor/archiver"
	"github.com/whale-net/everything/manmanv2/log-processor/consumer"
	"github.com/whale-net/everything/manmanv2/log-processor/lifecycle"
	"github.com/whale-net/everything/manmanv2/log-processor/server"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

func main() {
	log.Println("Starting log-processor service...")

	// Load configuration
	config := LoadConfig()
	log.Printf("Configuration: RabbitMQ=%s, gRPC Port=%s, Buffer TTL=%ds, Max Messages=%d",
		config.RabbitMQURL, config.GRPCPort, config.LogBufferTTL, config.LogBufferMaxMsgs)

	ctx := context.Background()

	// Initialize S3 client (if configured)
	var logArchiver *archiver.Archiver
	if config.S3Bucket != "" && config.DatabaseURL != "" {
		log.Println("Initializing S3 client for log archival...")
		s3Client, err := s3.NewClient(ctx, s3.Config{
			Bucket:    config.S3Bucket,
			Region:    config.S3Region,
			Endpoint:  config.S3Endpoint,
			AccessKey: config.S3AccessKey,
			SecretKey: config.S3SecretKey,
		})
		if err != nil {
			log.Fatalf("Failed to create S3 client: %v", err)
		}
		log.Printf("S3 client initialized: bucket=%s, region=%s", config.S3Bucket, config.S3Region)

		// Connect to database
		log.Println("Connecting to database...")
		dbPool, err := pgxpool.New(ctx, config.DatabaseURL)
		if err != nil {
			log.Fatalf("Failed to connect to database: %v", err)
		}
		defer dbPool.Close()

		// Verify database connection
		if err := dbPool.Ping(ctx); err != nil {
			log.Fatalf("Failed to ping database: %v", err)
		}
		log.Println("Connected to database")

		// Create log reference repository
		logRepo := postgres.NewLogReferenceRepository(dbPool)

		// Create archiver
		logArchiver = archiver.NewArchiver(s3Client, logRepo)
		log.Println("Log archiver initialized")
	} else {
		log.Println("S3 archival not configured (missing S3_BUCKET or DATABASE_URL)")
	}

	// Connect to API server for session queries
	log.Printf("Connecting to API server at %s...", config.APIAddress)
	apiConn, err := grpc.NewClient(config.APIAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect to API server: %v", err)
	}
	defer apiConn.Close()
	apiClient := manmanpb.NewManManAPIClient(apiConn)
	log.Println("Connected to API server")

	// Connect to RabbitMQ
	log.Println("Connecting to RabbitMQ...")
	rmqConn, err := rmq.NewConnectionFromURL(config.RabbitMQURL)
	if err != nil {
		log.Fatalf("Failed to connect to RabbitMQ: %v", err)
	}
	defer rmqConn.Close()
	log.Println("Connected to RabbitMQ")

	// Create consumer manager
	consumerConfig := &consumer.ConsumerConfig{
		LogBufferTTL:     config.LogBufferTTL,
		LogBufferMaxMsgs: config.LogBufferMaxMsgs,
		DebugLogOutput:   config.DebugLogOutput,
	}
	consumerManager := consumer.NewManager(rmqConn, consumerConfig, apiClient, logArchiver)
	defer consumerManager.Close()

	// Create lifecycle handler for session events
	log.Println("Initializing session lifecycle handler...")
	lifecycleHandler, err := lifecycle.NewHandler(rmqConn, consumerManager)
	if err != nil {
		log.Fatalf("Failed to create lifecycle handler: %v", err)
	}

	// Start lifecycle handler
	lifecycleCtx, lifecycleCancel := context.WithCancel(ctx)
	defer lifecycleCancel()

	if err := lifecycleHandler.Start(lifecycleCtx); err != nil {
		log.Fatalf("Failed to start lifecycle handler: %v", err)
	}
	defer lifecycleHandler.Close()
	log.Println("Session lifecycle handler started")

	// Create gRPC server
	grpcServer := grpc.NewServer()
	logProcessorServer := server.NewServer(consumerManager)
	manmanpb.RegisterLogProcessorServer(grpcServer, logProcessorServer)

	// Start gRPC server
	addr := fmt.Sprintf("0.0.0.0:%s", config.GRPCPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}

	log.Printf("gRPC server listening on %s", addr)

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	serverErrChan := make(chan error, 1)
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			serverErrChan <- err
		}
	}()

	select {
	case <-sigChan:
		log.Println("Received shutdown signal, stopping server...")
	case err := <-serverErrChan:
		log.Fatalf("Failed to serve: %v", err)
	}

	// Graceful shutdown
	log.Println("Shutting down...")

	// 1. Stop accepting new lifecycle events
	lifecycleCancel()
	lifecycleHandler.Close()

	// 2. Flush pending logs to S3
	if logArchiver != nil {
		log.Println("Flushing log archiver...")
		if err := logArchiver.Close(); err != nil {
			log.Printf("Error closing archiver: %v", err)
		}
	}

	// 3. Stop gRPC server (browser connections)
	log.Println("Stopping gRPC server...")
	grpcServer.GracefulStop()

	// 4. Close consumer manager (closes all consumers)
	consumerManager.Close()

	log.Println("Log-processor service stopped")
}
