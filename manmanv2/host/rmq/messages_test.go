package rmq_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2/host/rmq"
)

func TestStartSessionCommand_MarshalUnmarshal(t *testing.T) {
	cmd := rmq.StartSessionCommand{
		SessionID: 123,
		SGCID:     456,
		GameConfig: rmq.GameConfigMessage{
			ConfigID:     789,
			Image:        "test-image:latest",
			ArgsTemplate: "-port {{.Port}}",
			EnvTemplate: map[string]string{
				"GAME_MODE": "survival",
			},
		},
		ServerGameConfig: rmq.ServerGameConfigMessage{
			SGCID: 101,
			PortBindings: []rmq.PortBindingMessage{
				{ContainerPort: 27015, HostPort: 27015, Protocol: "UDP"},
			},
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

func TestSendInputCommand_MarshalUnmarshal(t *testing.T) {
	cmd := rmq.SendInputCommand{
		SessionID: 123,
		Input:     []byte("ping\n"),
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("Failed to marshal command: %v", err)
	}

	var unmarshaled rmq.SendInputCommand
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal command: %v", err)
	}

	if unmarshaled.SessionID != cmd.SessionID {
		t.Errorf("Expected SessionID %d, got %d", cmd.SessionID, unmarshaled.SessionID)
	}
	if string(unmarshaled.Input) != string(cmd.Input) {
		t.Errorf("Expected Input %q, got %q", string(cmd.Input), string(unmarshaled.Input))
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

// TestWorkshopCacheStatusUpdate_MarshalUnmarshal round-trips the new #2184 message. New
// message, new routing key -- additive alongside the existing status.* contract (NFR3),
// see WorkshopCacheStatusUpdate's doc comment.
func TestWorkshopCacheStatusUpdate_MarshalUnmarshal(t *testing.T) {
	verifiedAt := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	update := rmq.WorkshopCacheStatusUpdate{
		ServerID:       7,
		WorkshopID:     "987654321",
		ContentVersion: "17000001",
		CacheEntryID:   42,
		Event:          "refreshed",
		SizeBytes:      12345,
		VerifiedAt:     verifiedAt,
	}

	data, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("Failed to marshal update: %v", err)
	}

	var unmarshaled rmq.WorkshopCacheStatusUpdate
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal update: %v", err)
	}

	if unmarshaled.ServerID != update.ServerID {
		t.Errorf("Expected ServerID %d, got %d", update.ServerID, unmarshaled.ServerID)
	}
	if unmarshaled.WorkshopID != update.WorkshopID {
		t.Errorf("Expected WorkshopID %s, got %s", update.WorkshopID, unmarshaled.WorkshopID)
	}
	if unmarshaled.ContentVersion != update.ContentVersion {
		t.Errorf("Expected ContentVersion %s, got %s", update.ContentVersion, unmarshaled.ContentVersion)
	}
	if unmarshaled.CacheEntryID != update.CacheEntryID {
		t.Errorf("Expected CacheEntryID %d, got %d", update.CacheEntryID, unmarshaled.CacheEntryID)
	}
	if unmarshaled.Event != update.Event {
		t.Errorf("Expected Event %s, got %s", update.Event, unmarshaled.Event)
	}
	if unmarshaled.SizeBytes != update.SizeBytes {
		t.Errorf("Expected SizeBytes %d, got %d", update.SizeBytes, unmarshaled.SizeBytes)
	}
	if !unmarshaled.VerifiedAt.Equal(update.VerifiedAt) {
		t.Errorf("Expected VerifiedAt %v, got %v", update.VerifiedAt, unmarshaled.VerifiedAt)
	}
}

// TestDownloadAddonCommand_ShapeUnchanged and TestInstallationStatusUpdate_ShapeUnchanged
// are the NFR3 regression guard the issue's Testing section calls for: adding
// WorkshopCacheStatusUpdate and its new routing key must never rename a field, add a
// field, or otherwise change the wire shape of these two pre-existing messages that
// existing control-api/host consumers already depend on. Asserting the exact JSON key
// set (not just that a couple of known fields round-trip) catches an accidental add,
// rename, or removal that per-field assertions elsewhere in this file would miss.
func TestDownloadAddonCommand_ShapeUnchanged(t *testing.T) {
	cmd := rmq.DownloadAddonCommand{
		InstallationID: 1,
		SGCID:          2,
		AddonID:        3,
		WorkshopID:     "987654321",
		SteamAppID:     "550",
		InstallPath:    "/data/mods",
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("Failed to marshal command: %v", err)
	}

	var generic map[string]interface{}
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("Failed to unmarshal command into generic map: %v", err)
	}

	expectedKeys := []string{"installation_id", "sgc_id", "addon_id", "workshop_id", "steam_app_id", "install_path"}
	assertExactJSONKeys(t, generic, expectedKeys)
}

func TestInstallationStatusUpdate_ShapeUnchanged(t *testing.T) {
	errMsg := "boom"
	update := rmq.InstallationStatusUpdate{
		InstallationID:  1,
		Status:          "failed",
		ProgressPercent: 0,
		ErrorMessage:    &errMsg,
	}

	data, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("Failed to marshal update: %v", err)
	}

	var generic map[string]interface{}
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("Failed to unmarshal update into generic map: %v", err)
	}

	expectedKeys := []string{"installation_id", "status", "progress_percent", "error_message"}
	assertExactJSONKeys(t, generic, expectedKeys)
}

// assertExactJSONKeys fails the test if got's key set differs at all (missing, extra, or
// renamed) from want.
func assertExactJSONKeys(t *testing.T, got map[string]interface{}, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("expected exactly %d JSON keys %v, got %d: %v", len(want), want, len(got), keysOf(got))
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("expected JSON key %q to be present, got keys %v", k, keysOf(got))
		}
	}
}

func keysOf(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
