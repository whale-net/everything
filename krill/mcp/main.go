// Command mcp is krill's MCP surface: the only MCP-capable way any harness
// (Claude Code today) reaches the FR5-FR9 scoped-slice query
// (//krill/slice) and, as of issue #2547, the FR1-FR10 design-session
// surface (//krill/mcp/tools' design.go), with no krill-specific harness
// code. See ../ARCHITECTURE.md "The MCP spec surface" and "The
// design-session MCP surface" for the two-front-door design this mirrors
// from audience_score_system/mcp and whagent_net/mcp.
//
// `mcp` mounts two pre-filtered tool surfaces, each on its own *mcp.Server
// and its own mount point (server/transport.go's specMountPath and
// designMountPath): the FR5-FR8 read-only spec surface at /mcp/spec
// (unchanged since M1), and this task's FR1-FR10 design-session surface
// (three write tools, three read tools) at /mcp/design -- distinct from
// the future work-axis surface (M4, no endpoint exists for it yet, root
// plan issue #2485's roadmap). Both front doors (mcpauth/human,
// whagent-net/agent) apply to both mounts identically.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/whagent"
)

// config holds `mcp`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md).
type config struct {
	// MCPAddr is the address this binary's streamable-HTTP MCP surface
	// listens on.
	MCPAddr string

	// DatabaseURL is PG_DATABASE_URL -- the same pool //krill/slice's
	// Querier reads from (empty defers to libs/go/db.NewPool's own
	// PG_DATABASE_URL fallback).
	DatabaseURL string

	// MCPPublicURL is this instance's own externally reachable URL
	// (KRILL_MCP_PUBLIC_URL) -- passed as
	// server.ResourceMetadataConfig.Resource and as the audience every
	// whagent Claim this instance verifies must carry.
	MCPPublicURL string

	// OAuthIssuer is the mcpauth front door's OAuth2 authorization
	// server's issuer identifier (KRILL_MCP_OAUTH_ISSUER) -- the
	// authorization_servers entry this instance's protected-resource
	// metadata advertises. Left unset skips serving RFC 9728 metadata
	// entirely (server.ResourceMetadataConfig.enabled).
	OAuthIssuer string

	// WhagentJWKSURL and WhagentIssuer (KRILL_MCP_WHAGENT_JWKS_URL /
	// KRILL_MCP_WHAGENT_ISSUER) are whagent-net's own JWKS endpoint and
	// issuer identifier -- both required to enable the agent front door
	// (server.WhagentAuthConfig). Left unset, `mcp` mounts only the
	// mcpauth door (server.NewHTTPHandler), same as
	// audience_score_system/mcp's own pre-FR12(a) fallback.
	WhagentJWKSURL string
	WhagentIssuer  string
}

func loadConfig() config {
	return config{
		MCPAddr:        getEnv("KRILL_MCP_ADDR", ":8080"),
		DatabaseURL:    os.Getenv("PG_DATABASE_URL"),
		MCPPublicURL:   os.Getenv("KRILL_MCP_PUBLIC_URL"),
		OAuthIssuer:    os.Getenv("KRILL_MCP_OAUTH_ISSUER"),
		WhagentJWKSURL: os.Getenv("KRILL_MCP_WHAGENT_JWKS_URL"),
		WhagentIssuer:  os.Getenv("KRILL_MCP_WHAGENT_ISSUER"),
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := loadConfig()

	logging.Configure(logging.Config{
		ServiceName:   "krill-mcp",
		Domain:        "krill",
		Level:         slog.LevelInfo,
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	ctx := context.Background()
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	entities := store.New(pool)
	sessions := store.NewSessionStore(pool)
	querier := slice.NewQuerier(entities)

	// Two *mcp.Server instances, one per mount (server/transport.go's
	// specMountPath and designMountPath) -- registering a tool is a
	// per-server operation (mcp.AddTool), so the only way to guarantee the
	// FR1-FR10 write tools this task adds can never end up reachable from
	// specMountPath is to never register them on the same *mcp.Server that
	// backs it. tools.RegisterAll (FR5-FR8, read-only) is unchanged;
	// tools.RegisterDesignAll (this task) is new.
	specSrv := server.New()
	specReg := server.NewRegistry(specSrv)
	tools.RegisterAll(specReg, querier)

	designSrv := server.New()
	designReg := server.NewRegistry(designSrv)
	tools.RegisterDesignAll(designReg, entities, sessions, querier)

	// The mcpauth (human) front door's CredentialStore preflights the
	// consuming domain's credential table at boot -- exactly like
	// audience_score_system/mcp/main.go's own NewCredentialStore call.
	// krill has not yet shipped that migration (no later M1 task numbers
	// one in ../ARCHITECTURE.md's migration table as of this task) --
	// until it does, this degrades to an always-reject store rather than
	// a fatal boot error, mirroring whagent_net/mcp/main.go's
	// initializeAuthDeps degrade-and-log convention for every other
	// optional dependency: the agent front door below never depends on
	// this succeeding.
	credentials, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{Pool: pool})
	if err != nil {
		logger.Warn("mcpauth credential store unavailable; the mcpauth (human) front door will reject every call until its migration is applied", "error", err)
		credentials = rejectingCredentialStore{}
	}

	resourceMeta := server.ResourceMetadataConfig{
		Resource:            cfg.MCPPublicURL,
		AuthorizationServer: cfg.OAuthIssuer,
		ResourceName:        "krill MCP",
	}

	// Mount the agent (whagent-net) front door ALONGSIDE the mcpauth one
	// -- never in place of it -- whenever it's configured (NFR1: "both
	// front doors at the same mount point, each env-gated"). Both
	// KRILL_MCP_WHAGENT_JWKS_URL and KRILL_MCP_WHAGENT_ISSUER unset falls
	// back to the mcpauth-only handler, exactly mirroring
	// audience_score_system/mcp/main.go's own FR12(a) fallback.
	var handler http.Handler
	if cfg.WhagentJWKSURL != "" && cfg.WhagentIssuer != "" {
		whagentVerifier, err := whagent.NewVerifier(ctx, cfg.WhagentJWKSURL, cfg.WhagentIssuer)
		if err != nil {
			return fmt.Errorf("whagent verifier: %w", err)
		}
		specSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())
		designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())
		handler = server.NewDualAuthHTTPHandler(specSrv, designSrv, credentials, server.WhagentAuthConfig{
			Verifier: whagentVerifier,
			Audience: cfg.MCPPublicURL,
		}, resourceMeta)
	} else {
		handler = server.NewHTTPHandler(specSrv, designSrv, credentials, resourceMeta)
	}

	httpServer := &http.Server{
		Addr:         cfg.MCPAddr,
		Handler:      otelhttp.NewHandler(handler, "krill-mcp"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.MCPAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed { //nolint:errorlint // net/http documents this exact sentinel
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-shutdownCtx.Done()
	logger.Info("shutdown signal received, draining in-flight requests")

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(drainCtx); err != nil {
		logger.Warn("graceful shutdown did not complete cleanly", "error", err)
	}
	return nil
}

// rejectingCredentialStore is a mcpauth.CredentialStore of last resort:
// every call fails with the same opaque "invalid or revoked credential"
// mcpauth.TokenVerifier already produces for any other Verify failure, so
// a caller presenting an mcpauth-shaped credential against a
// not-yet-migrated krill deployment gets a clean 401 instead of `mcp`
// panicking on a nil CredentialStore interface value.
type rejectingCredentialStore struct{}

func (rejectingCredentialStore) Mint(context.Context, string) (string, mcpauth.Credential, error) {
	return "", mcpauth.Credential{}, fmt.Errorf("mcpauth: credential store not configured")
}

func (rejectingCredentialStore) Verify(context.Context, string) (string, mcpauth.Credential, error) {
	return "", mcpauth.Credential{}, fmt.Errorf("mcpauth: credential store not configured")
}

func (rejectingCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return fmt.Errorf("mcpauth: credential store not configured")
}

func (rejectingCredentialStore) List(context.Context, string) ([]mcpauth.Credential, error) {
	return nil, fmt.Errorf("mcpauth: credential store not configured")
}

var _ mcpauth.CredentialStore = rejectingCredentialStore{}
