package workshop

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/whale-net/everything/manmanv2/host/rmq"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// buildSingleFileTar produces an uncompressed tar archive (the wire format
// CacheClient.TryFetch expects, see cache_client.go's cacheObjectFormat) containing a
// single regular file, so orchestrator-level cache-hit tests can exercise the real
// fetch/extract/install path end to end without depending on cache_client_test.go's
// own (unexported-to-this-file) test helpers.
func buildSingleFileTar(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(content))}))
	_, err := tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

// MockInstallationStatusPublisher is a mock implementation for testing. It also
// implements WorkshopCachePublisher (#2184) so tests can assert on the new
// verified_unchanged/refreshed/populated status publishes alongside the pre-existing
// installation-status ones -- see publishWorkshopCacheStatus's doc comment for why that
// method type-asserts against a separate interface rather than requiring every
// InstallationStatusPublisher to implement it.
type MockInstallationStatusPublisher struct {
	updates      []*rmq.InstallationStatusUpdate
	cacheUpdates []*rmq.WorkshopCacheStatusUpdate
}

func (m *MockInstallationStatusPublisher) PublishInstallationStatus(ctx context.Context, update *rmq.InstallationStatusUpdate) error {
	m.updates = append(m.updates, update)
	return nil
}

func (m *MockInstallationStatusPublisher) PublishWorkshopCacheStatus(ctx context.Context, update *rmq.WorkshopCacheStatusUpdate) error {
	m.cacheUpdates = append(m.cacheUpdates, update)
	return nil
}

func TestNewDownloadOrchestrator(t *testing.T) {
	mockPublisher := &MockInstallationStatusPublisher{}

	orchestrator := NewDownloadOrchestrator(
		nil,             // dockerClient
		nil,             // grpcClient
		nil,             // workshopClient
		1,               // serverID
		"test",          // environment
		"/tmp/test",     // hostDataDir
		"/var/lib/test", // internalDataDir
		3,               // maxConcurrent
		mockPublisher,
	)

	assert.NotNil(t, orchestrator)
	assert.Equal(t, int64(1), orchestrator.serverID)
	assert.Equal(t, "test", orchestrator.environment)
	assert.Equal(t, "/tmp/test", orchestrator.hostDataDir)
	assert.Equal(t, 3, orchestrator.maxConcurrent)
	assert.NotNil(t, orchestrator.semaphore)
	assert.NotNil(t, orchestrator.inProgressDownloads)
}

func TestGetDownloadContainerName(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		sgcID       int64
		addonID     int64
		expected    string
	}{
		{
			name:        "with environment",
			environment: "dev",
			sgcID:       123,
			addonID:     456,
			expected:    "workshop-download-dev-123-456",
		},
		{
			name:        "without environment",
			environment: "",
			sgcID:       123,
			addonID:     456,
			expected:    "workshop-download-123-456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orchestrator := &DownloadOrchestrator{
				environment: tt.environment,
			}

			result := orchestrator.getDownloadContainerName(tt.sgcID, tt.addonID)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetSGCHostDir(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		hostDataDir string
		sgcID       int64
		expected    string
	}{
		{
			name:        "with environment",
			environment: "dev",
			hostDataDir: "/var/lib/manman",
			sgcID:       123,
			expected:    "/var/lib/manman/sgc-dev-123",
		},
		{
			name:        "without environment",
			environment: "",
			hostDataDir: "/var/lib/manman",
			sgcID:       123,
			expected:    "/var/lib/manman/sgc-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orchestrator := &DownloadOrchestrator{
				environment: tt.environment,
				hostDataDir: tt.hostDataDir,
			}

			result := orchestrator.getSGCHostDir(tt.sgcID)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildSteamCMDCommand(t *testing.T) {
	orchestrator := &DownloadOrchestrator{}

	// steamcmd/steamcmd image has ENTRYPOINT ["steamcmd"], so args are passed directly
	cmd := orchestrator.buildSteamCMDCommand("550", "123456789", "/data/mods")

	assert.Equal(t, []string{
		"+force_install_dir", "/data/mods",
		"+login", "anonymous",
		"+workshop_download_item", "550", "123456789",
		"+quit",
	}, cmd)
}

func TestParseProgress(t *testing.T) {
	tests := []struct {
		name     string
		logLine  string
		expected int
	}{
		{
			name:     "valid progress",
			logLine:  "Downloading item 123456 ... 45%",
			expected: 45,
		},
		{
			name:     "100 percent",
			logLine:  "Download complete 100%",
			expected: 100,
		},
		{
			name:     "no progress",
			logLine:  "Starting download...",
			expected: 0,
		},
		{
			name:     "multiple percentages - takes first",
			logLine:  "Progress: 25% of 100%",
			expected: 25,
		},
	}

	orchestrator := &DownloadOrchestrator{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := orchestrator.parseProgress(tt.logLine)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInProgressTracking(t *testing.T) {
	orchestrator := NewDownloadOrchestrator(
		nil, nil, nil, 1, "test", "/tmp", "/var/lib/test", 3, &MockInstallationStatusPublisher{},
	)

	// Initially not in progress; checkAndMark returns false and marks it
	assert.False(t, orchestrator.checkAndMarkDownloadInProgress(123))

	// Already marked; checkAndMark returns true without double-marking
	assert.True(t, orchestrator.checkAndMarkDownloadInProgress(123))

	// Mark as complete
	orchestrator.markDownloadComplete(123)
	// After completion, should be free again
	assert.False(t, orchestrator.checkAndMarkDownloadInProgress(123))
}

// --- #2184 verify-then-cache-or-refresh coverage ----------------------------
//
// #2183's cache-hit/miss tests originally exercised HandleDownloadCommand with
// dockerClient left nil, relying on a nil-dereference panic to prove the SteamCMD
// download path was (or wasn't) reached. #2184 inserts a live VerifyWorkshopItem call
// (which itself drives a SteamCMD container) ahead of that gate, so dockerClient can no
// longer be nil for any scenario that reaches HandleDownloadCommand at all -- the tests
// below use fakeDockerClient (fake_docker_test.go) instead, which lets both the verify
// and the ordinary download/upload SteamCMD lifecycles run against an in-memory fake
// rather than a live Docker daemon.
//
// mutation-tested (verified red, by hand, then reverted):
//   - Changing HandleDownloadCommand's `if !changed {` gate to always attempt a cache
//     read (i.e. attempting tryServeFromCache even when verify reports changed) made
//     TestHandleDownloadCommand_VerifyChanged_UploadsRefreshed fail on its "no
//     GetCacheDownloadURL call" assertion.
//   - Making tryServeFromCache's final copyDirectory call into a no-op (so cached
//     content is "fetched" but never actually installed) made
//     TestHandleDownloadCommand_CacheHit_SkipsSteamCMD's installed-content assertion
//     fail with "no such file or directory" -- proving that test really checks content
//     landed, not just that HandleDownloadCommand returned nil.
//   - Hardcoding uploadToCache's event to always publish "populated" (dropping the
//     `if changed` branch) made TestHandleDownloadCommand_VerifyChanged_UploadsRefreshed
//     fail its event-name assertion.
//
// Reverting each mutation restored green.

// fakeOrchestratorWorkshopClient embeds the nil WorkshopServiceClient interface and
// overrides only the RPCs HandleDownloadCommand's verify/cache-read/cache-write paths
// call -- GetAddon, GetCacheDownloadURL, ReportCacheRead, GetCacheUploadURL -- so any
// unexpected call panics loudly (same pattern as
// manmanv2/ui/handlers_sgc_test.go's fakeWorkshopServiceClient).
type fakeOrchestratorWorkshopClient struct {
	pb.WorkshopServiceClient

	addon        *pb.WorkshopAddon
	cacheHit     bool
	presignedURL string
	cacheEntryID int64

	reportCalls        []*pb.ReportCacheReadRequest
	cacheDownloadCalls []*pb.GetCacheDownloadURLRequest

	// upload-path fields (#2184 FR9 write side)
	uploadPresignedURL string
	uploadCacheEntryID int64
	uploadErr          error
	uploadCalls        []*pb.GetCacheUploadURLRequest
}

func (f *fakeOrchestratorWorkshopClient) GetAddon(ctx context.Context, in *pb.GetAddonRequest, opts ...grpc.CallOption) (*pb.GetAddonResponse, error) {
	return &pb.GetAddonResponse{Addon: f.addon}, nil
}

func (f *fakeOrchestratorWorkshopClient) GetCacheDownloadURL(ctx context.Context, in *pb.GetCacheDownloadURLRequest, opts ...grpc.CallOption) (*pb.GetCacheDownloadURLResponse, error) {
	f.cacheDownloadCalls = append(f.cacheDownloadCalls, in)
	return &pb.GetCacheDownloadURLResponse{
		CacheHit:     f.cacheHit,
		CacheEntryId: f.cacheEntryID,
		PresignedUrl: f.presignedURL,
	}, nil
}

func (f *fakeOrchestratorWorkshopClient) ReportCacheRead(ctx context.Context, in *pb.ReportCacheReadRequest, opts ...grpc.CallOption) (*pb.ReportCacheReadResponse, error) {
	f.reportCalls = append(f.reportCalls, in)
	return &pb.ReportCacheReadResponse{}, nil
}

func (f *fakeOrchestratorWorkshopClient) GetCacheUploadURL(ctx context.Context, in *pb.GetCacheUploadURLRequest, opts ...grpc.CallOption) (*pb.GetCacheUploadURLResponse, error) {
	f.uploadCalls = append(f.uploadCalls, in)
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	return &pb.GetCacheUploadURLResponse{
		CacheEntryId: f.uploadCacheEntryID,
		PresignedUrl: f.uploadPresignedURL,
	}, nil
}

// fakeOrchestratorManManClient embeds the nil ManManAPIClient interface and overrides
// only GetServerGameConfig/ListGameConfigVolumes -- the RPCs resolveInstallTarget
// (called from tryServeFromCache) needs to resolve a single bind-mount volume.
type fakeOrchestratorManManClient struct {
	pb.ManManAPIClient

	gameConfigID  int64
	containerPath string
	hostSubpath   string
	volumeName    string
}

func (f *fakeOrchestratorManManClient) GetServerGameConfig(ctx context.Context, in *pb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*pb.GetServerGameConfigResponse, error) {
	return &pb.GetServerGameConfigResponse{
		Config: &pb.ServerGameConfig{ServerGameConfigId: in.ServerGameConfigId, GameConfigId: f.gameConfigID},
	}, nil
}

func (f *fakeOrchestratorManManClient) ListGameConfigVolumes(ctx context.Context, in *pb.ListGameConfigVolumesRequest, opts ...grpc.CallOption) (*pb.ListGameConfigVolumesResponse, error) {
	return &pb.ListGameConfigVolumesResponse{
		Volumes: []*pb.GameConfigVolume{
			{
				VolumeId:      1,
				ConfigId:      f.gameConfigID,
				Name:          f.volumeName,
				ContainerPath: f.containerPath,
				HostSubpath:   f.hostSubpath,
				VolumeType:    "bind",
			},
		},
	}, nil
}

// newVerifyTestAddon returns a WorkshopAddon whose LastUpdated matches the "known
// content version" resolveVerifyPlan derives it from, for the workshop/steam app IDs
// every test below uses.
func newVerifyTestAddon() *pb.WorkshopAddon {
	return &pb.WorkshopAddon{
		AddonId:     123,
		WorkshopId:  "987654321",
		SteamAppId:  "550",
		LastUpdated: 17000000,
	}
}

// putHandler builds an httptest handler for uploadToCache's presigned PUT, returning the
// given status code and recording every uploaded body under a caller-provided pointer.
func putHandler(status int, capturedBody *[]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if capturedBody != nil {
			body, _ := io.ReadAll(r.Body)
			*capturedBody = body
		}
		w.WriteHeader(status)
	}
}

// TestHandleDownloadCommand_CacheHit_SkipsSteamCMD proves FR8's cost-saving claim: when
// verify reports the content is unchanged and control-api reports a cache hit at that
// version, the addon is served from S3 with no SteamCMD *download* container ever
// started (a verify container legitimately still runs -- see the file-level comment).
func TestHandleDownloadCommand_CacheHit_SkipsSteamCMD(t *testing.T) {
	const addonContent = "pretend addon vpk content"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// A single regular file at the tar root -- see cache_client_test.go's buildTar
		// for the general helper; inlined here since this file doesn't import
		// archive/tar for anything else.
		_, _ = w.Write(buildSingleFileTar(t, "addon.vpk", addonContent))
	}))
	defer srv.Close()

	workshopClient := &fakeOrchestratorWorkshopClient{
		addon:        newVerifyTestAddon(),
		cacheHit:     true,
		presignedURL: srv.URL,
		cacheEntryID: 42,
	}
	manManClient := &fakeOrchestratorManManClient{
		gameConfigID:  10,
		containerPath: "/data/mods",
		hostSubpath:   "mods",
		volumeName:    "mods",
	}
	publisher := &MockInstallationStatusPublisher{}

	tmpDir := t.TempDir()

	docker := newFakeDockerClient()
	docker.verifyContentVersions = map[string]string{"987654321": "17000000"} // matches LastUpdated -> unchanged

	orchestrator := NewDownloadOrchestrator(
		docker, manManClient, workshopClient, 1, "test", tmpDir, tmpDir, 3, publisher,
	)
	orchestrator.cacheClient = NewCacheClient(workshopClient, 1, srv.Client())

	cmd := &DownloadAddonCommand{
		InstallationID: 1,
		SGCID:          5,
		AddonID:        123,
		WorkshopID:     "987654321",
		SteamAppID:     "550",
		InstallPath:    "/data/mods",
	}

	err := orchestrator.HandleDownloadCommand(context.Background(), cmd)
	require.NoError(t, err)

	assert.Equal(t, 1, docker.verifyContainerCount(), "verify must still run to establish the current content version")
	assert.Equal(t, 0, docker.downloadContainerCount(), "a cache hit must never start a SteamCMD download container (FR8)")

	require.NotEmpty(t, publisher.updates)
	last := publisher.updates[len(publisher.updates)-1]
	assert.Equal(t, InstallationStatusInstalled, last.Status)
	assert.Equal(t, 100, last.ProgressPercent)

	// Content must have actually landed at the resolved bind-mount target -- not just
	// "no error returned" -- and FR10 presence must have been reported.
	installedPath := filepath.Join(tmpDir, "sgc-test-5", "mods", "addon.vpk")
	got, readErr := os.ReadFile(installedPath)
	require.NoError(t, readErr, "expected cached content at %s", installedPath)
	assert.Equal(t, addonContent, string(got))

	require.Len(t, workshopClient.reportCalls, 1)
	assert.Equal(t, int64(42), workshopClient.reportCalls[0].CacheEntryId)

	require.Len(t, publisher.cacheUpdates, 1)
	cacheUpdate := publisher.cacheUpdates[0]
	assert.Equal(t, "verified_unchanged", cacheUpdate.Event)
	assert.Equal(t, "17000000", cacheUpdate.ContentVersion)
	assert.Equal(t, int64(42), cacheUpdate.CacheEntryID)
}

// TestHandleDownloadCommand_CacheMiss_FallsThroughToSteamCMD proves that verify
// reporting "unchanged" but control-api reporting a cache miss (the ordinary case for a
// first-ever install of an addon whose metadata happens to already be in sync -- FR8's
// doc comment) falls through to the pre-existing full SteamCMD download, and that the
// resulting upload is published as "populated" (Testing bullet: "first-ever install
// (miss) -> download + upload + populated"), not "refreshed".
func TestHandleDownloadCommand_CacheMiss_FallsThroughToSteamCMD(t *testing.T) {
	var uploadedBody []byte
	putSrv := httptest.NewServer(putHandler(http.StatusOK, &uploadedBody))
	defer putSrv.Close()

	workshopClient := &fakeOrchestratorWorkshopClient{
		addon:              newVerifyTestAddon(),
		cacheHit:           false, // ordinary miss: no cache entry yet for this content version
		uploadPresignedURL: putSrv.URL,
		uploadCacheEntryID: 99,
	}
	manManClient := &fakeOrchestratorManManClient{
		gameConfigID:  10,
		containerPath: "/data/mods",
		hostSubpath:   "mods",
		volumeName:    "mods",
	}
	publisher := &MockInstallationStatusPublisher{}

	tmpDir := t.TempDir()

	docker := newFakeDockerClient()
	docker.verifyContentVersions = map[string]string{"987654321": "17000000"} // unchanged
	docker.downloadFiles = map[string]string{"addon.vpk": "downloaded content"}

	orchestrator := NewDownloadOrchestrator(
		docker, manManClient, workshopClient, 1, "test", tmpDir, tmpDir, 3, publisher,
	)

	cmd := &DownloadAddonCommand{
		InstallationID: 1,
		SGCID:          5,
		AddonID:        123,
		WorkshopID:     "987654321",
		SteamAppID:     "550",
		InstallPath:    "/data/mods",
	}

	err := orchestrator.HandleDownloadCommand(context.Background(), cmd)
	require.NoError(t, err)

	assert.Equal(t, 1, docker.downloadContainerCount(), "a cache miss must fall through to a full SteamCMD download")
	assert.Empty(t, workshopClient.reportCalls, "a miss must never report a cache read")

	require.NotEmpty(t, publisher.updates)
	last := publisher.updates[len(publisher.updates)-1]
	assert.Equal(t, InstallationStatusInstalled, last.Status)

	installedPath := filepath.Join(tmpDir, "sgc-test-5", "mods", "addon.vpk")
	got, readErr := os.ReadFile(installedPath)
	require.NoError(t, readErr, "expected downloaded content at %s", installedPath)
	assert.Equal(t, "downloaded content", string(got))

	require.Len(t, workshopClient.uploadCalls, 1)
	assert.Equal(t, "17000000", workshopClient.uploadCalls[0].ContentVersion)
	assert.NotEmpty(t, uploadedBody, "expected the packaged content to actually be uploaded")

	require.Len(t, publisher.cacheUpdates, 1)
	assert.Equal(t, "populated", publisher.cacheUpdates[0].Event)
	assert.Equal(t, int64(99), publisher.cacheUpdates[0].CacheEntryID)
}

// TestHandleDownloadCommand_VerifyChanged_UploadsRefreshed proves FR9's changed path:
// when verify reports a different content version than the addon's known one, no cache
// read is attempted at all (the stale version could never hit), a full download runs,
// and the upload is keyed to the *new* version and published as "refreshed".
func TestHandleDownloadCommand_VerifyChanged_UploadsRefreshed(t *testing.T) {
	var uploadedBody []byte
	putSrv := httptest.NewServer(putHandler(http.StatusOK, &uploadedBody))
	defer putSrv.Close()

	workshopClient := &fakeOrchestratorWorkshopClient{
		addon:              newVerifyTestAddon(), // LastUpdated == "17000000"
		uploadPresignedURL: putSrv.URL,
		uploadCacheEntryID: 7,
	}
	manManClient := &fakeOrchestratorManManClient{
		gameConfigID:  10,
		containerPath: "/data/mods",
		hostSubpath:   "mods",
		volumeName:    "mods",
	}
	publisher := &MockInstallationStatusPublisher{}

	tmpDir := t.TempDir()

	docker := newFakeDockerClient()
	docker.verifyContentVersions = map[string]string{"987654321": "17000099"} // differs -> changed
	docker.downloadFiles = map[string]string{"addon.vpk": "new content"}

	orchestrator := NewDownloadOrchestrator(
		docker, manManClient, workshopClient, 1, "test", tmpDir, tmpDir, 3, publisher,
	)

	cmd := &DownloadAddonCommand{
		InstallationID: 1,
		SGCID:          5,
		AddonID:        123,
		WorkshopID:     "987654321",
		SteamAppID:     "550",
		InstallPath:    "/data/mods",
	}

	err := orchestrator.HandleDownloadCommand(context.Background(), cmd)
	require.NoError(t, err)

	// A changed verify result must skip the cache-read RPC entirely -- reading at the
	// stale known version would only ever miss, and reading at the not-yet-resolved new
	// version isn't attempted either (the code always downloads fresh on change).
	assert.Empty(t, workshopClient.cacheDownloadCalls, "verify-changed must never attempt a cache read")
	assert.Equal(t, 1, docker.downloadContainerCount())

	require.Len(t, workshopClient.uploadCalls, 1)
	assert.Equal(t, "17000099", workshopClient.uploadCalls[0].ContentVersion, "the upload must be keyed to the new content version, not the stale known one")

	require.Len(t, publisher.cacheUpdates, 1)
	assert.Equal(t, "refreshed", publisher.cacheUpdates[0].Event)
	assert.Equal(t, "17000099", publisher.cacheUpdates[0].ContentVersion)

	// FR9: the prior version's entry/row/object is never touched by this flow. There is
	// no delete/overwrite RPC in WorkshopServiceClient for this write path at all --
	// fakeOrchestratorWorkshopClient embeds a nil interface, so any such call would have
	// panicked rather than silently succeeding. The only write RPC observed is the single
	// GetCacheUploadURL call asserted above, for the new version's key.
	require.Len(t, workshopClient.uploadCalls, 1)
}

// TestHandleDownloadCommand_UploadFails_NoStatusPublished proves NFR4's failed-upload
// guarantee at the host boundary: "a failed upload leaves no presence row and no claim
// that the entry is complete". Presence/cache-entry recording on control-api only ever
// happens in response to a WorkshopCacheStatusUpdate publish, so asserting no such
// publish occurred here is exactly what proves no presence row and no completeness claim
// resulted -- while the install itself, which had already succeeded locally, is
// unaffected.
func TestHandleDownloadCommand_UploadFails_NoStatusPublished(t *testing.T) {
	putSrv := httptest.NewServer(putHandler(http.StatusInternalServerError, nil))
	defer putSrv.Close()

	workshopClient := &fakeOrchestratorWorkshopClient{
		addon:              newVerifyTestAddon(),
		cacheHit:           false,
		uploadPresignedURL: putSrv.URL,
		uploadCacheEntryID: 55,
	}
	manManClient := &fakeOrchestratorManManClient{
		gameConfigID:  10,
		containerPath: "/data/mods",
		hostSubpath:   "mods",
		volumeName:    "mods",
	}
	publisher := &MockInstallationStatusPublisher{}

	tmpDir := t.TempDir()

	docker := newFakeDockerClient()
	docker.verifyContentVersions = map[string]string{"987654321": "17000000"}
	docker.downloadFiles = map[string]string{"addon.vpk": "downloaded content"}

	orchestrator := NewDownloadOrchestrator(
		docker, manManClient, workshopClient, 1, "test", tmpDir, tmpDir, 3, publisher,
	)

	cmd := &DownloadAddonCommand{
		InstallationID: 1,
		SGCID:          5,
		AddonID:        123,
		WorkshopID:     "987654321",
		SteamAppID:     "550",
		InstallPath:    "/data/mods",
	}

	err := orchestrator.HandleDownloadCommand(context.Background(), cmd)
	require.NoError(t, err, "an upload failure must never fail an install that already succeeded locally")

	require.NotEmpty(t, publisher.updates)
	last := publisher.updates[len(publisher.updates)-1]
	assert.Equal(t, InstallationStatusInstalled, last.Status)

	assert.Empty(t, publisher.cacheUpdates, "a failed upload must never be followed by a workshop cache status publish")
}

// TestHandleDownloadCommand_VerifyFailure_FallsBackToPlainDownload proves that a
// SteamCMD/RPC failure inside VerifyWorkshopItem never fails the install: it must fall
// back to an ordinary full download rather than surfacing the verify error.
func TestHandleDownloadCommand_VerifyFailure_FallsBackToPlainDownload(t *testing.T) {
	workshopClient := &fakeOrchestratorWorkshopClient{
		addon: newVerifyTestAddon(),
	}
	manManClient := &fakeOrchestratorManManClient{
		gameConfigID:  10,
		containerPath: "/data/mods",
		hostSubpath:   "mods",
		volumeName:    "mods",
	}
	publisher := &MockInstallationStatusPublisher{}

	tmpDir := t.TempDir()

	docker := newFakeDockerClient()
	docker.verifyExitCode = 1 // simulate a SteamCMD verify failure
	docker.downloadFiles = map[string]string{"addon.vpk": "downloaded content"}

	orchestrator := NewDownloadOrchestrator(
		docker, manManClient, workshopClient, 1, "test", tmpDir, tmpDir, 3, publisher,
	)

	cmd := &DownloadAddonCommand{
		InstallationID: 1,
		SGCID:          5,
		AddonID:        123,
		WorkshopID:     "987654321",
		SteamAppID:     "550",
		InstallPath:    "/data/mods",
	}

	err := orchestrator.HandleDownloadCommand(context.Background(), cmd)
	require.NoError(t, err, "a verify failure must never fail the install")

	assert.Equal(t, 1, docker.downloadContainerCount(), "a verify failure must fall back to a plain download")
	assert.Empty(t, workshopClient.cacheDownloadCalls, "a verify failure must skip the cache read entirely")

	require.NotEmpty(t, publisher.updates)
	last := publisher.updates[len(publisher.updates)-1]
	assert.Equal(t, InstallationStatusInstalled, last.Status)
}

// TestHandleDownloadCommand_ConcurrentSameKey_NoLockNoCorruption is the NFR4 regression
// test: two concurrent installs resolving to the same (workshopID, contentVersion) --
// simulating two hosts racing to populate the cache for the first time -- must both
// complete successfully and converge on the same cache_entry_id, with no distributed
// lock or coordination round-trip anywhere in this flow (LB8). fakeDockerClient and the
// httptest PUT server are the only shared state between the two goroutines, and both are
// safe for concurrent use without any of *this test's* synchronization standing in for a
// lock the production code doesn't have.
func TestHandleDownloadCommand_ConcurrentSameKey_NoLockNoCorruption(t *testing.T) {
	putSrv := httptest.NewServer(putHandler(http.StatusOK, nil))
	defer putSrv.Close()

	// A fixed cache_entry_id regardless of which racing caller's upload "wins" models
	// control-api's UpsertCacheEntry being idempotent on cache_key (NFR4): both racing
	// hosts derive the identical key and so both get back the identical entry id.
	const raceCacheEntryID = int64(77)

	manManClient := &fakeOrchestratorManManClient{
		gameConfigID:  10,
		containerPath: "/data/mods",
		hostSubpath:   "mods",
		volumeName:    "mods",
	}

	docker := newFakeDockerClient()
	docker.verifyContentVersions = map[string]string{"987654321": "17000000"}
	docker.downloadFiles = map[string]string{"addon.vpk": "racing content"}

	const numRacers = 2
	type raceResult struct {
		err          error
		cacheEntryID int64
	}
	results := make(chan raceResult, numRacers)

	for i := 0; i < numRacers; i++ {
		i := i
		go func() {
			workshopClient := &fakeOrchestratorWorkshopClient{
				addon:              newVerifyTestAddon(),
				cacheHit:           false,
				uploadPresignedURL: putSrv.URL,
				uploadCacheEntryID: raceCacheEntryID,
			}
			publisher := &MockInstallationStatusPublisher{}
			tmpDir := t.TempDir()

			orchestrator := NewDownloadOrchestrator(
				docker, manManClient, workshopClient, 1, "test", tmpDir, tmpDir, 2, publisher,
			)

			cmd := &DownloadAddonCommand{
				// Distinct InstallationIDs, matching two different hosts/installs racing
				// on the same underlying (workshopID, contentVersion) rather than the
				// same installation retried -- the in-progress-download dedupe is keyed
				// on InstallationID and must not be what prevents a "collision".
				InstallationID: int64(100 + i),
				SGCID:          5,
				AddonID:        123,
				WorkshopID:     "987654321",
				SteamAppID:     "550",
				InstallPath:    "/data/mods",
			}

			err := orchestrator.HandleDownloadCommand(context.Background(), cmd)

			var gotCacheEntryID int64
			if len(publisher.cacheUpdates) > 0 {
				gotCacheEntryID = publisher.cacheUpdates[0].CacheEntryID
			}
			results <- raceResult{err: err, cacheEntryID: gotCacheEntryID}
		}()
	}

	timeout := time.After(10 * time.Second)
	for i := 0; i < numRacers; i++ {
		select {
		case res := <-results:
			require.NoError(t, res.err, "no distributed lock exists, so neither racer's install may fail")
			assert.Equal(t, raceCacheEntryID, res.cacheEntryID, "both racers must converge on the same cache_entry_id")
		case <-timeout:
			t.Fatal("racing installs did not complete -- possible deadlock")
		}
	}
}
