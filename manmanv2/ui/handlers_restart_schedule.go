package main

import (
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// safeRedirectURL returns a safe relative redirect path from an untrusted URL.
// It ensures the result is a relative path that cannot be used for open redirect.
func safeRedirectURL(raw string) string {
	if raw == "" {
		return "/"
	}
	// Parse and return only the path (no scheme, host, query, or fragment)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path == "" {
		return "/"
	}
	// Ensure the path starts with '/' and has no second slash or backslash
	// (e.g. '//evil.com' or '/\evil.com' could be interpreted as external URLs)
	path := parsed.Path
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.HasPrefix(path, "/\\") {
		return "/"
	}
	return path
}

// handleRestartScheduleCreate handles POST /restart-schedules/create
func (app *App) handleRestartScheduleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sgcIDStr := r.FormValue("sgc_id")
	cadenceStr := r.FormValue("cadence_minutes")
	enabled := r.FormValue("enabled") == "true" || r.FormValue("enabled") == "on"
	redirectURL := r.FormValue("redirect_url")

	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid sgc_id", http.StatusBadRequest)
		return
	}
	cadence, err := strconv.ParseInt(cadenceStr, 10, 32)
	if err != nil || cadence <= 0 {
		http.Error(w, "cadence_minutes must be a positive integer", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	if _, err := app.grpc.CreateRestartSchedule(ctx, sgcID, int32(cadence), enabled); err != nil {
		log.Printf("Error creating restart schedule: %v", err)
		http.Error(w, "Failed to create restart schedule", http.StatusInternalServerError)
		return
	}

	if redirectURL == "" {
		redirectURL = r.Referer()
	}
	redirectURL = safeRedirectURL(redirectURL)
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

// handleRestartScheduleDelete handles POST /restart-schedules/{id}/delete
func (app *App) handleRestartScheduleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// /restart-schedules/{id}/delete
	if len(pathParts) < 3 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	scheduleID, err := strconv.ParseInt(pathParts[1], 10, 64)
	if err != nil {
		http.Error(w, "Invalid restart schedule ID", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	if err := app.grpc.DeleteRestartSchedule(ctx, scheduleID); err != nil {
		log.Printf("Error deleting restart schedule %d: %v", scheduleID, err)
		http.Error(w, "Failed to delete restart schedule", http.StatusInternalServerError)
		return
	}

	redirectURL := safeRedirectURL(r.Referer())
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}
