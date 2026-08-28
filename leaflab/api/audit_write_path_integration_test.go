//go:build integration

// Real-Postgres integration coverage for FR8/NFR6.2's transactional
// write-path recording added in this task: auditedWrite (repository.go)
// commits a write and its audit row together, a failed write leaves no
// audit row, a non-human actor is representable and queryable (FR8.3), and
// FR8.2's "re-send that writes no config row" is still audited even though
// the real re-send RPC (FR42) doesn't exist yet -- covered here with a
// standalone Auditor.Record call standing in for it. The append-only
// trigger itself (NFR6.2/NFR6.3) is a migration-level guarantee and is
// covered against the real migration in
// //leaflab/migrate:audit_log_migration_integration_test, not here --
// testSchema's audit_log (dbtest_helpers_integration_test.go) is schema-only
// and does not reproduce the trigger/REVOKE.
//
// Shared fixtures (testSchema, newTestRepository, countRows, insertBoard,
// testAuditEntry) live in dbtest_helpers_integration_test.go. See
// //libs/go/dbtest's README for how to run integration tests like this one.
package main

import (
	"context"
	"testing"

	"github.com/whale-net/everything/leaflab/api/audit"
)

// TestInsertDeviceConfigNextVersion_CommitsWriteAndAuditRowTogether proves
// the success half of NFR6.2's "a committed write always has exactly one
// [audit row]": InsertDeviceConfigNextVersion's device_config INSERT and its
// audit_log INSERT land in the same transaction, and the audit row carries
// the fields FR8.1 requires -- including entity_id filled in with the
// version only known once the write ran.
func TestInsertDeviceConfigNextVersion_CommitsWriteAndAuditRowTogether(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "device-audit-commit")

	entry := audit.Entry{
		ActorSubject:  "owner@example.com",
		ActorKind:     audit.ActorKindHuman,
		Action:        "PushConfig",
		EntityKind:    "device_config",
		CorrelationID: "corr-push-1",
	}

	version, err := repo.InsertDeviceConfigNextVersion(ctx, boardID, []byte(`{}`), nil, nil, entry, nil)
	if err != nil {
		t.Fatalf("InsertDeviceConfigNextVersion: %v", err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}

	if n := countRows(t, pool, "device_config"); n != 1 {
		t.Fatalf("device_config row count = %d, want 1", n)
	}
	if n := countRows(t, pool, "audit_log"); n != 1 {
		t.Fatalf("audit_log row count after a successful write = %d, want exactly 1", n)
	}

	var (
		actorSubject, actorKind, action, entityKind, entityID, correlationID string
	)
	err = pool.QueryRow(ctx, `
		SELECT actor_subject, actor_kind, action, entity_kind, entity_id, correlation_id
		FROM audit_log
	`).Scan(&actorSubject, &actorKind, &action, &entityKind, &entityID, &correlationID)
	if err != nil {
		t.Fatalf("read audit_log row: %v", err)
	}
	if actorSubject != entry.ActorSubject {
		t.Errorf("actor_subject = %q, want %q", actorSubject, entry.ActorSubject)
	}
	if actorKind != string(entry.ActorKind) {
		t.Errorf("actor_kind = %q, want %q", actorKind, entry.ActorKind)
	}
	if action != entry.Action {
		t.Errorf("action = %q, want %q", action, entry.Action)
	}
	if entityKind != entry.EntityKind {
		t.Errorf("entity_kind = %q, want %q", entityKind, entry.EntityKind)
	}
	if entityID != "1" {
		t.Errorf("entity_id = %q, want %q (the assigned version, filled in after the write ran)", entityID, "1")
	}
	if correlationID != entry.CorrelationID {
		t.Errorf("correlation_id = %q, want %q", correlationID, entry.CorrelationID)
	}
}

// TestInsertDeviceConfigNextVersion_FailedWriteLeavesNoAuditRow proves the
// failure half of NFR6.2: a write that fails mid-transaction (here, a
// device_config INSERT that violates the board_id foreign key because the
// board was never created) leaves neither the write's own row nor an audit
// row -- both roll back together, not just the write.
func TestInsertDeviceConfigNextVersion_FailedWriteLeavesNoAuditRow(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	const neverInsertedBoardID = int64(999999)

	_, err := repo.InsertDeviceConfigNextVersion(ctx, neverInsertedBoardID, []byte(`{}`), nil, nil, testAuditEntry(), nil)
	if err == nil {
		t.Fatal("InsertDeviceConfigNextVersion against a nonexistent board_id succeeded, want a foreign-key failure")
	}

	if n := countRows(t, pool, "device_config"); n != 0 {
		t.Errorf("device_config row count after a failed write = %d, want 0", n)
	}
	if n := countRows(t, pool, "audit_log"); n != 0 {
		t.Errorf("audit_log row count after a failed write = %d, want 0 (rollback must discard the audit row along with the write)", n)
	}
}

// TestRetireBoard_AlreadyRetired_LeavesNoAdditionalAuditRow is the same
// failure-half assertion as above, exercised through RetireBoard's own
// application-level refusal (not a DB constraint): a second RetireBoard call
// on an already-retired board returns ErrBoardAlreadyRetired before an audit
// row is ever built, so the audit_log row count from the first (successful)
// call must not grow.
func TestRetireBoard_AlreadyRetired_LeavesNoAdditionalAuditRow(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "device-retire-audit")

	if err := repo.RetireBoard(ctx, boardID, testAuditEntry()); err != nil {
		t.Fatalf("first RetireBoard call: %v", err)
	}
	if n := countRows(t, pool, "audit_log"); n != 1 {
		t.Fatalf("audit_log row count after the first (successful) RetireBoard call = %d, want 1", n)
	}

	if err := repo.RetireBoard(ctx, boardID, testAuditEntry()); err == nil {
		t.Fatal("second RetireBoard call on an already-retired board succeeded, want ErrBoardAlreadyRetired")
	}
	if n := countRows(t, pool, "audit_log"); n != 1 {
		t.Errorf("audit_log row count after a second, refused RetireBoard call = %d, want still 1 (no audit row for a refused write)", n)
	}
}

// TestRetireBoard_NonHumanActorIsRepresentableAndQueryable is FR8.3's
// end-to-end check: "actor" is not defined as something only a human can
// be -- an Entry built with ActorKindSystem (e.g. a scheduled retirement
// job with no human principal behind it) is written and queryable by
// actor_kind, not just accepted silently.
func TestRetireBoard_NonHumanActorIsRepresentableAndQueryable(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	boardID := insertBoard(t, pool, "device-system-retire")

	entry := audit.Entry{
		ActorSubject: "scheduled-retirement-job",
		ActorKind:    audit.ActorKindSystem,
		Action:       "RetireBoard",
		EntityKind:   "board",
	}
	if err := repo.RetireBoard(ctx, boardID, entry); err != nil {
		t.Fatalf("RetireBoard: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM audit_log
		WHERE actor_subject = $1 AND actor_kind = $2 AND action = $3 AND entity_kind = $4
	`, entry.ActorSubject, string(audit.ActorKindSystem), entry.Action, entry.EntityKind).Scan(&n); err != nil {
		t.Fatalf("query audit_log by actor_kind = 'system': %v", err)
	}
	if n != 1 {
		t.Errorf("audit_log rows matching the non-human actor = %d, want exactly 1", n)
	}
}

// TestAuditor_RecordsReSendWithNoDeviceConfigRow covers FR8.2's explicitly
// named case -- "re-sends that write no config row" -- using a stub, per
// this task's Testing section: the real re-send RPC is FR42 (Phase 4) and
// does not exist yet, but Auditor.Record itself must not require an
// accompanying data write to succeed. PostgresAuditor is constructed
// directly over the pool (not a tx participating in some other write),
// mirroring how a future re-send handler would call it standalone.
func TestAuditor_RecordsReSendWithNoDeviceConfigRow(t *testing.T) {
	_, pool := newTestRepository(t)
	ctx := context.Background()

	entry := audit.Entry{
		ActorSubject: "resend-job",
		ActorKind:    audit.ActorKindSystem,
		Action:       "ReSendConfig",
		EntityKind:   "device_config",
		// EntityID deliberately nil: FR8.2's named case is a re-send that
		// writes no device_config row, so there is no new entity to point at.
	}

	if err := audit.NewPostgresAuditor(pool).Record(ctx, entry); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if n := countRows(t, pool, "device_config"); n != 0 {
		t.Errorf("device_config row count after a re-send audit = %d, want 0 (FR8.2's named case: no config row written)", n)
	}
	if n := countRows(t, pool, "audit_log"); n != 1 {
		t.Fatalf("audit_log row count after the re-send audit = %d, want 1", n)
	}

	var actorSubject, action, entityKind string
	var entityID *string
	if err := pool.QueryRow(ctx, `
		SELECT actor_subject, action, entity_kind, entity_id FROM audit_log
	`).Scan(&actorSubject, &action, &entityKind, &entityID); err != nil {
		t.Fatalf("read audit_log row: %v", err)
	}
	if actorSubject != entry.ActorSubject {
		t.Errorf("actor_subject = %q, want %q", actorSubject, entry.ActorSubject)
	}
	if action != entry.Action {
		t.Errorf("action = %q, want %q", action, entry.Action)
	}
	if entityKind != entry.EntityKind {
		t.Errorf("entity_kind = %q, want %q", entityKind, entry.EntityKind)
	}
	if entityID != nil {
		t.Errorf("entity_id = %v, want nil (no device_config row was written)", *entityID)
	}
}
