// +build integration

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/docker"
	pb "github.com/whale-net/everything/manman/protos"
)

const (
	testImageName = "manman-test-game-server:latest"
	testTimeout   = 30 * time.Second
)

// requireDocker checks if Docker is available and skips the test if not
func requireDocker(t *testing.T) *docker.Client {
	t.Helper()

	client, err := docker.NewClient("/var/run/docker.sock")
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}

	// Test Docker connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try to ping Docker by getting client
	if client.GetClient() == nil {
		t.Skip("Docker client not properly initialized")
	}

	return client
}

// setupTestServer creates a test server with a temporary data directory
func setupTestServer(t *testing.T, dockerClient *docker.Client) (*server, string, func()) {
	t.Helper()

	// Create temp directory for test data
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("Failed to create test data dir: %v", err)
	}

	// Override /data mount point for testing
	// We'll need to handle this in the test by using the temp dir

	// Create server
	srv := newServer(dockerClient, "999", "bridge", nil)

	cleanup := func() {
		// Cleanup is handled by t.TempDir()
	}

	return srv, dataDir, cleanup
}

// ensureTestImage builds the test container image if it doesn't exist
func ensureTestImage(t *testing.T, dockerClient *docker.Client) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Check if image already exists
	// For now, we'll just log and expect the image to be built manually
	// In the future, we could build it programmatically

	t.Logf("Using test image: %s", testImageName)
	t.Logf("To build the test image, run:")
	t.Logf("  cd manman/wrapper/testdata && docker build -t %s .", testImageName)
}

// createTestGameConfig creates a minimal game config for testing
func createTestGameConfig() *pb.GameConfig {
	return &pb.GameConfig{
		ConfigId:     1,
		GameId:       1,
		Name:         "test-game",
		Image:        testImageName,
		ArgsTemplate: "",
		EnvTemplate:  map[string]string{},
	}
}

// createTestServerGameConfig creates a minimal server game config for testing
func createTestServerGameConfig() *pb.ServerGameConfig {
	return &pb.ServerGameConfig{
		ServerGameConfigId: 1,
		ServerId:           1,
		GameConfigId:       1,
		PortBindings:       []*pb.PortBinding{},
		Parameters:         map[string]string{},
		Status:             "active",
	}
}

// TestStart_Success tests successfully starting a game container
func TestStart_Success(t *testing.T) {
	dockerClient := requireDocker(t)
	defer dockerClient.Close()

	ensureTestImage(t, dockerClient)

	srv, dataDir, cleanup := setupTestServer(t, dockerClient)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Create start request
	req := &pb.StartRequest{
		SessionId:        1001,
		SgcId:            1,
		GameConfig:       createTestGameConfig(),
		ServerGameConfig: createTestServerGameConfig(),
		Parameters:       "{}",
	}

	// Start the container
	resp, err := srv.Start(ctx, req)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Start reported failure: %s", resp.ErrorMessage)
	}

	if resp.ContainerId == "" {
		t.Fatal("Start did not return container ID")
	}

	t.Logf("Started container: %s", resp.ContainerId)

	// Verify container is running
	time.Sleep(1 * time.Second) // Give container time to start

	status, err := srv.GetStatus(ctx, &pb.GetStatusRequest{SessionId: 1001})
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}

	if !status.IsRunning {
		t.Errorf("Container not running. Status: %s, ExitCode: %d", status.Status, status.ExitCode)
	}

	// Cleanup: stop the container
	stopResp, err := srv.Stop(ctx, &pb.StopRequest{SessionId: 1001, Force: true})
	if err != nil {
		t.Errorf("Stop failed: %v", err)
	}
	if !stopResp.Success {
		t.Errorf("Stop reported failure: %s", stopResp.ErrorMessage)
	}

	t.Logf("Test data dir: %s", dataDir)
}

// TestStreamOutput_Stdout tests streaming stdout from the container
func TestStreamOutput_Stdout(t *testing.T) {
	dockerClient := requireDocker(t)
	defer dockerClient.Close()

	ensureTestImage(t, dockerClient)

	srv, _, cleanup := setupTestServer(t, dockerClient)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Start container
	startReq := &pb.StartRequest{
		SessionId:        1002,
		SgcId:            1,
		GameConfig:       createTestGameConfig(),
		ServerGameConfig: createTestServerGameConfig(),
		Parameters:       "{}",
	}

	startResp, err := srv.Start(ctx, startReq)
	if err != nil || !startResp.Success {
		t.Fatalf("Failed to start container: %v, %s", err, startResp.ErrorMessage)
	}
	defer srv.Stop(ctx, &pb.StopRequest{SessionId: 1002, Force: true})

	// Wait for container to produce some output
	time.Sleep(2 * time.Second)

	// Stream output
	streamReq := &pb.StreamOutputRequest{
		SessionId: 1002,
		Stdout:    true,
		Stderr:    false,
	}

	// Create a mock stream
	mockStream := &mockStreamServer{ctx: ctx, responses: []*pb.StreamOutputResponse{}}

	err = srv.StreamOutput(streamReq, mockStream)
	if err != nil {
		t.Fatalf("StreamOutput failed: %v", err)
	}

	// Verify we received some stdout
	foundStarting := false
	foundReady := false
	for _, resp := range mockStream.responses {
		if resp.IsStderr {
			t.Errorf("Received stderr when only stdout was requested")
		}
		output := string(resp.Data)
		t.Logf("Received: %s", output)
		if strings.Contains(output, "Test game server starting") {
			foundStarting = true
		}
		if strings.Contains(output, "Server ready") {
			foundReady = true
		}
	}

	if !foundStarting {
		t.Error("Did not receive startup message")
	}
	if !foundReady {
		t.Error("Did not receive ready message")
	}
}

// TestSendInput_GracefulStop tests sending a stop command via stdin
func TestSendInput_GracefulStop(t *testing.T) {
	t.Skip("TODO: SendInput not fully implemented yet - deferred to Phase 4")

	dockerClient := requireDocker(t)
	defer dockerClient.Close()

	ensureTestImage(t, dockerClient)

	srv, _, cleanup := setupTestServer(t, dockerClient)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Start container
	startReq := &pb.StartRequest{
		SessionId:        1003,
		SgcId:            1,
		GameConfig:       createTestGameConfig(),
		ServerGameConfig: createTestServerGameConfig(),
		Parameters:       "{}",
	}

	startResp, err := srv.Start(ctx, startReq)
	if err != nil || !startResp.Success {
		t.Fatalf("Failed to start container: %v, %s", err, startResp.ErrorMessage)
	}

	// Wait for container to be ready
	time.Sleep(2 * time.Second)

	// Send "stop" command via stdin
	inputResp, err := srv.SendInput(ctx, &pb.SendInputRequest{
		SessionId: 1003,
		Input:     []byte("stop\n"),
	})
	if err != nil {
		t.Fatalf("SendInput failed: %v", err)
	}
	if !inputResp.Success {
		t.Fatalf("SendInput reported failure: %s", inputResp.ErrorMessage)
	}

	// Wait for graceful shutdown
	time.Sleep(2 * time.Second)

	// Check that container has stopped
	status, err := srv.GetStatus(ctx, &pb.GetStatusRequest{SessionId: 1003})
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}

	if status.IsRunning {
		t.Error("Container still running after stop command")
	}

	if status.ExitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", status.ExitCode)
	}
}

// TestStop_Force tests force stopping a container
func TestStop_Force(t *testing.T) {
	dockerClient := requireDocker(t)
	defer dockerClient.Close()

	ensureTestImage(t, dockerClient)

	srv, _, cleanup := setupTestServer(t, dockerClient)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Start container
	startReq := &pb.StartRequest{
		SessionId:        1004,
		SgcId:            1,
		GameConfig:       createTestGameConfig(),
		ServerGameConfig: createTestServerGameConfig(),
		Parameters:       "{}",
	}

	startResp, err := srv.Start(ctx, startReq)
	if err != nil || !startResp.Success {
		t.Fatalf("Failed to start container: %v, %s", err, startResp.ErrorMessage)
	}

	// Wait for container to be running
	time.Sleep(1 * time.Second)

	// Force stop
	stopResp, err := srv.Stop(ctx, &pb.StopRequest{
		SessionId: 1004,
		Force:     true,
	})
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if !stopResp.Success {
		t.Fatalf("Stop reported failure: %s", stopResp.ErrorMessage)
	}

	// Verify container is stopped
	status, err := srv.GetStatus(ctx, &pb.GetStatusRequest{SessionId: 1004})
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}

	if status.IsRunning {
		t.Error("Container still running after force stop")
	}

	t.Logf("Container stopped with exit code: %d", status.ExitCode)
}

// TestSendInput_EdgeCases tests stdin edge cases
func TestSendInput_EdgeCases(t *testing.T) {
	dockerClient := requireDocker(t)
	defer dockerClient.Close()

	srv, _, cleanup := setupTestServer(t, dockerClient)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	tests := []struct {
		name          string
		sessionID     int64
		setupFunc     func(t *testing.T, srv *server, sessionID int64)
		wantSuccess   bool
		wantErrorMsg  string
	}{
		{
			name:         "session not found",
			sessionID:    9999,
			setupFunc:    nil,
			wantSuccess:  false,
			wantErrorMsg: "session 9999 not found",
		},
		{
			name:      "container not started",
			sessionID: 1005,
			setupFunc: func(t *testing.T, srv *server, sessionID int64) {
				// Create session state but don't start container
				session := &SessionState{
					SessionID: sessionID,
					SGCID:     1,
					Status:    "pending",
				}
				srv.stateManager.SetSession(session)
			},
			wantSuccess:  false,
			wantErrorMsg: "no container associated with this session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFunc != nil {
				tt.setupFunc(t, srv, tt.sessionID)
			}

			resp, err := srv.SendInput(ctx, &pb.SendInputRequest{
				SessionId: tt.sessionID,
				Input:     []byte("test\n"),
			})

			if err != nil {
				t.Fatalf("SendInput returned error: %v", err)
			}

			if resp.Success != tt.wantSuccess {
				t.Errorf("Success = %v, want %v", resp.Success, tt.wantSuccess)
			}

			if !strings.Contains(resp.ErrorMessage, tt.wantErrorMsg) {
				t.Errorf("ErrorMessage = %q, want to contain %q", resp.ErrorMessage, tt.wantErrorMsg)
			}
		})
	}
}

// TestStart_InvalidImage tests starting with an invalid image
func TestStart_InvalidImage(t *testing.T) {
	dockerClient := requireDocker(t)
	defer dockerClient.Close()

	srv, _, cleanup := setupTestServer(t, dockerClient)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Create config with invalid image
	gameConfig := createTestGameConfig()
	gameConfig.Image = "nonexistent-image-12345:latest"

	req := &pb.StartRequest{
		SessionId:        1006,
		SgcId:            1,
		GameConfig:       gameConfig,
		ServerGameConfig: createTestServerGameConfig(),
		Parameters:       "{}",
	}

	resp, err := srv.Start(ctx, req)
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if resp.Success {
		t.Error("Start succeeded with invalid image")
	}

	if !strings.Contains(resp.ErrorMessage, "failed to create container") {
		t.Errorf("Unexpected error message: %s", resp.ErrorMessage)
	}
}

// TestStreamOutput_Stderr tests streaming stderr from the container
func TestStreamOutput_Stderr(t *testing.T) {
	dockerClient := requireDocker(t)
	defer dockerClient.Close()

	ensureTestImage(t, dockerClient)

	srv, _, cleanup := setupTestServer(t, dockerClient)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Start container
	startReq := &pb.StartRequest{
		SessionId:        1007,
		SgcId:            1,
		GameConfig:       createTestGameConfig(),
		ServerGameConfig: createTestServerGameConfig(),
		Parameters:       "{}",
	}

	startResp, err := srv.Start(ctx, startReq)
	if err != nil || !startResp.Success {
		t.Fatalf("Failed to start container: %v, %s", err, startResp.ErrorMessage)
	}
	defer srv.Stop(ctx, &pb.StopRequest{SessionId: 1007, Force: true})

	// Wait for output
	time.Sleep(2 * time.Second)

	// Stream stderr only
	streamReq := &pb.StreamOutputRequest{
		SessionId: 1007,
		Stdout:    false,
		Stderr:    true,
	}

	mockStream := &mockStreamServer{ctx: ctx, responses: []*pb.StreamOutputResponse{}}

	err = srv.StreamOutput(streamReq, mockStream)
	if err != nil {
		t.Fatalf("StreamOutput failed: %v", err)
	}

	// Verify we received stderr
	foundStderr := false
	for _, resp := range mockStream.responses {
		if !resp.IsStderr && len(resp.Data) > 0 && !resp.Eof {
			t.Errorf("Received stdout when only stderr was requested: %s", string(resp.Data))
		}
		if resp.IsStderr && strings.Contains(string(resp.Data), "Logging to stderr") {
			foundStderr = true
		}
	}

	if !foundStderr {
		t.Error("Did not receive expected stderr output")
	}
}

// TestGetStatus_Stopped tests querying status of a stopped container
func TestGetStatus_Stopped(t *testing.T) {
	dockerClient := requireDocker(t)
	defer dockerClient.Close()

	ensureTestImage(t, dockerClient)

	srv, _, cleanup := setupTestServer(t, dockerClient)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Start and immediately stop container
	startReq := &pb.StartRequest{
		SessionId:        1008,
		SgcId:            1,
		GameConfig:       createTestGameConfig(),
		ServerGameConfig: createTestServerGameConfig(),
		Parameters:       "{}",
	}

	startResp, err := srv.Start(ctx, startReq)
	if err != nil || !startResp.Success {
		t.Fatalf("Failed to start container: %v, %s", err, startResp.ErrorMessage)
	}

	time.Sleep(1 * time.Second)

	// Stop container
	stopResp, err := srv.Stop(ctx, &pb.StopRequest{SessionId: 1008, Force: false})
	if err != nil || !stopResp.Success {
		t.Fatalf("Failed to stop container: %v, %s", err, stopResp.ErrorMessage)
	}

	// Get status
	statusResp, err := srv.GetStatus(ctx, &pb.GetStatusRequest{SessionId: 1008})
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}

	if statusResp.IsRunning {
		t.Error("Status reports container is running after stop")
	}

	if statusResp.Status != "stopped" {
		t.Errorf("Status = %s, want stopped", statusResp.Status)
	}
}

// mockStreamServer implements pb.WrapperControl_StreamOutputServer for testing
type mockStreamServer struct {
	ctx       context.Context
	responses []*pb.StreamOutputResponse
}

func (m *mockStreamServer) Send(resp *pb.StreamOutputResponse) error {
	m.responses = append(m.responses, resp)
	return nil
}

func (m *mockStreamServer) Context() context.Context {
	return m.ctx
}

func (m *mockStreamServer) SendMsg(msg interface{}) error {
	return nil
}

func (m *mockStreamServer) RecvMsg(msg interface{}) error {
	return nil
}

func (m *mockStreamServer) SetHeader(metadata interface{}) error {
	return nil
}

func (m *mockStreamServer) SendHeader(metadata interface{}) error {
	return nil
}

func (m *mockStreamServer) SetTrailer(metadata interface{}) error {
	return nil
}
