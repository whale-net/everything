package main

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/leaflab/ui/components"
	"github.com/whale-net/everything/leaflab/ui/pages"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// handlers_regions.go is #2317's region management UI (FR1, FR2, FR3, FR5,
// FR6): the tree view with drill-down (handleRegions/handleRegionDetail)
// and the three write paths' POST targets (handleCreateRegion/
// handleRenameRegion/handleReparentRegion). Every leaflab-api call carries
// the signed-in user's own access token (NFR2, forwarded by
// htmxauth.Authenticator.WithAccessToken in setupRoutes); this handler set
// runs no SQL and touches no region/sensor placement table itself, and it
// makes no local ownership determination -- RegionTreeNode carries no
// ownership signal, so the forms render for every region and the backing
// RPCs' server-side authorization (NFR2) is the only real fence.
//
// The write handlers use plain-form POST-redirect-GET with the failure
// message carried back as a ?error= query parameter -- the same mechanism
// handleClaimBoard's claim_error uses (there is no fragment-render endpoint
// to hx-post into, and the screen is deliberately server-rendered with no
// live refresh, NFR1). After every attempt, success or failure, the
// redirect target re-fetches the tree fresh, so the view always reflects
// what leaflab-api actually has.

// handleRegions is the top-level regions screen (#2317: FR6), routed at
// "/regions". It calls GetRegionTree with root_region_id = 0 -- the whole
// forest, every top-level region with its descendants, siblings
// alphabetical (FR6) -- and renders exactly what the API returns: nested
// regions, both per-region sensor counts (here only / here or below,
// current placement), nothing recomputed locally. A failed load renders the
// loadErr banner instead of the tree (boards.templ's shape); the create
// form below it still works, since top-level creation needs no tree data.
func (app *App) handleRegions(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	resp, err := app.api.GetRegionTree(r.Context(), 0)
	if err != nil {
		if app.redirectToLoginOnUnauthenticated(w, r, err) {
			return
		}
		app.log().Warn("GetRegionTree failed", "err", err)
	}

	// resp.GetRegions() on a nil resp (the err != nil path above) is safe:
	// generated proto getters nil-check their receiver and return the zero
	// value. On success, the rendered forest doubles as the pickers' option
	// source -- the top-level view already contains every region.
	layoutData := components.LayoutData{
		Title: "Regions",
		User:  user,
	}
	if renderErr := RenderTempl(w, r, "Regions", pages.Regions(
		layoutData,
		resp.GetRegions(),
		0,
		pages.FlattenRegions(resp.GetRegions()),
		r.URL.Query().Get("error"),
		err,
	)); renderErr != nil {
		app.log().Error("failed to render regions page", "err", renderErr)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleRegionDetail is the drill-down view of the regions screen (#2317:
// FR6), routed at "/regions/{region_id}". It calls GetRegionTree with that
// region as the drill root -- the API returns the region's subtree as the
// response's single top element -- and renders it the same way the
// top-level view does. A malformed or unknown region_id gets a real HTTP
// 404 (the same distinguishable-from-empty treatment handleBoardDetail
// uses), not an error banner on a 200.
//
// The create/re-parent pickers need every region, not just the drilled
// subtree -- a re-parent target may live entirely outside the current view
// -- so a second GetRegionTree(0) fetches the full forest for
// pages.FlattenRegions. Best-effort: a forest-load failure leaves the
// pickers with only the Top-level option rather than failing the page,
// since the tree display itself (the primary content) succeeded.
func (app *App) handleRegionDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	regionID, parseErr := strconv.ParseInt(r.PathValue("region_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	resp, err := app.api.GetRegionTree(r.Context(), regionID)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.Unauthenticated:
				// Same re-authenticate flow as handleBoards' identical
				// branch -- see its comment for why this is a redirect,
				// not an error page.
				if app.redirectToLoginOnUnauthenticated(w, r, err) {
					return
				}
			case codes.NotFound:
				http.NotFound(w, r)
				return
			}
		}
		app.log().Warn("GetRegionTree failed", "root_region_id", regionID, "err", err)
	}

	forest, forestErr := app.api.GetRegionTree(r.Context(), 0)
	if forestErr != nil {
		app.log().Warn("GetRegionTree (forest for pickers) failed", "err", forestErr)
	}

	layoutData := components.LayoutData{
		Title: "Region Detail",
		User:  user,
	}
	if renderErr := RenderTempl(w, r, "Region Detail", pages.Regions(
		layoutData,
		resp.GetRegions(),
		regionID,
		pages.FlattenRegions(forest.GetRegions()),
		r.URL.Query().Get("error"),
		err,
	)); renderErr != nil {
		app.log().Error("failed to render region detail page", "region_id", regionID, "err", renderErr)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleCreateRegion is the create form's POST target (#2317: FR1), routed
// at "POST /regions/create". It calls CreateRegion on leaflab-api with the
// signed-in user's own access token (NFR2) -- the created region is owned
// by the caller from the moment it exists, server-side (FR1, NFR2) -- and
// redirects back to the page the form was submitted from (the form's
// regionsNextPath "next" field), where the fresh tree shows the new region.
//
// A rejection (codes.InvalidArgument for an empty/over-long name,
// codes.NotFound for an unknown parent) redirects back with ?error=
// carrying the API's own message (regionWriteErrorMessage) -- never a 500,
// and never a silent no-op that leaves the page looking like nothing
// happened.
func (app *App) handleCreateRegion(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")

	parentRegionID, parseErr := parseOptionalRegionID(r.FormValue("parent_region_id"))
	if parseErr != nil {
		http.Error(w, "invalid parent_region_id", http.StatusBadRequest)
		return
	}

	next := regionsNextOrDefault(r.FormValue("next"))

	_, err := app.api.CreateRegion(r.Context(), name, parentRegionID)
	if err != nil {
		if app.redirectToLoginOnUnauthenticated(w, r, err) {
			return
		}
		app.log().Info("create region refused or failed", "name", name, "parent_region_id", parentRegionID, "err", err)
		next = appendErrorParam(next, regionWriteErrorMessage(err))
	}

	http.Redirect(w, r, next, http.StatusSeeOther)
}

// handleRenameRegion is the rename form's POST target (#2317: FR2), routed
// at "POST /regions/{region_id}/rename". It calls RenameRegion on
// leaflab-api with the signed-in user's own access token (NFR2; the
// owner-or-admin check is authorizeRegionWrite server-side) and redirects
// back to the form's page, where the fresh tree shows the new name -- or,
// on a rejection (InvalidArgument empty/over-long name, PermissionDenied
// non-owner, NotFound unknown region), the ?error= parameter carries the
// API's message and the pre-filled form re-shows the unchanged current
// name. Forward-looking only (FR2): no history is involved on either side.
func (app *App) handleRenameRegion(w http.ResponseWriter, r *http.Request) {
	regionID, parseErr := strconv.ParseInt(r.PathValue("region_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	next := regionsNextOrDefault(r.FormValue("next"))

	_, err := app.api.RenameRegion(r.Context(), regionID, name)
	if err != nil {
		if app.redirectToLoginOnUnauthenticated(w, r, err) {
			return
		}
		app.log().Info("rename region refused or failed", "region_id", regionID, "err", err)
		next = appendErrorParam(next, regionWriteErrorMessage(err))
	}

	http.Redirect(w, r, next, http.StatusSeeOther)
}

// handleReparentRegion is the re-parent form's POST target (#2317: FR3,
// FR5), routed at "POST /regions/{region_id}/reparent". It calls
// ReparentRegion on leaflab-api with the signed-in user's own access token
// (NFR2) and redirects back to the form's page, where the fresh tree shows
// the moved subtree (FR3: the whole subtree moves with the region, which
// the API does by construction -- this UI touches nothing but the redirect).
//
// FR5's cycle rejection is surfaced, not pre-empted: the picker offers
// every region (including the region itself and its descendants, see
// regions.templ's regionReparentDetails), and a re-parent into
// self/descendant comes back as codes.FailedPrecondition, whose message --
// the API's own explanation -- is carried onto the redirect as ?error= and
// rendered as the action-error banner. The tree itself cannot be corrupted
// by the attempt: the API refused the write, and the re-fetched tree this
// redirect lands on is the unchanged, still-valid structure.
func (app *App) handleReparentRegion(w http.ResponseWriter, r *http.Request) {
	regionID, parseErr := strconv.ParseInt(r.PathValue("region_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	parentRegionID, parseErr := parseOptionalRegionID(r.FormValue("parent_region_id"))
	if parseErr != nil {
		http.Error(w, "invalid parent_region_id", http.StatusBadRequest)
		return
	}

	next := regionsNextOrDefault(r.FormValue("next"))

	_, err := app.api.ReparentRegion(r.Context(), regionID, parentRegionID)
	if err != nil {
		if app.redirectToLoginOnUnauthenticated(w, r, err) {
			return
		}
		app.log().Info("reparent region refused or failed", "region_id", regionID, "parent_region_id", parentRegionID, "err", err)
		next = appendErrorParam(next, regionWriteErrorMessage(err))
	}

	http.Redirect(w, r, next, http.StatusSeeOther)
}

// parseOptionalRegionID parses a picker-supplied parent region ID: empty
// means "no parent" (0 = top-level, CreateRegionRequest/ReparentRegionRequest's
// documented zero value), a non-numeric value is a tampered form and gets a
// 400 from the caller -- the same treatment handleReassignBoardOwner gives
// a non-numeric new_owner_leaflab_user_id.
func parseOptionalRegionID(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}

// regionWriteErrorMessage turns a CreateRegion/RenameRegion/ReparentRegion
// error into the message the regions page's action-error banner shows.
// codes.InvalidArgument, codes.NotFound, and codes.FailedPrecondition (FR5's
// cycle rejection, plus the API's no-op refusals like "already top-level")
// deliberately surface the API's own status message verbatim -- the issue's
// FR5 requirement is that the UI "shows the API's error", and the API's
// messages are already written for display. Only PermissionDenied gets a
// purpose-built message: the raw status names internal user IDs, nothing
// the caller can act on. Anything else (transport/Internal) falls back to
// the raw error text rather than hiding it, consistent with
// renameSensorErrorMessage's fallback.
//
// The status is extracted with a direct errors.As against the
// GRPCStatus() *status.Status interface -- NOT status.FromError/Convert:
// LeafLabClient wraps every RPC error with %w, and grpc >=1.60's FromError
// answers a wrapped status with a clone whose message it rewrites to the
// full err.Error() text ("failed to ... region N: rpc error: ..."), so the
// API's own message would never surface. errors.As on the same interface
// grpc's FromError uses internally hands back the original, unrewritten
// status instead.
func regionWriteErrorMessage(err error) string {
	var gs interface{ GRPCStatus() *status.Status }
	if !errors.As(err, &gs) {
		return "Region change failed: " + err.Error()
	}
	st := gs.GRPCStatus()
	if st == nil {
		return "Region change failed: " + err.Error()
	}
	switch st.Code() {
	case codes.PermissionDenied:
		return "You do not own this region."
	default:
		return st.Message()
	}
}

// regionsNextOrDefault validates the form-carried "next" redirect target:
// only regions paths are honored (a tampered next must not bounce the user
// off-site), anything else falls back to the top-level view. Path-only by
// construction (regions.templ's regionsNextPath never emits a query), so
// appendErrorParam can always use "?".
func regionsNextOrDefault(next string) string {
	if next == "/regions" || strings.HasPrefix(next, "/regions/") {
		return next
	}
	return "/regions"
}

// appendErrorParam appends the action-error message to a redirect target as
// its ?error= query parameter -- the POST-redirect-GET carrier the regions
// page reads back (r.URL.Query().Get("error")), the same mechanism
// handleClaimBoard's claim_error uses.
func appendErrorParam(next, msg string) string {
	return next + "?error=" + url.QueryEscape(msg)
}

// redirectToLoginOnUnauthenticated is the shared re-authenticate branch
// every app.api call site uses: a codes.Unauthenticated response means the
// token WithAccessToken already validated locally (session-side) was
// rejected server-side (revoked or expired mid-request), and the fix is the
// re-authenticate flow, not an error page -- an HX-Request gets the
// HX-Redirect header, anything else a 303 to /auth/login with the current
// URI as the post-login destination. Returns true when it handled the
// response (caller must return); false when err is something else.
func (app *App) redirectToLoginOnUnauthenticated(w http.ResponseWriter, r *http.Request, err error) bool {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		return false
	}
	loginURL := "/auth/login?next=" + url.QueryEscape(r.URL.RequestURI())
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", loginURL)
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	http.Redirect(w, r, loginURL, http.StatusSeeOther)
	return true
}
