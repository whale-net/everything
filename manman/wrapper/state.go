package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// SessionState represents the state of a session managed by the wrapper
type SessionState struct {
	SessionID       int64  `json:"session_id"`
	SGCID           int64  `json:"sgc_id"`
	Status          string `json:"status"` // "pending" | "starting" | "running" | "stopping" | "stopped" | "crashed"
	GameContainerID string `json:"game_container_id"`
	ExitCode        int    `json:"exit_code"`
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

// SaveState saves the session state to disk
func (ss *SessionState) SaveState() error {
	// Create wrapper state directory
	stateDir := "/data/wrapper"
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Marshal state to JSON
	data, err := json.MarshalIndent(ss, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Write to file atomically (write to temp file, then rename)
	statePath := filepath.Join(stateDir, "state.json")
	tempPath := statePath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}
	if err := os.Rename(tempPath, statePath); err != nil {
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// LoadState loads the session state from disk
func LoadState() (*SessionState, error) {
	statePath := "/data/wrapper/state.json"

	// Check if state file exists
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		return nil, nil // No state file, this is a fresh start
	}

	// Read state file
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	// Unmarshal JSON
	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state: %w", err)
	}

	return &state, nil
}
