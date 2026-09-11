// Command mcp is whagent-net's MCP surface: the front door an operator
// drives from Claude Code (ARCHITECTURE.md "Open items": "`mcp` from
// Claude Code is the v1 answer"). It is a thin, faithful facade over
// `api`'s SessionService gRPC service (issue #2113,
// ARCHITECTURE.md "Service boundary vs. package boundary") -- every
// tool call is a pass-through RPC to `api`, authenticated as the
// operator who made it, never a shared service account (FR10). It never
// talks to Temporal directly, and every Postgres table it ever touches
// (always optionally, gated on PG_DATABASE_URL) is reached only from this
// package (package main) -- never from mcp/server or mcp/tools, per
// issue #2120's TestBUILD_NoStoreOrTemporalDependency in each of those
// packages. Today that is: the mcp_credential table backing the FR9/
// issue #2249 OAuth2 identity-resolution path's mcpauth.CredentialStore
// -- see initializeAuthDeps below -- the agent_definition/session_agent
// tables backing whagent_net/mcpdomain.Resolver's DomainResolver
// implementation (issue #2427, FR7) -- and the grpcauth_delegated_grant/
// grpcauth_grant_index tables backing //whagent_net/delegatedgrant's
// Store/Index (see initializeDelegatedGrant, delegatedgrant.go).
//
// `mcp` mints nothing of its own for the browser-OAuth2/FR9 path: as of
// issue #2430 (FR7/FR8/FR19), a resolved operator identity is exchanged
// for a working credential exclusively via the shared
// //whagent_net/delegatedgrant.Components.Source's
// TokenSource(subject, grant).Token(ctx), called at tool-dispatch time
// (../mcp/tools' RegisterXxx handlers) once a call's target domain is
// known -- never at auth-middleware time, and never via RFC 8693
// impersonation exchange (server/tokenexchange.go, WHAGENT_MCP_KEYCLOAK_*,
// deleted by this same issue, not left dormant). Issue #2426's shared
// confidential client (WHAGENT_GRANT_*, distinct from
// WHAGENT_OIDC_CLIENT_ID/_SECRET) is what that TokenSource call
// ultimately authenticates against -- see delegatedgrant.go's doc comment
// and NFR5.
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
	"github.com/whale-net/everything/whagent_net/delegatedgrant"
	"github.com/whale-net/everything/whagent_net/mcpdomain"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"

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
	// OAuth2 credential path entirely (initializeAuthDeps) -- the
	// manual-token recipe never depends on it.
	DatabaseURL string

	// OIDCIssuer is the Keycloak realm issuer (WHAGENT_OIDC_ISSUER,
	// ../ENV.md "Identity") -- read here (not previously part of this
	// binary's own config struct) solely as the realm the shared
	// delegated-grant client (GrantClientID etc. below) lives in, per
	// //whagent_net/delegatedgrant.Config.Issuer.
	OIDCIssuer string

	// GrantClientID/GrantClientSecret/GrantRedirectURI/GrantEncryptionKey
	// configure the single shared confidential Keycloak client
	// //whagent_net/delegatedgrant.Build constructs (issue #2426,
	// FR10/FR13/NFR5/NFR6 of plan #2421) -- WHAGENT_GRANT_CLIENT_ID/
	// _CLIENT_SECRET/_REDIRECT_URI/_ENCRYPTION_KEY (../ENV.md). Distinct
	// from WHAGENT_OIDC_CLIENT_ID/_SECRET (which only ever verifies or
	// forwards a token, never mints one). This is now `mcp`'s only
	// token-acquisition path for the browser-OAuth2 identity-resolution
	// case (issue #2430, FR8) -- the RFC 8693 impersonation exchange it
	// replaced held its own, separate confidential-client settings, since
	// deleted along with tokenexchange.go.
	GrantClientID      string
	GrantClientSecret  string
	GrantRedirectURI   string
	GrantEncryptionKey string
}

func loadConfig() config {
	return config{
		MCPAddr:            getEnv("WHAGENT_MCP_ADDR", ":8082"),
		APIAddr:            os.Getenv("WHAGENT_API_URL"),
		MCPPublicURL:       os.Getenv("WHAGENT_MCP_PUBLIC_URL"),
		UIPublicURL:        os.Getenv("WHAGENT_UI_PUBLIC_URL"),
		DatabaseURL:        os.Getenv("PG_DATABASE_URL"),
		OIDCIssuer:         os.Getenv("WHAGENT_OIDC_ISSUER"),
		GrantClientID:      os.Getenv("WHAGENT_GRANT_CLIENT_ID"),
		GrantClientSecret:  os.Getenv("WHAGENT_GRANT_CLIENT_SECRET"),
		GrantRedirectURI:   os.Getenv("WHAGENT_GRANT_REDIRECT_URI"),
		GrantEncryptionKey: os.Getenv("WHAGENT_GRANT_ENCRYPTION_KEY"),
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// authDeps holds every optional Postgres-backed composition-root
// dependency `mcp` gates on PG_DATABASE_URL: a Postgres-backed
// mcpauth.CredentialStore against the same mcp_credential table `ui`'s
// mcpauth.Provider mints into (whagent_net/migrate/schema/migrations/
// 004_mcpauth_credential, issue #2245, FR9's OAuth2 identity-resolution
// path), whagent_net/mcpdomain.Resolver's DomainResolver implementation
// (issue #2427, FR7), and //whagent_net/delegatedgrant's
// Store/Index/DelegatedGrantSource triple (issue #2426, FR10/FR13/NFR5/
// NFR6). All three share this one struct and this one pool purely because
// they are constructed at the same point in startup against the same
// optional dependency -- not because they are otherwise related.
//
// run() passes credentials into server.NewHTTPHandler (auth.go's
// NewVerifier, the HTTP-layer classifier), and domainResolver/grant.Source
// into every tools.RegisterX call (issue #2430, FR7/FR8): *mcpdomain.Resolver
// satisfies tools.DomainResolver and *grpcauth.DelegatedGrantSource
// satisfies tools.GrantSource, both structurally (domain.go/grant.go) --
// passed as those small domain-neutral interfaces, never this concrete
// struct or the whagent_net/delegatedgrant/whagent_net/mcpdomain packages
// themselves, which is what mcp/server's and mcp/tools' own
// TestBUILD_NoStoreOrTemporalDependency (issue #2120) protects. Each tool
// handler now calls DomainForAgent/DomainForSession -> grantkey.ForDomain
// -> TokenSource(subject, grant).Token(ctx) at dispatch time for the
// browser-OAuth2 path (mcp/tools' resolveGrantTokenForAgent/
// resolveGrantTokenForSession) -- this is FR8's sole token-acquisition
// path; there is no RFC 8693 impersonation exchange left to fall back to
// (FR19, tokenexchange.go deleted).
//
// domainResolver/credentials may be nil exactly when cfg.DatabaseURL is
// unset or the pool is unreachable; grant is then also its zero value.
type authDeps struct {
	pool           *pgxpool.Pool
	credentials    mcpauth.CredentialStore
	domainResolver *mcpdomain.Resolver
	grant          delegatedgrant.Components
}

// Close releases pool, if initializeAuthDeps opened one.
func (d authDeps) Close() {
	if d.pool != nil {
		d.pool.Close()
	}
}

// initializeAuthDeps builds authDeps from cfg. Construction is entirely
// non-fatal (mirrors whagent_net/ui/main.go's initializeSSEHub
// degrade-and-log convention for every other optional dependency in this
// binary), except a *partially* configured delegated-grant client, which
// initializeDelegatedGrant itself treats as fatal (see its own doc
// comment for why): cfg.DatabaseURL unset, an unreachable database, or a
// missing mcp_credential table all degrade to "OAuth2 credential path
// unavailable" rather than preventing `mcp` from starting -- the
// manual-token recipe never depends on any of this.
func initializeAuthDeps(ctx context.Context, cfg config, logger *slog.Logger) (authDeps, error) {
	if cfg.DatabaseURL == "" {
		logger.Warn("PG_DATABASE_URL not set; FR9 OAuth2 credential path unavailable (manual-token recipe still works)")
		return authDeps{}, nil
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Warn("failed to connect to mcp_credential database; FR9 OAuth2 credential path unavailable (manual-token recipe still works)", "error", err)
		return authDeps{}, nil
	}

	// Delegated-grant wiring (issue #2426) is unrelated to FR9's
	// credential path below -- see authDeps' doc comment for why it
	// shares this pool and this function anyway. A partial
	// misconfiguration here is fatal (initializeDelegatedGrant's own doc
	// comment); ErrNotConfigured degrades to a WARNING and a zero-value
	// Components, same as every other optional dependency in this
	// function.
	grant, err := initializeDelegatedGrant(ctx, cfg, pool, logger)
	if err != nil {
		pool.Close()
		return authDeps{}, err
	}

	// NewCredentialStore preflights the mcp_credential table (the same
	// migration `ui`'s mcpauth.Provider requires, issue #2245) before
	// returning.
	credentials, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{Pool: pool})
	if err != nil {
		logger.Warn("failed to initialize mcpauth credential store; FR9 OAuth2 credential path unavailable (manual-token recipe still works)", "error", err)
		pool.Close()
		return authDeps{grant: grant}, nil
	}

	logger.Info("mcpauth credential store initialized for the FR9 OAuth2 identity-resolution path")

	// domainResolver (issue #2427, FR7) is constructed against the same
	// pool credentials just was. Unlike mcpauth.NewCredentialStore,
	// session.New/AgentDefinitions perform no preflight query of their
	// own, so there is nothing further to degrade on here: the pool
	// already proved reachable immediately above.
	domainResolver := mcpdomain.New(session.New(pool, nil).AgentDefinitions())

	return authDeps{pool: pool, credentials: credentials, domainResolver: domainResolver, grant: grant}, nil
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
	// side: when cfg.DatabaseURL is set, initializeAuthDeps below probes
	// the same mcp_credential table `ui`'s mcpauth.Provider mints into,
	// and wires the OAuth2 identity-resolution path into
	// server.NewHTTPHandler (the HTTP-layer verifier, auth.go's
	// NewVerifier) -- resolved before anything else is constructed, so a
	// misconfiguration is reported before `mcp` ever dials `api`.
	// NewUserTokenDialOption(AuthModeOIDC) is unconditional (not read from
	// GRPC_AUTH_MODE-style config): every call that reaches a tool handler
	// already carries a bearer token on ctx -- either forwarded byte for
	// byte by the manual-token path, or acquired at tool-dispatch time via
	// GrantSource for the browser-OAuth2 path (issue #2430, FR7/FR8;
	// server/auth.go's AuthMiddleware rejects any call carrying neither
	// before a tool handler runs) -- so this dial option always forwards
	// it as the outbound call's own Authorization header -- the operator's
	// identity, never a shared service account (FR10).
	auth, err := initializeAuthDeps(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer auth.Close()

	apiConn, err := grpcclient.NewClient(ctx, cfg.APIAddr, grpcauth.NewUserTokenDialOption(grpcauth.AuthModeOIDC))
	if err != nil {
		return fmt.Errorf("dial api at %s: %w", cfg.APIAddr, err)
	}
	defer apiConn.Close() //nolint:errcheck

	client := pb.NewSessionServiceClient(apiConn.GetConnection())

	srv := server.New()
	// auth.domainResolver (issue #2427, FR7) and auth.grant.Source (issue
	// #2426, FR8) are threaded into every tool here: *mcpdomain.Resolver
	// and *grpcauth.DelegatedGrantSource each satisfy tools.DomainResolver/
	// tools.GrantSource structurally (domain.go/grant.go's doc comments),
	// with no adapter and no direct import of mcpdomain/delegatedgrant
	// from mcp/tools itself. Each tool's call() resolves the domain the
	// call actually targets and acquires a token via GrantSource at
	// dispatch time for the browser-OAuth2 path only (issue #2430's
	// Implementation phase; see mcp/tools' resolveGrantTokenForAgent/
	// resolveGrantTokenForSession) -- both may be nil (cfg.DatabaseURL
	// unset), which those dispatch-time helpers treat identically to
	// "no identity resolved", since a nil domainResolver/grant is only
	// ever consulted when an Identity is actually on ctx, and mcp/server's
	// NewVerifier never produces one without a reachable
	// mcpauth.CredentialStore in the first place.
	tools.RegisterStartSession(srv, client, auth.domainResolver, auth.grant.Source)
	tools.RegisterSendTurn(srv, client, auth.domainResolver, auth.grant.Source)
	tools.RegisterStopSession(srv, client, auth.domainResolver, auth.grant.Source)
	tools.RegisterGetSession(srv, client, auth.domainResolver, auth.grant.Source)
	tools.RegisterReadTranscript(srv, client, auth.domainResolver, auth.grant.Source)

	resourceMeta := server.ResourceMetadataConfig{
		Resource:            cfg.MCPPublicURL,
		AuthorizationServer: cfg.UIPublicURL,
		ResourceName:        "whagent-net MCP",
	}

	httpServer := &http.Server{
		Addr:         cfg.MCPAddr,
		Handler:      otelhttp.NewHandler(server.NewHTTPHandler(srv, auth.credentials, resourceMeta), "whagent-net-mcp"),
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
