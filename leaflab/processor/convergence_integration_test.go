//go:build integration

// This file only builds under the "integration" build tag, same as
// leaflab/api's repository_integration_test.go/migration_016_integration_test.go
// -- see those files' doc comments for why (Docker-less machines, `bazel
// test //...` never even compiling it, let alone running it).
//
// Schema here is hand-written, self-contained DDL mirroring exactly what
// InsertCorrectiveConfigNextVersion (FR9/NFR4, this package's own
// repository.go) and a raw-SQL stand-in for leaflab-api's
// InsertDeviceConfigNextVersion (FR8) both touch: board, sensor's two NFR4
// guard columns (migration 016), and device_config (migration 007). Per
// repository_integration_test.go's own precedent and dbtest's README
// ("Options.Schema should be self-contained DDL -- do not depend on another
// package's migrations"), this does not import leaflab/migrate's real
// migrations.
package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
)

const processorIntegrationSchema = `
	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE sensor_type (
		sensor_type_id BIGSERIAL PRIMARY KEY,
		name           VARCHAR(64) NOT NULL UNIQUE,
		default_unit   VARCHAR(16) NOT NULL
	);

	CREATE TABLE sensor (
		sensor_id                           BIGSERIAL PRIMARY KEY,
		board_id                            BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		sensor_type_id                      BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		name                                VARCHAR(128) NOT NULL,
		unit                                VARCHAR(16) NOT NULL,
		registered_at                       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at                        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		corrective_push_attempts            INT NOT NULL DEFAULT 0,
		corrective_push_outstanding_version BIGINT,
		UNIQUE (board_id, name)
	);

	CREATE TABLE device_config (
		config_id        BIGSERIAL   PRIMARY KEY,
		board_id         BIGINT      NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		version          BIGINT      NOT NULL,
		config_json      JSONB       NOT NULL,
		accepted         BOOLEAN     NOT NULL DEFAULT FALSE,
		pushed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		acked_at         TIMESTAMPTZ,
		rejection_reason TEXT,
		UNIQUE (board_id, version)
	);
`

func newProcessorIntegrationTestDB(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: processorIntegrationSchema})
	return NewRepository(db.Pool), db.Pool
}

func seedProcessorBoard(t *testing.T, pool *pgxpool.Pool, deviceID string) int64 {
	t.Helper()
	var boardID int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, deviceID).Scan(&boardID); err != nil {
		t.Fatalf("seed board %s: %v", deviceID, err)
	}
	return boardID
}

func seedProcessorSensor(t *testing.T, pool *pgxpool.Pool, boardID int64, name string) int64 {
	t.Helper()
	ctx := context.Background()
	var typeID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO sensor_type (name, default_unit) VALUES ('temperature', 'C')
		ON CONFLICT (name) DO UPDATE SET default_unit = EXCLUDED.default_unit
		RETURNING sensor_type_id
	`).Scan(&typeID); err != nil {
		t.Fatalf("seed sensor_type: %v", err)
	}
	var sensorID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit) VALUES ($1, $2, $3, 'C')
		RETURNING sensor_id
	`, boardID, typeID, name).Scan(&sensorID); err != nil {
		t.Fatalf("seed sensor %s: %v", name, err)
	}
	return sensorID
}

// insertUserInitiatedConfigNextVersion is a raw-SQL stand-in for
// leaflab-api's Repository.InsertDeviceConfigNextVersion (FR8), copied
// verbatim from its own next-version statement
// (leaflab/api/repository.go:55-65) -- api_lib is package main there and
// not importable from this package, so this proves the *pattern* both
// repositories share is collision-safe, the same way
// migration_016_integration_test.go proves migration 016's DDL by
// hand-writing it rather than importing leaflab/migrate. It intentionally
// does not touch the NFR4 reset columns -- irrelevant to this test.
func insertUserInitiatedConfigNextVersion(ctx context.Context, pool *pgxpool.Pool, boardID int64, configJSON []byte) (int64, error) {
	for {
		var version int64
		err := pool.QueryRow(ctx, `
			WITH next AS (
				SELECT COALESCE(MAX(version), 0) + 1 AS v
				FROM device_config
				WHERE board_id = $1
			)
			INSERT INTO device_config (board_id, version, config_json)
			SELECT $1, next.v, $2 FROM next
			ON CONFLICT (board_id, version) DO NOTHING
			RETURNING version
		`, boardID, configJSON).Scan(&version)
		if err == nil {
			return version, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// another writer claimed this version; retry with the new MAX
			continue
		}
		return 0, err
	}
}

// TestInsertCorrectiveConfigNextVersion_DistinctFromUserInitiatedPush is
// Testing criterion 10: a corrective push (FR9, InsertCorrectiveConfigNextVersion)
// and a user-initiated push (FR8, mirrored here via
// insertUserInitiatedConfigNextVersion) for the same board are assigned
// distinct device_config.version values. Runs both concurrently, many
// times, against a real Postgres so the atomic next-version pattern
// (WITH...ON CONFLICT DO NOTHING against device_config's UNIQUE(board_id,
// version)) -- not incidental non-overlap -- is what is under test.
func TestInsertCorrectiveConfigNextVersion_DistinctFromUserInitiatedPush(t *testing.T) {
	repo, pool := newProcessorIntegrationTestDB(t)
	ctx := context.Background()
	boardID := seedProcessorBoard(t, pool, "device-collision")
	sensorID := seedProcessorSensor(t, pool, boardID, "temp")

	const rounds = 20
	for i := 0; i < rounds; i++ {
		var wg sync.WaitGroup
		var correctiveVersion, userVersion int64
		var correctiveErr, userErr error

		wg.Add(2)
		go func() {
			defer wg.Done()
			correctiveVersion, correctiveErr = repo.InsertCorrectiveConfigNextVersion(ctx, boardID, sensorID, []byte(`{"corrective":true}`))
		}()
		go func() {
			defer wg.Done()
			userVersion, userErr = insertUserInitiatedConfigNextVersion(ctx, pool, boardID, []byte(`{"user":true}`))
		}()
		wg.Wait()

		if correctiveErr != nil {
			t.Fatalf("round %d: InsertCorrectiveConfigNextVersion: %v", i, correctiveErr)
		}
		if userErr != nil {
			t.Fatalf("round %d: insertUserInitiatedConfigNextVersion: %v", i, userErr)
		}
		if correctiveVersion == userVersion {
			t.Fatalf("round %d: corrective push and user-initiated push collided on device_config.version %d", i, correctiveVersion)
		}
	}

	var totalRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM device_config WHERE board_id = $1`, boardID).Scan(&totalRows); err != nil {
		t.Fatalf("count device_config rows: %v", err)
	}
	if totalRows != rounds*2 {
		t.Fatalf("expected %d device_config rows (%d rounds x 2 concurrent inserts each), got %d -- a collision silently dropped a row",
			rounds*2, rounds, totalRows)
	}
}
