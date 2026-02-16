package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/host/rmq"
	"github.com/whale-net/everything/manmanv2/processor/handlers"
	"log/slog"
	"os"
)

// MockServerRepository implements repository.ServerRepository for testing
type MockServerRepository struct {
	servers     map[int64]*manman.Server
	updateCalls []UpdateCall
}

type UpdateCall struct {
	ServerID int64
	Status   string
	LastSeen time.Time
}

func NewMockServerRepository() *MockServerRepository {
	return &MockServerRepository{
		servers:     make(map[int64]*manman.Server),
		updateCalls: make([]UpdateCall, 0),
	}
}

func (m *MockServerRepository) Create(ctx context.Context, name string) (*manman.Server, error) {
	server := &manman.Server{
		ServerID: int64(len(m.servers) + 1),
		Name:     name,
		Status:   manman.ServerStatusOffline,
	}
	m.servers[server.ServerID] = server
	return server, nil
}

func (m *MockServerRepository) Get(ctx context.Context, serverID int64) (*manman.Server, error) {
	server, ok := m.servers[serverID]
	if !ok {
		return nil, &NotFoundError{ID: serverID}
	}
	return server, nil
}

func (m *MockServerRepository) GetByName(ctx context.Context, name string) (*manman.Server, error) {
	for _, server := range m.servers {
		if server.Name == name {
			return server, nil
		}
	}
	return nil, &NotFoundError{Name: name}
}

func (m *MockServerRepository) List(ctx context.Context, limit, offset int) ([]*manman.Server, error) {
	servers := make([]*manman.Server, 0, len(m.servers))
	for _, server := range m.servers {
		servers = append(servers, server)
	}
	return servers, nil
}

func (m *MockServerRepository) Update(ctx context.Context, server *manman.Server) error {
	m.servers[server.ServerID] = server
	return nil
}

func (m *MockServerRepository) Delete(ctx context.Context, serverID int64) error {
	delete(m.servers, serverID)
	return nil
}

func (m *MockServerRepository) UpdateStatusAndLastSeen(ctx context.Context, serverID int64, status string, lastSeen time.Time) error {
	server, ok := m.servers[serverID]
	if !ok {
		return &NotFoundError{ID: serverID}
	}
	server.Status = status
	server.LastSeen = &lastSeen
	m.updateCalls = append(m.updateCalls, UpdateCall{ServerID: serverID, Status: status, LastSeen: lastSeen})
	return nil
}

func (m *MockServerRepository) UpdateLastSeen(ctx context.Context, serverID int64, lastSeen time.Time) error {
	server, ok := m.servers[serverID]
	if !ok {
		return &NotFoundError{ID: serverID}
	}
	server.LastSeen = &lastSeen
	return nil
}

func (m *MockServerRepository) ListStaleServers(ctx context.Context, thresholdSeconds int) ([]*manman.Server, error) {
	stale := make([]*manman.Server, 0)
	threshold := time.Now().Add(-time.Duration(thresholdSeconds) * time.Second)

	for _, server := range m.servers {
		if server.Status == manman.ServerStatusOnline && server.LastSeen != nil && server.LastSeen.Before(threshold) {
			stale = append(stale, server)
		}
	}
	return stale, nil
}

func (m *MockServerRepository) MarkServersOffline(ctx context.Context, serverIDs []int64) error {
	for _, id := range serverIDs {
		if server, ok := m.servers[id]; ok {
			server.Status = manman.ServerStatusOffline
		}
	}
	return nil
}

// MockSessionRepository implements repository.SessionRepository for testing
type MockSessionRepository struct {
	sessions map[int64]*manman.Session
}

func NewMockSessionRepository() *MockSessionRepository {
	return &MockSessionRepository{
		sessions: make(map[int64]*manman.Session),
	}
}

func (m *MockSessionRepository) Create(ctx context.Context, session *manman.Session) (*manman.Session, error) {
	session.SessionID = int64(len(m.sessions) + 1)
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = time.Now()
	}
	m.sessions[session.SessionID] = session
	return session, nil
}

func (m *MockSessionRepository) Get(ctx context.Context, sessionID int64) (*manman.Session, error) {
	session, ok := m.sessions[sessionID]
	if !ok {
		return nil, &NotFoundError{ID: sessionID}
	}
	return session, nil
}

func (m *MockSessionRepository) List(ctx context.Context, sgcID *int64, limit, offset int) ([]*manman.Session, error) {
	sessions := make([]*manman.Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func (m *MockSessionRepository) ListWithFilters(ctx context.Context, filters *repository.SessionFilters, limit, offset int) ([]*manman.Session, error) {
	return m.List(ctx, nil, limit, offset)
}

func (m *MockSessionRepository) Update(ctx context.Context, session *manman.Session) error {
	session.UpdatedAt = time.Now()
	m.sessions[session.SessionID] = session
	return nil
}

func (m *MockSessionRepository) UpdateStatus(ctx context.Context, sessionID int64, status string) error {
	session, ok := m.sessions[sessionID]
	if !ok {
		return &NotFoundError{ID: sessionID}
	}
	session.Status = status
	session.UpdatedAt = time.Now()
	return nil
}

func (m *MockSessionRepository) UpdateSessionStart(ctx context.Context, sessionID int64, startedAt time.Time) error {
	session, ok := m.sessions[sessionID]
	if !ok {
		return &NotFoundError{ID: sessionID}
	}
	session.Status = manman.SessionStatusRunning
	session.StartedAt = &startedAt
	session.UpdatedAt = time.Now()
	return nil
}

func (m *MockSessionRepository) UpdateSessionEnd(ctx context.Context, sessionID int64, status string, endedAt time.Time, exitCode *int) error {
	session, ok := m.sessions[sessionID]
	if !ok {
		return &NotFoundError{ID: sessionID}
	}
	session.Status = status
	session.EndedAt = &endedAt
	session.ExitCode = exitCode
	session.UpdatedAt = time.Now()
	return nil
}

func (m *MockSessionRepository) GetStaleSessions(ctx context.Context, threshold time.Duration) ([]*manman.Session, error) {
	stale := make([]*manman.Session, 0)
	cutoff := time.Now().Add(-threshold)

	for _, session := range m.sessions {
		// Check status is one of pending, starting, stopping
		if session.Status != manman.SessionStatusPending &&
			session.Status != manman.SessionStatusStarting &&
			session.Status != manman.SessionStatusStopping {
			continue
		}

		// Check if updated_at is before cutoff
		if session.UpdatedAt.Before(cutoff) {
			stale = append(stale, session)
		}
	}
	return stale, nil
}

func (m *MockSessionRepository) StopOtherSessionsForSGC(ctx context.Context, sessionID int64, sgcID int64) error {
	for id, session := range m.sessions {
		if session.SGCID == sgcID && id != sessionID {
			session.Status = manman.SessionStatusStopped
			now := time.Now()
			session.EndedAt = &now
			session.UpdatedAt = now
		}
	}
	return nil
}

// NotFoundError represents an entity not found error
type NotFoundError struct {
	ID   int64
	Name string
}

func (e *NotFoundError) Error() string {
	if e.Name != "" {
		return "entity not found: " + e.Name
	}
	return "entity not found"
}

// MockPublisher implements handlers.Publisher for testing
type MockPublisher struct {
	PublishedEvents []PublishedEvent
}

type PublishedEvent struct {
	RoutingKey string
	Message    interface{}
}

func NewMockPublisher() *MockPublisher {
	return &MockPublisher{
		PublishedEvents: make([]PublishedEvent, 0),
	}
}

func (m *MockPublisher) PublishExternal(ctx context.Context, routingKey string, message interface{}) error {
	m.PublishedEvents = append(m.PublishedEvents, PublishedEvent{
		RoutingKey: routingKey,
		Message:    message,
	})
	return nil
}

// Integration Tests

func TestHostStatusUpdateFlow(t *testing.T) {
	// Setup
	serverRepo := NewMockServerRepository()
	publisher := NewMockPublisher()

	// Create a server
	server, err := serverRepo.Create(context.Background(), "test-server")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Simulate host status update message
	msg := rmq.HostStatusUpdate{
		ServerID: server.ServerID,
		Status:   manman.ServerStatusOnline,
	}

	// Process the message (simulate handler)
	ctx := context.Background()
	now := time.Now()
	err = serverRepo.UpdateStatusAndLastSeen(ctx, msg.ServerID, msg.Status, now)
	if err != nil {
		t.Fatalf("Failed to update server status: %v", err)
	}

	// Verify database update
	updated, err := serverRepo.Get(ctx, server.ServerID)
	if err != nil {
		t.Fatalf("Failed to get updated server: %v", err)
	}

	if updated.Status != manman.ServerStatusOnline {
		t.Errorf("Expected status %q, got %q", manman.ServerStatusOnline, updated.Status)
	}

	if updated.LastSeen == nil {
		t.Error("Expected LastSeen to be set")
	}

	// Simulate external publishing
	publisher.PublishExternal(ctx, "manman.host.online", msg)

	// Verify external event published
	if len(publisher.PublishedEvents) != 1 {
		t.Fatalf("Expected 1 published event, got %d", len(publisher.PublishedEvents))
	}

	event := publisher.PublishedEvents[0]
	if event.RoutingKey != "manman.host.online" {
		t.Errorf("Expected routing key %q, got %q", "manman.host.online", event.RoutingKey)
	}
}

func TestSessionLifecycleFlow(t *testing.T) {
	// Setup
	sessionRepo := NewMockSessionRepository()
	publisher := NewMockPublisher()

	// Create a session in pending state
	session, err := sessionRepo.Create(context.Background(), &manman.Session{
		SGCID:  1,
		Status: manman.SessionStatusPending,
	})
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	ctx := context.Background()

	// Test: pending -> starting
	err = sessionRepo.UpdateStatus(ctx, session.SessionID, manman.SessionStatusStarting)
	if err != nil {
		t.Fatalf("Failed to update to starting: %v", err)
	}

	// Test: starting -> running
	startTime := time.Now()
	err = sessionRepo.UpdateSessionStart(ctx, session.SessionID, startTime)
	if err != nil {
		t.Fatalf("Failed to update to running: %v", err)
	}

	publisher.PublishExternal(ctx, "manman.session.running", rmq.SessionStatusUpdate{
		SessionID: session.SessionID,
		Status:    manman.SessionStatusRunning,
	})

	// Verify session is running
	updated, _ := sessionRepo.Get(ctx, session.SessionID)
	if updated.Status != manman.SessionStatusRunning {
		t.Errorf("Expected status %q, got %q", manman.SessionStatusRunning, updated.Status)
	}
	if updated.StartedAt == nil {
		t.Error("Expected StartedAt to be set")
	}

	// Test: running -> stopping
	err = sessionRepo.UpdateStatus(ctx, session.SessionID, manman.SessionStatusStopping)
	if err != nil {
		t.Fatalf("Failed to update to stopping: %v", err)
	}

	// Test: stopping -> stopped
	endTime := time.Now()
	exitCode := 0
	err = sessionRepo.UpdateSessionEnd(ctx, session.SessionID, manman.SessionStatusStopped, endTime, &exitCode)
	if err != nil {
		t.Fatalf("Failed to update to stopped: %v", err)
	}

	publisher.PublishExternal(ctx, "manman.session.stopped", rmq.SessionStatusUpdate{
		SessionID: session.SessionID,
		Status:    manman.SessionStatusStopped,
		ExitCode:  &exitCode,
	})

	// Verify final state
	final, _ := sessionRepo.Get(ctx, session.SessionID)
	if final.Status != manman.SessionStatusStopped {
		t.Errorf("Expected status %q, got %q", manman.SessionStatusStopped, final.Status)
	}
	if final.EndedAt == nil {
		t.Error("Expected EndedAt to be set")
	}
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Error("Expected exit code 0")
	}

	// Verify 2 external events published (running, stopped)
	if len(publisher.PublishedEvents) != 2 {
		t.Errorf("Expected 2 published events, got %d", len(publisher.PublishedEvents))
	}
}

func TestSessionCrashScenario(t *testing.T) {
	sessionRepo := NewMockSessionRepository()
	publisher := NewMockPublisher()

	// Create session in running state
	session, _ := sessionRepo.Create(context.Background(), &manman.Session{
		SGCID:  1,
		Status: manman.SessionStatusRunning,
	})

	ctx := context.Background()
	now := time.Now()

	// Session crashes
	exitCode := 1
	err := sessionRepo.UpdateSessionEnd(ctx, session.SessionID, manman.SessionStatusCrashed, now, &exitCode)
	if err != nil {
		t.Fatalf("Failed to update to crashed: %v", err)
	}

	publisher.PublishExternal(ctx, "manman.session.crashed", rmq.SessionStatusUpdate{
		SessionID: session.SessionID,
		Status:    manman.SessionStatusCrashed,
		ExitCode:  &exitCode,
	})

	// Verify crash state
	crashed, _ := sessionRepo.Get(ctx, session.SessionID)
	if crashed.Status != manman.SessionStatusCrashed {
		t.Errorf("Expected status %q, got %q", manman.SessionStatusCrashed, crashed.Status)
	}
	if crashed.ExitCode == nil || *crashed.ExitCode != 1 {
		t.Error("Expected non-zero exit code")
	}

	// Verify external event
	if len(publisher.PublishedEvents) != 1 {
		t.Fatalf("Expected 1 published event, got %d", len(publisher.PublishedEvents))
	}
	if publisher.PublishedEvents[0].RoutingKey != "manman.session.crashed" {
		t.Errorf("Expected routing key %q, got %q", "manman.session.crashed", publisher.PublishedEvents[0].RoutingKey)
	}
}

func TestStaleHostDetection(t *testing.T) {
	serverRepo := NewMockServerRepository()
	publisher := NewMockPublisher()

	ctx := context.Background()

	// Create online server with old last_seen
	server, _ := serverRepo.Create(ctx, "stale-server")
	oldTime := time.Now().Add(-30 * time.Second)
	serverRepo.UpdateStatusAndLastSeen(ctx, server.ServerID, manman.ServerStatusOnline, oldTime)

	// Check for stale servers (10 second threshold)
	staleServers, err := serverRepo.ListStaleServers(ctx, 10)
	if err != nil {
		t.Fatalf("Failed to list stale servers: %v", err)
	}

	if len(staleServers) != 1 {
		t.Fatalf("Expected 1 stale server, got %d", len(staleServers))
	}

	if staleServers[0].ServerID != server.ServerID {
		t.Errorf("Expected server ID %d, got %d", server.ServerID, staleServers[0].ServerID)
	}

	// Mark servers offline
	serverIDs := []int64{server.ServerID}
	err = serverRepo.MarkServersOffline(ctx, serverIDs)
	if err != nil {
		t.Fatalf("Failed to mark servers offline: %v", err)
	}

	// Publish stale event
	publisher.PublishExternal(ctx, "manman.host.stale", rmq.HostStatusUpdate{
		ServerID: server.ServerID,
		Status:   manman.ServerStatusOffline,
	})

	// Verify server is offline
	updated, _ := serverRepo.Get(ctx, server.ServerID)
	if updated.Status != manman.ServerStatusOffline {
		t.Errorf("Expected status %q, got %q", manman.ServerStatusOffline, updated.Status)
	}

	// Verify stale event published
	if len(publisher.PublishedEvents) != 1 {
		t.Fatalf("Expected 1 published event, got %d", len(publisher.PublishedEvents))
	}
	if publisher.PublishedEvents[0].RoutingKey != "manman.host.stale" {
		t.Errorf("Expected routing key %q, got %q", "manman.host.stale", publisher.PublishedEvents[0].RoutingKey)
	}
}

func TestStaleSessionDetection(t *testing.T) {
	// Setup
	sessionRepo := NewMockSessionRepository()
	publisher := NewMockPublisher()

	// Create a "stale" session (updated long ago)
	oldTime := time.Now().Add(-10 * time.Minute)
	session, _ := sessionRepo.Create(context.Background(), &manman.Session{
		SGCID:     1,
		Status:    manman.SessionStatusStarting,
		UpdatedAt: oldTime,
	})

	// Create repository wrapper
	repo := &repository.Repository{
		Sessions: sessionRepo,
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := handlers.NewSessionStatusHandler(repo, publisher, logger)

	// Start checker with short interval and threshold
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 5 minute threshold matches our oldTime (-10m)
	// 1ms check interval so it runs immediately
	handler.StartStaleSessionChecker(ctx, 1*time.Millisecond, 5*time.Minute)

	// Wait for checker to run
	time.Sleep(100 * time.Millisecond)

	// Verify session marked as lost
	updated, _ := sessionRepo.Get(ctx, session.SessionID)
	if updated.Status != manman.SessionStatusLost {
		t.Errorf("Expected status %q, got %q", manman.SessionStatusLost, updated.Status)
	}
	if updated.EndedAt == nil {
		t.Error("Expected EndedAt to be set")
	}

	// Verify "lost" event published
	if len(publisher.PublishedEvents) != 1 {
		t.Fatalf("Expected 1 published event, got %d", len(publisher.PublishedEvents))
	}
	if publisher.PublishedEvents[0].RoutingKey != "manman.session.lost" {
		t.Errorf("Expected routing key %q, got %q", "manman.session.lost", publisher.PublishedEvents[0].RoutingKey)
	}
}

func TestHealthHeartbeatFlow(t *testing.T) {
	serverRepo := NewMockServerRepository()

	ctx := context.Background()

	// Create server
	server, _ := serverRepo.Create(ctx, "health-test")

	// Send heartbeat
	msg := rmq.HealthUpdate{
		ServerID: server.ServerID,
		SessionStats: &rmq.SessionStats{
			Total:   5,
			Running: 2,
			Stopped: 3,
		},
	}

	// Update last_seen
	now := time.Now()
	err := serverRepo.UpdateLastSeen(ctx, msg.ServerID, now)
	if err != nil {
		t.Fatalf("Failed to update last_seen: %v", err)
	}

	// Verify last_seen updated
	updated, _ := serverRepo.Get(ctx, server.ServerID)
	if updated.LastSeen == nil {
		t.Error("Expected LastSeen to be set")
	}

	// Verify timestamp is recent
	if time.Since(*updated.LastSeen) > time.Second {
		t.Error("LastSeen timestamp is not recent")
	}
}

func TestMessageSerialization(t *testing.T) {
	// Test HostStatusUpdate serialization
	hostMsg := rmq.HostStatusUpdate{
		ServerID: 42,
		Status:   "online",
	}

	data, err := json.Marshal(hostMsg)
	if err != nil {
		t.Fatalf("Failed to marshal HostStatusUpdate: %v", err)
	}

	var decoded rmq.HostStatusUpdate
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Failed to unmarshal HostStatusUpdate: %v", err)
	}

	if decoded.ServerID != hostMsg.ServerID || decoded.Status != hostMsg.Status {
		t.Error("HostStatusUpdate deserialization mismatch")
	}

	// Test SessionStatusUpdate serialization
	exitCode := 0
	sessionMsg := rmq.SessionStatusUpdate{
		SessionID: 123,
		SGCID:     456,
		Status:    "running",
		ExitCode:  &exitCode,
	}

	data, err = json.Marshal(sessionMsg)
	if err != nil {
		t.Fatalf("Failed to marshal SessionStatusUpdate: %v", err)
	}

	var decodedSession rmq.SessionStatusUpdate
	err = json.Unmarshal(data, &decodedSession)
	if err != nil {
		t.Fatalf("Failed to unmarshal SessionStatusUpdate: %v", err)
	}

	if decodedSession.SessionID != sessionMsg.SessionID {
		t.Error("SessionStatusUpdate deserialization mismatch")
	}
}

func TestErrorHandlingScenarios(t *testing.T) {
	serverRepo := NewMockServerRepository()
	sessionRepo := NewMockSessionRepository()

	ctx := context.Background()

	// Test: Update non-existent server
	err := serverRepo.UpdateStatusAndLastSeen(ctx, 99999, "online", time.Now())
	if err == nil {
		t.Error("Expected error for non-existent server")
	}

	// Test: Update non-existent session
	err = sessionRepo.UpdateStatus(ctx, 99999, "running")
	if err == nil {
		t.Error("Expected error for non-existent session")
	}

	// Test: Get non-existent server
	_, err = serverRepo.Get(ctx, 99999)
	if err == nil {
		t.Error("Expected error for non-existent server")
	}
}
