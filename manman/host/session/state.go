package session

import (
	"sync"
	"time"

	"github.com/whale-net/everything/manman/host/grpc"
)

// State represents the state of a session
type State struct {
	SessionID       int64
	SGCID           int64
	Status          string // "pending" | "starting" | "running" | "stopping" | "stopped" | "crashed"
	NetworkID       string
	NetworkName     string
	WrapperContainerID string
	GameContainerID string
	GRPCClient      *grpc.Client
	WrapperClient   *grpc.WrapperControlClient
	StartedAt       *time.Time
	StoppedAt       *time.Time
	ExitCode        *int
	mu              sync.RWMutex
}

// Manager manages session state
type Manager struct {
	sessions map[int64]*State
	mu       sync.RWMutex
}

// NewManager creates a new session manager
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[int64]*State),
	}
}

// GetSession gets a session by ID
func (m *Manager) GetSession(sessionID int64) (*State, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, ok := m.sessions[sessionID]
	return session, ok
}

// AddSession adds a session to the manager
func (m *Manager) AddSession(session *State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.SessionID] = session
}

// RemoveSession removes a session from the manager
func (m *Manager) RemoveSession(sessionID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
}

// ListSessions returns all sessions
func (m *Manager) ListSessions() []*State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sessions := make([]*State, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	return sessions
}

// UpdateStatus updates the status of a session
func (s *State) UpdateStatus(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = status
}

// GetStatus returns the current status
func (s *State) GetStatus() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status
}
