// Command web is Audience Score System's HTMX/templ web app -- the only UI
// surface (NFR3), limited to Google OAuth sign-in/sign-up (C1, this task),
// Channel connect (C2, #1571), analyst invite/accept (C3), and
// schedule-draft approval (C8). See ../ARCHITECTURE.md's "NFR3 interface
// allocation" for why every other capability is MCP-only.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/tokens"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/channel"
	"github.com/whale-net/everything/audience_score_system/web/components"
	"github.com/whale-net/everything/audience_score_system/web/invite"
	"github.com/whale-net/everything/audience_score_system/web/pages"
	"github.com/whale-net/everything/audience_score_system/web/schedule"
	"github.com/whale-net/everything/audience_score_system/worker/sync"
	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/htmxbase"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/mcpauth"
	temporallib "github.com/whale-net/everything/libs/go/temporal"
)

// faviconIco is a pixel-art donkey face -- ASS's on-brand icon (audience
// _score_system). Generated as a real .ico (16x16 + 32x32 PNG-in-ICO,
// matching manmanv2/ui and tools/app_registry/ui's embed pattern), not
// hand-drawn.
//
//go:embed favicon.ico
var faviconIco []byte

// sessionName is both the DB session store's row-scoping key and the
// browser cookie name it derives from that argument, mirroring
// leaflab/ui's sessionName convention.
const sessionName = "ass_web_session"

// defaultSyncInterval is ASS_SYNC_INTERVAL's default -- 24 hours, inside
// sync.MinSyncInterval/MaxSyncInterval's 1-24 hour NFR4 band (widened from
// 3h, which still spent enough quota on the schedule-discovery search.list
// call per Channel to threaten YouTube's default daily project quota at
// M1's Channel count; use mcp's trigger_channel_sync tool for on-demand
// freshness between cycles). Must match worker/main.go's identical constant
// exactly (issue #1614's interval-consistency caveat -- see
// ../ARCHITECTURE.md "OAuth grants"): sync.ScheduleManager.EnsureSchedule
// bakes in whichever interval its first caller passes at schedule-creation
// time and never updates it on a later call, so `web` and `worker`
// diverging here would silently create schedules at different cadences
// depending on which binary connects a Channel first.
const defaultSyncInterval = 24 * time.Hour

// config holds `web`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md).
type config struct {
	HTTPAddr string

	DatabaseURL string
	LogLevel    string

	GoogleClientID     string
	GoogleClientSecret string
	OAuthRedirectBase  string
	SessionSecret      string
	TokenEncryptionKey string

	// MCPPublicURL is ASS_MCP_PUBLIC_URL (issue #1646, FR12/NFR4) -- the
	// externally reachable URL of the `mcp` server, passed as
	// mcpauth.ProviderConfig.Resource. `web` (this binary, the OAuth2
	// authorization server) and `mcp` (the OAuth2 protected resource) must
	// agree on this exact value -- see ../ENV.md.
	MCPPublicURL string

	// SyncInterval is FR14/NFR4's per-Channel Temporal sync cadence,
	// passed to sync.NewScheduleManager (issue #1614) -- see
	// defaultSyncInterval's doc comment above for why this must match
	// worker's identically-loaded value.
	SyncInterval time.Duration
}

// loadConfig loads configuration from environment variables, failing fast
// if ASS_SYNC_INTERVAL is set but unparseable or outside NFR4's 1-24 hour
// band (sync.ValidateSyncInterval) -- mirrors worker/main.go's
// loadConfig exactly, per issue #1614. See ../ENV.md for the full
// variable list.
func loadConfig() (config, error) {
	interval := defaultSyncInterval
	if raw := os.Getenv("ASS_SYNC_INTERVAL"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return config{}, fmt.Errorf("parse ASS_SYNC_INTERVAL %q: %w", raw, err)
		}
		interval = parsed
	}
	if err := sync.ValidateSyncInterval(interval); err != nil {
		return config{}, fmt.Errorf("ASS_SYNC_INTERVAL: %w", err)
	}

	return config{
		HTTPAddr:           getEnv("ASS_HTTP_ADDR", ":8080"),
		DatabaseURL:        os.Getenv("PG_DATABASE_URL"),
		LogLevel:           getEnv("LOG_LEVEL", "info"),
		GoogleClientID:     os.Getenv("ASS_GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("ASS_GOOGLE_CLIENT_SECRET"),
		OAuthRedirectBase:  os.Getenv("ASS_OAUTH_REDIRECT_BASE_URL"),
		SessionSecret:      os.Getenv("ASS_SESSION_SECRET"),
		TokenEncryptionKey: os.Getenv("ASS_TOKEN_ENCRYPTION_KEY"),
		MCPPublicURL:       os.Getenv("ASS_MCP_PUBLIC_URL"),
		SyncInterval:       interval,
	}, nil
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// app holds the wired-up application state.
type app struct {
	store    *store.Store
	auth     *auth.Authenticator
	invite   *invite.Handlers
	channels *channel.Handler
	schedule *schedule.Handlers

	// mcpProvider is mcpauth's OAuth2 authorization-server front end
	// (issue #1646, FR12/NFR4): /authorize, /token, /register, and
	// discovery metadata, mounted in setupRoutes. `web` hosts this because
	// it is the only process holding the caller's session cookie
	// (auth.MCPCallerResolver reads it); `mcp` hosts only the
	// protected-resource half. See ../ARCHITECTURE.md "MCP server: caller
	// authentication".
	mcpProvider *mcpauth.Provider
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logLevel := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	}

	logging.Configure(logging.Config{
		ServiceName:   "audience-score-system-web",
		Domain:        "audience-score-system",
		Level:         logLevel,
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	ctx := context.Background()
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	if cfg.DatabaseURL == "" {
		return fmt.Errorf("PG_DATABASE_URL is required")
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer pool.Close()

	st := store.New(pool)

	// TokenEncryptionKey is derived via SHA-256 from ASS_TOKEN_ENCRYPTION_KEY
	// so any operator-supplied secret (regardless of raw length) yields a
	// full 32-byte AES-256 key -- same derivation htmxauth.NewDBSessionManager
	// uses for its own encKey.
	encKey := sha256.Sum256([]byte(cfg.TokenEncryptionKey))

	sessions := auth.NewSessionManager(pool, sessionName, cfg.SessionSecret, encKey)
	authenticator, err := auth.NewAuthenticator(ctx, auth.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  cfg.OAuthRedirectBase + "/oauth/google/callback",
		SessionName:  sessionName,
	}, st.Persons(), sessions)
	if err != nil {
		return fmt.Errorf("failed to initialize Google OAuth2 authenticator: %w", err)
	}

	inviteHandlers := invite.New(st, sessions)

	// tokenStore is the Channel-connect grant's (C2, #1571) credential
	// store -- a SEPARATE token store from `sessions` above (C1's Google
	// sign-in session), sharing only the ASS_TOKEN_ENCRYPTION_KEY-derived
	// encKey. See ../ARCHITECTURE.md "OAuth grants".
	tokenStore := tokens.NewStore(pool, st.Channels(), encKey, tokens.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
	})

	// `web` constructs its own Temporal client and sync.ScheduleManager,
	// following worker/main.go's exact pattern -- FR14/NFR4, issue #1614:
	// so a Channel connected (or reconnected) while `worker` is already
	// running gets its Temporal sync schedule immediately, rather than
	// waiting for worker's next-startup Reconcile. `web` hard-depends on
	// Temporal being reachable at startup the same way it already
	// hard-depends on Postgres above -- fail fast rather than silently
	// degrade, since a `web` that starts but can never schedule a Channel
	// would be a confusing partial failure mode.
	temporalCfg := temporallib.ConfigFromEnv()
	if temporalCfg.TaskQueue == "" {
		temporalCfg.TaskQueue = sync.TaskQueue
	}
	logger.Info("connecting to temporal", "host_port", temporalCfg.HostPort, "namespace", temporalCfg.Namespace, "task_queue", temporalCfg.TaskQueue)
	temporalClient, err := temporallib.NewClient(temporalCfg, temporallib.NewLogger("audience-score-system-web"))
	if err != nil {
		return fmt.Errorf("connect to temporal: %w", err)
	}
	defer temporalClient.Close()

	scheduleManager := sync.NewScheduleManager(temporalClient.ScheduleClient(), st.Channels(), cfg.SyncInterval)

	channelHandler := channel.NewHandler(channel.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  cfg.OAuthRedirectBase + "/oauth/youtube/callback",
	}, st.Channels(), st.Roles(), tokenStore, sessions, scheduleManager)

	// scheduleHandlers is C8's Creator-tier (Founder or Co-Creator,
	// symmetrically per FR32) approve/un-approve/edit surface (#1580,
	// FR19/FR20) -- needs only st (store.CanRead/store.CanApprove +
	// Schedules()/Roles()/Channels()), no separate OAuth grant of its own.
	scheduleHandlers := schedule.New(st)

	// mcpauth's OAuth2 authorization-server front end (issue #1646,
	// FR12/NFR4): mints the bearer credential an MCP client presents to
	// `mcp`, reusing this Person's existing C1 Google-OIDC-backed session
	// (auth.MCPCallerResolver) rather than any new sign-in UI. `web` and
	// `mcp` share one Postgres, so a credential minted here is immediately
	// verifiable by `mcp` -- no cross-service call.
	//
	// The client registry and pending-authorization-code store MUST be the
	// Postgres-backed implementations, not mcpauth's in-memory defaults:
	// /authorize, /token, and /register can each land on a different `web`
	// replica.
	mcpClients, err := mcpauth.NewPostgresClientRegistry(ctx, mcpauth.ClientRegistryConfig{Pool: pool})
	if err != nil {
		return fmt.Errorf("mcpauth client registry: apply migration 007_mcpauth_oauth before starting web: %w", err)
	}
	mcpAuthCodes, err := mcpauth.NewPostgresAuthCodeStore(ctx, mcpauth.AuthCodeStoreConfig{Pool: pool})
	if err != nil {
		return fmt.Errorf("mcpauth auth code store: apply migration 007_mcpauth_oauth before starting web: %w", err)
	}
	// Same table/column configuration mcp/main.go uses for its own
	// mcpauth.NewCredentialStore -- one shared mcp_credential table
	// (migration 006), keyed on the Person UUID.
	mcpCredentials, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{
		Pool:           pool,
		TableName:      "mcp_credential",
		IdentityColumn: "person_id",
		IdentityCast:   "uuid",
	})
	if err != nil {
		return fmt.Errorf("mcpauth credential store: apply migration 006_mcpauth_credential before starting web: %w", err)
	}
	if cfg.MCPPublicURL == "" {
		return fmt.Errorf("ASS_MCP_PUBLIC_URL is required")
	}
	mcpProvider, err := mcpauth.NewProvider(mcpauth.ProviderConfig{
		Issuer:       cfg.OAuthRedirectBase,
		Resource:     cfg.MCPPublicURL,
		ResourceName: "Audience Score System MCP",
		Resolver:     authenticator.MCPCallerResolver(),
		Credentials:  mcpCredentials,
		Clients:      mcpClients,
		AuthCodes:    mcpAuthCodes,
		SignInURL:    "/login",
	})
	if err != nil {
		return fmt.Errorf("construct mcpauth provider: %w", err)
	}

	application := &app{store: st, auth: authenticator, invite: inviteHandlers, channels: channelHandler, schedule: scheduleHandlers, mcpProvider: mcpProvider}

	mux := http.NewServeMux()
	application.setupRoutes(mux)

	server := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      otelhttp.NewHandler(mux, "audience-score-system-web"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-shutdownCtx.Done()
	logger.Info("shutdown signal received, draining in-flight requests")

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(drainCtx); err != nil {
		logger.Warn("graceful shutdown did not complete cleanly", "error", err)
	}
	return nil
}

func (a *app) setupRoutes(mux *http.ServeMux) {
	// Public routes -- must sit outside RequireSignedIn, or the sign-in
	// flow itself would redirect back to /login.
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/favicon.ico", htmxbase.FaviconHandler(faviconIco))
	mux.HandleFunc("/login", a.auth.HandleLogin)
	mux.HandleFunc("/oauth/google/callback", a.auth.HandleCallback)
	mux.HandleFunc("POST /logout", a.auth.HandleLogout)

	// mcpauth's OAuth2 authorization-server endpoints (/authorize, /token,
	// /register, discovery metadata -- issue #1646, FR12/NFR4) also sit
	// outside RequireSignedIn: mcpauth's own Resolver + SignInURL do the
	// gating for /authorize (an unauthenticated request round-trips through
	// /login?next=<authorize URL> and back), and /token and /register are
	// called directly by the MCP client with no session cookie at all --
	// wrapping them in RequireSignedIn would break both.
	a.mcpProvider.Mount(mux)

	// GET /invites/{code} is public (no session required, FR6/FR7/FR8) --
	// it must sit outside RequireSignedIn, since it renders differently
	// for a signed-in vs. anonymous caller rather than redirecting an
	// anonymous one away (see invite.Handlers.HandleShow).
	mux.HandleFunc("GET /invites/{code}", a.invite.HandleShow)

	// Protected: the signed-in landing page. It carries no content of its
	// own -- it redirects straight to /channels, FR26's Channel list/
	// switcher, so there is exactly one place a signed-in Person lands on
	// and exactly one Channel list rendered (see handleChannels's doc
	// comment for why the old inline list here was retired, #1722).
	mux.HandleFunc("/", a.auth.RequireSignedIn(a.handleHome))

	// Protected: FR26's Channel list/switcher -- one row per Channel the
	// signed-in Person holds an open role on, plus FR25's "Connect
	// another Channel" entry point. See handleChannels's doc comment.
	mux.HandleFunc("GET /channels", a.auth.RequireSignedIn(a.handleChannels))

	// Analyst invite generate/accept/decline (C3: FR5-FR8, #1572). All
	// four require a session -- HandleGenerate additionally 403s unless
	// store.CanInvite holds for the caller (see invite.go).
	mux.HandleFunc("POST /channels/{id}/invites", a.auth.RequireSignedIn(a.invite.HandleGenerate))
	mux.HandleFunc("GET /invites/{code}/resume", a.auth.RequireSignedIn(a.invite.HandleResume))
	mux.HandleFunc("POST /invites/{code}/accept", a.auth.RequireSignedIn(a.invite.HandleAccept))
	mux.HandleFunc("POST /invites/{code}/decline", a.auth.RequireSignedIn(a.invite.HandleDecline))

	// Protected: Channel-connect OAuth grant (C2, #1571) -- a SEPARATE
	// consent from /login's Google sign-in above. All three sit behind
	// RequireSignedIn per that issue's Implementation section.
	mux.HandleFunc("GET /channels/connect", a.auth.RequireSignedIn(a.channels.HandleConnect))
	mux.HandleFunc("GET /oauth/youtube/callback", a.auth.RequireSignedIn(a.channels.HandleCallback))
	mux.HandleFunc("POST /channels/{id}/reconnect", a.auth.RequireSignedIn(a.channels.HandleReconnect))

	// Protected: Channel detail -- shows connected/needs-reauth state, and
	// (Creator-tier -- Founder or Co-Creator, symmetrically per FR32, via
	// store.CanReconnect -- NFR5) the reconnect affordance (#1571's
	// Implementation section).
	mux.HandleFunc("GET /channels/{id}", a.auth.RequireSignedIn(a.handleChannelDetail))

	// Protected: schedule-draft approval (C8: FR19/FR20, #1580). GET is
	// Founder, Co-Creator, and Analyst all (store.CanRead); the three
	// mutating POSTs require Creator-tier authority -- Founder or
	// Co-Creator, symmetrically per FR32 (store.CanApprove, re-checked
	// fresh inside each handler -- see schedule.go's package doc comment
	// for why hiding the button client-side is never sufficient on its
	// own).
	mux.HandleFunc("GET /channels/{id}/schedule", a.auth.RequireSignedIn(a.schedule.HandleList))
	mux.HandleFunc("POST /schedule/{entryID}/approve", a.auth.RequireSignedIn(a.schedule.HandleApprove))
	mux.HandleFunc("POST /schedule/{entryID}/unapprove", a.auth.RequireSignedIn(a.schedule.HandleUnapprove))
	mux.HandleFunc("POST /schedule/{entryID}/edit", a.auth.RequireSignedIn(a.schedule.HandleEdit))
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}

// handleHome is the signed-in landing page. It renders nothing itself --
// see handleChannels's doc comment for why "/" is a bare redirect to
// /channels rather than a second, competing Channel list (#1722).
func (a *app) handleHome(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/channels", http.StatusSeeOther)
}

// handleChannels is FR26's Channel list/switcher: one row per Channel the
// signed-in Person holds an open role on, each showing the Channel's
// title, the Person's tier (pages.tierLabel), and connection state --
// sourced from exactly one store call, store.AccessStore.
// ChannelsWithRoleForPerson (#1716), so the query count does not grow
// with Channel count (NFR9). This replaces M1's inline list on the "/"
// home page (store.RoleStore.ChannelsForPerson) as the one place a
// signed-in Person sees every Channel they're associated with -- "/" now
// just redirects here (handleHome above).
//
// This handler introduces no session-held "current channel" and sets no
// cookie: clicking a row navigates straight to that Channel's existing
// GET /channels/{id} detail page (handleChannelDetail below), which
// re-derives its own authorization from the {id} path segment alone. See
// pages.Channels's doc comment for the FR25 "Connect another Channel"
// visibility rule this handler computes (showConnect).
func (a *app) handleChannels(w http.ResponseWriter, r *http.Request) {
	person := auth.PersonFromContext(r.Context())
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	rows, err := a.store.Access().ChannelsWithRoleForPerson(r.Context(), person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// showConnect (FR25): hidden only when every open role this Person
	// holds is Analyst -- the strict reading of #1709's Analyst persona
	// text ("no Connect another Channel action"). A Person with zero
	// rows still sees it, via pages.Channels's empty state.
	showConnect := true
	if len(rows) > 0 {
		showConnect = false
		for _, row := range rows {
			if row.Role != store.RoleAnalyst {
				showConnect = true
				break
			}
		}
	}

	data := components.LayoutData{
		Title: "Channels",
		User:  person,
	}
	if err := renderTempl(w, r, "Channels", pages.Channels(data, rows, showConnect)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleChannelDetail renders a Channel's connection state (connected /
// needs re-authentication) and, for a Founder or Co-Creator (Creator-tier,
// symmetrically per FR32, store.CanReconnect -- NFR5), the reconnect
// affordance (#1571's Implementation section) and the invite-analyst
// affordance (store.CanInvite, C3/FR5).
func (a *app) handleChannelDetail(w http.ResponseWriter, r *http.Request) {
	person := auth.PersonFromContext(r.Context())
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	ch, err := a.store.Channels().GetByID(r.Context(), channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Re-authorize strictly by this Channel's ID (store.CanRead), never by
	// anything the Channel list/switcher (handleChannels above) might have
	// implied -- that page introduces no session-held "current channel"
	// and grants nothing on its own (FR26, #1722).
	canRead, err := store.CanRead(r.Context(), a.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canRead {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	canReconnect, err := store.CanReconnect(r.Context(), a.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	canInvite, err := store.CanInvite(r.Context(), a.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := components.LayoutData{
		Title: ch.Title,
		User:  person,
	}
	if err := renderTempl(w, r, ch.Title, pages.ChannelDetail(data, ch, canReconnect, canInvite)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
