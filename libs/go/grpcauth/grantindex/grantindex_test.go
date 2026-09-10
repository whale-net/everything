package grantindex

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dummyPool returns a non-nil *pgxpool.Pool that never dials anything: with
// the default MinConns == 0, pgxpool.New only parses the connection string
// and starts a background goroutine that never attempts to open a
// connection, so this is safe to use in tests that only need a non-nil Pool
// value (e.g. to exercise validation that happens before any query is ever
// built or run). Mirrors pgstore_test.go's dummyPool.
func dummyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:5/db")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestNew_NilPool_Rejected asserts pool == nil is rejected with a clear
// error rather than panicking or silently proceeding.
func TestNew_NilPool_Rejected(t *testing.T) {
	idx, err := New(nil, Config{})
	if err == nil {
		t.Fatal("New(nil, ...): got nil error, want error")
	}
	if idx != nil {
		t.Fatalf("New(nil, ...): got non-nil *Index %v, want nil", idx)
	}
}

// TestNew_InvalidIdentifiers_Rejected asserts each caller-supplied
// table/column name that does not match identifierPattern is rejected
// with a clear error -- e.g. "foo; drop table" and "foo bar" -- for every
// one of TableName, SubjectIssColumn, SubjectSubColumn, DomainColumn,
// PreferredUsernameColumn, and GrantedAtColumn.
func TestNew_InvalidIdentifiers_Rejected(t *testing.T) {
	// A dummy, non-nil pool is enough here: identifier validation happens
	// entirely before New ever touches the pool.
	pool := dummyPool(t)

	// "" is deliberately excluded here: it is the zero value, which New
	// treats as "use the default" rather than a rejection case -- see
	// TestNew_DefaultsApplied for that behaviour.
	badValues := []string{"foo; drop table", "foo bar", "1abc", "Foo"}

	fieldSetters := map[string]func(cfg *Config, v string){
		"TableName":               func(cfg *Config, v string) { cfg.TableName = v },
		"SubjectIssColumn":        func(cfg *Config, v string) { cfg.SubjectIssColumn = v },
		"SubjectSubColumn":        func(cfg *Config, v string) { cfg.SubjectSubColumn = v },
		"DomainColumn":            func(cfg *Config, v string) { cfg.DomainColumn = v },
		"PreferredUsernameColumn": func(cfg *Config, v string) { cfg.PreferredUsernameColumn = v },
		"GrantedAtColumn":         func(cfg *Config, v string) { cfg.GrantedAtColumn = v },
	}

	for field, setter := range fieldSetters {
		for _, bad := range badValues {
			cfg := Config{}
			setter(&cfg, bad)
			if _, err := New(pool, cfg); err == nil {
				t.Errorf("New with %s = %q: got nil error, want rejection", field, bad)
			}
		}
	}
}

// TestNew_DefaultsApplied asserts every Config name field left
// zero-valued resolves to its documented default (grpcauth_grant_index /
// subject_iss / subject_sub / domain / preferred_username / granted_at),
// and that those defaults themselves pass validateIdentifier.
func TestNew_DefaultsApplied(t *testing.T) {
	pool := dummyPool(t)

	idx, err := New(pool, Config{})
	if err != nil {
		t.Fatalf("New with zero-valued Config: %v", err)
	}

	want := Config{
		TableName:               defaultTableName,
		SubjectIssColumn:        defaultSubjectIssColumn,
		SubjectSubColumn:        defaultSubjectSubColumn,
		DomainColumn:            defaultDomainColumn,
		PreferredUsernameColumn: defaultPreferredUsernameColumn,
		GrantedAtColumn:         defaultGrantedAtColumn,
	}
	if idx.cfg != want {
		t.Fatalf("idx.cfg = %+v, want %+v", idx.cfg, want)
	}

	// The defaults themselves must be valid identifiers -- otherwise New
	// would reject its own zero-valued Config.
	for label, name := range map[string]string{
		"TableName":               defaultTableName,
		"SubjectIssColumn":        defaultSubjectIssColumn,
		"SubjectSubColumn":        defaultSubjectSubColumn,
		"DomainColumn":            defaultDomainColumn,
		"PreferredUsernameColumn": defaultPreferredUsernameColumn,
		"GrantedAtColumn":         defaultGrantedAtColumn,
	} {
		if err := validateIdentifier(name, label); err != nil {
			t.Errorf("default %s %q fails validateIdentifier: %v", label, name, err)
		}
	}
}
