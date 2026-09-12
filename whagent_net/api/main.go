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
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/libs/go/s3"
	temporallib "github.com/whale-net/everything/libs/go/temporal"
	"github.com/whale-net/everything/whagent_net/api/handlers"
	"github.com/whale-net/everything/whagent_net/api/persona"
	"github.com/whale-net/everything/whagent_net/config"
	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
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

	// s3Client backs tier-transparent transcript reads (FR8, issue #2240):
	// api is the only ReadTranscript-serving process, so it is the only
	// caller that ever needs to hydrate an archived session -- see
	// session.WithS3's doc comment. Construction is non-fatal, same
	// pattern as initializeEventsConsumer below: WHAGENT_S3_BUCKET unset
	// (the default) or the client failing to construct both leave
	// s3Client nil, which session.New(..., session.WithS3(nil)) treats
	// identically to omitting the option -- ReadTranscript still works for
	// every session that hasn't been archived yet, it just can't hydrate
	// the ones that have.
	s3Client := initializeS3Client(ctx, logger)

	// pub is nil: none of api's own writes go through Transcript().Append
	// (NFR2's publish-on-commit path) -- StartSession only writes
	// `sessions`/`session_agent` rows directly, never a transcript event.
	// Transcript events are appended (and published) exclusively by
	// `worker`'s CommitTurn activity, which constructs its own store with a
	// real events.PublisherInterface (worker/main.go).
	store := session.New(pool, nil, session.WithS3(s3Client))

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

	// Model catalogue (FR5, issue #2117): StartSession checks a requested
	// model_override against this before any session row is written --
	// same OpenRouter client/catalogue shape `worker` builds for CallModel
	// (worker/main.go), constructed separately here because `api` and
	// `worker` are different processes/binaries sharing no in-memory state.
	catalogTTL := 5 * time.Minute
	if raw := os.Getenv("WHAGENT_MODEL_CATALOG_TTL"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("parse WHAGENT_MODEL_CATALOG_TTL %q: %w", raw, err)
		}
		catalogTTL = parsed
	}
	llmClient := llm.NewClient(os.Getenv("OPENROUTER_API_KEY"), getEnv("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"))
	catalog := llm.NewCatalog(llmClient, catalogTTL)

	// eventsConsumer is StreamEvents' (FR5/C17, issue #2239) per-process
	// subscription onto the whagent/events exchange -- see
	// initializeEventsConsumer's doc comment.
	eventsConsumer := initializeEventsConsumer(logger)
	if eventsConsumer != nil {
		defer eventsConsumer.Close() //nolint:errcheck
	}

	sessionServer := handlers.NewSessionServer(ctx, store, grpcOIDCIssuer, temporalClient, temporalCfg.TaskQueue, catalog, eventsConsumer)

	// agentDefs feeds DevRoles below: DevRoles matters only in
	// AuthModeNone, where it makes the injected dev Claims carry every
	// configured agent's required_role instead of grpcauth's generic
	// default of ["admin"] (which matches none of them) -- see FR9/issue
	// #2154 and tools/app_registry/server/main.go's identical precedent.
	// This load failing fails api's startup loudly rather than silently
	// falling back to no DevRoles.
	_, agentDefs, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// Every SessionService RPC authenticates (ARCHITECTURE.md "Identity and
	// auth chaining"; whagent_net/api/auth.go's requireClaims) -- unlike
	// leaflab/manmanv2 there is no unauthenticated-method allowlist to wire
	// here.
	unaryAuth, streamAuth, err := grpcauth.NewServerInterceptors(ctx, grpcauth.ServerConfig{
		Mode:      grpcauth.AuthMode(grpcAuthMode),
		IssuerURL: grpcOIDCIssuer,
		ClientID:  grpcOIDCAudience,
		DevRoles:  config.RequiredRoles(agentDefs),
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

// initializeEventsConsumer connects to RabbitMQ and declares/binds a
// single per-process ephemeral queue on the whagent/events exchange (FR5/
// C17, issue #2239's Scaffold), mirroring
// tools/app_registry/ui/main.go's initializeSSEHub attach shape: declare
// the exchange, then create a non-durable, auto-delete, server-named
// queue via rmq.NewConsumerWithOpts(conn, "", false, true, 0, 0) and bind
// it to every routing key ("#") the same way htmxsse.Hub.attach does
// (libs/go/htmxsse/hub.go) -- one shared subscription, not one per
// stream, so no StreamEvents call ever needs its own broker connection or
// credentials (C17's whole point). handlers.NewSessionServer registers a
// fresh eventBroadcaster (broadcast.go) as the returned consumer's sole
// message handler and starts consuming; handlers.SessionServer.StreamEvents
// (stream.go) then only ever talks to that in-process broadcaster, never
// this consumer directly, the same way htmxsse.Hub's clients each filter
// the shared feed to their own topic rather than touching the transport.
//
// Construction is non-fatal, matching worker/main.go's
// initializePublisher: RABBITMQ_URL unset or the broker unreachable at
// startup returns nil, and api still starts (NFR7) -- StreamEvents must
// report UNAVAILABLE for a nil consumer rather than block.
func initializeEventsConsumer(logger *slog.Logger) *rmq.Consumer {
	brokerURL := getEnv("RABBITMQ_URL", "")
	if brokerURL == "" {
		logger.Info("RABBITMQ_URL not set; StreamEvents will be unavailable")
		return nil
	}

	conn, err := rmq.NewConnectionFromURL(brokerURL)
	if err != nil {
		logger.Warn("failed to connect to RabbitMQ for StreamEvents; StreamEvents will be unavailable", "error", err)
		return nil
	}

	ch, err := conn.Channel()
	if err != nil {
		logger.Warn("failed to open channel to declare events exchange; StreamEvents will be unavailable", "error", err)
		conn.Close() //nolint:errcheck
		return nil
	}
	kind, durable, autoDelete, internal, noWait, args := events.DeclareArgs()
	err = ch.ExchangeDeclare(events.ExchangeName, kind, durable, autoDelete, internal, noWait, args)
	ch.Close() //nolint:errcheck
	if err != nil {
		logger.Warn("failed to declare events exchange; StreamEvents will be unavailable", "error", err, "exchange", events.ExchangeName)
		conn.Close() //nolint:errcheck
		return nil
	}

	consumer, err := rmq.NewConsumerWithOpts(conn, "", false, true, 0, 0)
	if err != nil {
		logger.Warn("failed to create ephemeral queue for StreamEvents; StreamEvents will be unavailable", "error", err)
		conn.Close() //nolint:errcheck
		return nil
	}
	if err := consumer.BindExchange(events.ExchangeName, []string{"#"}); err != nil {
		logger.Warn("failed to bind ephemeral queue to events exchange; StreamEvents will be unavailable", "error", err)
		consumer.Close() //nolint:errcheck
		return nil
	}

	logger.Info("bound ephemeral queue to events exchange for StreamEvents", "exchange", events.ExchangeName)
	return consumer
}

// initializeS3Client builds the S3 client session.WithS3 attaches to the
// Store for tier-transparent transcript hydration (FR8, issue #2240).
// Construction is non-fatal, matching initializeEventsConsumer above and
// worker/main.go's initializePublisher: WHAGENT_S3_BUCKET unset returns
// nil (no cold tier configured -- every session reads hot-only, which is
// correct as long as nothing has been archived yet, and stays a graceful
// degradation even after archiving starts, see session.WithS3's doc
// comment), and a construction failure (bad credentials/endpoint) logs a
// warning and also returns nil rather than failing api's startup.
//
// S3_ENDPOINT/S3_REGION/S3_ACCESS_KEY/S3_SECRET_KEY are the same
// unprefixed variable names manmanv2/api and tools/app_registry use for
// their own s3.Client construction (see ENV.md "S3 (cold tier)") --
// WHAGENT_S3_BUCKET is the only whagent-net-specific one, since it is the
// one setting that must differ from any other domain sharing the same S3
// account/endpoint.
func initializeS3Client(ctx context.Context, logger *slog.Logger) *s3.Client {
	bucket := getEnv("WHAGENT_S3_BUCKET", "")
	if bucket == "" {
		logger.Info("WHAGENT_S3_BUCKET not set; archived transcripts will be unavailable (hot-tier reads still work)")
		return nil
	}

	client, err := s3.NewClient(ctx, s3.Config{
		Bucket:    bucket,
		Region:    getEnv("S3_REGION", "us-east-1"),
		Endpoint:  getEnv("S3_ENDPOINT", ""),
		AccessKey: getEnv("S3_ACCESS_KEY", ""),
		SecretKey: getEnv("S3_SECRET_KEY", ""),
	})
	if err != nil {
		logger.Warn("failed to initialize S3 client; archived transcripts will be unavailable (hot-tier reads still work)", "error", err)
		return nil
	}
	logger.Info("S3 client initialized for transcript archive hydration", "bucket", bucket)
	return client
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
