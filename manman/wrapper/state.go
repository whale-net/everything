package main

import (
	"sync"
)

// SessionState represents the state of a session managed by the wrapper
type SessionState struct {
	SessionID       int64
	SGCID           int64
	Status          string // "pending" | "starting" | "running" | "stopping" | "stopped" | "crashed"
	GameContainerID string
	ExitCode        int
	// Add fields in future iterations:
	// - Process management details
	// - Volume paths
	// - Network information
}

// StateManager manages session state for the wrapper
type StateManager struct {
	sessions map[int64]*SessionState
	mu       sync.RWMutex
}

// NewStateManager creates a new state manager
func NewStateManager() *StateManager {
	return &StateManager{
		sessions: make(map[int64]*SessionState),
	}
}

// GetSession retrieves a session by ID
func (sm *StateManager) GetSession(sessionID int64) (*SessionState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	session, ok := sm.sessions[sessionID]
	return session, ok
}

// SetSession stores or updates a session
func (sm *StateManager) SetSession(session *SessionState) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.sessions[session.SessionID] = session
}

// RemoveSession removes a session from the manager
func (sm *StateManager) RemoveSession(sessionID int64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, sessionID)
}

// UpdateStatus updates the status of a session
func (ss *SessionState) UpdateStatus(status string) {
	ss.Status = status
}
