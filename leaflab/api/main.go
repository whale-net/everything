package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// AckSignalMessage represents the JSON structure of config ack signals from the processor.
type AckSignalMessage struct {
	AckedAt         time.Time `json:"acked_at"`
	DeviceID        string    `json:"device_id"`
	ConfigVersion   int64     `json:"config_version"`
	Accepted        bool      `json:"accepted"`
	RejectionReason string    `json:"rejection_reason,omitempty"`
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logging.Configure(logging.Config{
		ServiceName: "leaflab-api",
		Domain:      "leaflab",
		JSONFormat:  true,
		EnableOTLP:  true,
	})
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	port := getEnv("PORT", "50051")
	rabbitmqURL := getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	databaseURL := getEnv("PG_DATABASE_URL", "")
	grpcAuthMode := getEnv("LEAFLAB_API_AUTH_MODE", "none")
	grpcOIDCIssuer := getEnv("LEAFLAB_API_OIDC_ISSUER", "")
	grpcOIDCClientID := getEnv("LEAFLAB_API_OIDC_CLIENT_ID", "")
	reflectionEnabled := getEnv("LEAFLAB_API_REFLECTION_ENABLED", "false")

	pool, err := db.NewPool(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	logger.Info("database connected")

	rmqConn, err := rmq.NewConnectionFromURL(rabbitmqURL)
	if err != nil {
		return fmt.Errorf("rabbitmq: %w", err)
	}
	defer rmqConn.Close()
	logger.Info("rabbitmq connected")

	publisher, err := rmq.NewPublisher(rmqConn)
	if err != nil {
		return fmt.Errorf("publisher: %w", err)
	}
	defer publisher.Close() //nolint:errcheck

	repo := NewRepository(pool)

	// Create rate limiter with registry and configure default buckets
	registry := ratelimit.NewRegistry()
	configureDefaultBuckets(registry)
	limiter := ratelimit.NewLimiter(registry)

	// Create config ack waiter for bounded-wait RPC
	ackWaiter := NewConfigAckWaiter()

	// Create consumer for ack signals (using a unique queue name)
	ackQueueName := "leaflab-api-ack-signals"
	ackConsumer, err := rmq.NewConsumer(rmqConn, ackQueueName)
	if err != nil {
		return fmt.Errorf("failed to create consumer for ack signals: %w", err)
	}
	defer ackConsumer.Close() //nolint:errcheck

	// Bind to the ack signal exchange (fanout for all replicas)
	if err := ackConsumer.BindExchange("amq.topic", []string{"leaflab.config-ack"}); err != nil {
		return fmt.Errorf("failed to bind ack exchange: %w", err)
	}

	// Create a message handler that bridges ack signals to waiting clients
	ackMsgHandler := func(msgCtx context.Context, msg rmq.Message) error {
		var signal AckSignalMessage
		if err := json.Unmarshal(msg.Body, &signal); err != nil {
			logger.Warn("failed to unmarshal ack signal",
				"routing_key", msg.RoutingKey,
				"err", err)
			return nil // Non-fatal, don't requeue
		}

		// Look up the board ID from the device ID
		boardID, err := repo.GetOrCreateBoard(msgCtx, signal.DeviceID)
		if err != nil {
			logger.Warn("failed to look up board for ack notification",
				"device_id", signal.DeviceID,
				"err", err)
			return nil
		}

		// Notify all waiters for this (board_id, version)
		ackWaiter.NotifyAck(boardID, signal.ConfigVersion, signal.Accepted, signal.RejectionReason, signal.AckedAt)
		return nil
	}

	ackConsumer.RegisterHandler("leaflab.config-ack", ackMsgHandler)

	// Start the ack consumer in a background goroutine
	listenerCtx, listenerCancel := context.WithCancel(context.Background())
	defer listenerCancel()

	go func() {
		if err := ackConsumer.Start(listenerCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("ack consumer error", "err", err)
		}
	}()

	// Create the API server
	apiServer := NewLeafLabAPIServer(repo, publisher, logging.Get("api"), ackWaiter, limiter)

	// Create auth interceptors
	unaryInt, streamInt, err := grpcauth.NewServerInterceptors(ctx, grpcauth.ServerConfig{
		Mode:      grpcauth.AuthMode(grpcAuthMode),
		IssuerURL: grpcOIDCIssuer,
		ClientID:  grpcOIDCClientID,
	})
	if err != nil {
		return fmt.Errorf("failed to create auth interceptors: %w", err)
	}

	// Create correlation ID interceptors
	correlationUnaryInt := logging.NewCorrelationIDUnaryInterceptor()
	correlationStreamInt := logging.NewCorrelationIDStreamInterceptor()

	// Create rate limiting interceptors for read operations
	rateLimitUnaryInt := ratelimit.UnaryServerInterceptor(limiter, "read")
	rateLimitStreamInt := ratelimit.StreamServerInterceptor(limiter, "read")

	// Create gRPC server with interceptors
	// Chain: correlation ID first (to attach to context), then auth (to verify credentials),
	// then rate limiting (to enforce limits)
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(correlationUnaryInt, unaryInt, rateLimitUnaryInt),
		grpc.ChainStreamInterceptor(correlationStreamInt, streamInt, rateLimitStreamInt),
	)

	pb.RegisterLeafLabAPIServer(grpcServer, apiServer)

	// Register reflection only if explicitly enabled
	if reflectionEnabled == "true" {
		reflection.Register(grpcServer)
	}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("listen :%s: %w", port, err)
	}
	logger.Info("leaflab-api listening", "port", port)

	done := make(chan error, 1)
	var once sync.Once
	sendDone := func(err error) { once.Do(func() { done <- err }) }

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		logger.Info("shutting down")
		grpcServer.GracefulStop()
		listenerCancel()
		sendDone(nil)
	}()
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			if errors.Is(err, grpc.ErrServerStopped) {
				sendDone(nil)
				return
			}
			sendDone(fmt.Errorf("grpc serve: %w", err))
		}
	}()

	return <-done
}

// configureDefaultBuckets registers the default rate limit buckets.
// Configuration can be overridden via environment variables.
func configureDefaultBuckets(registry *ratelimit.Registry) {
	// Read environment variables for bucket configuration
	readRateLimit := getEnvInt("LEAFLAB_RATELIMIT_READ_RPS", 1000)
	claimInitiateRateLimit := getEnvInt("LEAFLAB_RATELIMIT_CLAIM_INITIATE_RPS", 100)
	challengeRateLimit := getEnvInt("LEAFLAB_RATELIMIT_CHALLENGE_RPS", 100)
	supportReferenceRateLimit := getEnvInt("LEAFLAB_RATELIMIT_SUPPORT_REFERENCE_RPS", 50)
	resendRateLimit := getEnvInt("LEAFLAB_RATELIMIT_RESEND_RPS", 100)
	concurrentWaitRateLimit := getEnvInt("LEAFLAB_RATELIMIT_CONCURRENT_WAIT_RPS", 100)

	// Register named buckets for each operation type
	registry.Register(ratelimit.Bucket{
		Name:              "read",
		RequestsPerSecond: readRateLimit,
		Description:       "Rate limit for all read operations",
	})

	registry.Register(ratelimit.Bucket{
		Name:              "claim-initiate",
		RequestsPerSecond: claimInitiateRateLimit,
		Description:       "Rate limit for claim initiation (FR76)",
	})

	registry.Register(ratelimit.Bucket{
		Name:              "challenge",
		RequestsPerSecond: challengeRateLimit,
		Description:       "Rate limit for challenge operations (FR76)",
	})

	registry.Register(ratelimit.Bucket{
		Name:              "support-reference",
		RequestsPerSecond: supportReferenceRateLimit,
		Description:       "Rate limit for support reference resolution (FR80)",
	})

	registry.Register(ratelimit.Bucket{
		Name:              "resend",
		RequestsPerSecond: resendRateLimit,
		Description:       "Rate limit for resend operations (FR42)",
	})

	registry.Register(ratelimit.Bucket{
		Name:              "concurrent-wait",
		RequestsPerSecond: concurrentWaitRateLimit,
		Description:       "Rate limit for concurrent open bounded waits (FR47)",
	})
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getEnvInt reads an integer environment variable with a default fallback.
func getEnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}
