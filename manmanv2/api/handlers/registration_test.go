package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/manmanv2"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockServerRepository is a mock implementation of ServerRepository for testing
type mockServerRepository struct {
	servers       map[string]*manman.Server
	nextID        int64
	getByNameErr  error
	createErr     error
	updateErr     error
}

func newMockServerRepository() *mockServerRepository {
	return &mockServerRepository{
		servers: make(map[string]*manman.Server),
		nextID:  1,
	}
}

func (m *mockServerRepository) Create(ctx context.Context, name string) (*manman.Server, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	server := &manman.Server{
		ServerID: m.nextID,
		Name:     name,
		Status:   manman.ServerStatusOffline,
	}
	m.servers[name] = server
	m.nextID++
	return server, nil
}

func (m *mockServerRepository) GetByName(ctx context.Context, name string) (*manman.Server, error) {
	if m.getByNameErr != nil {
		return nil, m.getByNameErr
	}
	server, ok := m.servers[name]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	// Return a copy to avoid mutation issues
	serverCopy := *server
	return &serverCopy, nil
}

func (m *mockServerRepository) Update(ctx context.Context, server *manman.Server) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.servers[server.Name] = server
	return nil
}

// Unused methods to satisfy interface
func (m *mockServerRepository) Get(ctx context.Context, serverID int64) (*manman.Server, error) {
	return nil, nil
}

func (m *mockServerRepository) List(ctx context.Context, limit, offset int) ([]*manman.Server, error) {
	return nil, nil
}

func (m *mockServerRepository) Delete(ctx context.Context, serverID int64) error {
	return nil
}

func (m *mockServerRepository) UpdateStatusAndLastSeen(ctx context.Context, serverID int64, status string, lastSeen time.Time) error {
	return nil
}

func (m *mockServerRepository) UpdateLastSeen(ctx context.Context, serverID int64, lastSeen time.Time) error {
	return nil
}

func (m *mockServerRepository) ListStaleServers(ctx context.Context, thresholdSeconds int) ([]*manman.Server, error) {
	return nil, nil
}

func (m *mockServerRepository) MarkServersOffline(ctx context.Context, serverIDs []int64) error {
	return nil
}

// mockCapabilityRepository is a mock implementation of ServerCapabilityRepository
type mockCapabilityRepository struct {
	capabilities map[int64]*manman.ServerCapability
	insertErr    error
}

func newMockCapabilityRepository() *mockCapabilityRepository {
	return &mockCapabilityRepository{
		capabilities: make(map[int64]*manman.ServerCapability),
	}
}

func (m *mockCapabilityRepository) Insert(ctx context.Context, cap *manman.ServerCapability) error {
	if m.insertErr != nil {
		return m.insertErr
	}
	m.capabilities[cap.ServerID] = cap
	return nil
}

func (m *mockCapabilityRepository) Get(ctx context.Context, serverID int64) (*manman.ServerCapability, error) {
	cap, ok := m.capabilities[serverID]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return cap, nil
}

func TestRegisterServer_NewServer(t *testing.T) {
	serverRepo := newMockServerRepository()
	capRepo := newMockCapabilityRepository()
	handler := NewRegistrationHandler(serverRepo, capRepo)

	req := &pb.RegisterServerRequest{
		Name:        "test-server-1",
		Environment: "dev",
		Capabilities: &pb.ServerCapabilities{
			TotalMemoryMb:          16384,
			AvailableMemoryMb:      8192,
			CpuCores:               4,
			AvailableCpuMillicores: 4000,
			DockerVersion:          "24.0.0",
		},
	}

	resp, err := handler.RegisterServer(context.Background(), req)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.ServerId != 1 {
		t.Errorf("Expected server_id=1, got: %d", resp.ServerId)
	}

	// Verify server was created with correct status
	server, _ := serverRepo.GetByName(context.Background(), "test-server-1")
	if server.Status != manman.ServerStatusOnline {
		t.Errorf("Expected status=online, got: %s", server.Status)
	}

	// Verify capabilities were stored
	cap, err := capRepo.Get(context.Background(), resp.ServerId)
	if err != nil {
		t.Fatalf("Expected capabilities to be stored, got error: %v", err)
	}
	if cap.TotalMemoryMB != 16384 {
		t.Errorf("Expected TotalMemoryMB=16384, got: %d", cap.TotalMemoryMB)
	}
}

func TestRegisterServer_IdempotentRegistration(t *testing.T) {
	serverRepo := newMockServerRepository()
	capRepo := newMockCapabilityRepository()
	handler := NewRegistrationHandler(serverRepo, capRepo)

	req := &pb.RegisterServerRequest{
		Name:        "test-server-idempotent",
		Environment: "dev",
		Capabilities: &pb.ServerCapabilities{
			TotalMemoryMb:          16384,
			AvailableMemoryMb:      8192,
			CpuCores:               4,
			AvailableCpuMillicores: 4000,
			DockerVersion:          "24.0.0",
		},
	}

	// First registration
	resp1, err := handler.RegisterServer(context.Background(), req)
	if err != nil {
		t.Fatalf("First registration failed: %v", err)
	}
	serverID1 := resp1.ServerId

	// Second registration (idempotent - should succeed and return same ID)
	resp2, err := handler.RegisterServer(context.Background(), req)
	if err != nil {
		t.Fatalf("Second registration should be idempotent but got error: %v", err)
	}

	if resp2.ServerId != serverID1 {
		t.Errorf("Expected same server_id=%d on re-registration, got: %d", serverID1, resp2.ServerId)
	}

	// Verify server status is still online
	server, _ := serverRepo.GetByName(context.Background(), "test-server-idempotent")
	if server.Status != manman.ServerStatusOnline {
		t.Errorf("Expected status=online after re-registration, got: %s", server.Status)
	}

	// Verify last_seen was updated (should not be nil)
	if server.LastSeen == nil {
		t.Error("Expected last_seen to be updated")
	}
}

func TestRegisterServer_UpdateCapabilitiesOnReRegistration(t *testing.T) {
	serverRepo := newMockServerRepository()
	capRepo := newMockCapabilityRepository()
	handler := NewRegistrationHandler(serverRepo, capRepo)

	// First registration with initial capabilities
	req1 := &pb.RegisterServerRequest{
		Name:        "test-server-caps",
		Environment: "dev",
		Capabilities: &pb.ServerCapabilities{
			TotalMemoryMb:          8192,
			AvailableMemoryMb:      4096,
			CpuCores:               2,
			AvailableCpuMillicores: 2000,
			DockerVersion:          "24.0.0",
		},
	}

	resp1, err := handler.RegisterServer(context.Background(), req1)
	if err != nil {
		t.Fatalf("First registration failed: %v", err)
	}

	// Second registration with updated capabilities
	req2 := &pb.RegisterServerRequest{
		Name:        "test-server-caps",
		Environment: "dev",
		Capabilities: &pb.ServerCapabilities{
			TotalMemoryMb:          16384,
			AvailableMemoryMb:      12288,
			CpuCores:               4,
			AvailableCpuMillicores: 4000,
			DockerVersion:          "25.0.0",
		},
	}

	resp2, err := handler.RegisterServer(context.Background(), req2)
	if err != nil {
		t.Fatalf("Second registration failed: %v", err)
	}

	if resp2.ServerId != resp1.ServerId {
		t.Errorf("Expected same server_id=%d, got: %d", resp1.ServerId, resp2.ServerId)
	}

	// Verify capabilities were updated
	cap, err := capRepo.Get(context.Background(), resp2.ServerId)
	if err != nil {
		t.Fatalf("Failed to get capabilities: %v", err)
	}

	if cap.TotalMemoryMB != 16384 {
		t.Errorf("Expected updated TotalMemoryMB=16384, got: %d", cap.TotalMemoryMB)
	}
	if cap.CPUCores != 4 {
		t.Errorf("Expected updated CPUCores=4, got: %d", cap.CPUCores)
	}
	if cap.DockerVersion != "25.0.0" {
		t.Errorf("Expected updated DockerVersion=25.0.0, got: %s", cap.DockerVersion)
	}
}

func TestRegisterServer_MissingName(t *testing.T) {
	serverRepo := newMockServerRepository()
	capRepo := newMockCapabilityRepository()
	handler := NewRegistrationHandler(serverRepo, capRepo)

	req := &pb.RegisterServerRequest{
		Name: "",
	}

	_, err := handler.RegisterServer(context.Background(), req)
	if err == nil {
		t.Fatal("Expected error for missing name")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("Expected InvalidArgument error, got: %v", st.Code())
	}
}

func TestRegisterServer_DatabaseError(t *testing.T) {
	serverRepo := newMockServerRepository()
	serverRepo.getByNameErr = errors.New("database connection error")
	capRepo := newMockCapabilityRepository()
	handler := NewRegistrationHandler(serverRepo, capRepo)

	req := &pb.RegisterServerRequest{
		Name: "test-server",
	}

	_, err := handler.RegisterServer(context.Background(), req)
	if err == nil {
		t.Fatal("Expected error for database failure")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}
	if st.Code() != codes.Internal {
		t.Errorf("Expected Internal error, got: %v", st.Code())
	}
}

func TestRegisterServer_UpdateEnvironment(t *testing.T) {
	serverRepo := newMockServerRepository()
	capRepo := newMockCapabilityRepository()
	handler := NewRegistrationHandler(serverRepo, capRepo)

	// First registration without environment
	req1 := &pb.RegisterServerRequest{
		Name: "test-server-env",
	}

	resp1, err := handler.RegisterServer(context.Background(), req1)
	if err != nil {
		t.Fatalf("First registration failed: %v", err)
	}

	// Second registration with environment
	req2 := &pb.RegisterServerRequest{
		Name:        "test-server-env",
		Environment: "production",
	}

	resp2, err := handler.RegisterServer(context.Background(), req2)
	if err != nil {
		t.Fatalf("Second registration failed: %v", err)
	}

	if resp2.ServerId != resp1.ServerId {
		t.Errorf("Expected same server_id=%d, got: %d", resp1.ServerId, resp2.ServerId)
	}

	// Verify environment was updated
	server, _ := serverRepo.GetByName(context.Background(), "test-server-env")
	if server.Environment == nil {
		t.Fatal("Expected environment to be set")
	}
	if *server.Environment != "production" {
		t.Errorf("Expected environment=production, got: %s", *server.Environment)
	}
}
