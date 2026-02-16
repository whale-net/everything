package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/manmanv2/protos"
)

func TestRenderPropertiesFilePreservesUnchangedProperties(t *testing.T) {
	// This test ensures that properties NOT included in patches are preserved
	renderer := NewRenderer(nil)

	baseContent := `# Server Properties
motd=Original Server Name
max-players=20
difficulty=normal
pvp=true
spawn-monsters=true
view-distance=10
online-mode=true`

	config := &pb.RenderedConfiguration{
		StrategyName:    "Server Properties",
		StrategyType:    "file_properties",
		TargetPath:      "/data/server.properties",
		RenderedContent: baseContent,
	}

	baseDataDir := "/tmp/test-data"
	files, err := renderer.RenderConfigurations([]*pb.RenderedConfiguration{config}, baseDataDir)
	if err != nil {
		t.Fatalf("Failed to render configurations: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	file := files[0]

	// Parse rendered content
	properties := parsePropertiesFile(file.Content)

	// Verify ALL original properties are preserved
	expectedProperties := map[string]string{
		"motd":           "Original Server Name",
		"max-players":    "20",
		"difficulty":     "normal",
		"pvp":            "true",
		"spawn-monsters": "true",
		"view-distance":  "10",
		"online-mode":    "true",
	}

	for key, expectedValue := range expectedProperties {
		if value, exists := properties[key]; !exists {
			t.Errorf("Property %s was not preserved", key)
		} else if value != expectedValue {
			t.Errorf("Property %s has wrong value: expected %s, got %s", key, expectedValue, value)
		}
	}

	// Verify correct path
	expectedHostPath := filepath.Join(baseDataDir, "data/server.properties")
	if file.HostPath != expectedHostPath {
		t.Errorf("Incorrect host path: expected %s, got %s", expectedHostPath, file.HostPath)
	}
}

func TestParsePropertiesFile(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected map[string]string
	}{
		{
			name: "basic properties",
			content: `key1=value1
key2=value2`,
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name: "properties with spaces",
			content: `key1 = value1
key2= value2
key3 =value3`,
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
				"key3": "value3",
			},
		},
		{
			name: "properties with comments",
			content: `# Comment line
key1=value1
! Another comment
key2=value2`,
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name: "properties with empty lines",
			content: `key1=value1

key2=value2

`,
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name: "properties with colon separator",
			content: `key1:value1
key2: value2`,
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parsePropertiesFile(tt.content)

			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d properties, got %d", len(tt.expected), len(result))
			}

			for key, expectedValue := range tt.expected {
				if value, exists := result[key]; !exists {
					t.Errorf("Key %s not found in result", key)
				} else if value != expectedValue {
					t.Errorf("Key %s: expected %s, got %s", key, expectedValue, value)
				}
			}
		})
	}
}

func TestRenderPropertiesMap(t *testing.T) {
	properties := map[string]string{
		"zebra": "last",
		"apple": "first",
		"mango": "middle",
	}

	result := renderPropertiesMap(properties)
	lines := strings.Split(result, "\n")

	// Verify all properties are present
	if len(lines) != len(properties) {
		t.Errorf("Expected %d lines, got %d", len(properties), len(lines))
	}

	// Verify alphabetical ordering
	if lines[0] != "apple=first" {
		t.Errorf("Expected first line to be 'apple=first', got '%s'", lines[0])
	}
	if lines[1] != "mango=middle" {
		t.Errorf("Expected second line to be 'mango=middle', got '%s'", lines[1])
	}
	if lines[2] != "zebra=last" {
		t.Errorf("Expected third line to be 'zebra=last', got '%s'", lines[2])
	}
}

func TestRenderConfigurationsMultipleFiles(t *testing.T) {
	renderer := NewRenderer(nil)

	configs := []*pb.RenderedConfiguration{
		{
			StrategyName:    "Server Properties",
			StrategyType:    "file_properties",
			TargetPath:      "/data/server.properties",
			RenderedContent: "motd=Test Server\nmax-players=20",
		},
		{
			StrategyName:    "Whitelist",
			StrategyType:    "file_json",
			TargetPath:      "/data/whitelist.json",
			RenderedContent: "[]",
		},
	}

	baseDataDir := "/tmp/test-data"
	files, err := renderer.RenderConfigurations(configs, baseDataDir)
	if err != nil {
		t.Fatalf("Failed to render configurations: %v", err)
	}

	// Should only render the properties file (JSON not implemented yet)
	if len(files) != 1 {
		t.Fatalf("Expected 1 file (only properties), got %d", len(files))
	}

	if files[0].HostPath != filepath.Join(baseDataDir, "data/server.properties") {
		t.Errorf("Incorrect host path for server.properties")
	}
}

func TestMergeModeEmptyBaseTemplate(t *testing.T) {
	// Test merge mode: empty base_template means read existing file and merge
	renderer := NewRenderer(nil)

	// Create a test directory and file
	testDir := t.TempDir()
	testFile := filepath.Join(testDir, "data", "server.properties")

	// Create parent directory
	if err := os.MkdirAll(filepath.Dir(testFile), 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Write existing file with default properties
	existingContent := `# Minecraft server properties
motd=Default Server
max-players=20
difficulty=normal
pvp=true
online-mode=true
whitelist-enabled=false`

	if err := os.WriteFile(testFile, []byte(existingContent), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Configuration with empty base_template (merge mode)
	config := &pb.RenderedConfiguration{
		StrategyName:    "Server Properties",
		StrategyType:    "file_properties",
		TargetPath:      "/data/server.properties",
		BaseContent:     "", // Empty = merge mode
		RenderedContent: "motd=Patched Server Name\nmax-players=50", // Only overrides
	}

	files, err := renderer.RenderConfigurations([]*pb.RenderedConfiguration{config}, testDir)
	if err != nil {
		t.Fatalf("Failed to render configurations: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	// Parse rendered content
	properties := parsePropertiesFile(files[0].Content)

	// Verify patched properties were updated
	if properties["motd"] != "Patched Server Name" {
		t.Errorf("motd not patched: expected 'Patched Server Name', got '%s'", properties["motd"])
	}
	if properties["max-players"] != "50" {
		t.Errorf("max-players not patched: expected '50', got '%s'", properties["max-players"])
	}

	// Verify unchanged properties were preserved
	if properties["difficulty"] != "normal" {
		t.Errorf("difficulty not preserved: expected 'normal', got '%s'", properties["difficulty"])
	}
	if properties["pvp"] != "true" {
		t.Errorf("pvp not preserved: expected 'true', got '%s'", properties["pvp"])
	}
	if properties["online-mode"] != "true" {
		t.Errorf("online-mode not preserved: expected 'true', got '%s'", properties["online-mode"])
	}
	if properties["whitelist-enabled"] != "false" {
		t.Errorf("whitelist-enabled not preserved: expected 'false', got '%s'", properties["whitelist-enabled"])
	}
}

func TestMergeModeNoExistingFile(t *testing.T) {
	// Test merge mode when no existing file exists
	renderer := NewRenderer(nil)

	testDir := t.TempDir()

	// Configuration with empty base_template but no existing file
	config := &pb.RenderedConfiguration{
		StrategyName:    "Server Properties",
		StrategyType:    "file_properties",
		TargetPath:      "/data/server.properties",
		BaseContent:     "", // Empty = merge mode
		RenderedContent: "motd=New Server\nmax-players=30",
	}

	files, err := renderer.RenderConfigurations([]*pb.RenderedConfiguration{config}, testDir)
	if err != nil {
		t.Fatalf("Failed to render configurations: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	// Should only contain the override properties
	properties := parsePropertiesFile(files[0].Content)

	if len(properties) != 2 {
		t.Errorf("Expected 2 properties, got %d", len(properties))
	}
	if properties["motd"] != "New Server" {
		t.Errorf("motd incorrect: expected 'New Server', got '%s'", properties["motd"])
	}
	if properties["max-players"] != "30" {
		t.Errorf("max-players incorrect: expected '30', got '%s'", properties["max-players"])
	}
}

func TestPatchCascadeWithDuplicateKeys(t *testing.T) {
	// This test verifies the critical patch cascade behavior:
	// GameConfig patches set base values, ServerGameConfig patches override them
	// When both set the same property (like motd), SGC value should win
	renderer := NewRenderer(nil)

	testDir := t.TempDir()
	testFile := filepath.Join(testDir, "data", "server.properties")

	// Create parent directory
	if err := os.MkdirAll(filepath.Dir(testFile), 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Simulate existing Minecraft-generated file with defaults
	existingContent := `# Minecraft server properties
motd=A Minecraft Server
max-players=20
difficulty=easy
online-mode=true
pvp=true
whitelist-enabled=false`

	if err := os.WriteFile(testFile, []byte(existingContent), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Simulate API cascade: GameConfig patch + ServerGameConfig patch
	// This is exactly what GetSessionConfiguration returns
	gameConfigPatch := `online-mode=true
max-players=20
difficulty=normal
pvp=true
motd=ManManV2 Minecraft Server`

	sgcPatch := `motd=ManManV2 Dev Server - SGC Override`

	// API concatenates patches: GameConfig + "\n" + SGC
	cascadedPatches := gameConfigPatch + "\n" + sgcPatch

	// Configuration with cascaded patches in rendered_content
	config := &pb.RenderedConfiguration{
		StrategyName:    "Server Properties",
		StrategyType:    "file_properties",
		TargetPath:      "/data/server.properties",
		BaseContent:     "", // Empty = merge mode
		RenderedContent: cascadedPatches,
	}

	files, err := renderer.RenderConfigurations([]*pb.RenderedConfiguration{config}, testDir)
	if err != nil {
		t.Fatalf("Failed to render configurations: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	// Parse rendered content
	properties := parsePropertiesFile(files[0].Content)

	// CRITICAL: Verify SGC patch overrides GameConfig patch
	if properties["motd"] != "ManManV2 Dev Server - SGC Override" {
		t.Errorf("motd NOT overridden by SGC patch: expected 'ManManV2 Dev Server - SGC Override', got '%s'", properties["motd"])
		t.Errorf("This means the patch cascade is broken!")
	}

	// Verify GameConfig patch values are applied
	if properties["online-mode"] != "true" {
		t.Errorf("online-mode from GameConfig patch not applied: expected 'true', got '%s'", properties["online-mode"])
	}
	if properties["max-players"] != "20" {
		t.Errorf("max-players from GameConfig patch not applied: expected '20', got '%s'", properties["max-players"])
	}
	if properties["difficulty"] != "normal" {
		t.Errorf("difficulty override from GameConfig patch not applied: expected 'normal', got '%s'", properties["difficulty"])
	}

	// Verify existing properties not in patches are preserved
	if properties["pvp"] != "true" {
		t.Errorf("pvp not preserved: expected 'true', got '%s'", properties["pvp"])
	}
	if properties["whitelist-enabled"] != "false" {
		t.Errorf("whitelist-enabled not preserved: expected 'false', got '%s'", properties["whitelist-enabled"])
	}

	// Verify the final rendered content doesn't have duplicate motd entries
	motdCount := 0
	for _, line := range strings.Split(files[0].Content, "\n") {
		if strings.HasPrefix(line, "motd=") {
			motdCount++
		}
	}
	if motdCount != 1 {
		t.Errorf("Final rendered content has %d motd entries, expected 1 (deduplication failed)", motdCount)
		t.Errorf("Content:\n%s", files[0].Content)
	}

	t.Logf("✅ Patch cascade test passed!")
	t.Logf("   GameConfig: motd=ManManV2 Minecraft Server")
	t.Logf("   SGC Override: motd=ManManV2 Dev Server - SGC Override")
	t.Logf("   Final: motd=%s", properties["motd"])
}

func TestServerGameConfigOverridesGameConfig(t *testing.T) {
	// Focused test: When both GC and SGC set the same property, SGC MUST win
	// This is the core requirement for the cascade system

	// Scenario: Both patches set "server-name" property
	gcPatch := "server-name=Production Server"
	sgcPatch := "server-name=Dev Override"

	// API concatenates: GC first, then SGC
	cascaded := gcPatch + "\n" + sgcPatch

	// Parse like the renderer does
	properties := parsePropertiesFile(cascaded)

	// CRITICAL: SGC value MUST override GC value
	if properties["server-name"] != "Dev Override" {
		t.Fatalf("SGC did NOT override GC! Expected 'Dev Override', got '%s'", properties["server-name"])
	}

	// Verify only one entry (deduplication)
	lines := strings.Split(cascaded, "\n")
	if len(lines) != 2 {
		t.Fatalf("Expected 2 input lines, got %d", len(lines))
	}

	// But map should have only 1 entry
	if len(properties) != 1 {
		t.Fatalf("Expected 1 deduplicated property, got %d", len(properties))
	}

	t.Logf("✅ SGC override test passed!")
	t.Logf("   Input: %s", cascaded)
	t.Logf("   Result: server-name=%s (SGC wins)", properties["server-name"])
}

func TestMultipleDuplicateKeysInCascade(t *testing.T) {
	// Test multiple properties being overridden at different levels
	// GC sets base values, SGC overrides some (not all)

	gcPatch := `motd=Base MOTD
max-players=10
difficulty=easy
pvp=false`

	sgcPatch := `motd=Override MOTD
max-players=50`

	cascaded := gcPatch + "\n" + sgcPatch
	properties := parsePropertiesFile(cascaded)

	// SGC overrides should win
	if properties["motd"] != "Override MOTD" {
		t.Errorf("motd: expected 'Override MOTD', got '%s'", properties["motd"])
	}
	if properties["max-players"] != "50" {
		t.Errorf("max-players: expected '50', got '%s'", properties["max-players"])
	}

	// GC values without SGC override should remain
	if properties["difficulty"] != "easy" {
		t.Errorf("difficulty: expected 'easy', got '%s'", properties["difficulty"])
	}
	if properties["pvp"] != "false" {
		t.Errorf("pvp: expected 'false', got '%s'", properties["pvp"])
	}

	// Verify total count (4 unique keys despite duplicates)
	if len(properties) != 4 {
		t.Errorf("Expected 4 unique properties, got %d: %v", len(properties), properties)
	}

	t.Logf("✅ Multiple duplicate keys test passed!")
	t.Logf("   GC sets: motd, max-players, difficulty, pvp")
	t.Logf("   SGC overrides: motd, max-players")
	t.Logf("   Final: motd=%s, max-players=%s, difficulty=%s, pvp=%s",
		properties["motd"], properties["max-players"], properties["difficulty"], properties["pvp"])
}
