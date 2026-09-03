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
package tokens

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/whale-net/everything/audience_score_system/store"
)

// refreshSkew is how far ahead of access_token_expires_at Store.token
// proactively refreshes -- mirrors htmxauth.DBSessionManager.GetAccessToken's
// 2-minute skew.
const refreshSkew = 2 * time.Minute

// defaultTokenTTL is the fallback expiry Store.Save/Store.token assumes when
// Google's response omits Expiry (oauth2.Token.Expiry can be the zero
// value) -- mirrors htmxauth's 5-minute fallback.
const defaultTokenTTL = 5 * time.Minute

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
	// requirement).
	MarkNeedsReauth(ctx context.Context, channelID uuid.UUID, reason string) error
}

// Config holds the Google OAuth2 client credentials
// (ASS_GOOGLE_CLIENT_ID/ASS_GOOGLE_CLIENT_SECRET) Store needs to refresh an
// expired access token against Google's token endpoint. This is the SAME
// client used for both the C1 sign-in grant and the C2 Channel-connect
// grant (see web/channel.Config's doc comment) -- never a second client
// ID/secret pair.
type Config struct {
	ClientID     string
	ClientSecret string
}

// refresher is the subset of *oauth2.Config's behavior Store depends on to
// refresh an expired access token -- factored out so tests can substitute a
// stub that never makes a live call to Google, mirroring
// web/auth.oauth2Exchanger.
type refresher interface {
	TokenSource(ctx context.Context, t *oauth2.Token) oauth2.TokenSource
}

// tokenStore implements Store against `channel_credential` (migration
// 004).
type tokenStore struct {
	pool      *pgxpool.Pool
	channels  store.ChannelStore
	encKey    [32]byte
	refresher refresher
}

var _ Store = tokenStore{}

// NewStore returns a Store backed by pool, using channels to flip
// connection_state on MarkNeedsReauth (FR4), encKey (derived from
// ASS_TOKEN_ENCRYPTION_KEY the same way web/auth.SessionManager derives its
// own encKey -- see ../ENV.md "OAuth scopes") to encrypt/decrypt token
// ciphertext at rest, and cfg's Google OAuth2 client credentials to refresh
// an expired access token against Google's token endpoint
// (golang.org/x/oauth2/google.Endpoint).
func NewStore(pool *pgxpool.Pool, channels store.ChannelStore, encKey [32]byte, cfg Config) Store {
	return tokenStore{
		pool:     pool,
		channels: channels,
		encKey:   encKey,
		refresher: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     google.Endpoint,
		},
	}
}

// channelTokenSource adapts tokenStore.token to oauth2.TokenSource.
type channelTokenSource struct {
	ctx       context.Context
	channelID uuid.UUID
	store     tokenStore
}

func (c channelTokenSource) Token() (*oauth2.Token, error) {
	return c.store.token(c.ctx, c.channelID)
}

func (s tokenStore) TokenSource(ctx context.Context, channelID uuid.UUID) (oauth2.TokenSource, error) {
	return channelTokenSource{ctx: ctx, channelID: channelID, store: s}, nil
}

// token resolves the current access token for channelID. If the stored
// access token is not within refreshSkew of expiry it is returned as-is;
// otherwise it is refreshed against Google's token endpoint and the
// refreshed credential is persisted before returning.
//
// The whole read-maybe-refresh-write sequence runs inside one transaction
// with `SELECT ... FOR UPDATE` on the live credential row, so two
// concurrent callers for the same Channel serialize on the row lock: the
// second caller's SELECT blocks until the first's transaction commits, and
// then observes the now-fresh row (no second refresh, no burned refresh
// token).
func (s tokenStore) token(ctx context.Context, channelID uuid.UUID) (*oauth2.Token, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id uuid.UUID
	var accessCiphertext, refreshCiphertext []byte
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, access_token_ciphertext, refresh_token_ciphertext, access_token_expires_at
		FROM channel_credential
		WHERE channel_id = $1 AND valid_to IS NULL
		FOR UPDATE
	`, channelID).Scan(&id, &accessCiphertext, &refreshCiphertext, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("no live credential for channel %s", channelID)
		}
		return nil, fmt.Errorf("query credential: %w", err)
	}

	accessToken, err := s.decrypt(accessCiphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token: %w", err)
	}

	if time.Until(expiresAt) > refreshSkew {
		// Token still has plenty of life left -- no refresh needed. Roll
		// back explicitly (rather than waiting for the deferred rollback)
		// so the row lock is released immediately.
		tx.Rollback(ctx) //nolint:errcheck
		return &oauth2.Token{AccessToken: accessToken, Expiry: expiresAt, TokenType: "Bearer"}, nil
	}

	refreshToken, err := s.decrypt(refreshCiphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt refresh token: %w", err)
	}

	newTok, err := s.refresher.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken}).Token()
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		if isInvalidGrant(err) {
			if markErr := s.MarkNeedsReauth(ctx, channelID, "invalid_grant"); markErr != nil {
				return nil, fmt.Errorf("refresh failed (invalid_grant), and marking needs-reauth also failed: %w", markErr)
			}
			return nil, fmt.Errorf("channel %s needs re-authentication: %w", channelID, err)
		}
		// Transient network/5xx failure -- do NOT call MarkNeedsReauth
		// (FR4): connection_state stays "connected" and the caller should
		// retry later.
		return nil, fmt.Errorf("refresh access token (retryable): %w", err)
	}

	// Google does not always return a new refresh token on refresh --
	// keep using the existing one when it doesn't.
	newRefreshToken := newTok.RefreshToken
	if newRefreshToken == "" {
		newRefreshToken = refreshToken
	}

	newAccessCiphertext, err := s.encrypt(newTok.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("encrypt refreshed access token: %w", err)
	}
	newRefreshCiphertext, err := s.encrypt(newRefreshToken)
	if err != nil {
		return nil, fmt.Errorf("encrypt refreshed refresh token: %w", err)
	}

	newExpiry := newTok.Expiry
	if newExpiry.IsZero() {
		newExpiry = time.Now().Add(defaultTokenTTL)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE channel_credential
		SET access_token_ciphertext = $1, refresh_token_ciphertext = $2, access_token_expires_at = $3
		WHERE id = $4
	`, newAccessCiphertext, newRefreshCiphertext, newExpiry, id); err != nil {
		return nil, fmt.Errorf("persist refreshed credential: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit refreshed credential: %w", err)
	}

	return &oauth2.Token{AccessToken: newTok.AccessToken, Expiry: newExpiry, TokenType: "Bearer"}, nil
}

// isInvalidGrant reports whether err is Google's invalid_grant response --
// the signal that a refresh token has been revoked or has otherwise become
// permanently unusable (FR4). Every other error (network failure, 5xx,
// timeout) is treated as transient and must NOT trip needs-reauth.
func isInvalidGrant(err error) bool {
	var retrieveErr *oauth2.RetrieveError
	if errors.As(err, &retrieveErr) {
		return retrieveErr.ErrorCode == "invalid_grant"
	}
	return false
}

// Save persists tok for channelID following the SCD2 close-and-open
// pattern (AGENTS.md "SCD2"): closes the prior live credential row (if
// any) and inserts a new one, in a single transaction so
// `channel_credential(channel_id) WHERE valid_to IS NULL`'s unique index
// never sees two open rows for the same Channel.
func (s tokenStore) Save(ctx context.Context, channelID, byPersonID uuid.UUID, tok *oauth2.Token, scopes []string) error {
	if tok == nil || tok.AccessToken == "" {
		return fmt.Errorf("save credential: missing access token")
	}
	if tok.RefreshToken == "" {
		return fmt.Errorf("save credential: missing refresh token")
	}

	accessCiphertext, err := s.encrypt(tok.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	refreshCiphertext, err := s.encrypt(tok.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}

	expiresAt := tok.Expiry
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(defaultTokenTTL)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		UPDATE channel_credential SET valid_to = NOW()
		WHERE channel_id = $1 AND valid_to IS NULL
	`, channelID); err != nil {
		return fmt.Errorf("close existing credential: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO channel_credential
			(channel_id, granted_by_person_id, access_token_ciphertext, refresh_token_ciphertext, access_token_expires_at, scopes)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, channelID, byPersonID, accessCiphertext, refreshCiphertext, expiresAt, scopes); err != nil {
		return fmt.Errorf("insert credential: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// MarkNeedsReauth flips channel.connection_state to needs_reauth via
// store.ChannelStore.SetConnectionState -- it deliberately never touches
// channel_credential or any synced_video/video_metrics/schedule_entry row
// (FR4's data-retention requirement: a reconnect happens through Save,
// which closes-and-opens the credential; nothing here deletes anything).
func (s tokenStore) MarkNeedsReauth(ctx context.Context, channelID uuid.UUID, reason string) error {
	if err := s.channels.SetConnectionState(ctx, channelID, store.ConnectionStateNeedsReauth); err != nil {
		return fmt.Errorf("mark needs-reauth (reason=%s): %w", reason, err)
	}
	return nil
}

// encrypt encrypts plaintext with AES-256-GCM and returns a
// base64-encoded ciphertext (nonce prepended). Mirrors
// web/auth.SessionManager.encryptToken / htmxauth.DBSessionManager.
// encryptToken -- copied per this package's doc comment rather than
// shared.
func (s tokenStore) encrypt(plaintext string) ([]byte, error) {
	block, err := aes.NewCipher(s.encKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	encoded := base64.StdEncoding.EncodeToString(ciphertext)
	return []byte(encoded), nil
}

// decrypt reverses encrypt.
func (s tokenStore) decrypt(encoded []byte) (string, error) {
	data, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.encKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
