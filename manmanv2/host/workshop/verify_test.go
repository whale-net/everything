package workshop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/whale-net/everything/manmanv2/host/rmq"
)

// TestVerifyWorkshopItem_Unchanged proves the "cheap when unchanged" claim
// VerifyWorkshopItem's doc comment makes: when the verify container's manifest reports
// the same content version already known, Changed is false and ContentVersion echoes it
// back.
func TestVerifyWorkshopItem_Unchanged(t *testing.T) {
	docker := newFakeDockerClient()
	docker.verifyContentVersions = map[string]string{"987654321": "17000000"}

	tmpDir := t.TempDir()
	do := &DownloadOrchestrator{dockerClient: docker, hostDataDir: tmpDir, internalDataDir: tmpDir}

	result, err := do.VerifyWorkshopItem(context.Background(), "987654321", "550", "17000000")
	require.NoError(t, err)
	assert.False(t, result.Changed)
	assert.Equal(t, "17000000", result.ContentVersion)
	assert.Equal(t, 1, docker.verifyContainerCount())
}

// TestVerifyWorkshopItem_Changed proves a manifest reporting a different content version
// than knownContentVersion is reported as Changed, carrying the new version forward.
func TestVerifyWorkshopItem_Changed(t *testing.T) {
	docker := newFakeDockerClient()
	docker.verifyContentVersions = map[string]string{"987654321": "17000099"}

	tmpDir := t.TempDir()
	do := &DownloadOrchestrator{dockerClient: docker, hostDataDir: tmpDir, internalDataDir: tmpDir}

	result, err := do.VerifyWorkshopItem(context.Background(), "987654321", "550", "17000000")
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Equal(t, "17000099", result.ContentVersion)
}

// TestVerifyWorkshopItem_DockerClientNil_ReturnsError proves VerifyWorkshopItem fails
// loudly rather than panicking when misconfigured, so callers (resolveVerifyPlan) get a
// real error to fall back on.
func TestVerifyWorkshopItem_DockerClientNil_ReturnsError(t *testing.T) {
	do := &DownloadOrchestrator{}

	_, err := do.VerifyWorkshopItem(context.Background(), "987654321", "550", "17000000")
	require.Error(t, err)
}

// TestVerifyWorkshopItem_MissingIdentifiers_ReturnsError guards the explicit
// workshopID/steamAppID validation, independent of the docker-client-nil check.
func TestVerifyWorkshopItem_MissingIdentifiers_ReturnsError(t *testing.T) {
	docker := newFakeDockerClient()
	tmpDir := t.TempDir()
	do := &DownloadOrchestrator{dockerClient: docker, hostDataDir: tmpDir, internalDataDir: tmpDir}

	_, err := do.VerifyWorkshopItem(context.Background(), "", "550", "17000000")
	require.Error(t, err)
}

// TestVerifyWorkshopItem_SteamCMDExitFailure_ReturnsError proves a nonzero verify
// container exit code surfaces as an error (this is the "verify RPC/SteamCMD failure"
// scenario resolveVerifyPlan must fall back on -- see
// TestHandleDownloadCommand_VerifyFailure_FallsBackToPlainDownload in orchestrator_test.go).
func TestVerifyWorkshopItem_SteamCMDExitFailure_ReturnsError(t *testing.T) {
	docker := newFakeDockerClient()
	docker.verifyExitCode = 1
	docker.verifyContentVersions = map[string]string{"987654321": "17000000"} // would otherwise report unchanged

	tmpDir := t.TempDir()
	do := &DownloadOrchestrator{dockerClient: docker, hostDataDir: tmpDir, internalDataDir: tmpDir}

	_, err := do.VerifyWorkshopItem(context.Background(), "987654321", "550", "17000000")
	require.Error(t, err)
}

// TestVerifyWorkshopItem_ManifestMissing_ReturnsError proves a verify run that exits 0
// but never writes a block for this workshopID (e.g. an unexpected SteamCMD output
// format) is still surfaced as an error rather than silently reporting "unchanged".
func TestVerifyWorkshopItem_ManifestMissing_ReturnsError(t *testing.T) {
	docker := newFakeDockerClient() // no verifyContentVersions entry for this workshopID

	tmpDir := t.TempDir()
	do := &DownloadOrchestrator{dockerClient: docker, hostDataDir: tmpDir, internalDataDir: tmpDir}

	_, err := do.VerifyWorkshopItem(context.Background(), "987654321", "550", "17000000")
	require.Error(t, err)
}

// --- HandleVerifyCacheEntryCommand (#2186, plan #2175 FR11) -----------------------------

// newVerifyCommandOrchestrator builds a DownloadOrchestrator wired the same way
// NewDownloadOrchestrator would for these tests' collaborators (docker, workshopClient,
// publisher), independent of orchestrator_test.go's own helpers so this file's tests stay
// self-contained.
func newVerifyCommandOrchestrator(docker *fakeDockerClient, workshopClient *fakeOrchestratorWorkshopClient, publisher *MockInstallationStatusPublisher, tmpDir string) *DownloadOrchestrator {
	return NewDownloadOrchestrator(docker, nil, workshopClient, 1, "test", tmpDir, tmpDir, 3, publisher)
}

// TestHandleVerifyCacheEntryCommand_UsesSameVerifyPrimitive proves the admin-triggered
// path reuses VerifyWorkshopItem -- exactly one verify container is driven (the same
// primitive install-time verify uses), not a second, parallel verify implementation.
func TestHandleVerifyCacheEntryCommand_UsesSameVerifyPrimitive(t *testing.T) {
	docker := newFakeDockerClient()
	docker.verifyContentVersions = map[string]string{"987654321": "17000000"}
	workshopClient := &fakeOrchestratorWorkshopClient{}
	publisher := &MockInstallationStatusPublisher{}
	tmpDir := t.TempDir()

	do := newVerifyCommandOrchestrator(docker, workshopClient, publisher, tmpDir)

	cmd := &rmq.VerifyCacheEntryCommand{
		CacheEntryID:   42,
		WorkshopID:     "987654321",
		ContentVersion: "17000000",
		SteamAppID:     "550",
	}
	err := do.HandleVerifyCacheEntryCommand(context.Background(), cmd)
	require.NoError(t, err)
	assert.Equal(t, 1, docker.verifyContainerCount())
	assert.Equal(t, 0, docker.downloadContainerCount(), "an on-demand verify must never start a plain download container")
}

// TestHandleVerifyCacheEntryCommand_Unchanged_PublishesVerifiedUnchanged proves an
// unchanged result publishes the same "verified_unchanged" event name the install-time
// flow uses, carrying the cache entry's identity through, and never attempts an upload.
func TestHandleVerifyCacheEntryCommand_Unchanged_PublishesVerifiedUnchanged(t *testing.T) {
	docker := newFakeDockerClient()
	docker.verifyContentVersions = map[string]string{"987654321": "17000000"} // matches known -> unchanged
	workshopClient := &fakeOrchestratorWorkshopClient{}
	publisher := &MockInstallationStatusPublisher{}
	tmpDir := t.TempDir()

	do := newVerifyCommandOrchestrator(docker, workshopClient, publisher, tmpDir)

	cmd := &rmq.VerifyCacheEntryCommand{
		CacheEntryID:   42,
		WorkshopID:     "987654321",
		ContentVersion: "17000000",
		SteamAppID:     "550",
	}
	err := do.HandleVerifyCacheEntryCommand(context.Background(), cmd)
	require.NoError(t, err)

	require.Len(t, publisher.cacheUpdates, 1)
	update := publisher.cacheUpdates[0]
	assert.Equal(t, "verified_unchanged", update.Event)
	assert.Equal(t, int64(42), update.CacheEntryID)
	assert.Equal(t, "987654321", update.WorkshopID)
	assert.Equal(t, "17000000", update.ContentVersion)

	assert.Empty(t, workshopClient.uploadCalls, "an unchanged verify must never attempt a cache upload")
}

// TestHandleVerifyCacheEntryCommand_Changed_UploadsNewEntry_NeverDeletesOrOverwrites
// proves FR9's append-only rule for the admin-triggered path: a changed result uploads
// the freshly-synced content as a brand new cache entry (a GetCacheUploadURL call keyed
// to the new content version) and publishes "refreshed" -- and, because
// fakeOrchestratorWorkshopClient embeds a nil pb.WorkshopServiceClient and only overrides
// the read/write RPCs it expects, any delete/overwrite-shaped call on the verified entry
// would panic here rather than silently succeeding.
func TestHandleVerifyCacheEntryCommand_Changed_UploadsNewEntry_NeverDeletesOrOverwrites(t *testing.T) {
	var uploadedBody []byte
	putSrv := httptest.NewServer(putHandler(http.StatusOK, &uploadedBody))
	defer putSrv.Close()

	docker := newFakeDockerClient()
	docker.verifyContentVersions = map[string]string{"987654321": "17000099"} // differs from known -> changed
	workshopClient := &fakeOrchestratorWorkshopClient{
		uploadPresignedURL: putSrv.URL,
		uploadCacheEntryID: 100,
	}
	publisher := &MockInstallationStatusPublisher{}
	tmpDir := t.TempDir()

	do := newVerifyCommandOrchestrator(docker, workshopClient, publisher, tmpDir)

	// simulateVerify only writes the manifest (see fake_docker_test.go) -- the content
	// directory itself is populated by the real SteamCMD "validate" re-sync, so seed it
	// directly here at the exact path HandleVerifyCacheEntryCommand reads from.
	contentDir := filepath.Join(do.getVerifyInternalDir("550", "987654321"), "steamapps", "workshop", "content", "550", "987654321")
	require.NoError(t, os.MkdirAll(contentDir, 0777))
	require.NoError(t, os.WriteFile(filepath.Join(contentDir, "addon.vpk"), []byte("refreshed content"), 0644))

	cmd := &rmq.VerifyCacheEntryCommand{
		CacheEntryID:   42, // the entry that was verified -- must never be touched
		WorkshopID:     "987654321",
		ContentVersion: "17000000", // stale known version -> triggers "changed"
		SteamAppID:     "550",
	}
	err := do.HandleVerifyCacheEntryCommand(context.Background(), cmd)
	require.NoError(t, err)

	require.Len(t, workshopClient.uploadCalls, 1)
	assert.Equal(t, "17000099", workshopClient.uploadCalls[0].ContentVersion, "the upload must be keyed to the newly-observed content version")
	assert.NotEmpty(t, uploadedBody, "the refreshed content must actually be uploaded")

	require.Len(t, publisher.cacheUpdates, 1)
	assert.Equal(t, "refreshed", publisher.cacheUpdates[0].Event)
	assert.Equal(t, int64(100), publisher.cacheUpdates[0].CacheEntryID, "the published entry id is the new upload's id, never the verified entry's id (42)")
	assert.Equal(t, "17000099", publisher.cacheUpdates[0].ContentVersion)
}

// TestHandleVerifyCacheEntryCommand_ChangedNoContentOnDisk_NoUploadNoError covers the
// defensive branch: a changed manifest but no content directory on disk (an unexpected
// SteamCMD outcome) must not be treated as a completed refresh -- no upload, no error.
func TestHandleVerifyCacheEntryCommand_ChangedNoContentOnDisk_NoUploadNoError(t *testing.T) {
	docker := newFakeDockerClient()
	docker.verifyContentVersions = map[string]string{"987654321": "17000099"} // changed
	workshopClient := &fakeOrchestratorWorkshopClient{}
	publisher := &MockInstallationStatusPublisher{}
	tmpDir := t.TempDir()

	do := newVerifyCommandOrchestrator(docker, workshopClient, publisher, tmpDir)
	// No content directory seeded this time.

	cmd := &rmq.VerifyCacheEntryCommand{
		CacheEntryID:   42,
		WorkshopID:     "987654321",
		ContentVersion: "17000000",
		SteamAppID:     "550",
	}
	err := do.HandleVerifyCacheEntryCommand(context.Background(), cmd)
	require.NoError(t, err)
	assert.Empty(t, workshopClient.uploadCalls)
	assert.Empty(t, publisher.cacheUpdates)
}

// TestHandleVerifyCacheEntryCommand_VerifyFailure_ReturnsErrorNoPublish proves a
// SteamCMD/RPC failure inside VerifyWorkshopItem surfaces as an error from the
// admin-triggered command wrapper too (unlike the install-time flow, there is no
// fallback-to-plain-download available here), and nothing is published.
func TestHandleVerifyCacheEntryCommand_VerifyFailure_ReturnsErrorNoPublish(t *testing.T) {
	docker := newFakeDockerClient()
	docker.verifyExitCode = 1
	workshopClient := &fakeOrchestratorWorkshopClient{}
	publisher := &MockInstallationStatusPublisher{}
	tmpDir := t.TempDir()

	do := newVerifyCommandOrchestrator(docker, workshopClient, publisher, tmpDir)

	cmd := &rmq.VerifyCacheEntryCommand{
		CacheEntryID:   42,
		WorkshopID:     "987654321",
		ContentVersion: "17000000",
		SteamAppID:     "550",
	}
	err := do.HandleVerifyCacheEntryCommand(context.Background(), cmd)
	require.Error(t, err)
	assert.Empty(t, publisher.cacheUpdates)
	assert.Empty(t, workshopClient.uploadCalls)
}
