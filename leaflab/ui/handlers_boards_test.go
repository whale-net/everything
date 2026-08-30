package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
)

// fakeLeafLabAPIClient is a minimal stand-in for
// leaflabapipb.LeafLabAPIClient (mirrors
// tools/app_registry/ui/handlers_promote_rollback_test.go's
// fakePromotionClient pattern: embed the real interface as a nil zero
// value and override only the method(s) a given handler test exercises).
// handleBoards only ever calls ListBoardsWithState.
type fakeLeafLabAPIClient struct {
	leaflabapipb.LeafLabAPIClient

	resp *leaflabapipb.ListBoardsWithStateResponse
	err  error
}

func (f *fakeLeafLabAPIClient) ListBoardsWithState(ctx context.Context, in *leaflabapipb.ListBoardsWithStateRequest, opts ...grpc.CallOption) (*leaflabapipb.ListBoardsWithStateResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// TestHandleBoards_RendersBoardsFromAPI covers the happy path: handleBoards
// calls ListBoardsWithState via app.api (no SQL) and renders what it
// returns, including the full-length device_id (FR4/FR5's UI half).
func TestHandleBoards_RendersBoardsFromAPI(t *testing.T) {
	fake := &fakeLeafLabAPIClient{resp: &leaflabapipb.ListBoardsWithStateResponse{
		Boards: []*leaflabapipb.BoardWithState{
			{BoardId: 1, DeviceId: "leaflab-ccdba79f5fac", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
		},
	}}
	app := &App{api: &LeafLabClient{api: fake}}

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	rec := httptest.NewRecorder()
	app.handleBoards(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /boards status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "leaflab-ccdba79f5fac") {
		t.Errorf("expected the board's device_id in the rendered page, got %q", body)
	}
}

// TestHandleBoards_Unauthenticated_RedirectsToLogin covers the
// Unauthenticated -> login-redirect path: a codes.Unauthenticated response
// from leaflab-api must route through the same redirect-to-login flow
// Authenticator.WithAccessToken uses on a missing/expired local token, not
// render a raw error (see handlers_boards.go's comment on this branch).
func TestHandleBoards_Unauthenticated_RedirectsToLogin(t *testing.T) {
	fake := &fakeLeafLabAPIClient{err: status.Error(codes.Unauthenticated, "token revoked")}
	app := &App{api: &LeafLabClient{api: fake}}

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	rec := httptest.NewRecorder()
	app.handleBoards(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/auth/login?next=/boards"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// TestHandleBoards_Unauthenticated_HXRequest_SetsHXRedirectHeader covers
// the htmx variant of the same redirect: an htmx-driven request
// (HX-Request: true) must get an HX-Redirect header instead of a plain
// 3xx, matching htmxauth's own convention for redirecting an in-page
// htmx swap.
func TestHandleBoards_Unauthenticated_HXRequest_SetsHXRedirectHeader(t *testing.T) {
	fake := &fakeLeafLabAPIClient{err: status.Error(codes.Unauthenticated, "token revoked")}
	app := &App{api: &LeafLabClient{api: fake}}

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handleBoards(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if got, want := rec.Header().Get("HX-Redirect"), "/auth/login?next=/boards"; got != want {
		t.Errorf("HX-Redirect = %q, want %q", got, want)
	}
}

// TestHandleBoards_GenericError_RendersErrorState covers a non-
// Unauthenticated gRPC failure: the page must still render (status 200)
// with a visible error, not redirect and not crash.
func TestHandleBoards_GenericError_RendersErrorState(t *testing.T) {
	fake := &fakeLeafLabAPIClient{err: status.Error(codes.Internal, "boom")}
	app := &App{api: &LeafLabClient{api: fake}}

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	rec := httptest.NewRecorder()
	app.handleBoards(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (error rendered on the page, not a hard failure)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Failed to load boards") {
		t.Errorf("expected the load-error message in the rendered page, got %q", rec.Body.String())
	}
}
