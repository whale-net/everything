// Package tokens is per-Channel OAuth credential storage and refresh for
// the YouTube Channel-connect grant (C2, issue #1571) --
// `channel_credential` (migration 004,
// migrate/schema/migrations/004_channel_credentials.up.sql). This is a
// SEPARATE token store from web/auth.SessionManager's web_session.
// refresh_token (C1's Google sign-in refresh token): that one refreshes
// this app's own sign-in session; this one refreshes the Channel-scoped
// YouTube Data/Analytics token web/channel and (later) mcp/worker read
// through Store.TokenSource. See ../ARCHITECTURE.md "OAuth grants" for why
// these stay two separate consents.
//
// Store is the ONLY sanctioned way any M1 component reads or refreshes a
// Channel's YouTube token -- no handler, tool, or workflow outside this
// package may read channel_credential directly, mirroring
// audience_score_system/store/authz.go's Can* convention for
// authorization.
//
// Scaffold only (issue #1571): every Store method is a stub returning
// errNotImplemented. AES-256-GCM encryption at rest and SELECT ... FOR
// UPDATE single-flight refresh -- mechanics copied from
// web/auth/session.go and libs/go/htmxauth.DBSessionManager, per those
// packages' own doc comments about why this is copied rather than shared
// -- land in the Implementation phase, along with the invalid_grant ->
// MarkNeedsReauth mapping (FR4) and the distinction from a transient
// network/5xx failure (which must NOT trip needs-reauth).
package tokens

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/whale-net/everything/audience_score_system/store"
)

// Store is per-Channel OAuth credential storage and refresh
// (`channel_credential`, migration 004).
type Store interface {
	// TokenSource returns an oauth2.TokenSource for channelID that
	// transparently refreshes an expired access token (single-flight via
	// SELECT ... FOR UPDATE on the live credential row, so concurrent
	// worker activities don't race and burn the refresh token) and
	// persists the refreshed token before returning it. A refresh failure
	// that indicates revocation (invalid_grant) calls MarkNeedsReauth
	// internally; a transient network/5xx failure must not.
	TokenSource(ctx context.Context, channelID uuid.UUID) (oauth2.TokenSource, error)

	// Save persists tok for channelID, following the SCD2 close-and-open
	// pattern from AGENTS.md "SCD2": closes the prior live credential (if
	// any) and opens a new one, recording byPersonID (FR3/FR4) and
	// scopes.
	Save(ctx context.Context, channelID, byPersonID uuid.UUID, tok *oauth2.Token, scopes []string) error

	// MarkNeedsReauth records that channelID's credential can no longer be
	// refreshed (reason is a short machine-readable cause, e.g.
	// "invalid_grant" -- logged for audit, not stored on
	// channel_credential, which has no reason column) and sets
	// channel.connection_state = needs_reauth (FR4). It does NOT delete
	// the credential row or any previously synced data (FR4's retention
	// requirement) -- Implementation must not add a delete here.
	MarkNeedsReauth(ctx context.Context, channelID uuid.UUID, reason string) error
}

// tokenStore implements Store against `channel_credential` (migration
// 004).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1571's "Implementation" scope).
type tokenStore struct {
	pool     *pgxpool.Pool
	channels store.ChannelStore
	encKey   [32]byte
}

var _ Store = tokenStore{}

// NewStore returns a Store backed by pool, using channels to flip
// connection_state on MarkNeedsReauth (FR4), and encKey (derived from
// ASS_TOKEN_ENCRYPTION_KEY the same way web/auth.SessionManager derives
// its own encKey -- see ../ENV.md "OAuth scopes") to encrypt/decrypt token
// ciphertext at rest.
func NewStore(pool *pgxpool.Pool, channels store.ChannelStore, encKey [32]byte) Store {
	return tokenStore{pool: pool, channels: channels, encKey: encKey}
}

func (s tokenStore) TokenSource(ctx context.Context, channelID uuid.UUID) (oauth2.TokenSource, error) {
	return nil, errNotImplemented
}

func (s tokenStore) Save(ctx context.Context, channelID, byPersonID uuid.UUID, tok *oauth2.Token, scopes []string) error {
	return errNotImplemented
}

func (s tokenStore) MarkNeedsReauth(ctx context.Context, channelID uuid.UUID, reason string) error {
	return errNotImplemented
}

var errNotImplemented = notImplementedError{}

// notImplementedError is a trivial sentinel distinct from errors.New so
// every stub above returns the exact same comparable value (mirrors
// web/auth/session.go's errNotImplemented).
type notImplementedError struct{}

func (notImplementedError) Error() string { return "not implemented" }
