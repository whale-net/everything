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
//
// Scaffold only (issue #1571): every handler is a stub returning "not
// implemented" except plain field assignment in NewHandler. Real consent
// URL construction (access_type=offline, prompt=consent, the NFR1/LB1
// Scopes below, a state CSRF nonce bound to the session), code exchange,
// channels.list?mine=true resolution, FR4/NFR5's CanReconnect gate, and
// the needs-reauth lifecycle wiring land in this issue's Implementation
// phase -- mirroring web/auth's own Scaffold-then-Implementation split
// (issue #1570).
package channel

import (
	"net/http"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/tokens"
	"github.com/whale-net/everything/audience_score_system/web/auth"
)

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

// Handler drives Channel-connect (C2, FR3) and reconnect (FR4, NFR5): the
// /channels/connect and POST /channels/{id}/reconnect -> YouTube consent ->
// /oauth/youtube/callback flow.
type Handler struct {
	config   Config
	channels store.ChannelStore
	roles    store.RoleStore
	tokens   tokens.Store
	sessions *auth.SessionManager
}

// NewHandler wires config, the Channel/Role stores (store.ChannelStore/
// store.RoleStore -- CanReconnect in particular, FR4/NFR5), the token
// store (tokens.Store), and the already-established (C1) session manager
// into a Handler.
//
// Scaffold only: this constructor does plain field assignment and does
// not yet build the real *oauth2.Config (ClientID/ClientSecret/
// RedirectURL/Endpoint/Scopes) HandleConnect/HandleCallback/
// HandleReconnect need -- that, plus the oauth2Exchanger stub-in-tests
// abstraction (mirroring web/auth.oauth2Exchanger), lands in the
// Implementation phase.
func NewHandler(config Config, channels store.ChannelStore, roles store.RoleStore, tokenStore tokens.Store, sessions *auth.SessionManager) *Handler {
	return &Handler{
		config:   config,
		channels: channels,
		roles:    roles,
		tokens:   tokenStore,
		sessions: sessions,
	}
}

// HandleConnect starts the YouTube consent screen (FR3): access_type=
// offline and prompt=consent (needed to reliably get a refresh token even
// for a Person who has consented before), the NFR1/LB1 Scopes above, and a
// state CSRF nonce bound to the session.
//
// Stub only -- filled in during the Implementation phase.
func (h *Handler) HandleConnect(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleCallback exchanges the authorization code, resolves the YouTube
// Channel (channels.list?mine=true), and in one transaction either creates
// the Channel plus its role=creator channel_person row
// (store.ChannelStore.Create, FR3, LB2) or -- if the Channel already
// exists -- verifies store.CanReconnect and closes-and-opens the
// credential (tokens.Store.Save), then sets connection_state = connected.
//
// Stub only -- filled in during the Implementation phase.
func (h *Handler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleReconnect starts the same consent flow as HandleConnect for an
// existing Channel, but first requires store.CanReconnect(channelID,
// person) -- 403 with no state change (no credential row written) for
// anyone else, e.g. an Analyst with no live creator row (FR4, NFR5).
//
// Stub only -- filled in during the Implementation phase.
func (h *Handler) HandleReconnect(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
