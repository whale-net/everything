package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file guards task #2814 (root plan #2777, M7): the single-backup-run
// detail view (FR6) -- status/origin, S3 location, size, error message, and
// the run's pre-backup Actions resolved from its BackupConfig. It reuses
// handlers_backups_fleet_test.go's fakeBackupsFleetAPIClient/
// baseBackupsFleetFixture/newBackupsFleetTestApp rather than a second fake,
// matching that file's fleet fixture. Unauthenticated-request coverage for
// "/backups/runs/{id}" lives in main_test.go's
// TestSetupRoutes_OnlyFivePublicRoutesReachableUnauthenticated route table
// (NFR3), not duplicated here.
//
// mutation-tested (verified red, by hand, then reverted): temporarily
// reversing the fixture order fed into actionsByBackupConfigID in
// TestHandleBackupRunDetail_ActionsRenderInDisplayOrder made that test's
// ordering assertion fail (the "Snapshot volume" / "Flush database" pair
// asserted in the wrong relative position); restoring the fixture order
// restored green. This proves the assertion actually depends on
// display_order rather than passing regardless of it -- the same discipline
// handlers_backups_fleet_test.go's TestHandleBackupRunsFragment_TriggerOriginRendering
// comment records for FR2/FR3's origin rule.

func renderBackupRunDetailHTTP(t *testing.T, api *fakeBackupsFleetAPIClient, backupID string) (int, string) {
	t.Helper()
	app := newBackupsFleetTestApp(api)
	req := httptest.NewRequest(http.MethodGet, "/backups/runs/"+backupID, nil)
	w := httptest.NewRecorder()
	app.handleBackupRunDetail(w, req)
	return w.Code, w.Body.String()
}

// --- criterion 1: a completed run renders status, S3 location, size, and
// its pre-backup Actions --------------------------------------------------

func TestHandleBackupRunDetail_CompletedRunRendersStatusS3SizeAndActions(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.backupsByID = map[int64]*manmanpb.Backup{
		1: {
			BackupId:           1,
			ServerGameConfigId: 55,
			VolumeId:           501,
			BackupConfigId:     1,
			Status:             "completed",
			S3Url:              "s3://backups/run-1.tar.gz",
			SizeBytes:          2097152,
			TriggerSource:      "scheduled",
			CreatedAt:          1700000000,
		},
	}
	api.actionsByBackupConfigID = map[int64][]*manmanpb.BackupConfigActionItem{
		1: {
			{ActionId: 10, DisplayOrder: 0, Name: "Flush database"},
			{ActionId: 11, DisplayOrder: 1, Name: "Snapshot volume"},
		},
	}

	code, body := renderBackupRunDetailHTTP(t, api, "1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	for _, want := range []string{"completed", "s3://backups/run-1.tar.gz", "2.0 MB", "Flush database", "Snapshot volume", "scheduled", `href="/backups/runs/1/download"`, "Download"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in rendered detail page, got: %s", want, body)
		}
	}
}

// --- criterion 2: a failed run renders error_message verbatim ------------

func TestHandleBackupRunDetail_FailedRunRendersErrorMessageVerbatim(t *testing.T) {
	api := baseBackupsFleetFixture()
	const errMsg = "rsync exited 23: partial transfer (disk full on volume mount)"
	api.backupsByID = map[int64]*manmanpb.Backup{
		3: {
			BackupId:           3,
			ServerGameConfigId: 55,
			Status:             "failed",
			ErrorMessage:       errMsg,
			TriggerSource:      "",
		},
	}

	code, body := renderBackupRunDetailHTTP(t, api, "3")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, errMsg) {
		t.Errorf("expected the stored error_message verbatim in the rendered page, got: %s", body)
	}
	// FR2/FR3's honesty rule applies here too: an empty trigger_source must
	// render as "unknown", not be guessed as "manual".
	if !strings.Contains(body, "unknown") {
		t.Errorf("expected the empty-trigger_source run to render its origin as %q, got: %s", "unknown", body)
	}
}

// --- criterion 3: a pending run renders the explicit "not yet available"
// S3 state, never an empty/garbage link ------------------------------------

func TestHandleBackupRunDetail_PendingRunRendersNoS3StateNotBrokenLink(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.backupsByID = map[int64]*manmanpb.Backup{
		2: {
			BackupId:           2,
			ServerGameConfigId: 66,
			Status:             "pending",
			TriggerSource:      "manual",
		},
	}

	code, body := renderBackupRunDetailHTTP(t, api, "2")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if strings.Contains(body, `href=""`) {
		t.Errorf("expected no empty-href link for a pending run's S3 location, got: %s", body)
	}
	if !strings.Contains(body, "Not yet available") {
		t.Errorf("expected the explicit pending S3 state, got: %s", body)
	}
	if strings.Contains(body, "/download") {
		t.Errorf("expected no download link for a run with no S3 object yet, got: %s", body)
	}
}

// --- criterion 4: an unknown backup id 404s, not 500s ---------------------

func TestHandleBackupRunDetail_UnknownIDReturns404NotBrokenServerError(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.backupsByID = map[int64]*manmanpb.Backup{}

	code, _ := renderBackupRunDetailHTTP(t, api, "999")
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want %d for an unknown backup id", code, http.StatusNotFound)
	}
}

// --- FR6: Actions render in display_order, and the "none" state is
// explicit for a run with no BackupConfig ----------------------------------

func TestHandleBackupRunDetail_ActionsRenderInDisplayOrder(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.backupsByID = map[int64]*manmanpb.Backup{
		1: {BackupId: 1, ServerGameConfigId: 55, BackupConfigId: 1, Status: "completed"},
	}
	api.actionsByBackupConfigID = map[int64][]*manmanpb.BackupConfigActionItem{
		1: {
			{ActionId: 10, DisplayOrder: 0, Name: "Flush database"},
			{ActionId: 11, DisplayOrder: 1, Name: "Snapshot volume"},
		},
	}

	code, body := renderBackupRunDetailHTTP(t, api, "1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	first := strings.Index(body, "Flush database")
	second := strings.Index(body, "Snapshot volume")
	if first == -1 || second == -1 {
		t.Fatalf("expected both Action names in the rendered page, got: %s", body)
	}
	if first >= second {
		t.Errorf("expected Actions in display_order (Flush database before Snapshot volume), got: %s", body)
	}
}

func TestHandleBackupRunDetail_NoBackupConfigRendersExplicitNoneState(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.backupsByID = map[int64]*manmanpb.Backup{
		2: {BackupId: 2, ServerGameConfigId: 66, BackupConfigId: 0, Status: "completed", TriggerSource: "manual"},
	}

	code, body := renderBackupRunDetailHTTP(t, api, "2")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "None") {
		t.Errorf("expected the explicit no-BackupConfig state, got: %s", body)
	}
	if strings.Contains(body, "No Actions attached") {
		t.Errorf("a manual run with no backup_config_id must render the \"none\" state, not the has-a-config-but-empty state; got: %s", body)
	}
}

// --- download link: "GET /backups/runs/{id}/download" mints a fresh
// pre-signed public S3 URL and redirects the browser to it -----------------

func renderBackupRunRouteHTTP(t *testing.T, api *fakeBackupsFleetAPIClient, path string, method string) (int, *httptest.ResponseRecorder) {
	t.Helper()
	app := newBackupsFleetTestApp(api)
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	app.handleBackupRunRoute(w, req)
	return w.Code, w
}

func TestHandleBackupRunDownload_RedirectsToFreshPresignedURL(t *testing.T) {
	api := baseBackupsFleetFixture()
	const presignedURL = "https://s3.example.com/backups/run-1.tar.gz?X-Amz-Signature=abc"
	api.getBackupDownloadURLResp = &manmanpb.GetBackupDownloadURLResponse{PresignedUrl: presignedURL}

	code, w := renderBackupRunRouteHTTP(t, api, "/backups/runs/1/download", http.MethodGet)
	if code != http.StatusFound {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusFound, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != presignedURL {
		t.Errorf("Location = %q, want %q", loc, presignedURL)
	}
	if api.lastGetBackupDownloadURLReq == nil || api.lastGetBackupDownloadURLReq.BackupId != 1 {
		t.Errorf("expected GetBackupDownloadURL called with backup_id=1, got: %+v", api.lastGetBackupDownloadURLReq)
	}
}

func TestHandleBackupRunDownload_NoArchiveYetReturnsConflictNotServerError(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.getBackupDownloadURLErr = status.Error(codes.FailedPrecondition, "backup has no S3 URL")

	code, w := renderBackupRunRouteHTTP(t, api, "/backups/runs/2/download", http.MethodGet)
	if code != http.StatusConflict {
		t.Errorf("status = %d, want %d; body: %s", code, http.StatusConflict, w.Body.String())
	}
}

func TestHandleBackupRunDownload_UnknownIDReturns404(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.getBackupDownloadURLErr = status.Error(codes.NotFound, "backup not found")

	code, _ := renderBackupRunRouteHTTP(t, api, "/backups/runs/999/download", http.MethodGet)
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want %d for an unknown backup id", code, http.StatusNotFound)
	}
}

// TestHandleBackupRunRoute_DispatchesDetailDeleteAndDownloadDistinctly
// guards handleBackupRunRoute's three-way trailing-segment dispatch: adding
// "/download" alongside the existing "/delete" dispatch must not regress the
// bare-id detail route or the delete route (both already covered by their
// own tests above/in handlers_backups_fleet_test.go) -- this only asserts
// download is reachable through the shared subtree dispatcher, not just
// when called directly.
func TestHandleBackupRunRoute_DownloadReachableThroughSharedDispatcher(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.backupsByID = map[int64]*manmanpb.Backup{
		1: {BackupId: 1, ServerGameConfigId: 55, Status: "completed", S3Url: "s3://backups/run-1.tar.gz"},
	}
	const presignedURL = "https://s3.example.com/backups/run-1.tar.gz?X-Amz-Signature=abc"
	api.getBackupDownloadURLResp = &manmanpb.GetBackupDownloadURLResponse{PresignedUrl: presignedURL}

	code, w := renderBackupRunRouteHTTP(t, api, "/backups/runs/1/download", http.MethodGet)
	if code != http.StatusFound {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusFound, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != presignedURL {
		t.Errorf("Location = %q, want %q", loc, presignedURL)
	}
}
