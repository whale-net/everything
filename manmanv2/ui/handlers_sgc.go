package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/whale-net/everything/manmanv2/ui/components"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// SGCLibraryAttachment holds computed library attachment data for display.
//
// Deliberately kept, along with computeLibraryAttachments below, even
// though its one caller (handleSGCDetail, the SGC detail page's renderer)
// retired with sgc_detail.templ (task #2279, FR16): per that task's own
// instruction, retiring the page is not a license to chase down and remove
// code it happened to be the last caller of.
type SGCLibraryAttachment struct {
	Library      *manmanpb.WorkshopLibrary
	PresetName   string
	ComputedPath string
	PathOverride string
	VolumeName   string
}

func (app *App) handleSGCUpdatePorts(w http.ResponseWriter, r *http.Request, sgcIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid SGC ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	portBindings, err := parsePortBindingsJSON(r.FormValue("port_bindings_json"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	_, err = app.grpc.UpdateServerGameConfig(ctx, &manmanpb.UpdateServerGameConfigRequest{
		ServerGameConfigId: sgcID,
		PortBindings:       portBindings,
		UpdatePaths:        []string{"port_bindings"},
	})
	if err != nil {
		log.Printf("Error updating port bindings for SGC %d: %v", sgcID, err)
		http.Error(w, "Failed to update port bindings", http.StatusInternalServerError)
		return
	}

	redirectURL := fmt.Sprintf("/sgc/%d", sgcID)
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}

	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (app *App) handleAddLibraryToSGC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sgcIDStr := r.FormValue("sgc_id")
	libraryIDStr := r.FormValue("library_id")
	presetIDStr := r.FormValue("preset_id")
	volumeIDStr := r.FormValue("volume_id")
	installationPathOverride := r.FormValue("installation_path_override")

	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid sgc_id", http.StatusBadRequest)
		return
	}

	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	var presetID, volumeID int64
	if presetIDStr != "" {
		presetID, _ = strconv.ParseInt(presetIDStr, 10, 64)
	}
	if volumeIDStr != "" {
		volumeID, _ = strconv.ParseInt(volumeIDStr, 10, 64)
	}

	ctx := r.Context()
	if err := app.grpc.AddLibraryToSGC(ctx, sgcID, libraryID, presetID, volumeID, installationPathOverride); err != nil {
		log.Printf("Error adding library %d to SGC %d: %v", libraryID, sgcID, err)
		http.Error(w, "Failed to add library", http.StatusInternalServerError)
		return
	}

	redirectURL := fmt.Sprintf("/sgc/%d", sgcID)
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}

	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (app *App) handleSGCAvailableLibraries(w http.ResponseWriter, r *http.Request) {
	sgcIDStr := r.URL.Query().Get("sgc_id")
	q := r.URL.Query().Get("q")

	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid sgc_id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Fetch SGC to get game/config info
	sgcResp, err := app.grpc.GetAPI().GetServerGameConfig(ctx, &manmanpb.GetServerGameConfigRequest{
		ServerGameConfigId: sgcID,
	})
	if err != nil {
		log.Printf("Error fetching SGC: %v", err)
		http.Error(w, "Failed to fetch SGC", http.StatusInternalServerError)
		return
	}
	sgc := sgcResp.Config

	// Fetch game config to get game_id
	gcResp, err := app.grpc.GetAPI().GetGameConfig(ctx, &manmanpb.GetGameConfigRequest{
		ConfigId: sgc.GameConfigId,
	})
	if err != nil {
		log.Printf("Error fetching game config: %v", err)
		http.Error(w, "Failed to fetch game config", http.StatusInternalServerError)
		return
	}
	gameConfig := gcResp.Config

	// Get all libraries for the game
	allLibraries, err := app.grpc.ListLibraries(ctx, 100, 0, gameConfig.GameId)
	if err != nil {
		log.Printf("Error listing libraries: %v", err)
		http.Error(w, "Failed to list libraries", http.StatusInternalServerError)
		return
	}

	// Get libraries already attached to this SGC
	attached, err := app.grpc.ListSGCLibraries(ctx, sgcID)
	if err != nil {
		log.Printf("Error listing SGC libraries: %v", err)
		attached = []*manmanpb.WorkshopLibrary{}
	}

	attachedSet := make(map[int64]struct{})
	for _, lib := range attached {
		attachedSet[lib.LibraryId] = struct{}{}
	}

	// Filter: not attached, and match query if provided
	var available []*manmanpb.WorkshopLibrary
	for _, lib := range allLibraries {
		if _, isAttached := attachedSet[lib.LibraryId]; isAttached {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(lib.Name), strings.ToLower(q)) {
			continue
		}
		available = append(available, lib)
	}

	// Fetch presets and volumes for the form
	presets, err := app.grpc.ListAddonPathPresets(ctx, gameConfig.GameId)
	if err != nil {
		log.Printf("Warning: failed to fetch presets: %v", err)
		presets = []*manmanpb.GameAddonPathPreset{}
	}

	volumes, err := app.grpc.ListGameConfigVolumes(ctx, sgc.GameConfigId)
	if err != nil {
		log.Printf("Warning: failed to fetch volumes: %v", err)
		volumes = []*manmanpb.GameConfigVolume{}
	}

	// Enrich libraries with preset info
	presetMap := make(map[int64]*manmanpb.GameAddonPathPreset)
	for _, p := range presets {
		presetMap[p.PresetId] = p
	}

	enriched := make([]*components.EnrichedLibrary, len(available))
	for i, lib := range available {
		e := &components.EnrichedLibrary{WorkshopLibrary: lib}
		if lib.PresetId != 0 {
			if preset := presetMap[lib.PresetId]; preset != nil {
				e.PresetName = preset.Name
				e.PresetPath = preset.InstallationPath
			}
		}
		enriched[i] = e
	}

	type AvailableLibrariesData struct {
		SGCID     int64
		Libraries []*components.EnrichedLibrary
		Presets   []*manmanpb.GameAddonPathPreset
		Volumes   []*manmanpb.GameConfigVolume
	}

	data := AvailableLibrariesData{
		SGCID:     sgcID,
		Libraries: enriched,
		Presets:   presets,
		Volumes:   volumes,
	}

	RenderTempl(w, r, "", components.SGCAvailableLibraries(data.SGCID, data.Libraries, data.Presets, data.Volumes))
}

func (app *App) handleSGCRemoveLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sgcIDStr := r.FormValue("sgc_id")
	libraryIDStr := r.FormValue("library_id")

	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid sgc_id", http.StatusBadRequest)
		return
	}

	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	if err := app.grpc.RemoveLibraryFromSGC(ctx, sgcID, libraryID); err != nil {
		log.Printf("Error removing library %d from SGC %d: %v", libraryID, sgcID, err)
		http.Error(w, "Failed to remove library", http.StatusInternalServerError)
		return
	}

	redirectURL := fmt.Sprintf("/sgc/%d", sgcID)
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}

	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

// computeLibraryAttachments fetches and enriches library attachment data with computed paths
func (app *App) computeLibraryAttachments(ctx context.Context, sgcID, configID int64) ([]*SGCLibraryAttachment, error) {
	// Fetch SGC library attachments (with override data)
	attachmentData, err := app.grpc.GetSGCLibraryAttachments(ctx, sgcID)
	if err != nil {
		return nil, err
	}

	// Fetch volumes for path computation
	volumes, err := app.grpc.ListGameConfigVolumes(ctx, configID)
	if err != nil {
		log.Printf("Warning: failed to fetch volumes: %v", err)
		volumes = []*manmanpb.GameConfigVolume{}
	}

	// Fetch libraries
	libraries, err := app.grpc.ListSGCLibraries(ctx, sgcID)
	if err != nil {
		return nil, err
	}

	// Fetch all presets for this game (need game_id from first library or config)
	var presets []*manmanpb.GameAddonPathPreset
	if len(libraries) > 0 {
		presets, _ = app.grpc.ListAddonPathPresets(ctx, libraries[0].GameId)
	}

	// Build lookup maps
	volumeMap := make(map[int64]*manmanpb.GameConfigVolume)
	for _, v := range volumes {
		volumeMap[v.VolumeId] = v
	}

	presetMap := make(map[int64]*manmanpb.GameAddonPathPreset)
	for _, p := range presets {
		presetMap[p.PresetId] = p
	}

	libraryMap := make(map[int64]*manmanpb.WorkshopLibrary)
	for _, l := range libraries {
		libraryMap[l.LibraryId] = l
	}

	// Compute display data
	var result []*SGCLibraryAttachment
	for _, attachment := range attachmentData {
		library := libraryMap[attachment.LibraryId]
		if library == nil {
			continue
		}

		display := &SGCLibraryAttachment{
			Library: library,
		}

		// Path resolution: override → SGC preset → Library preset
		var pathToUse string

		if attachment.InstallationPathOverride != "" {
			pathToUse = attachment.InstallationPathOverride
			display.PathOverride = "Custom"
		} else if attachment.PresetId != 0 {
			preset := presetMap[attachment.PresetId]
			if preset != nil {
				pathToUse = preset.InstallationPath
				display.PresetName = preset.Name
			}
		} else if library.PresetId != 0 {
			preset := presetMap[library.PresetId]
			if preset != nil {
				pathToUse = preset.InstallationPath
				display.PresetName = preset.Name + " (default)"
			}
		}

		// Volume resolution: SGC volume → first volume
		var volume *manmanpb.GameConfigVolume
		if attachment.VolumeId != 0 {
			volume = volumeMap[attachment.VolumeId]
		} else if len(volumes) > 0 {
			volume = volumes[0]
		}

		if volume != nil {
			display.VolumeName = volume.Name
			if pathToUse != "" {
				display.ComputedPath = volume.ContainerPath + "/" + pathToUse
			} else {
				display.ComputedPath = volume.ContainerPath
			}
		} else {
			display.ComputedPath = pathToUse
		}

		result = append(result, display)
	}

	return result, nil
}
