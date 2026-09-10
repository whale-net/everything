package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// handleWorkshopLibraryConflicts renders the FR12/US6 resolution page at
// "/workshop/conflicts": every unresolved SGC->GameConfig library
// migration conflict from ListLibraryMigrationConflicts (#2365), with each
// candidate's library and source deployment resolved to names -- a Server
// Manager cannot choose sensibly from bare ids (issue #2368's
// Implementation note).
func (app *App) handleWorkshopLibraryConflicts(w http.ResponseWriter, r *http.Request) {
	app.renderWorkshopLibraryConflicts(w, r, "")
}

// renderWorkshopLibraryConflicts does the actual list-and-render work
// behind handleWorkshopLibraryConflicts, plus an optional resolutionError
// banner -- handleResolveLibraryMigrationConflict re-renders this same
// page with that set rather than a bare error page when a submission is
// rejected (issue #2368's Logging note: a stale/already-resolved
// conflict, or an override missing its keep_library_id, is expected
// control flow, not a 500).
func (app *App) renderWorkshopLibraryConflicts(w http.ResponseWriter, r *http.Request, resolutionError string) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

	conflicts, err := app.grpc.ListLibraryMigrationConflicts(ctx)
	if err != nil {
		log.Printf("Error listing library migration conflicts: %v", err)
		http.Error(w, "Failed to list library migration conflicts", http.StatusInternalServerError)
		return
	}

	// Fetched once per page load, not per candidate, mirroring
	// buildWorkshopLibraryPanels' "loop the already-small fleet-wide list"
	// precedent (handlers_workshop_page.go) -- conflict volume itself is
	// expected to be small (a one-time backfill artifact), but a conflict
	// can carry several candidates each naming a different server.
	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error listing servers: %v", err)
		servers = nil
	}
	serverNames := make(map[int64]string, len(servers))
	for _, s := range servers {
		serverNames[s.ServerId] = s.Name
	}

	views := app.buildConflictViews(ctx, conflicts, serverNames)

	breadcrumbs := []components.Breadcrumb{
		{Label: "Workshop", URL: "/workshop"},
		{Label: "Library Migration Conflicts", URL: "/workshop/conflicts"},
	}
	layoutData, err := app.buildTemplLayoutData(r, "Library Migration Conflicts", "Workshop", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	pageData := pages.WorkshopLibraryConflictsPageData{
		Layout:          layoutData,
		Conflicts:       views,
		ResolutionError: resolutionError,
	}

	if err := RenderTempl(w, r, "Library Migration Conflicts", pages.WorkshopLibraryConflictsPage(pageData)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// buildConflictViews resolves each WorkshopLibraryMigrationConflict's bare
// ids into names: the GameConfig and Game it belongs to, and per candidate
// the library and the "<config> on <server>" deployment it came from
// (games.templ's naming, FR2). A lookup failure degrades to a numbered
// fallback ("config 12", "library 34", "deployment 56") rather than
// failing the whole page -- a conflict must still be resolvable even if
// one candidate's underlying SGC has since been deleted.
func (app *App) buildConflictViews(ctx context.Context, conflicts []*manmanpb.WorkshopLibraryMigrationConflict, serverNames map[int64]string) []pages.ConflictViewData {
	views := make([]pages.ConflictViewData, 0, len(conflicts))
	for _, c := range conflicts {
		configName := fmt.Sprintf("config %d", c.ConfigId)
		gameName := ""
		if cfg, err := app.grpc.GetGameConfig(ctx, c.ConfigId); err != nil {
			log.Printf("Error fetching game config %d for conflict %d: %v", c.ConfigId, c.ConflictId, err)
		} else if cfg != nil {
			configName = cfg.Name
			if game, err := app.grpc.GetGame(ctx, cfg.GameId); err != nil {
				log.Printf("Error fetching game %d for conflict %d: %v", cfg.GameId, c.ConflictId, err)
			} else if game != nil {
				gameName = game.Name
			}
		}

		candidates := make([]pages.ConflictCandidateViewData, 0, len(c.Candidates))
		for _, cand := range c.Candidates {
			libraryName := fmt.Sprintf("library %d", cand.LibraryId)
			if lib, err := app.grpc.GetLibrary(ctx, cand.LibraryId); err != nil {
				log.Printf("Error fetching library %d for conflict %d: %v", cand.LibraryId, c.ConflictId, err)
			} else if lib != nil {
				libraryName = lib.Name
			}

			candidates = append(candidates, pages.ConflictCandidateViewData{
				LibraryID:      cand.LibraryId,
				LibraryName:    libraryName,
				SGCID:          cand.SgcId,
				DeploymentName: app.resolveConflictDeploymentName(ctx, cand.SgcId, configName, serverNames),
			})
		}

		views = append(views, pages.ConflictViewData{
			ConflictID: c.ConflictId,
			ConfigID:   c.ConfigId,
			ConfigName: configName,
			GameName:   gameName,
			Candidates: candidates,
		})
	}
	return views
}

// resolveConflictDeploymentName renders "<configName> on <server>" for a
// candidate sgcID (games.templ's "config on server" naming, FR2). No
// ControlClient wrapper exists for GetServerGameConfig (unlike
// ListServerGameConfigs) -- handlers_sgc.go's handleSGCAvailableLibraries
// establishes the same app.grpc.GetAPI().GetServerGameConfig(...) direct-call
// precedent this follows.
func (app *App) resolveConflictDeploymentName(ctx context.Context, sgcID int64, configName string, serverNames map[int64]string) string {
	sgcResp, err := app.grpc.GetAPI().GetServerGameConfig(ctx, &manmanpb.GetServerGameConfigRequest{
		ServerGameConfigId: sgcID,
	})
	if err != nil || sgcResp.Config == nil {
		log.Printf("Error fetching SGC %d: %v", sgcID, err)
		return fmt.Sprintf("deployment %d", sgcID)
	}

	serverName := serverNames[sgcResp.Config.ServerId]
	if serverName == "" {
		serverName = fmt.Sprintf("server %d", sgcResp.Config.ServerId)
	}
	return fmt.Sprintf("%s on %s", configName, serverName)
}

// handleResolveLibraryMigrationConflict is the FR12 union/override
// resolution endpoint, registered at POST "/workshop/conflicts/resolve".
// It backs conflictCard's two forms (pages/workshop_library_conflicts.templ):
// one posts resolution="union" with no keep_library_id, the other posts
// resolution="override" plus a keep_library_id chosen from a `required`
// native radio group -- no third resolution shape exists (#2359's Out of
// scope note).
//
// A malformed request (missing/non-numeric conflict_id, an unrecognized
// resolution value, or a non-numeric keep_library_id) is a client bug --
// the templ forms never produce one -- and is rejected 400 rather than
// rendered inline. An override with no keep_library_id at all is
// different: the radio group's `required` attribute should have stopped
// it client-side, but a client that bypassed that (no JS, hand-crafted
// POST) reaches here, so it renders the same friendly re-prompt as a
// FailedPrecondition rather than a raw 400 (issue #2368's Testing
// section: "never silently submitted", not "never handled gracefully").
//
// A FailedPrecondition (the conflict was already resolved, e.g. two tabs
// racing) or InvalidArgument (a keep_library_id that isn't actually one of
// this conflict's candidates) from the API is expected control flow, not
// a system failure (issue #2368's Logging note) -- both re-render
// "/workshop/conflicts" with the API's message as an inline banner
// instead of a 500. Anything else is a genuine failure and does 500.
func (app *App) handleResolveLibraryMigrationConflict(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form submission", http.StatusBadRequest)
		return
	}

	conflictID, err := strconv.ParseInt(r.FormValue("conflict_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid conflict_id", http.StatusBadRequest)
		return
	}

	resolution := r.FormValue("resolution")
	if resolution != "union" && resolution != "override" {
		http.Error(w, fmt.Sprintf("resolution must be \"union\" or \"override\", got %q", resolution), http.StatusBadRequest)
		return
	}

	var keepLibraryID int64
	if resolution == "override" {
		keepLibraryIDStr := r.FormValue("keep_library_id")
		if keepLibraryIDStr == "" {
			app.renderWorkshopLibraryConflicts(w, r, "Choose a library to keep before submitting an override.")
			return
		}
		keepLibraryID, err = strconv.ParseInt(keepLibraryIDStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid keep_library_id", http.StatusBadRequest)
			return
		}
	}

	ctx := r.Context()
	if err := app.grpc.ResolveLibraryMigrationConflict(ctx, conflictID, resolution, keepLibraryID); err != nil {
		if st, ok := status.FromError(err); ok && (st.Code() == codes.FailedPrecondition || st.Code() == codes.InvalidArgument) {
			log.Printf("Rejected conflict %d resolution (%s): %v", conflictID, resolution, err)
			app.renderWorkshopLibraryConflicts(w, r, st.Message())
			return
		}
		log.Printf("Error resolving library migration conflict %d: %v", conflictID, err)
		http.Error(w, "Failed to resolve library migration conflict", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workshop/conflicts", http.StatusSeeOther)
}
