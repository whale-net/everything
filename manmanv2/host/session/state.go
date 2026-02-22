package session

import (
	"io"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
)

// State represents the state of a session
type State struct {
	SessionID       int64
	SGCID           int64
	Status          string // "pending" | "starting" | "running" | "stopping" | "stopped" | "crashed"
	NetworkID       string
	NetworkName     string
	GameContainerID string
	LogReader       io.ReadCloser               // Docker logs API stream for stdout/stderr
	AttachResp      *types.HijackedResponse     // stdin attach; nil until command is sent
	AttachStrategy  string                      // "lazy" | "persistent"
	IsTTY           bool                        // Whether container uses TTY mode
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

// GetActiveSessionIDs returns a set of active session IDs
func (m *Manager) GetActiveSessionIDs() map[int64]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	activeIDs := make(map[int64]bool, len(m.sessions))
	for sessionID := range m.sessions {
		activeIDs[sessionID] = true
	}
	return activeIDs
}

// GetActiveSGCIDs returns a set of active SGC IDs
func (m *Manager) GetActiveSGCIDs() map[int64]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	activeSGCs := make(map[int64]bool)
	for _, session := range m.sessions {
		activeSGCs[session.SGCID] = true
	}
	return activeSGCs
}

// GetSessionBySGCID returns the first active session found for a given SGC ID
// Only returns sessions that are not in terminal states (crashed, stopped, lost)
func (m *Manager) GetSessionBySGCID(sgcID int64) (*State, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, session := range m.sessions {
		if session.SGCID == sgcID {
			// Only return sessions that are not in terminal states
			status := session.GetStatus()
			if status != "crashed" && status != "stopped" && status != "lost" {
				return session, true
			}
		}
	}
	return nil, false
}

// SessionStats represents session statistics
type SessionStats struct {
	Total    int
	Pending  int
	Starting int
	Running  int
	Stopping int
	Stopped  int
	Crashed  int
}

// GetSessionStats returns statistics about all sessions
func (m *Manager) GetSessionStats() SessionStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := SessionStats{
		Total: len(m.sessions),
	}

	for _, session := range m.sessions {
		switch session.GetStatus() {
		case "pending":
			stats.Pending++
		case "starting":
			stats.Starting++
		case "running":
			stats.Running++
		case "stopping":
			stats.Stopping++
		case "stopped":
			stats.Stopped++
		case "crashed":
			stats.Crashed++
		}
	}

	return stats
}
