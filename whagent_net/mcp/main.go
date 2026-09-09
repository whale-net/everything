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
// initializeTokenExchange below. On that path, `mcp` also holds its own
// confidential Keycloak client (WHAGENT_MCP_KEYCLOAK_*, ../ENV.md) with
// token-exchange/impersonation rights, used to exchange a resolved
// operator identity for a short-lived, real Keycloak-signed JWT (RFC
// 8693, server/tokenexchange.go) before ever calling `api` -- `api`
// itself verifies real Keycloak tokens only, so this is what makes the
// two credential shapes indistinguishable downstream.
package main

import (
	"context"
	"errors"
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

// tokenExchangeDeps holds the FR9 OAuth2 token-exchange path's
// dependencies (issue #2249): a Postgres-backed mcpauth.CredentialStore
// against the same mcp_credential table `ui`'s mcpauth.Provider mints
// into (whagent_net/migrate/schema/migrations/004_mcpauth_credential,
// issue #2245), and mcp's own confidential-client server.Exchanger.
// run() passes credentials into server.NewHTTPHandler (auth.go's
// NewVerifier, the HTTP-layer classifier) and exchanger into server.New
// (auth.go's AuthMiddleware, the MCP-protocol-layer exchange call) --
// both fields are always consumed, though credentials may be nil (FR9
// not configured) and exchanger may be constructed disabled (see
// initializeTokenExchange's NFR8 fail-loud check for the one combination
// that is instead a startup error).
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
// is non-fatal for the parts of FR9 that are purely optional (mirrors
// whagent_net/ui/main.go's initializeSSEHub degrade-and-log convention
// for every other optional dependency in this binary): cfg.DatabaseURL
// unset, an unreachable database, or a missing mcp_credential table all
// degrade to "OAuth2 credential path unavailable" rather than preventing
// `mcp` from starting -- the manual-token recipe never depends on any of
// this.
//
// NFR8's fail-loud requirement is the one exception: once a
// mcpauth.CredentialStore is actually reachable, the OAuth2 path becomes
// reachable too (server.NewVerifier routes any credential-shaped token
// there regardless of whether an exchange can ever succeed), so running
// with credentials configured but cfg.TokenExchange disabled would mean
// every OAuth2-path call fails opaquely at Exchange time instead of at
// startup. This function returns an error in exactly that combination --
// run() below treats it as fatal -- rather than silently degrading like
// every other case here.
func initializeTokenExchange(ctx context.Context, cfg config, logger *slog.Logger) (tokenExchangeDeps, error) {
	exchanger := server.NewKeycloakExchanger(cfg.TokenExchange)
	if !cfg.TokenExchange.Enabled() {
		logger.Warn("WHAGENT_MCP_KEYCLOAK_CLIENT_ID/WHAGENT_MCP_KEYCLOAK_CLIENT_SECRET/WHAGENT_MCP_KEYCLOAK_TOKEN_URL not fully set; FR9 OAuth2 token exchange unavailable (manual-token recipe still works)")
	}

	if cfg.DatabaseURL == "" {
		logger.Warn("PG_DATABASE_URL not set; FR9 OAuth2 credential path unavailable (manual-token recipe still works)")
		return tokenExchangeDeps{exchanger: exchanger}, nil
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Warn("failed to connect to mcp_credential database; FR9 OAuth2 credential path unavailable (manual-token recipe still works)", "error", err)
		return tokenExchangeDeps{exchanger: exchanger}, nil
	}

	// NewCredentialStore preflights the mcp_credential table (the same
	// migration `ui`'s mcpauth.Provider requires, issue #2245) before
	// returning.
	credentials, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{Pool: pool})
	if err != nil {
		logger.Warn("failed to initialize mcpauth credential store; FR9 OAuth2 credential path unavailable (manual-token recipe still works)", "error", err)
		pool.Close()
		return tokenExchangeDeps{exchanger: exchanger}, nil
	}

	if !cfg.TokenExchange.Enabled() {
		pool.Close()
		return tokenExchangeDeps{}, errors.New(
			"PG_DATABASE_URL is set (FR9 OAuth2 credential path reachable) but WHAGENT_MCP_KEYCLOAK_CLIENT_ID/" +
				"WHAGENT_MCP_KEYCLOAK_CLIENT_SECRET/WHAGENT_MCP_KEYCLOAK_TOKEN_URL are not fully set (NFR8): " +
				"either configure all three, or unset PG_DATABASE_URL to run manual-token-only",
		)
	}

	logger.Info("mcpauth credential store initialized for the FR9 OAuth2 token-exchange path")
	return tokenExchangeDeps{pool: pool, credentials: credentials, exchanger: exchanger}, nil
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
	// into, and wires the OAuth2 path into both server.NewHTTPHandler
	// (the HTTP-layer verifier, auth.go's NewVerifier) and server.New
	// (the MCP-protocol-layer AuthMiddleware) below -- resolved and
	// checked for the NFR8 fail-loud combination before anything else is
	// constructed, so a misconfiguration is reported before `mcp` ever
	// dials `api`. NewUserTokenDialOption(AuthModeOIDC) is unconditional
	// (not read from GRPC_AUTH_MODE-style config): every call that reaches
	// a tool handler already carries a bearer token on ctx (server/auth.go's
	// AuthMiddleware rejects any call without one before a tool handler
	// runs, whichever path resolved it), so this dial option always
	// forwards it, byte for byte, as the outbound call's own Authorization
	// header -- the operator's identity, never a shared service account
	// (FR10).
	tex, err := initializeTokenExchange(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer tex.Close()

	apiConn, err := grpcclient.NewClient(ctx, cfg.APIAddr, grpcauth.NewUserTokenDialOption(grpcauth.AuthModeOIDC))
	if err != nil {
		return fmt.Errorf("dial api at %s: %w", cfg.APIAddr, err)
	}
	defer apiConn.Close() //nolint:errcheck

	client := pb.NewSessionServiceClient(apiConn.GetConnection())

	srv := server.New(tex.exchanger)
	tools.RegisterStartSession(srv, client)
	tools.RegisterSendTurn(srv, client)
	tools.RegisterStopSession(srv, client)
	tools.RegisterGetSession(srv, client)
	tools.RegisterReadTranscript(srv, client)

	resourceMeta := server.ResourceMetadataConfig{
		Resource:            cfg.MCPPublicURL,
		AuthorizationServer: cfg.UIPublicURL,
		ResourceName:        "whagent-net MCP",
	}

	httpServer := &http.Server{
		Addr:         cfg.MCPAddr,
		Handler:      otelhttp.NewHandler(server.NewHTTPHandler(srv, tex.credentials, resourceMeta), "whagent-net-mcp"),
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
