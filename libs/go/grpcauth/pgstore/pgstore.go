// Package pgstore is the pgx-backed reference implementation of
// grpcauth.Store (issue #2387), living in its own sub-package so core
// grpcauth (libs/go/grpcauth) keeps zero Postgres/pgx dependency. Local-write
// semantics only — the best-effort RFC 7009 remote revocation call is a
// follow-up task.
//
// # Schema contract
//
// No migration ships with this package (FR13) — exactly like
// libs/go/mcpauth's precedent, the consuming domain owns and applies its own
// migration before calling NewGrantStore. A consuming migration must create
// a table shaped like this (column/table names are configurable via
// StoreConfig; the shape must match):
//
//	CREATE TABLE grpcauth_delegated_grant (
//	    subject        TEXT        NOT NULL,
//	    grant_key      TEXT        NOT NULL,
//	    token_material BYTEA       NOT NULL,
//	    status         TEXT        NOT NULL,
//	    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
//	    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
//	    PRIMARY KEY (subject, grant_key)
//	);
//
// This is a plain current-state table, not SCD2 (see AGENTS.md "SCD2"): a
// grant's history is its status transitions (grpcauth.GrantStatus), not
// versioned rows.
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/grpcauth"
)

// Default StoreConfig values, used whenever the corresponding field is left
// zero-valued.
const (
	defaultTableName      = "grpcauth_delegated_grant"
	defaultSubjectColumn  = "subject"
	defaultGrantColumn    = "grant_key"
	defaultMaterialColumn = "token_material"
	defaultStatusColumn   = "status"
)

// identifierPattern is the strict allow-list StoreConfig's table/column/cast
// names must match. These names are interpolated directly into generated
// SQL (they cannot be bound query parameters), so NewGrantStore rejects
// anything not matching this pattern before ever building a query string —
// this is a hard requirement against SQL injection via configuration, not a
// style nicety. Identical to libs/go/mcpauth/credential.go's
// identifierPattern.
var identifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// validateIdentifier rejects any name that is not a safe, lowercase SQL
// identifier for direct interpolation into generated SQL.
func validateIdentifier(name, label string) error {
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("grpcauth/pgstore: StoreConfig.%s %q is not a valid SQL identifier (must match %s)", label, name, identifierPattern.String())
	}
	return nil
}

// StoreConfig configures NewGrantStore, shaped like mcpauth.StoreConfig
// (see libs/go/mcpauth/credential.go) — the same SQL-identifier allow-list
// discipline applies here.
type StoreConfig struct {
	// Pool is the PostgreSQL connection pool. Required.
	Pool *pgxpool.Pool

	// TableName is the unqualified name of the consuming domain's
	// grpcauth-delegated-grant-shaped table. Defaults to
	// "grpcauth_delegated_grant". Unqualified so it resolves through
	// whatever search_path every other runtime query uses (mirrors
	// mcpauth.StoreConfig.TableName).
	TableName string

	// SubjectColumn is the unqualified name of the subject column.
	// Defaults to "subject".
	SubjectColumn string

	// GrantColumn is the unqualified name of the grant-key column.
	// Defaults to "grant_key".
	GrantColumn string

	// MaterialColumn is the unqualified name of the encrypted-material
	// (bytea) column. Defaults to "token_material". This column always
	// holds ciphertext — see NewGrantStore's package doc and NFR2: no
	// exported pgstore API accepts or returns this column's raw bytes.
	MaterialColumn string

	// StatusColumn is the unqualified name of the persisted-status column.
	// Defaults to "status". Holds the string form of a grpcauth.GrantStatus.
	StatusColumn string

	// SubjectCast is an optional PostgreSQL type name (e.g. "uuid") to cast
	// the subject parameter to in generated SQL, producing
	// `<SubjectColumn> = $N::<SubjectCast>` instead of
	// `<SubjectColumn> = $N` — mirrors mcpauth.StoreConfig.IdentityCast.
	SubjectCast string

	// EncryptionKey is the AES-256-GCM key (exactly grpcauth.GrantKeySize
	// bytes) this store uses to encrypt token material before writing it
	// and decrypt it after reading it. Required. Encryption is
	// library-enforced (NFR2): no exported pgstore API accepts or returns
	// ciphertext, and no caller-side encrypt/decrypt call is required.
	EncryptionKey []byte
}

// NewGrantStore constructs a grpcauth.Store backed by cfg.Pool and the
// table/column names in cfg (defaults applied for anything left
// zero-valued).
//
// cfg.Pool and cfg.EncryptionKey are validated before defaults are applied.
// TableName, SubjectColumn, GrantColumn, MaterialColumn, StatusColumn, and
// (if set) SubjectCast are then validated as safe SQL identifiers before any
// query is ever built — mirrors libs/go/mcpauth.NewCredentialStore.
//
// The five grpcauth.Store methods on the returned value are scaffolded
// stubs; see the "## Implementation" section of issue #2387 for the
// authoritative spec each TODO comment below summarizes.
func NewGrantStore(ctx context.Context, cfg StoreConfig) (grpcauth.Store, error) {
	if cfg.Pool == nil {
		return nil, errors.New("grpcauth/pgstore: StoreConfig.Pool is required")
	}
	if len(cfg.EncryptionKey) != grpcauth.GrantKeySize {
		return nil, fmt.Errorf("grpcauth/pgstore: StoreConfig.EncryptionKey must be %d bytes, got %d", grpcauth.GrantKeySize, len(cfg.EncryptionKey))
	}

	if cfg.TableName == "" {
		cfg.TableName = defaultTableName
	}
	if cfg.SubjectColumn == "" {
		cfg.SubjectColumn = defaultSubjectColumn
	}
	if cfg.GrantColumn == "" {
		cfg.GrantColumn = defaultGrantColumn
	}
	if cfg.MaterialColumn == "" {
		cfg.MaterialColumn = defaultMaterialColumn
	}
	if cfg.StatusColumn == "" {
		cfg.StatusColumn = defaultStatusColumn
	}

	if err := validateIdentifier(cfg.TableName, "TableName"); err != nil {
		return nil, err
	}
	if err := validateIdentifier(cfg.SubjectColumn, "SubjectColumn"); err != nil {
		return nil, err
	}
	if err := validateIdentifier(cfg.GrantColumn, "GrantColumn"); err != nil {
		return nil, err
	}
	if err := validateIdentifier(cfg.MaterialColumn, "MaterialColumn"); err != nil {
		return nil, err
	}
	if err := validateIdentifier(cfg.StatusColumn, "StatusColumn"); err != nil {
		return nil, err
	}
	if cfg.SubjectCast != "" {
		if err := validateIdentifier(cfg.SubjectCast, "SubjectCast"); err != nil {
			return nil, err
		}
	}

	return &grantStore{cfg: cfg}, nil
}

// grantStore is the pgx-backed grpcauth.Store implementation.
type grantStore struct {
	cfg StoreConfig
}

// compile-time conformance assertion.
var _ grpcauth.Store = (*grantStore)(nil)

// errNotImplemented tags every scaffolded method below. Implementation
// phase replaces each body and removes this sentinel from that method.
var errNotImplemented = errors.New("grpcauth/pgstore: not implemented")

// Persist implements grpcauth.Store.
//
// TODO(Implementation phase): encrypt material.RefreshToken with
// grpcauth.Encrypt(cfg.EncryptionKey, ...) inside this method — NFR2:
// library-enforced, caller code never handles plaintext/ciphertext bytes on
// the storage path — then upsert on (subject, grant): overwrite the
// material column, set status = 'active', bump updated_at. This single
// statement must serve initial consent (FR2), re-consent (FR11 — clears
// needs_reauth in the same write) and refresh write-back (FR13). Wrap
// unexpected pgx failures in grpcauth.NewTransientError.
func (s *grantStore) Persist(ctx context.Context, subject, grant string, material grpcauth.TokenMaterial) error {
	return fmt.Errorf("grpcauth/pgstore: Persist: %w", errNotImplemented)
}

// TokenMaterial implements grpcauth.Store.
//
// TODO(Implementation phase): read the status column FIRST; if revoked ->
// grpcauth.ErrGrantRevoked, if needs_reauth -> grpcauth.ErrGrantNeedsReauth
// — in both cases WITHOUT decrypting anything and without any Keycloak
// call (mirrors mcpauth.Verify's RevokedAt == nil check). Only for active
// does it decrypt (grpcauth.Decrypt) and return material. A missing row
// (pgx.ErrNoRows) -> grpcauth.ErrGrantNotFound.
func (s *grantStore) TokenMaterial(ctx context.Context, subject, grant string) (grpcauth.TokenMaterial, error) {
	return grpcauth.TokenMaterial{}, fmt.Errorf("grpcauth/pgstore: TokenMaterial: %w", errNotImplemented)
}

// Status implements grpcauth.Store.
//
// TODO(Implementation phase): a distinct cheap plain read of the status
// column only; no decrypt, no fetch of the material column, no Keycloak
// call (FR10) — the same persisted column TokenMaterial consults, so there
// is exactly one definition of the state. A missing row (pgx.ErrNoRows) ->
// grpcauth.ErrGrantNotFound.
func (s *grantStore) Status(ctx context.Context, subject, grant string) (grpcauth.GrantStatus, error) {
	return "", fmt.Errorf("grpcauth/pgstore: Status: %w", errNotImplemented)
}

// MarkNeedsReauth implements grpcauth.Store.
//
// TODO(Implementation phase): set status = 'needs_reauth' for
// (subject, grant); the row and its history are retained, never deleted
// (FR8). A missing row (pgx.ErrNoRows) -> grpcauth.ErrGrantNotFound.
func (s *grantStore) MarkNeedsReauth(ctx context.Context, subject, grant string) error {
	return fmt.Errorf("grpcauth/pgstore: MarkNeedsReauth: %w", errNotImplemented)
}

// Revoke implements grpcauth.Store.
//
// TODO(Implementation phase): set status = 'revoked' unconditionally, keyed
// by the explicit (subject, grant) pair, with NO restriction tying the
// caller to that subject (FR6) — revoking one grant must not affect any
// other grant of the same subject (FR5). A missing row (pgx.ErrNoRows) ->
// grpcauth.ErrGrantNotFound. This task's Revoke is local-write only; the
// best-effort RFC 7009 remote revocation call is a follow-up task.
func (s *grantStore) Revoke(ctx context.Context, subject, grant string) error {
	return fmt.Errorf("grpcauth/pgstore: Revoke: %w", errNotImplemented)
}
