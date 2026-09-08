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
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	temporallib "github.com/whale-net/everything/libs/go/temporal"
	"github.com/whale-net/everything/whagent_net/api/handlers"
	"github.com/whale-net/everything/whagent_net/api/persona"
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

	// api is whagent-net's trust root (LB3/FR10/NFR4, issue #2115): it
	// owns the signing key(s) persona.LoadKeySet loads here, fails
	// startup loudly when none is configured -- never falling back to an
	// unsigned or symmetric mode -- and serves the resulting KeySet's
	// public half over persona.NewMux (below), alongside this process's
	// gRPC surface.
	keySet, err := persona.LoadKeySet(persona.KeySetEnvConfig{
		Issuer:              getEnv("WHAGENT_ISSUER", "whagent-net"),
		ActiveKeyID:         os.Getenv("WHAGENT_SIGNING_KEY_ID"),
		ActivePrivateKeyPEM: os.Getenv("WHAGENT_SIGNING_KEY"),
		AdditionalKeysJSON:  os.Getenv("WHAGENT_SIGNING_KEYS_ADDITIONAL"),
	})
	if err != nil {
		return fmt.Errorf("persona: %w", err)
	}
	// This process never constructs a persona.Issuer: minting happens in
	// `worker`'s own process, not `api`'s (issue #2115's Implementation
	// phase decision -- see whagent_net/ARCHITECTURE.md "Identity and auth
	// chaining" § "Issuance mechanism"). `worker` builds its own Issuer
	// from the identical WHAGENT_SIGNING_KEY/WHAGENT_SIGNING_KEY_ID/
	// WHAGENT_ISSUER configuration read here, in-process, immediately
	// before each tool call -- no RPC hop, and nothing to register on this
	// (or any) gRPC/HTTP surface. `api`'s job is solely key ownership
	// (above) and publishing the public JWKS (below) so a domain server's
	// whagent.Verifier can check what `worker` mints.

	jwksAddr := getEnv("WHAGENT_JWKS_ADDR", ":8090")
	jwksServer := &http.Server{
		Addr:    jwksAddr,
		Handler: persona.NewMux(keySet),
	}

	// pub is nil: this task's SessionService scope is read-only RPCs
	// (GetSession/ReadTranscript) that never publish events. The write
	// RPCs that will need a real events.PublisherInterface land with the
	// SessionWorkflow (#2117).
	store := session.New(pool, nil)

	// Temporal client (issue #2117's Scaffold): StartSession/SendTurn/
	// StopSession (handlers/start.go, send.go, stop.go) start and signal
	// each session's SessionWorkflow (#2114) through this same client --
	// api never hosts a worker itself, it only ever dials the frontend.
	// Dial connects eagerly (temporallib.NewClient's doc comment), so a
	// non-nil error here means Temporal was unreachable at startup, same
	// as the database connection above.
	temporalCfg := temporallib.ConfigFromEnv()
	logger.Info("connecting to temporal", "host_port", temporalCfg.HostPort, "namespace", temporalCfg.Namespace)
	temporalClient, err := temporallib.NewClient(temporalCfg, temporallib.NewLogger("whagent-net-api"))
	if err != nil {
		return fmt.Errorf("connect to temporal: %w", err)
	}
	defer temporalClient.Close()
	logger.Info("temporal connected")

	sessionServer := handlers.NewSessionServer(store, grpcOIDCIssuer, temporalClient, temporalCfg.TaskQueue)

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
	logger.Info("whagent-net jwks listening", "addr", jwksAddr, "path", persona.JWKSPath)

	done := make(chan error, 1)
	var once sync.Once
	sendDone := func(err error) { once.Do(func() { done <- err }) }

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		logger.Info("shutting down")
		grpcServer.GracefulStop()
		if err := jwksServer.Shutdown(context.Background()); err != nil {
			logger.Warn("jwks server shutdown", "error", err)
		}
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
	go func() {
		if err := jwksServer.ListenAndServe(); err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			sendDone(fmt.Errorf("jwks serve: %w", err))
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
