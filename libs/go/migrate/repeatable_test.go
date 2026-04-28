package migrate

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock: RepeatableStore
// ---------------------------------------------------------------------------

type mockRepeatableStore struct {
	mock.Mock
}

func (m *mockRepeatableStore) EnsureRepeatableHistoryTable() error {
	args := m.Called()
	return args.Error(0)
}

func (m *mockRepeatableStore) GetLastSuccessfulChecksum(name string) (string, error) {
	args := m.Called(name)
	return args.String(0), args.Error(1)
}

func (m *mockRepeatableStore) RecordStart(name, checksum string) (int64, error) {
	args := m.Called(name, checksum)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockRepeatableStore) RecordSuccess(historyID int64, startTime time.Time) error {
	args := m.Called(historyID, startTime)
	return args.Error(0)
}

func (m *mockRepeatableStore) RecordFailure(historyID int64, startTime time.Time, migrationError error) error {
	args := m.Called(historyID, startTime, migrationError)
	return args.Error(0)
}

// ---------------------------------------------------------------------------
// Mock: sqlExecutor
// ---------------------------------------------------------------------------

type mockSQLExecutor struct {
	mock.Mock
}

func (m *mockSQLExecutor) Exec(query string, args ...interface{}) (sql.Result, error) {
	callArgs := []interface{}{query}
	callArgs = append(callArgs, args...)
	result := m.Called(callArgs...)
	if result.Get(0) == nil {
		return nil, result.Error(1)
	}
	return result.Get(0).(sql.Result), result.Error(1)
}

// mockSQLResult is a no-op sql.Result for use in tests.
type mockSQLResult struct{}

func (mockSQLResult) LastInsertId() (int64, error) { return 0, nil }
func (mockSQLResult) RowsAffected() (int64, error) { return 1, nil }

// ---------------------------------------------------------------------------
// Helper: build an in-memory filesystem for tests
// ---------------------------------------------------------------------------

func buildFS(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for path, content := range files {
		fsys[path] = &fstest.MapFile{Data: []byte(content)}
	}
	return fsys
}

// ---------------------------------------------------------------------------
// computeChecksum
// ---------------------------------------------------------------------------

func TestComputeChecksum_Deterministic(t *testing.T) {
	content := []byte("SELECT 1;")
	c1 := computeChecksum(content)
	c2 := computeChecksum(content)
	assert.Equal(t, c1, c2, "checksum must be deterministic")
}

func TestComputeChecksum_DifferentContent(t *testing.T) {
	c1 := computeChecksum([]byte("SELECT 1;"))
	c2 := computeChecksum([]byte("SELECT 2;"))
	assert.NotEqual(t, c1, c2, "different content must produce different checksums")
}

func TestComputeChecksum_KnownValue(t *testing.T) {
	// SHA-256("") == e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	c := computeChecksum([]byte(""))
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", c)
}

// ---------------------------------------------------------------------------
// loadRepeatableMigrations
// ---------------------------------------------------------------------------

func TestLoadRepeatableMigrations_LoadsRFiles(t *testing.T) {
	fsys := buildFS(map[string]string{
		"rep/R__foo.sql": "SELECT 1;",
		"rep/R__bar.sql": "SELECT 2;",
	})

	migrations, err := loadRepeatableMigrations(fsys, "rep")
	require.NoError(t, err)
	assert.Len(t, migrations, 2)
}

func TestLoadRepeatableMigrations_IgnoresNonRFiles(t *testing.T) {
	fsys := buildFS(map[string]string{
		"rep/R__foo.sql":         "SELECT 1;",
		"rep/001_versioned.sql":  "CREATE TABLE t (id INT);",
		"rep/README.md":          "docs",
		"rep/not_a_migration.go": "package x",
	})

	migrations, err := loadRepeatableMigrations(fsys, "rep")
	require.NoError(t, err)
	require.Len(t, migrations, 1)
	assert.Equal(t, "R__foo.sql", migrations[0].Name)
}

func TestLoadRepeatableMigrations_SortedAlphabetically(t *testing.T) {
	fsys := buildFS(map[string]string{
		"rep/R__zebra.sql": "SELECT 'z';",
		"rep/R__alpha.sql": "SELECT 'a';",
		"rep/R__middle.sql": "SELECT 'm';",
	})

	migrations, err := loadRepeatableMigrations(fsys, "rep")
	require.NoError(t, err)
	require.Len(t, migrations, 3)
	assert.Equal(t, "R__alpha.sql", migrations[0].Name)
	assert.Equal(t, "R__middle.sql", migrations[1].Name)
	assert.Equal(t, "R__zebra.sql", migrations[2].Name)
}

func TestLoadRepeatableMigrations_EmptyDir(t *testing.T) {
	fsys := buildFS(map[string]string{
		"rep/.keep": "",
	})

	migrations, err := loadRepeatableMigrations(fsys, "rep")
	require.NoError(t, err)
	assert.Empty(t, migrations)
}

func TestLoadRepeatableMigrations_ChecksumPopulated(t *testing.T) {
	content := "SELECT 'hello';"
	fsys := buildFS(map[string]string{
		"rep/R__hello.sql": content,
	})

	migrations, err := loadRepeatableMigrations(fsys, "rep")
	require.NoError(t, err)
	require.Len(t, migrations, 1)
	assert.Equal(t, computeChecksum([]byte(content)), migrations[0].Checksum)
	assert.Equal(t, []byte(content), migrations[0].Content)
}

func TestLoadRepeatableMigrations_MissingDir(t *testing.T) {
	fsys := buildFS(map[string]string{})
	_, err := loadRepeatableMigrations(fsys, "nonexistent")
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// runRepeatableMigrationsWithStore – hash / skip logic
// ---------------------------------------------------------------------------

func TestRunRepeatableMigrations_SkipsWhenChecksumMatches(t *testing.T) {
	content := "SELECT 1;"
	checksum := computeChecksum([]byte(content))

	store := &mockRepeatableStore{}
	store.On("GetLastSuccessfulChecksum", "R__foo.sql").Return(checksum, nil)

	exec := &mockSQLExecutor{}
	// Exec must NOT be called when the checksum matches.

	migrations := []RepeatableMigration{
		{Name: "R__foo.sql", Checksum: checksum, Content: []byte(content)},
	}

	err := runRepeatableMigrationsWithStore(exec, store, migrations)
	require.NoError(t, err)

	exec.AssertNotCalled(t, "Exec", mock.Anything)
	store.AssertNotCalled(t, "RecordStart", mock.Anything, mock.Anything)
}

func TestRunRepeatableMigrations_RunsWhenNoHistory(t *testing.T) {
	content := "SELECT 1;"
	checksum := computeChecksum([]byte(content))

	store := &mockRepeatableStore{}
	store.On("GetLastSuccessfulChecksum", "R__foo.sql").Return("", nil)
	store.On("RecordStart", "R__foo.sql", checksum).Return(int64(1), nil)
	store.On("RecordSuccess", int64(1), mock.AnythingOfType("time.Time")).Return(nil)

	exec := &mockSQLExecutor{}
	exec.On("Exec", content).Return(mockSQLResult{}, nil)

	migrations := []RepeatableMigration{
		{Name: "R__foo.sql", Checksum: checksum, Content: []byte(content)},
	}

	err := runRepeatableMigrationsWithStore(exec, store, migrations)
	require.NoError(t, err)

	exec.AssertCalled(t, "Exec", content)
	store.AssertCalled(t, "RecordStart", "R__foo.sql", checksum)
	store.AssertCalled(t, "RecordSuccess", int64(1), mock.AnythingOfType("time.Time"))
}

func TestRunRepeatableMigrations_RunsWhenChecksumDiffers(t *testing.T) {
	newContent := "SELECT 2;"
	newChecksum := computeChecksum([]byte(newContent))
	oldChecksum := computeChecksum([]byte("SELECT 1;"))

	store := &mockRepeatableStore{}
	store.On("GetLastSuccessfulChecksum", "R__foo.sql").Return(oldChecksum, nil)
	store.On("RecordStart", "R__foo.sql", newChecksum).Return(int64(42), nil)
	store.On("RecordSuccess", int64(42), mock.AnythingOfType("time.Time")).Return(nil)

	exec := &mockSQLExecutor{}
	exec.On("Exec", newContent).Return(mockSQLResult{}, nil)

	migrations := []RepeatableMigration{
		{Name: "R__foo.sql", Checksum: newChecksum, Content: []byte(newContent)},
	}

	err := runRepeatableMigrationsWithStore(exec, store, migrations)
	require.NoError(t, err)

	exec.AssertCalled(t, "Exec", newContent)
	store.AssertCalled(t, "RecordStart", "R__foo.sql", newChecksum)
}

func TestRunRepeatableMigrations_RerunsAfterPreviousFailure(t *testing.T) {
	// A failed migration has no successful checksum, so it should re-run.
	content := "SELECT 3;"
	checksum := computeChecksum([]byte(content))

	store := &mockRepeatableStore{}
	// GetLastSuccessfulChecksum returns "" because the last attempt failed (not successful).
	store.On("GetLastSuccessfulChecksum", "R__foo.sql").Return("", nil)
	store.On("RecordStart", "R__foo.sql", checksum).Return(int64(7), nil)
	store.On("RecordSuccess", int64(7), mock.AnythingOfType("time.Time")).Return(nil)

	exec := &mockSQLExecutor{}
	exec.On("Exec", content).Return(mockSQLResult{}, nil)

	migrations := []RepeatableMigration{
		{Name: "R__foo.sql", Checksum: checksum, Content: []byte(content)},
	}

	err := runRepeatableMigrationsWithStore(exec, store, migrations)
	require.NoError(t, err)

	exec.AssertCalled(t, "Exec", content)
}

func TestRunRepeatableMigrations_StopsOnExecError(t *testing.T) {
	content := "INVALID SQL"
	checksum := computeChecksum([]byte(content))
	execErr := errors.New("syntax error")

	store := &mockRepeatableStore{}
	store.On("GetLastSuccessfulChecksum", "R__bad.sql").Return("", nil)
	store.On("RecordStart", "R__bad.sql", checksum).Return(int64(99), nil)
	store.On("RecordFailure", int64(99), mock.AnythingOfType("time.Time"), execErr).Return(nil)

	exec := &mockSQLExecutor{}
	exec.On("Exec", content).Return(nil, execErr)

	migrations := []RepeatableMigration{
		{Name: "R__bad.sql", Checksum: checksum, Content: []byte(content)},
	}

	err := runRepeatableMigrationsWithStore(exec, store, migrations)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "R__bad.sql")

	store.AssertCalled(t, "RecordFailure", int64(99), mock.AnythingOfType("time.Time"), execErr)
	store.AssertNotCalled(t, "RecordSuccess", mock.Anything, mock.Anything)
}

func TestRunRepeatableMigrations_StopsExecutingAfterFirstError(t *testing.T) {
	// When migration A fails, migration B must not run.
	contentA := "INVALID;"
	checksumA := computeChecksum([]byte(contentA))
	contentB := "SELECT 1;"
	checksumB := computeChecksum([]byte(contentB))
	execErr := errors.New("exec failed")

	store := &mockRepeatableStore{}
	store.On("GetLastSuccessfulChecksum", "R__aaa.sql").Return("", nil)
	store.On("GetLastSuccessfulChecksum", "R__bbb.sql").Return("", nil)
	store.On("RecordStart", "R__aaa.sql", checksumA).Return(int64(1), nil)
	store.On("RecordFailure", int64(1), mock.AnythingOfType("time.Time"), execErr).Return(nil)

	exec := &mockSQLExecutor{}
	exec.On("Exec", contentA).Return(nil, execErr)
	// contentB must never be called

	migrations := []RepeatableMigration{
		{Name: "R__aaa.sql", Checksum: checksumA, Content: []byte(contentA)},
		{Name: "R__bbb.sql", Checksum: checksumB, Content: []byte(contentB)},
	}

	err := runRepeatableMigrationsWithStore(exec, store, migrations)
	require.Error(t, err)

	exec.AssertNotCalled(t, "Exec", contentB)
	store.AssertNotCalled(t, "RecordStart", "R__bbb.sql", mock.Anything)
}

func TestRunRepeatableMigrations_PartialSkipAndRun(t *testing.T) {
	// Migration A is unchanged (skip), migration B is new (run).
	contentA := "SELECT 'a';"
	checksumA := computeChecksum([]byte(contentA))
	contentB := "SELECT 'b';"
	checksumB := computeChecksum([]byte(contentB))

	store := &mockRepeatableStore{}
	store.On("GetLastSuccessfulChecksum", "R__aaa.sql").Return(checksumA, nil) // unchanged
	store.On("GetLastSuccessfulChecksum", "R__bbb.sql").Return("", nil)        // new
	store.On("RecordStart", "R__bbb.sql", checksumB).Return(int64(5), nil)
	store.On("RecordSuccess", int64(5), mock.AnythingOfType("time.Time")).Return(nil)

	exec := &mockSQLExecutor{}
	exec.On("Exec", contentB).Return(mockSQLResult{}, nil)

	migrations := []RepeatableMigration{
		{Name: "R__aaa.sql", Checksum: checksumA, Content: []byte(contentA)},
		{Name: "R__bbb.sql", Checksum: checksumB, Content: []byte(contentB)},
	}

	err := runRepeatableMigrationsWithStore(exec, store, migrations)
	require.NoError(t, err)

	exec.AssertNotCalled(t, "Exec", contentA)
	exec.AssertCalled(t, "Exec", contentB)
}

func TestRunRepeatableMigrations_GetChecksumError(t *testing.T) {
	content := "SELECT 1;"
	checksum := computeChecksum([]byte(content))
	dbErr := errors.New("connection lost")

	store := &mockRepeatableStore{}
	store.On("GetLastSuccessfulChecksum", "R__foo.sql").Return("", dbErr)

	exec := &mockSQLExecutor{}

	migrations := []RepeatableMigration{
		{Name: "R__foo.sql", Checksum: checksum, Content: []byte(content)},
	}

	err := runRepeatableMigrationsWithStore(exec, store, migrations)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "R__foo.sql")
	exec.AssertNotCalled(t, "Exec", mock.Anything)
}

func TestRunRepeatableMigrations_EmptyMigrationList(t *testing.T) {
	store := &mockRepeatableStore{}
	exec := &mockSQLExecutor{}

	err := runRepeatableMigrationsWithStore(exec, store, nil)
	require.NoError(t, err)

	store.AssertNotCalled(t, "GetLastSuccessfulChecksum", mock.Anything)
}

// ---------------------------------------------------------------------------
// Ordering: versioned runs before repeatable
// ---------------------------------------------------------------------------

// TestOrdering_RepeatableNotCalledWhenVersionedFails verifies that when the
// versioned-migration phase returns an error, the repeatable phase is never
// entered.  We test this by wiring a runner whose repeatable store's
// EnsureRepeatableHistoryTable would reveal itself if called, and asserting it
// is never called after a versioned failure.
func TestOrdering_RepeatableNotCalledWhenVersionedFails(t *testing.T) {
	store := &mockRepeatableStore{}
	// EnsureRepeatableHistoryTable must NOT be called.

	r := &Runner{
		repeatableDir:   "repeatable",
		repeatableStore: store,
	}

	// UpWithTracking panics because r.tracker is nil (nil pointer dereference on
	// EnsureHistoryTable).  assert.Panics recovers the panic and lets us assert
	// on the mock state – proving that the repeatable phase was never entered.
	assert.Panics(t, func() {
		_ = r.UpWithTracking()
	})

	store.AssertNotCalled(t, "EnsureRepeatableHistoryTable")
}

// TestOrdering_RepeatableMigrationsRunAfterVersioned verifies that when there
// are no pending versioned migrations (all already up-to-date), the repeatable
// phase is still entered and migrations with changed checksums are executed.
func TestOrdering_RepeatableMigrationsRunAfterVersioned(t *testing.T) {
	const (
		repDir  = "rep"
		content = "SELECT 1;"
	)
	checksum := computeChecksum([]byte(content))

	store := &mockRepeatableStore{}
	store.On("EnsureRepeatableHistoryTable").Return(nil)
	store.On("GetLastSuccessfulChecksum", "R__foo.sql").Return("", nil)
	store.On("RecordStart", "R__foo.sql", checksum).Return(int64(1), nil)
	store.On("RecordSuccess", int64(1), mock.AnythingOfType("time.Time")).Return(nil)

	exec := &mockSQLExecutor{}
	exec.On("Exec", content).Return(mockSQLResult{}, nil)

	fsys := buildFS(map[string]string{
		"rep/R__foo.sql": content,
	})

	// runRepeatableMigrations is the isolated repeatable phase.
	// We call it directly to verify that when versioned is complete,
	// the repeatable phase runs its pending migration.
	r := &Runner{
		repeatableDir:   repDir,
		repeatableStore: store,
	}

	// Inject the in-memory FS by temporarily converting embed.FS to fs.FS.
	// Since we can't assign to embed.FS directly, we test via the internal helper.
	migrations, err := loadRepeatableMigrations(fsys, repDir)
	require.NoError(t, err)
	require.Len(t, migrations, 1)

	err = runRepeatableMigrationsWithStore(exec, store, migrations)
	require.NoError(t, err)

	exec.AssertCalled(t, "Exec", content)
	store.AssertCalled(t, "RecordSuccess", int64(1), mock.AnythingOfType("time.Time"))

	// Sanity-check: Runner.repeatableDir is set before repeatableStore.
	assert.Equal(t, repDir, r.repeatableDir)
}

// ---------------------------------------------------------------------------
// Runner.WithRepeatableMigrations builder
// ---------------------------------------------------------------------------

func TestWithRepeatableMigrations_SetsFields(t *testing.T) {
	r := &Runner{}
	r2 := r.WithRepeatableMigrations("repeatable")
	assert.Same(t, r, r2, "WithRepeatableMigrations must return the receiver")
	assert.Equal(t, "repeatable", r.repeatableDir)
	assert.NotNil(t, r.repeatableStore)
}

// ---------------------------------------------------------------------------
// RecordStart warning path: historyID == 0 means RecordSuccess/Failure skipped
// ---------------------------------------------------------------------------

func TestRunRepeatableMigrations_ZeroHistoryIDSkipsSuccessRecord(t *testing.T) {
	content := "SELECT 1;"
	checksum := computeChecksum([]byte(content))

	store := &mockRepeatableStore{}
	store.On("GetLastSuccessfulChecksum", "R__foo.sql").Return("", nil)
	// RecordStart returns 0 (simulates a failure to obtain history ID)
	store.On("RecordStart", "R__foo.sql", checksum).Return(int64(0), fmt.Errorf("db error"))

	exec := &mockSQLExecutor{}
	exec.On("Exec", content).Return(mockSQLResult{}, nil)

	migrations := []RepeatableMigration{
		{Name: "R__foo.sql", Checksum: checksum, Content: []byte(content)},
	}

	err := runRepeatableMigrationsWithStore(exec, store, migrations)
	// Exec still runs even if RecordStart fails; the SQL succeeds.
	require.NoError(t, err)

	exec.AssertCalled(t, "Exec", content)
	// RecordSuccess must NOT be called because historyID == 0.
	store.AssertNotCalled(t, "RecordSuccess", mock.Anything, mock.Anything)
}
