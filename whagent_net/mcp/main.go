// Command mcp is whagent-net's MCP surface: the front door an operator
// drives from Claude Code (ARCHITECTURE.md "Open items": "`mcp` from
// Claude Code is the v1 answer"). It is a thin, faithful facade over
// `api`'s SessionService gRPC service (issue #2113,
// ARCHITECTURE.md "Service boundary vs. package boundary") -- every
// tool call is a pass-through RPC to `api`, authenticated as the
// operator who made it, never a shared service account (FR10). It never
// talks to Temporal directly, and the only Postgres it ever touches
// (optionally, FR9/issue #2249) is the mcp_credential table backing the
// OAuth2 token-exchange path's mcpauth.CredentialStore -- see
// initializeTokenExchange below.
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

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/mcpauth"
	pb "github.com/whale-net/everything/whagent_net/protos"

	"github.com/whale-net/everything/whagent_net/mcp/server"
	"github.com/whale-net/everything/whagent_net/mcp/tools"
)

// config holds `mcp`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md).
type config struct {
	// MCPAddr is the address this binary's streamable-HTTP MCP surface
	// listens on.
	MCPAddr string

	// APIAddr is `api`'s gRPC address (WHAGENT_API_URL, ../ENV.md
	// "Service wiring") -- the only outbound dependency this binary
	// dials.
	APIAddr string

	// MCPPublicURL is this binary's own externally reachable base URL
	// (WHAGENT_MCP_PUBLIC_URL) -- FR9/issue #2249's
	// server.ResourceMetadataConfig.Resource, must be byte-identical to
	// `ui`'s own mcpauth.ProviderConfig.Resource (same env var name on
	// `ui`, ../ENV.md's "`ui`" section). Left empty skips serving RFC
	// 9728 protected-resource metadata entirely (server.NewHTTPHandler's
	// doc comment) -- the manual-token recipe never depends on it.
	MCPPublicURL string

	// UIPublicURL is `ui`'s own externally reachable base URL
	// (WHAGENT_UI_PUBLIC_URL) -- the OAuth2 authorization server's
	// issuer identifier this binary advertises in its own RFC 9728
	// metadata (server.ResourceMetadataConfig.AuthorizationServer).
	UIPublicURL string

	// DatabaseURL backs the mcpauth.CredentialStore this binary probes
	// at startup for the FR9 OAuth2 path (PG_DATABASE_URL, the same
	// mcp_credential table #2245's migration created and `ui`'s
	// mcpauth.Provider already mints into). Left empty disables the
	// OAuth2 credential path entirely (initializeTokenExchange) -- the
	// manual-token recipe never depends on it.
	DatabaseURL string

	// TokenExchange is mcp's own confidential-client settings for the
	// RFC 8693 Keycloak token exchange (NFR8, server.TokenExchangeConfig's
	// doc comment) -- WHAGENT_MCP_KEYCLOAK_CLIENT_ID/
	// WHAGENT_MCP_KEYCLOAK_CLIENT_SECRET/WHAGENT_MCP_KEYCLOAK_TOKEN_URL.
	TokenExchange server.TokenExchangeConfig
}

func loadConfig() config {
	return config{
		MCPAddr:      getEnv("WHAGENT_MCP_ADDR", ":8082"),
		APIAddr:      os.Getenv("WHAGENT_API_URL"),
		MCPPublicURL: os.Getenv("WHAGENT_MCP_PUBLIC_URL"),
		UIPublicURL:  os.Getenv("WHAGENT_UI_PUBLIC_URL"),
		DatabaseURL:  os.Getenv("PG_DATABASE_URL"),
		TokenExchange: server.TokenExchangeConfig{
			ClientID:      os.Getenv("WHAGENT_MCP_KEYCLOAK_CLIENT_ID"),
			ClientSecret:  os.Getenv("WHAGENT_MCP_KEYCLOAK_CLIENT_SECRET"),
			TokenEndpoint: os.Getenv("WHAGENT_MCP_KEYCLOAK_TOKEN_URL"),
		},
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// tokenExchangeDeps holds the FR9 OAuth2 token-exchange path's optional
// dependencies (issue #2249's Scaffold phase): a Postgres-backed
// mcpauth.CredentialStore against the same mcp_credential table `ui`'s
// mcpauth.Provider mints into (whagent_net/migrate/schema/migrations/
// 004_mcpauth_credential, issue #2245), and mcp's own confidential-client
// server.Exchanger. Neither is wired into the request path yet --
// server/auth.go's PassthroughVerifier/AuthMiddleware are unchanged by
// this task's Scaffold phase; a dependent Implementation-phase change
// consumes both fields.
type tokenExchangeDeps struct {
	pool        *pgxpool.Pool
	credentials mcpauth.CredentialStore
	exchanger   server.Exchanger
}

// Close releases pool, if initializeTokenExchange opened one.
func (d tokenExchangeDeps) Close() {
	if d.pool != nil {
		d.pool.Close()
	}
}

// initializeTokenExchange builds tokenExchangeDeps from cfg. Construction
// is non-fatal throughout (mirrors whagent_net/ui/main.go's
// initializeSSEHub degrade-and-log convention for every other optional
// dependency in this binary): cfg.DatabaseURL unset, an unreachable
// database, or a missing mcp_credential table all degrade to "OAuth2
// credential path unavailable" rather than preventing `mcp` from
// starting -- the manual-token recipe never depends on any of this.
func initializeTokenExchange(ctx context.Context, cfg config, logger *slog.Logger) tokenExchangeDeps {
	exchanger := server.NewKeycloakExchanger(cfg.TokenExchange)
	if !cfg.TokenExchange.Enabled() {
		logger.Warn("WHAGENT_MCP_KEYCLOAK_CLIENT_ID/WHAGENT_MCP_KEYCLOAK_CLIENT_SECRET/WHAGENT_MCP_KEYCLOAK_TOKEN_URL not fully set; FR9 OAuth2 token exchange unavailable (manual-token recipe still works)")
	}

	if cfg.DatabaseURL == "" {
		logger.Warn("PG_DATABASE_URL not set; FR9 OAuth2 credential path unavailable (manual-token recipe still works)")
		return tokenExchangeDeps{exchanger: exchanger}
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Warn("failed to connect to mcp_credential database; FR9 OAuth2 credential path unavailable (manual-token recipe still works)", "error", err)
		return tokenExchangeDeps{exchanger: exchanger}
	}

	// NewCredentialStore preflights the mcp_credential table (the same
	// migration `ui`'s mcpauth.Provider requires, issue #2245) before
	// returning.
	credentials, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{Pool: pool})
	if err != nil {
		logger.Warn("failed to initialize mcpauth credential store; FR9 OAuth2 credential path unavailable (manual-token recipe still works)", "error", err)
		pool.Close()
		return tokenExchangeDeps{exchanger: exchanger}
	}

	logger.Info("mcpauth credential store initialized for the FR9 OAuth2 token-exchange path")
	return tokenExchangeDeps{pool: pool, credentials: credentials, exchanger: exchanger}
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
		ServiceName:   "whagent-net-mcp",
		Domain:        "whagent-net",
		Level:         slog.LevelInfo,
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	ctx := context.Background()
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	if cfg.APIAddr == "" {
		return fmt.Errorf("WHAGENT_API_URL is required")
	}

	// mcp is a pure facade over api's SessionService: it never connects
	// to Temporal, and its only outbound RPC dependency is api's own
	// gRPC address (ARCHITECTURE.md "Service boundary vs. package
	// boundary"). FR9 (issue #2249) is the one exception on the Postgres
	// side: when cfg.DatabaseURL is set, initializeTokenExchange below
	// probes the same mcp_credential table `ui`'s mcpauth.Provider mints
	// into -- optional, non-fatal, and still wired to nothing in the
	// request path as of this Scaffold-phase change (a dependent
	// Implementation-phase change to auth.go is what actually routes a
	// call through it). NewUserTokenDialOption(AuthModeOIDC) is unconditional
	// (not read from GRPC_AUTH_MODE-style config): every call that reaches
	// a tool handler already carries a bearer token on ctx (server/auth.go's
	// AuthMiddleware rejects any call without one before a tool handler
	// runs), so this dial option always forwards it, byte for byte, as the
	// outbound call's own Authorization header -- the operator's identity,
	// never a shared service account (FR10).
	apiConn, err := grpcclient.NewClient(ctx, cfg.APIAddr, grpcauth.NewUserTokenDialOption(grpcauth.AuthModeOIDC))
	if err != nil {
		return fmt.Errorf("dial api at %s: %w", cfg.APIAddr, err)
	}
	defer apiConn.Close() //nolint:errcheck

	client := pb.NewSessionServiceClient(apiConn.GetConnection())

	srv := server.New()
	tools.RegisterStartSession(srv, client)
	tools.RegisterSendTurn(srv, client)
	tools.RegisterStopSession(srv, client)
	tools.RegisterGetSession(srv, client)
	tools.RegisterReadTranscript(srv, client)

	tex := initializeTokenExchange(ctx, cfg, logger)
	defer tex.Close()

	resourceMeta := server.ResourceMetadataConfig{
		Resource:            cfg.MCPPublicURL,
		AuthorizationServer: cfg.UIPublicURL,
		ResourceName:        "whagent-net MCP",
	}

	httpServer := &http.Server{
		Addr:         cfg.MCPAddr,
		Handler:      otelhttp.NewHandler(server.NewHTTPHandler(srv, resourceMeta), "whagent-net-mcp"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.MCPAddr, "api_addr", cfg.APIAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
