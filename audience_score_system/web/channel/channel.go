// Package channel is `web`'s YouTube Channel-connect OAuth grant (C2: FR3,
// FR4, NFR1) -- GET /channels/connect, GET /oauth/youtube/callback, and
// POST /channels/{id}/reconnect, all mounted behind
// web/auth.Authenticator.RequireSignedIn (see ../main.go's setupRoutes).
//
// This is a SEPARATE OAuth grant from web/auth's Google sign-in (C1): C1
// authenticates a Person via openid/email/profile; this package authorizes
// this app to call the YouTube Data/Analytics APIs on a specific Channel's
// behalf, storing the resulting token via
// //audience_score_system/tokens.Store. See ../../ARCHITECTURE.md "OAuth
// grants" for why these stay two distinct consents rather than one
// combined scope request at sign-in.
package channel

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	youtube "google.golang.org/api/youtube/v3"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/tokens"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/libs/go/logging"
)

var logger = logging.Get("audience_score_system/web/channel")

// Scopes is the NFR1/LB1 scope set requested at first Channel-connect
// consent -- see ../../ENV.md "OAuth scopes" for the full LB1 tradeoff
// writeup this decision resolves:
//
//   - yt-analytics.readonly: owner-level YouTube Analytics access,
//     requested up front even though M1 only surfaces
//     views/retention/CTR/impressions, so a later milestone (C16) never
//     forces a re-consent of every connected Creator (LB1).
//   - youtube.readonly: YouTube Data API access sufficient to list a
//     Channel's own scheduled/private draft uploads (C6) -- verified
//     against current Google OAuth scope docs
//     (developers.google.com/identity/protocols/oauth2/scopes) to resolve
//     private/scheduled uploads via the authenticated owner's uploads
//     playlist or search.list?forMine=true, both of which only require
//     this scope, not the broader `youtube` (manage) scope.
//
// yt-analytics-monetary.readonly is deliberately excluded: M1's
// store.VideoMetrics has no monetary field, and adding one later is
// exactly the re-consent cost LB1 exists to avoid paying twice -- if a
// monetary field is ever wanted, that is its own scope-and-re-consent
// decision, not an assumption to bake in now.
var Scopes = []string{
	"https://www.googleapis.com/auth/yt-analytics.readonly",
	"https://www.googleapis.com/auth/youtube.readonly",
}

// Config holds Channel-connect OAuth configuration, read from the same
// ASS_GOOGLE_CLIENT_ID/ASS_GOOGLE_CLIENT_SECRET/
// ASS_OAUTH_REDIRECT_BASE_URL as web/auth.Config (see ../../ENV.md "OAuth
// scopes") -- this grant reuses the same Google OAuth2 client, just a
// different redirect path and a YouTube-scoped consent, never a second
// client ID/secret pair.
type Config struct {
	ClientID     string
	ClientSecret string

	// RedirectURL is ASS_OAUTH_REDIRECT_BASE_URL + "/oauth/youtube/callback"
	// -- distinct from web/auth.Config.RedirectURL's
	// "/oauth/google/callback", so Google's consent screen and callback
	// routing can tell the two grants apart.
	RedirectURL string
}

// oauth2Exchanger is the subset of *oauth2.Config's behavior HandleConnect/
// HandleReconnect/HandleCallback depend on. *oauth2.Config satisfies this
// implicitly; tests can substitute a stub exchanger so no handler test
// makes a live call to Google -- mirrors web/auth.oauth2Exchanger.
type oauth2Exchanger interface {
	AuthCodeURL(state string, opts ...oauth2.AuthCodeOption) string
	Exchange(ctx context.Context, code string, opts ...oauth2.AuthCodeOption) (*oauth2.Token, error)
}

// channelResolver is the subset of behavior HandleCallback needs to
// resolve the connecting Person's own YouTube channel -- factored out so
// tests can substitute a stub without a live call to the YouTube Data API,
// mirroring oauth2Exchanger above. The default implementation
// (youtubeChannelResolver) calls channels.list?mine=true, which -- per
// Scopes' doc comment -- only requires the youtube.readonly scope granted
// above, never the broader `youtube` (manage) scope.
type channelResolver interface {
	// ResolveOwnChannel returns the youtube_channel_id and title of the
	// YouTube channel owned by the account tok authenticates, or an error
	// if the account has no channel (e.g. a brand-new Google account that
	// never created one).
	ResolveOwnChannel(ctx context.Context, tok *oauth2.Token) (youtubeChannelID, title string, err error)
}

// youtubeChannelResolver is channelResolver's real, YouTube-Data-API-backed
// implementation.
// scheduleManager is the subset of sync.ScheduleManager's behavior
// HandleCallback needs -- *sync.scheduleManager (via sync.NewScheduleManager)
// satisfies this implicitly. Factored out so tests can substitute a stub,
// mirroring oauth2Exchanger/channelResolver above -- this package
// deliberately does NOT import worker/sync's package for the interface
// itself, only main.go constructs the real implementation and passes it in
// through this narrow interface (see ../main.go).
type scheduleManager interface {
	EnsureSchedule(ctx context.Context, channelID uuid.UUID) error
}

type youtubeChannelResolver struct{}

func (youtubeChannelResolver) ResolveOwnChannel(ctx context.Context, tok *oauth2.Token) (string, string, error) {
	svc, err := youtube.NewService(ctx, option.WithTokenSource(oauth2.StaticTokenSource(tok)))
	if err != nil {
		return "", "", fmt.Errorf("create youtube service: %w", err)
	}

	resp, err := svc.Channels.List([]string{"snippet"}).Mine(true).Context(ctx).Do()
	if err != nil {
		return "", "", fmt.Errorf("channels.list mine=true: %w", err)
	}
	if len(resp.Items) == 0 {
		return "", "", fmt.Errorf("no YouTube channel found for the authenticated Google account")
	}

	item := resp.Items[0]
	title := ""
	if item.Snippet != nil {
		title = item.Snippet.Title
	}
	return item.Id, title, nil
}

// Handler drives Channel-connect (C2, FR3) and reconnect (FR4, NFR5): the
// /channels/connect and POST /channels/{id}/reconnect -> YouTube consent ->
// /oauth/youtube/callback flow.
type Handler struct {
	config   Config
	channels store.ChannelStore
	roles    store.RoleStore
	tokens   tokens.Store
	sessions *auth.SessionManager

	oauth2Config oauth2Exchanger
	resolver     channelResolver
	schedules    scheduleManager
}

// NewHandler wires config, the Channel/Role stores (store.ChannelStore/
// store.RoleStore -- CanReconnect in particular, FR4/NFR5), the token
// store (tokens.Store), the already-established (C1) session manager, and
// a scheduleManager (../main.go constructs the real
// sync.NewScheduleManager, following worker/main.go's exact Temporal
// client pattern -- FR14/NFR4, issue #1614) into a Handler, building the
// real *oauth2.Config (google.Endpoint, the NFR1/LB1 Scopes above)
// HandleConnect/HandleReconnect/HandleCallback use.
func NewHandler(config Config, channels store.ChannelStore, roles store.RoleStore, tokenStore tokens.Store, sessions *auth.SessionManager, schedules scheduleManager) *Handler {
	return &Handler{
		config:   config,
		channels: channels,
		roles:    roles,
		tokens:   tokenStore,
		sessions: sessions,
		oauth2Config: &oauth2.Config{
			ClientID:     config.ClientID,
			ClientSecret: config.ClientSecret,
			RedirectURL:  config.RedirectURL,
			Endpoint:     google.Endpoint,
			Scopes:       Scopes,
		},
		resolver:  youtubeChannelResolver{},
		schedules: schedules,
	}
}

// HandleConnect starts the YouTube consent screen (FR3): access_type=
// offline and prompt=consent (needed to reliably get a refresh token even
// for a Person who has consented before), the NFR1/LB1 Scopes above, and a
// state CSRF nonce bound to the session.
func (h *Handler) HandleConnect(w http.ResponseWriter, r *http.Request) {
	h.startConsent(w, r, "/")
}

// HandleReconnect starts the same consent flow as HandleConnect for an
// existing Channel, but first requires store.CanReconnect(channelID,
// person) -- 403 with no state change (no credential row written, no OAuth
// state cookie set) for anyone else, e.g. an Analyst with no live creator
// row (FR4, NFR5).
func (h *Handler) HandleReconnect(w http.ResponseWriter, r *http.Request) {
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

	ok, err := store.CanReconnect(r.Context(), h.roles, channelID, person.ID)
	if err != nil {
		http.Error(w, "authorization check failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "forbidden: only a Channel's creator may reconnect it", http.StatusForbidden)
		return
	}

	h.startConsent(w, r, "/channels/"+channelID.String())
}

// startConsent generates a CSRF state nonce, binds it (and nextURL, the
// post-callback redirect target) to the session via
// SessionManager.SetOAuthState, and redirects to Google's consent screen
// with access_type=offline + prompt=consent + Scopes.
func (h *Handler) startConsent(w http.ResponseWriter, r *http.Request, nextURL string) {
	state, err := generateState()
	if err != nil {
		http.Error(w, "failed to generate state", http.StatusInternalServerError)
		return
	}
	if err := h.sessions.SetOAuthState(w, r, state, nextURL); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}

	authURL := h.oauth2Config.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleCallback exchanges the authorization code, resolves the YouTube
// Channel (channels.list?mine=true), and either creates the Channel plus
// its role=creator channel_person row (store.ChannelStore.Create, FR3,
// LB2) or -- if the Channel already exists -- verifies store.CanReconnect
// and closes-and-opens the credential (tokens.Store.Save), then sets
// connection_state = connected.
func (h *Handler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	person := auth.PersonFromContext(ctx)
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	state := r.URL.Query().Get("state")
	valid, err := h.sessions.VerifyOAuthState(r, state)
	if err != nil || !valid {
		http.Error(w, "invalid state parameter", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	tok, err := h.oauth2Config.Exchange(ctx, code)
	if err != nil {
		http.Error(w, "failed to exchange token", http.StatusInternalServerError)
		return
	}
	if tok.RefreshToken == "" {
		// access_type=offline + prompt=consent should always yield one on
		// a fresh consent. Without it tokens.Store.TokenSource could never
		// refresh past the first access-token expiry, so fail loudly here
		// rather than silently persisting a credential doomed to
		// needs-reauth the moment it expires.
		http.Error(w, "no refresh token returned by Google -- retry the connect flow", http.StatusInternalServerError)
		return
	}

	youtubeChannelID, title, err := h.resolver.ResolveOwnChannel(ctx, tok)
	if err != nil {
		// The underlying cause (no channel on the Google account, YouTube
		// Data API not enabled on the project, insufficient/partially
		// granted scope, quota) is otherwise invisible: the response the
		// browser sees is always this same generic 500.
		logger.Error("failed to resolve YouTube channel", "person_id", person.ID, "error", err)
		http.Error(w, "failed to resolve YouTube channel", http.StatusInternalServerError)
		return
	}

	existing, err := h.channels.GetByYouTubeChannelID(ctx, youtubeChannelID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "failed to look up channel", http.StatusInternalServerError)
			return
		}

		// Not found -- fresh connect (FR3): create the Channel (its
		// role=creator row for person.ID is granted atomically inside
		// Create) before saving the credential.
		ch, err := h.channels.Create(ctx, youtubeChannelID, title, person.ID)
		if err != nil {
			http.Error(w, "failed to create channel", http.StatusInternalServerError)
			return
		}
		if err := h.tokens.Save(ctx, ch.ID, person.ID, tok, Scopes); err != nil {
			// Create and Save are not one low-level SQL transaction (each
			// store owns its own), so compensate here rather than leave a
			// Channel row claiming connection_state=connected with no
			// live credential behind it: flip it to needs_reauth so the
			// UI's reconnect affordance can recover it (FR4).
			_ = h.tokens.MarkNeedsReauth(ctx, ch.ID, "initial credential save failed")
			http.Error(w, "failed to save credential", http.StatusInternalServerError)
			return
		}

		// Best-effort, non-fatal (FR14/NFR4, issue #1614): the Channel is
		// already correctly connected in Postgres at this point, so a
		// transient Temporal hiccup here must degrade to "worker's next
		// startup Reconcile will pick it up" (previous behavior), not turn
		// an otherwise-successful connect into a 500 for the user.
		// EnsureSchedule is idempotent (deterministic sync.ScheduleID), so
		// this races safely against worker's own Reconcile/EnsureSchedule
		// call for the same Channel.
		if err := h.schedules.EnsureSchedule(ctx, ch.ID); err != nil {
			logger.Warn("failed to ensure temporal sync schedule after connect", "channel_id", ch.ID, "error", err)
		}

		http.Redirect(w, r, h.sessions.GetNextURL(w, r), http.StatusSeeOther)
		return
	}

	// Found -- reconnect path (FR4, NFR5): re-verify CanReconnect here too
	// (not just in HandleReconnect) so a forged-but-otherwise-valid state
	// parameter can never bypass the creator-only gate.
	ok, err := store.CanReconnect(ctx, h.roles, existing.ID, person.ID)
	if err != nil {
		http.Error(w, "authorization check failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "forbidden: only a Channel's creator may reconnect it", http.StatusForbidden)
		return
	}

	if err := h.tokens.Save(ctx, existing.ID, person.ID, tok, Scopes); err != nil {
		http.Error(w, "failed to save credential", http.StatusInternalServerError)
		return
	}
	if err := h.channels.SetConnectionState(ctx, existing.ID, store.ConnectionStateConnected); err != nil {
		http.Error(w, "failed to update connection state", http.StatusInternalServerError)
		return
	}

	// Best-effort, non-fatal -- see the fresh-connect branch above for the
	// full rationale (FR14/NFR4, issue #1614).
	if err := h.schedules.EnsureSchedule(ctx, existing.ID); err != nil {
		logger.Warn("failed to ensure temporal sync schedule after reconnect", "channel_id", existing.ID, "error", err)
	}

	http.Redirect(w, r, h.sessions.GetNextURL(w, r), http.StatusSeeOther)
}

// generateState returns a random base64url-encoded OAuth2 CSRF state nonce
// -- mirrors web/auth's own generateState (copied, not shared, per that
// package's doc comment about this domain's mechanics living locally
// rather than in a shared package).
func generateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
