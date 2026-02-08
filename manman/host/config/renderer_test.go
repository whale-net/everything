package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/manman/protos"
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
