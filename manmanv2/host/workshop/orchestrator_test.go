package workshop

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/whale-net/everything/manmanv2/host/rmq"
)

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
	
	// Initially not in progress
	assert.False(t, orchestrator.isDownloadInProgress(123))
	
	// Mark as in progress
	orchestrator.markDownloadInProgress(123)
	assert.True(t, orchestrator.isDownloadInProgress(123))
	
	// Mark as complete
	orchestrator.markDownloadComplete(123)
	assert.False(t, orchestrator.isDownloadInProgress(123))
}
