package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
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
// handleBoards only ever calls ListBoardsWithState; handleBoardDetail
// (#1503) only ever calls GetBoardDetail; handleSensorHistory/
// handleSensorHistoryData (#1504, handlers_sensors_test.go) only ever call
// GetSensorReadingHistory.
type fakeLeafLabAPIClient struct {
	leaflabapipb.LeafLabAPIClient

	resp *leaflabapipb.ListBoardsWithStateResponse
	err  error

	boardDetailResp *leaflabapipb.GetBoardDetailResponse
	boardDetailErr  error

	historyResp *leaflabapipb.GetSensorReadingHistoryResponse
	historyErr  error

	claimResp *leaflabapipb.ClaimBoardResponse
	claimErr  error
}

func (f *fakeLeafLabAPIClient) ListBoardsWithState(ctx context.Context, in *leaflabapipb.ListBoardsWithStateRequest, opts ...grpc.CallOption) (*leaflabapipb.ListBoardsWithStateResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func (f *fakeLeafLabAPIClient) GetBoardDetail(ctx context.Context, in *leaflabapipb.GetBoardDetailRequest, opts ...grpc.CallOption) (*leaflabapipb.GetBoardDetailResponse, error) {
	if f.boardDetailErr != nil {
		return nil, f.boardDetailErr
	}
	return f.boardDetailResp, nil
}

func (f *fakeLeafLabAPIClient) GetSensorReadingHistory(ctx context.Context, in *leaflabapipb.GetSensorReadingHistoryRequest, opts ...grpc.CallOption) (*leaflabapipb.GetSensorReadingHistoryResponse, error) {
	if f.historyErr != nil {
		return nil, f.historyErr
	}
	return f.historyResp, nil
}

func (f *fakeLeafLabAPIClient) ClaimBoard(ctx context.Context, in *leaflabapipb.ClaimBoardRequest, opts ...grpc.CallOption) (*leaflabapipb.ClaimBoardResponse, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	if f.claimResp != nil {
		return f.claimResp, nil
	}
	return &leaflabapipb.ClaimBoardResponse{}, nil
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

// --- handleBoardDetail (#1503: FR6, FR7) ------------------------------------

// newBoardDetailRequest builds a "/boards/{board_id}" request with the
// board_id path value set the way the real "/boards/{board_id}" route
// (main.go's setupRoutes) would populate it.
func newBoardDetailRequest(boardID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/boards/"+boardID, nil)
	req.SetPathValue("board_id", boardID)
	return req
}

// TestHandleBoardDetail_RendersEverySensorFromAPI covers the happy path:
// handleBoardDetail calls GetBoardDetail via app.api (no SQL) and renders
// every sensor the API returns, including the full-length device_id.
func TestHandleBoardDetail_RendersEverySensorFromAPI(t *testing.T) {
	fake := &fakeLeafLabAPIClient{boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
		BoardId:  7,
		DeviceId: "leaflab-ccdba79f5fac",
		Sensors: []*leaflabapipb.SensorDetail{
			{SensorId: 1, SensorName: "Soil Moisture", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
			{SensorId: 2, SensorName: "Air Temp", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_NEVER_REPORTED},
		},
	}}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleBoardDetail(rec, newBoardDetailRequest("7"))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /boards/7 status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"leaflab-ccdba79f5fac", "Soil Moisture", "Air Temp"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the rendered page, got %q", want, body)
		}
	}
}

// TestHandleBoardDetail_MalformedBoardID_NotFound proves a non-numeric
// board_id path segment short-circuits to a real HTTP 404 before any RPC
// is attempted -- a defensive guard, since the real route only ever
// matches whatever segment is in the URL.
func TestHandleBoardDetail_MalformedBoardID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleBoardDetail(rec, newBoardDetailRequest("not-a-number"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandleBoardDetail_UnknownBoardID_NotFound proves a codes.NotFound
// response from leaflab-api (an unknown board_id) maps to a real HTTP 404,
// distinguishable from "board has no sensors" (a 200 with the empty-state
// message) per the issue's Empty and error states section.
func TestHandleBoardDetail_UnknownBoardID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{boardDetailErr: status.Error(codes.NotFound, "board 999 not found")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleBoardDetail(rec, newBoardDetailRequest("999"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandleBoardDetail_ZeroSensors_RendersEmptyMessageOK proves a board
// with zero sensors renders normally (HTTP 200) with the empty-state
// message, not a 404 or an error -- distinct from
// TestHandleBoardDetail_UnknownBoardID_NotFound above.
func TestHandleBoardDetail_ZeroSensors_RendersEmptyMessageOK(t *testing.T) {
	fake := &fakeLeafLabAPIClient{boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
		BoardId:  7,
		DeviceId: "leaflab-ccdba79f5fac",
		Sensors:  nil,
	}}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleBoardDetail(rec, newBoardDetailRequest("7"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "This board has no sensors yet.") {
		t.Errorf("expected the empty-state message, got %q", rec.Body.String())
	}
}

// TestHandleBoardDetail_Unauthenticated_RedirectsToLogin mirrors
// TestHandleBoards_Unauthenticated_RedirectsToLogin for the board detail
// route.
func TestHandleBoardDetail_Unauthenticated_RedirectsToLogin(t *testing.T) {
	fake := &fakeLeafLabAPIClient{boardDetailErr: status.Error(codes.Unauthenticated, "token revoked")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleBoardDetail(rec, newBoardDetailRequest("7"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/auth/login?next=/boards/7"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// TestHandleBoardDetail_GenericError_RendersErrorState proves a non-
// NotFound, non-Unauthenticated gRPC failure still renders the page
// (status 200) with a visible error, not a crash and not a 404.
func TestHandleBoardDetail_GenericError_RendersErrorState(t *testing.T) {
	fake := &fakeLeafLabAPIClient{boardDetailErr: status.Error(codes.Internal, "boom")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleBoardDetail(rec, newBoardDetailRequest("7"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (error rendered on the page, not a hard failure)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Failed to load board detail") {
		t.Errorf("expected the load-error message in the rendered page, got %q", rec.Body.String())
	}
}

// --- handleClaimBoard (#1765: FR1, FR2) -------------------------------------

// newClaimBoardRequest builds a "/boards/{board_id}/claim" POST request with
// the board_id path value set the way the real route (main.go's
// setupRoutes) would populate it.
func newClaimBoardRequest(boardID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/boards/"+boardID+"/claim", nil)
	req.SetPathValue("board_id", boardID)
	return req
}

// TestHandleClaimBoard_Succeeds_RedirectsToBoardDetailNoError covers the
// happy path (FR1): a successful claim redirects back to the board detail
// page with no claim_error query param.
func TestHandleClaimBoard_Succeeds_RedirectsToBoardDetailNoError(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleClaimBoard(rec, newClaimBoardRequest("7"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/boards/7"; got != want {
		t.Errorf("Location = %q, want %q (no claim_error on success)", got, want)
	}
}

// TestHandleClaimBoard_FailedPrecondition_RedirectsWithInlineAlreadyOwnedError
// is Testing criterion 8: a codes.FailedPrecondition from the API (already
// owned) maps to an inline already-owned message carried on the redirect,
// never a 500 and never a silent no-op.
func TestHandleClaimBoard_FailedPrecondition_RedirectsWithInlineAlreadyOwnedError(t *testing.T) {
	fake := &fakeLeafLabAPIClient{claimErr: status.Error(codes.FailedPrecondition, "board 7 is already owned")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleClaimBoard(rec, newClaimBoardRequest("7"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect, not a 500)", rec.Code, http.StatusSeeOther)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/boards/7?claim_error=") {
		t.Fatalf("Location = %q, want a redirect back to /boards/7 carrying claim_error", loc)
	}
	// The purpose-built FailedPrecondition message, not the generic
	// "Failed to claim board: <raw gRPC message>" fallback -- distinguishes
	// this from TestHandleClaimBoard_GenericError_RedirectsWithRawMessage
	// below, which would otherwise pass on the same substring since the
	// fixture's raw error text itself happens to contain "already owned".
	unescaped, err := url.QueryUnescape(loc)
	if err != nil {
		t.Fatalf("unescape Location %q: %v", loc, err)
	}
	if !strings.Contains(unescaped, "This board is already owned.") {
		t.Errorf("expected the purpose-built already-owned message in the claim_error query param, got %q", unescaped)
	}
	if strings.Contains(unescaped, "Failed to claim board:") {
		t.Errorf("expected the FailedPrecondition branch, not the generic fallback message, got %q", unescaped)
	}
}

// TestHandleClaimBoard_FailedPrecondition_HXRequest_SetsHXRedirectHeader
// mirrors the htmx variant of the redirect: an htmx-driven claim POST must
// get an HX-Redirect header carrying the same claim_error, not a plain 3xx.
func TestHandleClaimBoard_FailedPrecondition_HXRequest_SetsHXRedirectHeader(t *testing.T) {
	fake := &fakeLeafLabAPIClient{claimErr: status.Error(codes.FailedPrecondition, "board 7 is already owned")}
	app := &App{api: &LeafLabClient{api: fake}}

	req := newClaimBoardRequest("7")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handleClaimBoard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	redirect := rec.Header().Get("HX-Redirect")
	if !strings.HasPrefix(redirect, "/boards/7?claim_error=") {
		t.Errorf("HX-Redirect = %q, want a redirect back to /boards/7 carrying claim_error", redirect)
	}
}

// TestHandleClaimBoard_UnknownBoardID_NotFound proves a codes.NotFound
// response from leaflab-api (an unknown board_id) maps to a real HTTP 404,
// not the inline claim_error path.
func TestHandleClaimBoard_UnknownBoardID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{claimErr: status.Error(codes.NotFound, "board 999 not found")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleClaimBoard(rec, newClaimBoardRequest("999"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandleClaimBoard_Unauthenticated_RedirectsToLogin mirrors
// TestHandleBoards_Unauthenticated_RedirectsToLogin for the claim route.
func TestHandleClaimBoard_Unauthenticated_RedirectsToLogin(t *testing.T) {
	fake := &fakeLeafLabAPIClient{claimErr: status.Error(codes.Unauthenticated, "token revoked")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleClaimBoard(rec, newClaimBoardRequest("7"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/auth/login?next=/boards/7/claim"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// TestHandleClaimBoard_MalformedBoardID_NotFound proves a non-numeric
// board_id path segment short-circuits to a real HTTP 404 before any RPC
// is attempted.
func TestHandleClaimBoard_MalformedBoardID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleClaimBoard(rec, newClaimBoardRequest("not-a-number"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandleClaimBoard_GenericError_RedirectsWithRawMessage proves a non-
// FailedPrecondition, non-NotFound, non-Unauthenticated failure still
// redirects with a claim_error rather than a 500, carrying the raw gRPC
// message per claimErrorMessage's fallback.
func TestHandleClaimBoard_GenericError_RedirectsWithRawMessage(t *testing.T) {
	fake := &fakeLeafLabAPIClient{claimErr: status.Error(codes.Internal, "boom")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleClaimBoard(rec, newClaimBoardRequest("7"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect, not a 500)", rec.Code, http.StatusSeeOther)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), "/boards/7?claim_error=") {
		t.Errorf("Location = %q, want a redirect back to /boards/7 carrying claim_error", rec.Header().Get("Location"))
	}
}
