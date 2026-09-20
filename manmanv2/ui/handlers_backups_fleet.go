package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	resp, listErr := app.grpc.ListBackups(ctx, filter)
	if listErr != nil {
		log.Printf("ERROR: backups: failed to fetch fleet-wide backups: %v", listErr)
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Backups", URL: "/backups"},
	}
	layoutData, err := app.buildTemplLayoutData(r, "Backups", "Backups", user, breadcrumbs)
	if err != nil {
		log.Printf("ERROR: backups: failed to build layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	configFilter := parseBackupConfigsFilter(r)
	configResp, configListErr := app.grpc.ListBackupConfigsFleet(ctx, configFilter)
	if configListErr != nil {
		log.Printf("ERROR: backups: failed to fetch fleet-wide backup configs: %v", configListErr)
	}

	pageData := pages.BackupsPageData{
		Layout:        layoutData,
		Runs:          backupRunsFragmentData(resp, filter, listErr),
		FilterOptions: app.backupRunsFilterOptions(ctx),
		Configs:       backupConfigsFragmentData(configResp, configFilter, configListErr),
	}

	if err := RenderTempl(w, r, "Backups", pages.BackupsPage(pageData)); err != nil {
		log.Printf("ERROR: backups: failed to render template: %v", err)
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
	resp, listErr := app.grpc.ListBackups(ctx, filter)
	if listErr != nil {
		log.Printf("ERROR: backups: failed to fetch fleet-wide backups: %v", listErr)
	}

	fragData := backupRunsFragmentData(resp, filter, listErr)
	if err := pages.BackupRunsFragment(fragData).Render(ctx, w); err != nil {
		log.Printf("ERROR: backups: failed to render backup runs fragment: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleBackupConfigsFragment serves "/backups/configs", the htmx target
// both backupConfigsFilterBar's filter submit and BackupConfigsFragment's
// "Load more" button hit (pages/backups.templ) -- mirrors
// handleBackupRunsFragment's shape for the "Backup configs" tab (FR9-FR11).
func (app *App) handleBackupConfigsFragment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	filter := parseBackupConfigsFilter(r)
	resp, listErr := app.grpc.ListBackupConfigsFleet(ctx, filter)
	if listErr != nil {
		log.Printf("ERROR: backups: failed to fetch fleet-wide backup configs: %v", listErr)
	}

	fragData := backupConfigsFragmentData(resp, filter, listErr)
	if err := pages.BackupConfigsFragment(fragData).Render(ctx, w); err != nil {
		log.Printf("ERROR: backups: failed to render backup configs fragment: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleBackupRunDetail renders "/backups/runs/{backup_id}" (task #2814,
// FR6): a single run's diagnosable detail -- status, trigger origin, S3
// location (once completed), size, error message when failed, deployment/
// volume/BackupConfig context, and the run's pre-backup Actions. An unknown
// id 404s rather than 500ing (the acceptance criteria's NotFound
// requirement); GetBackup's NotFound is the only gRPC status this handler
// distinguishes from a generic upstream failure.
func (app *App) handleBackupRunDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := htmxauth.GetUser(ctx)

	backupID, ok := parseBackupRunID(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	backup, err := app.grpc.GetBackup(ctx, backupID)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			http.NotFound(w, r)
			return
		}
		log.Printf("ERROR: backups: failed to fetch backup run %d: %v", backupID, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Backups", URL: "/backups"},
		{Label: fmt.Sprintf("Run #%d", backupID), URL: fmt.Sprintf("/backups/runs/%d", backupID)},
	}
	layoutData, err := app.buildTemplLayoutData(r, fmt.Sprintf("Backup run #%d", backupID), "Backups", user, breadcrumbs)
	if err != nil {
		log.Printf("ERROR: backups: failed to build layout data for run %d: %v", backupID, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	data := pages.BackupRunDetailData{Layout: layoutData, Backup: backup}
	data.ServerGameConfigName, data.VolumeName, data.BackupConfigLabel = app.resolveBackupRunDisplayNames(ctx, backup)

	// A run's pre-backup Actions come from its BackupConfig, not a per-run
	// execution record -- one does not exist in the schema (see this task's
	// issue body). A manual backup with no backup_config_id renders the
	// template's explicit "none" state instead of an empty list.
	if backup.BackupConfigId != 0 {
		data.HasBackupConfig = true
		actions, err := app.grpc.ListBackupConfigActions(ctx, backup.BackupConfigId)
		if err != nil {
			log.Printf("WARNING: backups: failed to list pre-backup actions for backup config %d (run %d): %v", backup.BackupConfigId, backupID, err)
		} else {
			data.Actions = actions
		}
	}

	if err := RenderTempl(w, r, fmt.Sprintf("Backup run #%d", backupID), pages.BackupRunDetail(data)); err != nil {
		log.Printf("ERROR: backups: failed to render backup run detail template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleBackupRunDelete serves "POST /backups/runs/{backup_id}/delete"
// (root plan #2777, task #2815 -- FR7, FR8): deletes one backup run via the
// API's existing DeleteBackup RPC, which deletes the S3 object first and
// only then soft-deletes the row (manmanv2/api/handlers/backup.go) -- this
// handler wires that behavior to the fleet surface as-is and does not add a
// second, UI-side S3 delete path. It always re-renders the same
// BackupRunsFragment the row's own view was showing (via
// backupRunsFilterFromReferer), whether the delete succeeded or not, so a
// successful delete simply drops the row from the refreshed list and a
// failed one leaves it in place with an inline notice -- neither path
// returns a 500 for an expected API error.
func (app *App) handleBackupRunDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// "/backups/runs/{backup_id}/delete"
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) != 4 || pathParts[3] != "delete" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	backupID, err := strconv.ParseInt(pathParts[2], 10, 64)
	if err != nil {
		http.Error(w, "Invalid backup ID", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	filter := backupRunsFilterFromReferer(r)

	var notice, variant string
	if err := app.grpc.DeleteBackup(ctx, backupID); err != nil {
		switch status.Code(err) {
		case codes.FailedPrecondition:
			notice = "This run has no archive uploaded yet -- wait for it to finish or fail before deleting it."
			variant = "warning"
		case codes.NotFound:
			notice = "This backup run was not found -- it may already have been deleted."
			variant = "warning"
		default:
			log.Printf("ERROR: backups: failed to delete backup run %d: %v", backupID, err)
			notice = "Failed to delete backup run. Try again."
			variant = "error"
		}
	}

	resp, listErr := app.grpc.ListBackups(ctx, filter)
	if listErr != nil {
		log.Printf("ERROR: backups: failed to refresh backup runs after delete attempt on %d: %v", backupID, listErr)
	}
	fragData := backupRunsFragmentData(resp, filter, listErr)
	fragData.DeleteNotice = notice
	fragData.DeleteNoticeVariant = variant
	if err := pages.BackupRunsFragment(fragData).Render(ctx, w); err != nil {
		log.Printf("ERROR: backups: failed to render backup runs fragment after delete: %v", err)

		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleBackupRunRoute dispatches the "/backups/runs/" subtree (registered
// once in main.go's setupRoutes) between the two shapes it serves: "POST
// /backups/runs/{id}/delete" (task #2815, FR7/FR8) and "GET /backups/runs/
// {id}" (task #2814, FR6). net/http.ServeMux panics at registration time if
// two handlers are registered on the same pattern, so both tasks' handlers
// stay as separate functions and this dispatches between them on the
// trailing "/delete" segment -- not on method alone, so a non-POST request
// to the delete path still reaches handleBackupRunDelete and gets its own
// 405 rather than falling through to handleBackupRunDetail's id parsing
// (which would otherwise 404 on the non-numeric "delete" segment).
func (app *App) handleBackupRunRoute(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.Trim(r.URL.Path, "/"), "/delete") {
		app.handleBackupRunDelete(w, r)
		return
	}
	app.handleBackupRunDetail(w, r)
}

// parseBackupRunID extracts the backup id from "/backups/runs/{id}", the
// only sub-route "/backups/runs/" (registered as a subtree pattern
// alongside the exact-match "/backups/runs" fragment route) serves --
// matching handleGameDetail's (handlers_games.go) manual path-segment
// convention rather than net/http's newer "{wildcard}" mux patterns, so
// every route in this file stays on the one parsing style. A non-numeric or
// non-positive id is treated the same as "unknown id" (404), not a 400 --
// there is no meaningful distinction a Server Manager would act on
// differently.
func parseBackupRunID(path string) (int64, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// resolveBackupRunDisplayNames resolves a single run's deployment (SGC),
// volume and BackupConfig display names best-effort, reusing the same
// fleet-wide helpers backupRunsFilterOptions above already relies on for its
// pickers. Resolution failure -- or the id simply not being found in the
// resolved set -- degrades to an empty string per field (the template falls
// back to a raw id) rather than failing the whole detail page, matching
// backupRunsFilterOptions' own degrade posture.
func (app *App) resolveBackupRunDisplayNames(ctx context.Context, backup *manmanpb.Backup) (sgcName, volumeName, backupConfigLabel string) {
	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("WARNING: backups: failed to list servers to resolve run %d's deployment name: %v", backup.BackupId, err)
	} else {
		sgcByID, serverByID := app.resolveFleetWideActivitySet(ctx, servers)
		if sgc, ok := sgcByID[backup.ServerGameConfigId]; ok {
			gameConfigByID, _ := app.activityDisplayNames(ctx, map[int64]*manmanpb.ServerGameConfig{sgc.ServerGameConfigId: sgc})
			server := serverByID[sgc.ServerId]
			gameConfig := gameConfigByID[sgc.GameConfigId]
			if server != nil && gameConfig != nil {
				sgcName = fmt.Sprintf("%s / %s", server.Name, gameConfig.Name)
			}
		}
	}

	items, err := app.grpc.ListBackupConfigItems(ctx)
	if err != nil {
		log.Printf("WARNING: backups: failed to list backup configs to resolve run %d's volume/config name: %v", backup.BackupId, err)
		return sgcName, volumeName, backupConfigLabel
	}
	for _, item := range items {
		if item.Config == nil {
			continue
		}
		if item.Config.VolumeId == backup.VolumeId {
			volumeName = item.VolumeName
		}
		if backup.BackupConfigId != 0 && item.Config.BackupConfigId == backup.BackupConfigId {
			backupConfigLabel = fmt.Sprintf("%s / %s", item.GameConfigName, item.VolumeName)
		}
	}
	return sgcName, volumeName, backupConfigLabel
}

// parseBackupConfigsFilter reads FR10's filter set plus paging off the
// request query string, mirroring parseBackupRunsFilter -- both "/backups"
// (first page, filters usually absent) and "/backups/configs" (filter-bar
// submit or "Load more") share this one parser. "enabled" parses to nil
// (unset) unless it is exactly "true" or "false", so an unrecognized or
// missing value never accidentally filters on a tri-state value.
func parseBackupConfigsFilter(r *http.Request) BackupConfigListFilter {
	q := r.URL.Query()
	filter := BackupConfigListFilter{
		PageToken: q.Get("page_token"),
	}
	if v, err := strconv.ParseInt(q.Get("volume_id"), 10, 64); err == nil {
		filter.VolumeID = v
	}
	if v, err := strconv.ParseInt(q.Get("game_config_id"), 10, 64); err == nil {
		filter.GameConfigID = v
	}
	switch q.Get("enabled") {
	case "true":
		enabled := true
		filter.Enabled = &enabled
	case "false":
		enabled := false
		filter.Enabled = &enabled
	}
	return filter
}

// backupConfigsFragmentData assembles pages.BackupConfigsFragmentData from a
// ListBackupConfigsFleet response plus the filter that produced it,
// mirroring backupRunsFragmentData -- the filter round-trips back into
// BackupConfigsFilterValues so the filter bar redisplays what was just
// submitted and so paging (nextConfigPageQuery) carries it forward.
// listErr non-nil means the ListBackupConfigsFleet call itself failed
// (resp is nil): the fragment renders an inline error alert instead of the
// table rather than the caller returning a bare 500.
func backupConfigsFragmentData(resp *manmanpb.ListBackupConfigsResponse, filter BackupConfigListFilter, listErr error) pages.BackupConfigsFragmentData {
	data := pages.BackupConfigsFragmentData{
		Filter: backupConfigsFilterValues(filter),
	}
	if listErr != nil {
		data.Err = "Failed to load backup configs. Try again."
		return data
	}
	data.Items = resp.Items
	data.NextPageToken = resp.NextPageToken
	return data
}

// backupConfigsFilterValues renders BackupConfigListFilter's fields as
// strings for the filter bar's <input>/<select>s, mirroring
// backupRunsFilterValues -- 0 (unset) becomes "" for the ID filters, and a
// nil Enabled becomes "" (matching the filter bar's "All" option) rather
// than a stringified "false".
func backupConfigsFilterValues(filter BackupConfigListFilter) pages.BackupConfigsFilterValues {
	values := pages.BackupConfigsFilterValues{
		VolumeID:     formatFilterID(filter.VolumeID),
		GameConfigID: formatFilterID(filter.GameConfigID),
	}
	if filter.Enabled != nil {
		values.Enabled = strconv.FormatBool(*filter.Enabled)
	}
	return values

}

// parseBackupRunsFilter reads FR4's filter set plus paging off the request
// query string -- both "/backups" (first page, filters usually absent) and
// "/backups/runs" (filter-bar submit or "Load more") share this one
// parser so the two routes can never drift on what a given query param
// means. A missing or non-numeric ID filter parses as 0, i.e. unset,
// matching BackupListFilter's zero-value-means-unset convention.
func parseBackupRunsFilter(r *http.Request) BackupListFilter {
	return backupRunsFilterFromQuery(r.URL.Query())
}

// backupRunsFilterFromReferer reconstructs the filter+page that was active
// on whichever "/backups" or "/backups/runs" view a delete request
// (handleBackupRunDelete) came from, so the refreshed fragment it returns
// matches the view the row was deleted from rather than resetting to the
// unfiltered first page. This works because both the filter bar and "Load
// more" set hx-push-url="true" (pages/backups.templ), keeping the browser's
// address bar -- and therefore the Referer header a same-page htmx request
// carries -- in sync with the current filter and page_token. A missing or
// unparsable Referer degrades to the unfiltered first page rather than
// failing the delete itself.
func backupRunsFilterFromReferer(r *http.Request) BackupListFilter {
	u, err := url.Parse(r.Referer())
	if err != nil {
		return BackupListFilter{}
	}
	return backupRunsFilterFromQuery(u.Query())
}

func backupRunsFilterFromQuery(q url.Values) BackupListFilter {
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
// carries it forward. listErr non-nil means the ListBackups call itself
// failed (resp is nil): the fragment renders an inline error alert instead
// of the table rather than the caller returning a bare 500.
func backupRunsFragmentData(resp *manmanpb.ListBackupsResponse, filter BackupListFilter, listErr error) pages.BackupRunsFragmentData {
	data := pages.BackupRunsFragmentData{
		Filter: backupRunsFilterValues(filter),
	}
	if listErr != nil {
		data.Err = "Failed to load backup runs. Try again."
		return data
	}
	data.Items = resp.Items
	data.NextPageToken = resp.NextPageToken
	return data
}

// backupRunsFilterOptions resolves the filter bar's name-based picker
// options (Implementation-phase refinement of the raw ID inputs Scaffold
// shipped): every deployment (SGC) fleet-wide, via the same
// ListServers -> per-server ListServerGameConfigs -> Game/GameConfig-name
// resolution handleActivity already established (handlers_activity.go's
// resolveFleetWideActivitySet/activityDisplayNames), plus every volume and
// BackupConfig fleet-wide via ListBackupConfigItems (#2809's
// BackupConfigListItem already carries volume/game-config display names,
// so no extra per-row RPC is needed).
//
// A resolution failure degrades to an empty option list for that one
// picker (logged as a WARNING, matching resolveFleetWideActivitySet's own
// per-server degrade posture) rather than failing the whole "/backups"
// page -- the actual row data comes from a separate ListBackups call that
// has already succeeded or failed on its own by the time this runs.
func (app *App) backupRunsFilterOptions(ctx context.Context) pages.BackupRunsFilterOptions {
	var options pages.BackupRunsFilterOptions

	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("WARNING: backups: failed to list servers for filter options: %v", err)
	} else {
		sgcByID, serverByID := app.resolveFleetWideActivitySet(ctx, servers)
		gameConfigByID, _ := app.activityDisplayNames(ctx, sgcByID)
		for _, sgc := range sgcByID {
			server := serverByID[sgc.ServerId]
			gameConfig := gameConfigByID[sgc.GameConfigId]
			if server == nil || gameConfig == nil {
				continue
			}
			options.ServerGameConfigs = append(options.ServerGameConfigs, components.SelectOption{
				Value: strconv.FormatInt(sgc.ServerGameConfigId, 10),
				Label: fmt.Sprintf("%s / %s", server.Name, gameConfig.Name),
			})
		}
		sortSelectOptions(options.ServerGameConfigs)
	}

	items, err := app.grpc.ListBackupConfigItems(ctx)
	if err != nil {
		log.Printf("WARNING: backups: failed to list backup configs for filter options: %v", err)
		return options
	}
	seenVolumes := make(map[int64]bool, len(items))
	for _, item := range items {
		if item.Config == nil {
			continue
		}
		options.BackupConfigs = append(options.BackupConfigs, components.SelectOption{
			Value: strconv.FormatInt(item.Config.BackupConfigId, 10),
			Label: fmt.Sprintf("%s / %s", item.GameConfigName, item.VolumeName),
		})
		if !seenVolumes[item.Config.VolumeId] {
			seenVolumes[item.Config.VolumeId] = true
			options.Volumes = append(options.Volumes, components.SelectOption{
				Value: strconv.FormatInt(item.Config.VolumeId, 10),
				Label: fmt.Sprintf("%s (%s)", item.VolumeName, item.GameConfigName),
			})
		}
	}
	sortSelectOptions(options.BackupConfigs)
	sortSelectOptions(options.Volumes)
	return options
}

// sortSelectOptions orders a filter picker's options alphabetically by
// label so the dropdown is scannable regardless of the fleet-wide
// aggregation order (map iteration for SGCs, list order for BackupConfigs/
// volumes).
func sortSelectOptions(opts []components.SelectOption) {
	sort.Slice(opts, func(i, j int) bool { return opts[i].Label < opts[j].Label })
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

// handleBackupTriggerForm serves "/backups/trigger-form" (task #2813, FR5):
// the "Trigger backup" Blade's own lazy hx-get on open
// (BackupTriggerFormPlaceholder), and the BackupConfig picker's onchange
// re-fetch once an operator selects one. Both cases render the same
// pages.BackupTriggerForm, built by buildBackupTriggerFormData -- the only
// difference is whether a backup_config_id query param is present.
func (app *App) handleBackupTriggerForm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	data, err := app.buildBackupTriggerFormData(ctx, r.URL.Query().Get("backup_config_id"), 0, "")
	if err != nil {
		log.Printf("ERROR: backups: failed to build trigger form: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	if err := pages.BackupTriggerForm(data).Render(ctx, w); err != nil {
		log.Printf("ERROR: backups: failed to render trigger form: %v", err)
	}
}

// handleBackupTrigger serves "POST /backups/trigger" (task #2813, FR5): the
// fleet-wide entry point to the existing TriggerBackup RPC
// (manmanv2/api/handlers/backup_config.go) -- a well-formed request with
// both ids calls it and re-renders the form with the outcome; a
// malformed/missing id is rejected before ever calling the RPC (400).
//
// Every outcome, success or failure, responds 200 with the form re-derived
// from the submitted backup_config_id (same shape buildDeploymentRowData's
// callers use elsewhere in this UI: never an assumed-success render).
// A successful trigger additionally writes an out-of-band refresh of the
// fleet run list (pages.BackupRunsPanelOOB) after the primary response, so
// the new "pending" row appears on the underlying "/backups" page without
// it ever being re-fetched or navigated (FR10's Blade contract).
func (app *App) handleBackupTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	sgcID, sgcErr := strconv.ParseInt(r.FormValue("server_game_config_id"), 10, 64)
	backupConfigID, bcErr := strconv.ParseInt(r.FormValue("backup_config_id"), 10, 64)
	if sgcErr != nil || bcErr != nil || sgcID <= 0 || backupConfigID <= 0 {
		http.Error(w, "server_game_config_id and backup_config_id are required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	var successID int64
	var triggerErr string
	backupID, err := app.grpc.TriggerBackup(ctx, sgcID, backupConfigID)
	if err != nil {
		log.Printf("ERROR: backups: failed to trigger backup for deployment %d, backup config %d: %v", sgcID, backupConfigID, err)
		triggerErr = backupTriggerErrorMessage(err)
	} else {
		successID = backupID
	}

	formData, err := app.buildBackupTriggerFormData(ctx, strconv.FormatInt(backupConfigID, 10), successID, triggerErr)
	if err != nil {
		log.Printf("ERROR: backups: failed to rebuild trigger form: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	if err := pages.BackupTriggerForm(formData).Render(ctx, w); err != nil {
		log.Printf("ERROR: backups: failed to render trigger form: %v", err)
		return
	}

	if successID > 0 {
		resp, listErr := app.grpc.ListBackups(ctx, BackupListFilter{})
		if listErr != nil {
			log.Printf("WARNING: backups: failed to refresh fleet run list after trigger: %v", listErr)
		}
		oobData := backupRunsFragmentData(resp, BackupListFilter{}, listErr)
		if err := pages.BackupRunsPanelOOB(oobData).Render(ctx, w); err != nil {
			log.Printf("ERROR: backups: failed to render fleet run list refresh: %v", err)
		}
	}
}

// buildBackupTriggerFormData assembles pages.BackupTriggerFormData: the
// fleet-wide BackupConfig list (#2810's ListBackupConfigItems) always, and
// -- once backupConfigIDStr names a real BackupConfig -- the deployments
// (SGCs) eligible for it, i.e. every SGC whose GameConfigId matches that
// BackupConfig's own game config (BackupConfigListItem.GameConfigId). Reuses
// resolveFleetWideActivitySet (handlers_activity.go) for the SGC/server
// join rather than re-deriving it, matching backupRunsFilterOptions' own
// fleet-wide-SGC posture just above.
func (app *App) buildBackupTriggerFormData(ctx context.Context, backupConfigIDStr string, successBackupID int64, triggerErr string) (pages.BackupTriggerFormData, error) {
	items, err := app.grpc.ListBackupConfigItems(ctx)
	if err != nil {
		return pages.BackupTriggerFormData{}, fmt.Errorf("failed to list backup configs: %w", err)
	}

	data := pages.BackupTriggerFormData{
		SelectedBackupConfigID: backupConfigIDStr,
		SuccessBackupID:        successBackupID,
		Err:                    triggerErr,
	}

	itemByBackupConfigID := make(map[int64]*manmanpb.BackupConfigListItem, len(items))
	for _, item := range items {
		if item.Config == nil {
			continue
		}
		itemByBackupConfigID[item.Config.BackupConfigId] = item
		data.BackupConfigs = append(data.BackupConfigs, components.SelectOption{
			Value: strconv.FormatInt(item.Config.BackupConfigId, 10),
			Label: fmt.Sprintf("%s / %s", item.GameConfigName, item.VolumeName),
		})
	}
	sortSelectOptions(data.BackupConfigs)

	backupConfigID, parseErr := strconv.ParseInt(backupConfigIDStr, 10, 64)
	if parseErr != nil || backupConfigID <= 0 {
		return data, nil
	}
	selected, ok := itemByBackupConfigID[backupConfigID]
	if !ok {
		return data, nil
	}

	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("WARNING: backups: failed to list servers for trigger picker: %v", err)
		return data, nil
	}
	sgcByID, serverByID := app.resolveFleetWideActivitySet(ctx, servers)
	for _, sgc := range sgcByID {
		if sgc.GameConfigId != selected.GameConfigId {
			continue
		}
		server := serverByID[sgc.ServerId]
		if server == nil {
			continue
		}
		data.ServerGameConfigs = append(data.ServerGameConfigs, components.SelectOption{
			Value: strconv.FormatInt(sgc.ServerGameConfigId, 10),
			Label: fmt.Sprintf("%s / %s", server.Name, selected.GameConfigName),
		})
	}
	sortSelectOptions(data.ServerGameConfigs)
	return data, nil
}

// backupTriggerErrorMessage turns a TriggerBackup error into a
// human-readable inline message (FR5's "readable inline message, not a
// generic 500" requirement) rather than leaking raw gRPC status text.
// FailedPrecondition's "no session found for SGC"
// (api/handlers/backup_config.go) is the common, expected case -- an
// operator picked a deployment with nothing currently running -- and gets
// its own actionable, deployment-first wording (NFR2); any other
// FailedPrecondition/NotFound still surfaces the API's own message rather
// than a generic fallback, since those are already server-authored
// sentences, not raw transport text.
func backupTriggerErrorMessage(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return "Failed to trigger backup. Try again."
	}
	switch st.Code() {
	case codes.FailedPrecondition:
		if strings.Contains(st.Message(), "no session found for SGC") {
			return "No running session for this deployment -- start it before triggering a backup."
		}
		return st.Message()
	case codes.NotFound:
		return st.Message()
	default:
		return "Failed to trigger backup. Try again."
	}
}
