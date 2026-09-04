package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
)

// --- handleRenameSensor (#1770: FR4) ----------------------------------------

// newRenameSensorRequest builds a "POST /sensors/{sensor_id}/rename" request
// the way the real route (main.go's setupRoutes) would populate it, with
// form-encoded "board_id"/"name" body values the way renameSensorForm
// submits them (board_id rides along as a hidden field -- see
// handlers_rename.go's handleRenameSensor doc comment).
func newRenameSensorRequest(sensorID, boardID, name string) *http.Request {
	form := url.Values{}
	form.Set("board_id", boardID)
	form.Set("name", name)
	req := httptest.NewRequest(http.MethodPost, "/sensors/"+sensorID+"/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("sensor_id", sensorID)
	return req
}

// TestHandleRenameSensor_Success_RerendersRowWithNewName is Testing
// criterion 14: a successful RenameSensor is followed by a GetBoardDetail
// re-fetch, and the re-rendered row fragment reflects the new name (from
// the fresh fetch, not the posted form value, mirroring
// TestHandleRenameBoard_Success_RerendersHeaderWithNewName's precedent).
func TestHandleRenameSensor_Success_RerendersRowWithNewName(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: true,
			Sensors: []*leaflabapipb.SensorDetail{
				{SensorId: 10, SensorName: "soil-moisture-2", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
			},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := newRenameSensorRequest("10", "7", "soil-moisture-2")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handleRenameSensor(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "soil-moisture-2") {
		t.Errorf("expected the new name in the rendered fragment, got %q", rec.Body.String())
	}
}

// TestHandleRenameSensor_InvalidArgument_RendersInlineMessageNot500 is
// Testing criterion 5's UI half: an empty-name rejection from leaflab-api
// renders inline in the re-rendered fragment (status 200), not a 500.
func TestHandleRenameSensor_InvalidArgument_RendersInlineMessageNot500(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		renameSensorErr: status.Error(codes.InvalidArgument, "name must not be empty"),
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: true,
			Sensors: []*leaflabapipb.SensorDetail{
				{SensorId: 10, SensorName: "old-name", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
			},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := newRenameSensorRequest("10", "7", "")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handleRenameSensor(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (inline error, not a 500)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Sensor name must not be empty.") {
		t.Errorf("expected the inline validation message, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "old-name") {
		t.Errorf("expected the unchanged current name to still be shown, got %q", rec.Body.String())
	}
}

// TestHandleRenameSensor_PermissionDenied_RendersInlineMessageNot500 is
// Testing criterion 2/3's UI half: a non-owner (or unowned-board) rejection
// from leaflab-api renders inline, not a 500.
func TestHandleRenameSensor_PermissionDenied_RendersInlineMessageNot500(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		renameSensorErr: status.Error(codes.PermissionDenied, "caller does not own this board"),
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: false,
			Sensors: []*leaflabapipb.SensorDetail{
				{SensorId: 10, SensorName: "old-name", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
			},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := newRenameSensorRequest("10", "7", "someone-elses-sensor")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handleRenameSensor(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (inline error, not a 500)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "You do not own this board.") {
		t.Errorf("expected the inline permission-denied message, got %q", rec.Body.String())
	}
}

// TestHandleRenameSensor_FailedPrecondition_RendersInlineMessageNot500
// covers the same-board-same-name collision (repository.ErrSensorNameConflict,
// server.go's codes.FailedPrecondition mapping): it renders inline, not a
// 500.
func TestHandleRenameSensor_FailedPrecondition_RendersInlineMessageNot500(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		renameSensorErr: status.Error(codes.FailedPrecondition, `sensor name "taken" is already used by another sensor on this board`),
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: true,
			Sensors: []*leaflabapipb.SensorDetail{
				{SensorId: 10, SensorName: "old-name", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
			},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := newRenameSensorRequest("10", "7", "taken")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handleRenameSensor(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (inline error, not a 500)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Another sensor on this board already has that name.") {
		t.Errorf("expected the inline name-conflict message, got %q", rec.Body.String())
	}
}

// TestHandleRenameSensor_NotFound_RendersInlineMessageNot500 covers an
// unknown sensor_id rejection from leaflab-api: it renders inline, not a
// 500 (the sensor row itself is still found via boardDetailResp's fixture,
// since the RPC-level NotFound is independent from the row this handler is
// re-rendering).
func TestHandleRenameSensor_NotFound_RendersInlineMessageNot500(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		renameSensorErr: status.Error(codes.NotFound, "sensor 10 not found"),
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: true,
			Sensors: []*leaflabapipb.SensorDetail{
				{SensorId: 10, SensorName: "old-name", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
			},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := newRenameSensorRequest("10", "7", "new-name")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.handleRenameSensor(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (inline error, not a 500)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Sensor not found.") {
		t.Errorf("expected the inline not-found message, got %q", rec.Body.String())
	}
}

// TestHandleRenameSensor_MalformedSensorID_NotFound proves a non-numeric
// sensor_id path segment short-circuits to a real 404 before any RPC is
// attempted.
func TestHandleRenameSensor_MalformedSensorID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameSensor(rec, newRenameSensorRequest("not-a-number", "7", "new-name"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandleRenameSensor_MalformedBoardID_NotFound proves a non-numeric
// board_id form field short-circuits to a real 404, mirroring the
// sensor_id case above -- the handler needs board_id to re-render the row
// afterward (see handlers_rename.go's doc comment).
func TestHandleRenameSensor_MalformedBoardID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameSensor(rec, newRenameSensorRequest("10", "not-a-number", "new-name"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandleRenameSensor_Unauthenticated_RedirectsToLogin mirrors
// TestHandleClaimBoard_Unauthenticated_RedirectsToLogin for the per-sensor
// rename route.
func TestHandleRenameSensor_Unauthenticated_RedirectsToLogin(t *testing.T) {
	fake := &fakeLeafLabAPIClient{renameSensorErr: status.Error(codes.Unauthenticated, "token revoked")}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameSensor(rec, newRenameSensorRequest("10", "7", "new-name"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/auth/login?next=/sensors/10/rename"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// TestHandleRenameSensor_NonHXRequest_RedirectsToBoardDetail proves a
// non-htmx POST (no HX-Request header -- renderSensorRow's no-JS fallback)
// redirects back to the board detail page instead of returning a bare row
// fragment.
func TestHandleRenameSensor_NonHXRequest_RedirectsToBoardDetail(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		boardDetailResp: &leaflabapipb.GetBoardDetailResponse{
			BoardId: 7, DeviceId: "leaflab-aaaaaaaaaaaa", OwnedByCaller: true,
			Sensors: []*leaflabapipb.SensorDetail{
				{SensorId: 10, SensorName: "soil-moisture-2", ReportingState: leaflabapipb.ReportingState_REPORTING_STATE_REPORTING},
			},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameSensor(rec, newRenameSensorRequest("10", "7", "soil-moisture-2"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/boards/7"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}
