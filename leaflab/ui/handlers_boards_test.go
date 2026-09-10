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

	renameBoardResp *leaflabapipb.RenameBoardResponse
	renameBoardErr  error

	// -- #1777: admin ownership screen (FR11-FR14) fixtures --
	ownedBoardsResp *leaflabapipb.ListOwnedBoardsResponse
	ownedBoardsErr  error

	usersResp *leaflabapipb.ListUsersResponse
	usersErr  error

	reassignResp *leaflabapipb.ReassignBoardOwnerResponse
	reassignErr  error

	clearResp *leaflabapipb.ClearBoardOwnerResponse
	clearErr  error

	renameSensorResp *leaflabapipb.RenameSensorResponse
	renameSensorErr  error

	// -- #2317: region management fixtures --
	// GetRegionTree fixtures are keyed by the request's root_region_id:
	// handleRegionDetail makes two calls (the drilled subtree, then the full
	// forest for the pickers) and a test must be able to answer them
	// differently. A root with neither fixture fails loudly (NotFound) so a
	// handler path the test did not anticipate shows up as a rendered error
	// banner rather than a silently-empty tree. Every call's root_region_id
	// is recorded for call-order assertions.
	regionTreeByRoot    map[int64]*leaflabapipb.GetRegionTreeResponse
	regionTreeErrByRoot map[int64]error
	regionTreeRoots     []int64

	createRegionResp *leaflabapipb.CreateRegionResponse
	createRegionErr  error
	createdRegionReq *leaflabapipb.CreateRegionRequest

	renameRegionResp *leaflabapipb.RenameRegionResponse
	renameRegionErr  error
	renamedRegionReq *leaflabapipb.RenameRegionRequest

	reparentRegionResp  *leaflabapipb.ReparentRegionResponse
	reparentRegionErr   error
	reparentedRegionReq *leaflabapipb.ReparentRegionRequest

	// -- #2318: placement UI fixtures --
	// Both write RPCs record every request they receive: the handler tests
	// assert the exact wire arguments (sensor_id/region_id, board_id, and
	// region_id nil-vs-set for clear) and -- for FR10's "no automatic sensor
	// moves" rule -- that handleSetBoardRegion leaves PlaceSensor's call
	// record empty.
	placeSensorErr     error
	placedSensorReqs   []*leaflabapipb.PlaceSensorRequest
	setBoardRegionResp *leaflabapipb.SetBoardRegionResponse
	setBoardRegionErr  error
	setBoardRegionReqs []*leaflabapipb.SetBoardRegionRequest
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

func (f *fakeLeafLabAPIClient) RenameBoard(ctx context.Context, in *leaflabapipb.RenameBoardRequest, opts ...grpc.CallOption) (*leaflabapipb.RenameBoardResponse, error) {
	if f.renameBoardErr != nil {
		return nil, f.renameBoardErr
	}
	if f.renameBoardResp != nil {
		return f.renameBoardResp, nil
	}
	return &leaflabapipb.RenameBoardResponse{}, nil
}

// -- #1777: admin ownership screen (FR11-FR14) fakes -------------------

func (f *fakeLeafLabAPIClient) ListOwnedBoards(ctx context.Context, in *leaflabapipb.ListOwnedBoardsRequest, opts ...grpc.CallOption) (*leaflabapipb.ListOwnedBoardsResponse, error) {
	if f.ownedBoardsErr != nil {
		return nil, f.ownedBoardsErr
	}
	if f.ownedBoardsResp != nil {
		return f.ownedBoardsResp, nil
	}
	return &leaflabapipb.ListOwnedBoardsResponse{}, nil
}

func (f *fakeLeafLabAPIClient) ListUsers(ctx context.Context, in *leaflabapipb.ListUsersRequest, opts ...grpc.CallOption) (*leaflabapipb.ListUsersResponse, error) {
	if f.usersErr != nil {
		return nil, f.usersErr
	}
	if f.usersResp != nil {
		return f.usersResp, nil
	}
	return &leaflabapipb.ListUsersResponse{}, nil
}

func (f *fakeLeafLabAPIClient) ReassignBoardOwner(ctx context.Context, in *leaflabapipb.ReassignBoardOwnerRequest, opts ...grpc.CallOption) (*leaflabapipb.ReassignBoardOwnerResponse, error) {
	if f.reassignErr != nil {
		return nil, f.reassignErr
	}
	if f.reassignResp != nil {
		return f.reassignResp, nil
	}
	return &leaflabapipb.ReassignBoardOwnerResponse{}, nil
}

func (f *fakeLeafLabAPIClient) ClearBoardOwner(ctx context.Context, in *leaflabapipb.ClearBoardOwnerRequest, opts ...grpc.CallOption) (*leaflabapipb.ClearBoardOwnerResponse, error) {
	if f.clearErr != nil {
		return nil, f.clearErr
	}
	if f.clearResp != nil {
		return f.clearResp, nil
	}
	return &leaflabapipb.ClearBoardOwnerResponse{}, nil
}

func (f *fakeLeafLabAPIClient) RenameSensor(ctx context.Context, in *leaflabapipb.RenameSensorRequest, opts ...grpc.CallOption) (*leaflabapipb.RenameSensorResponse, error) {
	if f.renameSensorErr != nil {
		return nil, f.renameSensorErr
	}
	if f.renameSensorResp != nil {
		return f.renameSensorResp, nil
	}
	return &leaflabapipb.RenameSensorResponse{}, nil
}

// -- #2317: region management fakes -------------------------------------

func (f *fakeLeafLabAPIClient) GetRegionTree(ctx context.Context, in *leaflabapipb.GetRegionTreeRequest, opts ...grpc.CallOption) (*leaflabapipb.GetRegionTreeResponse, error) {
	f.regionTreeRoots = append(f.regionTreeRoots, in.GetRootRegionId())
	if err, ok := f.regionTreeErrByRoot[in.GetRootRegionId()]; ok {
		return nil, err
	}
	if resp, ok := f.regionTreeByRoot[in.GetRootRegionId()]; ok {
		return resp, nil
	}
	return nil, status.Errorf(codes.NotFound, "fakeLeafLabAPIClient: no GetRegionTree fixture for root_region_id %d", in.GetRootRegionId())
}

func (f *fakeLeafLabAPIClient) CreateRegion(ctx context.Context, in *leaflabapipb.CreateRegionRequest, opts ...grpc.CallOption) (*leaflabapipb.CreateRegionResponse, error) {
	f.createdRegionReq = in
	if f.createRegionErr != nil {
		return nil, f.createRegionErr
	}
	if f.createRegionResp != nil {
		return f.createRegionResp, nil
	}
	return &leaflabapipb.CreateRegionResponse{}, nil
}

func (f *fakeLeafLabAPIClient) RenameRegion(ctx context.Context, in *leaflabapipb.RenameRegionRequest, opts ...grpc.CallOption) (*leaflabapipb.RenameRegionResponse, error) {
	f.renamedRegionReq = in
	if f.renameRegionErr != nil {
		return nil, f.renameRegionErr
	}
	if f.renameRegionResp != nil {
		return f.renameRegionResp, nil
	}
	return &leaflabapipb.RenameRegionResponse{}, nil
}

func (f *fakeLeafLabAPIClient) ReparentRegion(ctx context.Context, in *leaflabapipb.ReparentRegionRequest, opts ...grpc.CallOption) (*leaflabapipb.ReparentRegionResponse, error) {
	f.reparentedRegionReq = in
	if f.reparentRegionErr != nil {
		return nil, f.reparentRegionErr
	}
	if f.reparentRegionResp != nil {
		return f.reparentRegionResp, nil
	}
	return &leaflabapipb.ReparentRegionResponse{}, nil
}

// -- #2318: placement UI fakes -------------------------------------------

func (f *fakeLeafLabAPIClient) PlaceSensor(ctx context.Context, in *leaflabapipb.PlaceSensorRequest, opts ...grpc.CallOption) (*leaflabapipb.PlaceSensorResponse, error) {
	f.placedSensorReqs = append(f.placedSensorReqs, in)
	if f.placeSensorErr != nil {
		return nil, f.placeSensorErr
	}
	return &leaflabapipb.PlaceSensorResponse{}, nil
}

func (f *fakeLeafLabAPIClient) SetBoardRegion(ctx context.Context, in *leaflabapipb.SetBoardRegionRequest, opts ...grpc.CallOption) (*leaflabapipb.SetBoardRegionResponse, error) {
	f.setBoardRegionReqs = append(f.setBoardRegionReqs, in)
	if f.setBoardRegionErr != nil {
		return nil, f.setBoardRegionErr
	}
	if f.setBoardRegionResp != nil {
		return f.setBoardRegionResp, nil
	}
	return &leaflabapipb.SetBoardRegionResponse{}, nil
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

// --- handleRenameBoard (#1767: FR3) -----------------------------------------

// newRenameBoardRequest builds a "POST /boards/{board_id}/rename" request
// the way the real route (main.go's setupRoutes) would populate it, with a
// form-encoded "name" body value the way renameBoardForm submits it.
func newRenameBoardRequest(boardID, name string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/boards/"+boardID+"/rename", strings.NewReader("name="+name))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("board_id", boardID)
	return req
}

// TestHandleRenameBoard_Success_RerendersHeaderWithNewName covers the
// happy path: a successful RenameBoard is followed by a GetBoardDetail
// re-fetch, and the header fragment reflects the new name (not whatever
// was posted, since the handler re-fetches rather than assumes).
func TestHandleRenameBoard_Success_RerendersHeaderWithNewName(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", BoardName: "greenhouse", OwnedByCaller: true,
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameBoard(rec, newRenameBoardRequest("7", "greenhouse"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "greenhouse") {
		t.Errorf("expected the new name in the rendered fragment, got %q", rec.Body.String())
	}
}

// TestHandleRenameBoard_InvalidArgument_RendersInlineMessageNot500 is
// Testing criterion 9's InvalidArgument half: an empty-name rejection from
// leaflab-api renders inline in the re-rendered fragment (status 200), not
// a 500.
func TestHandleRenameBoard_InvalidArgument_RendersInlineMessageNot500(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		renameBoardErr: status.Error(codes.InvalidArgument, "name must not be empty"),
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", BoardName: "old-name", OwnedByCaller: true,
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameBoard(rec, newRenameBoardRequest("7", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (inline error, not a 500)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "name must not be empty") {
		t.Errorf("expected the inline validation message, got %q", rec.Body.String())
	}
	// A rejected rename must re-show the unchanged current name, not the
	// caller's rejected input.
	if !strings.Contains(rec.Body.String(), "old-name") {
		t.Errorf("expected the unchanged current name to still be shown, got %q", rec.Body.String())
	}
}

// TestHandleRenameBoard_PermissionDenied_RendersInlineMessageNot500 is
// Testing criterion 9's PermissionDenied half: a non-owner (or unowned-
// board) rejection from leaflab-api renders inline, not a 500.
func TestHandleRenameBoard_PermissionDenied_RendersInlineMessageNot500(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		renameBoardErr: status.Error(codes.PermissionDenied, "caller does not own this board"),
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", BoardName: "old-name", OwnedByCaller: false,
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameBoard(rec, newRenameBoardRequest("7", "someone-elses-board"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (inline error, not a 500)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "caller does not own this board") {
		t.Errorf("expected the inline permission-denied message, got %q", rec.Body.String())
	}
}

// TestHandleRenameBoard_MalformedBoardID_NotFound mirrors
// TestHandleBoardDetail_MalformedBoardID_NotFound: a non-numeric board_id
// path segment short-circuits to a real 404 before any RPC is attempted.
func TestHandleRenameBoard_MalformedBoardID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameBoard(rec, newRenameBoardRequest("not-a-number", "greenhouse"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// --- #2318: placement UI (FR7 sensor place/move, FR10/FR11 board recorded
// region + nudge, FR12 regions on list/detail) ------------------------------

// regionIDPtr is the *int64 the region fixtures need: recorded_region_id /
// region_id are optional proto fields whose nil IS the "no region" signal
// (there is no region_id = 0 sentinel), so tests must pass real pointers.
func regionIDPtr(v int64) *int64 {
	return &v
}

// placeForestFixture is the flattened-option source the pickers render
// from: two top-level regions. The place/move success test asserts both
// names appear (the sensor's new region as its Region cell AND as picker
// options), proving the forest actually fed the picker.
func placeForestFixture() []*leaflabapipb.RegionTreeNode {
	return []*leaflabapipb.RegionTreeNode{
		regionNodeFixture(3, "Shelf C", 0, 0),
		regionNodeFixture(4, "Basement", 0, 0),
	}
}

// newPlaceSensorRequest builds a "POST /sensors/{sensor_id}/place" request
// the way the real route (main.go's setupRoutes) would populate it, with
// form-encoded "board_id"/"region_id" body values the way placeSensorForm
// submits them (board_id rides along as a hidden field -- see
// handlePlaceSensor's doc comment).
func newPlaceSensorRequest(sensorID, boardID, regionID string) *http.Request {
	form := url.Values{}
	form.Set("board_id", boardID)
	form.Set("region_id", regionID)
	req := httptest.NewRequest(http.MethodPost, "/sensors/"+sensorID+"/place", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("sensor_id", sensorID)
	return req
}

// newSetBoardRegionRequest builds a "POST /boards/{board_id}/region"
// request the way boardRegionSetForm (regionID set) or boardRegionClearForm
// (clear == true, no region_id) submits it.
func newSetBoardRegionRequest(boardID, regionID string, clear bool) *http.Request {
	form := url.Values{}
	if clear {
		form.Set("clear", "1")
	} else {
		form.Set("region_id", regionID)
	}
	req := httptest.NewRequest(http.MethodPost, "/boards/"+boardID+"/region", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("board_id", boardID)
	return req
}

// -- FR12: regions render on the boards list and board detail --------------

// TestHandleBoards_RendersRecordedRegionColumn is FR12's list half: each
// board's row shows its current recorded region verbatim from the API, and
// a board with no recorded region shows the em-dash placeholder -- the
// absence of an open board_region_history row, never a sentinel id.
func TestHandleBoards_RendersRecordedRegionColumn(t *testing.T) {
	fake := &fakeLeafLabAPIClient{resp: &leaflabapipb.ListBoardsWithStateResponse{
		Boards: []*leaflabapipb.BoardWithState{
			{BoardId: 1, DeviceId: "leaflab-aaaaaaaaaaaa", RecordedRegionId: regionIDPtr(4), RecordedRegionName: "Basement"},
			{BoardId: 2, DeviceId: "leaflab-bbbbbbbbbbbb"},
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
	if !strings.Contains(body, "<th>Region</th>") {
		t.Errorf("expected the Region column header, got %q", body)
	}
	if !strings.Contains(body, "Basement") {
		t.Errorf("expected the recorded board's region name in its row, got %q", body)
	}
	if !strings.Contains(body, `<span class="text-base-content/40">—</span>`) {
		t.Errorf("expected the em-dash placeholder for the unrecorded board's region cell, got %q", body)
	}
}

// TestHandleBoardDetail_RendersSensorRegionsAndRecordedRegion is FR12's
// detail half: every sensor's Region cell shows its currently-placed region
// (or the em-dash placeholder when unplaced), the board's recorded region
// card shows its region, and -- for a non-owner -- none of the write
// controls render (the place/move picker is owner-gated presentation; the
// enforcement point is the RPCs' owner checks).
func TestHandleBoardDetail_RendersSensorRegionsAndRecordedRegion(t *testing.T) {
	fake := &fakeLeafLabAPIClient{boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
		BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: false,
		RecordedRegionId: regionIDPtr(4), RecordedRegionName: "Basement",
		Sensors: []*leaflabapipb.SensorDetail{
			{SensorId: 10, SensorName: "Soil Moisture", RegionId: regionIDPtr(3), RegionName: "Shelf C",
				ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING,
				LatestReading:  &leaflabapipb.LatestReading{Value: 42.0, Valid: true}},
			{SensorId: 11, SensorName: "Air Temp",
				ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING,
				LatestReading:  &leaflabapipb.LatestReading{Value: 21.5, Valid: true}},
		},
	}}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleBoardDetail(rec, newBoardDetailRequest("7"))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /boards/7 status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<th>Region</th>") {
		t.Errorf("expected the sensors table's Region column header, got %q", body)
	}
	if !strings.Contains(body, "Shelf C") {
		t.Errorf("expected the placed sensor's region name in its Region cell, got %q", body)
	}
	if !strings.Contains(body, "Recorded in") || !strings.Contains(body, "Basement") {
		t.Errorf("expected the recorded-region card to show Basement, got %q", body)
	}
	// Exactly one em-dash placeholder: the unplaced sensor's Region cell.
	// Both sensors have readings (so neither reading cell renders one), and
	// the recorded-region card renders text, not the placeholder.
	if got := strings.Count(body, `<span class="text-base-content/40">—</span>`); got != 1 {
		t.Errorf("expected exactly 1 em-dash placeholder (the unplaced sensor's Region cell), got %d in %q", got, body)
	}
	if strings.Contains(body, `hx-post="/sensors/10/place"`) {
		t.Errorf("expected no place/move picker for a non-owner, got %q", body)
	}
}

// -- FR7: place/move calls the API and re-renders the row -------------------

// TestHandlePlaceSensor_Success_CallsAPIAndRerendersRowWithNewRegion is
// FR7's core handler criterion: the POST calls PlaceSensor with exactly the
// submitted sensor_id/region_id, then re-renders the affected sensor row
// (never the whole page) from a fresh GetBoardDetail -- so the new region
// name comes from what leaflab-api actually has, and the row keeps its
// owner-only picker fed by the region forest.
func TestHandlePlaceSensor_Success_CallsAPIAndRerendersRowWithNewRegion(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: true,
			Sensors: []*leaflabapipb.SensorDetail{
				// The fresh post-write detail: sensor 10 is now placed in
				// region 3, "Shelf C" -- the name the re-render must show.
				{SensorId: 10, SensorName: "Soil Moisture", RegionId: regionIDPtr(3), RegionName: "Shelf C",
					ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING,
					LatestReading:  &leaflabapipb.LatestReading{Value: 42.0, Valid: true}},
			},
		},
		regionTreeByRoot: map[int64]*leaflabapipb.GetRegionTreeResponse{
			0: {Regions: placeForestFixture()},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := newPlaceSensorRequest("10", "7", "3")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handlePlaceSensor(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if len(fake.placedSensorReqs) != 1 {
		t.Fatalf("PlaceSensor calls = %d, want exactly 1", len(fake.placedSensorReqs))
	}
	call := fake.placedSensorReqs[0]
	if call.GetSensorId() != 10 || call.GetRegionId() != 3 {
		t.Errorf("PlaceSensor called with sensor_id=%d region_id=%d, want sensor_id=10 region_id=3", call.GetSensorId(), call.GetRegionId())
	}
	if !strings.Contains(body, "Shelf C") {
		t.Errorf("expected the re-rendered row to show the new region Shelf C, got %q", body)
	}
	// The fragment targets the sensor row (hx-post from the re-rendered
	// picker), never the whole page -- and the forest fed its options.
	if !strings.Contains(body, `hx-post="/sensors/10/place"`) {
		t.Errorf("expected the re-rendered row's picker (hx-post to /sensors/10/place), got %q", body)
	}
	if !strings.Contains(body, "Basement") {
		t.Errorf("expected the other region option (Basement) from the forest in the picker, got %q", body)
	}
}

// TestHandlePlaceSensor_PermissionDenied_RendersInlineMessageNot500 covers
// a non-owner rejection: an inline message on the re-rendered row (status
// 200), never a 500, and the row re-shows the unchanged placement from the
// fresh detail.
func TestHandlePlaceSensor_PermissionDenied_RendersInlineMessageNot500(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		placeSensorErr: status.Error(codes.PermissionDenied, "no leaflab_user found for subject oidc|abc123"),
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: false,
			Sensors: []*leaflabapipb.SensorDetail{
				{SensorId: 10, SensorName: "Soil Moisture", RegionId: regionIDPtr(2), RegionName: "Shelf B",
					ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
			},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := newPlaceSensorRequest("10", "7", "3")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handlePlaceSensor(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (inline error, not a 500); body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "You do not own this board.") {
		t.Errorf("expected the purpose-built PermissionDenied message (not the raw subject-bearing status), got %q", body)
	}
	if strings.Contains(body, "no leaflab_user found for subject") {
		t.Errorf("expected the raw OIDC-subject status text to be hidden, got %q", body)
	}
	if !strings.Contains(body, "Shelf B") {
		t.Errorf("expected the unchanged current placement to still be shown, got %q", body)
	}
}

// TestHandlePlaceSensor_TamperedRegionID_BadRequest_NoRPCCall proves a
// non-numeric or non-positive region_id (a tampered form, since the real
// form's picker always submits a real region id) is refused with a 400
// before any RPC is attempted.
func TestHandlePlaceSensor_TamperedRegionID_BadRequest_NoRPCCall(t *testing.T) {
	for _, tampered := range []string{"", "0", "-1", "abc"} {
		fake := &fakeLeafLabAPIClient{}
		app := &App{api: &LeafLabClient{api: fake}}

		req := newPlaceSensorRequest("10", "7", tampered)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		app.handlePlaceSensor(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("region_id %q: status = %d, want %d; body: %s", tampered, rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		if len(fake.placedSensorReqs) != 0 {
			t.Errorf("region_id %q: PlaceSensor called %d time(s), want 0 (refused before the RPC)", tampered, len(fake.placedSensorReqs))
		}
	}
}

// TestHandlePlaceSensor_NonHXRequest_RedirectsToBoardDetail mirrors
// TestHandleRenameSensor_NonHXRequest_RedirectsToBoardDetail: a no-JS POST
// redirects back to the board detail page instead of returning a bare row
// fragment.
func TestHandlePlaceSensor_NonHXRequest_RedirectsToBoardDetail(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: true,
			Sensors: []*leaflabapipb.SensorDetail{
				{SensorId: 10, SensorName: "Soil Moisture", RegionId: regionIDPtr(3), RegionName: "Shelf C"},
			},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handlePlaceSensor(rec, newPlaceSensorRequest("10", "7", "3"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/boards/7"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// TestHandlePlaceSensor_Unauthenticated_RedirectsToLogin mirrors
// TestHandleClaimBoard_Unauthenticated_RedirectsToLogin for the place route:
// a codes.Unauthenticated response routes to the re-authenticate flow, not
// an inline error. The shared redirectToLoginOnUnauthenticated escapes the
// next value, so the assertion pins the escaped form.
func TestHandlePlaceSensor_Unauthenticated_RedirectsToLogin(t *testing.T) {
	fake := &fakeLeafLabAPIClient{placeSensorErr: status.Error(codes.Unauthenticated, "token revoked")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handlePlaceSensor(rec, newPlaceSensorRequest("10", "7", "3"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/auth/login?next=%2Fsensors%2F10%2Fplace"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// -- FR10/FR11: board recorded region set/change/clear + non-blocking nudge --

// setBoardRegionNudgeSensorsFixture is the FR11 snapshot SetBoardRegion
// returns post-write: two sensors, one placed, one unplaced -- so the nudge
// test can prove both placements render in the notice.
func setBoardRegionNudgeSensorsFixture() []*leaflabapipb.BoardSensorRegion {
	return []*leaflabapipb.BoardSensorRegion{
		{SensorId: 10, SensorName: "Soil Moisture", RegionId: regionIDPtr(3), RegionName: "Shelf C"},
		{SensorId: 11, SensorName: "Air Temp"},
	}
}

// TestHandleSetBoardRegion_Success_RerendersCardWithNudge is FR10/FR11's
// core handler criterion: the POST calls SetBoardRegion with exactly the
// submitted board_id/region_id, and the re-rendered "#board-region-card"
// fragment shows the post-write recorded region AND the non-blocking nudge
// listing the board's sensors with their current placements -- verbatim
// from the write response's snapshot (one placed in Shelf C, one
// unplaced), never rebuilt from a later read.
func TestHandleSetBoardRegion_Success_RerendersCardWithNudge(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		setBoardRegionResp: &leaflabapipb.SetBoardRegionResponse{
			BoardId: 7, RegionId: regionIDPtr(4), RegionName: "Basement",
			Sensors: setBoardRegionNudgeSensorsFixture(),
		},
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: true,
		},
		regionTreeByRoot: map[int64]*leaflabapipb.GetRegionTreeResponse{
			0: {Regions: placeForestFixture()},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := newSetBoardRegionRequest("7", "4", false)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handleSetBoardRegion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(fake.setBoardRegionReqs) != 1 {
		t.Fatalf("SetBoardRegion calls = %d, want exactly 1", len(fake.setBoardRegionReqs))
	}
	call := fake.setBoardRegionReqs[0]
	if call.GetBoardId() != 7 || call.GetRegionId() != 4 {
		t.Errorf("SetBoardRegion called with board_id=%d region_id=%d, want board_id=7 region_id=4", call.GetBoardId(), call.GetRegionId())
	}
	body := rec.Body.String()
	// The card fragment only (this is the hx-target="#board-region-card"
	// swap), showing the post-write recorded region...
	if !strings.Contains(body, `<div id="board-region-card">`) {
		t.Errorf("expected the re-rendered board-region-card fragment, got %q", body)
	}
	if !strings.Contains(body, "Recorded in") || !strings.Contains(body, "Basement") {
		t.Errorf("expected the card to show the post-write recorded region Basement, got %q", body)
	}
	// ...and the non-blocking nudge listing the board's sensors verbatim
	// from the response snapshot.
	if !strings.Contains(body, `role="status"`) {
		t.Errorf("expected the nudge to be a non-blocking role=status notice, got %q", body)
	}
	if !strings.Contains(body, "Recorded region changed to") || !strings.Contains(body, "Basement") {
		t.Errorf("expected the nudge to announce the new recorded region, got %q", body)
	}
	if !strings.Contains(body, "Review whether these sensors' placements should also move") {
		t.Errorf("expected the nudge's review prompt, got %q", body)
	}
	if !strings.Contains(body, "Soil Moisture") || !strings.Contains(body, "Shelf C") {
		t.Errorf("expected the placed sensor and its current region in the nudge, got %q", body)
	}
	if !strings.Contains(body, "Air Temp") || !strings.Contains(body, "unplaced") {
		t.Errorf("expected the unplaced sensor to appear in the nudge marked unplaced, got %q", body)
	}
}

// TestHandleSetBoardRegion_Clear_RendersClearedNudgeNoCardForms covers
// FR10's clear half: clear=1 posts with no region_id (the un-recorded state
// is the absence of an open history row, not a sentinel id), the response's
// unset region_id renders "No recorded region." on the card, the nudge
// announces the clear with the sensors still listed, and the clear control
// itself disappears (nothing left to clear).
func TestHandleSetBoardRegion_Clear_RendersClearedNudgeNoCardForms(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		setBoardRegionResp: &leaflabapipb.SetBoardRegionResponse{
			BoardId: 7,
			Sensors: setBoardRegionNudgeSensorsFixture(),
		},
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: true,
		},
		regionTreeByRoot: map[int64]*leaflabapipb.GetRegionTreeResponse{
			0: {Regions: placeForestFixture()},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := newSetBoardRegionRequest("7", "", true)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handleSetBoardRegion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(fake.setBoardRegionReqs) != 1 {
		t.Fatalf("SetBoardRegion calls = %d, want exactly 1", len(fake.setBoardRegionReqs))
	}
	call := fake.setBoardRegionReqs[0]
	if call.RegionId != nil {
		t.Errorf("clear must post an unset region_id (no region_id = 0 sentinel), got %d", call.GetRegionId())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No recorded region.") {
		t.Errorf("expected the card to show the cleared state, got %q", body)
	}
	if !strings.Contains(body, "Recorded region cleared.") {
		t.Errorf("expected the nudge to announce the clear, got %q", body)
	}
	if !strings.Contains(body, "Soil Moisture") || !strings.Contains(body, "Air Temp") {
		t.Errorf("expected the nudge to still list the board's sensors after a clear, got %q", body)
	}
	if strings.Contains(body, `name="clear"`) {
		t.Errorf("expected the clear control to disappear once nothing is recorded, got %q", body)
	}
}

// TestHandleSetBoardRegion_PermissionDenied_RendersInlineMessageNot500
// covers a non-owner rejection: an inline message inside the re-rendered
// card (status 200), never a 500, no nudge (the write never happened), and
// the card re-shows the unchanged recorded region from the fresh detail.
func TestHandleSetBoardRegion_PermissionDenied_RendersInlineMessageNot500(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		setBoardRegionErr: status.Error(codes.PermissionDenied, "no leaflab_user found for subject oidc|abc123"),
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: false,
			RecordedRegionId: regionIDPtr(2), RecordedRegionName: "Shelf B",
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := newSetBoardRegionRequest("7", "4", false)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handleSetBoardRegion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (inline error, not a 500); body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "You do not own this board.") {
		t.Errorf("expected the purpose-built PermissionDenied message (not the raw subject-bearing status), got %q", body)
	}
	if strings.Contains(body, "no leaflab_user found for subject") {
		t.Errorf("expected the raw OIDC-subject status text to be hidden, got %q", body)
	}
	if strings.Contains(body, "Recorded region changed to") {
		t.Errorf("expected no nudge for a refused write, got %q", body)
	}
	if !strings.Contains(body, "Shelf B") {
		t.Errorf("expected the unchanged recorded region to still be shown, got %q", body)
	}
	if strings.Contains(body, `hx-post="/boards/7/region"`) {
		t.Errorf("expected no region controls for a non-owner, got %q", body)
	}
}

// TestHandleSetBoardRegion_FailedPrecondition_RendersAPIMessageVerbatim
// covers a no-op refusal (recording the already-recorded region): the API's
// own display-ready message surfaces verbatim inline, not a 500 and not a
// rewritten generic. The wrapper-prefix assertion also pins the deliberate
// errors.As(GRPCStatus) extraction: grpc >=1.60's status.FromError answers
// a wrapped status with a clone whose message it rewrites to the full
// err.Error() text ("failed to set recorded region for board 7: rpc error:
// ..."), so FromError here would render that wrapper -- see
// boardRegionErrorMessage's doc comment.
func TestHandleSetBoardRegion_FailedPrecondition_RendersAPIMessageVerbatim(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		setBoardRegionErr: status.Error(codes.FailedPrecondition, "board 7 is already recorded in region 4"),
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: true,
			RecordedRegionId: regionIDPtr(4), RecordedRegionName: "Basement",
		},
		regionTreeByRoot: map[int64]*leaflabapipb.GetRegionTreeResponse{
			0: {Regions: placeForestFixture()},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := newSetBoardRegionRequest("7", "4", false)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handleSetBoardRegion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (inline error, not a 500); body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "board 7 is already recorded in region 4") {
		t.Errorf("expected the API's verbatim no-op message, got %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "failed to set recorded region") {
		t.Errorf("expected the API's message alone, not the client-side-wrapped err.Error() text -- the errors.As GRPCStatus extraction must hand back the original, unrewritten status, got %q", rec.Body.String())
	}
}

// TestHandleSetBoardRegion_NoJS_Success_RedirectsWithNudgeParam covers the
// no-JS fallback of the success path: a plain POST redirects back to the
// board detail page carrying region_nudge=1, which the redirected GET
// (TestHandleBoardDetail_RegionNudgeParam_RendersNudgeFromFreshDetail)
// turns into the nudge.
func TestHandleSetBoardRegion_NoJS_Success_RedirectsWithNudgeParam(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		setBoardRegionResp: &leaflabapipb.SetBoardRegionResponse{
			BoardId: 7, RegionId: regionIDPtr(4), RegionName: "Basement",
			Sensors: setBoardRegionNudgeSensorsFixture(),
		},
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: true,
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleSetBoardRegion(rec, newSetBoardRegionRequest("7", "4", false))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/boards/7?region_nudge=1"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// TestHandleBoardDetail_RegionNudgeParam_RendersNudgeFromFreshDetail covers
// the landing half of the no-JS flow: GET /boards/{id}?region_nudge=1
// rebuilds the nudge from the fresh GetBoardDetail -- the recorded region
// and every sensor's current placement -- while a normal navigation to the
// same page renders no nudge at all.
func TestHandleBoardDetail_RegionNudgeParam_RendersNudgeFromFreshDetail(t *testing.T) {
	detail := func() *leaflabapipb.GetBoardDetailResponse {
		return &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: false,
			RecordedRegionId: regionIDPtr(4), RecordedRegionName: "Basement",
			Sensors: []*leaflabapipb.SensorDetail{
				{SensorId: 10, SensorName: "Soil Moisture", RegionId: regionIDPtr(3), RegionName: "Shelf C",
					ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING,
					LatestReading:  &leaflabapipb.LatestReading{Value: 42.0, Valid: true}},
				{SensorId: 11, SensorName: "Air Temp",
					ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING,
					LatestReading:  &leaflabapipb.LatestReading{Value: 21.5, Valid: true}},
			},
		}
	}

	t.Run("region_nudge=1 renders the nudge", func(t *testing.T) {
		fake := &fakeLeafLabAPIClient{boardDetailResp: detail()}
		app := &App{api: &LeafLabClient{api: fake}}

		req := httptest.NewRequest(http.MethodGet, "/boards/7?region_nudge=1", nil)
		req.SetPathValue("board_id", "7")
		rec := httptest.NewRecorder()
		app.handleBoardDetail(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `role="status"`) || !strings.Contains(body, "Recorded region changed to") {
			t.Errorf("expected the rebuilt nudge notice, got %q", body)
		}
		for _, want := range []string{"Basement", "Soil Moisture", "Shelf C", "Air Temp", "unplaced"} {
			if !strings.Contains(body, want) {
				t.Errorf("expected %q in the rebuilt nudge's sensor list, got %q", want, body)
			}
		}
	})

	t.Run("no param renders no nudge", func(t *testing.T) {
		fake := &fakeLeafLabAPIClient{boardDetailResp: detail()}
		app := &App{api: &LeafLabClient{api: fake}}

		rec := httptest.NewRecorder()
		app.handleBoardDetail(rec, newBoardDetailRequest("7"))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "Recorded region changed to") {
			t.Errorf("expected no nudge on a normal navigation, got %q", rec.Body.String())
		}
	})
}

// TestHandleSetBoardRegion_NeverMovesSensors is FR10's "no automatic sensor
// moves" rule, checked at the UI handler level: set, clear, and a refused
// set all leave PlaceSensor's call record empty -- the nudge is display-
// only, and the only placement write in the handler tree is the one
// handlePlaceSensor makes. Every scenario runs with a non-empty sensor
// snapshot (the board HAS sensors to move), so a regression that bulk-moved
// the listed sensors would be caught, not silently pass on an empty board.
func TestHandleSetBoardRegion_NeverMovesSensors(t *testing.T) {
	scenarios := []struct {
		name string
		req  func() *http.Request
	}{
		{"successful set", func() *http.Request { return newSetBoardRegionRequest("7", "4", false) }},
		{"successful clear", func() *http.Request { return newSetBoardRegionRequest("7", "", true) }},
		{"refused set", func() *http.Request { return newSetBoardRegionRequest("7", "4", false) }},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			fake := &fakeLeafLabAPIClient{
				setBoardRegionResp: &leaflabapipb.SetBoardRegionResponse{
					BoardId: 7, RegionId: regionIDPtr(4), RegionName: "Basement",
					Sensors: setBoardRegionNudgeSensorsFixture(),
				},
				setBoardRegionErr: nil,
				boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
					BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: true,
				},
				regionTreeByRoot: map[int64]*leaflabapipb.GetRegionTreeResponse{
					0: {Regions: placeForestFixture()},
				},
			}
			if scenario.name == "refused set" {
				fake.setBoardRegionResp = nil
				fake.setBoardRegionErr = status.Error(codes.PermissionDenied, "caller does not own this board")
			}
			app := &App{api: &LeafLabClient{api: fake}}

			req := scenario.req()
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			app.handleSetBoardRegion(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
			}
			if len(fake.placedSensorReqs) != 0 {
				t.Errorf("PlaceSensor called %d time(s) during SetBoardRegion handling, want 0 -- changing a board's recorded region must never move sensors", len(fake.placedSensorReqs))
			}
		})
	}
}
