//go:build integration

// Real-Postgres integration coverage for FR40's "rollback writes forward"
// restore guarantee and its audit trail (FR8, NFR6.2) -- see this task's
// own Testing section, in particular its named load-bearing test.
//
// This composes the same private/repository pieces RollbackDeviceConfig's
// handler (server.go) itself calls -- Repository.GetConfigVersionRow and
// Repository.InsertDeviceConfigNextVersion -- directly against a real
// Repository, rather than driving the full RPC end to end:
// liveRollbackWriter.write reaches s.publisher (a concrete *rmq.Publisher
// this repo has no in-process fake for) after storing the row, so a
// non-dry-run RollbackDeviceConfig call can't be driven to a genuine
// success here -- see push_device_config_scope_integration_test.go's
// identical note. server_rollback_device_config_test.go already proves
// RollbackDeviceConfig's handler wires GetConfigVersionRow's result into
// InsertDeviceConfigNextVersion's derivedFromVersion argument in this
// exact order (at the fakeRepo/dispatch level, one step short of
// Publish); this file's job is proving what happens when those same
// calls hit real SQL: the new version's stored payload is genuinely
// byte-identical to the version rolled back to, the original row is left
// completely untouched, and the audit trail lands in the same
// transaction.
//
// NFR6.2's append-only trigger itself (UPDATE of a payload column
// raising, ack columns remaining updatable) is a migration-level
// guarantee and is covered against the real migration 035 in
// //leaflab/migrate:ownership_migration_integration_test (which embeds
// config_derived_from_migration_integration_test.go), the same split
// audit_log_migration_integration_test.go's own doc comment describes for
// migration 016 -- this file's hand-rolled schema below is schema-only
// and does not reproduce that trigger, matching
// dbtest_helpers_integration_test.go's own testSchema precedent.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:rollback_device_config_integration_test --test_output=all
package main

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
)

// This file reuses dbtest_helpers_integration_test.go's newTestRepository
// and testSchema (board, device_config -- including migration 034's
// push_group_id and migration 035's derived_from_version columns -- and
// audit_log) rather than declaring its own hermetic schema: testSchema's
// own doc comment already covers derived_from_version's presence there for
// exactly this reason. Every InsertDeviceConfigNextVersion call below
// passes nil entries/removed, so device_config_entry/device_config_removal
// (which testSchema does not declare either) are never touched.

// rollbackConfigJSON protojson-marshals a minimal DeviceConfig naming
// exactly sensorNames, each at a distinct i2c_address -- realistic
// stored-payload bytes, the same shape GetConfigVersionRow returns
// verbatim (see ConfigVersionRow.ConfigJSON's own doc comment).
func rollbackConfigJSON(t *testing.T, deviceID string, sensorNames ...string) []byte {
	t.Helper()
	cfg := &configpb.DeviceConfig{DeviceId: deviceID}
	for i, name := range sensorNames {
		cfg.Sensors = append(cfg.Sensors, &configpb.SensorConfig{Name: name, I2CAddress: uint32(0x20 + i)})
	}
	b, err := protojson.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config for sensors %v: %v", sensorNames, err)
	}
	return b
}

// deviceConfigRowSnapshot is every column of one device_config row --
// TestRollback_OriginalVersionRowUnchanged compares this before and after
// the rollback insert to prove the original row was never touched.
type deviceConfigRowSnapshot struct {
	configID           int64
	boardID            int64
	version            int64
	configJSON         []byte
	accepted           bool
	acked              bool // acked_at IS NOT NULL
	rejectionReason    *string
	pushGroupID        *int64
	derivedFromVersion *int64
}

func snapshotDeviceConfigRow(t *testing.T, pool *pgxpool.Pool, boardID, version int64) deviceConfigRowSnapshot {
	t.Helper()
	var s deviceConfigRowSnapshot
	var ackedAt any
	if err := pool.QueryRow(context.Background(), `
		SELECT config_id, board_id, version, config_json, accepted, acked_at, rejection_reason, push_group_id, derived_from_version
		FROM device_config WHERE board_id = $1 AND version = $2
	`, boardID, version).Scan(&s.configID, &s.boardID, &s.version, &s.configJSON, &s.accepted, &ackedAt, &s.rejectionReason, &s.pushGroupID, &s.derivedFromVersion); err != nil {
		t.Fatalf("snapshot device_config board=%d version=%d: %v", boardID, version, err)
	}
	s.acked = ackedAt != nil
	return s
}

// TestRollback_RestoresByteIdenticalPayloadAndDesiredState is this task's
// own named load-bearing test: push v1..v4 (each changing the sensor set),
// roll back to v3 (standing in for "v47" -- the exact version numbers
// don't matter, only that it is neither the first nor the last of a
// growing sequence), and assert the newly created version's stored
// payload is byte-identical to v3's own stored payload and the resulting
// desired state (its decoded sensors) is v3's.
//
// "Byte-identical" is checked against v3's own config_json as read back
// from Postgres (via GetConfigVersionRow), not against the literal bytes
// protojson.Marshal produced when v3 was seeded: JSONB canonicalizes text
// on write (re-orders keys, changes whitespace), so what a later SELECT
// returns is never bit-for-bit what was INSERTed -- true of the original
// v3 row just as much as any other. The guarantee FR40 actually rests on
// (and ConfigVersionRow.ConfigJSON's own doc comment states) is narrower
// and just as load-bearing: the rollback path never re-marshals the
// decoded proto, it re-inserts GetConfigVersionRow's own returned bytes
// verbatim -- so the new row's config_json must equal that exact
// already-canonicalized value, not merely be semantically equivalent to
// it.
func TestRollback_RestoresByteIdenticalPayloadAndDesiredState(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID, err := repo.GetOrCreateBoard(ctx, "device-rollback-restore")
	if err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}

	v1JSON := rollbackConfigJSON(t, "device-rollback-restore", "light")
	v2JSON := rollbackConfigJSON(t, "device-rollback-restore", "light", "temperature")
	v3JSON := rollbackConfigJSON(t, "device-rollback-restore", "light", "temperature", "humidity")
	v4JSON := rollbackConfigJSON(t, "device-rollback-restore", "temperature", "humidity") // light dropped

	for _, cfgJSON := range [][]byte{v1JSON, v2JSON, v3JSON, v4JSON} {
		if _, err := repo.InsertDeviceConfigNextVersion(ctx, boardID, cfgJSON, nil, nil, testAuditEntry(), nil, nil); err != nil {
			t.Fatalf("InsertDeviceConfigNextVersion (seeding v1..v4): %v", err)
		}
	}

	const toVersion = 3 // v3's config named "light", "temperature", "humidity"
	row, found, err := repo.GetConfigVersionRow(ctx, "device-rollback-restore", toVersion)
	if err != nil {
		t.Fatalf("GetConfigVersionRow: %v", err)
	}
	if !found {
		t.Fatalf("GetConfigVersionRow found = false for version %d, want true", toVersion)
	}

	derivedFrom := int64(toVersion)
	entry := audit.NewRollbackEntry("alice", audit.ActorKindHuman, nil, toVersion, "restore known-good config", "corr-rollback-1")
	newVersion, err := repo.InsertDeviceConfigNextVersion(ctx, boardID, row.ConfigJSON, nil, nil, entry, nil, &derivedFrom)
	if err != nil {
		t.Fatalf("InsertDeviceConfigNextVersion (rollback write): %v", err)
	}
	if newVersion != 5 {
		t.Fatalf("newVersion = %d, want 5 (the next version after v1..v4)", newVersion)
	}

	newRow := snapshotDeviceConfigRow(t, pool, boardID, newVersion)
	if !bytes.Equal(newRow.configJSON, row.ConfigJSON) {
		t.Errorf("new version's config_json = %s, want byte-identical to v3's own stored payload (read back via GetConfigVersionRow) %s", newRow.configJSON, row.ConfigJSON)
	}
	if newRow.derivedFromVersion == nil || *newRow.derivedFromVersion != toVersion {
		t.Errorf("new version's derived_from_version = %v, want a pointer to %d", newRow.derivedFromVersion, toVersion)
	}

	var restored configpb.DeviceConfig
	if err := protojson.Unmarshal(newRow.configJSON, &restored); err != nil {
		t.Fatalf("unmarshal new version's config_json: %v", err)
	}
	gotNames := map[string]bool{}
	for _, s := range restored.Sensors {
		gotNames[s.Name] = true
	}
	if len(gotNames) != 3 || !gotNames["light"] || !gotNames["temperature"] || !gotNames["humidity"] {
		t.Errorf("restored sensors = %+v, want exactly v3's own {light, temperature, humidity} -- not v4's current set", restored.Sensors)
	}
}

// TestRollback_OriginalVersionRowUnchanged is this task's own
// "v47's row is unchanged" assertion: a rollback never mutates the
// version it rolled back to -- every column of that row, snapshotted
// before and after the rollback insert, must be identical.
func TestRollback_OriginalVersionRowUnchanged(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID, err := repo.GetOrCreateBoard(ctx, "device-rollback-unchanged")
	if err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}

	v1JSON := rollbackConfigJSON(t, "device-rollback-unchanged", "light")
	v2JSON := rollbackConfigJSON(t, "device-rollback-unchanged", "light", "temperature")
	for _, cfgJSON := range [][]byte{v1JSON, v2JSON} {
		if _, err := repo.InsertDeviceConfigNextVersion(ctx, boardID, cfgJSON, nil, nil, testAuditEntry(), nil, nil); err != nil {
			t.Fatalf("InsertDeviceConfigNextVersion (seeding v1..v2): %v", err)
		}
	}

	const toVersion = 1
	before := snapshotDeviceConfigRow(t, pool, boardID, toVersion)

	row, found, err := repo.GetConfigVersionRow(ctx, "device-rollback-unchanged", toVersion)
	if err != nil || !found {
		t.Fatalf("GetConfigVersionRow(%d): found=%v err=%v", toVersion, found, err)
	}
	derivedFrom := int64(toVersion)
	if _, err := repo.InsertDeviceConfigNextVersion(ctx, boardID, row.ConfigJSON, nil, nil, testAuditEntry(), nil, &derivedFrom); err != nil {
		t.Fatalf("InsertDeviceConfigNextVersion (rollback write): %v", err)
	}

	after := snapshotDeviceConfigRow(t, pool, boardID, toVersion)
	// deviceConfigRowSnapshot carries a []byte field (config_json), which
	// makes the struct type itself non-comparable with == / != regardless
	// of value -- compare config_json separately (bytes.Equal) and every
	// other field individually.
	if !bytes.Equal(before.configJSON, after.configJSON) {
		t.Errorf("device_config version %d's config_json changed across the rollback:\nbefore = %s\nafter  = %s", toVersion, before.configJSON, after.configJSON)
	}
	if before.configID != after.configID {
		t.Errorf("config_id changed across the rollback: before=%d after=%d", before.configID, after.configID)
	}
	if before.boardID != after.boardID {
		t.Errorf("board_id changed across the rollback: before=%d after=%d", before.boardID, after.boardID)
	}
	if before.version != after.version {
		t.Errorf("version changed across the rollback: before=%d after=%d", before.version, after.version)
	}
	if before.accepted != after.accepted {
		t.Errorf("accepted changed across the rollback: before=%v after=%v", before.accepted, after.accepted)
	}
	if before.acked != after.acked {
		t.Errorf("acked_at (IS NOT NULL) changed across the rollback: before=%v after=%v", before.acked, after.acked)
	}
	if !equalStringPtr(before.rejectionReason, after.rejectionReason) {
		t.Errorf("rejection_reason changed across the rollback: before=%v after=%v", before.rejectionReason, after.rejectionReason)
	}
	if !equalInt64Ptr(before.pushGroupID, after.pushGroupID) {
		t.Errorf("push_group_id changed across the rollback: before=%v after=%v", before.pushGroupID, after.pushGroupID)
	}
	if !equalInt64Ptr(before.derivedFromVersion, after.derivedFromVersion) {
		t.Errorf("derived_from_version changed across the rollback: before=%v after=%v", before.derivedFromVersion, after.derivedFromVersion)
	}
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func equalInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// TestRollback_AnyVersionFetchable_RegardlessOfAccepted is FR35.2's own
// real-SQL half: GetConfigVersionRow does not filter on accepted --
// rolling back to a version that was never accepted must still find it.
func TestRollback_AnyVersionFetchable_RegardlessOfAccepted(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	boardID, err := repo.GetOrCreateBoard(ctx, "device-rollback-never-accepted")
	if err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}
	cfgJSON := rollbackConfigJSON(t, "device-rollback-never-accepted", "light")
	if _, err := repo.InsertDeviceConfigNextVersion(ctx, boardID, cfgJSON, nil, nil, testAuditEntry(), nil, nil); err != nil {
		t.Fatalf("InsertDeviceConfigNextVersion: %v", err)
	}
	// accepted is never flipped to TRUE here -- this version was never
	// accepted (the same shape as a rejected or still-pending version).

	row, found, err := repo.GetConfigVersionRow(ctx, "device-rollback-never-accepted", 1)
	if err != nil {
		t.Fatalf("GetConfigVersionRow: %v", err)
	}
	if !found {
		t.Fatal("GetConfigVersionRow found = false for a never-accepted version, want true (FR35.2: any version is fetchable)")
	}
	if row.Accepted {
		t.Error("row.Accepted = true, want false (this version was never accepted -- test setup error)")
	}
}

// TestRollback_AuditedWithReasonSourceAndNewVersion is FR8/FR40's own
// assertion: a rollback's audit row carries the reason, the source
// version and the new version, in the same transaction as the write.
func TestRollback_AuditedWithReasonSourceAndNewVersion(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID, err := repo.GetOrCreateBoard(ctx, "device-rollback-audit")
	if err != nil {
		t.Fatalf("GetOrCreateBoard: %v", err)
	}
	cfgJSON := rollbackConfigJSON(t, "device-rollback-audit", "light")
	if _, err := repo.InsertDeviceConfigNextVersion(ctx, boardID, cfgJSON, nil, nil, testAuditEntry(), nil, nil); err != nil {
		t.Fatalf("InsertDeviceConfigNextVersion (seed v1): %v", err)
	}

	const toVersion = 1
	const reason = "revert accidental sensor removal"
	derivedFrom := int64(toVersion)
	entry := audit.NewRollbackEntry("alice", audit.ActorKindHuman, nil, toVersion, reason, "corr-rollback-audit")
	newVersion, err := repo.InsertDeviceConfigNextVersion(ctx, boardID, cfgJSON, nil, nil, entry, nil, &derivedFrom)
	if err != nil {
		t.Fatalf("InsertDeviceConfigNextVersion (rollback write): %v", err)
	}

	var action, entityKind, entityID, auditReason string
	err = pool.QueryRow(ctx, `
		SELECT action, entity_kind, entity_id, reason FROM audit_log WHERE action = $1
	`, audit.ActionRollback).Scan(&action, &entityKind, &entityID, &auditReason)
	if err != nil {
		t.Fatalf("query audit_log for action=Rollback: %v", err)
	}
	if entityKind != "device_config" {
		t.Errorf("entity_kind = %q, want %q", entityKind, "device_config")
	}
	wantEntityID := strconv.FormatInt(newVersion, 10)
	if entityID != wantEntityID {
		t.Errorf("entity_id = %q, want %q (the new version InsertDeviceConfigNextVersion assigned)", entityID, wantEntityID)
	}
	if !strings.Contains(auditReason, "1") || !strings.Contains(auditReason, reason) {
		t.Errorf("reason = %q, want it to name the source version (1) and the caller's stated reason %q", auditReason, reason)
	}
}
