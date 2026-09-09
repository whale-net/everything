package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	whagentpb "github.com/whale-net/everything/whagent_net/protos"
)

// This file guards issue #2247's Testing section: GET /sessions' five FR3
// filters map 1:1 onto ListSessionsRequest (an unset filter is omitted,
// never sent as a zero value; started_by_kind=service round-trips),
// next/prev pagination returns to the original page, the pagination
// controls are absent at the correct end, and NFR3's "started by"
// rendering shows a service-started session and a session started by
// someone other than the viewer.

// fakeSessionListServer is a real whagentpb.SessionServiceServer serving
// ListSessions only (any other RPC panics on the embedded
// UnimplementedSessionServiceServer -- these tests only exercise
// handleSessionList's call graph). It captures the last request it saw
// (for the filter-mapping assertions) and paginates its fixed sessions
// slice by an integer offset encoded in page_token/prev_page_token/
// next_page_token -- deliberately ignoring req.PageSize in favor of
// pageSize, so pagination-boundary tests can use a small, easy-to-reason
// -about page size independently of sessionListPageSize's production
// value. It does not apply req's filters to sessions -- filter mapping
// (this file's TestHandleSessionList_FiltersMapToRequest) and pagination/
// rendering (this file's other tests) are proven independently, so the
// fake never needs to combine both.
type fakeSessionListServer struct {
	whagentpb.UnimplementedSessionServiceServer

	sessions []*whagentpb.Session
	pageSize int

	lastReq *whagentpb.ListSessionsRequest
}

func (f *fakeSessionListServer) ListSessions(ctx context.Context, req *whagentpb.ListSessionsRequest) (*whagentpb.ListSessionsResponse, error) {
	f.lastReq = req

	offset := 0
	if req.GetPageToken() != "" {
		o, err := strconv.Atoi(req.GetPageToken())
		if err != nil {
			return nil, err
		}
		offset = o
	}

	size := f.pageSize
	if size <= 0 || size > len(f.sessions) {
		size = len(f.sessions)
	}

	end := offset + size
	if end > len(f.sessions) {
		end = len(f.sessions)
	}
	if offset > end {
		offset = end
	}

	resp := &whagentpb.ListSessionsResponse{Sessions: f.sessions[offset:end]}
	if end < len(f.sessions) {
		resp.NextPageToken = strconv.Itoa(end)
	}
	if offset > 0 {
		prevOffset := offset - size
		if prevOffset < 0 {
			prevOffset = 0
		}
		resp.PrevPageToken = strconv.Itoa(prevOffset)
	}
	return resp, nil
}

// renderSessionList drives handleSessionList through the real
// RequireAuthFunc/WithAccessToken wrapping setupRoutes uses (mirrors
// handlers_session_test.go's renderSessionDetail). query is the raw
// query string (no leading "?"); an empty query renders GET /sessions
// with no params. When hxRequest is true, the request carries an
// HX-Request header, exercising handleSessionList's fragment-only branch.
func renderSessionList(t *testing.T, app *App, query string, hxRequest bool) *httptest.ResponseRecorder {
	t.Helper()
	wrapped := app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSessionList))

	target := "/sessions"
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if hxRequest {
		req.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	wrapped(w, req)
	return w
}

func newSessionListTestApp(t *testing.T, server *fakeSessionListServer) *App {
	t.Helper()
	return &App{
		auth:    devModeAuthenticator(t),
		session: newBufconnUISessionClient(t, server),
	}
}

// TestHandleSessionList_FiltersMapToRequest is this task's Testing
// section's first bullet: "each filter control produces the expected
// ListSessionsRequest; an unset filter is omitted rather than sent as a
// zero value; started_by_kind = service round-trips."
func TestHandleSessionList_FiltersMapToRequest(t *testing.T) {
	tests := []struct {
		name  string
		query string
		check func(t *testing.T, req *whagentpb.ListSessionsRequest)
	}{
		{
			name:  "no filters set: every optional field is omitted (nil), never a zero value",
			query: "",
			check: func(t *testing.T, req *whagentpb.ListSessionsRequest) {
				require.Nil(t, req.AgentId, "AgentId must be nil when agent_id is unset")
				require.Nil(t, req.State, "State must be nil when state is unset")
				require.Nil(t, req.StartedByKind, "StartedByKind must be nil when started_by_kind is unset")
				require.Nil(t, req.StartedAfter, "StartedAfter must be nil when started_after is unset")
				require.Nil(t, req.StartedBefore, "StartedBefore must be nil when started_before is unset")
				require.Empty(t, req.PageToken)
			},
		},
		{
			name:  "agent_id filter",
			query: "agent_id=agent-1",
			check: func(t *testing.T, req *whagentpb.ListSessionsRequest) {
				require.NotNil(t, req.AgentId)
				require.Equal(t, "agent-1", req.GetAgentId())
			},
		},
		{
			name:  "state filter",
			query: "state=running",
			check: func(t *testing.T, req *whagentpb.ListSessionsRequest) {
				require.NotNil(t, req.State)
				require.Equal(t, whagentpb.SessionState_SESSION_STATE_RUNNING, req.GetState())
			},
		},
		{
			name:  "started_by_kind=service round-trips",
			query: "started_by_kind=service",
			check: func(t *testing.T, req *whagentpb.ListSessionsRequest) {
				require.NotNil(t, req.StartedByKind)
				require.Equal(t, whagentpb.SubjectKind_SUBJECT_KIND_SERVICE, req.GetStartedByKind())
			},
		},
		{
			name:  "started_by_kind=human round-trips",
			query: "started_by_kind=human",
			check: func(t *testing.T, req *whagentpb.ListSessionsRequest) {
				require.NotNil(t, req.StartedByKind)
				require.Equal(t, whagentpb.SubjectKind_SUBJECT_KIND_HUMAN, req.GetStartedByKind())
			},
		},
		{
			name:  "started_after/started_before filters",
			query: "started_after=2024-01-02T03%3A04&started_before=2024-06-07T08%3A09",
			check: func(t *testing.T, req *whagentpb.ListSessionsRequest) {
				require.NotNil(t, req.StartedAfter)
				require.NotNil(t, req.StartedBefore)
				require.True(t, req.GetStartedAfter().AsTime().Equal(time.Date(2024, 1, 2, 3, 4, 0, 0, time.UTC)))
				require.True(t, req.GetStartedBefore().AsTime().Equal(time.Date(2024, 6, 7, 8, 9, 0, 0, time.UTC)))
			},
		},
		{
			name:  "page_token passes through opaquely",
			query: "page_token=17",
			check: func(t *testing.T, req *whagentpb.ListSessionsRequest) {
				require.Equal(t, "17", req.GetPageToken())
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := &fakeSessionListServer{}
			app := newSessionListTestApp(t, server)

			w := renderSessionList(t, app, tc.query, true)
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			require.NotNil(t, server.lastReq)
			tc.check(t, server.lastReq)
		})
	}
}

// TestBuildListSessionsRequest_InvalidFilterIsBadRequest covers the
// buildListSessionsRequest error path handleSessionList maps to a 400 --
// an unparseable state/started_by_kind/time value must not reach
// ListSessions at all.
func TestBuildListSessionsRequest_InvalidFilterIsBadRequest(t *testing.T) {
	server := &fakeSessionListServer{}
	app := newSessionListTestApp(t, server)

	w := renderSessionList(t, app, "state=not-a-real-state", true)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Nil(t, server.lastReq, "ListSessions must not be called for an invalid filter")
}

// newFixtureListSession builds a minimal, valid Session for the pagination/
// rendering tests below: id, a fixed agent, RUNNING state, and
// createdAt/subject/onBehalfOf as given.
func newFixtureListSession(id string, createdAt time.Time, onBehalfOf *whagentpb.Subject) *whagentpb.Session {
	ts := timestamppb.New(createdAt)
	return &whagentpb.Session{
		SessionId:  id,
		State:      whagentpb.SessionState_SESSION_STATE_RUNNING,
		AgentId:    "agent-1",
		Subject:    onBehalfOf,
		OnBehalfOf: onBehalfOf,
		CreatedAt:  ts,
		UpdatedAt:  ts,
	}
}

// TestHandleSessionList_PaginationRoundTrip is this task's Testing
// section's second bullet: "next then prev returns to the original page
// (same rows, same order); the prev control is absent on page one and
// the next control is absent on the last page."
func TestHandleSessionList_PaginationRoundTrip(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	human := &whagentpb.Subject{Iss: "https://issuer.example.com", Sub: "dev-user", Kind: whagentpb.SubjectKind_SUBJECT_KIND_HUMAN}

	var sessions []*whagentpb.Session
	for i := 0; i < 5; i++ {
		sessions = append(sessions, newFixtureListSession(uuid.New().String(), base.Add(time.Duration(i)*time.Hour), human))
	}

	server := &fakeSessionListServer{sessions: sessions, pageSize: 2}
	app := newSessionListTestApp(t, server)

	// Page one: no page_token. Prev control must be absent; next present.
	page1 := renderSessionList(t, app, "", true)
	require.Equal(t, http.StatusOK, page1.Code, "body: %s", page1.Body.String())
	body1 := page1.Body.String()
	requireNoPreviousLink(t, body1)
	requireNextLink(t, body1)
	require.True(t, strings.Contains(body1, sessions[0].SessionId))
	require.True(t, strings.Contains(body1, sessions[1].SessionId))
	require.False(t, strings.Contains(body1, sessions[2].SessionId))
	require.Empty(t, server.lastReq.GetPageToken(), "page one's own request must carry no page_token")
	require.True(t, strings.Contains(body1, `href="/sessions?page_token=2"`), "expected page one's Next control to carry the next page's opaque token, got %q", body1)

	// Page two: page_token=2 is exactly what page one's rendered Next
	// control encodes (fakeSessionListServer's offset scheme, pageSize=2).
	page2 := renderSessionList(t, app, "page_token=2", true)
	require.Equal(t, http.StatusOK, page2.Code, "body: %s", page2.Body.String())
	body2 := page2.Body.String()
	requirePreviousLink(t, body2)
	requireNextLink(t, body2)
	require.True(t, strings.Contains(body2, sessions[2].SessionId))
	require.True(t, strings.Contains(body2, sessions[3].SessionId))

	// Last page: page_token=4, only one row left, next control absent.
	page3 := renderSessionList(t, app, "page_token=4", true)
	require.Equal(t, http.StatusOK, page3.Code, "body: %s", page3.Body.String())
	body3 := page3.Body.String()
	requirePreviousLink(t, body3)
	requireNoNextLink(t, body3)
	require.True(t, strings.Contains(body3, sessions[4].SessionId))

	// Prev from page two returns to page one: same rows, same order.
	prevBack := renderSessionList(t, app, "page_token=0", true)
	require.Equal(t, http.StatusOK, prevBack.Code, "body: %s", prevBack.Body.String())
	bodyBack := prevBack.Body.String()
	requireNoPreviousLink(t, bodyBack)
	requireNextLink(t, bodyBack)
	idxFirst := strings.Index(bodyBack, sessions[0].SessionId)
	idxSecond := strings.Index(bodyBack, sessions[1].SessionId)
	require.True(t, idxFirst >= 0 && idxSecond >= 0 && idxFirst < idxSecond,
		"expected page one's rows back in the original order, got %q", bodyBack)
	require.False(t, strings.Contains(bodyBack, sessions[2].SessionId))
}

func requirePreviousLink(t *testing.T, body string) {
	t.Helper()
	require.True(t, strings.Contains(body, `>Previous</a>`), "expected an active Previous link, got %q", body)
}

func requireNoPreviousLink(t *testing.T, body string) {
	t.Helper()
	require.False(t, strings.Contains(body, `>Previous</a>`), "expected no active Previous link (page one), got %q", body)
	require.True(t, strings.Contains(body, `btn-disabled" aria-disabled="true">Previous</span>`), "expected a disabled Previous control, got %q", body)
}

func requireNextLink(t *testing.T, body string) {
	t.Helper()
	require.True(t, strings.Contains(body, `>Next</a>`), "expected an active Next link, got %q", body)
}

func requireNoNextLink(t *testing.T, body string) {
	t.Helper()
	require.False(t, strings.Contains(body, `>Next</a>`), "expected no active Next link (last page), got %q", body)
	require.True(t, strings.Contains(body, `btn-disabled" aria-disabled="true">Next</span>`), "expected a disabled Next control, got %q", body)
}

// TestHandleSessionList_StartedByRendering is this task's Testing
// section's third bullet: "a service-started session renders its
// 'started by' as a service; a session started by another user still
// appears" (NFR3, and FR3/C15's "list shows sessions started by anyone").
func TestHandleSessionList_StartedByRendering(t *testing.T) {
	now := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	service := &whagentpb.Subject{Iss: "https://issuer.example.com", Sub: "svc-worker", Kind: whagentpb.SubjectKind_SUBJECT_KIND_SERVICE}
	otherUser := &whagentpb.Subject{Iss: "https://issuer.example.com", Sub: "someone-else", Kind: whagentpb.SubjectKind_SUBJECT_KIND_HUMAN}

	serviceSession := newFixtureListSession("session-service", now, service)
	otherUserSession := newFixtureListSession("session-other-user", now.Add(-time.Hour), otherUser)

	server := &fakeSessionListServer{sessions: []*whagentpb.Session{serviceSession, otherUserSession}}
	app := newSessionListTestApp(t, server)

	w := renderSessionList(t, app, "", true)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()

	require.True(t, strings.Contains(body, "session-service"))
	require.True(t, strings.Contains(body, "svc-worker"))
	require.True(t, strings.Contains(body, ">service<"), "expected the service-started session's kind badge, got %q", body)

	// Started by someone other than the viewer (dev-user) still appears --
	// there is no owner-only filter on the list (FR3/C15).
	require.True(t, strings.Contains(body, "session-other-user"))
	require.True(t, strings.Contains(body, "someone-else"))
}
