// Command web is Audience Score System's HTMX/templ web app -- the only UI
// surface (NFR3), limited to Google OAuth sign-in/sign-up (C1, this task),
// Channel connect (C2, #1571), analyst invite/accept (C3), and
// schedule-draft approval (C8). See ../ARCHITECTURE.md's "NFR3 interface
// allocation" for why every other capability is MCP-only.
package main

import (
	"context"
	"crypto/sha256"
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
	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/logging"
)

// sessionName is both the DB session store's row-scoping key and the
// browser cookie name it derives from that argument, mirroring
// leaflab/ui's sessionName convention.
const sessionName = "ass_web_session"

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
}

// loadConfig loads configuration from environment variables. See
// ../ENV.md for the full variable list.
func loadConfig() config {
	return config{
		HTTPAddr:           getEnv("ASS_HTTP_ADDR", ":8080"),
		DatabaseURL:        os.Getenv("PG_DATABASE_URL"),
		LogLevel:           getEnv("LOG_LEVEL", "info"),
		GoogleClientID:     os.Getenv("ASS_GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("ASS_GOOGLE_CLIENT_SECRET"),
		OAuthRedirectBase:  os.Getenv("ASS_OAUTH_REDIRECT_BASE_URL"),
		SessionSecret:      os.Getenv("ASS_SESSION_SECRET"),
		TokenEncryptionKey: os.Getenv("ASS_TOKEN_ENCRYPTION_KEY"),
	}
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
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := loadConfig()

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
	channelHandler := channel.NewHandler(channel.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  cfg.OAuthRedirectBase + "/oauth/youtube/callback",
	}, st.Channels(), st.Roles(), tokenStore, sessions)

	// scheduleHandlers is C8's Creator-only approve/un-approve/edit surface
	// (#1580, FR19/FR20) -- needs only st (store.CanRead/store.CanApprove +
	// Schedules()/Roles()/Channels()), no separate OAuth grant of its own.
	scheduleHandlers := schedule.New(st)

	application := &app{store: st, auth: authenticator, invite: inviteHandlers, channels: channelHandler, schedule: scheduleHandlers}

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
	mux.HandleFunc("/login", a.auth.HandleLogin)
	mux.HandleFunc("/oauth/google/callback", a.auth.HandleCallback)
	mux.HandleFunc("POST /logout", a.auth.HandleLogout)

	// GET /invites/{code} is public (no session required, FR6/FR7/FR8) --
	// it must sit outside RequireSignedIn, since it renders differently
	// for a signed-in vs. anonymous caller rather than redirecting an
	// anonymous one away (see invite.Handlers.HandleShow).
	mux.HandleFunc("GET /invites/{code}", a.invite.HandleShow)

	// Protected: the signed-in landing page listing the Channels the
	// Person has a live channel_person row for (via store.RoleStore).
	mux.HandleFunc("/", a.auth.RequireSignedIn(a.handleHome))

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
	// (Creator only, via store.CanReconnect -- NFR5) the reconnect
	// affordance (#1571's Implementation section).
	mux.HandleFunc("GET /channels/{id}", a.auth.RequireSignedIn(a.handleChannelDetail))

	// Protected: schedule-draft approval (C8: FR19/FR20, #1580). GET is
	// Creator and Analyst both (store.CanRead); the three mutating POSTs
	// are Creator only (store.CanApprove, re-checked fresh inside each
	// handler -- see schedule.go's package doc comment for why hiding the
	// button client-side is never sufficient on its own).
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

func (a *app) handleHome(w http.ResponseWriter, r *http.Request) {
	person := auth.PersonFromContext(r.Context())
	data := components.LayoutData{
		Title: "Home",
		User:  person,
	}

	var channels []store.Channel
	if person != nil {
		var err error
		channels, err = a.store.Roles().ChannelsForPerson(r.Context(), person.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := renderTempl(w, r, "Home", pages.Home(data, channels)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleChannelDetail renders a Channel's connection state (connected /
// needs re-authentication) and, for a Creator only (store.CanReconnect --
// NFR5), the reconnect affordance (#1571's Implementation section).
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

	canReconnect, err := store.CanReconnect(r.Context(), a.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := components.LayoutData{
		Title: ch.Title,
		User:  person,
	}
	if err := renderTempl(w, r, ch.Title, pages.ChannelDetail(data, ch, canReconnect)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
