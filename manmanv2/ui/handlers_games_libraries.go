package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"sort"
	"strconv"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// This file backs the Games page's GC-level Workshop Libraries panel (task
// #2367, FR8/FR9/FR10, US5): editable attach/detach at GameConfig scope,
// replacing M5's read-only SGC-scoped panel. See pages.WorkshopPanelData's
// doc comment (workshop_panel.templ) for why this data is a lazily-fetched
// fragment rather than part of handleGames' own NFR7-bounded join.
//
// Routes (wired in main.go's handleGameDetail/handleGameConfigDetail
// dispatch, following the existing "/games/{id}/configs/{config_id}/..."
// sub-route convention rather than a new top-level path):
//
//	GET  /games/{gameID}/workshop-panel
//	GET  /games/{gameID}/configs/{configID}/libraries/available
//	POST /games/{gameID}/configs/{configID}/libraries/add
//	POST /games/{gameID}/configs/{configID}/libraries/{libraryID}/remove
//
// NFR6 guard: none of this touches /sgc/add-library, /sgc/remove-library,
// or /sgc/api/available-libraries -- those stay exactly as they are until
// the dependent retirement task (#2370) cuts over.

// buildWorkshopPanelData assembles one game's Workshop Libraries panel
// (FR8/FR9/FR10): every GameConfig of the game, each either flagged with
// an unresolved migration conflict (FR12 -- Libraries is never populated
// in that case, see ConfigWorkshopSection.HasConflict) or populated with
// its attached libraries and resolved install target.
//
// Every RPC here degrades gracefully (a failure logs a warning and
// continues with an empty result) rather than failing the whole panel,
// matching handleGameDetail's existing convention for this page family --
// a Server Manager should still see whatever did load rather than a bare
// error for one config's hiccup.
func (app *App) buildWorkshopPanelData(ctx context.Context, gameID int64) pages.WorkshopPanelData {
	configs, err := app.grpc.ListGameConfigs(ctx, gameID)
	if err != nil {
		log.Printf("Warning: failed to fetch game configs for workshop panel (game %d): %v", gameID, err)
		configs = nil
	}

	conflictConfigIDs := make(map[int64]bool)
	conflicts, err := app.grpc.ListLibraryMigrationConflicts(ctx)
	if err != nil {
		log.Printf("Warning: failed to fetch library migration conflicts for workshop panel (game %d): %v", gameID, err)
	} else {
		for _, c := range conflicts {
			conflictConfigIDs[c.ConfigId] = true
		}
	}

	presets, err := app.grpc.ListAddonPathPresets(ctx, gameID)
	if err != nil {
		log.Printf("Warning: failed to fetch path presets for workshop panel (game %d): %v", gameID, err)
		presets = nil
	}
	presetByID := make(map[int64]*manmanpb.GameAddonPathPreset, len(presets))
	for _, p := range presets {
		presetByID[p.PresetId] = p
	}

	sections := make([]pages.ConfigWorkshopSection, 0, len(configs))
	for _, cfg := range configs {
		section := pages.ConfigWorkshopSection{
			ConfigID:   cfg.ConfigId,
			ConfigName: cfg.Name,
		}

		if conflictConfigIDs[cfg.ConfigId] {
			// FR12 guard: never render a bare empty list for a
			// conflicted config -- the pointer is the whole point.
			section.HasConflict = true
			sections = append(sections, section)
			continue
		}

		attachments, err := app.grpc.GetGameConfigLibraryAttachments(ctx, cfg.ConfigId)
		if err != nil {
			log.Printf("Warning: failed to fetch library attachments for config %d: %v", cfg.ConfigId, err)
			attachments = nil
		}
		libraries, err := app.grpc.ListGameConfigLibraries(ctx, cfg.ConfigId)
		if err != nil {
			log.Printf("Warning: failed to fetch libraries for config %d: %v", cfg.ConfigId, err)
			libraries = nil
		}
		libraryByID := make(map[int64]*manmanpb.WorkshopLibrary, len(libraries))
		for _, l := range libraries {
			libraryByID[l.LibraryId] = l
		}

		for _, a := range attachments {
			lib := libraryByID[a.LibraryId]
			if lib == nil {
				// An attachment whose library metadata didn't come back
				// (e.g. transient fetch gap) is skipped rather than
				// rendered with a blank name.
				continue
			}
			section.Libraries = append(section.Libraries, pages.WorkshopLibraryRow{
				LibraryID:   a.LibraryId,
				LibraryName: lib.Name,
				Target:      resolveGCLibraryTarget(a, lib, presetByID),
			})
		}
		sort.Slice(section.Libraries, func(i, j int) bool {
			return section.Libraries[i].LibraryName < section.Libraries[j].LibraryName
		})

		sections = append(sections, section)
	}
	sort.Slice(sections, func(i, j int) bool {
		if sections[i].ConfigName != sections[j].ConfigName {
			return sections[i].ConfigName < sections[j].ConfigName
		}
		return sections[i].ConfigID < sections[j].ConfigID
	})

	return pages.WorkshopPanelData{
		GameID:   gameID,
		Sections: sections,
	}
}

// resolveGCLibraryTarget resolves a GC-level attachment's install path:
// the attachment's own override, else its overriding preset's path, else
// the library's own default preset's path, in that order -- "" when none
// resolves. Mirrors handlers_sgc.go's computeLibraryAttachments path
// resolution, minus the volume-prefix half: M6's GC-level attachment
// carries a volume_id override too, but resolving it into a full
// "container_path/install_path" string would cost an extra
// ListGameConfigVolumes call per config on top of the two this panel
// already makes, which this task's design intentionally does not add --
// the install path alone is enough to distinguish attachments in the
// panel (no two libraries on the same config share both name and path).
func resolveGCLibraryTarget(a *manmanpb.GameConfigWorkshopLibrary, lib *manmanpb.WorkshopLibrary, presetByID map[int64]*manmanpb.GameAddonPathPreset) string {
	if a.InstallationPathOverride != "" {
		return a.InstallationPathOverride
	}
	if a.PresetId != 0 {
		if p := presetByID[a.PresetId]; p != nil {
			return p.InstallationPath
		}
	}
	if lib.PresetId != 0 {
		if p := presetByID[lib.PresetId]; p != nil {
			return p.InstallationPath
		}
	}
	return ""
}

// renderWorkshopPanel renders the panel fragment directly (not through
// RenderTempl's full-page layout) -- the shared re-render target for the
// lazy GET and every attach/detach POST, mirroring
// handlers_config_editor.go's renderConfigEditorBlade convention.
func (app *App) renderWorkshopPanel(w http.ResponseWriter, r *http.Request, gameID int64) {
	data := app.buildWorkshopPanelData(r.Context(), gameID)
	w.Header().Set("Content-Type", "text/html")
	if err := pages.WorkshopPanel(data).Render(r.Context(), w); err != nil {
		log.Printf("Error rendering workshop panel for game %d: %v", gameID, err)
	}
}

// handleGameWorkshopPanel is GET /games/{gameID}/workshop-panel (see
// gameWorkshopPlaceholder's doc comment in games.templ for why this is a
// lazy fragment fetch rather than part of handleGames' join).
func (app *App) handleGameWorkshopPanel(w http.ResponseWriter, r *http.Request, gameIDStr string) {
	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	app.renderWorkshopPanel(w, r, gameID)
}

// handleGameConfigAvailableLibraries is GET
// /games/{gameID}/configs/{configID}/libraries/available -- the GC-scoped
// attach picker (FR8), reusing handleSGCAvailableLibraries' lookup shape:
// every library of this GameConfig's game that isn't already attached,
// optionally filtered by a "q" query param.
func (app *App) handleGameConfigAvailableLibraries(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr string) {
	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid config ID", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	allLibraries, err := app.grpc.ListLibraries(ctx, 100, 0, gameID)
	if err != nil {
		log.Printf("Error listing libraries for game %d: %v", gameID, err)
		http.Error(w, "Failed to list libraries", http.StatusInternalServerError)
		return
	}

	attached, err := app.grpc.ListGameConfigLibraries(ctx, configID)
	if err != nil {
		log.Printf("Warning: failed to list attached libraries for config %d: %v", configID, err)
		attached = nil
	}
	attachedSet := make(map[int64]struct{}, len(attached))
	for _, lib := range attached {
		attachedSet[lib.LibraryId] = struct{}{}
	}

	presets, err := app.grpc.ListAddonPathPresets(ctx, gameID)
	if err != nil {
		log.Printf("Warning: failed to fetch presets for game %d: %v", gameID, err)
		presets = nil
	}
	presetMap := make(map[int64]*manmanpb.GameAddonPathPreset, len(presets))
	for _, p := range presets {
		presetMap[p.PresetId] = p
	}

	var available []*components.EnrichedLibrary
	for _, lib := range allLibraries {
		if _, isAttached := attachedSet[lib.LibraryId]; isAttached {
			continue
		}
		e := &components.EnrichedLibrary{WorkshopLibrary: lib}
		if lib.PresetId != 0 {
			if preset := presetMap[lib.PresetId]; preset != nil {
				e.PresetName = preset.Name
				e.PresetPath = preset.InstallationPath
			}
		}
		available = append(available, e)
	}

	w.Header().Set("Content-Type", "text/html")
	if err := components.GameConfigAvailableLibraries(gameID, configID, available).Render(ctx, w); err != nil {
		log.Printf("Error rendering available libraries for config %d: %v", configID, err)
	}
}

// handleGameConfigLibraryAdd is POST
// /games/{gameID}/configs/{configID}/libraries/add (FR8): attaches a
// library to this GameConfig -- every deployment of the config inherits
// it, with no per-deployment step. Re-renders the whole panel fragment on
// success, the same convention handleConfigEditorVolumeBackupConfig uses
// for its own inline mutations.
func (app *App) handleGameConfigLibraryAdd(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid config ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	libraryID, err := strconv.ParseInt(r.FormValue("library_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	var presetID int64
	if v := r.FormValue("preset_id"); v != "" {
		presetID, _ = strconv.ParseInt(v, 10, 64)
	}

	ctx := r.Context()
	if err := app.grpc.AddLibraryToGameConfig(ctx, configID, libraryID, presetID, 0, ""); err != nil {
		log.Printf("Error adding library %d to game config %d: %v", libraryID, configID, err)
		http.Error(w, "Failed to add library", http.StatusInternalServerError)
		return
	}
	slog.Info("workshop library attached to game config", "config_id", configID, "library_id", libraryID)

	app.renderWorkshopPanel(w, r, gameID)
}

// handleGameConfigLibraryRemove is POST
// /games/{gameID}/configs/{configID}/libraries/{libraryID}/remove (FR9):
// detaches a library from this GameConfig -- removing it from every
// deployment that had inherited it, with no per-deployment bookkeeping.
func (app *App) handleGameConfigLibraryRemove(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr, libraryIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid config ID", http.StatusBadRequest)
		return
	}
	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library ID", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	if err := app.grpc.RemoveLibraryFromGameConfig(ctx, configID, libraryID); err != nil {
		log.Printf("Error removing library %d from game config %d: %v", libraryID, configID, err)
		http.Error(w, "Failed to remove library", http.StatusInternalServerError)
		return
	}
	slog.Info("workshop library detached from game config", "config_id", configID, "library_id", libraryID)

	app.renderWorkshopPanel(w, r, gameID)
}
