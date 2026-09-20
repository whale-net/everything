package main

import (
	"log"
	"net/http"
	"strconv"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// handleBackupsPage renders the fleet-wide "/backups" surface (root plan
// #2777, task #2812 -- FR1, FR3, FR4): the one place a Server Manager sees
// backup runs across every deployment and volume in the fleet, no
// per-deployment page required. Registered in main.go alongside every
// other authenticated page, behind the same RequireAuthFunc/
// WithAccessToken wrapper (NFR3 -- no new authorization axis).
//
// This handler owns the surface's shell (route, page, nav entry, tab
// scaffolding, gRPC client wrapper) so #2813/#2814/#2815/#2816 can each add
// one capability without re-litigating layout; it renders the "Backup
// runs" tab's initial (unfiltered, first-page) content itself via the same
// BackupRunsFragment htmx fragment "/backups/runs" uses
// (handleBackupRunsFragment below), so both routes stay in lock-step.
func (app *App) handleBackupsPage(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

	filter := parseBackupRunsFilter(r)
	resp, err := app.grpc.ListBackups(ctx, filter)
	if err != nil {
		log.Printf("Error fetching fleet-wide backups: %v", err)
		http.Error(w, "Failed to fetch backups", http.StatusInternalServerError)
		return
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Backups", URL: "/backups"},
	}
	layoutData, err := app.buildTemplLayoutData(r, "Backups", "Backups", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	pageData := pages.BackupsPageData{
		Layout: layoutData,
		Runs:   backupRunsFragmentData(resp, filter),
	}

	if err := RenderTempl(w, r, "Backups", pages.BackupsPage(pageData)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleBackupRunsFragment serves "/backups/runs", the htmx target both
// backupRunsFilterBar's filter submit and BackupRunsFragment's "Load more"
// button hit (pages/backups.templ) -- it renders exactly the swapped
// fragment (table + pagination control), not the full page, matching
// handlers_games.go's pages.GameOverview(...).Render(ctx, w) direct-render
// convention for htmx fragments.
func (app *App) handleBackupRunsFragment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	filter := parseBackupRunsFilter(r)
	resp, err := app.grpc.ListBackups(ctx, filter)
	if err != nil {
		log.Printf("Error fetching fleet-wide backups: %v", err)
		http.Error(w, "Failed to fetch backups", http.StatusInternalServerError)
		return
	}

	fragData := backupRunsFragmentData(resp, filter)
	if err := pages.BackupRunsFragment(fragData).Render(ctx, w); err != nil {
		log.Printf("Error rendering backup runs fragment: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// parseBackupRunsFilter reads FR4's filter set plus paging off the request
// query string -- both "/backups" (first page, filters usually absent) and
// "/backups/runs" (filter-bar submit or "Load more") share this one
// parser so the two routes can never drift on what a given query param
// means. A missing or non-numeric ID filter parses as 0, i.e. unset,
// matching BackupListFilter's zero-value-means-unset convention.
func parseBackupRunsFilter(r *http.Request) BackupListFilter {
	q := r.URL.Query()
	filter := BackupListFilter{
		Status:    q.Get("status"),
		PageToken: q.Get("page_token"),
	}
	if v, err := strconv.ParseInt(q.Get("server_game_config_id"), 10, 64); err == nil {
		filter.ServerGameConfigID = v
	}
	if v, err := strconv.ParseInt(q.Get("volume_id"), 10, 64); err == nil {
		filter.VolumeID = v
	}
	if v, err := strconv.ParseInt(q.Get("backup_config_id"), 10, 64); err == nil {
		filter.BackupConfigID = v
	}
	return filter
}

// backupRunsFragmentData assembles pages.BackupRunsFragmentData from a
// ListBackups response plus the filter that produced it -- the filter
// round-trips back into BackupRunsFilterValues so the filter bar
// redisplays what was just submitted and so paging (nextPageQuery)
// carries it forward.
func backupRunsFragmentData(resp *manmanpb.ListBackupsResponse, filter BackupListFilter) pages.BackupRunsFragmentData {
	return pages.BackupRunsFragmentData{
		Items:         resp.Items,
		NextPageToken: resp.NextPageToken,
		Filter:        backupRunsFilterValues(filter),
	}
}

// backupRunsFilterValues renders BackupListFilter's int64 ID fields as
// strings for the filter bar's <input>s, with 0 (unset) becoming ""
// rather than the literal "0" -- an empty filter input, not a filter on
// ID 0.
func backupRunsFilterValues(filter BackupListFilter) pages.BackupRunsFilterValues {
	return pages.BackupRunsFilterValues{
		ServerGameConfigID: formatFilterID(filter.ServerGameConfigID),
		VolumeID:           formatFilterID(filter.VolumeID),
		BackupConfigID:     formatFilterID(filter.BackupConfigID),
		Status:             filter.Status,
	}
}

func formatFilterID(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
