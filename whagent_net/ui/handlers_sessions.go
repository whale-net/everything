package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	whagentpb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/ui/components"
)

// sessionListPageSize is GET /sessions' fixed ListSessions page size
// (FR3/C15, issue #2247). No page_size query param is exposed to the
// operator -- next/prev pagination through ListSessionsResponse's opaque
// cursors is the whole M2 surface (issue #2247's Context: "no search box
// or sortable column headers").
const sessionListPageSize = 25

// parseSessionListFilters reads GET /sessions' five FR3 filter query
// params into components.SessionListFilters, exactly as submitted -- no
// validation here, buildListSessionsRequest below is where an
// unparseable state/started_by_kind/time value becomes a 400. Each
// filter's empty string means "unset" (components.SessionListFilters'
// doc comment).
func parseSessionListFilters(r *http.Request) components.SessionListFilters {
	q := r.URL.Query()
	return components.SessionListFilters{
		AgentID:       strings.TrimSpace(q.Get("agent_id")),
		State:         strings.TrimSpace(q.Get("state")),
		StartedByKind: strings.TrimSpace(q.Get("started_by_kind")),
		StartedAfter:  strings.TrimSpace(q.Get("started_after")),
		StartedBefore: strings.TrimSpace(q.Get("started_before")),
	}
}

// buildListSessionsRequest converts f + pageToken into a
// whagentpb.ListSessionsRequest 1:1 (issue #2247's Implementation:
// "Filters map 1:1 onto ListSessionsRequest's fields ... the UI performs
// no client-side filtering or sorting over a fetched page"). An unset
// filter (f's empty string) is left nil on the request, never sent as a
// zero-value enum/string -- see this task's Testing section's red/green
// note ("send a zero-value filter unconditionally and confirm the
// 'unset filter is omitted' test goes red").
func buildListSessionsRequest(f components.SessionListFilters, pageToken string) (*whagentpb.ListSessionsRequest, error) {
	req := &whagentpb.ListSessionsRequest{
		PageSize:  sessionListPageSize,
		PageToken: pageToken,
	}

	if f.AgentID != "" {
		agentID := f.AgentID
		req.AgentId = &agentID
	}

	if f.State != "" {
		state, ok := sessionStateFromString(f.State)
		if !ok {
			return nil, fmt.Errorf("invalid state %q", f.State)
		}
		req.State = &state
	}

	if f.StartedByKind != "" {
		kind, ok := subjectKindFromString(f.StartedByKind)
		if !ok {
			return nil, fmt.Errorf("invalid started_by_kind %q", f.StartedByKind)
		}
		req.StartedByKind = &kind
	}

	if f.StartedAfter != "" {
		t, err := time.ParseInLocation(components.SessionListTimeFormat, f.StartedAfter, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("invalid started_after %q: %w", f.StartedAfter, err)
		}
		req.StartedAfter = timestamppb.New(t)
	}

	if f.StartedBefore != "" {
		t, err := time.ParseInLocation(components.SessionListTimeFormat, f.StartedBefore, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("invalid started_before %q: %w", f.StartedBefore, err)
		}
		req.StartedBefore = timestamppb.New(t)
	}

	return req, nil
}

// handleSessionList is GET /sessions (FR3/C15, NFR3, issue #2247): the
// authenticated landing page (mounted at both "/" and "/sessions",
// replacing issue #2236's placeholder index), listing sessions started by
// anyone -- the read-any-session rule #2237 establishes for GetSession/
// ReadTranscript applies here too, there is no owner filter -- with the
// five FR3 filters and next/prev keyset pagination.
//
// The filter form (components.sessionListFilterForm) and the pagination
// links (components.sessionListPagination) both plain-GET back to this
// same route, targeting #session-list-results, so a filtered/paged view's
// query string is always exactly what produced what's on screen --
// linkable, bookmarkable, and reload-safe (Implementation). An HTMX
// request (HX-Request header set, from either of those) gets only the
// results fragment rendered directly via component.Render -- no
// RenderTempl/layout wrap -- mirroring
// manmanv2/ui/handlers_deployment_actions.go's identical HX-Request
// branch; a full (non-HTMX) request gets the whole page.
func (app *App) handleSessionList(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()

	filters := parseSessionListFilters(r)
	pageToken := r.URL.Query().Get("page_token")

	req, err := buildListSessionsRequest(filters, pageToken)
	if err != nil {
		http.Error(w, "invalid filter: "+err.Error(), http.StatusBadRequest)
		return
	}

	data := components.SessionListPageData{Filters: filters}

	resp, err := app.session.Client().ListSessions(ctx, req)
	if err != nil {
		// api unavailable (Implementation: "Empty and error states are
		// explicit ... vs. api unavailable") -- render the page/fragment
		// with FetchError set rather than failing the whole request, so
		// the filter form and nav chrome stay usable.
		logger.Error("failed to list sessions", "error", err)
		data.FetchError = "the whagent-net api is unavailable"
	} else {
		sessions := resp.GetSessions()
		data.Sessions = make([]components.SessionView, len(sessions))
		for i, sess := range sessions {
			data.Sessions[i] = sessionToView(sess)
		}
		data.PrevPageToken = resp.GetPrevPageToken()
		data.HasPrev = data.PrevPageToken != ""
		data.NextPageToken = resp.GetNextPageToken()
		data.HasNext = data.NextPageToken != ""
	}

	data.Layout = components.LayoutData{
		Title:  "Sessions",
		Active: "Sessions",
		User:   htmxauth.GetUser(ctx),
	}

	if r.Header.Get("HX-Request") != "" {
		if err := components.SessionListResults(data).Render(ctx, w); err != nil {
			logger.Error("failed to render session list fragment", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	if err := RenderTempl(w, r, data.Layout.Title, components.SessionList(data)); err != nil {
		logger.Error("failed to render session list page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
