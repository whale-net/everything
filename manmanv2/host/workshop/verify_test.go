package workshop

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
