package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/ui/components"
	"github.com/whale-net/everything/leaflab/ui/pages"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// handleBoards is the boards list screen (#1502: FR4, FR5, NFR1). It calls
// ListBoardsWithState on leaflab-api with the signed-in user's own access
// token (NFR2 -- forwarded onto the request context by
// htmxauth.Authenticator.WithAccessToken in setupRoutes) and renders
// exactly what the API returns: every board (FR4), each board's
// API-supplied ReportingState verbatim (FR5), and nothing on a timer
// (NFR1). This handler runs no SQL and touches no board/sensor/
// sensor_reading table itself.
func (app *App) handleBoards(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	resp, err := app.api.ListBoardsWithState(r.Context())
	if err != nil {
		// A codes.Unauthenticated response means the token
		// WithAccessToken already validated locally (session-side) was
		// rejected server-side -- e.g. revoked or expired between
		// WithAccessToken's check and this call. The fix is the same
		// re-authenticate flow WithAccessToken itself takes on a missing/
		// expired local token (libs/go/htmxauth/auth.go), not an error
		// page: mirror that redirect here rather than falling through to
		// the generic loadErr rendering below.
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unauthenticated {
			loginURL := fmt.Sprintf("/auth/login?next=%s", r.URL.RequestURI())
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", loginURL)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		app.log().Warn("ListBoardsWithState failed", "err", err)
	}

	layoutData := components.LayoutData{
		Title: "Boards",
		User:  user,
	}

	// resp.GetBoards() on a nil resp (the err != nil, non-Unauthenticated
	// path above) is safe: generated proto getters nil-check their
	// receiver and return the zero value.
	if renderErr := RenderTempl(w, r, "Boards", pages.Boards(layoutData, resp.GetBoards(), err)); renderErr != nil {
		app.log().Error("failed to render boards page", "err", renderErr)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleBoardDetail is the board detail screen (#1503: FR6, FR7, NFR1;
// #2318 added the recorded-region card and the sensors' Region column),
// routed at "/boards/{board_id}". It calls GetBoardDetail on leaflab-api
// with the signed-in user's own access token (NFR2, forwarded by
// htmxauth.Authenticator.WithAccessToken in setupRoutes) and renders every
// sensor the API returns (FR6) with its own reporting state and, when
// present, its most recent reading including its valid flag (FR7), each
// sensor's currently-placed region and the board's recorded region (FR12),
// the owner-only place/move and recorded-region controls (FR7/FR10), and --
// after a completed recorded-region change via handleSetBoardRegion's
// redirect -- FR11's non-blocking review notice. This handler runs no SQL
// and touches no board/sensor/sensor_reading table itself.
//
// A malformed or unknown board_id gets a real HTTP 404
// (http.NotFound/codes.NotFound below) rather than the "loadErr" banner
// handleBoards uses for a generic failure -- the issue's Empty and error
// states section requires a 404 be distinguishable from "board has no
// sensors", which an error banner on a 200 response would not be.
func (app *App) handleBoardDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	boardID, parseErr := strconv.ParseInt(r.PathValue("board_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	// Set only by handleClaimBoard's post-claim redirect (#1765) -- empty on
	// a normal navigation to this page.
	claimErr := r.URL.Query().Get("claim_error")

	resp, err := app.api.GetBoardDetail(r.Context(), boardID)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.Unauthenticated:
				// Same re-authenticate flow as handleBoards' identical
				// branch -- see its comment for why this is a redirect,
				// not an error page.
				loginURL := fmt.Sprintf("/auth/login?next=%s", r.URL.RequestURI())
				if r.Header.Get("HX-Request") == "true" {
					w.Header().Set("HX-Redirect", loginURL)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, loginURL, http.StatusSeeOther)
				return
			case codes.NotFound:
				http.NotFound(w, r)
				return
			}
		}
		app.log().Warn("GetBoardDetail failed", "board_id", boardID, "err", err)
	}

	layoutData := components.LayoutData{
		Title: "Board Detail",
		User:  user,
	}

	// resp is passed through as-is (nil on the err != nil,
	// non-Unauthenticated/non-NotFound path above) -- pages.BoardDetail's
	// own comment covers why that's safe: every field access goes through a
	// generated proto getter, which nil-checks its receiver.
	//
	// The region forest (pickers' option source) is fetched only when the
	// board loaded and the caller owns it -- the only case in which the
	// page renders any region picker (the controls are owner-gated, the
	// region names themselves come from resp). Best-effort: a forest-load
	// failure leaves the pickers as "No regions yet" hints rather than
	// failing the page (handleRegionDetail's forest-for-pickers precedent).
	var allRegions []*leaflabapipb.RegionTreeNode
	if err == nil && resp.GetOwnedByCaller() {
		forest, forestErr := app.api.GetRegionTree(r.Context(), 0)
		if forestErr != nil {
			app.log().Warn("GetRegionTree (forest for pickers) failed", "board_id", boardID, "err", forestErr)
		}
		allRegions = pages.FlattenRegions(forest.GetRegions())
	}

	// Set only by handleSetBoardRegion's no-JS redirect (#2318) -- the FR11
	// review notice is rebuilt from the fresh detail (the write response
	// the notice would otherwise come from no longer exists by the time the
	// redirected GET runs), empty on a normal navigation. regionErr is the
	// same redirect's carrier for a refused set/change/clear.
	var regionNudge *pages.BoardRegionNudge
	if err == nil && r.URL.Query().Get("region_nudge") == "1" {
		regionNudge = boardRegionNudgeFromDetail(resp)
	}
	regionErr := r.URL.Query().Get("region_error")

	if renderErr := RenderTempl(w, r, "Board Detail", pages.BoardDetail(layoutData, resp, err, claimErr, regionErr, regionNudge, allRegions)); renderErr != nil {
		app.log().Error("failed to render board detail page", "board_id", boardID, "err", renderErr)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleClaimBoard is the Claim button's POST target (#1765: FR1, FR2),
// routed at "/boards/{board_id}/claim". It calls ClaimBoard on leaflab-api
// with the signed-in user's own access token (same NFR2 forwarding as
// every other app.api call, via htmxauth.Authenticator.WithAccessToken in
// setupRoutes) and always redirects back to the board's own detail page --
// there is no dedicated fragment-render endpoint for this button to target
// (see boards.templ's claimForm doc comment), so a normal POST-redirect-GET
// is the mechanism, matching the query-param-carried error pattern already
// used by manmanv2/ui/handlers_sessions.go's handleSessionStart.
//
// A codes.FailedPrecondition (already owned, including a re-claim by the
// current owner) is carried onto the redirect as claim_error and rendered
// inline by handleBoardDetail/pages.BoardDetail -- never a 500, and never a
// silent no-op that leaves the page looking like nothing happened.
func (app *App) handleClaimBoard(w http.ResponseWriter, r *http.Request) {
	boardID, parseErr := strconv.ParseInt(r.PathValue("board_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	redirectTo := fmt.Sprintf("/boards/%d", boardID)

	_, err := app.api.ClaimBoard(r.Context(), boardID)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.Unauthenticated:
				// Same re-authenticate flow as handleBoards/handleBoardDetail's
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
			case codes.NotFound:
				http.NotFound(w, r)
				return
			}
		}
		app.log().Info("claim board refused or failed", "board_id", boardID, "err", err)
		redirectTo += "?claim_error=" + url.QueryEscape(claimErrorMessage(err))
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", redirectTo)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

// claimErrorMessage renders a ClaimBoard error for display on the board
// detail page. codes.FailedPrecondition gets a purpose-built "already
// owned" message (server.go's ClaimBoard doesn't echo the current owner's
// name on the wire, so this can't say who); anything else falls back to
// the raw gRPC status message rather than hiding it, consistent with
// handleBoards'/handleBoardDetail's existing generic-error rendering.
func claimErrorMessage(err error) string {
	if st, ok := status.FromError(err); ok && st.Code() == codes.FailedPrecondition {
		return "This board is already owned."
	}
	return "Failed to claim board: " + err.Error()
}

// handleRenameBoard is FR3's write path, routed at
// "POST /boards/{board_id}/rename" (main.go's setupRoutes). It calls
// RenameBoard on leaflab-api with the signed-in user's own access token
// (NFR2) and re-renders exactly the "#board-header" fragment
// (pages.BoardHeader) via HTMX, matching the renameBoardForm's
// hx-target="#board-header" hx-swap="outerHTML" -- never the full page,
// and never a 500 for the two expected rejection cases:
//
//   - codes.InvalidArgument (empty/whitespace-only name) and
//     codes.PermissionDenied (non-owner or unowned board, FR5 has no admin
//     exception) both render as an inline message inside the fragment, not
//     an error page or a hard failure.
//   - Any other failure (transport/Internal) logs a warning and still
//     re-renders the fragment with a generic inline message -- the rename
//     attempt failed, but the page must not break.
//
// After either outcome, the board's current state is re-fetched via
// GetBoardDetail (not assumed from the request) so the fragment always
// reflects what leaflab-api actually has -- including on a rejected
// rename, where the form must re-show the unchanged current name, not
// whatever the caller typed.
func (app *App) handleRenameBoard(w http.ResponseWriter, r *http.Request) {
	boardID, parseErr := strconv.ParseInt(r.PathValue("board_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")

	var renameErr string
	if _, err := app.api.RenameBoard(r.Context(), boardID, name); err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.Unauthenticated:
				// Same re-authenticate flow as handleBoardDetail's
				// identical branch -- see its comment for why this is a
				// redirect, not an error page.
				loginURL := fmt.Sprintf("/auth/login?next=/boards/%d", boardID)
				if r.Header.Get("HX-Request") == "true" {
					w.Header().Set("HX-Redirect", loginURL)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, loginURL, http.StatusSeeOther)
				return
			case codes.InvalidArgument, codes.PermissionDenied:
				renameErr = st.Message()
			default:
				app.log().Warn("RenameBoard failed", "board_id", boardID, "err", err)
				renameErr = "Failed to rename board."
			}
		} else {
			app.log().Warn("RenameBoard failed", "board_id", boardID, "err", err)
			renameErr = "Failed to rename board."
		}
	}

	resp, err := app.api.GetBoardDetail(r.Context(), boardID)
	if err != nil {
		app.log().Warn("GetBoardDetail failed after rename attempt", "board_id", boardID, "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	if renderErr := pages.BoardHeader(boardID, resp.GetDeviceId(), resp.GetBoardName(), resp.GetOwnedByCaller(), renameErr).Render(r.Context(), w); renderErr != nil {
		app.log().Error("failed to render board header fragment", "board_id", boardID, "err", renderErr)
	}
}

// -- #2318: placement UI (FR7 sensor place/move, FR10/FR11 board recorded
// region) ---------------------------------------------------------------

// handlePlaceSensor is FR7's place/move form's POST target (#2318), routed
// at "POST /sensors/{sensor_id}/place". It calls PlaceSensor on leaflab-api
// with the signed-in user's own access token (NFR2, forwarded by
// htmxauth.Authenticator.WithAccessToken in setupRoutes) and re-renders only
// the affected sensor row -- never the whole page -- so the place/move
// control's hx-target="#sensor-row-N"/hx-swap="outerHTML" can swap it in
// place, the exact flow handleRenameSensor established for this row.
//
// One control serves assign and move: PlaceSensor is the API's only
// placement write, and the write is an ordinary Postgres write (LB2) --
// immediate effect, no device round trip expected or waited on. The re-render
// fetches GetBoardDetail fresh, so the row always shows what leaflab-api
// actually has, never an assumed success.
//
// Rejections render an inline message on the row -- never a 500 and never a
// silent no-op: codes.InvalidArgument (region_id <= 0),
// codes.NotFound (unknown sensor or region), codes.PermissionDenied
// (non-owner or unowned board, NFR2). A tampered form (non-numeric or
// non-positive region_id) is refused client-side with a 400 before the RPC
// -- the same tampered-form treatment parseOptionalRegionID's callers use.
func (app *App) handlePlaceSensor(w http.ResponseWriter, r *http.Request) {
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
	// placeSensorForm) -- the RPC itself only needs sensor_id and region_id,
	// but this handler needs board_id to re-fetch and re-render the row
	// afterward; there is no RPC that maps sensor_id back to board_id on
	// this side (see #1504's sensor_history.templ doc comment for the same
	// existing gap). The row's other context (device id, reading, state)
	// comes from the fresh GetBoardDetail, not this form.
	boardID, boardIDErr := strconv.ParseInt(r.FormValue("board_id"), 10, 64)
	if boardIDErr != nil {
		http.NotFound(w, r)
		return
	}

	// region_id is required and must be a real region id (> 0): PlaceSensor
	// has no unassign case (api.proto), so an empty/zero/non-numeric value
	// is a tampered form, not an API round trip.
	regionID, regionIDErr := strconv.ParseInt(r.FormValue("region_id"), 10, 64)
	if regionIDErr != nil || regionID <= 0 {
		http.Error(w, "invalid region_id", http.StatusBadRequest)
		return
	}

	var placementErrMsg string
	if _, err := app.api.PlaceSensor(r.Context(), sensorID, regionID); err != nil {
		if app.redirectToLoginOnUnauthenticated(w, r, err) {
			return
		}
		app.log().Info("place sensor refused or failed", "sensor_id", sensorID, "board_id", boardID, "region_id", regionID, "err", err)
		placementErrMsg = placeSensorErrorMessage(err)
	} else {
		app.log().Info("sensor placed", "sensor_id", sensorID, "board_id", boardID, "region_id", regionID)
	}

	app.renderSensorRow(w, r, boardID, sensorID, "", placementErrMsg)
}

// handleSetBoardRegion is FR10's set/change/clear form's POST target
// (#2318), routed at "POST /boards/{board_id}/region". It calls
// SetBoardRegion on leaflab-api with the signed-in user's own access token
// (NFR2; the owner-or-admin check is the RPC's
// authorizeBoardWriteWithAdminBypass) and re-renders exactly the
// "#board-region-card" fragment (pages.BoardRegionCard) via HTMX -- the
// picker's hx-target="#board-region-card" hx-swap="outerHTML" -- never the
// whole page.
//
// The response's FR11 nudge snapshot (the board's sensors with their
// current placements, read post-write) is what the re-rendered card's
// non-blocking notice lists -- verbatim from the response, never rebuilt
// from a later read -- and the card's recorded-region display comes from
// the response's post-write fields; a fresh GetBoardDetail supplies only
// the ownership flag and, with a GetRegionTree forest, the pickers'
// options. No handler here ever calls PlaceSensor: FR10 bookkeeping never
// moves a sensor (FR11's nudge is display-only).
//
// A rejection (codes.NotFound unknown board/region, codes.PermissionDenied
// non-owner/unowned, codes.FailedPrecondition no-op refusals like
// recording the already-recorded region or clearing an unrecorded board)
// re-renders the fragment with the API's message inline -- never a 500,
// and never a silent no-op. For non-HTMX requests (no-JS fallback), the
// outcome rides the redirect instead: success lands on
// "/boards/{id}?region_nudge=1" (the notice rebuilt from a fresh
// GetBoardDetail), a failure on "/boards/{id}?region_error=..." -- the
// same query-param-carried mechanism handleClaimBoard's claim_error uses.
func (app *App) handleSetBoardRegion(w http.ResponseWriter, r *http.Request) {
	boardID, parseErr := strconv.ParseInt(r.PathValue("board_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	// Two forms post to this route: the picker form (a required region_id)
	// and the clear form (clear=1, no region_id -- a board's un-recorded
	// state is the absence of an open board_region_history row; there is no
	// region_id = 0 sentinel to send). A non-numeric or non-positive
	// region_id is a tampered form, refused with a 400 before the RPC --
	// the same treatment parseOptionalRegionID's callers give a tampered
	// parent picker.
	var regionID *int64
	if r.FormValue("clear") != "1" {
		parsed, regionIDErr := strconv.ParseInt(r.FormValue("region_id"), 10, 64)
		if regionIDErr != nil || parsed <= 0 {
			http.Error(w, "invalid region_id", http.StatusBadRequest)
			return
		}
		regionID = &parsed
	}

	setResp, err := app.api.SetBoardRegion(r.Context(), boardID, regionID)
	if err != nil {
		if app.redirectToLoginOnUnauthenticated(w, r, err) {
			return
		}
		app.log().Info("set board region refused or failed", "board_id", boardID, "clear", regionID == nil, "err", err)

		if r.Header.Get("HX-Request") == "true" {
			app.renderBoardRegionCard(w, r, boardID, nil, nil, boardRegionErrorMessage(err))
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/boards/%d?region_error=%s", boardID, url.QueryEscape(boardRegionErrorMessage(err))), http.StatusSeeOther)
		return
	}

	app.log().Info("board recorded region changed",
		"board_id", boardID,
		"recorded_region_id", setResp.GetRegionId(),
		"sensors_in_nudge", len(setResp.GetSensors()))

	if r.Header.Get("HX-Request") == "true" {
		// The nudge lists the board's sensors with their current placements
		// verbatim from this write's response (FR11's stated input) -- not
		// re-read from a later GetBoardDetail, which could already differ.
		nudge := &pages.BoardRegionNudge{
			NewRegionName: setResp.GetRegionName(),
			Cleared:       setResp.RegionId == nil,
			Sensors:       setResp.GetSensors(),
		}
		app.renderBoardRegionCard(w, r, boardID, setResp, nudge, "")
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/boards/%d?region_nudge=1", boardID), http.StatusSeeOther)
}

// renderBoardRegionCard re-renders the board detail screen's
// "#board-region-card" fragment (pages.BoardRegionCard) after a
// set/change/clear attempt -- success or failure. Every outcome responds
// from freshly observed state: a fresh GetBoardDetail supplies the
// ownership signal the owner-gated controls render from (and the recorded
// region itself on a failure, where the write was refused), while the
// successful write's own response supplies the post-write recorded region
// and the FR11 nudge snapshot. A GetRegionTree forest fetch (best-effort)
// supplies the pickers' options. Never called for a non-HTMX request --
// the callers redirect instead -- and never a 500 for an expected
// rejection: only a failed post-attempt reload 500s, matching
// renderSensorRow's precedent.
func (app *App) renderBoardRegionCard(w http.ResponseWriter, r *http.Request, boardID int64, setResp *leaflabapipb.SetBoardRegionResponse, nudge *pages.BoardRegionNudge, actionErr string) {
	detail, err := app.api.GetBoardDetail(r.Context(), boardID)
	if err != nil {
		app.log().Warn("GetBoardDetail failed while rendering board region card", "board_id", boardID, "err", err)
		http.Error(w, "Failed to reload board", http.StatusInternalServerError)
		return
	}

	forest, forestErr := app.api.GetRegionTree(r.Context(), 0)
	if forestErr != nil {
		app.log().Warn("GetRegionTree (forest for pickers) failed while rendering board region card", "board_id", boardID, "err", forestErr)
	}
	allRegions := pages.FlattenRegions(forest.GetRegions())

	// Post-write recorded region from the write response when the write
	// succeeded; the freshly re-read board state otherwise (a rejected
	// write must re-show the unchanged recorded region, not whatever the
	// caller attempted).
	recordedRegionID := detail.RecordedRegionId
	recordedRegionName := detail.GetRecordedRegionName()
	if setResp != nil {
		recordedRegionID = setResp.RegionId
		recordedRegionName = setResp.GetRegionName()
	}

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	if renderErr := pages.BoardRegionCard(boardID, detail.GetOwnedByCaller(), recordedRegionID, recordedRegionName, allRegions, nudge, actionErr).Render(r.Context(), w); renderErr != nil {
		app.log().Error("failed to render board region card", "board_id", boardID, "err", renderErr)
	}
}

// boardRegionNudgeFromDetail builds the FR11 review notice from a freshly
// read GetBoardDetail -- the no-JS redirect path, where the
// SetBoardRegionResponse that carried the post-write snapshot no longer
// exists by the time the redirected GET runs. The sensors' placements come
// from the detail response's (equally current) region fields; the notice
// text reflects what the recorded region is now, read from the same
// response.
func boardRegionNudgeFromDetail(resp *leaflabapipb.GetBoardDetailResponse) *pages.BoardRegionNudge {
	sensors := make([]*leaflabapipb.BoardSensorRegion, 0, len(resp.GetSensors()))
	for _, s := range resp.GetSensors() {
		sensors = append(sensors, &leaflabapipb.BoardSensorRegion{
			SensorId:   s.GetSensorId(),
			SensorName: s.GetSensorName(),
			RegionId:   s.RegionId,
			RegionName: s.GetRegionName(),
		})
	}
	return &pages.BoardRegionNudge{
		NewRegionName: resp.GetRecordedRegionName(),
		Cleared:       resp.RecordedRegionId == nil,
		Sensors:       sensors,
	}
}

// placeSensorErrorMessage turns a PlaceSensor error into the message the
// sensor row's Region cell shows inline. codes.PermissionDenied gets a
// purpose-built message -- the raw status can name the caller's OIDC
// subject ("no leaflab_user found for subject ..."), nothing the page's
// reader can act on (the same reasoning as handlers_regions.go's
// regionWriteErrorMessage); every other expected status
// (InvalidArgument/NotFound) surfaces the API's own display-ready message
// verbatim, and anything else (transport/Internal) falls back to the raw
// error text rather than hiding it.
//
// The status is extracted with a direct errors.As against the GRPCStatus()
// *status.Status interface -- NOT status.FromError/Convert:
// LeafLabClient wraps every RPC error with %w, and grpc >=1.60's FromError
// answers a wrapped status with a clone whose message it rewrites to the
// full err.Error() text ("failed to ... sensor N: rpc error: ..."), so the
// API's own message would never surface. errors.As on the same interface
// grpc's FromError uses internally hands back the original, unrewritten
// status instead.
func placeSensorErrorMessage(err error) string {
	var gs interface{ GRPCStatus() *status.Status }
	if !errors.As(err, &gs) {
		return "Failed to place sensor: " + err.Error()
	}
	st := gs.GRPCStatus()
	if st == nil {
		return "Failed to place sensor: " + err.Error()
	}
	switch st.Code() {
	case codes.PermissionDenied:
		return "You do not own this board."
	default:
		return st.Message()
	}
}

// boardRegionErrorMessage turns a SetBoardRegion error into the message the
// board region card shows inline -- the same mapping shape and
// errors.As GRPCStatus() extraction placeSensorErrorMessage uses (see its
// doc comment for why FromError/Convert is avoided). codes.PermissionDenied
// gets a purpose-built message (the raw status can name the caller's OIDC
// subject); the no-op refusals (FailedPrecondition: "board %d is already
// recorded in region %d", "board %d has no recorded region") and the
// NotFound cases surface the API's own messages verbatim -- they are
// display-ready and say exactly what the user needs to change.
func boardRegionErrorMessage(err error) string {
	var gs interface{ GRPCStatus() *status.Status }
	if !errors.As(err, &gs) {
		return "Failed to record board region: " + err.Error()
	}
	st := gs.GRPCStatus()
	if st == nil {
		return "Failed to record board region: " + err.Error()
	}
	switch st.Code() {
	case codes.PermissionDenied:
		return "You do not own this board."
	default:
		return st.Message()
	}
}
