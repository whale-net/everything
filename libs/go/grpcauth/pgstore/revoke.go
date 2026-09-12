package pgstore

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/whale-net/everything/libs/go/grpcauth"
)

// Revoker is the best-effort RFC 7009 hook Revoke uses for remote
// token-revocation (FR13). Optional: when StoreConfig.Revoker is nil, Revoke
// performs the local write only -- exactly issue #2387's behaviour.
//
// pgstore never imports an OIDC/Keycloak client to implement this itself
// (keeping this package pgx-only-on-the-network-side); a caller wires in its
// own implementation. See libs/go/grpcauth/delegatedgrant_revoke.go's
// *grpcauth.DelegatedGrantSource for the concrete implementation this
// interface is shaped for.
type Revoker interface {
	// RevokeRefreshToken best-effort revokes refreshToken with the remote
	// authorization server. A non-nil error means the remote call did not
	// succeed (non-2xx response or a transport failure); Revoke treats
	// that as best-effort and never fails because of it. Implementations
	// must never include refreshToken (or any client secret) in a
	// returned error (NFR1).
	RevokeRefreshToken(ctx context.Context, refreshToken string) error
}

// readActiveRefreshToken returns the decrypted refresh token currently
// persisted for (subject, grant) plus true, but ONLY when that grant's
// persisted status is active -- mirroring TokenMaterial's status-first
// check (FR7). A revoked/needs_reauth/absent grant, or one whose ciphertext
// fails to decrypt, returns ("", false) rather than an error: this helper's
// only job is deciding whether there is a refresh token worth revoking
// remotely, not reporting "no such grant" -- Revoke's own local status write
// (via setStatus) is the authoritative source of grpcauth.ErrGrantNotFound.
func (s *grantStore) readActiveRefreshToken(ctx context.Context, subject, grant string) (string, bool) {
	query := fmt.Sprintf(`
		SELECT %s, %s
		FROM %s
		WHERE %s AND %s = $2
	`, s.cfg.StatusColumn, s.cfg.MaterialColumn, s.cfg.TableName, s.subjectWhere(1), s.cfg.GrantColumn)

	var status string
	var ciphertext []byte
	if err := s.cfg.Pool.QueryRow(ctx, query, subject, grant).Scan(&status, &ciphertext); err != nil {
		return "", false
	}
	if grpcauth.GrantStatus(status) != grpcauth.GrantStatusActive {
		return "", false
	}

	plaintext, err := grpcauth.Decrypt(s.cfg.EncryptionKey, ciphertext)
	if err != nil {
		return "", false
	}
	return string(plaintext), true
}

// Revoke implements grpcauth.Store.
//
// Order of operations (do not reorder -- see the package doc's "local
// write-first" note):
//
//  1. Read the current refresh token for (subject, grant), but only if the
//     grant is still active -- an already revoked/needs_reauth/absent grant
//     has nothing to usefully revoke remotely.
//  2. Perform the local status write to revoked, unconditionally and never
//     gated on step 3. A missing row surfaces grpcauth.ErrGrantNotFound
//     here, same as before this hook existed.
//  3. If cfg.Revoker is configured and step 1 found a token, best-effort
//     call it. A crash between steps 2 and 3 leaves the grant locally
//     revoked (FR7 holds) with a possibly-live remote token -- the safe
//     direction; do not reorder to remote-first.
//  4. A remote failure (non-2xx or transport error) is logged at WARNING
//     (AGENTS.md: the operation still completed, just not exactly as
//     expected) identifying only (subject, grant) and the error -- never the
//     refresh token or any client secret (NFR1) -- and Revoke still returns
//     nil: the remote call is best-effort and must never fail this call or
//     roll back the local write.
func (s *grantStore) Revoke(ctx context.Context, subject, grant string) error {
	refreshToken, hasToken := s.readActiveRefreshToken(ctx, subject, grant)

	if err := s.setStatus(ctx, "Revoke", subject, grant, grpcauth.GrantStatusRevoked); err != nil {
		return err
	}

	if s.cfg.Revoker == nil || !hasToken {
		return nil
	}

	if err := s.cfg.Revoker.RevokeRefreshToken(ctx, refreshToken); err != nil {
		slog.Warn("grpcauth/pgstore: best-effort RFC 7009 remote revocation failed; local revoke was already applied",
			"subject", subject, "grant", grant, "error", err)
	}
	return nil
}
