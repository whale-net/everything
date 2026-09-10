// Package grantindex is a pgx-backed, domain-neutral grant existence index
// (FR12), living in its own sub-package alongside grpcauth/pgstore so core
// grpcauth (libs/go/grpcauth) keeps zero Postgres/pgx dependency.
// grpcauth.Store deliberately has no "list grants for a subject" method
// (see libs/go/grpcauth/store.go's own doc comment) -- this package builds
// the reusable index that makes that listing possible, alongside -- not
// instead of -- the existing Store/pgstore surface.
//
// # Schema contract
//
// No migration ships with this package (FR13), exactly like pgstore and
// libs/go/mcpauth's precedent -- the consuming domain owns and applies its
// own migration before calling New. A consuming migration must create a
// table shaped like this (column/table names are configurable via Config;
// the shape must match):
//
//	CREATE TABLE grpcauth_grant_index (
//	    subject_iss        TEXT        NOT NULL,
//	    subject_sub        TEXT        NOT NULL,
//	    domain             TEXT        NOT NULL,
//	    preferred_username TEXT        NOT NULL,
//	    granted_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
//	    PRIMARY KEY (subject_iss, subject_sub, domain)
//	);
//
// # Hard semantics
//
// Pure existence index. A row is written once, at successful consent, and
// never updated afterward. There is deliberately no status column: every
// caller reads live status from grpcauth.Store.Status(ctx, subject, grant)
// instead, so this index and grpcauth's own store never need syncing on
// Persist/Revoke/MarkNeedsReauth. Do not add a status column "for
// convenience" -- a second driftable source of truth for status is exactly
// what FR12 rules out.
//
// Not SCD2. There is no valid_from/valid_to pair here. This mirrors
// grpcauth.GrantStatus's plain-mutable-state design and pgstore's own
// table, and it carries no status column for SCD2 to apply to. AGENTS.md's
// SCD2 convention explicitly does not apply to this table -- do not "fix"
// it into an SCD2 shape.
//
// preferred_username is a captured snapshot, not a live lookup.
// grpcauth.Claims carries no such field; the only source is the consenting
// operator's own request context. It is stored once at write time because
// it cannot be re-derived later (an admin viewing another operator's grant
// has no path to look it up). This is a deliberate, narrow, display-only
// exception to "pure existence index".
package grantindex

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Default Config values, used whenever the corresponding field is left
// zero-valued.
const (
	defaultTableName               = "grpcauth_grant_index"
	defaultSubjectIssColumn        = "subject_iss"
	defaultSubjectSubColumn        = "subject_sub"
	defaultDomainColumn            = "domain"
	defaultPreferredUsernameColumn = "preferred_username"
	defaultGrantedAtColumn         = "granted_at"
)

// identifierPattern is the strict allow-list Config's table/column names
// must match. These names are interpolated directly into generated SQL
// (they cannot be bound query parameters), so New rejects anything not
// matching this pattern before ever building a query string -- this is a
// hard requirement against SQL injection via configuration, not a style
// nicety. Identical to grpcauth/pgstore's identifierPattern (and
// libs/go/mcpauth/credential.go's).
var identifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// validateIdentifier rejects any name that is not a safe, lowercase SQL
// identifier for direct interpolation into generated SQL.
func validateIdentifier(name, label string) error {
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("grpcauth/grantindex: Config.%s %q is not a valid SQL identifier (must match %s)", label, name, identifierPattern.String())
	}
	return nil
}

// Config configures New. Every field is an unqualified SQL identifier and
// defaults to the schema-contract name documented in the package doc
// comment above when left zero-valued -- validated against the same
// strict identifier allow-list pgstore.NewGrantStore uses (see
// identifierPattern).
type Config struct {
	// TableName is the unqualified name of the consuming domain's
	// grpcauth_grant_index-shaped table. Defaults to "grpcauth_grant_index".
	TableName string

	// SubjectIssColumn is the unqualified name of the subject issuer
	// column. Defaults to "subject_iss".
	SubjectIssColumn string

	// SubjectSubColumn is the unqualified name of the subject "sub" claim
	// column. Defaults to "subject_sub".
	SubjectSubColumn string

	// DomainColumn is the unqualified name of the domain column. Defaults
	// to "domain".
	DomainColumn string

	// PreferredUsernameColumn is the unqualified name of the captured
	// display-name snapshot column. Defaults to "preferred_username".
	PreferredUsernameColumn string

	// GrantedAtColumn is the unqualified name of the granted-at timestamp
	// column. Defaults to "granted_at".
	GrantedAtColumn string
}

// Entry is a plain data type describing one grant-index row, with no
// whagent_net-specific fields, so FR13's future extraction of the
// rendering layer is a copy, not a rewrite.
type Entry struct {
	SubjectIss        string
	SubjectSub        string
	Domain            string
	PreferredUsername string
	GrantedAt         time.Time
}

// Index is the pgx-backed grant existence index.
type Index struct {
	pool *pgxpool.Pool
	cfg  Config
}

// errNotImplemented tags every scaffolded method below. Implementation
// phase replaces each body and removes this sentinel from that method.
var errNotImplemented = errors.New("grpcauth/grantindex: not implemented")

// New constructs an *Index backed by pool and the table/column names in
// cfg (defaults applied for anything left zero-valued).
//
// pool is validated as non-nil first. TableName, SubjectIssColumn,
// SubjectSubColumn, DomainColumn, PreferredUsernameColumn, and
// GrantedAtColumn are then validated as safe SQL identifiers before any
// query is ever built -- mirrors pgstore.NewGrantStore.
func New(pool *pgxpool.Pool, cfg Config) (*Index, error) {
	if pool == nil {
		return nil, errors.New("grpcauth/grantindex: pool is required")
	}

	if cfg.TableName == "" {
		cfg.TableName = defaultTableName
	}
	if cfg.SubjectIssColumn == "" {
		cfg.SubjectIssColumn = defaultSubjectIssColumn
	}
	if cfg.SubjectSubColumn == "" {
		cfg.SubjectSubColumn = defaultSubjectSubColumn
	}
	if cfg.DomainColumn == "" {
		cfg.DomainColumn = defaultDomainColumn
	}
	if cfg.PreferredUsernameColumn == "" {
		cfg.PreferredUsernameColumn = defaultPreferredUsernameColumn
	}
	if cfg.GrantedAtColumn == "" {
		cfg.GrantedAtColumn = defaultGrantedAtColumn
	}

	if err := validateIdentifier(cfg.TableName, "TableName"); err != nil {
		return nil, err
	}
	if err := validateIdentifier(cfg.SubjectIssColumn, "SubjectIssColumn"); err != nil {
		return nil, err
	}
	if err := validateIdentifier(cfg.SubjectSubColumn, "SubjectSubColumn"); err != nil {
		return nil, err
	}
	if err := validateIdentifier(cfg.DomainColumn, "DomainColumn"); err != nil {
		return nil, err
	}
	if err := validateIdentifier(cfg.PreferredUsernameColumn, "PreferredUsernameColumn"); err != nil {
		return nil, err
	}
	if err := validateIdentifier(cfg.GrantedAtColumn, "GrantedAtColumn"); err != nil {
		return nil, err
	}

	return &Index{pool: pool, cfg: cfg}, nil
}

// Record implements an idempotent upsert of e on the primary key
// (subject_iss, subject_sub, domain).
//
// TODO(Implementation phase): re-consent for an already-recorded domain
// must not error -- ON CONFLICT (subject_iss, subject_sub, domain) DO
// NOTHING (granted_at is never bumped on re-record; preferred_username is
// a write-once snapshot, not refreshed either -- see the package doc
// comment's "preferred_username is a captured snapshot" section).
func (i *Index) Record(ctx context.Context, e Entry) error {
	return fmt.Errorf("grpcauth/grantindex: Record: %w", errNotImplemented)
}

// ListBySubject implements a read of every Entry recorded for
// (subjectIss, subjectSub), across all domains.
//
// TODO(Implementation phase): must never return another subject's rows --
// filter strictly on (subject_iss, subject_sub).
func (i *Index) ListBySubject(ctx context.Context, subjectIss, subjectSub string) ([]Entry, error) {
	return nil, fmt.Errorf("grpcauth/grantindex: ListBySubject: %w", errNotImplemented)
}

// ListAll implements a read of every recorded Entry across every subject
// and domain, ordered deterministically by (preferred_username, domain).
//
// TODO(Implementation phase): implement the ordered SELECT.
func (i *Index) ListAll(ctx context.Context) ([]Entry, error) {
	return nil, fmt.Errorf("grpcauth/grantindex: ListAll: %w", errNotImplemented)
}
