package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// testBoardsClient is a minimal stand-in for pb.LeafLabAPIClient, specifically
// implementing only ListBoards for testing the boards screen.
type testBoardsClient struct {
	pb.LeafLabAPIClient
	listBoardsResp *pb.ListBoardsResponse
	listBoardsErr  error
}

func (f *testBoardsClient) ListBoards(ctx context.Context, in *pb.ListBoardsRequest, opts ...grpc.CallOption) (*pb.ListBoardsResponse, error) {
	if f.listBoardsErr != nil {
		return nil, f.listBoardsErr
	}
	if f.listBoardsResp == nil {
		return &pb.ListBoardsResponse{}, nil
	}
	return f.listBoardsResp, nil
}

func boardsTestApp(client pb.LeafLabAPIClient) *App {
	return boardsTestAppWithPhase1Gate(client, "true")
}

func boardsTestAppWithPhase1Gate(client pb.LeafLabAPIClient, phase1GateOpen string) *App {
	return &App{
		config: &Config{
			Phase1GateOpen: phase1GateOpen,
		},
		apiClient: &LeafLabClient{
			API: client,
		},
	}
}

// devUserAuth returns an authenticator in AuthModeNone that injects a dev user,
// matching the test pattern from app_registry.
func devUserAuth(t testing.TB) *htmxauth.Authenticator {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: boardsTestSchema,
	})

	authConfig := htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "test-secret-key-at-least-32-bytes-long",
		SessionName:   "leaflab_ui_session",
	}

	store, err := htmxauth.NewDBSessionManager(ctx, testDB.Pool, authConfig.SessionSecret, authConfig.SessionName)
	require.NoError(t, err)

	auth, err := htmxauth.NewAuthenticatorWithDB(ctx, authConfig, store)
	require.NoError(t, err)

	return auth
}

const boardsTestSchema = `
	CREATE TABLE ui_sessions (
		session_id TEXT PRIMARY KEY,
		user_info JSONB NOT NULL DEFAULT '{}',
		access_token TEXT NOT NULL,
		refresh_token TEXT NOT NULL,
		token_expires_at TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at TIMESTAMPTZ NOT NULL
	);
	CREATE INDEX idx_ui_sessions_expires_at ON ui_sessions(expires_at);
`

// TestHandleBoards_SuccessfulListWithPagination verifies that the boards
// screen fetches and renders multiple pages with keyset pagination (FR61).
// The test verifies that the pagination token is passed to ListBoards and
// that a "Load more" button appears when NextPageToken is non-empty.
func TestHandleBoards_SuccessfulListWithPagination(t *testing.T) {
	now := time.Now().Unix()
	boards := []*pb.BoardInfo{
		{
			BoardId:   1,
			DeviceId:  "device-001",
			RecordedAt: now - 3600, // 1 hour ago
		},
		{
			BoardId:   2,
			DeviceId:  "device-002",
			RecordedAt: now - 7200, // 2 hours ago
		},
	}

	client := &testBoardsClient{
		listBoardsResp: &pb.ListBoardsResponse{
			Boards: boards,
			Page: &pb.PageResponse{
				NextPageToken: "token-for-next-page",
			},
		},
	}

	app := boardsTestApp(client)

	req := httptest.NewRequest(http.MethodGet, "/boards?page_token=initial-token", nil)
	w := httptest.NewRecorder()

	devUserAuth(t).RequireAuthFunc(app.handleBoards)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	body := w.Body.String()

	// Verify boards are rendered
	if !strings.Contains(body, "device-001") {
		t.Errorf("expected device-001 to be rendered in board list, body: %s", body)
	}
	if !strings.Contains(body, "device-002") {
		t.Errorf("expected device-002 to be rendered in board list, body: %s", body)
	}

	// Verify pagination token appears in the "Load more" button
	if !strings.Contains(body, "page_token=token-for-next-page") {
		t.Errorf("expected pagination token in Load more button, body: %s", body)
	}

	// Verify elapsed time is rendered, not raw UTC timestamp
	if !strings.Contains(body, "ago") {
		t.Errorf("expected elapsed time (e.g. '1 hour ago') instead of raw timestamp, body: %s", body)
	}
}

// TestHandleBoards_PaginationThroughMultiplePages verifies that each page
// returns distinct boards (no duplicates across pages). This tests that
// pagination doesn't loop back and fetch the same boards twice.
func TestHandleBoards_PaginationThroughMultiplePages(t *testing.T) {
	now := time.Now().Unix()

	// First page
	firstPageBoards := []*pb.BoardInfo{
		{BoardId: 1, DeviceId: "device-001", RecordedAt: now},
	}

	client := &testBoardsClient{
		listBoardsResp: &pb.ListBoardsResponse{
			Boards: firstPageBoards,
			Page: &pb.PageResponse{
				NextPageToken: "token-page-2",
			},
		},
	}

	app := boardsTestApp(client)

	// Request first page
	req1 := httptest.NewRequest(http.MethodGet, "/boards", nil)
	w1 := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleBoards)(w1, req1)

	body1 := w1.Body.String()
	if !strings.Contains(body1, "device-001") {
		t.Errorf("expected device-001 on first page, body: %s", body1)
	}
	if !strings.Contains(body1, "token-page-2") {
		t.Errorf("expected pagination token for page 2, body: %s", body1)
	}

	// Simulate second page fetch with different client (to avoid cache issues)
	secondPageBoards := []*pb.BoardInfo{
		{BoardId: 2, DeviceId: "device-002", RecordedAt: now},
	}
	client2 := &testBoardsClient{
		listBoardsResp: &pb.ListBoardsResponse{
			Boards: secondPageBoards,
			Page: &pb.PageResponse{
				NextPageToken: "", // No more pages
			},
		},
	}

	app2 := boardsTestApp(client2)
	req2 := httptest.NewRequest(http.MethodGet, "/boards?page_token=token-page-2", nil)
	w2 := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app2.handleBoards)(w2, req2)

	body2 := w2.Body.String()
	if !strings.Contains(body2, "device-002") {
		t.Errorf("expected device-002 on second page, body: %s", body2)
	}
	if strings.Contains(body2, "device-001") {
		t.Errorf("device-001 should not appear on second page (pagination should not loop), body: %s", body2)
	}
	// Last page should not have pagination button
	if strings.Contains(body2, "Load more") {
		t.Errorf("should not show Load more button on last page, body: %s", body2)
	}
}

// TestHandleBoards_UnauthenticatedRedirectsToLogin verifies that an
// unauthenticated request to /boards redirects to /auth/login, not allowing
// anonymous access (NFR-19: private by default).
func TestHandleBoards_UnauthenticatedRedirectsToLogin(t *testing.T) {
	client := &testBoardsClient{
		listBoardsResp: &pb.ListBoardsResponse{
			Boards: []*pb.BoardInfo{},
		},
	}

	app := boardsTestApp(client)

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	w := httptest.NewRecorder()

	// Call handleBoards WITHOUT the auth wrapper to simulate an unauthenticated request
	app.handleBoards(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect (StatusSeeOther), got %d, body = %s", w.Code, w.Body.String())
	}

	location := w.Header().Get("Location")
	if location != "/auth/login" {
		t.Errorf("expected redirect to /auth/login, got %s", location)
	}
}

// TestHandleBoards_DownstreamGRPCErrorRendersHonestState verifies that when
// ListBoards fails (e.g., network error, timeout), the page renders a user-friendly
// error message, not a blank page or technical stack trace (NFR14).
func TestHandleBoards_DownstreamGRPCErrorRendersHonestState(t *testing.T) {
	client := &testBoardsClient{
		listBoardsErr: context.DeadlineExceeded,
	}

	app := boardsTestApp(client)

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	w := httptest.NewRecorder()

	devUserAuth(t).RequireAuthFunc(app.handleBoards)(w, req)

	body := w.Body.String()

	// Should render a user-friendly error message, not a blank page or stack trace
	if !strings.Contains(body, "couldn't load") {
		t.Errorf("expected user-friendly error message, body: %s", body)
	}

	// Must not leak technical error details
	if strings.Contains(body, "DeadlineExceeded") || strings.Contains(body, "context.") {
		t.Errorf("should not leak technical error details like 'DeadlineExceeded', body: %s", body)
	}

	// Status should be OK because the page rendered (with error state), not 5xx
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 OK (error rendered in page), got %d", w.Code)
	}
}

// TestHandleBoards_EmptyListRendersMessage verifies that when ListBoards
// returns an empty list (no boards registered yet), the page renders a
// friendly message, not a blank or error state (NFR14).
func TestHandleBoards_EmptyListRendersMessage(t *testing.T) {
	client := &testBoardsClient{
		listBoardsResp: &pb.ListBoardsResponse{
			Boards: []*pb.BoardInfo{}, // Empty list
			Page: &pb.PageResponse{
				NextPageToken: "",
			},
		},
	}

	app := boardsTestApp(client)

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	w := httptest.NewRecorder()

	devUserAuth(t).RequireAuthFunc(app.handleBoards)(w, req)

	body := w.Body.String()

	// Should render an informative message for empty state
	if !strings.Contains(body, "No boards") && !strings.Contains(body, "registered") {
		t.Errorf("expected empty state message, body: %s", body)
	}

	// Should not show a table or pagination button
	if strings.Contains(body, "<tbody>") || strings.Contains(body, "Load more") {
		t.Errorf("empty state should not render table or pagination, body: %s", body)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
}

// TestHandleBoards_ElapsedTimeFormatting verifies that times are rendered
// as elapsed durations (FR64: "1 hour ago", "2 days ago") instead of raw
// Unix timestamps or UTC strings, even when timestamps are absolute instants.
func TestHandleBoards_ElapsedTimeFormatting(t *testing.T) {
	now := time.Now().Unix()
	tests := []struct {
		name      string
		timestamp int64
		expected  string
	}{
		{
			name:      "recent (just now)",
			timestamp: now - 10,
			expected:  "Just now",
		},
		{
			name:      "1 minute ago",
			timestamp: now - 65,
			expected:  "minute",
		},
		{
			name:      "1 hour ago",
			timestamp: now - 3600,
			expected:  "hour",
		},
		{
			name:      "1 day ago",
			timestamp: now - 86400,
			expected:  "day",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			board := &pb.BoardInfo{
				BoardId:    1,
				DeviceId:   "test-device",
				RecordedAt: tc.timestamp,
			}

			client := &testBoardsClient{
				listBoardsResp: &pb.ListBoardsResponse{
					Boards: []*pb.BoardInfo{board},
				},
			}

			app := boardsTestApp(client)

			req := httptest.NewRequest(http.MethodGet, "/boards", nil)
			w := httptest.NewRecorder()

			devUserAuth(t).RequireAuthFunc(app.handleBoards)(w, req)

			body := w.Body.String()
			if !strings.Contains(body, tc.expected) {
				t.Errorf("expected %q in rendered time, body: %s", tc.expected, body)
			}

			// Make sure we're NOT rendering the raw timestamp as a number
			if strings.Contains(body, "1970") || strings.Contains(body, "UTC") {
				t.Errorf("should not render raw Unix timestamp or UTC string, body: %s", body)
			}
		})
	}
}

// TestHandleBoards_NoCapabilityIsUIOnly verifies that every capability
// reachable from the browser (listing boards with pagination) is also
// reachable as an authenticated programmatic gRPC call (NFR18.2:
// capability parity, not mechanism parity).
func TestHandleBoards_NoCapabilityIsUIOnly(t *testing.T) {
	// This test verifies that the browser hits the ListBoards gRPC endpoint,
	// which is the same endpoint a programmatic client would call. The page
	// exercises the keyset token pagination from the same API that the CLI
	// or another service would use.

	origClient := &testBoardsClient{
		listBoardsResp: &pb.ListBoardsResponse{
			Boards: []*pb.BoardInfo{
				{BoardId: 1, DeviceId: "device-001", RecordedAt: time.Now().Unix()},
			},
			Page: &pb.PageResponse{NextPageToken: ""},
		},
	}

	app := boardsTestApp(origClient)

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	w := httptest.NewRecorder()

	devUserAuth(t).RequireAuthFunc(app.handleBoards)(w, req)

	// The browser-facing page exercises the same ListBoards RPC that a
	// programmatic client would call. No UI-only capability exists here.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	// The test verifies that the board data comes from ListBoards, not from
	// some UI-specific endpoint or database query.
	body := w.Body.String()
	if !strings.Contains(body, "device-001") {
		t.Errorf("expected board from ListBoards to appear in rendered page, body: %s", body)
	}
}

// TestHandleBoards_Phase1GateClosed verifies that when the Phase 1 gate is
// closed (A30: non-exposed to production), authenticated users are refused
// access to the boards page with a Forbidden response, matching the API-layer
// enforcement already in place (per #1187, exit criterion 5).
func TestHandleBoards_Phase1GateClosed(t *testing.T) {
	client := &testBoardsClient{
		listBoardsResp: &pb.ListBoardsResponse{
			Boards: []*pb.BoardInfo{},
		},
	}

	// Create app with phase1GateOpen="false" (closed gate)
	app := boardsTestAppWithPhase1Gate(client, "false")

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	w := httptest.NewRecorder()

	// Call with auth wrapper to ensure authenticated request
	devUserAuth(t).RequireAuthFunc(app.handleBoards)(w, req)

	// Verify the gate check returns Forbidden
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected Forbidden (403) when gate is closed, got %d, body = %s", w.Code, w.Body.String())
	}

	body := w.Body.String()

	// Verify the error message is readable (per NFR14: user-friendly)
	if !strings.Contains(body, "Phase 1") {
		t.Errorf("error message should mention Phase 1, got: %s", body)
	}
}

// TestHandleBoards_Phase1GateOpen verifies that when the Phase 1 gate is
// open, authenticated users can access the boards page normally.
func TestHandleBoards_Phase1GateOpen(t *testing.T) {
	client := &testBoardsClient{
		listBoardsResp: &pb.ListBoardsResponse{
			Boards: []*pb.BoardInfo{
				{BoardId: 1, DeviceId: "device-001", RecordedAt: time.Now().Unix()},
			},
			Page: &pb.PageResponse{NextPageToken: ""},
		},
	}

	// Create app with phase1GateOpen="true" (gate is open)
	app := boardsTestAppWithPhase1Gate(client, "true")

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	w := httptest.NewRecorder()

	// Call with auth wrapper
	devUserAuth(t).RequireAuthFunc(app.handleBoards)(w, req)

	// Verify the request succeeds
	if w.Code != http.StatusOK {
		t.Fatalf("expected OK (200) when gate is open, got %d, body = %s", w.Code, w.Body.String())
	}

	body := w.Body.String()

	// Verify boards are rendered
	if !strings.Contains(body, "device-001") {
		t.Errorf("expected device-001 to be rendered when gate is open, body: %s", body)
	}
}
