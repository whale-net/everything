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
	"time"

	"github.com/jackc/pgx/v5"
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

// subjectPlaceholder renders "$<paramNum>[::<SubjectCast>]" for use as a
// bound-parameter value in generated SQL — mirrors
// mcpauth.pgxCredentialStore.identityPlaceholder's cast handling.
func (s *grantStore) subjectPlaceholder(paramNum int) string {
	if s.cfg.SubjectCast == "" {
		return fmt.Sprintf("$%d", paramNum)
	}
	return fmt.Sprintf("$%d::%s", paramNum, s.cfg.SubjectCast)
}

// subjectWhere renders "<SubjectColumn> = $<paramNum>[::<SubjectCast>]" for
// use in a WHERE clause.
func (s *grantStore) subjectWhere(paramNum int) string {
	return fmt.Sprintf("%s = %s", s.cfg.SubjectColumn, s.subjectPlaceholder(paramNum))
}

// setStatus is the shared implementation behind MarkNeedsReauth and Revoke:
// both are an unconditional status write keyed by the explicit
// (subject, grant) pair, with no restriction tying the caller to that
// subject and no row deletion — only the persisted status column changes.
// A missing row is reported as grpcauth.ErrGrantNotFound rather than a
// silent no-op, so callers can distinguish "nothing to do" from "no such
// grant".
func (s *grantStore) setStatus(ctx context.Context, op, subject, grant string, status grpcauth.GrantStatus) error {
	query := fmt.Sprintf(`
		UPDATE %s SET %s = $3, updated_at = NOW()
		WHERE %s AND %s = $2
	`, s.cfg.TableName, s.cfg.StatusColumn, s.subjectWhere(1), s.cfg.GrantColumn)

	tag, err := s.cfg.Pool.Exec(ctx, query, subject, grant, status.String())
	if err != nil {
		return grpcauth.NewTransientError(op, err)
	}
	if tag.RowsAffected() == 0 {
		return grpcauth.ErrGrantNotFound
	}
	return nil
}

// Persist implements grpcauth.Store.
//
// material.RefreshToken is encrypted with grpcauth.Encrypt inside this
// method — NFR2: library-enforced, caller code never handles ciphertext on
// the storage path — then written with a single upsert on (subject, grant):
// overwrite the material column, set status = active, bump updated_at. This
// one statement serves initial consent (FR2), re-consent (FR11 — clears any
// needs_reauth in the same write) and refresh write-back (FR13), since it
// always resets status to active regardless of what it was before.
func (s *grantStore) Persist(ctx context.Context, subject, grant string, material grpcauth.TokenMaterial) error {
	ciphertext, err := grpcauth.Encrypt(s.cfg.EncryptionKey, []byte(material.RefreshToken))
	if err != nil {
		return fmt.Errorf("grpcauth/pgstore: Persist: encrypt: %w", err)
	}

	query := fmt.Sprintf(`
		INSERT INTO %s (%s, %s, %s, %s, updated_at)
		VALUES (%s, $2, $3, $4, NOW())
		ON CONFLICT (%s, %s) DO UPDATE SET
			%s = EXCLUDED.%s,
			%s = EXCLUDED.%s,
			updated_at = NOW()
	`,
		s.cfg.TableName, s.cfg.SubjectColumn, s.cfg.GrantColumn, s.cfg.MaterialColumn, s.cfg.StatusColumn,
		s.subjectPlaceholder(1),
		s.cfg.SubjectColumn, s.cfg.GrantColumn,
		s.cfg.MaterialColumn, s.cfg.MaterialColumn,
		s.cfg.StatusColumn, s.cfg.StatusColumn,
	)

	if _, err := s.cfg.Pool.Exec(ctx, query, subject, grant, ciphertext, grpcauth.GrantStatusActive.String()); err != nil {
		return grpcauth.NewTransientError("Persist", err)
	}
	return nil
}

// TokenMaterial implements grpcauth.Store.
//
// It reads the status and (still-encrypted) material columns together in a
// single round trip, then branches on status BEFORE ever calling
// grpcauth.Decrypt: revoked/needs_reauth return their sentinel error with
// the fetched ciphertext simply discarded, unexamined — no decrypt attempt
// and no Keycloak call, mirroring mcpauth.Verify's RevokedAt == nil check.
// Only status == active proceeds to decrypt and return material. A missing
// row is grpcauth.ErrGrantNotFound.
func (s *grantStore) TokenMaterial(ctx context.Context, subject, grant string) (grpcauth.TokenMaterial, error) {
	query := fmt.Sprintf(`
		SELECT %s, %s, updated_at
		FROM %s
		WHERE %s AND %s = $2
	`, s.cfg.StatusColumn, s.cfg.MaterialColumn, s.cfg.TableName, s.subjectWhere(1), s.cfg.GrantColumn)

	var status string
	var ciphertext []byte
	var updatedAt time.Time
	err := s.cfg.Pool.QueryRow(ctx, query, subject, grant).Scan(&status, &ciphertext, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return grpcauth.TokenMaterial{}, grpcauth.ErrGrantNotFound
		}
		return grpcauth.TokenMaterial{}, grpcauth.NewTransientError("TokenMaterial", err)
	}

	switch grpcauth.GrantStatus(status) {
	case grpcauth.GrantStatusRevoked:
		return grpcauth.TokenMaterial{}, grpcauth.ErrGrantRevoked
	case grpcauth.GrantStatusNeedsReauth:
		return grpcauth.TokenMaterial{}, grpcauth.ErrGrantNeedsReauth
	case grpcauth.GrantStatusActive:
		// fall through: only the active case decrypts and returns material.
	default:
		// A row with a status outside the three defined values indicates
		// corruption or a schema/version mismatch, not a normal grant
		// state — refuse to guess rather than treating it as active.
		return grpcauth.TokenMaterial{}, fmt.Errorf("grpcauth/pgstore: TokenMaterial: persisted status %q is not a recognized grpcauth.GrantStatus", status)
	}

	plaintext, err := grpcauth.Decrypt(s.cfg.EncryptionKey, ciphertext)
	if err != nil {
		return grpcauth.TokenMaterial{}, fmt.Errorf("grpcauth/pgstore: TokenMaterial: decrypt: %w", err)
	}

	return grpcauth.TokenMaterial{RefreshToken: string(plaintext), ObtainedAt: updatedAt}, nil
}

// Status implements grpcauth.Store.
//
// A distinct, cheap plain read of the status column only — no decrypt, no
// fetch of the material column, no Keycloak call (FR10). It is the exact
// same persisted column TokenMaterial consults, so there is exactly one
// definition of grant state, not two that could drift. A missing row is
// grpcauth.ErrGrantNotFound.
func (s *grantStore) Status(ctx context.Context, subject, grant string) (grpcauth.GrantStatus, error) {
	query := fmt.Sprintf(`
		SELECT %s FROM %s WHERE %s AND %s = $2
	`, s.cfg.StatusColumn, s.cfg.TableName, s.subjectWhere(1), s.cfg.GrantColumn)

	var status string
	if err := s.cfg.Pool.QueryRow(ctx, query, subject, grant).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", grpcauth.ErrGrantNotFound
		}
		return "", grpcauth.NewTransientError("Status", err)
	}
	return grpcauth.GrantStatus(status), nil
}

// MarkNeedsReauth implements grpcauth.Store.
//
// Sets status = needs_reauth for (subject, grant); the row and its history
// are retained, never deleted (FR8). A missing row is
// grpcauth.ErrGrantNotFound.
func (s *grantStore) MarkNeedsReauth(ctx context.Context, subject, grant string) error {
	return s.setStatus(ctx, "MarkNeedsReauth", subject, grant, grpcauth.GrantStatusNeedsReauth)
}

// Revoke implements grpcauth.Store.
//
// Sets status = revoked unconditionally, keyed by the explicit
// (subject, grant) pair, with NO restriction tying the caller to that
// subject (FR6) — revoking one grant does not touch any other grant of the
// same subject (FR5), since the WHERE clause is scoped to that one key. A
// missing row is grpcauth.ErrGrantNotFound. This is local-write only; the
// best-effort RFC 7009 remote revocation call is a follow-up task.
func (s *grantStore) Revoke(ctx context.Context, subject, grant string) error {
	return s.setStatus(ctx, "Revoke", subject, grant, grpcauth.GrantStatusRevoked)
}
