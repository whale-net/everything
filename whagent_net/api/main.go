// Command api is whagent-net's SessionService gRPC server -- the
// `external-api` service boundary described in
// whagent_net/ARCHITECTURE.md "Service boundary vs. package boundary"
// (issue #2113). It shares the `whagent_net/session` store package
// directly with `worker` (no network hop between them); `mcp` and any
// other gRPC client talk to this binary instead.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/api/handlers"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	if err := run(); err != nil {
		logging.Get("main").Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logging.Configure(logging.Config{
		ServiceName:   "whagent-net-api",
		Domain:        "whagent-net",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	port := getEnv("PORT", "50051")
	databaseURL := getEnv("PG_DATABASE_URL", "")
	grpcAuthMode := getEnv("GRPC_AUTH_MODE", "none")
	grpcOIDCIssuer := getEnv("WHAGENT_OIDC_ISSUER", "")
	grpcOIDCAudience := getEnv("WHAGENT_OIDC_AUDIENCE", "")
	if grpcAuthMode == "none" {
		logger.Warn("GRPC_AUTH_MODE=none — whagent-net api is running without authentication (development only)")
	}

	pool, err := db.NewPool(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	logger.Info("database connected")

	// pub is nil: this task's SessionService scope is read-only RPCs
	// (GetSession/ReadTranscript) that never publish events. The write
	// RPCs that will need a real events.PublisherInterface land with the
	// SessionWorkflow (#2117).
	store := session.New(pool, nil)
	sessionServer := handlers.NewSessionServer(store, grpcOIDCIssuer)

	// Every SessionService RPC authenticates (ARCHITECTURE.md "Identity and
	// auth chaining"; whagent_net/api/auth.go's requireClaims) -- unlike
	// leaflab/manmanv2 there is no unauthenticated-method allowlist to wire
	// here.
	unaryAuth, streamAuth, err := grpcauth.NewServerInterceptors(ctx, grpcauth.ServerConfig{
		Mode:      grpcauth.AuthMode(grpcAuthMode),
		IssuerURL: grpcOIDCIssuer,
		ClientID:  grpcOIDCAudience,
	})
	if err != nil {
		return fmt.Errorf("grpcauth: %w", err)
	}

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(logging.NewUnaryServerLoggingInterceptor("grpc"), unaryAuth, handlers.RequireClaimsUnaryInterceptor),
		grpc.ChainStreamInterceptor(logging.NewStreamServerLoggingInterceptor("grpc"), streamAuth, handlers.RequireClaimsStreamInterceptor),
	)
	pb.RegisterSessionServiceServer(grpcServer, sessionServer)
	reflection.Register(grpcServer)

	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("listen :%s: %w", port, err)
	}
	logger.Info("whagent-net api listening", "port", port)

	done := make(chan error, 1)
	var once sync.Once
	sendDone := func(err error) { once.Do(func() { done <- err }) }

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		logger.Info("shutting down")
		grpcServer.GracefulStop()
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

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
