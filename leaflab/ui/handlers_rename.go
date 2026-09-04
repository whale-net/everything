package main

import (
	"fmt"
	"net/http"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/ui/pages"
)

// handleRenameSensor is the FR4 inline rename form's POST target (#1770),
// routed at "/sensors/{sensor_id}/rename". It calls RenameSensor on
// leaflab-api with the signed-in user's own access token (NFR2, forwarded
// by htmxauth.Authenticator.WithAccessToken in setupRoutes) and re-renders
// only the affected sensor row -- never the whole page -- so the rename
// control's own row.templ doc comment (renderSensorRow below) can hx-swap
// it in place, mirroring manmanv2/ui's renderDeploymentRow pattern.
//
// InvalidArgument (empty/whitespace name), PermissionDenied (non-owner or
// unowned board), NotFound (unknown sensor_id), and FailedPrecondition (the
// one same-board-same-name collision leaflab/DATA.md's "Sensor rename
// uniqueness" section documents) all render an inline message on the row --
// never a 500 and never a silent no-op.
func (app *App) handleRenameSensor(w http.ResponseWriter, r *http.Request) {
	sensorID, parseErr := strconv.ParseInt(r.PathValue("sensor_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	// board_id rides along as a hidden field (board_detail.templ's
	// renameSensorForm) -- the RPC itself only needs sensor_id, but this
	// handler needs board_id to re-fetch and re-render the row afterward,
	// and there is no RPC that maps sensor_id back to board_id on this
	// side (see #1504's sensor_history.templ doc comment for the same
	// existing gap).
	boardID, boardIDErr := strconv.ParseInt(r.FormValue("board_id"), 10, 64)
	if boardIDErr != nil {
		http.NotFound(w, r)
		return
	}

	name := r.FormValue("name")

	var renameErrMsg string
	if _, err := app.api.RenameSensor(r.Context(), sensorID, name); err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unauthenticated {
			// Same re-authenticate flow as handleBoards/handleClaimBoard's
			// identical branch -- see handleBoards' comment for why this is
			// a redirect, not an error page.
			loginURL := fmt.Sprintf("/auth/login?next=%s", r.URL.RequestURI())
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", loginURL)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		app.log().Info("rename sensor refused or failed", "sensor_id", sensorID, "board_id", boardID, "err", err)
		renameErrMsg = renameSensorErrorMessage(err)
	} else {
		app.log().Info("sensor renamed", "sensor_id", sensorID, "board_id", boardID)
	}

	app.renderSensorRow(w, r, boardID, sensorID, renameErrMsg)
}

// renderSensorRow re-fetches the board's detail (the only RPC that returns
// a sensor's current name/type/reading/ownership together) and writes the
// pages.SensorRow fragment for exactly one sensor, with renameErr surfaced
// inline. Every outcome, success or failure, responds from freshly
// observed state -- never an assumed-success render -- matching
// manmanv2/ui's renderDeploymentRow precedent (handlers_deployment_actions.go).
//
// For non-HTMX requests (no HX-Request header -- a no-JS fallback), it
// redirects back to the board detail page instead of returning a bare
// fragment, the same split handleClaimBoard already uses.
func (app *App) renderSensorRow(w http.ResponseWriter, r *http.Request, boardID, sensorID int64, renameErr string) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, fmt.Sprintf("/boards/%d", boardID), http.StatusSeeOther)
		return
	}

	resp, err := app.api.GetBoardDetail(r.Context(), boardID)
	if err != nil {
		app.log().Warn("GetBoardDetail failed while rendering sensor row", "board_id", boardID, "sensor_id", sensorID, "err", err)
		http.Error(w, "Failed to reload sensor", http.StatusInternalServerError)
		return
	}

	var sensor *leaflabapipb.SensorDetail
	for _, s := range resp.GetSensors() {
		if s.GetSensorId() == sensorID {
			sensor = s
			break
		}
	}
	if sensor == nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	if renderErr := pages.SensorRow(resp.GetBoardId(), resp.GetDeviceId(), resp.GetOwnedByCaller(), sensor, renameErr).Render(r.Context(), w); renderErr != nil {
		app.log().Error("failed to render sensor row", "sensor_id", sensorID, "err", renderErr)
	}
}

// renameSensorErrorMessage turns a RenameSensor error into a
// human-readable inline message, mirroring handlers_boards.go's
// claimErrorMessage precedent: a purpose-built message for each expected
// gRPC status the RPC can return, falling back to the raw status text for
// anything else rather than hiding it.
func renameSensorErrorMessage(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return "Failed to rename sensor: " + err.Error()
	}
	switch st.Code() {
	case codes.InvalidArgument:
		return "Sensor name must not be empty."
	case codes.PermissionDenied:
		return "You do not own this board."
	case codes.NotFound:
		return "Sensor not found."
	case codes.FailedPrecondition:
		return "Another sensor on this board already has that name."
	default:
		return "Failed to rename sensor: " + err.Error()
	}
}
