//go:build integration

// Real-Postgres integration coverage for FR1.3's caller-visible skip
// surface: GetDeviceConfig reads back the audit_log rows
// leaflab/processor's ApplyConfigRegions (a separate binary; see that
// package's apply_config_regions_integration_test.go for the write-side
// proof that a skip writes exactly one such row) wrote for a board's
// skipped config entries, and returns them on the response's Skips field.
// This file proves the read side against real SQL: given an audit_log row
// of the exact shape ApplyConfigRegions writes (action, entity_kind,
// entity_id, reason -- see leaflab/api/audit/reason.go's shared constants,
// which keep the two binaries' action/entity_kind strings from drifting
// apart), GetDeviceConfig surfaces it correctly, whether or not an
// accepted config exists yet for that board.
//
// Schema is self-contained hand-written DDL (household /
// household_membership / board / sensor / device_config / audit_log),
// deliberately not shared with dbtest_helpers_integration_test.go's
// testSchema (no household/sensor concept) or authz_scope_integration_test
// .go's authzTestSchema (no sensor/audit_log concept) -- see this
// package's other integration test files for the same rationale. See
// //libs/go/dbtest's README for how to run integration tests like this
// one.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:get_device_config_skips_integration_test --test_output=all
package main

import (
	"context"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

const getDeviceConfigSkipsTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE household_membership (
		membership_id     BIGSERIAL PRIMARY KEY,
		household_id      BIGINT NOT NULL REFERENCES household(household_id),
		principal_subject TEXT NOT NULL,
		valid_from        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to          TIMESTAMPTZ
	);
	CREATE INDEX idx_gdcs_household_membership_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		household_id  BIGINT REFERENCES household(household_id),
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE sensor (
		sensor_id BIGSERIAL PRIMARY KEY,
		board_id  BIGINT NOT NULL REFERENCES board(board_id)
	);

	CREATE TABLE device_config (
		config_id        BIGSERIAL   PRIMARY KEY,
		board_id         BIGINT      NOT NULL REFERENCES board(board_id),
		version          BIGINT      NOT NULL,
		config_json      JSONB       NOT NULL,
		accepted         BOOLEAN     NOT NULL DEFAULT FALSE,
		pushed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		acked_at         TIMESTAMPTZ,
		rejection_reason TEXT,
		UNIQUE (board_id, version)
	);

	CREATE TABLE audit_log (
		audit_id            BIGSERIAL PRIMARY KEY,
		actor_subject       TEXT NOT NULL,
		actor_kind          TEXT NOT NULL,
		target_household_id BIGINT NULL,
		action              TEXT NOT NULL,
		entity_kind         TEXT NOT NULL,
		entity_id           TEXT NULL,
		reason              TEXT NULL,
		occurred_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		correlation_id      TEXT NULL
	);
`

func newGetDeviceConfigSkipsTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: getDeviceConfigSkipsTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return NewLeafLabAPIServer(repo, resolver, nil, nil, nil, nil, discardLogger(), defaultPollIntervalBounds), db.Pool
}

func gdcsCtxFor(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject})
}

func gdcsInsertHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household DEFAULT VALUES RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func gdcsInsertMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

func gdcsInsertBoard(t *testing.T, pool *pgxpool.Pool, deviceID string, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id, household_id) VALUES ($1, $2) RETURNING board_id`,
		deviceID, householdID).Scan(&id); err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	return id
}

func gdcsInsertSensor(t *testing.T, pool *pgxpool.Pool, boardID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO sensor (board_id) VALUES ($1) RETURNING sensor_id`, boardID).Scan(&id); err != nil {
		t.Fatalf("insert sensor for board %d: %v", boardID, err)
	}
	return id
}

// gdcsInsertApplySkipAuditRow inserts an audit_log row shaped exactly like
// leaflab/processor's recordApplySkip writes (repository.go) -- the same
// action/entity_kind constants (audit.ActionApplyConfigRegionSkip,
// audit.EntityKindSensor) both binaries share, per that constant's doc
// comment.
func gdcsInsertApplySkipAuditRow(t *testing.T, pool *pgxpool.Pool, boardID, sensorID, targetHouseholdID int64, reason string) {
	t.Helper()
	entityID := strconv.FormatInt(sensorID, 10)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO audit_log (actor_subject, actor_kind, target_household_id, action, entity_kind, entity_id, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`,
		"board:"+strconv.FormatInt(boardID, 10),
		string(audit.ActorKindSystem),
		targetHouseholdID,
		audit.ActionApplyConfigRegionSkip,
		audit.EntityKindSensor,
		entityID,
		reason,
	); err != nil {
		t.Fatalf("insert apply-skip audit row for sensor %d: %v", sensorID, err)
	}
}

// TestGetDeviceConfig_SurfacesRegionApplySkip_RealDB is FR1.3's
// caller-visible skip surface, proven against real SQL: given exactly one
// ApplyConfigRegions-shaped audit_log row for this board's sensor,
// GetDeviceConfig's response carries exactly one matching RegionApplySkip
// entry -- even though no device_config has ever been accepted for this
// board (Found stays false; the skip surface is independent of that).
func TestGetDeviceConfig_SurfacesRegionApplySkip_RealDB(t *testing.T) {
	server, pool := newGetDeviceConfigSkipsTestServer(t)

	householdA := gdcsInsertHousehold(t, pool)
	gdcsInsertMembership(t, pool, householdA, "alice")
	boardID := gdcsInsertBoard(t, pool, "device-a", householdA)
	sensorID := gdcsInsertSensor(t, pool, boardID)

	const wantReason = "This sensor's board and its assigned region belong to different households."
	gdcsInsertApplySkipAuditRow(t, pool, boardID, sensorID, householdA, wantReason)

	resp, err := server.GetDeviceConfig(gdcsCtxFor("alice"), &pb.GetDeviceConfigRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("GetDeviceConfig: %v", err)
	}
	if resp.Found {
		t.Error("Found = true, want false -- no device_config was ever accepted for this board")
	}
	if len(resp.Skips) != 1 {
		t.Fatalf("len(Skips) = %d, want exactly 1", len(resp.Skips))
	}
	skip := resp.Skips[0]
	if skip.SensorId != sensorID {
		t.Errorf("Skips[0].SensorId = %d, want %d", skip.SensorId, sensorID)
	}
	if skip.Reason != wantReason {
		t.Errorf("Skips[0].Reason = %q, want %q", skip.Reason, wantReason)
	}
	if skip.OccurredAt == nil {
		t.Error("Skips[0].OccurredAt is nil, want a populated Instant")
	}
}

// TestGetDeviceConfig_NoApplySkipAuditRows_EmptySkips_RealDB is the
// companion "not over-surfaced" case: a board with no
// ApplyConfigRegions-skip audit rows gets an empty Skips list, not a nil
// dereference or a spurious entry from unrelated audit_log rows for the
// same board.
func TestGetDeviceConfig_NoApplySkipAuditRows_EmptySkips_RealDB(t *testing.T) {
	server, pool := newGetDeviceConfigSkipsTestServer(t)

	householdA := gdcsInsertHousehold(t, pool)
	gdcsInsertMembership(t, pool, householdA, "alice")
	gdcsInsertBoard(t, pool, "device-a", householdA)

	resp, err := server.GetDeviceConfig(gdcsCtxFor("alice"), &pb.GetDeviceConfigRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("GetDeviceConfig: %v", err)
	}
	if len(resp.Skips) != 0 {
		t.Errorf("len(Skips) = %d, want 0", len(resp.Skips))
	}
}
