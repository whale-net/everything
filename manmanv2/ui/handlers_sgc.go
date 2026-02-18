package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// SGCDetailPageData holds data for the SGC detail page.
type SGCDetailPageData struct {
	Title         string
	Active        string
	User          *htmxauth.UserInfo
	SGC           *manmanpb.ServerGameConfig
	Server        *manmanpb.Server
	GameConfig    *manmanpb.GameConfig
	Sessions      []*manmanpb.Session
	Libraries     []*manmanpb.WorkshopLibrary
	Installations []*manmanpb.WorkshopInstallation
	// AddonStatusMap maps addon_id -> installation status for quick lookup
	AddonStatusMap map[int64]*manmanpb.WorkshopInstallation
	PendingCount   int
}

func (app *App) handleSGCDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	// Parse sgc_id from path: /sgc/{sgc_id}
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 || pathParts[1] == "" {
		http.Error(w, "Missing SGC ID", http.StatusBadRequest)
		return
	}

	sgcIDStr := pathParts[1]
	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid SGC ID", http.StatusBadRequest)
		return
	}

	ctx := context.Background()

	// Fetch SGC
	sgcResp, err := app.grpc.GetAPI().GetServerGameConfig(ctx, &manmanpb.GetServerGameConfigRequest{
		ServerGameConfigId: sgcID,
	})
	if err != nil {
		log.Printf("Error fetching SGC %d: %v", sgcID, err)
		http.Error(w, "SGC not found", http.StatusNotFound)
		return
	}
	sgc := sgcResp.Config

	// Fetch server
	serverResp, err := app.grpc.GetAPI().GetServer(ctx, &manmanpb.GetServerRequest{
		ServerId: sgc.ServerId,
	})
	var server *manmanpb.Server
	if err != nil {
		log.Printf("Warning: failed to fetch server %d: %v", sgc.ServerId, err)
	} else {
		server = serverResp.Server
	}

	// Fetch game config
	gcResp, err := app.grpc.GetAPI().GetGameConfig(ctx, &manmanpb.GetGameConfigRequest{
		ConfigId: sgc.GameConfigId,
	})
	var gameConfig *manmanpb.GameConfig
	if err != nil {
		log.Printf("Warning: failed to fetch game config %d: %v", sgc.GameConfigId, err)
	} else {
		gameConfig = gcResp.Config
	}

	// Fetch sessions for this SGC
	sessionsResp, err := app.grpc.ListSessionsWithFilters(ctx, &manmanpb.ListSessionsRequest{
		ServerGameConfigId: sgc.ServerGameConfigId,
		PageSize:           50,
	})
	var sessions []*manmanpb.Session
	if err != nil {
		log.Printf("Warning: failed to list sessions for SGC %d: %v", sgcID, err)
	} else {
		sessions = sessionsResp
	}

	// Fetch libraries attached to SGC
	libraries, err := app.grpc.ListSGCLibraries(ctx, sgcID)
	if err != nil {
		log.Printf("Warning: failed to list SGC libraries: %v", err)
		libraries = []*manmanpb.WorkshopLibrary{}
	}

	// Fetch installations for this SGC
	installations, err := app.grpc.ListWorkshopInstallations(ctx, sgcID)
	if err != nil {
		log.Printf("Warning: failed to list workshop installations: %v", err)
		installations = []*manmanpb.WorkshopInstallation{}
	}

	// Build addon status map
	addonStatusMap := make(map[int64]*manmanpb.WorkshopInstallation)
	for _, inst := range installations {
		addonStatusMap[inst.AddonId] = inst
	}

	// Count pending installs
	pendingCount := 0
	for _, inst := range installations {
		if inst.Status == "pending" || inst.Status == "downloading" {
			pendingCount++
		}
	}

	data := SGCDetailPageData{
		Title:          fmt.Sprintf("SGC %d", sgcID),
		Active:         "sessions",
		User:           user,
		SGC:            sgc,
		Server:         server,
		GameConfig:     gameConfig,
		Sessions:       sessions,
		Libraries:      libraries,
		Installations:  installations,
		AddonStatusMap: addonStatusMap,
		PendingCount:   pendingCount,
	}

	layoutData, err := app.buildLayoutData(r, data.Title, data.Active, user)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := renderPage(w, "sgc_detail_content", data, layoutData); err != nil {
		log.Printf("Error rendering SGC detail template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
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

	ctx := context.Background()
	if err := app.grpc.AddLibraryToSGC(ctx, sgcID, libraryID); err != nil {
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

	ctx := context.Background()

	// Get all libraries (no game filter here; could add later)
	allLibraries, err := app.grpc.ListLibraries(ctx, 100, 0, 0)
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

	type AvailableLibrariesData struct {
		Libraries []*manmanpb.WorkshopLibrary
		SGCID     int64
	}

	data := AvailableLibrariesData{
		Libraries: available,
		SGCID:     sgcID,
	}

	if err := renderTemplate(w, "sgc_available_libraries_partial", data); err != nil {
		log.Printf("Error rendering available libraries partial: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
