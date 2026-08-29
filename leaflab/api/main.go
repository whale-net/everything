package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/whale-net/everything/leaflab/api/ackwait"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"github.com/whale-net/everything/leaflab/api/readings"
	"github.com/whale-net/everything/leaflab/invalidation"
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

	// FR39: poll_interval_ms's stated min/max (leaflab/api/ENV.md) --
	// resolved once at boot, never inferred or left at the zero value (a
	// zero PollIntervalBounds would fail every nonzero poll_interval_ms;
	// see config.PollIntervalBounds' own doc comment).
	pollIntervalBounds, err := parsePollIntervalBounds(
		getEnv("LEAFLAB_API_POLL_INTERVAL_MS_MIN", strconv.FormatUint(uint64(DefaultPollIntervalMsMin), 10)),
		getEnv("LEAFLAB_API_POLL_INTERVAL_MS_MAX", strconv.FormatUint(uint64(DefaultPollIntervalMsMax), 10)),
	)
	if err != nil {
		return err
	}

	// FR11: AuthModeNone injects fake dev Claims with no token required --
	// refuse to boot with it outside explicit dev mode rather than silently
	// serving every RPC unauthenticated in a real deployment. Checked
	// before any dependency (DB, RabbitMQ) is dialed so a misconfigured
	// deploy fails immediately, not after standing up connections it will
	// never use.
	if err := validateAuthBootConfig(grpcauth.AuthMode(authMode), devMode); err != nil {
		return err
	}

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

	// FR10.1: "60 minutes, configurable" -- LEAFLAB_ADMIN_ELEVATION_MINUTES
	// overrides DefaultElevationDuration when set. leaflab/api has no
	// ENV.md of its own yet (see DefaultElevationDuration's doc comment in
	// server.go), so this is documented here and there instead.
	elevationDuration := DefaultElevationDuration
	if raw := getEnv("LEAFLAB_ADMIN_ELEVATION_MINUTES", ""); raw != "" {
		minutes, err := strconv.Atoi(raw)
		if err != nil || minutes <= 0 {
			return fmt.Errorf("LEAFLAB_ADMIN_ELEVATION_MINUTES: must be a positive integer, got %q", raw)
		}
		elevationDuration = time.Duration(minutes) * time.Minute
	}

	// FR73: broadcasts an invalidation event after every sensor-affecting
	// write this server commits, so leaflab/processor's SensorCache never
	// keeps serving a stale cached view. See leaflab/invalidation's doc
	// comment.
	invalidationPub, err := invalidation.NewPublisher(rmqConn)
	if err != nil {
		return fmt.Errorf("invalidation publisher: %w", err)
	}
	defer invalidationPub.Close() //nolint:errcheck

	repo := NewRepository(pool)
	// FR73: AssignSensorRegion/RenameSensor (sensor_region.go) publish
	// through this Repository directly, not via a handler-layer call like
	// RewireSensor's -- see Repository.SetInvalidationPublisher's doc
	// comment for why. Same underlying *invalidation.Publisher instance
	// LeafLabAPIServer is given below; one connection, two writers.
	repo.SetInvalidationPublisher(invalidationPub)
	authzSvc := authz.NewPGResolver(pool)
	readingsSvc := readings.NewReader(pool)

	// FR47/NFR15: this replica's in-memory registry of open AwaitConfigAck
	// waiters. One Registry per process is sufficient -- see
	// leaflab/api/ackwait's doc comment for why this satisfies NFR15's
	// every-replica broadcast constraint without a shared store.
	ackWaitRegistry := ackwait.NewRegistry()
	apiServer := NewLeafLabAPIServer(repo, authzSvc, readingsSvc, publisher, rmqConn, invalidationPub, logging.Get("api"), pollIntervalBounds, WithElevationDuration(elevationDuration)).
		WithAckWaitRegistry(ackWaitRegistry)

	// NFR15: observes every KindAck event published by leaflab/processor's
	// ack write path (handleConfigAck), on the same fanout exchange FR73
	// already uses -- not a second transport -- and resolves this replica's
	// own ackWaitRegistry waiters. Every API replica runs its own Subscriber
	// bound to the same exchange, so a bounded wait pinned to any one
	// replica resolves the same way regardless of which replica received
	// the AwaitConfigAck call. See leaflab/invalidation's doc comment.
	ackInvalidationSub, err := invalidation.NewSubscriber(rmqConn, logging.Get("ackwait"))
	if err != nil {
		return fmt.Errorf("ack invalidation subscriber: %w", err)
	}
	defer ackInvalidationSub.Close() //nolint:errcheck
	if err := ackInvalidationSub.Start(ctx, func(_ context.Context, ev invalidation.Event) {
		if ev.Kind != invalidation.KindAck {
			return
		}
		ackWaitRegistry.Notify(ev.DeviceID, ev.Version, ev.Accepted, ev.RejectionReason)
	}); err != nil {
		return fmt.Errorf("start ack invalidation subscriber: %w", err)
	}

	// FR11: every RPC goes through grpcauth. AuthModeNone injects fake dev
	// Claims and is intended for local development only -- see the
	// boot-time refusal above (LEAFLAB_API_DEV_MODE) that keeps it out of
	// real deployments. DevRoles is set to leaflab-admin (FR12) rather than
	// grpcauth's default "admin" so local/Tilt development and dev-mode CI
	// actually exercise admin-eligibility recording; see
	// libs/go/grpcauth/README.md's DevRoles note.
	authUnary, authStream, err := grpcauth.NewServerInterceptors(ctx, grpcauth.ServerConfig{
		Mode:      grpcauth.AuthMode(authMode),
		IssuerURL: oidcIssuer,
		ClientID:  oidcClientID,
		DevRoles:  []string{RoleAdmin},
	})
	if err != nil {
		return fmt.Errorf("grpcauth: %w", err)
	}

	rpcLogger := logging.Get("rpc")

	// NFR10: per-principal and per-session rate limiting, configurable per
	// environment via leaflab/api/ENV.md's LEAFLAB_API_RATELIMIT_* variables
	// (see ratelimit.EnvVarNames). Loaded before the server is built so a
	// malformed variable fails boot immediately, same as the auth config
	// validated above.
	rateLimitConfigs, err := ratelimit.LoadConfigFromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("rate limit config: %w", err)
	}
	limiter := ratelimit.NewInMemoryLimiter(rateLimitConfigs)

	grpcServer := buildServer(authUnary, authStream, rpcLogger, apiServer, devMode, limiter)

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

// validateAuthBootConfig enforces FR11.1's boot-time refusal:
// grpcauth.AuthModeNone (the unauthenticated dev bypass, which injects fake
// Claims for every request with no token required) is never reachable
// outside explicit LEAFLAB_API_DEV_MODE=true configuration -- never
// inferred from ENVIRONMENT, hostname, or issuer-URL presence/absence, per
// FR11.1's "development is determined by explicit configuration, never
// inferred from an environment." Extracted from run() as a pure function so
// this is unit-testable without dialing Postgres/RabbitMQ; see
// main_test.go.
func validateAuthBootConfig(mode grpcauth.AuthMode, devMode bool) error {
	if mode == grpcauth.AuthModeNone && !devMode {
		return fmt.Errorf("LEAFLAB_API_AUTH_MODE=%q requires LEAFLAB_API_DEV_MODE=true; refusing to start unauthenticated outside explicit development configuration", mode)
	}
	return nil
}

// buildServer wires the production interceptor chain and RPC registration
// around apiServer, exactly as run() serves it. Extracted so tests can
// build the identical wiring behind a bufconn listener (see
// startTestServer in main_test.go) or assert on reflection registration,
// without a TCP listener or a dialed DB/RabbitMQ connection.
//
// Chain order (NFR12): correlation-id -> auth -> rate limit -> acting-
// subject logging -> handler. "auth" is two interceptors: authUnary/
// authStream (verifies a presented token, injects Claims -- grpcauth's own
// in production; see run()) followed immediately by auth.go's enforcement
// interceptor (rejects any non-allowlisted method that reaches it with no
// Claims -- see its doc comment for why grpcauth alone doesn't enforce
// this). Correlation-id runs first so even an auth/rate-limit rejection is
// logged against the same id. Rate limiting (NFR10, ratelimit_interceptor.go)
// runs immediately after auth, before subject-logging, for two reasons:
// key derivation needs Claims already in context, and a request auth has
// already rejected must never consume rate-limit budget.
//
// Server reflection is a discovery/debugging aid; disabled outside
// explicit dev mode so a deployed environment never exposes it (FR11).
//
// MustValidateAuditRegistrations runs first (FR8/NFR8-adjacent structural
// check, audit_registry.go): a write RPC registered with no audit
// registration panics here, at startup, rather than shipping a silently
// unaudited write RPC to production.
func buildServer(authUnary grpc.UnaryServerInterceptor, authStream grpc.StreamServerInterceptor, rpcLogger *slog.Logger, apiServer pb.LeafLabAPIServer, devMode bool, limiter ratelimit.Limiter) *grpc.Server {
	MustValidateAuditRegistrations()

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			NewCorrelationUnaryInterceptor(),
			authUnary,
			NewAuthEnforcementUnaryInterceptor(),
			NewRateLimitUnaryInterceptor(limiter),
			NewSubjectLoggingUnaryInterceptor(rpcLogger),
		),
		grpc.ChainStreamInterceptor(
			NewCorrelationStreamInterceptor(),
			authStream,
			NewAuthEnforcementStreamInterceptor(),
			NewRateLimitStreamInterceptor(limiter),
			NewSubjectLoggingStreamInterceptor(rpcLogger),
		),
	)
	pb.RegisterLeafLabAPIServer(grpcServer, apiServer)

	if devMode {
		reflection.Register(grpcServer)
	}

	return grpcServer
}

// parsePollIntervalBounds parses LEAFLAB_API_POLL_INTERVAL_MS_MIN/_MAX
// (leaflab/api/ENV.md) into FR39's config.PollIntervalBounds. Extracted
// from run() so boot-time validation is unit-testable without dialing
// Postgres/RabbitMQ, matching validateAuthBootConfig's own pattern above.
func parsePollIntervalBounds(minStr, maxStr string) (config.PollIntervalBounds, error) {
	minMs, err := strconv.ParseUint(minStr, 10, 32)
	if err != nil {
		return config.PollIntervalBounds{}, fmt.Errorf("LEAFLAB_API_POLL_INTERVAL_MS_MIN: %w", err)
	}
	maxMs, err := strconv.ParseUint(maxStr, 10, 32)
	if err != nil {
		return config.PollIntervalBounds{}, fmt.Errorf("LEAFLAB_API_POLL_INTERVAL_MS_MAX: %w", err)
	}
	if minMs == 0 || maxMs == 0 || minMs > maxMs {
		return config.PollIntervalBounds{}, fmt.Errorf(
			"LEAFLAB_API_POLL_INTERVAL_MS_MIN/_MAX: min=%d max=%d is not a valid nonzero, min<=max range", minMs, maxMs)
	}
	return config.PollIntervalBounds{MinMs: uint32(minMs), MaxMs: uint32(maxMs)}, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
