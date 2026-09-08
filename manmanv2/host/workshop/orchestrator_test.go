package workshop

import (
	"archive/tar"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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

// MockInstallationStatusPublisher is a mock implementation for testing
type MockInstallationStatusPublisher struct {
	updates []*rmq.InstallationStatusUpdate
}

func (m *MockInstallationStatusPublisher) PublishInstallationStatus(ctx context.Context, update *rmq.InstallationStatusUpdate) error {
	m.updates = append(m.updates, update)
	return nil
}

func TestNewDownloadOrchestrator(t *testing.T) {
	mockPublisher := &MockInstallationStatusPublisher{}
	
	orchestrator := NewDownloadOrchestrator(
		nil, // dockerClient
		nil, // grpcClient
		nil, // workshopClient
		1,   // serverID
		"test", // environment
		"/tmp/test",     // hostDataDir
		"/var/lib/test", // internalDataDir
		3, // maxConcurrent
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

// --- #2183 cache fast-path regression coverage -----------------------------
//
// These tests exercise HandleDownloadCommand's cache-hit/miss gating with
// dockerClient left nil. That is deliberate, not an oversight: a cache hit
// must return before ever touching dockerClient (proven below by the call
// simply not panicking), while a cache miss must fall through to the
// pre-existing SteamCMD path unchanged -- which, with a real *docker.Client,
// is the very next thing HandleDownloadCommand touches
// (do.dockerClient.GetContainerStatus, before resolveVolumeMounts even
// runs). Calling a method on a nil *docker.Client dereferences a nil field
// and panics, so reaching that call is what assert.Panics below actually
// proves: the miss path was not silently short-circuited the way the hit
// path is. libs/go/docker's Client wraps a real Docker SDK client with no
// interface seam to fake, so a full end-to-end SteamCMD container run isn't
// exercised here -- that already required a live Docker daemon before this
// task and is unaffected by it (NFR3).
//
// mutation-tested (verified red, by hand, then reverted):
//   - Changing HandleDownloadCommand's `if do.tryServeFromCache(...) {` gate to always
//     take the cache-hit branch (i.e. treating a miss the same as a hit) made
//     TestHandleDownloadCommand_CacheMiss_FallsThroughToSteamCMD fail with "should
//     panic" -- the call returned early instead of ever reaching dockerClient.
//   - Making tryServeFromCache's final copyDirectory call into a no-op (so cached
//     content is "fetched" but never actually installed) made
//     TestHandleDownloadCommand_CacheHit_SkipsSteamCMD's installed-content assertion
//     fail with "no such file or directory" -- proving that test really checks content
//     landed, not just that HandleDownloadCommand returned nil.
//
// Reverting both mutations restored green.

// fakeOrchestratorWorkshopClient embeds the nil WorkshopServiceClient interface and
// overrides only GetAddon/GetCacheDownloadURL/ReportCacheRead -- the RPCs
// HandleDownloadCommand's cache fast path calls -- so any unexpected call panics
// loudly (same pattern as manmanv2/ui/handlers_sgc_test.go's fakeWorkshopServiceClient).
type fakeOrchestratorWorkshopClient struct {
	pb.WorkshopServiceClient

	addon        *pb.WorkshopAddon
	cacheHit     bool
	presignedURL string
	cacheEntryID int64

	reportCalls []*pb.ReportCacheReadRequest
}

func (f *fakeOrchestratorWorkshopClient) GetAddon(ctx context.Context, in *pb.GetAddonRequest, opts ...grpc.CallOption) (*pb.GetAddonResponse, error) {
	return &pb.GetAddonResponse{Addon: f.addon}, nil
}

func (f *fakeOrchestratorWorkshopClient) GetCacheDownloadURL(ctx context.Context, in *pb.GetCacheDownloadURLRequest, opts ...grpc.CallOption) (*pb.GetCacheDownloadURLResponse, error) {
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
		addon: &pb.WorkshopAddon{
			AddonId:     123,
			WorkshopId:  "987654321",
			SteamAppId:  "550",
			LastUpdated: 17000000,
		},
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

	// dockerClient is deliberately left nil -- see the file-level comment above for why
	// that's exactly what proves the SteamCMD container is never started on a hit.
	orchestrator := NewDownloadOrchestrator(
		nil, manManClient, workshopClient, 1, "test", tmpDir, tmpDir, 3, publisher,
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
}

func TestHandleDownloadCommand_CacheMiss_FallsThroughToSteamCMD(t *testing.T) {
	workshopClient := &fakeOrchestratorWorkshopClient{
		addon: &pb.WorkshopAddon{
			AddonId:     123,
			WorkshopId:  "987654321",
			SteamAppId:  "550",
			LastUpdated: 17000000,
		},
		cacheHit: false, // ordinary miss: no cache entry yet for this content version
	}
	manManClient := &fakeOrchestratorManManClient{
		gameConfigID:  10,
		containerPath: "/data/mods",
		hostSubpath:   "mods",
		volumeName:    "mods",
	}
	publisher := &MockInstallationStatusPublisher{}

	tmpDir := t.TempDir()

	// dockerClient is deliberately left nil: this is the regression guard for the
	// unchanged legacy SteamCMD path (see file-level comment). A miss must not return
	// early the way a hit does -- it must proceed into the code that manages the
	// SteamCMD download container, which (with dockerClient == nil) panics on first
	// contact rather than silently skipping the download the way a hit legitimately
	// does. That panic is the proof the miss fell through correctly.
	orchestrator := NewDownloadOrchestrator(
		nil, manManClient, workshopClient, 1, "test", tmpDir, tmpDir, 3, publisher,
	)

	cmd := &DownloadAddonCommand{
		InstallationID: 1,
		SGCID:          5,
		AddonID:        123,
		WorkshopID:     "987654321",
		SteamAppID:     "550",
		InstallPath:    "/data/mods",
	}

	assert.Panics(t, func() {
		_ = orchestrator.HandleDownloadCommand(context.Background(), cmd)
	}, "a cache miss must fall through into the legacy SteamCMD path (which needs a real dockerClient), not return early the way a hit does")

	assert.Empty(t, workshopClient.reportCalls, "a miss must never report a cache read")
}
