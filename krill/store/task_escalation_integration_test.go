//go:build integration

// Real-Postgres coverage for 016_escalation_axis.up.sql and
// task_escalation.go (issue #2868, root plan #2851's M5, FR2's index
// rule, NFR1-NFR3) -- this file is deliberately `package store`, not
// `package store_test` like every sibling *_integration_test.go in this
// package: recordEscalationTx and forceCloseClaimTx are unexported (no
// verb calls them yet -- that is FR2/FR3/FR7/FR8/FR9, #2870-#2873, each
// its own later task), so exercising them at all requires same-package
// access. Because these fixture helpers duplicate task_integration_test.go's
// shape rather than reuse it (that file's helpers live in the sibling
// store_test package, not importable from here), they carry an `esc`
// prefix throughout.
//
// Covers: migration 016 up-then-down-then-up idempotency; scope_id and
// both LB4 subject pairs NOT NULL on all three new tables (NFR1, NFR3);
// each widened CHECK (task_claim.release_reason, task_attempt.outcome)
// accepting every new value and rejecting an unknown one; the Go enum and
// DB CHECK for EscalationReason/InterventionAction/NoteLifecycleStatus
// rejecting the identical unknown-value set; recordEscalationTx's
// one-active-escalation rule; forceCloseClaimTx's claim-closing behavior
// and the post-force-close zombie-heartbeat rejection; the real-Postgres
// concurrency proof that an escalated or cancelled task is excluded by
// task_claimable_idx itself, not only ClaimTask's Go-level check; and the
// plain regression that an ordinary task is still claimable exactly as M4
// leaves it.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_escalation_integration_test --test_output=all
package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// assertCheckViolation is this file's own shared "was this a CHECK
// violation" assertion (SQLSTATE 23514), mirroring
// entities_integration_test.go's unique-violation (23505) assertion
// shape.
func assertCheckViolation(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "expected a *pgconn.PgError in the error chain, got %v", err)
	assert.Equal(t, "23514", pgErr.Code, "expected a check_violation")
}

// assertNotNullViolation is this file's own shared "was this a NOT NULL
// violation" assertion (SQLSTATE 23502).
func assertNotNullViolation(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "expected a *pgconn.PgError in the error chain, got %v", err)
	assert.Equal(t, "23502", pgErr.Code, "expected a not_null_violation")
}

func escTestStore(t *testing.T) (*Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return New(db.Pool), db
}

func escTestScope(t *testing.T, ctx context.Context, db *dbtest.Postgres) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "scope-esc-"+uuid.NewString()).Scan(&id))
	return id
}

func escTestSubject(sub string) Subject {
	return Subject{Iss: "https://issuer.example.com", Sub: sub, Kind: SubjectKindService}
}

// escTestMilestone seeds a Product and one uncut milestone -- the minimal
// delivery-axis target CreateTask needs, mirroring
// task_integration_test.go's taskTestWorld but without the milepebble/
// Feature/Requirement fixtures this file's tests never touch.
func escTestMilestone(t *testing.T, ctx context.Context, s *Store, scopeID uuid.UUID, self Subject) uuid.UUID {
	t.Helper()
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M-esc", "ship it", nil, self, self)
	require.NoError(t, err)
	return milestone.ID
}

func escTestTask(t *testing.T, ctx context.Context, s *Store, scopeID, milestoneID uuid.UUID, title string, self Subject) Task {
	t.Helper()
	task, err := s.Tasks().CreateTask(ctx, CreateTaskParams{
		ScopeID:      scopeID,
		MilestoneID:  milestoneID,
		Title:        title,
		LaneSequence: []Lane{LaneScaffold, LaneImplementation, LaneTesting, LaneValidation, LaneDone},
		StartingLane: LaneScaffold,
		Acting:       self,
		OnBehalfOf:   self,
	})
	require.NoError(t, err)
	return task
}

func escTestSession(t *testing.T, ctx context.Context, db *dbtest.Postgres, scopeID uuid.UUID, self Subject) SessionID {
	t.Helper()
	sessions := NewSessionStore(db.Pool)
	id, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)
	return id
}

// TestMigration016_UpDownUp_Clean is the Testing section's first item:
// applying every migration (including 016), rolling every migration back,
// then re-applying, all against a fresh database, must be clean -- and
// the restored task_claimable_idx must be 016's own down migration's
// single-predicate shape, not the three-predicate one it replaced.
func TestMigration016_UpDownUp_Clean(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	tableExists := func(name string) bool {
		var count int
		require.NoError(t, db.Pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.tables WHERE table_name = $1
		`, name).Scan(&count))
		return count == 1
	}

	require.NoError(t, runner.Up(), "first Up must apply every migration, including 016, cleanly")
	assert.True(t, tableExists("task_escalation_event"))
	assert.True(t, tableExists("task_intervention_event"))
	assert.True(t, tableExists("task_note_lifecycle_event"))

	var indexDef string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT indexdef FROM pg_indexes WHERE indexname = 'task_claimable_idx'
	`).Scan(&indexDef))
	assert.Contains(t, indexDef, "current_escalation_id")
	assert.Contains(t, indexDef, "cancelled_at")

	// Runner.Down() rolls back every migration in the schema, not just
	// this one (libs/go/migrate's own doc comment) -- Steps(-1) is the
	// single-migration-down primitive this test actually needs, to prove
	// 016's own down.sql, not migration 001's.
	require.NoError(t, runner.Steps(-1), "stepping down one migration (016's own down.sql) must be clean")
	assert.False(t, tableExists("task_escalation_event"))
	assert.False(t, tableExists("task_intervention_event"))
	assert.False(t, tableExists("task_note_lifecycle_event"))

	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT indexdef FROM pg_indexes WHERE indexname = 'task_claimable_idx'
	`).Scan(&indexDef))
	assert.NotContains(t, indexDef, "current_escalation_id", "the down migration must restore 015's single-predicate index")

	require.NoError(t, runner.Steps(1), "the second Up (up-down-up) must re-apply 016 cleanly")
	assert.True(t, tableExists("task_escalation_event"))
	assert.True(t, tableExists("task_intervention_event"))
	assert.True(t, tableExists("task_note_lifecycle_event"))
}

// TestNewTables_ScopeAndSubjectColumns_NotNull is the Testing section's
// second item (NFR1, NFR3): scope_id and both LB4 subject-pair columns
// are NOT NULL on all three tables 016 creates.
func TestNewTables_ScopeAndSubjectColumns_NotNull(t *testing.T) {
	ctx := context.Background()
	s, db := escTestStore(t)
	scopeID := escTestScope(t, ctx, db)
	self := escTestSubject("agent-1")
	milestoneID := escTestMilestone(t, ctx, s, scopeID, self)
	task := escTestTask(t, ctx, s, scopeID, milestoneID, "T-notnull", self)

	note, err := s.Tasks().RecordNote(ctx, RecordNoteParams{
		ScopeID: scopeID, TaskID: &task.ID, Kind: NoteKindComment, Body: "a note",
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{
			"task_escalation_event.scope_id",
			`INSERT INTO task_escalation_event (scope_id, task_id, reason, counter_value, cap_value, lane_at_escalation, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind) VALUES ($1, $2, 'manual', NULL, NULL, 'Scaffold', 'iss', 'sub', 'service', 'iss', 'sub', 'service')`,
			[]any{nil, task.ID},
		},
		{
			"task_escalation_event.created_by_acting_sub",
			`INSERT INTO task_escalation_event (scope_id, task_id, reason, counter_value, cap_value, lane_at_escalation, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind) VALUES ($1, $2, 'manual', NULL, NULL, 'Scaffold', 'iss', $3, 'service', 'iss', 'sub', 'service')`,
			[]any{scopeID, task.ID, nil},
		},
		{
			"task_escalation_event.created_by_on_behalf_of_sub",
			`INSERT INTO task_escalation_event (scope_id, task_id, reason, counter_value, cap_value, lane_at_escalation, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind) VALUES ($1, $2, 'manual', NULL, NULL, 'Scaffold', 'iss', 'sub', 'service', 'iss', $3, 'service')`,
			[]any{scopeID, task.ID, nil},
		},
		{
			"task_intervention_event.scope_id",
			`INSERT INTO task_intervention_event (scope_id, task_id, action, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind) VALUES ($1, $2, 'cancel', 'iss', 'sub', 'service', 'iss', 'sub', 'service')`,
			[]any{nil, task.ID},
		},
		{
			"task_intervention_event.created_by_acting_sub",
			`INSERT INTO task_intervention_event (scope_id, task_id, action, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind) VALUES ($1, $2, 'cancel', 'iss', $3, 'service', 'iss', 'sub', 'service')`,
			[]any{scopeID, task.ID, nil},
		},
		{
			"task_intervention_event.created_by_on_behalf_of_sub",
			`INSERT INTO task_intervention_event (scope_id, task_id, action, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind) VALUES ($1, $2, 'cancel', 'iss', 'sub', 'service', 'iss', $3, 'service')`,
			[]any{scopeID, task.ID, nil},
		},
		{
			"task_note_lifecycle_event.scope_id",
			`INSERT INTO task_note_lifecycle_event (scope_id, note_id, status, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind) VALUES ($1, $2, 'noted', 'iss', 'sub', 'service', 'iss', 'sub', 'service')`,
			[]any{nil, note.ID},
		},
		{
			"task_note_lifecycle_event.created_by_acting_sub",
			`INSERT INTO task_note_lifecycle_event (scope_id, note_id, status, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind) VALUES ($1, $2, 'noted', 'iss', $3, 'service', 'iss', 'sub', 'service')`,
			[]any{scopeID, note.ID, nil},
		},
		{
			"task_note_lifecycle_event.created_by_on_behalf_of_sub",
			`INSERT INTO task_note_lifecycle_event (scope_id, note_id, status, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind) VALUES ($1, $2, 'noted', 'iss', 'sub', 'service', 'iss', $3, 'service')`,
			[]any{scopeID, note.ID, nil},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := db.Pool.Exec(ctx, c.sql, c.args...)
			assertNotNullViolation(t, err)
		})
	}
}

// TestCheckEnumWidening_TaskClaimReleaseReason is the Testing section's
// third item, task_claim.release_reason half: the three FR7/FR8/FR9
// force-close reasons are accepted, and an unknown value is still
// rejected.
func TestCheckEnumWidening_TaskClaimReleaseReason(t *testing.T) {
	ctx := context.Background()
	s, db := escTestStore(t)
	scopeID := escTestScope(t, ctx, db)
	self := escTestSubject("agent-1")
	milestoneID := escTestMilestone(t, ctx, s, scopeID, self)

	newClaim := func(title string) uuid.UUID {
		task := escTestTask(t, ctx, s, scopeID, milestoneID, title, self)
		sessionID := escTestSession(t, ctx, db, scopeID, self)
		claim, err := s.Tasks().ClaimTask(ctx, ClaimTaskParams{
			ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
		return claim.ID
	}

	for _, reason := range []string{"release", "cancel", "escalate"} {
		t.Run(reason, func(t *testing.T) {
			claimID := newClaim("T-release-reason-" + reason)
			_, err := db.Pool.Exec(ctx, `UPDATE task_claim SET released_at = NOW(), release_reason = $1 WHERE id = $2`, reason, claimID)
			assert.NoError(t, err, "release_reason %q must be accepted by the widened CHECK", reason)
		})
	}

	t.Run("unknown value rejected", func(t *testing.T) {
		claimID := newClaim("T-release-reason-bogus")
		_, err := db.Pool.Exec(ctx, `UPDATE task_claim SET released_at = NOW(), release_reason = 'bogus' WHERE id = $1`, claimID)
		assertCheckViolation(t, err)
	})
}

// TestCheckEnumWidening_TaskAttemptOutcome is the Testing section's third
// item, task_attempt.outcome half: 'released' (FR8) and 'force-closed'
// (FR9) are accepted, and an unknown value is still rejected -- and this
// is the exact set the migration's own comment names as final.
func TestCheckEnumWidening_TaskAttemptOutcome(t *testing.T) {
	ctx := context.Background()
	s, db := escTestStore(t)
	scopeID := escTestScope(t, ctx, db)
	self := escTestSubject("agent-1")
	milestoneID := escTestMilestone(t, ctx, s, scopeID, self)

	newClaim := func(title string) uuid.UUID {
		task := escTestTask(t, ctx, s, scopeID, milestoneID, title, self)
		sessionID := escTestSession(t, ctx, db, scopeID, self)
		claim, err := s.Tasks().ClaimTask(ctx, ClaimTaskParams{
			ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
		return claim.ID
	}

	for _, outcome := range []string{"released", "force-closed"} {
		t.Run(outcome, func(t *testing.T) {
			claimID := newClaim("T-outcome-" + outcome)
			_, err := db.Pool.Exec(ctx, `UPDATE task_attempt SET outcome = $1 WHERE claim_id = $2`, outcome, claimID)
			assert.NoError(t, err, "outcome %q must be accepted by the widened CHECK", outcome)
		})
	}

	t.Run("unknown value rejected", func(t *testing.T) {
		claimID := newClaim("T-outcome-bogus")
		_, err := db.Pool.Exec(ctx, `UPDATE task_attempt SET outcome = 'bogus' WHERE claim_id = $1`, claimID)
		assertCheckViolation(t, err)
	})
}

// TestEscalationReason_GoAndDBLockstep is the Testing section's fourth
// item for EscalationReason: recordEscalationTx (the Go layer) and
// task_escalation_event's own CHECK (the DB layer) accept the identical
// three-value set and reject the identical unknown value.
func TestEscalationReason_GoAndDBLockstep(t *testing.T) {
	ctx := context.Background()
	s, db := escTestStore(t)
	scopeID := escTestScope(t, ctx, db)
	self := escTestSubject("agent-1")
	milestoneID := escTestMilestone(t, ctx, s, scopeID, self)

	counter, cap := 3, 5
	cases := []struct {
		reason          EscalationReason
		counter, capPtr *int
	}{
		{EscalationReasonThrashCap, &counter, &cap},
		{EscalationReasonAttemptCap, &counter, &cap},
		{EscalationReasonManual, nil, nil},
	}
	for _, c := range cases {
		t.Run(string(c.reason)+"/go", func(t *testing.T) {
			task := escTestTask(t, ctx, s, scopeID, milestoneID, "T-reason-"+string(c.reason), self)
			tx, err := db.Pool.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx) //nolint:errcheck
			_, err = recordEscalationTx(ctx, tx, RecordEscalationParams{
				ScopeID: scopeID, TaskID: task.ID, Reason: c.reason,
				CounterValue: c.counter, CapValue: c.capPtr, LaneAtEscalation: LaneScaffold,
				Acting: self, OnBehalfOf: self,
			})
			require.NoError(t, err, "the Go layer must accept %q", c.reason)
			require.NoError(t, tx.Commit(ctx))
		})
	}

	t.Run("unknown reason rejected by the Go layer", func(t *testing.T) {
		task := escTestTask(t, ctx, s, scopeID, milestoneID, "T-reason-unknown-go", self)
		tx, err := db.Pool.Begin(ctx)
		require.NoError(t, err)
		defer tx.Rollback(ctx) //nolint:errcheck
		_, err = recordEscalationTx(ctx, tx, RecordEscalationParams{
			ScopeID: scopeID, TaskID: task.ID, Reason: EscalationReason("bogus"),
			LaneAtEscalation: LaneScaffold, Acting: self, OnBehalfOf: self,
		})
		require.ErrorIs(t, err, ErrUnknownEscalationReason)
	})

	t.Run("unknown reason rejected by the DB layer", func(t *testing.T) {
		task := escTestTask(t, ctx, s, scopeID, milestoneID, "T-reason-unknown-db", self)
		_, err := db.Pool.Exec(ctx, `
			INSERT INTO task_escalation_event (scope_id, task_id, reason, counter_value, cap_value, lane_at_escalation, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind)
			VALUES ($1, $2, 'bogus', NULL, NULL, 'Scaffold', 'iss', 'sub', 'service', 'iss', 'sub', 'service')
		`, scopeID, task.ID)
		assertCheckViolation(t, err)
	})
}

// TestInterventionAction_GoAndDBLockstep is the Testing section's fourth
// item for InterventionAction.
func TestInterventionAction_GoAndDBLockstep(t *testing.T) {
	for _, action := range []InterventionAction{InterventionActionRequeue, InterventionActionCancel, InterventionActionRelease, InterventionActionEscalate} {
		assert.NoError(t, ValidateInterventionAction(action), "the Go layer must accept %q", action)
	}
	assert.ErrorIs(t, ValidateInterventionAction(InterventionAction("bogus")), ErrUnknownInterventionAction)

	ctx := context.Background()
	s, db := escTestStore(t)
	scopeID := escTestScope(t, ctx, db)
	self := escTestSubject("agent-1")
	milestoneID := escTestMilestone(t, ctx, s, scopeID, self)
	task := escTestTask(t, ctx, s, scopeID, milestoneID, "T-intervention", self)

	for _, action := range []string{"requeue", "cancel", "release", "escalate"} {
		t.Run(action, func(t *testing.T) {
			_, err := db.Pool.Exec(ctx, `
				INSERT INTO task_intervention_event (scope_id, task_id, action, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind)
				VALUES ($1, $2, $3, 'iss', 'sub', 'service', 'iss', 'sub', 'service')
			`, scopeID, task.ID, action)
			assert.NoError(t, err, "the DB layer must accept %q", action)
		})
	}

	t.Run("unknown action rejected by the DB layer", func(t *testing.T) {
		_, err := db.Pool.Exec(ctx, `
			INSERT INTO task_intervention_event (scope_id, task_id, action, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind)
			VALUES ($1, $2, 'bogus', 'iss', 'sub', 'service', 'iss', 'sub', 'service')
		`, scopeID, task.ID)
		assertCheckViolation(t, err)
	})
}

// TestNoteLifecycleStatus_GoAndDBLockstep is the Testing section's fourth
// item for NoteLifecycleStatus, covering both task_note_lifecycle_event's
// own CHECK and task_note.current_status's identical vocabulary (FR11).
func TestNoteLifecycleStatus_GoAndDBLockstep(t *testing.T) {
	for _, status := range []NoteLifecycleStatus{NoteLifecycleStatusNoted, NoteLifecycleStatusCarriedOver, NoteLifecycleStatusDeferred, NoteLifecycleStatusClosed} {
		assert.NoError(t, ValidateNoteLifecycleStatus(status), "the Go layer must accept %q", status)
	}
	assert.ErrorIs(t, ValidateNoteLifecycleStatus(NoteLifecycleStatus("bogus")), ErrUnknownNoteLifecycleStatus)

	ctx := context.Background()
	s, db := escTestStore(t)
	scopeID := escTestScope(t, ctx, db)
	self := escTestSubject("agent-1")
	milestoneID := escTestMilestone(t, ctx, s, scopeID, self)
	task := escTestTask(t, ctx, s, scopeID, milestoneID, "T-note-lifecycle", self)
	note, err := s.Tasks().RecordNote(ctx, RecordNoteParams{
		ScopeID: scopeID, TaskID: &task.ID, Kind: NoteKindComment, Body: "a note",
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	for _, status := range []string{"noted", "carried-over", "deferred", "closed"} {
		t.Run(status, func(t *testing.T) {
			_, err := db.Pool.Exec(ctx, `
				INSERT INTO task_note_lifecycle_event (scope_id, note_id, status, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind)
				VALUES ($1, $2, $3, 'iss', 'sub', 'service', 'iss', 'sub', 'service')
			`, scopeID, note.ID, status)
			assert.NoError(t, err, "the DB layer must accept %q", status)

			_, err = db.Pool.Exec(ctx, `UPDATE task_note SET current_status = $1 WHERE id = $2`, status, note.ID)
			assert.NoError(t, err, "task_note.current_status must accept %q", status)
		})
	}

	t.Run("unknown status rejected by task_note_lifecycle_event's CHECK", func(t *testing.T) {
		_, err := db.Pool.Exec(ctx, `
			INSERT INTO task_note_lifecycle_event (scope_id, note_id, status, created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind)
			VALUES ($1, $2, 'bogus', 'iss', 'sub', 'service', 'iss', 'sub', 'service')
		`, scopeID, note.ID)
		assertCheckViolation(t, err)
	})

	t.Run("unknown status rejected by task_note.current_status's CHECK", func(t *testing.T) {
		_, err := db.Pool.Exec(ctx, `UPDATE task_note SET current_status = 'bogus' WHERE id = $1`, note.ID)
		assertCheckViolation(t, err)
	})
}

// TestRecordEscalationTx_SetsCurrentEscalation_RefusesSecondWhileActive is
// the Testing section's fifth item (FR9's one-event rule): a successful
// call sets task.current_escalation_id and inserts exactly one event; a
// second call against the same still-active escalation is refused, with
// nothing additional written.
func TestRecordEscalationTx_SetsCurrentEscalation_RefusesSecondWhileActive(t *testing.T) {
	ctx := context.Background()
	s, db := escTestStore(t)
	scopeID := escTestScope(t, ctx, db)
	self := escTestSubject("agent-1")
	milestoneID := escTestMilestone(t, ctx, s, scopeID, self)
	task := escTestTask(t, ctx, s, scopeID, milestoneID, "T-record-escalation", self)

	counter, cap := 3, 3
	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	event, err := recordEscalationTx(ctx, tx, RecordEscalationParams{
		ScopeID: scopeID, TaskID: task.ID, Reason: EscalationReasonThrashCap,
		CounterValue: &counter, CapValue: &cap, LaneAtEscalation: LaneImplementation,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CurrentEscalationID)
	assert.Equal(t, event.ID, *got.CurrentEscalationID)

	countEvents := func() int {
		var n int
		require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_escalation_event WHERE task_id = $1`, task.ID).Scan(&n))
		return n
	}
	assert.Equal(t, 1, countEvents(), "exactly one escalation event must exist")

	tx2, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer tx2.Rollback(ctx) //nolint:errcheck
	_, err = recordEscalationTx(ctx, tx2, RecordEscalationParams{
		ScopeID: scopeID, TaskID: task.ID, Reason: EscalationReasonManual,
		LaneAtEscalation: LaneImplementation, Acting: self, OnBehalfOf: self,
	})
	require.ErrorIs(t, err, ErrTaskEscalated, "a second concurrently-active escalation must be refused")
	require.NoError(t, tx2.Rollback(ctx))

	assert.Equal(t, 1, countEvents(), "the refused second attempt must write no additional event")
}

// TestForceCloseClaimTx_ClosesClaimAndClearsState_HeartbeatRejectedAfter
// is the Testing section's sixth item: forceCloseClaimTx closes the open
// claim and clears the task's claim/lease state, and a heartbeat against
// that now-closed claim is rejected exactly like a post-reclaim heartbeat
// (ErrClaimNotCurrent). Also covers forceCloseClaimTx's documented no-op
// case: a task with no open claim.
func TestForceCloseClaimTx_ClosesClaimAndClearsState_HeartbeatRejectedAfter(t *testing.T) {
	ctx := context.Background()
	s, db := escTestStore(t)
	scopeID := escTestScope(t, ctx, db)
	self := escTestSubject("agent-1")
	milestoneID := escTestMilestone(t, ctx, s, scopeID, self)
	task := escTestTask(t, ctx, s, scopeID, milestoneID, "T-force-close", self)
	sessionID := escTestSession(t, ctx, db, scopeID, self)

	claim, err := s.Tasks().ClaimTask(ctx, ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, forceCloseClaimTx(ctx, tx, task.ID, "cancel"))
	require.NoError(t, tx.Commit(ctx))

	closed, err := s.Tasks().GetClaimByID(ctx, claim.ID)
	require.NoError(t, err)
	require.NotNil(t, closed.ReleasedAt, "forceCloseClaimTx must mark the open claim released")
	require.NotNil(t, closed.ReleaseReason)
	assert.Equal(t, "cancel", *closed.ReleaseReason)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Nil(t, got.CurrentClaimID, "forceCloseClaimTx must clear task.current_claim_id")
	assert.Nil(t, got.LeaseExpiresAt, "forceCloseClaimTx must clear task.lease_expires_at")

	_, err = s.Tasks().Heartbeat(ctx, HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.ErrorIs(t, err, ErrClaimNotCurrent, "a heartbeat against a force-closed claim must be rejected exactly like a post-reclaim heartbeat")

	t.Run("no-op when the task has no open claim", func(t *testing.T) {
		unclaimed := escTestTask(t, ctx, s, scopeID, milestoneID, "T-force-close-noop", self)
		tx, err := db.Pool.Begin(ctx)
		require.NoError(t, err)
		require.NoError(t, forceCloseClaimTx(ctx, tx, unclaimed.ID, "cancel"), "forceCloseClaimTx must be a no-op, not an error, when there is no open claim")
		require.NoError(t, tx.Commit(ctx))
	})
}

// TestClaimTask_Concurrent_EscalatedTaskExcludedByIndex is the Testing
// section's concurrency item, escalation half: a task marked escalated
// directly in SQL (recordEscalationTx's own write path is proven
// separately above -- this test is only about the claimability
// predicate) must reject every one of N concurrent ClaimTask callers via
// task_claimable_idx itself, not only ClaimTask's Go-level check --
// mirroring task_claim_integration_test.go's own real-Postgres race
// proof for ErrTaskAlreadyClaimed.
func TestClaimTask_Concurrent_EscalatedTaskExcludedByIndex(t *testing.T) {
	ctx := context.Background()
	s, db := escTestStore(t)
	scopeID := escTestScope(t, ctx, db)
	self := escTestSubject("agent-1")
	milestoneID := escTestMilestone(t, ctx, s, scopeID, self)
	task := escTestTask(t, ctx, s, scopeID, milestoneID, "T-escalated-race", self)

	_, err := db.Pool.Exec(ctx, `UPDATE task SET current_escalation_id = gen_random_uuid() WHERE id = $1`, task.ID)
	require.NoError(t, err)

	const n = 5
	sessions := make([]SessionID, n)
	for i := range sessions {
		sessions[i] = escTestSession(t, ctx, db, scopeID, self)
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := s.Tasks().ClaimTask(ctx, ClaimTaskParams{
				ScopeID: scopeID, TaskID: task.ID, SessionID: sessions[i], Acting: self, OnBehalfOf: self,
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.ErrorIs(t, err, ErrTaskEscalated, "claimant %d must be excluded by the escalated predicate", i)
	}

	var claimCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_claim WHERE task_id = $1`, task.ID).Scan(&claimCount))
	assert.Equal(t, 0, claimCount, "no concurrent claimant may succeed against an escalated task")
}

// TestClaimTask_Concurrent_CancelledTaskExcludedByIndex mirrors
// TestClaimTask_Concurrent_EscalatedTaskExcludedByIndex for FR7's
// dead-letter state.
func TestClaimTask_Concurrent_CancelledTaskExcludedByIndex(t *testing.T) {
	ctx := context.Background()
	s, db := escTestStore(t)
	scopeID := escTestScope(t, ctx, db)
	self := escTestSubject("agent-1")
	milestoneID := escTestMilestone(t, ctx, s, scopeID, self)
	task := escTestTask(t, ctx, s, scopeID, milestoneID, "T-cancelled-race", self)

	_, err := db.Pool.Exec(ctx, `UPDATE task SET cancelled_at = NOW() WHERE id = $1`, task.ID)
	require.NoError(t, err)

	const n = 5
	sessions := make([]SessionID, n)
	for i := range sessions {
		sessions[i] = escTestSession(t, ctx, db, scopeID, self)
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := s.Tasks().ClaimTask(ctx, ClaimTaskParams{
				ScopeID: scopeID, TaskID: task.ID, SessionID: sessions[i], Acting: self, OnBehalfOf: self,
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.ErrorIs(t, err, ErrTaskCancelled, "claimant %d must be excluded by the cancelled predicate", i)
	}

	var claimCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_claim WHERE task_id = $1`, task.ID).Scan(&claimCount))
	assert.Equal(t, 0, claimCount, "no concurrent claimant may succeed against a cancelled task")
}

// TestClaimTask_OrdinaryTask_StillClaimable is the Testing section's
// regression item: an ordinary, unescalated, uncancelled task is still
// claimable exactly as M4 leaves it -- this migration's tightened index
// and ClaimTask's new checks change no existing behavior for the row this
// test creates.
func TestClaimTask_OrdinaryTask_StillClaimable(t *testing.T) {
	ctx := context.Background()
	s, db := escTestStore(t)
	scopeID := escTestScope(t, ctx, db)
	self := escTestSubject("agent-1")
	milestoneID := escTestMilestone(t, ctx, s, scopeID, self)
	task := escTestTask(t, ctx, s, scopeID, milestoneID, "T-ordinary", self)
	sessionID := escTestSession(t, ctx, db, scopeID, self)

	claim, err := s.Tasks().ClaimTask(ctx, ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err, "an ordinary unescalated, uncancelled task must still be claimable exactly as M4 leaves it")
	assert.NotEqual(t, uuid.Nil, claim.ID)
}
