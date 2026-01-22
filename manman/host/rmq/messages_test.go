package rmq_test

import (
	"encoding/json"
	"testing"

	"github.com/whale-net/everything/manman/host/rmq"
)

func TestStartSessionCommand_MarshalUnmarshal(t *testing.T) {
	cmd := rmq.StartSessionCommand{
		SessionID: 123,
		SGCID:     456,
		GameConfig: map[string]interface{}{
			"config_id": float64(789),
			"name":      "test-config",
		},
		ServerGameConfig: map[string]interface{}{
			"sgc_id": float64(101),
		},
		Parameters: map[string]interface{}{
			"max_players": float64(20),
		},
	}

	// Marshal
	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("Failed to marshal command: %v", err)
	}

	// Unmarshal
	var unmarshaled rmq.StartSessionCommand
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal command: %v", err)
	}

	// Verify
	if unmarshaled.SessionID != cmd.SessionID {
		t.Errorf("Expected SessionID %d, got %d", cmd.SessionID, unmarshaled.SessionID)
	}
	if unmarshaled.SGCID != cmd.SGCID {
		t.Errorf("Expected SGCID %d, got %d", cmd.SGCID, unmarshaled.SGCID)
	}
}

func TestStopSessionCommand_MarshalUnmarshal(t *testing.T) {
	cmd := rmq.StopSessionCommand{
		SessionID: 123,
		Force:     true,
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("Failed to marshal command: %v", err)
	}

	var unmarshaled rmq.StopSessionCommand
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal command: %v", err)
	}

	if unmarshaled.SessionID != cmd.SessionID {
		t.Errorf("Expected SessionID %d, got %d", cmd.SessionID, unmarshaled.SessionID)
	}
	if unmarshaled.Force != cmd.Force {
		t.Errorf("Expected Force %v, got %v", cmd.Force, unmarshaled.Force)
	}
}

func TestKillSessionCommand_MarshalUnmarshal(t *testing.T) {
	cmd := rmq.KillSessionCommand{
		SessionID: 123,
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("Failed to marshal command: %v", err)
	}

	var unmarshaled rmq.KillSessionCommand
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal command: %v", err)
	}

	if unmarshaled.SessionID != cmd.SessionID {
		t.Errorf("Expected SessionID %d, got %d", cmd.SessionID, unmarshaled.SessionID)
	}
}

func TestSessionStatusUpdate_MarshalUnmarshal(t *testing.T) {
	exitCode := 1
	update := rmq.SessionStatusUpdate{
		SessionID: 123,
		SGCID:     456,
		Status:    "running",
		ExitCode:  &exitCode,
	}

	data, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("Failed to marshal update: %v", err)
	}

	var unmarshaled rmq.SessionStatusUpdate
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal update: %v", err)
	}

	if unmarshaled.SessionID != update.SessionID {
		t.Errorf("Expected SessionID %d, got %d", update.SessionID, unmarshaled.SessionID)
	}
	if unmarshaled.Status != update.Status {
		t.Errorf("Expected Status %s, got %s", update.Status, unmarshaled.Status)
	}
	if unmarshaled.ExitCode == nil || *unmarshaled.ExitCode != exitCode {
		t.Errorf("Expected ExitCode %d, got %v", exitCode, unmarshaled.ExitCode)
	}
}

func TestHostStatusUpdate_MarshalUnmarshal(t *testing.T) {
	update := rmq.HostStatusUpdate{
		ServerID: 789,
		Status:   "online",
	}

	data, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("Failed to marshal update: %v", err)
	}

	var unmarshaled rmq.HostStatusUpdate
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal update: %v", err)
	}

	if unmarshaled.ServerID != update.ServerID {
		t.Errorf("Expected ServerID %d, got %d", update.ServerID, unmarshaled.ServerID)
	}
	if unmarshaled.Status != update.Status {
		t.Errorf("Expected Status %s, got %s", update.Status, unmarshaled.Status)
	}
}
