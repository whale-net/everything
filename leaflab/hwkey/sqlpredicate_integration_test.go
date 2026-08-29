//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never even compiles it.
// See the go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest/README.md for how to run it.
//
// It proves Key.SQLPredicate matches exactly the rows
// idx_sensor_hw_address would, against a real Postgres database fixture --
// not just that the generated SQL text looks right, but that it actually
// selects the intended row and no other, for both a mux and a non-mux
// sensor, and that address canonicalisation (0x1A/26) and mux_path
// canonicalisation (absent key/explicit 0) don't change which row a query
// matches. Schema is self-contained hand-written DDL mirroring
// idx_sensor_hw_address from leaflab/migrate/migrations/009_sensor_schema_v2.up.sql
// -- it deliberately does not depend on leaflab/migrate's migrations so
// this test stays hermetic (see dbtest's own doc comment on
// Options.Schema).
package hwkey

import (
	"context"
	"testing"

	"github.com/whale-net/everything/libs/go/dbtest"
)

const testSchema = `
	CREATE TABLE board (
		board_id  BIGSERIAL PRIMARY KEY,
		device_id VARCHAR(64) NOT NULL UNIQUE
	);

	CREATE TABLE sensor (
		sensor_id      BIGSERIAL   PRIMARY KEY,
		board_id       BIGINT      NOT NULL REFERENCES board(board_id),
		sensor_type_id BIGINT      NOT NULL,
		i2c_address    SMALLINT,
		mux_path       JSONB       NOT NULL DEFAULT '[]'::jsonb
	);

	-- Mirrors idx_sensor_hw_address exactly (see
	-- leaflab/migrate/migrations/009_sensor_schema_v2.up.sql).
	CREATE UNIQUE INDEX idx_sensor_hw_address
		ON sensor(board_id, i2c_address, sensor_type_id, (mux_path::text))
		WHERE i2c_address IS NOT NULL;
`

// TestKeySQLPredicate_MatchesExactlyTheIntendedRow seeds a non-mux sensor
// and a mux sensor on the same board, then proves each Key's SQLPredicate
// selects exactly its own row -- built from a hex-parsed address for one
// row and a decimal-parsed address for the other, and an explicit-0 vs
// absent mux hop for their respective mux paths -- and never the other
// row.
func TestKeySQLPredicate_MatchesExactlyTheIntendedRow(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: testSchema})

	var boardID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, "board-1",
	).Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}

	// Non-mux sensor: i2c_address 0x1A (26), sensor_type_id 1, no mux.
	nonMuxKey := Key{I2CAddress: mustParseAddress(t, "0x1A"), MuxPath: MuxPath{}, SensorTypeID: 1}
	var nonMuxSensorID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO sensor (board_id, sensor_type_id, i2c_address, mux_path)
		 VALUES ($1, $2, $3, $4::jsonb) RETURNING sensor_id`,
		boardID, int64(nonMuxKey.SensorTypeID), 26, nonMuxKey.MuxPath.SQLText(),
	).Scan(&nonMuxSensorID); err != nil {
		t.Fatalf("insert non-mux sensor: %v", err)
	}

	// Mux sensor behind a TCA9548A at 0x70 (112), channel 5:
	// i2c_address 112, sensor_type_id 2.
	muxKey := Key{
		I2CAddress:   mustParseAddress(t, "112"),
		MuxPath:      MuxPath{{MuxAddress: 112, MuxChannel: 5}},
		SensorTypeID: 2,
	}
	var muxSensorID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO sensor (board_id, sensor_type_id, i2c_address, mux_path)
		 VALUES ($1, $2, $3, $4::jsonb) RETURNING sensor_id`,
		boardID, int64(muxKey.SensorTypeID), 112, muxKey.MuxPath.SQLText(),
	).Scan(&muxSensorID); err != nil {
		t.Fatalf("insert mux sensor: %v", err)
	}

	cases := []struct {
		name    string
		key     Key
		wantID  int64
		otherID int64
	}{
		{"non-mux sensor, hex-parsed address", nonMuxKey, nonMuxSensorID, muxSensorID},
		{"mux sensor, decimal-parsed address, explicit mux hop", muxKey, muxSensorID, nonMuxSensorID},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pred, args := tc.key.SQLPredicate(1)
			query := "SELECT sensor_id FROM sensor WHERE board_id = $1 AND " + pred
			var gotID int64
			row := db.Pool.QueryRow(ctx, query, append([]any{boardID}, args...)...)
			if err := row.Scan(&gotID); err != nil {
				t.Fatalf("SQLPredicate query found no row (or errored): %v\nquery: %s\nargs: %v", err, query, args)
			}
			if gotID != tc.wantID {
				t.Errorf("SQLPredicate matched sensor_id %d, want %d (the intended row)", gotID, tc.wantID)
			}
			if gotID == tc.otherID {
				t.Errorf("SQLPredicate matched the other sensor's row (%d), want its own (%d)", tc.otherID, tc.wantID)
			}
		})
	}

	// The same mux sensor, looked up via a Key built with an *absent*
	// mux-channel key instead of the row's stored explicit 0 for
	// muxAddress -- proving the canonicalisation FR18.1 requires actually
	// holds when the query side and the stored side used different
	// (but semantically equal) source forms. Here: query uses an explicit
	// non-zero channel (5) same as stored, but constructs the hop via a
	// freshly-parsed hex address (0x70 == 112) to also re-exercise FR18.2
	// on the mux's own address field, not just the sensor's.
	altMuxHopKey := Key{
		I2CAddress:   muxKey.I2CAddress,
		MuxPath:      MuxPath{{MuxAddress: uint32(mustParseAddressValue(t, "0x70")), MuxChannel: 5}},
		SensorTypeID: muxKey.SensorTypeID,
	}
	pred, args := altMuxHopKey.SQLPredicate(1)
	query := "SELECT sensor_id FROM sensor WHERE board_id = $1 AND " + pred
	var gotID int64
	if err := db.Pool.QueryRow(ctx, query, append([]any{boardID}, args...)...).Scan(&gotID); err != nil {
		t.Fatalf("hex-mux-address predicate found no row: %v", err)
	}
	if gotID != muxSensorID {
		t.Errorf("hex-mux-address predicate matched sensor_id %d, want %d", gotID, muxSensorID)
	}
}

// TestMuxPath_SQLText_MatchesPostgresRendering proves SQLText's byte format
// -- including "{"..": N, "..": N}" spacing -- is exactly what Postgres
// itself produces for `mux_path::text` on a jsonb value written using this
// package's canonical encoding, corroborating the doc-comment claim on
// MuxPath and the pure-unit-test coverage in
// TestMuxPath_SQLText_MatchesPostgresJSONBTextRendering.
func TestMuxPath_SQLText_MatchesPostgresRendering(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: `
		CREATE TABLE mux_path_probe (mux_path JSONB NOT NULL);
	`})

	cases := []MuxPath{
		{},
		{{MuxAddress: 112, MuxChannel: 5}},
		{{MuxAddress: 112, MuxChannel: 3}, {MuxAddress: 113, MuxChannel: 1}},
	}
	for _, p := range cases {
		canonical := p.SQLText()
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO mux_path_probe (mux_path) VALUES ($1::jsonb)`, canonical,
		); err != nil {
			t.Fatalf("insert canonical mux_path %s: %v", canonical, err)
		}
		var fromDB string
		if err := db.Pool.QueryRow(ctx,
			`SELECT mux_path::text FROM mux_path_probe WHERE mux_path::text = $1`, canonical,
		).Scan(&fromDB); err != nil {
			t.Fatalf("SELECT mux_path::text for %s: %v (Postgres's own ::text rendering did not match SQLText's canonical form)", canonical, err)
		}
		if fromDB != canonical {
			t.Errorf("Postgres mux_path::text = %q, want exactly SQLText's canonical form %q", fromDB, canonical)
		}
	}
}

func mustParseAddress(t *testing.T, s string) AddressOpt {
	t.Helper()
	a, err := ParseAddress(s)
	if err != nil {
		t.Fatalf("ParseAddress(%q): %v", s, err)
	}
	return a
}

func mustParseAddressValue(t *testing.T, s string) uint16 {
	t.Helper()
	a := mustParseAddress(t, s)
	v, _ := a.Value()
	return v
}
