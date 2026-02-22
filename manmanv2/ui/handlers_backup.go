package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// handleBackupConfigCreate handles POST /games/{id}/configs/{config_id}/volumes/{volume_id}/backup-configs/create
func (app *App) handleBackupConfigCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	volumeIDStr := r.FormValue("volume_id")
	cadenceStr := r.FormValue("cadence_minutes")
	backupPath := r.FormValue("backup_path")
	enabled := r.FormValue("enabled") == "true" || r.FormValue("enabled") == "on"
	redirectURL := r.FormValue("redirect_url")

	volumeID, err := strconv.ParseInt(volumeIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid volume_id", http.StatusBadRequest)
		return
	}
	cadence, err := strconv.ParseInt(cadenceStr, 10, 32)
	if err != nil || cadence <= 0 {
		http.Error(w, "cadence_minutes must be a positive integer", http.StatusBadRequest)
		return
	}
	if backupPath == "" {
		backupPath = "."
	}

	ctx := context.Background()
	if _, err := app.grpc.CreateBackupConfig(ctx, volumeID, int32(cadence), backupPath, enabled); err != nil {
		log.Printf("Error creating backup config: %v", err)
		http.Error(w, "Failed to create backup config", http.StatusInternalServerError)
		return
	}

	if redirectURL == "" {
		redirectURL = r.Referer()
	}
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

// handleBackupConfigDelete handles POST /backup-configs/{id}/delete
func (app *App) handleBackupConfigDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// /backup-configs/{id}/delete
	if len(pathParts) < 3 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	backupConfigID, err := strconv.ParseInt(pathParts[1], 10, 64)
	if err != nil {
		http.Error(w, "Invalid backup config ID", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	if err := app.grpc.DeleteBackupConfig(ctx, backupConfigID); err != nil {
		log.Printf("Error deleting backup config %d: %v", backupConfigID, err)
		http.Error(w, "Failed to delete backup config", http.StatusInternalServerError)
		return
	}

	redirectURL := r.Referer()
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

// handleTriggerBackup handles POST /sgc/{sgc_id}/backup/trigger
func (app *App) handleTriggerBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// /sgc/{sgc_id}/backup/trigger
	if len(pathParts) < 4 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	sgcID, err := strconv.ParseInt(pathParts[1], 10, 64)
	if err != nil {
		http.Error(w, "Invalid SGC ID", http.StatusBadRequest)
		return
	}

	backupConfigIDStr := r.FormValue("backup_config_id")
	backupConfigID, err := strconv.ParseInt(backupConfigIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid backup_config_id", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	backupID, err := app.grpc.TriggerBackup(ctx, sgcID, backupConfigID)
	if err != nil {
		log.Printf("Error triggering backup for SGC %d config %d: %v", sgcID, backupConfigID, err)
		http.Error(w, fmt.Sprintf("Failed to trigger backup: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("Triggered backup %d for SGC %d", backupID, sgcID)
	redirectURL := fmt.Sprintf("/sgc/%d", sgcID)
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

// BackupConfigsForVolume groups backup configs with their volume for template rendering
type BackupConfigsForVolume struct {
	Volume  *manmanpb.GameConfigVolume
	Configs []*manmanpb.BackupConfig
}
