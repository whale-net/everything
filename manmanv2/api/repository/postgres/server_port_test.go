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
	if allocation.SessionID == nil || *allocation.SessionID != 100 {
		t.Errorf("Expected SessionID 100, got %v", allocation.SessionID)
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

	// Allocate port for SessionID 100
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

	// Test: List ports for SessionID 100
	ports, err = repo.ListPortsBySessionID(ctx, 100)
	if err != nil {
		t.Fatalf("Failed to list ports by SGCID: %v", err)
	}
	if len(ports) != 2 {
		t.Errorf("Expected 2 ports for SessionID 100, got %d", len(ports))
	}
}

// TestDeallocateBySGCID tests deallocating all ports for a ServerGameConfig
func TestDeallocateBySGCID(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Allocate ports for SessionID 100
	repo.AllocatePort(ctx, 1, 8080, "TCP", 100)
	repo.AllocatePort(ctx, 1, 8081, "TCP", 100)
	repo.AllocatePort(ctx, 1, 8082, "UDP", 100)

	// Allocate port for different SGCID
	repo.AllocatePort(ctx, 1, 9000, "TCP", 200)

	// Test: Deallocate all ports for SessionID 100
	err := repo.DeallocatePortsBySessionID(ctx, 100)
	if err != nil {
		t.Fatalf("Failed to deallocate ports by SGCID: %v", err)
	}

	// Verify SessionID 100 ports are deallocated
	ports, _ := repo.ListPortsBySessionID(ctx, 100)
	if len(ports) != 0 {
		t.Errorf("Expected 0 ports for SessionID 100 after deallocation, got %d", len(ports))
	}

	// Verify SessionID 200 port is still allocated
	ports, _ = repo.ListPortsBySessionID(ctx, 200)
	if len(ports) != 1 {
		t.Errorf("Expected 1 port for SessionID 200, got %d", len(ports))
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

	// Verify NO ports were allocated for SessionID 100 (transaction rollback)
	ports, _ := repo.ListPortsBySessionID(ctx, 100)
	if len(ports) != 0 {
		t.Errorf("Expected 0 ports allocated after failed batch, got %d", len(ports))
	}

	// Verify original allocation still exists
	allocation, _ := repo.GetPortAllocation(ctx, 1, 8080, "TCP")
	if allocation.SessionID == nil || *allocation.SessionID != 999 {
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

// TestTCPandUDPSamePort tests that TCP and UDP can use the same port number
// This is the core requirement for games like L4D2 that need both 27015/TCP and 27015/UDP
func TestTCPandUDPSamePort(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Allocate port 27015/TCP for session 100
	err := repo.AllocatePort(ctx, 1, 27015, "TCP", 100)
	if err != nil {
		t.Fatalf("Failed to allocate 27015/TCP: %v", err)
	}

	// Allocate port 27015/UDP for the same session - should succeed
	err = repo.AllocatePort(ctx, 1, 27015, "UDP", 100)
	if err != nil {
		t.Fatalf("Should allow 27015/UDP when 27015/TCP is allocated: %v", err)
	}

	// Verify both are allocated
	tcpAllocation, err := repo.GetPortAllocation(ctx, 1, 27015, "TCP")
	if err != nil {
		t.Errorf("Failed to get TCP allocation: %v", err)
	}
	if tcpAllocation.Protocol != "TCP" {
		t.Errorf("Expected TCP protocol, got %s", tcpAllocation.Protocol)
	}

	udpAllocation, err := repo.GetPortAllocation(ctx, 1, 27015, "UDP")
	if err != nil {
		t.Errorf("Failed to get UDP allocation: %v", err)
	}
	if udpAllocation.Protocol != "UDP" {
		t.Errorf("Expected UDP protocol, got %s", udpAllocation.Protocol)
	}

	// Verify both show as unavailable for other sessions
	tcpAvailable, _ := repo.IsPortAvailable(ctx, 1, 27015, "TCP")
	if tcpAvailable {
		t.Error("27015/TCP should not be available")
	}

	udpAvailable, _ := repo.IsPortAvailable(ctx, 1, 27015, "UDP")
	if udpAvailable {
		t.Error("27015/UDP should not be available")
	}
}

// TestAllocateMultiplePortsBothProtocols tests allocating multiple ports with both TCP and UDP
// This simulates the L4D2 deployment scenario where we need 27015/TCP and 27015/UDP
func TestAllocateMultiplePortsBothProtocols(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Simulate L4D2 port requirements
	portBindings := []*manman.PortBinding{
		{ContainerPort: 27015, HostPort: 27015, Protocol: "TCP"},
		{ContainerPort: 27015, HostPort: 27015, Protocol: "UDP"},
	}

	// Allocate both ports for session 100
	err := repo.AllocateMultiplePorts(ctx, 1, portBindings, 100)
	if err != nil {
		t.Fatalf("Failed to allocate L4D2 ports: %v", err)
	}

	// Verify both are allocated
	ports, err := repo.ListPortsBySessionID(ctx, 100)
	if err != nil {
		t.Fatalf("Failed to list ports: %v", err)
	}

	if len(ports) != 2 {
		t.Fatalf("Expected 2 ports allocated, got %d", len(ports))
	}

	// Verify we have one TCP and one UDP
	protocolCount := make(map[string]int)
	for _, port := range ports {
		protocolCount[port.Protocol]++
		if port.Port != 27015 {
			t.Errorf("Expected port 27015, got %d", port.Port)
		}
	}

	if protocolCount["TCP"] != 1 {
		t.Errorf("Expected 1 TCP allocation, got %d", protocolCount["TCP"])
	}
	if protocolCount["UDP"] != 1 {
		t.Errorf("Expected 1 UDP allocation, got %d", protocolCount["UDP"])
	}
}

// TestPortConflictWithSameProtocolOnly tests that port conflicts only occur with same protocol
func TestPortConflictWithSameProtocolOnly(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Session 1 allocates 27015/TCP
	err := repo.AllocatePort(ctx, 1, 27015, "TCP", 100)
	if err != nil {
		t.Fatalf("Failed to allocate 27015/TCP for session 100: %v", err)
	}

	// Session 2 tries to allocate 27015/TCP - should fail
	err = repo.AllocatePort(ctx, 1, 27015, "TCP", 200)
	if err == nil {
		t.Error("Expected conflict when allocating 27015/TCP for different session")
	}
	if !IsPortConflictError(err) {
		t.Errorf("Expected PortConflictError, got %v", err)
	}

	// Session 2 tries to allocate 27015/UDP - should succeed
	err = repo.AllocatePort(ctx, 1, 27015, "UDP", 200)
	if err != nil {
		t.Errorf("Should allow 27015/UDP for session 200 when only TCP is taken: %v", err)
	}

	// Session 3 tries to allocate 27015/UDP - should fail (already taken by session 2)
	err = repo.AllocatePort(ctx, 1, 27015, "UDP", 300)
	if err == nil {
		t.Error("Expected conflict when allocating 27015/UDP for different session")
	}
	if !IsPortConflictError(err) {
		t.Errorf("Expected PortConflictError, got %v", err)
	}
}

// TestMultipleSessionsWithMixedProtocols tests realistic multi-session scenario
func TestMultipleSessionsWithMixedProtocols(t *testing.T) {
	ctx := context.Background()
	repo := NewMockServerPortRepository()

	// Session 1: L4D2 server using ports 27015 TCP/UDP
	l4d2Ports := []*manman.PortBinding{
		{ContainerPort: 27015, HostPort: 27015, Protocol: "TCP"},
		{ContainerPort: 27015, HostPort: 27015, Protocol: "UDP"},
	}
	err := repo.AllocateMultiplePorts(ctx, 1, l4d2Ports, 100)
	if err != nil {
		t.Fatalf("Failed to allocate L4D2 ports: %v", err)
	}

	// Session 2: CS2 server using different ports 27016 TCP/UDP - should succeed
	cs2Ports := []*manman.PortBinding{
		{ContainerPort: 27015, HostPort: 27016, Protocol: "TCP"},
		{ContainerPort: 27015, HostPort: 27016, Protocol: "UDP"},
		{ContainerPort: 27020, HostPort: 27020, Protocol: "UDP"},
	}
	err = repo.AllocateMultiplePorts(ctx, 1, cs2Ports, 200)
	if err != nil {
		t.Fatalf("Failed to allocate CS2 ports: %v", err)
	}

	// Session 3: Tries to use 27015/TCP - should fail
	minecraftPorts := []*manman.PortBinding{
		{ContainerPort: 25565, HostPort: 27015, Protocol: "TCP"},
	}
	err = repo.AllocateMultiplePorts(ctx, 1, minecraftPorts, 300)
	if err == nil {
		t.Error("Expected conflict when trying to allocate already-used port 27015/TCP")
	}

	// Verify total allocations
	allPorts, err := repo.ListAllocatedPorts(ctx, 1)
	if err != nil {
		t.Fatalf("Failed to list all ports: %v", err)
	}
	if len(allPorts) != 5 {
		t.Errorf("Expected 5 total port allocations (2 L4D2 + 3 CS2), got %d", len(allPorts))
	}

	// Deallocate L4D2 session
	err = repo.DeallocatePortsBySessionID(ctx, 100)
	if err != nil {
		t.Fatalf("Failed to deallocate L4D2 ports: %v", err)
	}

	// Now Session 3 should be able to allocate 27015/TCP
	err = repo.AllocateMultiplePorts(ctx, 1, minecraftPorts, 300)
	if err != nil {
		t.Errorf("Should be able to allocate 27015/TCP after L4D2 session ended: %v", err)
	}

	// But 27015/UDP should still be blocked (CS2 doesn't use it, so it should be free)
	// Actually, L4D2 had both TCP and UDP, and we deallocated both, so UDP should be free too
	udpAvailable, _ := repo.IsPortAvailable(ctx, 1, 27015, "UDP")
	if !udpAvailable {
		t.Error("27015/UDP should be available after L4D2 session ended")
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

func (r *MockServerPortRepository) AllocatePort(ctx context.Context, serverID int64, port int, protocol string, sessionID int64) error {
	// Validate port range
	if port < 1 || port > 65535 {
		return &InvalidPortError{Port: port}
	}

	// Validate protocol
	if protocol != "TCP" && protocol != "UDP" {
		return &InvalidProtocolError{Protocol: protocol}
	}

	// Validate SessionID
	if sessionID <= 0 {
		return &InvalidSessionIDError{SessionID: sessionID}
	}

	key := portKey(serverID, port, protocol)

	// Check for conflict
	if existing, exists := r.allocations[key]; exists {
		existingID := int64(0)
		if existing.SessionID != nil {
			existingID = *existing.SessionID
		}
		return &PortConflictError{
			ServerID:     serverID,
			Port:         port,
			Protocol:     protocol,
			ExistingSGC:  existingID,
			RequestedSGC: sessionID,
		}
	}

	// Allocate port
	r.allocations[key] = &manman.ServerPort{
		ServerID:    serverID,
		Port:        port,
		Protocol:    protocol,
		SessionID:   &sessionID,
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

func (r *MockServerPortRepository) ListPortsBySessionID(ctx context.Context, sessionID int64) ([]*manman.ServerPort, error) {
	var ports []*manman.ServerPort
	for _, allocation := range r.allocations {
		if allocation.SessionID != nil && *allocation.SessionID == sessionID {
			ports = append(ports, allocation)
		}
	}
	return ports, nil
}

func (r *MockServerPortRepository) DeallocatePortsBySessionID(ctx context.Context, sessionID int64) error {
	keysToDelete := []string{}
	for key, allocation := range r.allocations {
		if allocation.SessionID != nil && *allocation.SessionID == sessionID {
			keysToDelete = append(keysToDelete, key)
		}
	}
	for _, key := range keysToDelete {
		delete(r.allocations, key)
	}
	return nil
}

func (r *MockServerPortRepository) AllocateMultiplePorts(ctx context.Context, serverID int64, portBindings []*manman.PortBinding, sessionID int64) error {
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
		if err := r.AllocatePort(ctx, serverID, int(binding.HostPort), binding.Protocol, sessionID); err != nil {
			// Rollback on error
			r.DeallocatePortsBySessionID(ctx, sessionID)
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
