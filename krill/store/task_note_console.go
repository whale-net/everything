// This file (issue #2874, root plan #2851, FR12) is M5's open-notes
// console query: ListOpenNotes returns every note still at
// NoteLifecycleStatusNoted in a scope, across both target shapes a
// task_note row can name (task_note.go) -- a task, or a spec-axis entity --
// carrying enough identifying context for a Swarm Operator to act without a
// second lookup, mirroring task_console.go's own "never a bare id" rule for
// FR4's claimed-task view. Shares paging.go's keyset paging/continuation-
// token machinery (NFR6) rather than rolling its own, exactly as
// task_console.go's own doc comment calls for every M5 console query to do.
//
// A note moved to `carried-over`, `deferred`, or `closed` is deliberately
// excluded -- this file's one filter (`current_status = 'noted'`) is never
// widened with a "show all statuses" option in this milestone (#2851 Out of
// scope, Assumption 10); M4 FR10's per-task fetch (GET /tasks/{id}) is
// still where every note on a task, regardless of status, is visible.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// OpenNoteTaskContext is one OpenNoteRow's identifying context when the
// note targets a task (task_note.task_id) -- the task's own title plus its
// delivery reference, the exact same join ClaimedTaskDeliveryRef resolves
// for FR4's claimed-task view (task_console.go), reused here rather than a
// second copy of the same milestone_ref lookup.
type OpenNoteTaskContext struct {
	TaskID      uuid.UUID
	Title       string
	DeliveryRef ClaimedTaskDeliveryRef
}

// OpenNoteEntityContext is one OpenNoteRow's identifying context when the
// note targets a spec-axis entity (task_note.entity_kind/entity_id) -- the
// entity's own immutable id plus its current display name, resolved across
// whichever of the five NoteEntityKind tables EntityKind names (never a
// Feature/Requirement id misread as a task, and vice versa).
type OpenNoteEntityContext struct {
	EntityKind NoteEntityKind
	EntityID   uuid.UUID
	Title      string
}

// OpenNoteRow is one row of ListOpenNotes' page (FR12): a note's own body
// and kind (M4 FR11), plus the identifying context of whichever one target
// it names. Exactly one of TaskContext/EntityContext is set, mirroring
// task_note's own exactly-one-target CHECK (task_note.go's
// ErrInvalidNoteTarget) -- this struct never carries both nor neither.
type OpenNoteRow struct {
	NoteID    uuid.UUID
	Kind      NoteKind
	Body      string
	CreatedAt time.Time

	TaskContext   *OpenNoteTaskContext
	EntityContext *OpenNoteEntityContext
}

// ListOpenNotesParams is ListOpenNotes' input: scopeID (NFR1), the
// optional ConsoleFilter narrowing, and this query's own PageParams
// (NFR6). A milestone filter keeps only task-targeted notes, since a
// note on a spec-axis entity has no delivery container; a product filter
// also keeps entity-targeted notes whose entity belongs to that product.
type ListOpenNotesParams struct {
	ScopeID uuid.UUID
	ConsoleFilter
	Page PageParams
}

// Filters is the FilterSet ListOpenNotes' continuation token binds.
func (p ListOpenNotesParams) Filters() FilterSet {
	return p.ConsoleFilter.Filters()
}

// ListOpenNotes returns every note in params.ScopeID whose current_status
// is NoteLifecycleStatusNoted (FR12), oldest-first (`created_at ASC, tn.id
// ASC` -- the order a note was flagged, so the longest-unaddressed note
// surfaces first), bounded and continuable per params.Page (NFR6).
//
// The identifying-context join is a LEFT JOIN across `task`+`milestone_ref`
// (a task target) and, independently, across all five NoteEntityKind
// tables (an entity target) -- task_note's own CHECK guarantees a row
// matches at most one task join and at most one entity-kind join, so
// exactly one of TaskContext/EntityContext resolves non-nil per row, never
// both, never neither.
func (s taskStore) ListOpenNotes(ctx context.Context, params ListOpenNotesParams) (Page[OpenNoteRow], error) {
	pageSize := ResolvePageSize(params.Page.PageSize)

	if err := s.guardConsoleFilter(ctx, params.ScopeID, params.ConsoleFilter); err != nil {
		return Page[OpenNoteRow]{}, err
	}

	var sortVal time.Time
	var cursorID *uuid.UUID
	if params.Page.ContinuationToken != "" {
		c, err := DecodeFilteredContinuationToken(params.ScopeID, params.Filters(), params.Page.ContinuationToken)
		if err != nil {
			return Page[OpenNoteRow]{}, err
		}
		if sortVal, err = time.Parse(time.RFC3339Nano, c.SortKey); err != nil {
			return Page[OpenNoteRow]{}, fmt.Errorf("%w: %v", ErrInvalidContinuationToken, err)
		}
		cursorID = &c.ID
	}

	fromWhere, args := openNotesQuery(params)
	if cursorID != nil {
		args = append(args, sortVal, *cursorID)
		fromWhere += fmt.Sprintf(
			" AND (tn.created_at, tn.id) > ($%d::timestamptz, $%d::uuid)", len(args)-1, len(args))
	}
	query := `
		SELECT
			tn.id, tn.kind, tn.body, tn.created_at,
			tn.task_id, t.title, mr.id, mr.kind, mr.name,
			tn.entity_kind, tn.entity_id,
			COALESCE(p.name, fs.name, f.name, r.name, lbd.name)
	` + fromWhere + fmt.Sprintf(` ORDER BY tn.created_at ASC, tn.id ASC LIMIT $%d`, len(args)+1)
	args = append(args, pageSize+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page[OpenNoteRow]{}, fmt.Errorf("list open task_note: %w", err)
	}
	defer rows.Close()

	var items []OpenNoteRow
	for rows.Next() {
		var row OpenNoteRow
		var taskID *uuid.UUID
		var taskTitle *string
		var deliveryID *uuid.UUID
		var deliveryKind, deliveryTitle *string
		var entityKind *string
		var entityID *uuid.UUID
		var entityTitle *string

		if err := rows.Scan(
			&row.NoteID, &row.Kind, &row.Body, &row.CreatedAt,
			&taskID, &taskTitle, &deliveryID, &deliveryKind, &deliveryTitle,
			&entityKind, &entityID, &entityTitle,
		); err != nil {
			return Page[OpenNoteRow]{}, fmt.Errorf("scan open task_note: %w", err)
		}

		if taskID != nil {
			row.TaskContext = &OpenNoteTaskContext{
				TaskID: *taskID,
				Title:  derefString(taskTitle),
				DeliveryRef: ClaimedTaskDeliveryRef{
					ID:    derefUUID(deliveryID),
					Kind:  MilestoneKind(derefString(deliveryKind)),
					Title: derefString(deliveryTitle),
				},
			}
		} else {
			row.EntityContext = &OpenNoteEntityContext{
				EntityKind: NoteEntityKind(derefString(entityKind)),
				EntityID:   derefUUID(entityID),
				Title:      derefString(entityTitle),
			}
		}

		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return Page[OpenNoteRow]{}, fmt.Errorf("list open task_note: %w", err)
	}

	var nextToken string
	if len(items) > pageSize {
		last := items[pageSize-1]
		nextToken = EncodeFilteredContinuationToken(params.ScopeID, params.Filters(), Cursor{
			SortKey: last.CreatedAt.Format(time.RFC3339Nano),
			ID:      last.NoteID,
		})
		items = items[:pageSize]
	}

	return Page[OpenNoteRow]{Items: items, NextToken: nextToken}, nil
}

// derefString returns "" for a nil *string, s's value otherwise -- every
// nullable text column ListOpenNotes' join can leave unmatched (the
// opposite branch's target shape) scans as nil, never a false empty string
// this helper would otherwise have to distinguish from a genuinely blank
// title.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// derefUUID mirrors derefString for a nullable UUID column.
func derefUUID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.UUID{}
	}
	return *id
}

// openNotesQuery returns the FROM/JOIN/WHERE clause behind both
// ListOpenNotes and CountOpenNotes, plus the arguments it binds. Paging
// stays with the list: the keyset half of the predicate and LIMIT are
// appended by ListOpenNotes alone, since a count describes the whole
// open-notes set and must not be narrowed to a page.
//
// The ConsoleFilter narrowing rides in the same builder, so the count is
// filtered by construction. A task-targeted note narrows on the task's
// own delivery container; an entity-targeted note has no container at
// all, so a milestone filter excludes it outright while a product filter
// keeps it only when the entity it names resolves to that product. That
// resolution is the COALESCE below, walking whichever of the five
// NoteEntityKind tables the note names up to the product that owns it.
func openNotesQuery(params ListOpenNotesParams) (string, []any) {
	// $3/$4 are the two narrowing ids, always bound (nil reads as NULL
	// and every conjunct below is written to admit it), so the placeholders
	// never shift with how many filters the caller set.
	productArg, milestoneArg := 3, 4
	args := []any{params.ScopeID, string(NoteLifecycleStatusNoted), params.ProductID, params.MilestoneID}
	filterSQL := fmt.Sprintf(`
			AND (
				(tn.task_id IS NOT NULL
					AND ($%[1]d::uuid IS NULL OR mr.product_id = $%[1]d)
					AND ($%[2]d::uuid IS NULL OR mr.id = $%[2]d OR mr.parent_milestone_id = $%[2]d))
				OR
				(tn.task_id IS NULL AND $%[2]d::uuid IS NULL
					AND ($%[1]d::uuid IS NULL OR COALESCE(p.id, fs.product_id, f_fs.product_id, r_fs.product_id, lbd_fs.product_id) = $%[1]d))
			)`, productArg, milestoneArg)
	return `
		FROM task_note tn
		LEFT JOIN task t ON tn.task_id = t.id
		LEFT JOIN milestone_ref mr ON t.milestone_id = mr.id AND mr.valid_to IS NULL
		LEFT JOIN product p ON tn.entity_kind = 'product' AND tn.entity_id = p.id AND p.valid_to IS NULL
		LEFT JOIN feature_set fs ON tn.entity_kind = 'feature_set' AND tn.entity_id = fs.id AND fs.valid_to IS NULL
		LEFT JOIN feature f ON tn.entity_kind = 'feature' AND tn.entity_id = f.id AND f.valid_to IS NULL
		LEFT JOIN requirement r ON tn.entity_kind = 'requirement' AND tn.entity_id = r.id AND r.valid_to IS NULL
		LEFT JOIN load_bearing_decision lbd ON tn.entity_kind = 'load_bearing_decision' AND tn.entity_id = lbd.id AND lbd.valid_to IS NULL
		LEFT JOIN feature_set f_fs ON f.feature_set_id = f_fs.id AND f_fs.valid_to IS NULL
		LEFT JOIN feature r_f ON r.feature_id = r_f.id AND r_f.valid_to IS NULL
		LEFT JOIN feature_set r_fs ON r_f.feature_set_id = r_fs.id AND r_fs.valid_to IS NULL
		LEFT JOIN feature_set lbd_fs ON lbd.feature_set_id = lbd_fs.id AND lbd_fs.valid_to IS NULL
		WHERE tn.scope_id = $1 AND tn.current_status = $2
	` + filterSQL, args
}

// openNotesQueryOfKind is openNotesQuery narrowed to one NoteKind -- the
// Overview sub-line "open scope-notes", the same rows with one more
// conjunct rather than a query of its own.
func openNotesQueryOfKind(params ListOpenNotesParams, kind NoteKind) (string, []any) {
	fromWhere, args := openNotesQuery(params)
	args = append(args, string(kind))
	return fromWhere + fmt.Sprintf(` AND tn.kind = $%d`, len(args)), args
}

// CountOpenNotes returns how many rows the unpaged ListOpenNotes would
// hold for params -- the open-notes queue's full size, never the current
// page's length; params.Page is ignored. Shares openNotesQuery with its
// list, and returns a failed count as its error rather than as 0.
//
// These per-product figures are NOT additive into a scope-wide total.
// An open note on a spec-axis entity is attributed to a product only when
// that entity is still current; once the entity is voided or superseded
// the note has no live product, so it is counted scope-wide and under no
// product. A UI showing a per-product open-notes count therefore shows
// exactly that product's number and never presents it, or a sum of
// siblings, as the scope-wide figure.
func (s taskStore) CountOpenNotes(ctx context.Context, params ListOpenNotesParams) (int, error) {
	_ = ctx
	_ = params
	return 0, ErrNotImplemented
}
