// This file (issue #2720, FR2, C14) widens store.TaskStore (task.go's own
// doc comment: "every later M4 task ... widens this same interface rather
// than introducing a sibling") with the work axis's dependency-declaration
// and claimability-predicate surface over `task_dependency`
// (015_work_axis.up.sql): DeclareDependency writes one append-only edge
// row per (task_id, depends_on_task_id) pair, ListDependencies reads them
// back in declaration order (the list the claim payload, #2721, carries),
// and UnsatisfiedDependencies is the terminal-`Done` predicate FR3's claim
// path (#2722) consumes directly rather than re-deriving dependency state
// itself.
//
// `task_dependency` is plain (not SCD2, LB3 -- see the migration's own
// note on this table), so parentage is checked with plainRowExists, the
// same helper CreateMilepebble/AddDeferral use for their own plain
// milestone_ref parent, never currentRowExists (which assumes a
// `valid_to` column this table does not have).
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TaskDependency is one row of `task_dependency` (migration 015) -- a
// declared, immutable fact that TaskID depends on DependsOnTaskID (FR2).
// Never revised or removed: a duplicate declaration is absorbed by the
// unique index at INSERT time (idempotent, not a second row).
type TaskDependency struct {
	ID              uuid.UUID
	ScopeID         uuid.UUID
	TaskID          uuid.UUID
	DependsOnTaskID uuid.UUID

	// CreatedByActing/CreatedByOnBehalfOf are always populated (NFR3,
	// LB4) -- DeclareDependency is the only write path onto this table,
	// and it always has a real caller session (NFR6's write gate).
	CreatedByActing     Subject
	CreatedByOnBehalfOf Subject
	CreatedAt           time.Time
}

// DeclareDependencyParams is DeclareDependency's input (FR2): TaskID
// depends on every id in DependsOnTaskIDs, all recorded in one
// transaction.
type DeclareDependencyParams struct {
	ScopeID          uuid.UUID
	TaskID           uuid.UUID
	DependsOnTaskIDs []uuid.UUID
	Acting           Subject
	OnBehalfOf       Subject
}

// ErrSelfDependency is DeclareDependency's named, loud rejection of a
// self-edge (task_id == depends_on_task_id) -- backstopped at the DB
// layer by task_dependency's own CHECK constraint, but rejected here
// first so the caller gets a named Go error rather than a raw constraint
// violation.
var ErrSelfDependency = errors.New("krill/store: a task cannot depend on itself")

// ErrDependencyCycle is DeclareDependency's named, loud rejection of an
// edge that would close a dependency cycle -- e.g. declaring B depends on
// A when A already (transitively) depends on B. A cycle would make every
// task on it permanently unclaimable (FR2's own rationale), so this is
// checked by walking the existing edge set inside the same transaction as
// the INSERT, before it happens, never left to be discovered later.
var ErrDependencyCycle = errors.New("krill/store: declaring this dependency would create a cycle")

const taskDependencyColumns = `id, scope_id, task_id, depends_on_task_id, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at`

func scanTaskDependency(row pgx.Row) (TaskDependency, error) {
	var d TaskDependency
	var actingKind, onBehalfOfKind string
	err := row.Scan(
		&d.ID, &d.ScopeID, &d.TaskID, &d.DependsOnTaskID,
		&d.CreatedByActing.Iss, &d.CreatedByActing.Sub, &actingKind,
		&d.CreatedByOnBehalfOf.Iss, &d.CreatedByOnBehalfOf.Sub, &onBehalfOfKind,
		&d.CreatedAt,
	)
	if err != nil {
		return TaskDependency{}, err
	}
	d.CreatedByActing.Kind = SubjectKind(actingKind)
	d.CreatedByOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	return d, nil
}

func (s taskStore) DeclareDependency(ctx context.Context, params DeclareDependencyParams) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	exists, err := plainRowExists(ctx, tx, "task", params.TaskID, params.ScopeID)
	if err != nil {
		return err
	}
	if !exists {
		return errParentNotFound("task", params.TaskID)
	}

	for _, dependsOnID := range params.DependsOnTaskIDs {
		if dependsOnID == params.TaskID {
			return fmt.Errorf("%w: task id %s", ErrSelfDependency, params.TaskID)
		}

		depExists, err := plainRowExists(ctx, tx, "task", dependsOnID, params.ScopeID)
		if err != nil {
			return err
		}
		if !depExists {
			return errParentNotFound("task", dependsOnID)
		}

		// A new edge TaskID -> dependsOnID closes a cycle exactly when
		// dependsOnID can already reach TaskID via the existing edge set
		// -- i.e. dependsOnID already (transitively) depends on TaskID,
		// which would make TaskID -> dependsOnID -> ... -> TaskID a loop.
		// Checked inside this same transaction, against edges already
		// inserted earlier in this same call, so a batch like
		// [A depends on B, B depends on A] in one DeclareDependency call
		// is caught exactly like two separate calls would be.
		cyclic, err := taskDependencyReaches(ctx, tx, params.ScopeID, dependsOnID, params.TaskID)
		if err != nil {
			return err
		}
		if cyclic {
			return fmt.Errorf("%w: task %s already (transitively) depends on task %s", ErrDependencyCycle, dependsOnID, params.TaskID)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO task_dependency (
				scope_id, task_id, depends_on_task_id,
				created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
				created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (scope_id, task_id, depends_on_task_id) DO NOTHING
		`, params.ScopeID, params.TaskID, dependsOnID,
			params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
			params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
		); err != nil {
			return fmt.Errorf("insert task_dependency: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// taskDependencyReaches reports whether to is reachable from from by
// following existing task_dependency edges (from depends_on ... depends_on
// to), scoped to scopeID -- DeclareDependency's cycle check.
func taskDependencyReaches(ctx context.Context, q txQuerier, scopeID, from, to uuid.UUID) (bool, error) {
	var reaches bool
	err := q.QueryRow(ctx, `
		WITH RECURSIVE reachable(task_id) AS (
			SELECT depends_on_task_id FROM task_dependency
			WHERE task_id = $1 AND scope_id = $3
			UNION
			SELECT td.depends_on_task_id
			FROM task_dependency td
			JOIN reachable r ON td.task_id = r.task_id
			WHERE td.scope_id = $3
		)
		SELECT EXISTS (SELECT 1 FROM reachable WHERE task_id = $2)
	`, from, to, scopeID).Scan(&reaches)
	if err != nil {
		return false, fmt.Errorf("check dependency cycle: %w", err)
	}
	return reaches, nil
}

func (s taskStore) ListDependencies(ctx context.Context, scopeID, taskID uuid.UUID) ([]TaskDependency, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+taskDependencyColumns+`
		FROM task_dependency
		WHERE scope_id = $1 AND task_id = $2
		ORDER BY created_at, id
	`, scopeID, taskID)
	if err != nil {
		return nil, fmt.Errorf("list task_dependency: %w", err)
	}
	defer rows.Close()

	var deps []TaskDependency
	for rows.Next() {
		d, err := scanTaskDependency(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task_dependency: %w", err)
		}
		deps = append(deps, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list task_dependency: %w", err)
	}
	return deps, nil
}

func (s taskStore) UnsatisfiedDependencies(ctx context.Context, scopeID, taskID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT td.depends_on_task_id
		FROM task_dependency td
		JOIN task t ON t.id = td.depends_on_task_id AND t.scope_id = td.scope_id
		WHERE td.scope_id = $1 AND td.task_id = $2 AND t.current_lane <> $3
		ORDER BY td.created_at, td.id
	`, scopeID, taskID, string(LaneDone))
	if err != nil {
		return nil, fmt.Errorf("list unsatisfied task_dependency: %w", err)
	}
	defer rows.Close()

	var unsatisfied []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan unsatisfied task_dependency: %w", err)
		}
		unsatisfied = append(unsatisfied, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list unsatisfied task_dependency: %w", err)
	}
	return unsatisfied, nil
}
