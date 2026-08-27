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

	// FR11: auth mode and dev mode are explicit configuration, never
	// inferred from ENVIRONMENT, hostname, or the presence/absence of an
	// issuer URL -- a bench box running this stack in "oidc" mode is a real
	// deployment, not a dev environment, no matter where it physically sits.
	authMode := getEnv("LEAFLAB_API_AUTH_MODE", string(grpcauth.AuthModeNone))
	devMode, err := strconv.ParseBool(getEnv("LEAFLAB_API_DEV_MODE", "false"))
	if err != nil {
		return fmt.Errorf("LEAFLAB_API_DEV_MODE: %w", err)
	}
	oidcIssuer := getEnv("LEAFLAB_API_OIDC_ISSUER", "")
	oidcClientID := getEnv("LEAFLAB_API_OIDC_CLIENT_ID", "")

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
	apiServer := NewLeafLabAPIServer(repo, publisher, logging.Get("api"))

	// FR11: every RPC goes through grpcauth. AuthModeNone injects fake dev
	// Claims and is intended for local development only -- see
	// LEAFLAB_API_DEV_MODE above and the Implementation phase's boot-time
	// refusal to serve AuthModeNone outside dev mode.
	authUnary, authStream, err := grpcauth.NewServerInterceptors(ctx, grpcauth.ServerConfig{
		Mode:      grpcauth.AuthMode(authMode),
		IssuerURL: oidcIssuer,
		ClientID:  oidcClientID,
	})
	if err != nil {
		return fmt.Errorf("grpcauth: %w", err)
	}

	rpcLogger := logging.Get("rpc")

	// Chain order (NFR12): correlation-id -> auth -> acting-subject logging
	// -> handler. Correlation-id runs first so even an auth rejection is
	// logged against the same id; subject-logging runs after auth so Claims
	// are already in context.
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			NewCorrelationUnaryInterceptor(),
			authUnary,
			NewSubjectLoggingUnaryInterceptor(rpcLogger),
		),
		grpc.ChainStreamInterceptor(
			NewCorrelationStreamInterceptor(),
			authStream,
			NewSubjectLoggingStreamInterceptor(rpcLogger),
		),
	)
	pb.RegisterLeafLabAPIServer(grpcServer, apiServer)

	// Server reflection is a discovery/debugging aid; disabled outside
	// explicit dev mode so a deployed environment never exposes it (FR11).
	if devMode {
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
