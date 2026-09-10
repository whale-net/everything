// Package delegatedgrant builds whagent_net's single shared
// grpcauth.DelegatedGrantSource + pgstore.Store + grantindex.Index triple
// (issue #2426, FR10/FR13/NFR5/NFR6 of plan #2421). Both `ui`'s and
// `mcp`'s composition roots (main.go) call Build identically -- NFR5
// requires one shared confidential Keycloak client used as the caller
// identity by both binaries (KEYCLOAK.md's "one client per caller
// identity" principle, applied here to whagent_net as a whole rather
// than per-domain: domain isolation is carried entirely by the grant key,
// //whagent_net/grantkey (FR4), not by provisioning a separate Keycloak
// client per domain).
//
// This is purely additive wiring (issue #2426's scope): nothing built
// here is on any request path yet. A dependent task swaps `mcp`'s
// per-call token acquisition (FR8) and `ui`'s /authorize handling (FR9)
// onto the Components this package builds.
//
// This is not `mcp/server` or `mcp/tools` (issue #2120's
// TestBUILD_NoStoreOrTemporalDependency) -- it freely imports
// //libs/go/grpcauth/pgstore and //libs/go/grpcauth/grantindex (both
// pgx-backed); only those two mcp sub-packages are restricted from doing
// so. `ui`'s and `mcp`'s own main.go (this package's two callers) are
// plain `package main`, outside that restriction.
package delegatedgrant

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantindex"
	"github.com/whale-net/everything/libs/go/grpcauth/pgstore"
)

// Config is the shared confidential client's settings, read from the
// environment by each binary's own loadConfig (WHAGENT_GRANT_*/
// WHAGENT_OIDC_ISSUER, ../ENV.md) -- this package itself never calls
// os.Getenv, mirroring //whagent_net/grantkey's dependency-light stance.
type Config struct {
	// Issuer is the Keycloak realm issuer (WHAGENT_OIDC_ISSUER) -- the
	// shared delegated-grant client lives in the same realm as the
	// existing sign-in/verification client both binaries already read
	// that variable for.
	Issuer string

	// ClientID/ClientSecret/RedirectURI are the one shared confidential
	// client's own settings (WHAGENT_GRANT_CLIENT_ID/_CLIENT_SECRET/
	// _REDIRECT_URI) -- distinct from WHAGENT_OIDC_CLIENT_ID, which only
	// ever verifies or forwards a token, never mints one (NFR5).
	ClientID     string
	ClientSecret string
	RedirectURI  string

	// EncryptionKeySecret is the raw secret (WHAGENT_GRANT_ENCRYPTION_KEY)
	// this package hashes via SHA-256 into pgstore's required 32-byte
	// AES-256-GCM key -- mirrors audience_score_system's
	// ASS_TOKEN_ENCRYPTION_KEY derivation (audience_score_system/ENV.md),
	// itself mirroring libs/go/htmxauth.DBSessionManager's encKey.
	EncryptionKeySecret string
}

// unset reports whether every field of cfg is empty -- the "feature not
// configured at all" case, which Build treats as a non-fatal
// ErrNotConfigured (see its doc comment) rather than an error, mirroring
// mcp/server.TokenExchangeConfig's own Enabled()/degrade precedent for
// this repo's other optional confidential-client wiring.
func (cfg Config) unset() bool {
	return cfg.Issuer == "" && cfg.ClientID == "" && cfg.ClientSecret == "" && cfg.RedirectURI == "" && cfg.EncryptionKeySecret == ""
}

// missing names which of cfg's required fields are empty, for Build's
// fail-loud partial-configuration error -- names only the env var,
// never any field's *value* (NFR5).
func (cfg Config) missing() []string {
	var m []string
	if cfg.Issuer == "" {
		m = append(m, "WHAGENT_OIDC_ISSUER")
	}
	if cfg.ClientID == "" {
		m = append(m, "WHAGENT_GRANT_CLIENT_ID")
	}
	if cfg.ClientSecret == "" {
		m = append(m, "WHAGENT_GRANT_CLIENT_SECRET")
	}
	if cfg.RedirectURI == "" {
		m = append(m, "WHAGENT_GRANT_REDIRECT_URI")
	}
	if cfg.EncryptionKeySecret == "" {
		m = append(m, "WHAGENT_GRANT_ENCRYPTION_KEY")
	}
	return m
}

// String redacts ClientSecret and EncryptionKeySecret so an accidental
// %v/%s of a Config (in a log line or error) cannot leak them (NFR5) --
// mirrors grpcauth.DelegatedGrantConfig's own String().
func (cfg Config) String() string {
	return fmt.Sprintf(
		"delegatedgrant.Config{Issuer:%q ClientID:%q ClientSecret:%s RedirectURI:%q EncryptionKeySecret:%s}",
		cfg.Issuer, cfg.ClientID, redactedPlaceholder(cfg.ClientSecret), cfg.RedirectURI, redactedPlaceholder(cfg.EncryptionKeySecret),
	)
}

// LogValue implements slog.LogValuer so slog.Any("config", cfg) (or an
// accidental %v inside a structured log call) redacts ClientSecret and
// EncryptionKeySecret the same way String() does (NFR5).
func (cfg Config) LogValue() slog.Value {
	return slog.StringValue(cfg.String())
}

// redactedPlaceholder mirrors grpcauth's own helper of the same name
// (unexported there too, so this package keeps its own copy rather than
// depending on an unexported symbol across packages).
func redactedPlaceholder(secret string) string {
	if secret == "" {
		return "(unset)"
	}
	return "(redacted)"
}

// ErrNotConfigured is returned by Build when cfg is entirely unset (see
// Config.unset's doc comment). Both `ui` and `mcp`'s main.go treat this as
// non-fatal (skip, WARNING-log, leave the delegated-grant wiring absent)
// -- mirroring mcp/server.TokenExchangeConfig's own degrade precedent --
// not as a startup failure, since local dev (whagent_net/Tiltfile) leaves
// these variables unset by default exactly like WHAGENT_MCP_KEYCLOAK_*
// (../ENV.md's "`mcp` server" section).
var ErrNotConfigured = errors.New("delegatedgrant: not configured (every WHAGENT_GRANT_*/WHAGENT_OIDC_ISSUER variable is unset)")

// Components is the constructed triple Build returns: the shared
// DelegatedGrantSource (also wired as pgstore's RFC 7009 Revoker, FR13),
// the Store every future request-path call will use (FR10), and the
// grant-bookkeeping index (FR13) alongside it.
type Components struct {
	Source *grpcauth.DelegatedGrantSource
	Store  grpcauth.Store
	Index  *grantindex.Index
}

// deferredRevoker breaks the construction-order cycle between pgstore's
// Store (which needs a Revoker at construction time,
// pgstore.StoreConfig.Revoker) and grpcauth.DelegatedGrantSource (which
// needs a Store at construction time, grpcauth.DelegatedGrantConfig.Store
// -- and is itself the Revoker implementation,
// libs/go/grpcauth/delegatedgrant_revoke.go's RevokeRefreshToken method).
// Build calls pgstore.NewGrantStore first with a *deferredRevoker whose
// source field is still nil, then grpcauth.NewDelegatedGrantSource with
// the resulting Store, then assigns the resulting source onto that same
// deferredRevoker instance already wired into the store. Every call
// through StoreConfig.Revoker happens strictly after Build returns, by
// which point source is always set -- Store.Revoke is never called
// during Build itself.
type deferredRevoker struct {
	source *grpcauth.DelegatedGrantSource
}

// RevokeRefreshToken satisfies pgstore.Revoker by forwarding to source,
// set by Build before this method can ever be reached by a real caller
// (see deferredRevoker's doc comment).
func (r *deferredRevoker) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	return r.source.RevokeRefreshToken(ctx, refreshToken)
}

// Build constructs Components against pool: a pgstore.Store over
// pool's grpcauth_delegated_grant table (whagent_net/migrate/schema/
// migrations/008_delegated_grant), a grantindex.Index over pool's
// grpcauth_grant_index table (the same migration), and the shared
// DelegatedGrantSource wired as the store's RFC 7009 Revoker.
//
// Returns ErrNotConfigured (never wrapped, checkable with errors.Is) when
// cfg is entirely unset -- see ErrNotConfigured's doc comment for why
// that is not itself fatal. Returns a plain, named error -- always
// fatal, treated that way by both callers -- when cfg is only *partially*
// set: silently constructing a DelegatedGrantSource that will only fail
// at the first token call, rather than at startup, is exactly what this
// check exists to avoid. Neither error path, nor the success path's log
// output, ever includes cfg.ClientSecret or cfg.EncryptionKeySecret's
// value (NFR5) -- see Config.String/LogValue.
//
// NewDelegatedGrantSource performs OIDC discovery against cfg.Issuer
// (unless it already fully resolves internal endpoints, which callers in
// this repo never supply) -- Build therefore makes a real network call
// to Keycloak whenever cfg is fully configured, exactly like
// htmxauth.NewAuthenticatorWithDB already does for `ui`'s own sign-in
// client.
func Build(ctx context.Context, cfg Config, pool *pgxpool.Pool) (Components, error) {
	if cfg.unset() {
		return Components{}, ErrNotConfigured
	}
	if missing := cfg.missing(); len(missing) > 0 {
		return Components{}, fmt.Errorf(
			"delegatedgrant: partially configured, missing %v -- set every WHAGENT_GRANT_*/WHAGENT_OIDC_ISSUER variable together, or none of them",
			missing,
		)
	}

	encKey := sha256.Sum256([]byte(cfg.EncryptionKeySecret))

	revoker := &deferredRevoker{}
	store, err := pgstore.NewGrantStore(ctx, pgstore.StoreConfig{
		Pool:          pool,
		EncryptionKey: encKey[:],
		Revoker:       revoker,
	})
	if err != nil {
		return Components{}, fmt.Errorf("delegatedgrant: store: %w", err)
	}

	source, err := grpcauth.NewDelegatedGrantSource(ctx, grpcauth.DelegatedGrantConfig{
		Issuer:        cfg.Issuer,
		ClientID:      cfg.ClientID,
		ClientSecret:  cfg.ClientSecret,
		RedirectURI:   cfg.RedirectURI,
		Store:         store,
		EncryptionKey: encKey[:],
	})
	if err != nil {
		return Components{}, fmt.Errorf("delegatedgrant: source: %w", err)
	}
	revoker.source = source

	index, err := grantindex.New(pool, grantindex.Config{})
	if err != nil {
		return Components{}, fmt.Errorf("delegatedgrant: grant index: %w", err)
	}

	return Components{Source: source, Store: store, Index: index}, nil
}
