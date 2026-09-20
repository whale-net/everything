package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
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

	pageData := pages.BackupsPageData{
		Layout:        layoutData,
		Runs:          backupRunsFragmentData(resp, filter, listErr),
		FilterOptions: app.backupRunsFilterOptions(ctx),
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
