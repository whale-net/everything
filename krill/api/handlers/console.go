// This file (issue #2869, FR4, NFR6) is M5's console query HTTP surface:
// the home for every console handler this milestone adds. This task ships
// GET /console/claimed over store.TaskStore.ListClaimedTasks (FR4); issue
// #2874 (FR12) adds GET /console/notes over store.TaskStore.ListOpenNotes;
// issue #2873 (FR10) adds GET /console/cancelled; issue #2875 (FR5) adds
// GET /console/escalated over store.TaskStore.ListEscalatedTasks.
// Ungated like every other read endpoint in this package (NFR6's gate is
// write-only) -- unlike every other ungated GET route here, this query
// has no single path entity to resolve scope_id from (contrast
// ListTaskDependenciesHandler's/GetTaskPayloadHandler's "resolve scope
// from the entity itself" posture, task_dependency.go/task_payload.go),
// so scope_id is instead a required query parameter.
package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

const (
	scopeIDQueryParam   = "scope_id"
	pageSizeQueryParam  = "page_size"
	pageTokenQueryParam = "page_token"
)

// ClaimedTaskDeliveryRefWire is the wire shape of one
// store.ClaimedTaskDeliveryRef.
type ClaimedTaskDeliveryRefWire struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// ClaimedTaskClaimantWire is the wire shape of one claimed task row's
// claimant: the claim's own session id and both LB4 subject pairs.
type ClaimedTaskClaimantWire struct {
	SessionID  string      `json:"session_id"`
	Acting     SubjectWire `json:"acting"`
	OnBehalfOf SubjectWire `json:"on_behalf_of"`
}

// ClaimedTaskWire is the wire shape of one store.ClaimedTaskRow (FR4).
// Exported (mirrors IDResponse's/SubjectWire's own precedent, types.go/
// revision_event.go) so krill/mcp/tools' list_claimed_tasks tool can
// return this exact shape rather than an MCP-local mirror (LB7's "the
// same parity krill/README.md documents for every other krill tool").
type ClaimedTaskWire struct {
	TaskID         string                     `json:"task_id"`
	Title          string                     `json:"title"`
	DeliveryRef    ClaimedTaskDeliveryRefWire `json:"delivery_ref"`
	Claimant       ClaimedTaskClaimantWire    `json:"claimant"`
	CurrentLane    string                     `json:"current_lane"`
	LeaseExpiresAt time.Time                  `json:"lease_expires_at"`
	AttemptCount   int                        `json:"attempt_count"`
}

// ToClaimedTaskWire converts one store.ClaimedTaskRow to its wire shape.
func ToClaimedTaskWire(row store.ClaimedTaskRow) ClaimedTaskWire {
	return ClaimedTaskWire{
		TaskID: row.TaskID.String(),
		Title:  row.Title,
		DeliveryRef: ClaimedTaskDeliveryRefWire{
			ID:    row.DeliveryRef.ID.String(),
			Kind:  string(row.DeliveryRef.Kind),
			Title: row.DeliveryRef.Title,
		},
		Claimant: ClaimedTaskClaimantWire{
			SessionID:  row.ClaimantSessionID.String(),
			Acting:     ToSubjectWire(row.ClaimantActing),
			OnBehalfOf: ToSubjectWire(row.ClaimantOnBehalfOf),
		},
		CurrentLane:    string(row.CurrentLane),
		LeaseExpiresAt: row.LeaseExpiresAt,
		AttemptCount:   row.AttemptCount,
	}
}

// listClaimedTasksResponse is ListClaimedTasksHandler's response body
// (NFR6): a bounded page plus a continuation token, present exactly when
// more rows remain.
type listClaimedTasksResponse struct {
	Tasks     []ClaimedTaskWire `json:"tasks"`
	NextToken string            `json:"next_token,omitempty"`
}

// ListClaimedTasksHandler returns the console claimed-task view (FR4):
// GET /console/claimed?scope_id=...&page_size=...&page_token=....
func ListClaimedTasksHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scopeID, err := uuid.Parse(r.URL.Query().Get(scopeIDQueryParam))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "scope_id: invalid or missing UUID")
			return
		}

		pageSize, err := parsePageSizeParam(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		page, err := tasks.ListClaimedTasks(r.Context(), store.ListClaimedTasksParams{
			ScopeID: scopeID,
			Page: store.PageParams{
				PageSize:          pageSize,
				ContinuationToken: r.URL.Query().Get(pageTokenQueryParam),
			},
		})
		if err != nil {
			writeConsoleQueryError(w, err)
			return
		}

		resp := listClaimedTasksResponse{
			Tasks:     make([]ClaimedTaskWire, len(page.Items)),
			NextToken: page.NextToken,
		}
		for i, row := range page.Items {
			resp.Tasks[i] = ToClaimedTaskWire(row)
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// parsePageSizeParam parses page_size's query value, or 0 (ResolvePageSize's
// own "apply the default" input) when the parameter is absent.
func parsePageSizeParam(r *http.Request) (int, error) {
	raw := r.URL.Query().Get(pageSizeQueryParam)
	if raw == "" {
		return 0, nil
	}
	size, err := strconv.Atoi(raw)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("page_size: must be a non-negative integer")
	}
	return size, nil
}

// CancelledTaskDeliveryRefWire is the wire shape of one
// store.CancelledTaskDeliveryRef.
type CancelledTaskDeliveryRefWire struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// CancelledTaskWire is the wire shape of one store.CancelledTaskRow
// (FR10). Exported (mirrors ClaimedTaskWire's own precedent above) so
// krill/mcp/tools' list_cancelled_tasks tool can return this exact shape
// rather than an MCP-local mirror (LB7).
type CancelledTaskWire struct {
	TaskID                string                       `json:"task_id"`
	Title                 string                       `json:"title"`
	DeliveryRef           CancelledTaskDeliveryRefWire `json:"delivery_ref"`
	CancelledByActing     SubjectWire                  `json:"cancelled_by_acting"`
	CancelledByOnBehalfOf SubjectWire                  `json:"cancelled_by_on_behalf_of"`
	CancelledAt           time.Time                    `json:"cancelled_at"`
}

// ToCancelledTaskWire converts one store.CancelledTaskRow to its wire
// shape.
func ToCancelledTaskWire(row store.CancelledTaskRow) CancelledTaskWire {
	return CancelledTaskWire{
		TaskID: row.TaskID.String(),
		Title:  row.Title,
		DeliveryRef: CancelledTaskDeliveryRefWire{
			ID:    row.DeliveryRef.ID.String(),
			Kind:  string(row.DeliveryRef.Kind),
			Title: row.DeliveryRef.Title,
		},
		CancelledByActing:     ToSubjectWire(row.CancelledByActing),
		CancelledByOnBehalfOf: ToSubjectWire(row.CancelledByOnBehalfOf),
		CancelledAt:           row.CancelledAt,
	}
}

// listCancelledTasksResponse is ListCancelledTasksHandler's response body
// (NFR6): a bounded page plus a continuation token, present exactly when
// more rows remain.
type listCancelledTasksResponse struct {
	Tasks     []CancelledTaskWire `json:"tasks"`
	NextToken string              `json:"next_token,omitempty"`
}

// ListCancelledTasksHandler returns the console cancelled-task view
// (FR10): GET /console/cancelled?scope_id=...&page_size=...&page_token=....
// Ungated like ListClaimedTasksHandler above -- the query has no single
// path entity to resolve scope_id from, so scope_id is a required query
// parameter here too.
func ListCancelledTasksHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scopeID, err := uuid.Parse(r.URL.Query().Get(scopeIDQueryParam))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "scope_id: invalid or missing UUID")
			return
		}

		pageSize, err := parsePageSizeParam(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		page, err := tasks.ListCancelledTasks(r.Context(), store.ListCancelledTasksParams{
			ScopeID: scopeID,
			Page: store.PageParams{
				PageSize:          pageSize,
				ContinuationToken: r.URL.Query().Get(pageTokenQueryParam),
			},
		})
		if err != nil {
			writeConsoleQueryError(w, err)
			return
		}

		resp := listCancelledTasksResponse{
			Tasks:     make([]CancelledTaskWire, len(page.Items)),
			NextToken: page.NextToken,
		}
		for i, row := range page.Items {
			resp.Tasks[i] = ToCancelledTaskWire(row)
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// OpenNoteTaskContextWire is the wire shape of one
// store.OpenNoteTaskContext.
type OpenNoteTaskContextWire struct {
	TaskID      string                     `json:"task_id"`
	Title       string                     `json:"title"`
	DeliveryRef ClaimedTaskDeliveryRefWire `json:"delivery_ref"`
}

// OpenNoteEntityContextWire is the wire shape of one
// store.OpenNoteEntityContext.
type OpenNoteEntityContextWire struct {
	EntityKind string `json:"entity_kind"`
	EntityID   string `json:"entity_id"`
	Title      string `json:"title"`
}

// OpenNoteWire is the wire shape of one store.OpenNoteRow (FR12). Exported
// (mirrors ClaimedTaskWire's own precedent) so krill/mcp/tools' list_open_
// notes tool can return this exact shape rather than an MCP-local mirror
// (LB7). Exactly one of TaskContext/EntityContext is set, mirroring
// store.OpenNoteRow's own doc comment.
type OpenNoteWire struct {
	NoteID    string                     `json:"note_id"`
	Kind      string                     `json:"kind"`
	Body      string                     `json:"body"`
	CreatedAt time.Time                  `json:"created_at"`
	Task      *OpenNoteTaskContextWire   `json:"task,omitempty"`
	Entity    *OpenNoteEntityContextWire `json:"entity,omitempty"`
}

// ToOpenNoteWire converts one store.OpenNoteRow to its wire shape.
func ToOpenNoteWire(row store.OpenNoteRow) OpenNoteWire {
	wire := OpenNoteWire{
		NoteID:    row.NoteID.String(),
		Kind:      string(row.Kind),
		Body:      row.Body,
		CreatedAt: row.CreatedAt,
	}
	if row.TaskContext != nil {
		wire.Task = &OpenNoteTaskContextWire{
			TaskID: row.TaskContext.TaskID.String(),
			Title:  row.TaskContext.Title,
			DeliveryRef: ClaimedTaskDeliveryRefWire{
				ID:    row.TaskContext.DeliveryRef.ID.String(),
				Kind:  string(row.TaskContext.DeliveryRef.Kind),
				Title: row.TaskContext.DeliveryRef.Title,
			},
		}
	}
	if row.EntityContext != nil {
		wire.Entity = &OpenNoteEntityContextWire{
			EntityKind: string(row.EntityContext.EntityKind),
			EntityID:   row.EntityContext.EntityID.String(),
			Title:      row.EntityContext.Title,
		}
	}
	return wire
}

// listOpenNotesResponse is ListOpenNotesHandler's response body (NFR6): a
// bounded page plus a continuation token, present exactly when more rows
// remain.
type listOpenNotesResponse struct {
	Notes     []OpenNoteWire `json:"notes"`
	NextToken string         `json:"next_token,omitempty"`
}

// ListOpenNotesHandler returns the console open-notes view (FR12): GET
// /console/notes?scope_id=...&page_size=...&page_token=.... Ungated like
// ListClaimedTasksHandler -- see this file's own doc comment.
func ListOpenNotesHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scopeID, err := uuid.Parse(r.URL.Query().Get(scopeIDQueryParam))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "scope_id: invalid or missing UUID")
			return
		}

		pageSize, err := parsePageSizeParam(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		page, err := tasks.ListOpenNotes(r.Context(), store.ListOpenNotesParams{
			ScopeID: scopeID,
			Page: store.PageParams{
				PageSize:          pageSize,
				ContinuationToken: r.URL.Query().Get(pageTokenQueryParam),
			},
		})
		if err != nil {
			writeConsoleQueryError(w, err)
			return
		}

		resp := listOpenNotesResponse{
			Notes:     make([]OpenNoteWire, len(page.Items)),
			NextToken: page.NextToken,
		}
		for i, row := range page.Items {
			resp.Notes[i] = ToOpenNoteWire(row)
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// EscalatedTaskDeliveryRefWire is the wire shape of one
// store.EscalatedTaskDeliveryRef.
type EscalatedTaskDeliveryRefWire struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// EscalatedTaskWire is the wire shape of one store.EscalatedTaskRow (FR5,
// issue #2875). Exported (mirrors ClaimedTaskWire's/CancelledTaskWire's
// own precedent above) so krill/mcp/tools' list_escalated_tasks tool can
// return this exact shape rather than an MCP-local mirror (LB7).
// CounterValue/CapValue/MostRecentVerdict omit from the wire body when nil
// (NULL for a manual reason's counter/cap, and for MostRecentVerdict
// whenever it is not derivable -- store.EscalatedTaskRow's own doc
// comment) rather than serializing as a literal `null`.
type EscalatedTaskWire struct {
	TaskID      string                       `json:"task_id"`
	Title       string                       `json:"title"`
	DeliveryRef EscalatedTaskDeliveryRefWire `json:"delivery_ref"`

	Reason       string `json:"reason"`
	CounterValue *int   `json:"counter_value,omitempty"`
	CapValue     *int   `json:"cap_value,omitempty"`

	Lane        string    `json:"lane"`
	EscalatedAt time.Time `json:"escalated_at"`

	EscalatedByActing     SubjectWire `json:"escalated_by_acting"`
	EscalatedByOnBehalfOf SubjectWire `json:"escalated_by_on_behalf_of"`

	AttemptCount        int     `json:"attempt_count"`
	FailingVerdictCount int     `json:"failing_verdict_count"`
	MostRecentVerdict   *string `json:"most_recent_verdict,omitempty"`
	NoteCount           int     `json:"note_count"`
}

// ToEscalatedTaskWire converts one store.EscalatedTaskRow to its wire
// shape. Never carries a per-attempt, per-verdict, or per-note array --
// FR5/NFR6/#2851 Assumption 11's hard constraint that this row is summary
// history only, enforced here by this type simply having no such field to
// populate.
func ToEscalatedTaskWire(row store.EscalatedTaskRow) EscalatedTaskWire {
	var mostRecentVerdict *string
	if row.MostRecentVerdict != nil {
		v := string(*row.MostRecentVerdict)
		mostRecentVerdict = &v
	}
	return EscalatedTaskWire{
		TaskID: row.TaskID.String(),
		Title:  row.Title,
		DeliveryRef: EscalatedTaskDeliveryRefWire{
			ID:    row.DeliveryRef.ID.String(),
			Kind:  string(row.DeliveryRef.Kind),
			Title: row.DeliveryRef.Title,
		},
		Reason:                string(row.Reason),
		CounterValue:          row.CounterValue,
		CapValue:              row.CapValue,
		Lane:                  string(row.Lane),
		EscalatedAt:           row.EscalatedAt,
		EscalatedByActing:     ToSubjectWire(row.EscalatedByActing),
		EscalatedByOnBehalfOf: ToSubjectWire(row.EscalatedByOnBehalfOf),
		AttemptCount:          row.AttemptCount,
		FailingVerdictCount:   row.FailingVerdictCount,
		MostRecentVerdict:     mostRecentVerdict,
		NoteCount:             row.NoteCount,
	}
}

// listEscalatedTasksResponse is ListEscalatedTasksHandler's response body
// (NFR6): a bounded page plus a continuation token, present exactly when
// more rows remain.
type listEscalatedTasksResponse struct {
	Tasks     []EscalatedTaskWire `json:"tasks"`
	NextToken string              `json:"next_token,omitempty"`
}

// ListEscalatedTasksHandler returns the console escalated-task view (FR5):
// GET /console/escalated?scope_id=...&page_size=...&page_token=....
// Ungated like ListClaimedTasksHandler above -- the query has no single
// path entity to resolve scope_id from, so scope_id is a required query
// parameter here too.
func ListEscalatedTasksHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scopeID, err := uuid.Parse(r.URL.Query().Get(scopeIDQueryParam))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "scope_id: invalid or missing UUID")
			return
		}

		pageSize, err := parsePageSizeParam(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		page, err := tasks.ListEscalatedTasks(r.Context(), store.ListEscalatedTasksParams{
			ScopeID: scopeID,
			Page: store.PageParams{
				PageSize:          pageSize,
				ContinuationToken: r.URL.Query().Get(pageTokenQueryParam),
			},
		})
		if err != nil {
			writeConsoleQueryError(w, err)
			return
		}

		resp := listEscalatedTasksResponse{
			Tasks:     make([]EscalatedTaskWire, len(page.Items)),
			NextToken: page.NextToken,
		}
		for i, row := range page.Items {
			resp.Tasks[i] = ToEscalatedTaskWire(row)
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// writeConsoleQueryError maps a console query's store error onto this
// package's one JSON error shape -- store.ErrTokenScopeMismatch and
// store.ErrInvalidContinuationToken (paging.go) are caller errors (a
// stale, cross-scope, or forged token), never a genuine store failure.
func writeConsoleQueryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrTokenScopeMismatch), errors.Is(err, store.ErrInvalidContinuationToken):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSONError(w, http.StatusInternalServerError, "internal error")
	}
}
