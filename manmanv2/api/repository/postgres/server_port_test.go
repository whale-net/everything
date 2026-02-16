package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2"
)

// TestPortAllocationBasic tests basic port allocation and deallocation
func TestPortAllocationBasic(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Test: Allocate a port successfully
	err := repo.AllocatePort(ctx, 1, 8080, "TCP", 100)
	if err != nil {
		t.Fatalf("Failed to allocate port: %v", err)
	}

	// Test: Verify port is allocated
	isAvailable, err := repo.IsPortAvailable(ctx, 1, 8080, "TCP")
	if err != nil {
		t.Fatalf("Failed to check port availability: %v", err)
	}
	if isAvailable {
		t.Error("Port should not be available after allocation")
	}

	// Test: Get allocation details
	allocation, err := repo.GetPortAllocation(ctx, 1, 8080, "TCP")
	if err != nil {
		t.Fatalf("Failed to get port allocation: %v", err)
	}
	if allocation.SGCID == nil || *allocation.SGCID != 100 {
		t.Errorf("Expected SGCID 100, got %v", allocation.SGCID)
	}

	// Test: Deallocate port
	err = repo.DeallocatePort(ctx, 1, 8080, "TCP")
	if err != nil {
		t.Fatalf("Failed to deallocate port: %v", err)
	}

	// Test: Verify port is available after deallocation
	isAvailable, err = repo.IsPortAvailable(ctx, 1, 8080, "TCP")
	if err != nil {
		t.Fatalf("Failed to check port availability: %v", err)
	}
	if !isAvailable {
		t.Error("Port should be available after deallocation")
	}
}

// TestPortConflictDetection tests port conflict scenarios
func TestPortConflictDetection(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Allocate port for SGCID 100
	err := repo.AllocatePort(ctx, 1, 8080, "TCP", 100)
	if err != nil {
		t.Fatalf("Failed to allocate port: %v", err)
	}

	// Test: Try to allocate same port for different SGCID (should fail)
	err = repo.AllocatePort(ctx, 1, 8080, "TCP", 200)
	if err == nil {
		t.Error("Expected error when allocating already-allocated port")
	}
	if !IsPortConflictError(err) {
		t.Errorf("Expected PortConflictError, got %v", err)
	}

	// Test: Same port number but different protocol should succeed
	err = repo.AllocatePort(ctx, 1, 8080, "UDP", 200)
	if err != nil {
		t.Fatalf("Should allow same port with different protocol: %v", err)
	}

	// Test: Same port on different server should succeed
	err = repo.AllocatePort(ctx, 2, 8080, "TCP", 300)
	if err != nil {
		t.Fatalf("Should allow same port on different server: %v", err)
	}
}

// TestPortRangeValidation tests port number validation
func TestPortRangeValidation(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	tests := []struct {
		name      string
		port      int
		shouldErr bool
	}{
		{"valid port 1", 1, false},
		{"valid port 80", 80, false},
		{"valid port 8080", 8080, false},
		{"valid port 65535", 65535, false},
		{"invalid port 0", 0, true},
		{"invalid port -1", -1, true},
		{"invalid port 65536", 65536, true},
		{"invalid port 100000", 100000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := repo.AllocatePort(ctx, 1, tt.port, "TCP", 100)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for port %d", tt.port)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Unexpected error for port %d: %v", tt.port, err)
			}
		})
	}
}

// TestProtocolValidation tests protocol validation
func TestProtocolValidation(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	validProtocols := []string{"TCP", "UDP"}
	for _, protocol := range validProtocols {
		err := repo.AllocatePort(ctx, 1, 8080, protocol, 100)
		if err != nil {
			t.Errorf("Valid protocol %s should be accepted: %v", protocol, err)
		}
		repo.DeallocatePort(ctx, 1, 8080, protocol)
	}

	invalidProtocols := []string{"tcp", "udp", "SCTP", "ICMP", "", "invalid"}
	for _, protocol := range invalidProtocols {
		err := repo.AllocatePort(ctx, 1, 8080, protocol, 100)
		if err == nil {
			t.Errorf("Invalid protocol %s should be rejected", protocol)
		}
	}
}

// TestListAllocatedPorts tests listing ports for a server
func TestListAllocatedPorts(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Allocate multiple ports
	allocations := []struct {
		port     int
		protocol string
		sgcID    int64
	}{
		{8080, "TCP", 100},
		{8081, "TCP", 100},
		{8080, "UDP", 200},
		{9000, "TCP", 300},
	}

	for _, alloc := range allocations {
		err := repo.AllocatePort(ctx, 1, alloc.port, alloc.protocol, alloc.sgcID)
		if err != nil {
			t.Fatalf("Failed to allocate port %d/%s: %v", alloc.port, alloc.protocol, err)
		}
	}

	// Test: List all ports for server 1
	ports, err := repo.ListAllocatedPorts(ctx, 1)
	if err != nil {
		t.Fatalf("Failed to list ports: %v", err)
	}
	if len(ports) != 4 {
		t.Errorf("Expected 4 allocated ports, got %d", len(ports))
	}

	// Test: List ports for SGCID 100
	ports, err = repo.ListPortsBySGCID(ctx, 100)
	if err != nil {
		t.Fatalf("Failed to list ports by SGCID: %v", err)
	}
	if len(ports) != 2 {
		t.Errorf("Expected 2 ports for SGCID 100, got %d", len(ports))
	}
}

// TestDeallocateBySGCID tests deallocating all ports for a ServerGameConfig
func TestDeallocateBySGCID(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Allocate ports for SGCID 100
	repo.AllocatePort(ctx, 1, 8080, "TCP", 100)
	repo.AllocatePort(ctx, 1, 8081, "TCP", 100)
	repo.AllocatePort(ctx, 1, 8082, "UDP", 100)

	// Allocate port for different SGCID
	repo.AllocatePort(ctx, 1, 9000, "TCP", 200)

	// Test: Deallocate all ports for SGCID 100
	err := repo.DeallocatePortsBySGCID(ctx, 100)
	if err != nil {
		t.Fatalf("Failed to deallocate ports by SGCID: %v", err)
	}

	// Verify SGCID 100 ports are deallocated
	ports, _ := repo.ListPortsBySGCID(ctx, 100)
	if len(ports) != 0 {
		t.Errorf("Expected 0 ports for SGCID 100 after deallocation, got %d", len(ports))
	}

	// Verify SGCID 200 port is still allocated
	ports, _ = repo.ListPortsBySGCID(ctx, 200)
	if len(ports) != 1 {
		t.Errorf("Expected 1 port for SGCID 200, got %d", len(ports))
	}
}

// TestAllocateMultiplePorts tests batch port allocation
func TestAllocateMultiplePorts(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	portBindings := []*manman.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
		{ContainerPort: 25565, HostPort: 25565, Protocol: "UDP"},
		{ContainerPort: 8080, HostPort: 8080, Protocol: "TCP"},
	}

	// Test: Allocate multiple ports at once
	err := repo.AllocateMultiplePorts(ctx, 1, portBindings, 100)
	if err != nil {
		t.Fatalf("Failed to allocate multiple ports: %v", err)
	}

	// Verify all ports are allocated
	for _, binding := range portBindings {
		isAvailable, _ := repo.IsPortAvailable(ctx, 1, int(binding.HostPort), binding.Protocol)
		if isAvailable {
			t.Errorf("Port %d/%s should be allocated", binding.HostPort, binding.Protocol)
		}
	}
}

// TestAllocateMultiplePortsConflict tests partial allocation failure
func TestAllocateMultiplePortsConflict(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Pre-allocate one port
	repo.AllocatePort(ctx, 1, 8080, "TCP", 999)

	portBindings := []*manman.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
		{ContainerPort: 8080, HostPort: 8080, Protocol: "TCP"}, // Conflict!
		{ContainerPort: 9000, HostPort: 9000, Protocol: "TCP"},
	}

	// Test: Batch allocation should fail entirely due to conflict
	err := repo.AllocateMultiplePorts(ctx, 1, portBindings, 100)
	if err == nil {
		t.Error("Expected error due to port conflict")
	}

	// Verify NO ports were allocated for SGCID 100 (transaction rollback)
	ports, _ := repo.ListPortsBySGCID(ctx, 100)
	if len(ports) != 0 {
		t.Errorf("Expected 0 ports allocated after failed batch, got %d", len(ports))
	}

	// Verify original allocation still exists
	allocation, _ := repo.GetPortAllocation(ctx, 1, 8080, "TCP")
	if allocation.SGCID == nil || *allocation.SGCID != 999 {
		t.Error("Original port allocation should remain after failed batch")
	}
}

// TestGetAvailablePortsInRange tests finding available ports
func TestGetAvailablePortsInRange(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Allocate some ports
	repo.AllocatePort(ctx, 1, 8080, "TCP", 100)
	repo.AllocatePort(ctx, 1, 8081, "TCP", 100)
	repo.AllocatePort(ctx, 1, 8083, "TCP", 100)

	// Test: Get available ports in range
	availablePorts, err := repo.GetAvailablePortsInRange(ctx, 1, "TCP", 8080, 8090, 5)
	if err != nil {
		t.Fatalf("Failed to get available ports: %v", err)
	}

	// Should return 8082, 8084, 8085, 8086, 8087 (first 5 available)
	if len(availablePorts) != 5 {
		t.Errorf("Expected 5 available ports, got %d", len(availablePorts))
	}

	// Verify returned ports are actually available
	expectedPorts := []int{8082, 8084, 8085, 8086, 8087}
	for i, port := range availablePorts {
		if port != expectedPorts[i] {
			t.Errorf("Expected port %d at position %d, got %d", expectedPorts[i], i, port)
		}
	}
}

// TestDeallocateNonExistentPort tests deallocation edge case
func TestDeallocateNonExistentPort(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Test: Deallocate port that was never allocated (should not error)
	err := repo.DeallocatePort(ctx, 1, 8080, "TCP")
	if err != nil {
		t.Errorf("Deallocating non-existent port should not error: %v", err)
	}
}

// TestZeroSGCIDHandling tests handling of nil/zero SGCID
func TestZeroSGCIDHandling(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Test: Allocate with zero SGCID (should be treated as unallocated/reserved)
	err := repo.AllocatePort(ctx, 1, 8080, "TCP", 0)
	if err == nil {
		t.Error("Should reject allocation with zero SGCID")
	}
}

// TestConcurrentAllocation tests concurrent allocation attempts
func TestConcurrentAllocation(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Simulate two concurrent allocations of the same port
	done := make(chan error, 2)

	allocate := func(sgcID int64) {
		err := repo.AllocatePort(ctx, 1, 8080, "TCP", sgcID)
		done <- err
	}

	go allocate(100)
	go allocate(200)

	// Collect results
	errors := make([]error, 2)
	for i := 0; i < 2; i++ {
		errors[i] = <-done
	}

	// Exactly one should succeed, one should fail with conflict
	successCount := 0
	conflictCount := 0

	for _, err := range errors {
		if err == nil {
			successCount++
		} else if IsPortConflictError(err) {
			conflictCount++
		}
	}

	if successCount != 1 || conflictCount != 1 {
		t.Errorf("Expected 1 success and 1 conflict, got %d success and %d conflict", successCount, conflictCount)
	}
}

// TestPortAllocationTimestamp tests that allocation timestamps are set
func TestPortAllocationTimestamp(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	before := time.Now()
	err := repo.AllocatePort(ctx, 1, 8080, "TCP", 100)
	if err != nil {
		t.Fatalf("Failed to allocate port: %v", err)
	}
	after := time.Now()

	allocation, _ := repo.GetPortAllocation(ctx, 1, 8080, "TCP")
	if allocation.AllocatedAt.Before(before) || allocation.AllocatedAt.After(after) {
		t.Error("AllocatedAt timestamp is outside expected range")
	}
}

// Mock implementation for testing (to be replaced with real PostgreSQL implementation)
type MockServerPortRepository struct {
	allocations map[string]*manman.ServerPort
}

func NewMockServerPortRepository() *MockServerPortRepository {
	return &MockServerPortRepository{
		allocations: make(map[string]*manman.ServerPort),
	}
}

func (r *MockServerPortRepository) AllocatePort(ctx context.Context, serverID int64, port int, protocol string, sgcID int64) error {
	// Validate port range
	if port < 1 || port > 65535 {
		return &InvalidPortError{Port: port}
	}

	// Validate protocol
	if protocol != "TCP" && protocol != "UDP" {
		return &InvalidProtocolError{Protocol: protocol}
	}

	// Validate SGCID
	if sgcID <= 0 {
		return &InvalidSGCIDError{SGCID: sgcID}
	}

	key := portKey(serverID, port, protocol)

	// Check for conflict
	if existing, exists := r.allocations[key]; exists {
		return &PortConflictError{
			ServerID:     serverID,
			Port:         port,
			Protocol:     protocol,
			ExistingSGC: *existing.SGCID,
			RequestedSGC: sgcID,
		}
	}

	// Allocate port
	r.allocations[key] = &manman.ServerPort{
		ServerID:    serverID,
		Port:        port,
		Protocol:    protocol,
		SGCID:       &sgcID,
		AllocatedAt: time.Now(),
	}

	return nil
}

func (r *MockServerPortRepository) DeallocatePort(ctx context.Context, serverID int64, port int, protocol string) error {
	key := portKey(serverID, port, protocol)
	delete(r.allocations, key)
	return nil
}

func (r *MockServerPortRepository) IsPortAvailable(ctx context.Context, serverID int64, port int, protocol string) (bool, error) {
	key := portKey(serverID, port, protocol)
	_, exists := r.allocations[key]
	return !exists, nil
}

func (r *MockServerPortRepository) GetPortAllocation(ctx context.Context, serverID int64, port int, protocol string) (*manman.ServerPort, error) {
	key := portKey(serverID, port, protocol)
	allocation, exists := r.allocations[key]
	if !exists {
		return nil, &PortNotFoundError{ServerID: serverID, Port: port, Protocol: protocol}
	}
	return allocation, nil
}

func (r *MockServerPortRepository) ListAllocatedPorts(ctx context.Context, serverID int64) ([]*manman.ServerPort, error) {
	var ports []*manman.ServerPort
	for _, allocation := range r.allocations {
		if allocation.ServerID == serverID {
			ports = append(ports, allocation)
		}
	}
	return ports, nil
}

func (r *MockServerPortRepository) ListPortsBySGCID(ctx context.Context, sgcID int64) ([]*manman.ServerPort, error) {
	var ports []*manman.ServerPort
	for _, allocation := range r.allocations {
		if allocation.SGCID != nil && *allocation.SGCID == sgcID {
			ports = append(ports, allocation)
		}
	}
	return ports, nil
}

func (r *MockServerPortRepository) DeallocatePortsBySGCID(ctx context.Context, sgcID int64) error {
	keysToDelete := []string{}
	for key, allocation := range r.allocations {
		if allocation.SGCID != nil && *allocation.SGCID == sgcID {
			keysToDelete = append(keysToDelete, key)
		}
	}
	for _, key := range keysToDelete {
		delete(r.allocations, key)
	}
	return nil
}

func (r *MockServerPortRepository) AllocateMultiplePorts(ctx context.Context, serverID int64, portBindings []*manman.PortBinding, sgcID int64) error {
	// Check all ports for conflicts first
	for _, binding := range portBindings {
		key := portKey(serverID, int(binding.HostPort), binding.Protocol)
		if _, exists := r.allocations[key]; exists {
			return &PortConflictError{
				ServerID: serverID,
				Port:     int(binding.HostPort),
				Protocol: binding.Protocol,
			}
		}
	}

	// Allocate all ports
	for _, binding := range portBindings {
		if err := r.AllocatePort(ctx, serverID, int(binding.HostPort), binding.Protocol, sgcID); err != nil {
			// Rollback on error
			r.DeallocatePortsBySGCID(ctx, sgcID)
			return err
		}
	}

	return nil
}

func (r *MockServerPortRepository) GetAvailablePortsInRange(ctx context.Context, serverID int64, protocol string, startPort, endPort, limit int) ([]int, error) {
	available := []int{}
	for port := startPort; port <= endPort && len(available) < limit; port++ {
		key := portKey(serverID, port, protocol)
		if _, exists := r.allocations[key]; !exists {
			available = append(available, port)
		}
	}
	return available, nil
}

func portKey(serverID int64, port int, protocol string) string {
	return fmt.Sprintf("%d:%d:%s", serverID, port, protocol)
}
