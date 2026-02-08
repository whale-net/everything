package config

import (
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
