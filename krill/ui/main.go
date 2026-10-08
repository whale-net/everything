// Command ui is krill's operator web UI: the Keycloak sign-in that the
// OAuth2 `/authorize` endpoint redirects to, and behind it the workspace shell
// whose pages all render through renderShellStatus.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/htmxbase"
	"github.com/whale-net/everything/libs/go/logging"
)

// faviconIco is embedded so FaviconHandler never serves a zero-byte 200;
// BUILD.bazel's embedsrcs must list the file.
//
//go:embed favicon.ico
var faviconIco []byte

// config is loaded entirely from environment variables (see ../ENV.md).
type config struct {
	Addr string

	// AuthMode is "none" (dev-only synthetic user) or "oidc" (Keycloak).
	AuthMode string

	// OIDC configuration, required when AuthMode == "oidc".
	OIDCIssuer string
	// RoleOperator/RoleReader are the realm roles for the operator and reader
	// personas.
	RoleOperator     string
	RoleReader       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string

	// SessionSecret encrypts the DB session store's tokens.
	SessionSecret string

	// DatabaseURL backs the UI session store and the MCP credential, client,
	// and auth-code stores. Always required.
	DatabaseURL string

	// UIPublicURL is this binary's external base URL and the OAuth2 issuer.
	UIPublicURL string

	// MCPPublicURL is the OAuth2 `resource`; it must byte-match `mcp`'s
	// KRILL_MCP_PUBLIC_URL or RFC 9728 discovery breaks.
	MCPPublicURL string

	// APIBaseURL is krill `api`, which every write goes to. Required: without
	// it no write can be attributed to an operator.
	APIBaseURL string

	// DevAPIToken is the bearer forwarded to api under AUTH_MODE=none; it must
	// equal api's KRILL_DEV_AUTH_TOKEN.
	DevAPIToken string
}

func loadConfig() config {
	return config{
		Addr:             getEnv("KRILL_UI_ADDR", ":8080"),
		AuthMode:         strings.ToLower(getEnv("AUTH_MODE", "none")),
		OIDCIssuer:       getEnv("KRILL_OIDC_ISSUER", ""),
		RoleOperator:     os.Getenv("KRILL_ROLE_OPERATOR"),
		RoleReader:       os.Getenv("KRILL_ROLE_READER"),
		OIDCClientID:     getEnv("KRILL_OIDC_CLIENT_ID", ""),
		OIDCClientSecret: getEnv("KRILL_OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:  getEnv("KRILL_OIDC_REDIRECT_URI", "http://localhost:8080/auth/callback"),
		SessionSecret:    getEnv("SECRET_KEY", "dev-secret-key-change-in-production"),
		DatabaseURL:      getEnv("PG_DATABASE_URL", ""),
		UIPublicURL:      getEnv("KRILL_UI_PUBLIC_URL", ""),
		MCPPublicURL:     getEnv("KRILL_MCP_PUBLIC_URL", ""),
		APIBaseURL:       getEnv("KRILL_API_URL", ""),
		DevAPIToken:      os.Getenv("KRILL_DEV_API_TOKEN"),
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type App struct {
	auth *htmxauth.Authenticator

	// oidcIssuer is the fixed issuer every operator's encoded identity carries.
	oidcIssuer string

	// devAPIToken is set only under AUTH_MODE=none; see requireOperator.
	devAPIToken string

	// devAuth admits the synthetic dev user as an operator under AUTH_MODE=none.
	devAuth bool

	// sessionRoles overrides how realm roles are read; nil uses the session.
	// Tests set it because cookie-backed test sessions do not persist roles.
	sessionRoles func(r *http.Request) ([]string, error)

	// roles maps realm roles to personas at credential-mint time.
	roles server.RoleConfig

	// mcpProvider is the OAuth2 authorization server. Its routes are
	// unauthenticated; `/authorize` mints only once the operator is signed in.
	mcpProvider *auth.Provider

	// credentials backs the self-serve credential API and credentials page;
	// the named variant lets the page mint with an operator-chosen name.
	credentials auth.NamedCredentialStore

	// writes issues krill writes under a session minted for the signed-in
	// operator (writeclient.go).
	writes *writeClient

	// scopes resolves the deployment's sole scope for minted sessions.
	scopes store.ScopeStore

	// tasks is the console query surface the ops views read directly; reads
	// need no krill session.
	tasks store.TaskStore

	// designSessions and revisionEvents are the same store accessors the MCP
	// design tools use, so browser and MCP see one session and ordering.
	designSessions store.DesignSessionStore
	revisionEvents store.RevisionEventStore

	// spec reads the spec axis for /spec pages (readclient.go); an interface so
	// view assembly is testable against a fake.
	spec specReadClient

	// now is the Overview's clock; nil means wall time. Tests freeze it.
	now func() time.Time
}

// NewApp wires Keycloak sign-in and the OAuth2 provider. Any error is
// startup-fatal.
func NewApp(ctx context.Context, cfg config) (*App, error) {
	var authMode htmxauth.AuthMode
	switch cfg.AuthMode {
	case "none", "":
		authMode = htmxauth.AuthModeNone
	case "oidc":
		authMode = htmxauth.AuthModeOIDC
	default:
		return nil, fmt.Errorf("invalid AUTH_MODE: %s (must be 'none' or 'oidc')", cfg.AuthMode)
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("PG_DATABASE_URL is required: krill-ui always uses DB-backed sessions and never falls back to cookie sessions")
	}
	if cfg.UIPublicURL == "" {
		return nil, fmt.Errorf("KRILL_UI_PUBLIC_URL is required")
	}
	if cfg.MCPPublicURL == "" {
		return nil, fmt.Errorf("KRILL_MCP_PUBLIC_URL is required")
	}
	if cfg.APIBaseURL == "" {
		return nil, fmt.Errorf("KRILL_API_URL is required: krill-ui issues its writes against krill's api binary")
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session DB: %w", err)
	}

	// Probes ui_sessions so a missing migration fails boot, not first sign-in.
	sessionStore, err := htmxauth.NewDBSessionManager(ctx, pool, cfg.SessionSecret, "krill_ui_session")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize session store: %w", err)
	}

	authConfig := htmxauth.Config{
		Mode:             authMode,
		SessionSecret:    cfg.SessionSecret,
		SessionName:      "krill_ui_session",
		OIDCIssuer:       cfg.OIDCIssuer,
		OIDCClientID:     cfg.OIDCClientID,
		OIDCClientSecret: cfg.OIDCClientSecret,
		OIDCRedirectURL:  cfg.OIDCRedirectURL,
	}

	// Performs Keycloak discovery; failure means the UI cannot start.
	auth, err := htmxauth.NewAuthenticatorWithDB(ctx, authConfig, sessionStore)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize authenticator (keycloak discovery): %w", err)
	}

	entities := store.New(pool)
	app := &App{
		auth:           auth,
		oidcIssuer:     cfg.OIDCIssuer,
		devAPIToken:    devTokenFor(cfg),
		devAuth:        authMode == htmxauth.AuthModeNone,
		roles:          server.RoleConfig{OperatorRole: cfg.RoleOperator, ReaderRole: cfg.RoleReader},
		scopes:         entities.Scopes(),
		tasks:          entities.Tasks(),
		designSessions: entities.DesignSessions(),
		revisionEvents: entities.RevisionEvents(),
		spec:           newSpecReader(entities),
	}

	// Each store preflights its own table and fails naming it if unmigrated.
	mcpProvider, credentials, err := setupMCPAuth(ctx, pool, cfg, app.mcpCallerResolver())
	if err != nil {
		return nil, fmt.Errorf("failed to initialize auth provider: %w", err)
	}
	app.mcpProvider = mcpProvider
	app.credentials = credentials

	writes, err := newWriteClient(writeClientConfig{BaseURL: cfg.APIBaseURL})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize write client: %w", err)
	}
	app.writes = writes

	return app, nil
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
		ServiceName:   "krill-ui",
		Domain:        "krill",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	ctx := context.Background()
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	if cfg.AuthMode == "none" || cfg.AuthMode == "" {
		logger.Warn("AUTH_MODE=none — krill-ui is running without Keycloak authentication (development only)")
	} else {
		logger.Info("running in oidc mode")
	}

	app, err := NewApp(ctx, cfg)
	if err != nil {
		logger.Error("failed to initialize application", "error", err)
		return err
	}

	mux := http.NewServeMux()
	app.setupRoutes(mux)

	httpServer := &http.Server{
		Addr:         cfg.Addr,
		Handler:      otelhttp.NewHandler(mux, "krill-ui"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.Addr)
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

// mountStaticRoutes registers unauthenticated static assets. Separate from
// setupRoutes so tests can mount the real registration.
func mountStaticRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/favicon.ico", htmxbase.FaviconHandler(faviconIco))
}

// setupRoutes registers every route. "/auth/login" aliases "/login" because
// htmxauth hardcodes that redirect target.
func (app *App) setupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", handleHealthz)

	// Unauthenticated on purpose: a static asset needs no session.
	mountStaticRoutes(mux)

	mux.HandleFunc("/login", app.auth.HandleLogin)
	mux.HandleFunc("/auth/login", app.auth.HandleLogin) // alias: see doc comment above.
	mux.HandleFunc("/auth/callback", app.auth.HandleCallback)
	mux.HandleFunc("/logout", app.auth.HandleLogout)

	// OAuth2 endpoints sit outside RequireAuth: discovery and client
	// registration must work before a client has any credential.
	app.mcpProvider.Mount(mux)

	// The self-serve credential API authenticates via the session cookie in its
	// own handler, so it needs no mux-level gate. An error here is a bug.
	if err := app.mcpProvider.MountSelfServe(mux); err != nil {
		panic(err)
	}

	// Operator writes. operatorRoute guarantees a resolved operator, and
	// withKrillSession attributes the write to them.
	mux.HandleFunc("POST /tasks/{id}/escalate", app.operatorRoute(app.handleEscalateTask))
	mux.HandleFunc("POST /design-sessions", app.operatorRoute(app.handleOpenDesignSession))

	// The four task interventions (interventions.go), one route per verb.
	mux.HandleFunc("POST "+opsTaskActionBase+"{id}/"+actionRelease, app.operatorRoute(app.handleTaskIntervention(actionRelease)))
	mux.HandleFunc("POST "+opsTaskActionBase+"{id}/"+actionRequeue, app.operatorRoute(app.handleTaskIntervention(actionRequeue)))
	mux.HandleFunc("POST "+opsTaskActionBase+"{id}/"+actionEscalate, app.operatorRoute(app.handleTaskIntervention(actionEscalate)))
	mux.HandleFunc("POST "+opsTaskActionBase+"{id}/"+actionCancel, app.operatorRoute(app.handleTaskIntervention(actionCancel)))
	// Cancel is irreversible, so its row control links to this confirm page.
	mux.HandleFunc("GET "+opsTaskActionBase+"{id}"+cancelConfirmSuffix, app.operatorRoute(app.handleCancelConfirm))

	app.mountShellRoutes(mux)
}

// mountShellRoutes registers the shell's pages behind sign-in. Separate so
// tests mount the same registrations production does.
func (app *App) mountShellRoutes(mux *http.ServeMux) {
	// Legacy URLs (ops console, spec/delivery, design browser, "/"), served
	// from one table; see legacyURLs.
	app.mountLegacyRoutes(mux)
	app.mountShellPages(mux)
}

// mountShellPages registers every shell page that is not a legacy URL.
// Separate so tests can pair it with a modified legacy table.
func (app *App) mountShellPages(mux *http.ServeMux) {
	// Credentials are reader routes: readers manage their own. Revoke's confirm
	// and dismiss steps are GETs so they work without JavaScript; only the POST
	// writes.
	mux.HandleFunc("GET "+credentialsPath, app.readerRoute(app.handleCredentials))
	mux.HandleFunc("GET "+credentialsNewPath, app.readerRoute(app.handleNewCredentialBlade))
	mux.HandleFunc("GET "+credentialsPath+"/{id}", app.readerRoute(app.handleCredentialRow))
	mux.HandleFunc("GET "+credentialsPath+"/{id}/revoke", app.readerRoute(app.handleRevokeConfirm))
	mux.HandleFunc("POST "+credentialsMintPath, app.readerRoute(app.handleMintCredential))
	mux.HandleFunc("POST "+credentialsPath+"/{id}/revoke", app.readerRoute(app.handleRevokeCredential))

	// JS-free product browse: 302 to the typed product's session list. The id
	// is uuid.Parse'd first, so it is not an open redirect.
	mux.HandleFunc("GET "+designGoPath, app.readerRoute(app.handleDesignGo))

	// Product-scoped session detail; the pid in the URL lets a copied link be
	// checked against its product.
	mux.HandleFunc("GET /design/products/{productID}/design-sessions/{id}", app.readerRoute(app.handleDesignSessionDetail))

	// The new-session blade answers both htmx (bare region) and browsers (full
	// page). The literal "new" outranks the {id} wildcard in the Go 1.22 mux.
	mux.HandleFunc("GET /design/products/{productID}/design-sessions/new", app.readerRoute(app.handleDesignSessionNew))

	// Design-session writes, both operatorRoute. The answer form posts under
	// the detail URL so the 303 back is one hop.
	mux.HandleFunc("POST /design/products/{productID}/design-sessions", app.operatorRoute(app.handleOpenDesignSessionForm))
	mux.HandleFunc("POST /design/products/{productID}/design-sessions/{id}/answers", app.operatorRoute(app.handleDesignSessionAnswerForm))

	// Product-scoped pages. Each handler resolves {pid} against the caller's
	// scope first, so an out-of-scope link is an in-shell 404.
	mux.HandleFunc("GET "+productPathPrefix+overviewSuffix, app.readerRoute(app.handleProductOverview))
	// Needs attention replaces the /ops queues, which redirect here via
	// legacyURLs.
	mux.HandleFunc("GET "+productPathPrefix+needsAttentionSuffix, app.readerRoute(app.handleNeedsAttention))
	// Tasks and Board are two views over the product-wide task read layer.
	mux.HandleFunc("GET "+productPathPrefix+tasksSuffix, app.readerRoute(app.handleProductTasks))
	// Task detail hangs beneath the tasks prefix so a copied link resolves its
	// product before the id.
	mux.HandleFunc("GET "+productPathPrefix+tasksSuffix+"/{tid}", app.readerRoute(app.handleProductTaskDetail))
	mux.HandleFunc("GET "+productPathPrefix+boardSuffix, app.readerRoute(app.handleProductBoard))
	mux.HandleFunc("GET "+productPathPrefix+milestonesSuffix, app.readerRoute(app.handleProductMilestones))
	// Milestone detail; milestones and milepebbles share {mid} since both are
	// milestone_ref rows.
	mux.HandleFunc("GET "+productPathPrefix+milestonesSuffix+"/{mid}", app.readerRoute(app.handleProductMilestoneDetail))
	// Status history, spelled from the rail's own suffix so link and route match.
	mux.HandleFunc("GET "+productPathPrefix+milestonesSuffix+"/{mid}"+milestoneStatusHistorySuffix, app.readerRoute(app.handleProductMilestoneStatusHistory))

	// The Product switcher records the pick as last-viewed and 302s to the same
	// area under the new product.
	mux.HandleFunc("GET "+productSwitchPath, app.readerRoute(app.handleProductSwitch))
}

// operatorRoute wraps a route in RequireAuth then requireOperator, so the
// handler always has a resolved Subject.
func (app *App) operatorRoute(next http.HandlerFunc) http.HandlerFunc {
	return app.auth.RequireAuthFunc(app.requireOperator(next))
}

// readerRoute wraps a route in RequireAuth then a reader-or-operator check
// (403 otherwise). Under AUTH_MODE=none the dev user is an operator.
func (app *App) readerRoute(next http.HandlerFunc) http.HandlerFunc {
	return app.auth.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		if !app.devAuth {
			roles, err := app.requestRoles(r)
			if err != nil {
				http.Error(w, "unauthenticated", http.StatusUnauthorized)
				return
			}
			if _, ok := app.roles.ResolvePersona(roles); !ok {
				http.Error(w, "forbidden: reader or operator role required", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	})
}

func (app *App) requestRoles(r *http.Request) ([]string, error) {
	if app.sessionRoles != nil {
		return app.sessionRoles(r)
	}
	user, err := app.auth.CurrentUser(r)
	if err != nil {
		return nil, err
	}
	return user.Roles, nil
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}

// devTokenFor returns the dev api token only in AUTH_MODE=none.
func devTokenFor(cfg config) string {
	if cfg.AuthMode == "none" || cfg.AuthMode == "" {
		return cfg.DevAPIToken
	}
	return ""
}
