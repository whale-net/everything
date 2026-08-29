// Package migrations embeds this directory's *.sql files -- LeafLab's
// checked-in schema history -- as an importable Go value, so a package
// outside leaflab/migrate can run the real, shipped migrations against a
// fresh database rather than hand-copying a trim of them.
//
// leaflab/migrate/main.go's :migrate go_binary keeps its own historical
// `//go:embed migrations/*.sql` (identical bytes, since Go embed patterns
// cannot reach outside a package's own directory tree with "../") and every
// leaflab/migrate/*_test.go integration test does the same for the same
// reason -- this package does not replace those, it exists so a consumer
// that cannot sit inside leaflab/migrate's own directory (this package's
// first is leaflab/conformance/nfr1c_test.go, NFR1.c: comparing FR72's real
// v_sensor_reading_with_plant view against FR71's API read path needs the
// real migration 021 view definition, not a second, possibly-drifted copy
// of it) can still run every migration through libs/go/migrate.NewRunner
// without duplicating this directory's SQL as Go string literals.
package migrations

import "embed"

// FS is every *.sql file directly in this directory, at its root (no
// "migrations/" path prefix -- unlike leaflab/migrate/main.go's own embed,
// whose pattern is relative to that package's directory one level up).
// Pass "." as libs/go/migrate.NewRunner's migrateDir argument when using
// this value.
//
//go:embed *.sql
var FS embed.FS
