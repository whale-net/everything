package main

import (
	"log"
	"log/slog"
	"net/http"
	"strconv"
)

// handleConfigEditorVolumeBackupConfig dispatches the Config Editor's
// Volumes tab inline backup-config routes (FR14/FR15, #2363), under the
// existing config-editor route family (handlers_games.go's
// handleGameConfigDetail "volumes" case):
//
//	POST /games/{gameID}/configs/{configID}/volumes/{volumeID}/backup-config/assign
//	POST /games/{gameID}/configs/{configID}/volumes/{volumeID}/backup-config/edit
//	POST /games/{gameID}/configs/{configID}/volumes/{volumeID}/backup-config/remove
//
// Every branch re-renders the whole Config Editor blade fragment via
// renderConfigEditorBlade with activeTab="volumes" (handlers_config_editor.go,
// the same outerHTML-swap convention handleGameConfigEditorSave's
// validation-error branch already uses for the Basics/Environment form) so
// a Server Manager never leaves the Blade to assign, edit, or remove a
// volume's backup config (FR14's acceptance criterion 1).
func (app *App) handleConfigEditorVolumeBackupConfig(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr, volumeIDStr, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	volumeID, err := strconv.ParseInt(volumeIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid volume ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	switch action {
	case "assign":
		// FR14: "assign an existing BackupConfig" against this schema is
		// creating one bound to this volume -- see ConfigEditorVolume's
		// doc comment (config_editor.templ) for why there is no separate,
		// reusable "template" row to pick from instead.
		cadence, path, enabled, ok := parseConfigEditorBackupForm(w, r)
		if !ok {
			return
		}
		if _, err := app.grpc.CreateBackupConfig(ctx, volumeID, cadence, path, enabled); err != nil {
			log.Printf("Error assigning backup config to volume %d: %v", volumeID, err)
			http.Error(w, "Failed to assign backup config", http.StatusInternalServerError)
			return
		}
		slog.Info("backup config assigned", "volume_id", volumeID, "cadence_minutes", cadence, "backup_path", path, "enabled", enabled)

	case "edit":
		backupConfigID, ok := parseConfigEditorBackupConfigID(w, r)
		if !ok {
			return
		}
		cadence, path, enabled, ok := parseConfigEditorBackupForm(w, r)
		if !ok {
			return
		}
		if _, err := app.grpc.UpdateBackupConfig(ctx, backupConfigID, cadence, path, enabled); err != nil {
			log.Printf("Error updating backup config %d for volume %d: %v", backupConfigID, volumeID, err)
			http.Error(w, "Failed to update backup config", http.StatusInternalServerError)
			return
		}
		slog.Info("backup config updated", "backup_config_id", backupConfigID, "volume_id", volumeID, "cadence_minutes", cadence, "backup_path", path, "enabled", enabled)

	case "remove":
		backupConfigID, ok := parseConfigEditorBackupConfigID(w, r)
		if !ok {
			return
		}
		// FR15: this deletes the BackupConfig row itself -- under this
		// schema the row *is* the volume's assignment (see
		// ConfigEditorVolume's doc comment) -- but never any other
		// volume's row, and never a "template" shared across volumes,
		// since no such thing exists here. The underlying template
		// concept NFR4 protects is the standalone surface's own
		// create/edit/delete, which this route never calls.
		if err := app.grpc.DeleteBackupConfig(ctx, backupConfigID); err != nil {
			log.Printf("Error removing backup config %d for volume %d: %v", backupConfigID, volumeID, err)
			http.Error(w, "Failed to remove backup config", http.StatusInternalServerError)
			return
		}
		slog.Info("backup config assignment removed", "backup_config_id", backupConfigID, "volume_id", volumeID)

	default:
		http.Error(w, "Unknown backup-config action", http.StatusNotFound)
		return
	}

	app.renderConfigEditorBlade(w, r, gameIDStr, configIDStr, "volumes")
}

// parseConfigEditorBackupForm reads and validates the shared
// cadence_minutes/backup_path/enabled fields the assign and edit forms both
// submit (config_editor.templ). A validation failure writes the response
// itself and returns ok=false -- this is normal control flow (a rejected
// form submission), not an error worth WARNING/ERROR-level logging.
func parseConfigEditorBackupForm(w http.ResponseWriter, r *http.Request) (cadenceMinutes int32, backupPath string, enabled bool, ok bool) {
	cadence, err := strconv.ParseInt(r.FormValue("cadence_minutes"), 10, 32)
	if err != nil || cadence <= 0 {
		http.Error(w, "cadence_minutes must be a positive integer", http.StatusBadRequest)
		return 0, "", false, false
	}

	backupPath = r.FormValue("backup_path")
	if backupPath == "" {
		// Mirrors handleBackupConfigCreate's own default (handlers_backup.go)
		// -- NFR2: same default, not a new convention.
		backupPath = "."
	}

	enabled = r.FormValue("enabled") == "true"

	return int32(cadence), backupPath, enabled, true
}

// parseConfigEditorBackupConfigID reads the hidden backup_config_id field
// the edit and remove forms both submit (config_editor.templ).
func parseConfigEditorBackupConfigID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	backupConfigID, err := strconv.ParseInt(r.FormValue("backup_config_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid backup_config_id", http.StatusBadRequest)
		return 0, false
	}
	return backupConfigID, true
}
