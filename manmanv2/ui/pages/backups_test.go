package pages

import (
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// This file guards task #2812 (root plan #2777, M7 -- FR1, FR3, FR4): the
// fleet-wide "/backups" surface's "Backup runs" tab, specifically the
// rendering rules that don't have their own gRPC round trip to fail on --
// trigger-origin display (FR2/FR3's "never guess unknown as manual" rule),
// completed-only size rendering, and pagination query-string composition.

// --- backupRunOrigin: FR2/FR3's core rule -----------------------------------

func TestBackupRunOrigin_RendersEachSourceDistinctly(t *testing.T) {
	cases := []struct {
		name          string
		triggerSource string
		want          string
	}{
		{"scheduled", "scheduled", "scheduled"},
		{"manual", "manual", "manual"},
		{"unknown explicit", "unknown", "unknown"},
		{"empty (pre-#2809 historical row)", "", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := backupRunOrigin(&manmanpb.Backup{TriggerSource: tc.triggerSource})
			if got != tc.want {
				t.Errorf("backupRunOrigin(trigger_source=%q) = %q, want %q", tc.triggerSource, got, tc.want)
			}
		})
	}
}

// TestBackupRunOrigin_UnknownNeverRendersAsManual is FR2/FR3's core rule
// asserted explicitly and in isolation, matching the issue's Testing
// section wording: an unknown-origin row must never be mistaken for a
// manual one.
//
// mutation-tested (verified red, by hand, then reverted): temporarily
// changing backupRunOrigin's empty-string branch to `return "manual"`
// made this test fail with `backupRunOrigin("") = "manual", want "unknown"
// (never "manual")`; reverting to `return "unknown"` restored green.
func TestBackupRunOrigin_UnknownNeverRendersAsManual(t *testing.T) {
	for _, triggerSource := range []string{"", "unknown"} {
		got := backupRunOrigin(&manmanpb.Backup{TriggerSource: triggerSource})
		if got == "manual" {
			t.Fatalf("backupRunOrigin(%q) = %q, want %q (never %q)", triggerSource, got, "unknown", "manual")
		}
		if got != "unknown" {
			t.Fatalf("backupRunOrigin(%q) = %q, want %q", triggerSource, got, "unknown")
		}
	}
}

// --- backupRunSize: "size once completed" (FR3) -----------------------------

func TestBackupRunSize_OnlyRendersOnceCompleted(t *testing.T) {
	cases := []struct {
		name   string
		backup *manmanpb.Backup
		want   string
	}{
		{"pending, no size yet", &manmanpb.Backup{Status: "pending", SizeBytes: 0}, "-"},
		{"running, no size yet", &manmanpb.Backup{Status: "running", SizeBytes: 0}, "-"},
		{"failed, never got a size", &manmanpb.Backup{Status: "failed", SizeBytes: 0}, "-"},
		{"completed with a stale zero size", &manmanpb.Backup{Status: "completed", SizeBytes: 0}, "-"},
		{"completed with a real size", &manmanpb.Backup{Status: "completed", SizeBytes: 2097152}, "2.0 MB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := backupRunSize(tc.backup); got != tc.want {
				t.Errorf("backupRunSize(status=%q, size_bytes=%d) = %q, want %q", tc.backup.Status, tc.backup.SizeBytes, got, tc.want)
			}
		})
	}
}

// --- nextPageQuery: paging composes with filtering (NFR4) -------------------

func TestNextPageQuery_CarriesActiveFiltersAndToken(t *testing.T) {
	filter := BackupRunsFilterValues{
		ServerGameConfigID: "55",
		VolumeID:           "501",
		BackupConfigID:     "1",
		Status:             "completed",
	}
	got := nextPageQuery(filter, "tok-123")
	for _, want := range []string{
		"server_game_config_id=55",
		"volume_id=501",
		"backup_config_id=1",
		"status=completed",
		"page_token=tok-123",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("nextPageQuery(%+v, %q) = %q, want it to contain %q", filter, "tok-123", got, want)
		}
	}
}

func TestNextPageQuery_OmitsUnsetFilters(t *testing.T) {
	got := nextPageQuery(BackupRunsFilterValues{}, "tok-123")
	for _, unwanted := range []string{"server_game_config_id=", "volume_id=", "backup_config_id=", "status="} {
		if strings.Contains(got, unwanted) {
			t.Errorf("nextPageQuery(empty filter, %q) = %q, want no %q param", "tok-123", got, unwanted)
		}
	}
	if !strings.Contains(got, "page_token=tok-123") {
		t.Errorf("nextPageQuery(empty filter, %q) = %q, want page_token carried forward", "tok-123", got)
	}
}

// --- BackupRunsFragment: empty/error state render in-page, not a 500 -------

func TestBackupRunsFragment_ErrorStateRendersInline(t *testing.T) {
	body := renderPage(t, BackupRunsFragment(BackupRunsFragmentData{Err: "Failed to load backup runs. Try again."}))
	if !strings.Contains(body, "Failed to load backup runs. Try again.") {
		t.Errorf("expected the inline error message in the fragment, got: %s", body)
	}
	if strings.Contains(body, "<table") {
		t.Errorf("expected no table alongside the error state, got: %s", body)
	}
	if strings.Contains(body, "Load more") {
		t.Errorf("expected no pagination control alongside the error state, got: %s", body)
	}
}

func TestBackupRunsFragment_EmptyStateRendersWhenNoItems(t *testing.T) {
	body := renderPage(t, BackupRunsFragment(BackupRunsFragmentData{Items: nil}))
	if !strings.Contains(body, "No backup runs") {
		t.Errorf("expected the empty state, got: %s", body)
	}
	if strings.Contains(body, "<table") {
		t.Errorf("expected no table alongside the empty state, got: %s", body)
	}
}

func TestBackupRunsFragment_NextPageControlOnlyWhenTokenReturned(t *testing.T) {
	item := &manmanpb.BackupListItem{
		Backup:              &manmanpb.Backup{BackupId: 1, Status: "completed", TriggerSource: "manual"},
		ServerGameConfigName: "Alpha / Survival",
		VolumeName:           "World Data",
	}

	withToken := renderPage(t, BackupRunsFragment(BackupRunsFragmentData{Items: []*manmanpb.BackupListItem{item}, NextPageToken: "tok-123"}))
	if !strings.Contains(withToken, "Load more") {
		t.Errorf("expected a Load more control when next_page_token is set, got: %s", withToken)
	}
	if !strings.Contains(withToken, "page_token=tok-123") {
		t.Errorf("expected the Load more control to carry the next page token, got: %s", withToken)
	}

	withoutToken := renderPage(t, BackupRunsFragment(BackupRunsFragmentData{Items: []*manmanpb.BackupListItem{item}, NextPageToken: ""}))
	if strings.Contains(withoutToken, "Load more") {
		t.Errorf("expected no Load more control when next_page_token is empty, got: %s", withoutToken)
	}
}
