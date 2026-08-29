//go:build integration

// Real-Postgres integration coverage for this task's Testing section
// (#1364, FR30): Reader.ConfigLag's derivation of "a board's recent
// readings lag its active accepted config version" -- a lagging board is
// reported with its divergence instant, a caught-up board is not, a board
// missing either half (no accepted config, or no reading inside the
// bounded recent window) is reported as such rather than erroring or
// guessing, the check reuses NFR3.2's bounded window rather than scanning
// sensor_reading unbounded, and it is household-scoped (FR5) so any
// caller runs it for their own boards only -- never another household's.
//
// Schema is a separate hermetic trim from reader_integration_test.go's
// testSchema (same file/package rationale as this package's sibling
// integration tests): board gains household_id/device_id/retired_at
// (ConfigLag's own concerns; testSchema's board has neither, since no
// other Reader method needs them) and device_config is added fresh.
// Reader.ConfigLag itself takes an already-resolved authz.Scope (it
// performs no scope *resolution*, mirroring every other Reader method's
// authz-agnostic shape -- see readings.go's package doc comment) so this
// file constructs authz.HouseholdScope/UnionScope directly rather than
// standing up household_membership or a real grpcauth principal, the same
// way leaflab/api/authz's own scope_test.go exercises Scope without a
// database at all.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api/readings:reader_integration_test --test_output=all
package readings

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/tiers"
	"github.com/whale-net/everything/libs/go/dbtest"
)

const configLagTestSchema = `
	CREATE TABLE board (
		board_id     BIGSERIAL PRIMARY KEY,
		device_id    VARCHAR(64) NOT NULL UNIQUE,
		household_id BIGINT,
		retired_at   TIMESTAMPTZ
	);

	CREATE TABLE device_config (
		config_id BIGSERIAL PRIMARY KEY,
		board_id  BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		version   BIGINT NOT NULL,
		accepted  BOOLEAN NOT NULL DEFAULT FALSE,
		acked_at  TIMESTAMPTZ,
		UNIQUE (board_id, version)
	);

	CREATE TABLE sensor (
		sensor_id BIGSERIAL PRIMARY KEY,
		board_id  BIGINT NOT NULL REFERENCES board(board_id)
	);

	CREATE TABLE sensor_reading (
		reading_id     BIGSERIAL PRIMARY KEY,
		sensor_id      BIGINT NOT NULL REFERENCES sensor(sensor_id),
		value          DOUBLE PRECISION NOT NULL,
		recorded_at    TIMESTAMPTZ NOT NULL,
		config_version BIGINT
	);
`

type configLagFixture struct {
	reader *Reader
	pool   *pgxpool.Pool
}

func newConfigLagFixture(t *testing.T) *configLagFixture {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: configLagTestSchema})
	return &configLagFixture{reader: NewReader(db.Pool), pool: db.Pool}
}

func (f *configLagFixture) insertBoard(t *testing.T, deviceID string, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO board (device_id, household_id) VALUES ($1, $2) RETURNING board_id
	`, deviceID, householdID).Scan(&id); err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	return id
}

func (f *configLagFixture) insertAcceptedConfig(t *testing.T, boardID, version int64, ackedAt time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO device_config (board_id, version, accepted, acked_at) VALUES ($1, $2, TRUE, $3)
	`, boardID, version, ackedAt); err != nil {
		t.Fatalf("insert accepted device_config for board %d: %v", boardID, err)
	}
}

func (f *configLagFixture) insertSensor(t *testing.T, boardID int64) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO sensor (board_id) VALUES ($1) RETURNING sensor_id
	`, boardID).Scan(&id); err != nil {
		t.Fatalf("insert sensor for board %d: %v", boardID, err)
	}
	return id
}

func (f *configLagFixture) insertReading(t *testing.T, sensorID int64, configVersion int64, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO sensor_reading (sensor_id, value, recorded_at, config_version) VALUES ($1, 0, $2, $3)
	`, sensorID, at, configVersion); err != nil {
		t.Fatalf("insert reading for sensor %d: %v", sensorID, err)
	}
}

func (f *configLagFixture) entryFor(t *testing.T, result ConfigLagResult, boardID int64) ConfigLagEntry {
	t.Helper()
	for _, e := range result.Entries {
		if e.BoardID == boardID {
			return e
		}
	}
	t.Fatalf("no ConfigLagEntry for board %d in result %+v", boardID, result)
	return ConfigLagEntry{}
}

// TestConfigLag_OlderStampedVersion_ReportsLaggingWithDuration proves this
// task's core Testing criterion: a board whose most recent reading (inside
// the bounded recent window) is stamped with a version older than the
// board's active accepted version appears as lagging, and LaggingSince is
// the instant the accepted version actually became active (device_config.
// acked_at) -- not zero, not the reading's own timestamp.
func TestConfigLag_OlderStampedVersion_ReportsLaggingWithDuration(t *testing.T) {
	f := newConfigLagFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	ackedAt := now.Add(-3 * time.Hour)

	boardID := f.insertBoard(t, "device-lag", 1)
	f.insertAcceptedConfig(t, boardID, 2, ackedAt)
	sensorID := f.insertSensor(t, boardID)
	f.insertReading(t, sensorID, 1, now.Add(-5*time.Minute)) // still stamping the older version

	result, err := f.reader.ConfigLag(context.Background(), authz.NewHouseholdScope(1), Page{})
	if err != nil {
		t.Fatalf("ConfigLag: %v", err)
	}
	entry := f.entryFor(t, result, boardID)

	if !entry.HasAcceptedConfig || entry.AcceptedVersion != 2 {
		t.Fatalf("entry = %+v, want HasAcceptedConfig=true AcceptedVersion=2", entry)
	}
	if !entry.HasRecentReadings || entry.ObservedVersion != 1 {
		t.Fatalf("entry = %+v, want HasRecentReadings=true ObservedVersion=1", entry)
	}
	if !entry.Lagging {
		t.Fatal("Lagging = false, want true -- observed version 1 is older than accepted version 2")
	}
	if !entry.LaggingSince.Equal(ackedAt) {
		t.Errorf("LaggingSince = %s, want %s (the instant the active version became accepted)", entry.LaggingSince, ackedAt)
	}
}

// TestConfigLag_CaughtUpBoard_NotLagging proves the complement: a board
// whose most recent reading is already stamped with the active accepted
// version is not reported as lagging.
func TestConfigLag_CaughtUpBoard_NotLagging(t *testing.T) {
	f := newConfigLagFixture(t)
	now := time.Now().UTC().Truncate(time.Second)

	boardID := f.insertBoard(t, "device-caught-up", 1)
	f.insertAcceptedConfig(t, boardID, 2, now.Add(-time.Hour))
	sensorID := f.insertSensor(t, boardID)
	f.insertReading(t, sensorID, 2, now.Add(-5*time.Minute))

	result, err := f.reader.ConfigLag(context.Background(), authz.NewHouseholdScope(1), Page{})
	if err != nil {
		t.Fatalf("ConfigLag: %v", err)
	}
	entry := f.entryFor(t, result, boardID)

	if entry.Lagging {
		t.Errorf("Lagging = true, want false -- observed version equals accepted version")
	}
	if !entry.LaggingSince.IsZero() {
		t.Errorf("LaggingSince = %s, want zero when Lagging is false", entry.LaggingSince)
	}
}

// TestConfigLag_NoAcceptedConfig_ReportedNotErrored proves FR30's "a board
// with no accepted config is handled without error and is reported as
// such" -- HasAcceptedConfig is false, Lagging is false (never guessed at
// true or false-positive), and the call itself does not fail.
func TestConfigLag_NoAcceptedConfig_ReportedNotErrored(t *testing.T) {
	f := newConfigLagFixture(t)
	now := time.Now().UTC().Truncate(time.Second)

	boardID := f.insertBoard(t, "device-never-configured", 1)
	sensorID := f.insertSensor(t, boardID)
	f.insertReading(t, sensorID, 1, now.Add(-5*time.Minute))

	result, err := f.reader.ConfigLag(context.Background(), authz.NewHouseholdScope(1), Page{})
	if err != nil {
		t.Fatalf("ConfigLag: %v", err)
	}
	entry := f.entryFor(t, result, boardID)

	if entry.HasAcceptedConfig {
		t.Error("HasAcceptedConfig = true, want false -- this board has never had an accepted config")
	}
	if entry.Lagging {
		t.Error("Lagging = true, want false -- lagging is meaningless without an accepted config to compare against")
	}
}

// TestConfigLag_NoRecentReadings_ReportedNotErrored is the mirror case,
// and this task's proof of NFR3.2's "does not scan the hypertable
// unbounded": a board with an accepted config but only a reading *older*
// than the bounded recent window (tiers.RawCapWindow) is reported as
// HasRecentReadings=false, not errored and not guessed at as lagging or
// caught up. This is a genuine red/green guard on the bound itself, not
// merely on "no readings at all" -- widening or removing the
// recorded_at >= cutoff in Reader.ConfigLag's query makes this reading
// visible again and flips the assertion (verified by hand during this
// task's Testing phase: widening the cutoff to 1000x RawCapWindow turns
// this test red while every other ConfigLag test stays green, since only
// this one depends on the *absence* of an out-of-window row).
func TestConfigLag_NoRecentReadings_ReportedNotErrored(t *testing.T) {
	f := newConfigLagFixture(t)
	now := time.Now().UTC().Truncate(time.Second)

	boardID := f.insertBoard(t, "device-quiet", 1)
	f.insertAcceptedConfig(t, boardID, 1, now.Add(-100*time.Hour))
	sensorID := f.insertSensor(t, boardID)
	f.insertReading(t, sensorID, 1, now.Add(-tiers.RawCapWindow-time.Hour)) // outside the bounded window

	result, err := f.reader.ConfigLag(context.Background(), authz.NewHouseholdScope(1), Page{})
	if err != nil {
		t.Fatalf("ConfigLag: %v", err)
	}
	entry := f.entryFor(t, result, boardID)

	if entry.HasRecentReadings {
		t.Error("HasRecentReadings = true, want false -- the only reading is older than the bounded recent window")
	}
	if entry.Lagging {
		t.Error("Lagging = true, want false -- lagging is meaningless without a recent reading to compare against")
	}
}

// ── FR5: household-scoped by default ────────────────────────────────────

// TestConfigLag_HouseholdScope_ExcludesOtherHouseholdsBoards proves FR30's
// "the lag check is available to any caller for their own boards, not
// only to the admin" from the other direction: a caller's Scope (however
// obtained -- server.go's scopeForCaller resolves it from household
// membership, mirroring ListBoards' shape) only ever sees boards it
// covers. A board belonging to a different household never appears in
// the result, the same way ListBoards never leaks another household's
// board (authz_scope_integration_test.go).
func TestConfigLag_HouseholdScope_ExcludesOtherHouseholdsBoards(t *testing.T) {
	f := newConfigLagFixture(t)
	now := time.Now().UTC().Truncate(time.Second)

	boardA := f.insertBoard(t, "device-a", 1)
	f.insertAcceptedConfig(t, boardA, 1, now.Add(-time.Hour))
	boardB := f.insertBoard(t, "device-b", 2)
	f.insertAcceptedConfig(t, boardB, 1, now.Add(-time.Hour))

	result, err := f.reader.ConfigLag(context.Background(), authz.NewHouseholdScope(1), Page{})
	if err != nil {
		t.Fatalf("ConfigLag: %v", err)
	}

	foundA, foundB := false, false
	for _, e := range result.Entries {
		if e.BoardID == boardA {
			foundA = true
		}
		if e.BoardID == boardB {
			foundB = true
		}
	}
	if !foundA {
		t.Error("household 1's own board is missing from ConfigLag -- a member must be able to run the check for their own boards")
	}
	if foundB {
		t.Error("household 2's board leaked into household 1's ConfigLag result")
	}
}

// TestConfigLag_NonMemberScope_PermitsNothing is FR30's "not only to the
// admin" from the non-member side, mirrored on UnionScope's own
// documented "no household" empty-scope behavior (authz/scope.go,
// server.go's scopeForCaller nil-Claims fallback): a caller with no
// household membership at all -- the sharpest non-member case -- gets an
// empty result, never another household's boards and never an error. This
// is this RPC's analogue of NFR2's not-found: since ListConfigLag has no
// per-board request field to refuse (it is a scoped listing, like
// ListBoards, not a single-entity lookup), "sees nothing for a household
// it isn't in" is the shape that guarantee takes here.
func TestConfigLag_NonMemberScope_PermitsNothing(t *testing.T) {
	f := newConfigLagFixture(t)
	now := time.Now().UTC().Truncate(time.Second)

	boardA := f.insertBoard(t, "device-a", 1)
	f.insertAcceptedConfig(t, boardA, 1, now.Add(-time.Hour))

	result, err := f.reader.ConfigLag(context.Background(), authz.NewUnionScope(), Page{})
	if err != nil {
		t.Fatalf("ConfigLag: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Errorf("len(Entries) = %d, want 0 for a caller in no household", len(result.Entries))
	}
}
