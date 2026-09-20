// This file (issue #2869, FR4, NFR6) is M5's console query HTTP surface:
// the home for every console handler this milestone adds. This task ships
// GET /console/claimed over store.TaskStore.ListClaimedTasks (FR4).
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
