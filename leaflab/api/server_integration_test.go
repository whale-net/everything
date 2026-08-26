//go:build integration

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/dbtest"
)

// auditTestSchema provides the minimal schema for audit testing
const auditTestSchema = `
CREATE TABLE IF NOT EXISTS household (
    household_id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS audit_record (
    audit_id BIGSERIAL PRIMARY KEY,
    actor_subject VARCHAR(255) NOT NULL,
    target_household_id BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    action VARCHAR(64) NOT NULL,
    entity_type VARCHAR(64) NOT NULL,
    entity_id BIGINT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason TEXT,
    config_version BIGINT,
    i2c_address SMALLINT,
    mux_path JSONB
);

-- Append-only trigger: reject UPDATE and DELETE operations
CREATE OR REPLACE FUNCTION audit_record_append_only()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' OR TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'audit_record is append-only: UPDATE and DELETE are not permitted';
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_record_no_modify
BEFORE UPDATE OR DELETE ON audit_record
FOR EACH ROW EXECUTE FUNCTION audit_record_append_only();

-- Indexes for common query patterns
CREATE INDEX idx_audit_record_household_occurred
    ON audit_record(target_household_id, occurred_at DESC);

CREATE INDEX idx_audit_record_action_occurred
    ON audit_record(action, occurred_at DESC);

CREATE INDEX idx_audit_record_entity
    ON audit_record(entity_type, entity_id, occurred_at DESC);

CREATE INDEX idx_audit_record_actor_occurred
    ON audit_record(actor_subject, occurred_at DESC);

-- Insert a test household if it doesn't exist
INSERT INTO household (name) VALUES ('test-household') ON CONFLICT DO NOTHING;
`

// TestAuditRecord_AppendOnlyTriggerRejectsUpdate verifies that UPDATE on audit_record
// is rejected at the database level by the append-only trigger (NFR6.2, NFR6.3).
func TestAuditRecord_AppendOnlyTriggerRejectsUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: auditTestSchema,
	})
	defer pg.Pool.Close()

	// Create a household for testing
	var householdID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household-update')
		RETURNING household_id
	`).Scan(&householdID)
	if err != nil {
		t.Fatalf("create household: %v", err)
	}

	// Insert an audit record
	var auditID int64
	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO audit_record (actor_subject, target_household_id, action, entity_type, entity_id, reason)
		VALUES ('user-123', $1, 'test_action', 'test_entity', 42, 'test reason')
		RETURNING audit_id
	`, householdID).Scan(&auditID)
	if err != nil {
		t.Fatalf("insert audit record: %v", err)
	}

	// Try to UPDATE the audit record - should fail
	_, err = pg.Pool.Exec(ctx, `
		UPDATE audit_record SET reason = 'modified' WHERE audit_id = $1
	`, auditID)
	if err == nil {
		t.Fatal("UPDATE audit_record: expected error, got nil (trigger did not reject UPDATE)")
	}

	// Verify the error message contains 'append-only'
	if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("UPDATE audit_record error message should mention 'append-only', got: %v", err)
	}

	t.Logf("UPDATE correctly rejected by trigger: %v", err)
}

// TestAuditRecord_AppendOnlyTriggerRejectsDelete verifies that DELETE on audit_record
// is rejected at the database level by the append-only trigger (NFR6.2, NFR6.3).
func TestAuditRecord_AppendOnlyTriggerRejectsDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: auditTestSchema,
	})
	defer pg.Pool.Close()

	// Create a household
	var householdID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household-delete')
		RETURNING household_id
	`).Scan(&householdID)
	if err != nil {
		t.Fatalf("create household: %v", err)
	}

	// Insert an audit record
	var auditID int64
	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO audit_record (actor_subject, target_household_id, action, entity_type, entity_id, reason)
		VALUES ('user-123', $1, 'test_action', 'test_entity', 42, 'test reason')
		RETURNING audit_id
	`, householdID).Scan(&auditID)
	if err != nil {
		t.Fatalf("insert audit record: %v", err)
	}

	// Try to DELETE the audit record - should fail
	_, err = pg.Pool.Exec(ctx, `
		DELETE FROM audit_record WHERE audit_id = $1
	`, auditID)
	if err == nil {
		t.Fatal("DELETE audit_record: expected error, got nil (trigger did not reject DELETE)")
	}

	// Verify the error message contains 'append-only'
	if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("DELETE audit_record error message should mention 'append-only', got: %v", err)
	}

	t.Logf("DELETE correctly rejected by trigger: %v", err)
}

// TestRecordAudit_WritesAuditRecord verifies that RecordAudit correctly writes
// audit records to the database (FR8).
func TestRecordAudit_WritesAuditRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: auditTestSchema,
	})
	defer pg.Pool.Close()

	// Create a household
	var householdID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household-write')
		RETURNING household_id
	`).Scan(&householdID)
	if err != nil {
		t.Fatalf("create household: %v", err)
	}

	// Create a repository and record an audit entry
	repo := NewRepository(pg.Pool)
	err = repo.RecordAudit(ctx, "user-principal-123", householdID, "test_action", "test_entity", 42, "test reason")
	if err != nil {
		t.Fatalf("RecordAudit: %v", err)
	}

	// Verify the record was written
	var auditID int64
	var actorSubject string
	var targetHouseholdID int64
	var action string
	var entityType string
	var entityID int64
	var reason *string

	err = pg.Pool.QueryRow(ctx, `
		SELECT audit_id, actor_subject, target_household_id, action, entity_type, entity_id, reason
		FROM audit_record
		WHERE target_household_id = $1 AND entity_id = $2
		ORDER BY audit_id DESC
		LIMIT 1
	`, householdID, int64(42)).Scan(&auditID, &actorSubject, &targetHouseholdID, &action, &entityType, &entityID, &reason)
	if err != nil {
		t.Fatalf("query audit record: %v", err)
	}

	if actorSubject != "user-principal-123" {
		t.Errorf("actor_subject: expected 'user-principal-123', got %q", actorSubject)
	}
	if targetHouseholdID != householdID {
		t.Errorf("target_household_id: expected %d, got %d", householdID, targetHouseholdID)
	}
	if action != "test_action" {
		t.Errorf("action: expected 'test_action', got %q", action)
	}
	if entityType != "test_entity" {
		t.Errorf("entity_type: expected 'test_entity', got %q", entityType)
	}
	if entityID != 42 {
		t.Errorf("entity_id: expected 42, got %d", entityID)
	}
	if reason == nil || *reason != "test reason" {
		t.Errorf("reason: expected 'test reason', got %v", reason)
	}

	t.Logf("Audit record correctly written: audit_id=%d", auditID)
}

// TestRecordAuditWithConfig_WritesFullContext verifies that RecordAuditWithConfig
// correctly writes audit records with config-specific fields (FR8 for config operations).
func TestRecordAuditWithConfig_WritesFullContext(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: auditTestSchema,
	})
	defer pg.Pool.Close()

	// Create a household
	var householdID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household-config')
		RETURNING household_id
	`).Scan(&householdID)
	if err != nil {
		t.Fatalf("create household: %v", err)
	}

	// Record with config context
	repo := NewRepository(pg.Pool)
	configVersion := int64(5)
	i2cAddr := uint32(0x40)
	muxPath := []byte(`{"path": "main"}`)
	err = repo.RecordAuditWithConfig(ctx, "config-agent", householdID, "apply_config_region", "sensor", 99,
		&configVersion, &i2cAddr, muxPath, "applied to sensor")
	if err != nil {
		t.Fatalf("RecordAuditWithConfig: %v", err)
	}

	// Verify the record
	var cvResult *int64
	var i2cResult *uint32
	var muxPathResult []byte

	err = pg.Pool.QueryRow(ctx, `
		SELECT config_version, i2c_address, mux_path
		FROM audit_record
		WHERE target_household_id = $1 AND entity_id = $2
		ORDER BY audit_id DESC
		LIMIT 1
	`, householdID, int64(99)).Scan(&cvResult, &i2cResult, &muxPathResult)
	if err != nil {
		t.Fatalf("query audit record with config: %v", err)
	}

	if cvResult == nil || *cvResult != 5 {
		t.Errorf("config_version: expected 5, got %v", cvResult)
	}
	if i2cResult == nil || *i2cResult != 0x40 {
		t.Errorf("i2c_address: expected 0x40, got %v", i2cResult)
	}
	if string(muxPathResult) != `{"path": "main"}` {
		t.Errorf("mux_path: expected '{\"path\": \"main\"}', got %q", string(muxPathResult))
	}

	t.Log("Config audit record correctly written with full context")
}

// TestListActivityRecords_ReturnsRecordsInReverseChronological verifies that
// ListActivityRecords returns audit records in reverse chronological order (most recent first).
func TestListActivityRecords_ReturnsRecordsInReverseChronological(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: auditTestSchema,
	})
	defer pg.Pool.Close()

	// Create a household
	var householdID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household-chrono')
		RETURNING household_id
	`).Scan(&householdID)
	if err != nil {
		t.Fatalf("create household: %v", err)
	}

	// Create a repository and write several audit records
	repo := NewRepository(pg.Pool)
	for i := 1; i <= 3; i++ {
		err = repo.RecordAudit(ctx, "user-123", householdID, "action", "entity", int64(i), "")
		if err != nil {
			t.Fatalf("RecordAudit %d: %v", i, err)
		}
		// Small delay to ensure different timestamps
		time.Sleep(10 * time.Millisecond)
	}

	// Query the records
	records, _, err := repo.ListActivityRecords(ctx, householdID, "", 50)
	if err != nil {
		t.Fatalf("ListActivityRecords: %v", err)
	}

	if len(records) != 3 {
		t.Errorf("expected 3 records, got %d", len(records))
	}

	// Verify reverse chronological order (most recent first)
	for i := 0; i < len(records)-1; i++ {
		if records[i].AuditID <= records[i+1].AuditID {
			t.Errorf("records not in reverse chronological order: record[%d].AuditID=%d, record[%d].AuditID=%d",
				i, records[i].AuditID, i+1, records[i+1].AuditID)
		}
	}

	// Verify entities are in reverse order (3, 2, 1)
	expectedOrder := []int64{3, 2, 1}
	for i, expected := range expectedOrder {
		if records[i].EntityID != expected {
			t.Errorf("record[%d]: expected entity_id %d, got %d", i, expected, records[i].EntityID)
		}
	}

	t.Log("Records correctly returned in reverse chronological order")
}

// TestListActivityRecords_HouseholdScoping verifies that ListActivityRecords
// only returns records for the requested household (FR5 scoping).
func TestListActivityRecords_HouseholdScoping(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: auditTestSchema,
	})
	defer pg.Pool.Close()

	// Create two households
	var household1ID, household2ID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('household-1')
		RETURNING household_id
	`).Scan(&household1ID)
	if err != nil {
		t.Fatalf("create household 1: %v", err)
	}

	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('household-2')
		RETURNING household_id
	`).Scan(&household2ID)
	if err != nil {
		t.Fatalf("create household 2: %v", err)
	}

	// Write audit records to both households
	repo := NewRepository(pg.Pool)
	for i := 1; i <= 2; i++ {
		err = repo.RecordAudit(ctx, "user-123", household1ID, "action", "entity", int64(i), "")
		if err != nil {
			t.Fatalf("RecordAudit household1 %d: %v", i, err)
		}
		err = repo.RecordAudit(ctx, "user-456", household2ID, "action", "entity", int64(i*10), "")
		if err != nil {
			t.Fatalf("RecordAudit household2 %d: %v", i, err)
		}
	}

	// Query records for household 1
	records1, _, err := repo.ListActivityRecords(ctx, household1ID, "", 50)
	if err != nil {
		t.Fatalf("ListActivityRecords household1: %v", err)
	}

	// Query records for household 2
	records2, _, err := repo.ListActivityRecords(ctx, household2ID, "", 50)
	if err != nil {
		t.Fatalf("ListActivityRecords household2: %v", err)
	}

	if len(records1) != 2 {
		t.Errorf("household1: expected 2 records, got %d", len(records1))
	}
	if len(records2) != 2 {
		t.Errorf("household2: expected 2 records, got %d", len(records2))
	}

	// Verify household 1 records
	for _, rec := range records1 {
		if rec.TargetHouseholdID != household1ID {
			t.Errorf("household1 record: expected target_household_id %d, got %d", household1ID, rec.TargetHouseholdID)
		}
	}

	// Verify household 2 records
	for _, rec := range records2 {
		if rec.TargetHouseholdID != household2ID {
			t.Errorf("household2 record: expected target_household_id %d, got %d", household2ID, rec.TargetHouseholdID)
		}
	}

	t.Log("Household scoping correctly enforced")
}

// TestListActivityRecords_KeysetPagination verifies that keyset pagination
// works correctly (FR61 pagination).
func TestListActivityRecords_KeysetPagination(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: auditTestSchema,
	})
	defer pg.Pool.Close()

	// Create a household
	var householdID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household-paginate')
		RETURNING household_id
	`).Scan(&householdID)
	if err != nil {
		t.Fatalf("create household: %v", err)
	}

	// Write 10 audit records
	repo := NewRepository(pg.Pool)
	for i := 1; i <= 10; i++ {
		err = repo.RecordAudit(ctx, "user-123", householdID, "action", "entity", int64(i), "")
		if err != nil {
			t.Fatalf("RecordAudit %d: %v", i, err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Query first page with size 3
	page1, nextToken1, err := repo.ListActivityRecords(ctx, householdID, "", 3)
	if err != nil {
		t.Fatalf("ListActivityRecords page 1: %v", err)
	}

	if len(page1) != 3 {
		t.Errorf("page 1: expected 3 records, got %d", len(page1))
	}

	if nextToken1 == "" {
		t.Error("page 1: expected nextToken, got empty")
	}

	// Query second page using next token
	page2, nextToken2, err := repo.ListActivityRecords(ctx, householdID, nextToken1, 3)
	if err != nil {
		t.Fatalf("ListActivityRecords page 2: %v", err)
	}

	if len(page2) != 3 {
		t.Errorf("page 2: expected 3 records, got %d", len(page2))
	}

	// Verify no duplicates between pages
	for _, rec1 := range page1 {
		for _, rec2 := range page2 {
			if rec1.AuditID == rec2.AuditID {
				t.Errorf("audit_id %d appears in both pages", rec1.AuditID)
			}
		}
	}

	// Query third page: 10 records, page size 3 → pages of 3,3,3,1. One record remains
	// after two pages of 3, so page 3 is a full page of 3 with a next token.
	page3, nextToken3, err := repo.ListActivityRecords(ctx, householdID, nextToken2, 3)
	if err != nil {
		t.Fatalf("ListActivityRecords page 3: %v", err)
	}

	if len(page3) != 3 {
		t.Errorf("page 3: expected 3 records, got %d", len(page3))
	}

	if nextToken3 == "" {
		t.Error("page 3: expected nextToken (one record remains), got empty")
	}

	// Query final page: the last remaining record, with no further token.
	page4, nextToken4, err := repo.ListActivityRecords(ctx, householdID, nextToken3, 3)
	if err != nil {
		t.Fatalf("ListActivityRecords page 4: %v", err)
	}

	if len(page4) != 1 {
		t.Errorf("page 4: expected 1 remaining record, got %d", len(page4))
	}

	if nextToken4 != "" {
		t.Errorf("page 4: expected empty nextToken (no more pages), got %q", nextToken4)
	}

	t.Logf("Pagination works correctly: page1=%d, page2=%d, page3=%d, page4=%d", len(page1), len(page2), len(page3), len(page4))
}

// TestAuditRecord_NonHumanActor verifies that a non-human actor (e.g., a system service)
// can be recorded as an audit actor (FR8.3).
func TestAuditRecord_NonHumanActor(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: auditTestSchema,
	})
	defer pg.Pool.Close()

	// Create a household
	var householdID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household-nonhuman')
		RETURNING household_id
	`).Scan(&householdID)
	if err != nil {
		t.Fatalf("create household: %v", err)
	}

	// Record with a non-human actor (service account)
	repo := NewRepository(pg.Pool)
	nonHumanActor := "system:background-processor"
	err = repo.RecordAudit(ctx, nonHumanActor, householdID, "scheduled_task", "sensor_reading", 42, "daily maintenance")
	if err != nil {
		t.Fatalf("RecordAudit with non-human actor: %v", err)
	}

	// Verify the non-human actor was recorded
	var recordedActor string
	err = pg.Pool.QueryRow(ctx, `
		SELECT actor_subject FROM audit_record
		WHERE target_household_id = $1 AND actor_subject = $2
	`, householdID, nonHumanActor).Scan(&recordedActor)
	if err != nil {
		t.Fatalf("query audit record: %v", err)
	}

	if recordedActor != nonHumanActor {
		t.Errorf("actor_subject: expected %q, got %q", nonHumanActor, recordedActor)
	}

	t.Logf("Non-human actor correctly recorded: %s", nonHumanActor)
}
