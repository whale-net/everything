package migrate

import (
	"database/sql"
	"embed"
	"io/fs"
)

// Source is one embedded migration directory that can be merged with others
// via WithSource / NewMultiRunner. FS is typically a library's own
// `//go:embed migrations/*.sql` var; Dir is the subdirectory within it.
type Source struct {
	FS  embed.FS
	Dir string
}

// reservedSharedVersionFloor is the lowest migration version a shared
// library (e.g. htmxauth) is allowed to use for its own bundled migrations.
// Domain-owned migration sequences (manmanv2/migrate, tools/app_registry's
// migrate, leaflab/migrate, ...) number from 1 and are expected to never
// reach this range, which keeps merged version numbers globally unique
// without requiring coordination between independent numbering sequences.
const reservedSharedVersionFloor = 900000

// mergedFS presents multiple migration Sources as a single flat directory,
// as required by golang-migrate's iofs source driver. Each Source keeps its
// own file names; collisions are only a problem if two sources embed a file
// with the exact same name, which shared-library migrations avoid by
// numbering above reservedSharedVersionFloor.
type mergedFS struct {
	subs []fs.FS
}

func newMergedFS(sources []Source) (fs.FS, error) {
	m := &mergedFS{subs: make([]fs.FS, 0, len(sources))}
	for _, s := range sources {
		sub, err := fs.Sub(s.FS, s.Dir)
		if err != nil {
			return nil, err
		}
		m.subs = append(m.subs, sub)
	}
	return m, nil
}

func (m *mergedFS) Open(name string) (fs.File, error) {
	var firstErr error
	for _, sub := range m.subs {
		f, err := sub.Open(name)
		if err == nil {
			return f, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// ReadDir implements fs.ReadDirFS so golang-migrate's iofs driver (which
// asks for the root directory's contents up front) sees every source's
// files as one directory instead of only the first source's.
func (m *mergedFS) ReadDir(name string) ([]fs.DirEntry, error) {
	var entries []fs.DirEntry
	for _, sub := range m.subs {
		des, err := fs.ReadDir(sub, name)
		if err != nil {
			return nil, err
		}
		entries = append(entries, des...)
	}
	return entries, nil
}

// NewMultiRunner is NewRunner for more than one migration Source, merged
// into a single version sequence. Use WithSource via RunCLI instead of
// calling this directly unless you need a Runner outside of RunCLI's flag
// handling.
func NewMultiRunner(db *sql.DB, sources ...Source) (*Runner, error) {
	merged, err := newMergedFS(sources)
	if err != nil {
		return nil, err
	}
	return &Runner{
		db:         db,
		migrations: merged,
		migrateDir: ".",
		tracker:    NewHistoryTracker(db),
	}, nil
}
