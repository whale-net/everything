package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

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
//
// Scaffold note (#2368): this is the read path. Resolution
// (handleResolveLibraryMigrationConflict below) is a placeholder the
// Implementation phase fills in.
func (app *App) handleWorkshopLibraryConflicts(w http.ResponseWriter, r *http.Request) {
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
		Layout:    layoutData,
		Conflicts: views,
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
//
// Scaffold placeholder (#2368): the Implementation phase wires the actual
// form submission (parsing conflict_id/resolution/keep_library_id, calling
// ControlClient.ResolveLibraryMigrationConflict, and surfacing a stale/
// already-resolved submission's FailedPrecondition as a clear user-facing
// message rather than a 500 -- issue #2368's Logging note: that is expected
// control flow, not an ERROR-level failure) and redirects back to
// "/workshop/conflicts" on success. The route is registered now so
// conflictCard's future resolution controls (pages/workshop_library_conflicts.templ)
// have a stable target to submit to.
func (app *App) handleResolveLibraryMigrationConflict(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Not implemented yet -- Implementation phase of #2368", http.StatusNotImplemented)
}
