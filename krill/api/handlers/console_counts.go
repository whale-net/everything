// This file (FR c4ab6c68) is the console count surface: the HTTP twins of
// the store's count reads, so every number the console shows next to a
// queue -- the queue's own size, and the Overview's headline figures --
// arrives from a read that took the same filters as the list it sits
// beside rather than from a page length or a second filter vocabulary.
//
// Each count endpoint here and its list endpoint parse the query string
// through one shared helper (parseClaimedTasksParams and its siblings),
// so a filter parameter added for the list is added for the count too, by
// construction rather than by remembering.
package handlers

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// writeParamParseError is the one 400 every shared query-string parser in
// this file funnels through, so a bad filter reads the same whether it
// arrived on a list endpoint or the count endpoint beside it.
func writeParamParseError(w http.ResponseWriter, err error) {
	writeJSONError(w, http.StatusBadRequest, err.Error())
}

// ConsoleCountWire is the response body of every per-queue count endpoint
// (FR c4ab6c68). Exported so krill/mcp/tools' count_* tools return this
// exact shape rather than an MCP-local mirror (LB7). Count is the number
// of rows the matching list would return unpaged under the same filters,
// not the length of any one page.
type ConsoleCountWire struct {
	Count int `json:"count"`
}

// ConsoleOverviewWire is the response body of GET /console/overview --
// the four queue sizes and the three sub-line figures, mirroring
// store.ConsoleOverviewCounts field for field (FR c4ab6c68). Exported for
// the console_overview_counts MCP tool's LB7 parity.
type ConsoleOverviewWire struct {
	Escalated int `json:"escalated"`
	Claimed   int `json:"claimed"`
	Cancelled int `json:"cancelled"`
	OpenNotes int `json:"open_notes"`

	EscalatedRecently  int `json:"escalated_recently"`
	ClaimsExpiringSoon int `json:"claims_expiring_soon"`
	OpenScopeNotes     int `json:"open_scope_notes"`
}

// ToConsoleOverviewWire converts one store.ConsoleOverviewCounts to its
// wire shape.
func ToConsoleOverviewWire(counts store.ConsoleOverviewCounts) ConsoleOverviewWire {
	return ConsoleOverviewWire{
		Escalated:          counts.Escalated,
		Claimed:            counts.Claimed,
		Cancelled:          counts.Cancelled,
		OpenNotes:          counts.OpenNotes,
		EscalatedRecently:  counts.EscalatedRecently,
		ClaimsExpiringSoon: counts.ClaimsExpiringSoon,
		OpenScopeNotes:     counts.OpenScopeNotes,
	}
}

// parseClaimedTasksParams is GET /console/claimed's and
// GET /console/claimed/count's one reader for the query string: scope_id
// (required -- this query has no path entity to resolve scope from, see
// console.go's own doc comment), plus page_size and page_token. A count
// endpoint simply ignores the Page it is handed.
func parseClaimedTasksParams(r *http.Request) (store.ListClaimedTasksParams, error) {
	scopeID, err := uuid.Parse(r.URL.Query().Get(scopeIDQueryParam))
	if err != nil {
		return store.ListClaimedTasksParams{}, queryParamError(scopeIDQueryParam, "invalid or missing UUID")
	}
	filter, err := parseConsoleFilter(r)
	if err != nil {
		return store.ListClaimedTasksParams{}, err
	}
	pageSize, err := parsePageSizeParam(r)
	if err != nil {
		return store.ListClaimedTasksParams{}, err
	}
	return store.ListClaimedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: filter,
		Page: store.PageParams{
			PageSize:          pageSize,
			ContinuationToken: r.URL.Query().Get(pageTokenQueryParam),
		},
	}, nil
}

// parseCancelledTasksParams, parseEscalatedTasksParams and
// parseOpenNotesParams are their queues' equivalents of
// parseClaimedTasksParams -- see that helper's own doc comment.
func parseCancelledTasksParams(r *http.Request) (store.ListCancelledTasksParams, error) {
	scopeID, err := uuid.Parse(r.URL.Query().Get(scopeIDQueryParam))
	if err != nil {
		return store.ListCancelledTasksParams{}, queryParamError(scopeIDQueryParam, "invalid or missing UUID")
	}
	filter, err := parseConsoleFilter(r)
	if err != nil {
		return store.ListCancelledTasksParams{}, err
	}
	pageSize, err := parsePageSizeParam(r)
	if err != nil {
		return store.ListCancelledTasksParams{}, err
	}
	return store.ListCancelledTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: filter,
		Page: store.PageParams{
			PageSize:          pageSize,
			ContinuationToken: r.URL.Query().Get(pageTokenQueryParam),
		},
	}, nil
}

func parseEscalatedTasksParams(r *http.Request) (store.ListEscalatedTasksParams, error) {
	scopeID, err := uuid.Parse(r.URL.Query().Get(scopeIDQueryParam))
	if err != nil {
		return store.ListEscalatedTasksParams{}, queryParamError(scopeIDQueryParam, "invalid or missing UUID")
	}
	filter, err := parseConsoleFilter(r)
	if err != nil {
		return store.ListEscalatedTasksParams{}, err
	}
	var reason *store.EscalationReason
	if raw := r.URL.Query().Get(reasonQueryParam); raw != "" {
		parsed := store.EscalationReason(raw)
		reason = &parsed
	}
	pageSize, err := parsePageSizeParam(r)
	if err != nil {
		return store.ListEscalatedTasksParams{}, err
	}
	return store.ListEscalatedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: filter,
		Reason:        reason,
		Page: store.PageParams{
			PageSize:          pageSize,
			ContinuationToken: r.URL.Query().Get(pageTokenQueryParam),
		},
	}, nil
}

func parseOpenNotesParams(r *http.Request) (store.ListOpenNotesParams, error) {
	scopeID, err := uuid.Parse(r.URL.Query().Get(scopeIDQueryParam))
	if err != nil {
		return store.ListOpenNotesParams{}, queryParamError(scopeIDQueryParam, "invalid or missing UUID")
	}
	filter, err := parseConsoleFilter(r)
	if err != nil {
		return store.ListOpenNotesParams{}, err
	}
	pageSize, err := parsePageSizeParam(r)
	if err != nil {
		return store.ListOpenNotesParams{}, err
	}
	return store.ListOpenNotesParams{
		ScopeID:       scopeID,
		ConsoleFilter: filter,
		Page: store.PageParams{
			PageSize:          pageSize,
			ContinuationToken: r.URL.Query().Get(pageTokenQueryParam),
		},
	}, nil
}

// CountClaimedTasksHandler returns the claimed queue's size: GET
// /console/claimed/count, taking the same query parameters as
// ListClaimedTasksHandler and returning how many rows that list would
// return unpaged. A count the store cannot compute is a 500, never a 0.
func CountClaimedTasksHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params, err := parseClaimedTasksParams(r)
		if err != nil {
			writeParamParseError(w, err)
			return
		}
		count, err := tasks.CountClaimedTasks(r.Context(), params)
		if err != nil {
			writeConsoleQueryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ConsoleCountWire{Count: count})
	}
}

// CountCancelledTasksHandler returns the cancelled queue's size: GET
// /console/cancelled/count -- see CountClaimedTasksHandler.
func CountCancelledTasksHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params, err := parseCancelledTasksParams(r)
		if err != nil {
			writeParamParseError(w, err)
			return
		}
		count, err := tasks.CountCancelledTasks(r.Context(), params)
		if err != nil {
			writeConsoleQueryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ConsoleCountWire{Count: count})
	}
}

// CountEscalatedTasksHandler returns the escalation queue's size: GET
// /console/escalated/count -- see CountClaimedTasksHandler.
func CountEscalatedTasksHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params, err := parseEscalatedTasksParams(r)
		if err != nil {
			writeParamParseError(w, err)
			return
		}
		count, err := tasks.CountEscalatedTasks(r.Context(), params)
		if err != nil {
			writeConsoleQueryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ConsoleCountWire{Count: count})
	}
}

// CountOpenNotesHandler returns the open-notes queue's size: GET
// /console/notes/count -- see CountClaimedTasksHandler. Per-product
// figures from this endpoint do not sum to the scope-wide one; see
// store.CountOpenNotes' own doc comment.
func CountOpenNotesHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params, err := parseOpenNotesParams(r)
		if err != nil {
			writeParamParseError(w, err)
			return
		}
		count, err := tasks.CountOpenNotes(r.Context(), params)
		if err != nil {
			writeConsoleQueryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ConsoleCountWire{Count: count})
	}
}

// ConsoleOverviewHandler returns every Overview figure in one call: GET
// /console/overview?scope_id=..., over the same scope each queue's own
// list endpoint takes. A count the store cannot compute fails the whole
// read rather than being reported as 0.
func ConsoleOverviewHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		escalated, err := parseEscalatedTasksParams(r)
		if err != nil {
			writeParamParseError(w, err)
			return
		}
		claimed, err := parseClaimedTasksParams(r)
		if err != nil {
			writeParamParseError(w, err)
			return
		}
		cancelled, err := parseCancelledTasksParams(r)
		if err != nil {
			writeParamParseError(w, err)
			return
		}
		notes, err := parseOpenNotesParams(r)
		if err != nil {
			writeParamParseError(w, err)
			return
		}

		counts, err := tasks.CountConsoleOverview(r.Context(), store.ConsoleOverviewParams{
			Escalated: escalated,
			Claimed:   claimed,
			Cancelled: cancelled,
			Notes:     notes,
		})
		if err != nil {
			writeConsoleQueryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ToConsoleOverviewWire(counts))
	}
}
