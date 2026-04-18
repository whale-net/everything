package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
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
	log.Println("starting log-processor service")

	ctx := context.Background()

	logging.Configure(logging.Config{
		ServiceName:   "log-processor",
		Domain:        "manmanv2",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	defer logging.Shutdown(ctx) //nolint:errcheck

	// Load configuration
	config := LoadConfig()
	slog.Info("configuration loaded",
		"rabbitmq", config.RabbitMQURL,
		"grpc_port", config.GRPCPort,
		"buffer_ttl_s", config.LogBufferTTL,
		"buffer_max_msgs", config.LogBufferMaxMsgs,
	)

	// Initialize S3 client (if configured)
	var logArchiver *archiver.Archiver
	if config.S3Bucket != "" && config.DatabaseURL != "" {
		slog.Info("initializing S3 client for log archival")
		s3Client, err := s3.NewClient(ctx, s3.Config{
			Bucket:    config.S3Bucket,
			Region:    config.S3Region,
			Endpoint:  config.S3Endpoint,
			AccessKey: config.S3AccessKey,
			SecretKey: config.S3SecretKey,
		})
		if err != nil {
			slog.Error("failed to create S3 client", "error", err)
			os.Exit(1)
		}
		slog.Info("S3 client initialized", "bucket", config.S3Bucket, "region", config.S3Region)

		// Connect to database (URL already loaded from PG_DATABASE_URL into config)
		slog.Info("connecting to database")
		dbPool, err := db.NewPool(ctx, config.DatabaseURL)
		if err != nil {
			slog.Error("failed to connect to database", "error", err)
			os.Exit(1)
		}
		defer dbPool.Close()
		slog.Info("connected to database")

		// Create log reference repository
		logRepo := postgres.NewLogReferenceRepository(dbPool)

		// Create archiver
		logArchiver = archiver.NewArchiver(s3Client, logRepo)
		slog.Info("log archiver initialized")
	} else {
		slog.Info("S3 archival not configured (missing S3_BUCKET or DATABASE_URL)")
	}

	// Build service account dial option for outgoing API calls
	authOpt, err := grpcauth.NewServiceAccountDialOption(grpcauth.ClientConfig{
		Mode:                     grpcauth.AuthMode(config.GRPCAuthMode),
		TokenURL:                 config.GRPCAuthTokenURL,
		ClientID:                 config.GRPCAuthClientID,
		ClientSecret:             config.GRPCAuthClientSecret,
		RequireTransportSecurity: false, // internal cluster
	})
	if err != nil {
		slog.Error("failed to create auth dial option", "error", err)
		os.Exit(1)
	}

	// Connect to API server for session queries
	slog.Info("connecting to API server", "address", config.APIAddress)
	apiConn, err := grpc.NewClient(config.APIAddress, grpc.WithTransportCredentials(insecure.NewCredentials()), authOpt)
	if err != nil {
		slog.Error("failed to connect to API server", "error", err)
		os.Exit(1)
	}
	defer apiConn.Close()
	apiClient := manmanpb.NewManManAPIClient(apiConn)
	slog.Info("connected to API server")

	// Connect to RabbitMQ
	slog.Info("connecting to RabbitMQ")
	rmqConn, err := rmq.NewConnectionFromURL(config.RabbitMQURL)
	if err != nil {
		slog.Error("failed to connect to RabbitMQ", "error", err)
		os.Exit(1)
	}
	defer rmqConn.Close()
	slog.Info("connected to RabbitMQ")

	// Create consumer manager
	consumerConfig := &consumer.ConsumerConfig{
		LogBufferTTL:     config.LogBufferTTL,
		LogBufferMaxMsgs: config.LogBufferMaxMsgs,
		DebugLogOutput:   config.DebugLogOutput,
	}
	consumerManager := consumer.NewManager(rmqConn, consumerConfig, apiClient, logArchiver)
	defer consumerManager.Close()

	// Create lifecycle handler for session events
	slog.Info("initializing session lifecycle handler")
	lifecycleHandler, err := lifecycle.NewHandler(rmqConn, consumerManager)
	if err != nil {
		slog.Error("failed to create lifecycle handler", "error", err)
		os.Exit(1)
	}

	// Start lifecycle handler
	lifecycleCtx, lifecycleCancel := context.WithCancel(ctx)
	defer lifecycleCancel()

	if err := lifecycleHandler.Start(lifecycleCtx); err != nil {
		slog.Error("failed to start lifecycle handler", "error", err)
		os.Exit(1)
	}
	defer lifecycleHandler.Close()
	slog.Info("session lifecycle handler started")

	// Create auth interceptors for incoming requests
	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(ctx, grpcauth.ServerConfig{
		Mode:      grpcauth.AuthMode(config.GRPCAuthMode),
		IssuerURL: config.GRPCOIDCIssuer,
		ClientID:  config.GRPCOIDCClientID,
	})
	if err != nil {
		slog.Error("failed to create auth interceptors", "error", err)
		os.Exit(1)
	}

	// Create gRPC server
	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(unaryInt),
		grpc.ChainStreamInterceptor(streamInt),
	)
	logProcessorServer := server.NewServer(consumerManager)
	manmanpb.RegisterLogProcessorServer(grpcServer, logProcessorServer)

	// Start gRPC server
	addr := fmt.Sprintf("0.0.0.0:%s", config.GRPCPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("failed to listen", "address", addr, "error", err)
		os.Exit(1)
	}

	slog.Info("gRPC server listening", "address", addr)

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
		slog.Info("received shutdown signal, stopping server")
	case err := <-serverErrChan:
		slog.Error("failed to serve", "error", err)
		os.Exit(1)
	}

	// Graceful shutdown
	slog.Info("shutting down")

	// 1. Stop accepting new lifecycle events
	lifecycleCancel()
	lifecycleHandler.Close()

	// 2. Flush pending logs to S3
	if logArchiver != nil {
		slog.Info("flushing log archiver")
		if err := logArchiver.Close(); err != nil {
			slog.Error("error closing archiver", "error", err)
		}
	}

	// 3. Stop gRPC server (browser connections)
	slog.Info("stopping gRPC server")
	grpcServer.GracefulStop()

	// 4. Close consumer manager (closes all consumers)
	consumerManager.Close()

	slog.Info("log-processor service stopped")
}
