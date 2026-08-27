package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

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

	// Subscribe to config-ack broadcast signals over the shared leaflab
	// broadcast exchange (leaflab/broadcast) — the same fanout exchange
	// leaflab/processor/cache.go's cache-invalidation listener uses
	// (FR73/#1203) — via a private, ephemeral queue exclusive to this
	// replica. Every API replica does this independently, so every replica
	// receives every ack signal (NFR15); this is not a competing-consumer
	// work queue.
	ackListener, err := NewAckListener(logging.Get("ack-listener"), rmqConn, repo, ackWaiter)
	if err != nil {
		return fmt.Errorf("failed to create ack listener: %w", err)
	}
	defer ackListener.Close() //nolint:errcheck

	listenerCtx, listenerCancel := context.WithCancel(context.Background())
	defer listenerCancel()

	go func() {
		if err := ackListener.Start(listenerCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("ack listener error", "err", err)
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
