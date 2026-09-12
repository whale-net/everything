package pgstore

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/grpcauth"
)

// dummyPool returns a non-nil *pgxpool.Pool that never dials anything: with
// the default MinConns == 0, pgxpool.New only parses the connection string
// and starts a background goroutine that never attempts to open a
// connection, so this is safe to use in tests that only need a non-nil
// StoreConfig.Pool value (e.g. to exercise validation that happens before
// any query is ever built or run).
func dummyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:5/db")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// validKey returns a grpcauth.GrantKeySize-byte key, valid for
// StoreConfig.EncryptionKey.
func validKey() []byte {
	return make([]byte, grpcauth.GrantKeySize)
}

// TestNewGrantStore_NilPool_Rejected asserts StoreConfig.Pool == nil is
// rejected with a clear error rather than panicking or silently proceeding.
func TestNewGrantStore_NilPool_Rejected(t *testing.T) {
	store, err := NewGrantStore(context.Background(), StoreConfig{
		Pool:          nil,
		EncryptionKey: validKey(),
	})
	if err == nil {
		t.Fatal("NewGrantStore with nil Pool: got nil error, want error")
	}
	if store != nil {
		t.Fatal("NewGrantStore with nil Pool: got non-nil store, want nil")
	}
	if !strings.Contains(err.Error(), "Pool") {
		t.Fatalf("error = %q, want it to name Pool", err.Error())
	}
}

// TestNewGrantStore_InvalidEncryptionKeySize_Rejected asserts any
// EncryptionKey that is not exactly grpcauth.GrantKeySize (32) bytes is
// rejected -- including nil/empty and both too-short and too-long keys.
func TestNewGrantStore_InvalidEncryptionKeySize_Rejected(t *testing.T) {
	cases := map[string][]byte{
		"nil":        nil,
		"empty":      {},
		"too short":  make([]byte, grpcauth.GrantKeySize-1),
		"too long":   make([]byte, grpcauth.GrantKeySize+1),
		"way short":  []byte("short"),
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			store, err := NewGrantStore(context.Background(), StoreConfig{
				Pool:          dummyPool(t),
				EncryptionKey: key,
			})
			if err == nil {
				t.Fatalf("NewGrantStore with %s key (len %d): got nil error, want error", name, len(key))
			}
			if store != nil {
				t.Fatalf("NewGrantStore with %s key: got non-nil store, want nil", name)
			}
			if !strings.Contains(err.Error(), "EncryptionKey") {
				t.Fatalf("error = %q, want it to name EncryptionKey", err.Error())
			}
		})
	}
}

// TestNewGrantStore_InvalidIdentifiers_Rejected asserts each
// caller-supplied table/column/cast name that does not match
// identifierPattern is rejected with a clear error -- e.g.
// "foo; drop table" and "foo bar" -- for every one of TableName,
// SubjectColumn, GrantColumn, MaterialColumn, StatusColumn, and
// SubjectCast.
func TestNewGrantStore_InvalidIdentifiers_Rejected(t *testing.T) {
	badNames := []string{"foo; drop table", "foo bar", "Foo", "1foo", ""}

	// A base config with every identifier valid, so each subtest only
	// exercises one field at a time.
	baseCfg := func() StoreConfig {
		return StoreConfig{
			Pool:           dummyPool(t),
			EncryptionKey:  validKey(),
			TableName:      "custom_table",
			SubjectColumn:  "custom_subject",
			GrantColumn:    "custom_grant",
			MaterialColumn: "custom_material",
			StatusColumn:   "custom_status",
			SubjectCast:    "uuid",
		}
	}

	fields := map[string]func(cfg *StoreConfig, v string){
		"TableName":      func(cfg *StoreConfig, v string) { cfg.TableName = v },
		"SubjectColumn":  func(cfg *StoreConfig, v string) { cfg.SubjectColumn = v },
		"GrantColumn":    func(cfg *StoreConfig, v string) { cfg.GrantColumn = v },
		"MaterialColumn": func(cfg *StoreConfig, v string) { cfg.MaterialColumn = v },
		"StatusColumn":   func(cfg *StoreConfig, v string) { cfg.StatusColumn = v },
		"SubjectCast":    func(cfg *StoreConfig, v string) { cfg.SubjectCast = v },
	}

	for fieldName, setField := range fields {
		for _, bad := range badNames {
			// The empty string is a legitimate "leave it zero-valued" input
			// for every field except TableName/columns, which fall back to
			// defaults instead of failing -- and SubjectCast, which is
			// simply omitted (no cast) rather than validated. Skip that
			// combination; it is covered by TestNewGrantStore_DefaultsApplied
			// instead.
			if bad == "" {
				continue
			}
			t.Run(fieldName+"/"+bad, func(t *testing.T) {
				cfg := baseCfg()
				setField(&cfg, bad)

				store, err := NewGrantStore(context.Background(), cfg)
				if err == nil {
					t.Fatalf("NewGrantStore with %s=%q: got nil error, want error", fieldName, bad)
				}
				if store != nil {
					t.Fatalf("NewGrantStore with %s=%q: got non-nil store, want nil", fieldName, bad)
				}
				if !strings.Contains(err.Error(), fieldName) {
					t.Fatalf("error = %q, want it to name %s", err.Error(), fieldName)
				}
			})
		}
	}
}

// TestNewGrantStore_DefaultsApplied asserts every StoreConfig name field
// left zero-valued resolves to its documented default
// (grpcauth_delegated_grant / subject / grant_key / token_material /
// status), and that those defaults themselves pass validateIdentifier.
func TestNewGrantStore_DefaultsApplied(t *testing.T) {
	store, err := NewGrantStore(context.Background(), StoreConfig{
		Pool:          dummyPool(t),
		EncryptionKey: validKey(),
		// TableName, SubjectColumn, GrantColumn, MaterialColumn,
		// StatusColumn, SubjectCast all left zero-valued.
	})
	if err != nil {
		t.Fatalf("NewGrantStore with defaults: %v", err)
	}

	gs, ok := store.(*grantStore)
	if !ok {
		t.Fatalf("NewGrantStore returned %T, want *grantStore", store)
	}

	checks := []struct {
		name string
		got  string
		want string
	}{
		{"TableName", gs.cfg.TableName, defaultTableName},
		{"SubjectColumn", gs.cfg.SubjectColumn, defaultSubjectColumn},
		{"GrantColumn", gs.cfg.GrantColumn, defaultGrantColumn},
		{"MaterialColumn", gs.cfg.MaterialColumn, defaultMaterialColumn},
		{"StatusColumn", gs.cfg.StatusColumn, defaultStatusColumn},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want default %q", c.name, c.got, c.want)
		}
		if err := validateIdentifier(c.want, c.name); err != nil {
			t.Errorf("default %s %q does not itself pass validateIdentifier: %v", c.name, c.want, err)
		}
	}

	if gs.cfg.SubjectCast != "" {
		t.Errorf("SubjectCast = %q, want empty (no cast) when left unset", gs.cfg.SubjectCast)
	}
}
