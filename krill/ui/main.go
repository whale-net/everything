// Command ui is krill's operator web UI. It is two things layered on one
// binary: the Keycloak sign-in flow that gives //libs/go/auth's
// `/authorize` endpoint (mounted here) somewhere to redirect a
// not-yet-signed-in caller, per ProviderConfig.SignInURL -- before this
// binary existed, `/authorize` had no SignInURL configured and just 401ed
// on an unresolved caller (see ARCHITECTURE.md "krill/ui and the auth front
// door") -- and, behind that sign-in, the workspace shell: a drawer
// sidebar (nav.go) whose grouped nav links the ops console, the
// design-session browser, and the spec+delivery browser, plus the
// credential widget that shell inherited from the original single-page
// UI. Its home is the Overview (overview_page.go), served at both "/" and
// /products/{pid}/overview.
//
// Every signed-in page renders in that one chrome, from the one seam
// (renderShellStatus in templ_render.go), so the product switcher, the
// Needs-attention badge, and the toast host are never a per-page decision.
//
// The OAuth2 authorization-code + PKCE flow (discovery -> registration ->
// sign-in -> `/authorize` -> `/token`) must stay completable regardless of
// what the shell grows: every one of its pages sits behind
// app.auth.RequireAuthFunc, and none of them is on the OAuth2 path
// (auth.go's setupMCPAuth mounts that, plus the self-serve credential API).
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

// faviconIco is served at /favicon.ico, which htmxbase's layout links on
// every page. Embedded rather than served from disk so the image stays a
// pure-Go cross-compile with no asset pipeline.
//
// The directive is load-bearing: without it this stays nil and
// FaviconHandler serves a zero-byte image with a 200, which is harder to
// notice than a 404 but just as wrong. krill/ui/BUILD.bazel's embedsrcs
// makes the file a dep of this package.
//
//go:embed favicon.ico
var faviconIco []byte

// config holds `ui`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md).
type config struct {
	// Addr is the address this binary's HTTP surface listens on.
	Addr string

	// AuthMode is the HTTP-facing auth mode: "none" (dev-only, synthetic
	// dev-user -- see htmxauth.AuthModeNone) or "oidc" (real Keycloak
	// sign-in).
	AuthMode string

	// OIDC configuration (required when AuthMode == "oidc").
	OIDCIssuer string
	// RoleOperator/RoleReader (KRILL_ROLE_OPERATOR / KRILL_ROLE_READER) are
	// the realm roles that resolve to the operator and reader personas.
	RoleOperator     string
	RoleReader       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string

	// SessionSecret encrypts the DB-backed session store's access/refresh
	// tokens (htmxauth.NewDBSessionManager).
	SessionSecret string

	// DatabaseURL backs both htmxauth's DB-backed session manager
	// (ui_sessions table, migration 007) and auth's Postgres-backed
	// credential/client/auth-code stores (mcp_credential/mcp_oauth_client/
	// mcp_auth_code, migration 006) -- always required, never falls back
	// to cookie-only sessions, mirroring whagent_net/ui/main.go's config.
	DatabaseURL string

	// UIPublicURL is this binary's own externally-reachable base URL --
	// auth.ProviderConfig.Issuer, the base every auth endpoint URL
	// (`/authorize`, `/token`, `/register`,
	// `/.well-known/oauth-authorization-server`) is built from.
	UIPublicURL string

	// MCPPublicURL is `mcp`'s own externally-reachable base URL --
	// auth.ProviderConfig.Resource, the OAuth2 `resource` identifier.
	// Must be byte-identical to what `mcp` itself advertises
	// (KRILL_MCP_PUBLIC_URL, see krill/mcp/main.go) -- a mismatch breaks
	// an MCP client's RFC 9728 discovery chain.
	MCPPublicURL string

	// APIBaseURL is krill `api`'s own base URL, the target this binary's
	// app write client mints krill sessions against and issues every
	// mutating request to (writeclient.go). Required: a UI with no
	// configured `api` cannot attribute a write to a real operator
	// identity, so it refuses to boot rather than run write-less.
	APIBaseURL string

	// DevAPIToken (KRILL_DEV_API_TOKEN) is the static bearer forwarded to
	// api under AUTH_MODE=none; must equal api's KRILL_DEV_AUTH_TOKEN.
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

// App holds this binary's application state.
type App struct {
	auth *htmxauth.Authenticator

	// oidcIssuer is cfg.OIDCIssuer verbatim -- the fixed issuer every
	// signed-in operator's encoded identity carries (auth.go's
	// mcpCallerResolver).
	oidcIssuer string

	// devAPIToken is set only under AUTH_MODE=none; see requireOperator.
	devAPIToken string

	// devAuth is true under AUTH_MODE=none, where the synthetic dev user
	// holds the operator persona (as api's dev token does) so readerRoute
	// admits it; see readerRoute.
	devAuth bool

	// sessionRoles overrides how a request's realm roles are read; nil
	// means the signed-in user's session roles. Tests set it because the
	// cookie-backed test session does not persist roles (DB sessions do).
	sessionRoles func(r *http.Request) ([]string, error)

	// roles maps realm roles to personas at credential-mint time.
	roles server.RoleConfig

	// mcpProvider is auth's OAuth2 authorization-server front end,
	// constructed in NewApp and mounted on this binary's mux in
	// setupRoutes on unauthenticated routes (discovery metadata and
	// dynamic client registration must be reachable before an MCP client
	// has any credential at all). Its Resolver reads this binary's own
	// Keycloak session (mcpCallerResolver, auth.go) -- `/authorize`
	// mints a credential only once the operator is already signed in via
	// app.auth.
	mcpProvider *auth.Provider

	// credentials is the store behind both the self-serve JSON API and the
	// credentials page's htmx handlers (credentials_page.go).
	credentials auth.CredentialStore

	// writes is the client this binary's own app pages use to issue krill
	// writes (writeclient.go): it mints a krill session whose acting /
	// on-behalf-of subjects are the signed-in operator's real (iss, sub)
	// pair, then presents that session on every mutating request.
	writes *writeClient

	// scopes is the read-only `scope` view this binary uses to resolve the
	// scope a krill session is minted under (writes.go's withKrillSession)
	// -- a browser has no way to learn a scope id, and there is exactly
	// one, so GetSole is the whole of it.
	scopes store.ScopeStore

	// tasks is the console query surface the ops read views (ops.go) call
	// directly. Reads are role-gated (readerRoute) and the
	// views resolve the sole scope themselves, so -- unlike writes -- they
	// reach the same List* store methods the MCP ops mount and
	// GET /console/* serve, over the same store/paging.go pagination
	// contract, with no krill session in between.
	tasks store.TaskStore

	// designSessions and revisionEvents back the design-session read
	// surface (design_page.go): the exact store accessors the MCP tools'
	// get_design_session / list_open_questions call, reused directly so a
	// browser and an MCP client see one session, one ordering, and one
	// open-question derivation. A read carries no attribution, so -- unlike
	// app.writes -- it needs no krill session and reads the store in
	// process; the route's readerRoute gate mirrors api's reader check.
	designSessions store.DesignSessionStore
	revisionEvents store.RevisionEventStore

	// spec reads the spec axis (products, the capability map, decisions,
	// personas, non-goals, and the delivery/roadmap view) for the /spec
	// pages. Unlike writes it is not a session-attributed HTTP client:
	// reads are gated by readerRoute, and the reader calls the same //krill/slice.Querier
	// and //krill/store methods the MCP spec tools wrap, so a page and the
	// matching tool agree (see readclient.go). Held as the specReadClient
	// interface so the view assembly is testable against a fake.
	spec specReadClient

	// now is the clock the Overview's time-windowed figures are measured
	// from. Nil in production, where clock() reads the wall time; a test
	// sets it to hold time still and assert that a sub-line agrees with
	// the figure it is derived from.
	now func() time.Time
}

// NewApp wires up Keycloak sign-in and the auth OAuth2 provider. A
// failure here is always a startup-fatal condition -- see run()'s
// logger.Error call at the call site -- never a degrade-and-serve path.
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

	// NewDBSessionManager probes the ui_sessions table before returning; a
	// missing table (migration 007) fails boot here rather than at the
	// first sign-in.
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

	// initOIDC (inside NewAuthenticatorWithDB, oidc.NewProvider) performs
	// Keycloak discovery -- a failure here means the UI cannot start at
	// all, so the caller logs it at ERROR (AGENTS.md "Logging Levels").
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

	// auth.NewCredentialStore/NewPostgresClientRegistry/
	// NewPostgresAuthCodeStore each preflight their own table (migration
	// 006) and fail loudly, naming the table, if it hasn't been applied
	// yet -- exactly like htmxauth.NewDBSessionManager's ui_sessions probe
	// above.
	mcpProvider, credentials, err := setupMCPAuth(ctx, pool, cfg, app.mcpCallerResolver())
	if err != nil {
		return nil, fmt.Errorf("failed to initialize auth provider: %w", err)
	}
	app.mcpProvider = mcpProvider
	app.credentials = credentials

	// The write client is what this binary's own app pages call krill's
	// write API through; an unusable APIBaseURL is startup-fatal for the
	// same reason the two URLs above are.
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

// setupRoutes registers every route this binary serves. "/healthz" is the
// only unauthenticated app route; "/login", "/auth/callback", and
// "/logout" are the Keycloak sign-in flow's own public routes; every
// other app route requires a signed-in operator.
//
// The signed-in app surface is the workspace shell: "/{$}" is its home
// page and each nav area's own prefix is registered here (see nav.go's
// navGroupTable). The home page is registered as "/{$}" rather than the
// old catch-all "/" so an unknown path 404s instead of silently rendering
// the landing page.
//
// "/login" is this binary's chosen route name, but
// libs/go/htmxauth.Authenticator's RequireAuth/WithAccessToken hardcode
// their own unauthenticated-redirect target to "/auth/login" (not
// configurable), so "/auth/login" is registered as an alias for the exact
// same handler rather than moved or duplicated in logic (mirrors
// whagent_net/ui/main.go's setupRoutes).
// mountStaticRoutes registers the browser's unauthenticated static
// assets. Split out of setupRoutes so a test can mount the real
// registration -- newTestMux builds its own mux and only calls
// mountShellRoutes, so a route wired inline in setupRoutes is not
// reachable from any test, which is how the favicon shipped serving an
// empty 200 with nothing failing.
func mountStaticRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/favicon.ico", htmxbase.FaviconHandler(faviconIco))
}

func (app *App) setupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", handleHealthz)

	// htmxbase's layout links /favicon.ico by default; serving it from
	// the embedded bytes keeps every page from 404-ing a favicon it
	// asked for. Unauthenticated on purpose -- it is a static asset, and
	// gating it would mean a redirect loop's worth of state on a request
	// that carries none.
	mountStaticRoutes(mux)

	mux.HandleFunc("/login", app.auth.HandleLogin)
	mux.HandleFunc("/auth/login", app.auth.HandleLogin) // alias: see doc comment above.
	mux.HandleFunc("/auth/callback", app.auth.HandleCallback)
	mux.HandleFunc("/logout", app.auth.HandleLogout)

	// auth's OAuth2 authorization-server endpoints (/authorize, /token,
	// /register, and both discovery metadata documents) are registered
	// directly on mux here, outside app.auth.RequireAuth -- discovery and
	// dynamic client registration must be reachable before an MCP client
	// has any credential at all; /authorize itself is where
	// app.mcpProvider's own Resolver + SignInURL gate access to a
	// signed-in operator, not RequireAuth.
	app.mcpProvider.Mount(mux)

	// The self-serve credential API (POST/GET /credentials, DELETE
	// /credentials/{id}) lets an already-signed-in operator mint a static
	// bearer token for a non-OAuth2 MCP client (any harness that can't run
	// the authorization-code + PKCE dance) without ever needing DB access.
	// Gated the same way /authorize is -- app.mcpCallerResolver reads the
	// same session cookie -- so it is safe to leave unauthenticated at the
	// mux level; an unresolved caller gets a 401 from the handler itself.
	// MountSelfServe only errors on a nil Resolver, which setupMCPAuth
	// above never leaves unset, so a returned error here would be a
	// programming mistake, not a runtime condition -- panic is correct.
	if err := app.mcpProvider.MountSelfServe(mux); err != nil {
		panic(err)
	}

	// The mutating actions this binary's own app pages perform. Each is
	// mounted through operatorRoute, so a request without a signed-in
	// operator never reaches the handler at all; writes.go's
	// withKrillSession is then the only way any of them can reach krill,
	// and it attributes what it does to the operator requireOperator
	// resolved (LB4).
	mux.HandleFunc("POST /tasks/{id}/escalate", app.operatorRoute(app.handleEscalateTask))
	mux.HandleFunc("POST /design-sessions", app.operatorRoute(app.handleOpenDesignSession))

	// The console's four task interventions (interventions.go), one route per
	// verb, reached from a console view's row action forms. Each is mounted
	// through operatorRoute exactly like every other write here, so the
	// browser's submission is attributed to the signed-in operator's real
	// (iss, sub) by the same withKrillSession path, and forwarded to the same
	// krill api endpoint the ops-mount MCP tools drive.
	mux.HandleFunc("POST "+opsTaskActionBase+"{id}/"+actionRelease, app.operatorRoute(app.handleTaskIntervention(actionRelease)))
	mux.HandleFunc("POST "+opsTaskActionBase+"{id}/"+actionRequeue, app.operatorRoute(app.handleTaskIntervention(actionRequeue)))
	mux.HandleFunc("POST "+opsTaskActionBase+"{id}/"+actionEscalate, app.operatorRoute(app.handleTaskIntervention(actionEscalate)))
	mux.HandleFunc("POST "+opsTaskActionBase+"{id}/"+actionCancel, app.operatorRoute(app.handleTaskIntervention(actionCancel)))
	// Cancel is the one irreversible verb, so its row control is a link to
	// this confirmation page; nothing posts to the cancel route until the
	// operator confirms here.
	mux.HandleFunc("GET "+opsTaskActionBase+"{id}"+cancelConfirmSuffix, app.operatorRoute(app.handleCancelConfirm))

	// The signed-in shell: the home page plus one root per nav area,
	// every one of them rendered in the same chrome by
	// renderShellStatus. Each area's sub-pages register under its prefix
	// alongside its root.
	app.mountShellRoutes(mux)
}

// mountShellRoutes registers the workspace shell's pages, each behind
// the sign-in gate. Split out of setupRoutes so the shell's tests mount
// the same registrations production does, rather than a copy that could
// drift from it.
func (app *App) mountShellRoutes(mux *http.ServeMux) {
	// Every pre-redesign URL (FR 2544224c), from one table: the ops console,
	// the spec and delivery browser, the design-session browser and "/". Each
	// serves its existing page inside the shell until the phase that ships
	// that page's replacement moves it to a redirect. See legacyURLs.
	app.mountLegacyRoutes(mux)
	app.mountShellPages(mux)
}

// mountShellPages registers everything the shell serves that is not a
// pre-redesign URL: the credential widget, the design root's product
// browse and write surface, the product-scoped prefixes, and the Product
// switcher.
//
// It is split out of mountShellRoutes so a test can mount the real pages
// alongside a doctored copy of the legacy table -- the shape the phase
// that replaces a page will actually mount.
func (app *App) mountShellPages(mux *http.ServeMux) {
	mux.HandleFunc("GET "+credentialsPath, app.readerRoute(app.handleCredentials))
	mux.HandleFunc("POST "+credentialsMintPath, app.readerRoute(app.handleMintCredential))
	mux.HandleFunc("POST "+credentialsPath+"/{id}/revoke", app.readerRoute(app.handleRevokeCredential))

	// The design root's JS-free product browse: the operator types a
	// product id into a plain GET form and this 302s them to that
	// product's session list. It replaces a window.location script; a
	// read, so RequireAuthFunc and no operator identity. The id is
	// uuid.Parse'd before it reaches the path, so it is not a
	// user-controlled redirect target.
	mux.HandleFunc("GET "+designGoPath, app.readerRoute(app.handleDesignGo))

	// The design-session write surface (design_write.go), hung off the read
	// views in legacyURLs: the list page's "open a session" form and a session
	// detail page's "submit follow-up" form. Both are operatorRoute (RequireAuth
	// + requireOperator), so a write only ever proceeds with the signed-in
	// operator's real (iss, sub) resolved onto the request context, and both
	// reach krill only through withKrillSession. The open form posts to the
	// same product-scoped path as the list view (POST vs GET on one pattern);
	// the answer form posts to a sub-path of the detail route.
	mux.HandleFunc("POST /design/products/{productID}/design-sessions", app.operatorRoute(app.handleOpenDesignSessionForm))
	mux.HandleFunc("POST /design/design-sessions/{id}/answers", app.operatorRoute(app.handleDesignSessionAnswerForm))

	// The product-scoped prefixes (FR c4bd4bf8). Overview is the shell's
	// home and serves its real page; every other sub-path serves a
	// placeholder until its area's own page ships, so the current product
	// becomes resolvable and carried in the path without moving any
	// existing page. Registering a placeholder ahead of its content is safe
	// precisely because the handler resolves the {pid} against the
	// caller's scope first: an out-of-scope link is already an in-shell
	// 404 by the time the placeholder would render.
	//
	// The legacy prefixes above stay registered alongside these; the task
	// that retires them owns the redirects.
	mux.HandleFunc("GET "+productPathPrefix+overviewSuffix, app.readerRoute(app.handleProductOverview))
	mux.HandleFunc("GET "+productPathPrefix+needsAttentionSuffix, app.readerRoute(app.handleProductPlaceholder))
	// Tasks and Board are two views of one scope (FR ab5f4936): each has
	// its own handler, both over the product-wide task read layer
	// (product_task_scope.go, product_task_page.go).
	mux.HandleFunc("GET "+productPathPrefix+tasksSuffix, app.readerRoute(app.handleProductTasks))
	mux.HandleFunc("GET "+productPathPrefix+boardSuffix, app.readerRoute(app.handleProductBoard))
	mux.HandleFunc("GET "+productPathPrefix+milestonesSuffix, app.readerRoute(app.handleProductPlaceholder))
	// Milestone detail ids hang beneath the milestones prefix, so a copied
	// link to one resolves its product before the id is even looked at.
	mux.HandleFunc("GET "+productPathPrefix+milestonesSuffix+"/{mid}", app.readerRoute(app.handleProductPlaceholder))

	// The sidebar's Product switcher (FR c4bd4bf8). A reader route: it
	// reads the scope, records the pick as the last-viewed product, and
	// 302s to the same area's list page under it. It sits outside the
	// product prefixes because it names no product of its own -- it is
	// the hop between two.
	mux.HandleFunc("GET "+productSwitchPath, app.readerRoute(app.handleProductSwitch))
}

// operatorRoute is the wrapper every signed-in-operator route in this
// binary wears: RequireAuth first (an unauthenticated browser is sent to
// the Keycloak sign-in flow), then requireOperator, which resolves the
// operator's real (iss, sub) Subject onto the request context and rejects
// the request when it does not resolve. A handler mounted this way can
// always read a Subject, and can never be reached without one.
func (app *App) operatorRoute(next http.HandlerFunc) http.HandlerFunc {
	return app.auth.RequireAuthFunc(app.requireOperator(next))
}

// readerRoute is the wrapper every read page wears: RequireAuth, then a
// reader-or-operator persona check via the same RoleConfig.ResolvePersona
// api uses. A signed-in user holding neither role gets 403, matching api's
// reader gate. Under AUTH_MODE=none the synthetic dev user is admitted as
// an operator, the same identity api resolves for its dev token.
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
