// This file (issue #2727, FR11, FR12, C25) widens store.TaskStore (task.go's
// own doc comment: "every later M4 task ... widens this same interface
// rather than introducing a sibling") with the work axis's note-recording
// surface over `task_note` (015_work_axis.up.sql): RecordNote appends one
// flat, immutable note row against exactly one target -- a task, or a
// spec-axis entity a task's work touches -- and ListNotesForTask/
// ListNotesForEntity are its scope-qualified reads.
//
// RecordNote deliberately never checks `current_claim_id` -- FR11 says any
// Agent with an active session may note, claimant or not, and this store
// method has no session concept at all; the NFR6 session gate is the
// caller's job (RequireSession in the HTTP layer, requireKrillSession in the
// MCP layer), never re-implemented here.
//
// `task_note` is flat and immutable (FR12): no status/state column, no
// lifecycle transition, and no update or delete method exists anywhere in
// this package for it -- see this file's own doc comment on Note for why
// that must stay true even after M5's C26 lifecycle lands.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// NoteKind is the fixed enumeration of `task_note.kind` values (FR11),
// defined in exactly this one place in Go and mirrored exactly by
// 015_work_axis.up.sql's `CHECK (kind IN ('scope-note', 'comment'))` --
// the two sets must be kept in lockstep by hand; widening this set is a new
// migration's CHECK edit paired with a new named constant here, never a
// bare string at a call site and never an edit to migration 015's CHECK in
// place.
type NoteKind string

const (
	// NoteKindScopeNote (FR11) models today's GitHub `source:scope-note`
	// label convention as a first-class krill note kind: scope an Agent
	// noticed but is not acting on, recorded directly against the task or
	// spec-axis entity it concerns rather than filed as a separate,
	// easy-to-miss issue.
	NoteKindScopeNote NoteKind = "scope-note"

	// NoteKindComment replaces no specific GitHub convention -- it is the
	// general-purpose free-form note kind every note that is not
	// scope-discovery falls under.
	NoteKindComment NoteKind = "comment"
)

// validNoteKinds is the set validateNoteKind checks against -- the Go-layer
// half of the "unknown kind rejected at both layers" contract this file's
// package doc comment describes; migration 015's CHECK is the DB-layer half.
var validNoteKinds = map[NoteKind]bool{
	NoteKindScopeNote: true,
	NoteKindComment:   true,
}

// ErrUnknownNoteKind is RecordNote's named, loud rejection of a Kind outside
// NoteKind's fixed enumeration -- checked in Go ahead of the INSERT, so a
// caller gets this named error rather than a raw Postgres CHECK-constraint
// violation for the same condition.
var ErrUnknownNoteKind = errors.New("krill/store: unknown note kind")

func validateNoteKind(kind NoteKind) error {
	if !validNoteKinds[kind] {
		return fmt.Errorf("%w: %q", ErrUnknownNoteKind, kind)
	}
	return nil
}

// NoteEntityKind is the fixed enumeration of `task_note.entity_kind` values
// (FR11) -- the five spec-axis tables a note may target instead of a task,
// mirrored exactly by 015_work_axis.up.sql's own CHECK constraint. Each
// value is also the literal name of the spec-axis table it names (e.g.
// NoteEntityKindFeature's table is `feature`), the same convention
// currentRowExists' `table` argument already assumes throughout this
// package.
type NoteEntityKind string

const (
	NoteEntityKindProduct             NoteEntityKind = "product"
	NoteEntityKindFeatureSet          NoteEntityKind = "feature_set"
	NoteEntityKindFeature             NoteEntityKind = "feature"
	NoteEntityKindRequirement         NoteEntityKind = "requirement"
	NoteEntityKindLoadBearingDecision NoteEntityKind = "load_bearing_decision"
)

// validNoteEntityKinds is RecordNote's set of allowed params.EntityKind
// values -- mirrors migration 015's own CHECK.
var validNoteEntityKinds = map[NoteEntityKind]bool{
	NoteEntityKindProduct:             true,
	NoteEntityKindFeatureSet:          true,
	NoteEntityKindFeature:             true,
	NoteEntityKindRequirement:         true,
	NoteEntityKindLoadBearingDecision: true,
}

// ErrUnknownNoteEntityKind is RecordNote's named, loud rejection of an
// EntityKind outside NoteEntityKind's fixed enumeration.
var ErrUnknownNoteEntityKind = errors.New("krill/store: unknown note entity kind")

// ErrInvalidNoteTarget is RecordNote's named, loud rejection of a
// RecordNoteParams that does not name exactly one target -- either TaskID
// alone, or EntityKind+EntityID together, never both and never neither.
// Backstopped at the DB layer by task_note's own CHECK constraint
// (015_work_axis.up.sql), but rejected here first so the caller gets a
// named Go error rather than a raw constraint violation.
var ErrInvalidNoteTarget = errors.New("krill/store: a note must target exactly one of task_id or entity_kind+entity_id")

// ErrEmptyNoteBody is RecordNote's named, loud rejection of a blank Body --
// `task_note.body` is NOT NULL but has no DB-level non-empty CHECK, so this
// is a Go-only validation.
var ErrEmptyNoteBody = errors.New("krill/store: note body must not be empty")

// Note is one row of `task_note` (migration 015) -- a flat, immutable
// record that Body (of Kind) was recorded against exactly one target,
// either the task TaskID names or the spec-axis entity EntityKind+EntityID
// names. Never revised, resolved, or deleted (FR12): no status/state field
// exists on this struct, and none may be added by widening this struct in
// place -- M5's C26 lifecycle, when it lands, is a new migration adding new
// columns, not a reshaping of this one.
type Note struct {
	ID      uuid.UUID
	ScopeID uuid.UUID

	// TaskID is set exactly when EntityKind/EntityID are both nil, and
	// vice versa (ErrInvalidNoteTarget, task_note's own CHECK).
	TaskID *uuid.UUID

	EntityKind *NoteEntityKind
	EntityID   *uuid.UUID

	Kind NoteKind
	Body string

	// CreatedByActing/CreatedByOnBehalfOf are always populated (NFR3,
	// LB4) -- RecordNote is the only write path onto this table, and it
	// is only ever called by a caller with an active session (NFR6),
	// enforced above this store layer.
	CreatedByActing     Subject
	CreatedByOnBehalfOf Subject
	CreatedAt           time.Time
}

// RecordNoteParams is RecordNote's input (FR11): exactly one target
// (TaskID, or EntityKind+EntityID naming a spec-axis entity), a Kind from
// NoteKind's fixed enumeration, a Body, and the LB4 subject pair.
type RecordNoteParams struct {
	ScopeID uuid.UUID

	TaskID *uuid.UUID

	EntityKind *NoteEntityKind
	EntityID   *uuid.UUID

	Kind NoteKind
	Body string

	Acting     Subject
	OnBehalfOf Subject
}

// validateNoteTarget enforces ErrInvalidNoteTarget's "exactly one target"
// rule in Go, ahead of task_note's own CHECK.
func validateNoteTarget(params RecordNoteParams) error {
	hasTask := params.TaskID != nil
	hasEntity := params.EntityKind != nil && params.EntityID != nil
	// A caller supplying only one of EntityKind/EntityID (never both nil,
	// never both set) is also invalid -- neither a task target nor a
	// complete entity target.
	partialEntity := (params.EntityKind != nil) != (params.EntityID != nil)
	if partialEntity || hasTask == hasEntity {
		return ErrInvalidNoteTarget
	}
	return nil
}

const noteColumns = `id, scope_id, task_id, entity_kind, entity_id, kind, body, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at`

func scanNote(row pgx.Row) (Note, error) {
	var n Note
	var entityKind *string
	var actingKind, onBehalfOfKind string
	err := row.Scan(
		&n.ID, &n.ScopeID, &n.TaskID, &entityKind, &n.EntityID, &n.Kind, &n.Body,
		&n.CreatedByActing.Iss, &n.CreatedByActing.Sub, &actingKind,
		&n.CreatedByOnBehalfOf.Iss, &n.CreatedByOnBehalfOf.Sub, &onBehalfOfKind,
		&n.CreatedAt,
	)
	if err != nil {
		return Note{}, err
	}
	if entityKind != nil {
		kind := NoteEntityKind(*entityKind)
		n.EntityKind = &kind
	}
	n.CreatedByActing.Kind = SubjectKind(actingKind)
	n.CreatedByOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	return n, nil
}

func (s taskStore) RecordNote(ctx context.Context, params RecordNoteParams) (Note, error) {
	if err := validateNoteTarget(params); err != nil {
		return Note{}, err
	}
	if err := validateNoteKind(params.Kind); err != nil {
		return Note{}, err
	}
	if params.EntityKind != nil && !validNoteEntityKinds[*params.EntityKind] {
		return Note{}, fmt.Errorf("%w: %q", ErrUnknownNoteEntityKind, *params.EntityKind)
	}
	if params.Body == "" {
		return Note{}, ErrEmptyNoteBody
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Note{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// RecordNote deliberately never inspects `task.current_claim_id` here
	// -- FR11's "any Agent, claimant or not" rule means note-recording is
	// never claim-gated, only existence-gated (this file's own doc
	// comment).
	if params.TaskID != nil {
		exists, err := plainRowExists(ctx, tx, "task", *params.TaskID, params.ScopeID)
		if err != nil {
			return Note{}, err
		}
		if !exists {
			return Note{}, errParentNotFound("task", *params.TaskID)
		}
	} else {
		table := string(*params.EntityKind)
		exists, err := currentRowExists(ctx, tx, table, *params.EntityID, params.ScopeID)
		if err != nil {
			return Note{}, err
		}
		if !exists {
			return Note{}, errParentNotFound(table, *params.EntityID)
		}
	}

	// entityKind is passed as *string, not *NoteEntityKind, so pgx's
	// nullable-scalar encoding path (already exercised for Body's own
	// *string field elsewhere in this package) applies uniformly rather
	// than depending on pgx's handling of a pointer to a named string
	// type.
	var entityKind *string
	if params.EntityKind != nil {
		ek := string(*params.EntityKind)
		entityKind = &ek
	}

	note, err := scanNote(tx.QueryRow(ctx, `
		INSERT INTO task_note (
			scope_id, task_id, entity_kind, entity_id, kind, body,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING `+noteColumns,
		params.ScopeID, params.TaskID, entityKind, params.EntityID, string(params.Kind), params.Body,
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind)))
	if err != nil {
		return Note{}, fmt.Errorf("insert task_note: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Note{}, fmt.Errorf("commit: %w", err)
	}
	return note, nil
}

func (s taskStore) ListNotesForTask(ctx context.Context, scopeID, taskID uuid.UUID) ([]Note, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+noteColumns+`
		FROM task_note
		WHERE scope_id = $1 AND task_id = $2
		ORDER BY created_at, id
	`, scopeID, taskID)
	if err != nil {
		return nil, fmt.Errorf("list task_note by task: %w", err)
	}
	defer rows.Close()

	var notes []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task_note: %w", err)
		}
		notes = append(notes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list task_note by task: %w", err)
	}
	return notes, nil
}

func (s taskStore) ListNotesForEntity(ctx context.Context, scopeID uuid.UUID, kind NoteEntityKind, entityID uuid.UUID) ([]Note, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+noteColumns+`
		FROM task_note
		WHERE scope_id = $1 AND entity_kind = $2 AND entity_id = $3
		ORDER BY created_at, id
	`, scopeID, string(kind), entityID)
	if err != nil {
		return nil, fmt.Errorf("list task_note by entity: %w", err)
	}
	defer rows.Close()

	var notes []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task_note: %w", err)
		}
		notes = append(notes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list task_note by entity: %w", err)
	}
	return notes, nil
}
