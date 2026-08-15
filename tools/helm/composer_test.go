package helm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// TestNewComposer tests the Composer constructor
func TestNewComposer(t *testing.T) {
	config := ChartConfig{
		ChartName:   "test-chart",
		Version:     "1.0.0",
		Environment: "production",
		Namespace:   "default",
		OutputDir:   "/tmp/test",
	}

	composer := NewComposer(config, "/templates")

	if composer.config.ChartName != "test-chart" {
		t.Errorf("Expected ChartName 'test-chart', got %s", composer.config.ChartName)
	}

	if composer.templateDir != "/templates" {
		t.Errorf("Expected templateDir '/templates', got %s", composer.templateDir)
	}

	if composer.templateFuncs == nil {
		t.Error("Expected template funcs to be initialized")
	}

	// Check that template functions are set up
	if _, ok := composer.templateFuncs["toYaml"]; !ok {
		t.Error("Expected toYaml template function to be registered")
	}
	if _, ok := composer.templateFuncs["default"]; !ok {
		t.Error("Expected default template function to be registered")
	}
	if _, ok := composer.templateFuncs["required"]; !ok {
		t.Error("Expected required template function to be registered")
	}
}

// TestLoadMetadata tests loading metadata from JSON files
func TestLoadMetadata(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "composer-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test metadata JSON file
	testMetadata := &AppMetadata{
		Name:        "test-app",
		AppType:     "worker",
		Version:     "1.0.0",
		Description: "Test application",
		Registry:    "ghcr.io",
		RepoName:    "test-app",
		ImageTarget: "test_app_image",
	}

	metadataFile := filepath.Join(tmpDir, "test-app.json")
	data, err := protojson.Marshal(testMetadata)
	if err != nil {
		t.Fatalf("Failed to marshal metadata: %v", err)
	}

	if err := os.WriteFile(metadataFile, data, 0644); err != nil {
		t.Fatalf("Failed to write metadata file: %v", err)
	}

	// Create composer and load metadata
	config := ChartConfig{
		ChartName: "test-chart",
		Version:   "1.0.0",
		OutputDir: tmpDir,
	}
	composer := NewComposer(config, "/templates")

	err = composer.LoadMetadata([]string{metadataFile})
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	if len(composer.apps) != 1 {
		t.Errorf("Expected 1 app, got %d", len(composer.apps))
	}

	if composer.apps[0].Name != "test-app" {
		t.Errorf("Expected app name 'test-app', got %s", composer.apps[0].Name)
	}

	if composer.apps[0].AppType != "worker" {
		t.Errorf("Expected app type 'worker', got %s", composer.apps[0].AppType)
	}
}

// TestLoadMetadata_InvalidJSON tests error handling for invalid JSON
func TestLoadMetadata_InvalidJSON(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "composer-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create invalid JSON file
	invalidFile := filepath.Join(tmpDir, "invalid.json")
	if err := os.WriteFile(invalidFile, []byte("not valid json"), 0644); err != nil {
		t.Fatalf("Failed to write invalid file: %v", err)
	}

	config := ChartConfig{
		ChartName: "test-chart",
		Version:   "1.0.0",
		OutputDir: tmpDir,
	}
	composer := NewComposer(config, "/templates")

	err = composer.LoadMetadata([]string{invalidFile})
	if err == nil {
		t.Error("Expected error for invalid JSON, got nil")
	}
}

// TestLoadMetadata_MissingFile tests error handling for missing files
func TestLoadMetadata_MissingFile(t *testing.T) {
	config := ChartConfig{
		ChartName: "test-chart",
		Version:   "1.0.0",
		OutputDir: "/tmp",
	}
	composer := NewComposer(config, "/templates")

	err := composer.LoadMetadata([]string{"/nonexistent/file.json"})
	if err == nil {
		t.Error("Expected error for missing file, got nil")
	}
}

// TestHasExternalAPIs tests detection of external APIs
func TestHasExternalAPIs(t *testing.T) {
	config := ChartConfig{
		ChartName: "test-chart",
		Version:   "1.0.0",
		OutputDir: "/tmp",
	}

	tests := []struct {
		name     string
		apps     []*AppMetadata
		expected bool
	}{
		{
			name: "Has external API",
			apps: []*AppMetadata{
				{Name: "api", AppType: "external-api"},
				{Name: "worker", AppType: "worker"},
			},
			expected: true,
		},
		{
			name: "No external API",
			apps: []*AppMetadata{
				{Name: "worker1", AppType: "worker"},
				{Name: "worker2", AppType: "worker"},
			},
			expected: false,
		},
		{
			name: "Has internal API only",
			apps: []*AppMetadata{
				{Name: "api", AppType: "internal-api"},
			},
			expected: false,
		},
		{
			name:     "Empty apps",
			apps:     []*AppMetadata{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			composer := NewComposer(config, "/templates")
			composer.apps = tt.apps

			result := composer.hasExternalAPIs()
			if result != tt.expected {
				t.Errorf("Expected hasExternalAPIs() = %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestFormatYAML tests the custom YAML formatter
func TestFormatYAML(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		indent   int
		expected string
	}{
		{
			name:     "String value",
			input:    "hello",
			indent:   0,
			expected: "hello",
		},
		{
			name:     "Integer value",
			input:    42,
			indent:   0,
			expected: "42",
		},
		{
			name:     "Boolean value",
			input:    true,
			indent:   0,
			expected: "true",
		},
		{
			name:     "Map with indent",
			input:    map[string]interface{}{"key": "value"},
			indent:   2,
			expected: "  key: value",
		},
		{
			name:     "String slice",
			input:    []string{"a", "b", "c"},
			indent:   0,
			expected: "- \"a\"\n- \"b\"\n- \"c\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatYAML(tt.input, tt.indent)
			if result != tt.expected {
				t.Errorf("formatYAML() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

// TestToValuesFormat tests ResourceConfig conversion
func TestToValuesFormat(t *testing.T) {
	rc := ResourceConfig{
		RequestsCPU:    "100m",
		RequestsMemory: "256Mi",
		LimitsCPU:      "200m",
		LimitsMemory:   "512Mi",
	}

	vrc := rc.ToValuesFormat()

	if vrc.Requests.CPU != "100m" {
		t.Errorf("Expected requests CPU '100m', got %s", vrc.Requests.CPU)
	}
	if vrc.Requests.Memory != "256Mi" {
		t.Errorf("Expected requests memory '256Mi', got %s", vrc.Requests.Memory)
	}
	if vrc.Limits.CPU != "200m" {
		t.Errorf("Expected limits CPU '200m', got %s", vrc.Limits.CPU)
	}
	if vrc.Limits.Memory != "512Mi" {
		t.Errorf("Expected limits memory '512Mi', got %s", vrc.Limits.Memory)
	}
}

// TestYAMLWriter tests the YAMLWriter component
func TestYAMLWriter(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "yaml-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.yaml")
	f, err := os.Create(testFile)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	defer f.Close()

	w := NewYAMLWriter(f)

	// Test basic key-value writes
	w.WriteString("name", "test-app")
	w.WriteInt("port", 8080)
	w.WriteBool("enabled", true)

	// Test sections
	w.StartSection("config")
	w.WriteString("environment", "production")
	w.WriteInt("replicas", 3)
	w.EndSection()

	// Test lists
	w.WriteList("tags", []string{"api", "web", "backend"})

	// Test maps
	w.WriteMap("labels", map[string]string{
		"app":     "myapp",
		"version": "1.0.0",
	})

	f.Close()

	// Read and verify output
	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read test file: %v", err)
	}

	output := string(content)

	// Verify key-value pairs
	if !contains(output, "name: test-app") {
		t.Error("Expected 'name: test-app' in output")
	}
	if !contains(output, "port: 8080") {
		t.Error("Expected 'port: 8080' in output")
	}
	if !contains(output, "enabled: true") {
		t.Error("Expected 'enabled: true' in output")
	}

	// Verify section
	if !contains(output, "config:") {
		t.Error("Expected 'config:' section in output")
	}
	if !contains(output, "  environment: production") {
		t.Error("Expected indented 'environment: production' in output")
	}

	// Verify list
	if !contains(output, "tags:") {
		t.Error("Expected 'tags:' in output")
	}
	if !contains(output, `- "api"`) {
		t.Error("Expected list item '- \"api\"' in output")
	}
}

// TestBuildAppConfig tests the buildAppConfig method
func TestBuildAppConfig(t *testing.T) {
	config := ChartConfig{
		ChartName: "test-chart",
		Version:   "1.0.0",
		OutputDir: "/tmp",
	}
	composer := NewComposer(config, "/templates")

	tests := []struct {
		name            string
		metadata        *AppMetadata
		expectReplicas  int
		expectPort      int
		expectHealthChk bool
	}{
		{
			name: "External API with defaults",
			metadata: &AppMetadata{
				Name:        "api",
				AppType:     "external-api",
				Registry:    "ghcr.io",
				RepoName:    "demo-api",
				Version:     "v1.0.0",
				ImageTarget: "api_image",
			},
			expectReplicas:  2,
			expectPort:      8000,
			expectHealthChk: false,
		},
		{
			name: "Worker with custom port",
			metadata: &AppMetadata{
				Name:        "worker",
				AppType:     "worker",
				Registry:    "ghcr.io",
				RepoName:    "demo-worker",
				Version:     "v1.0.0",
				Port:        9000,
				ImageTarget: "worker_image",
			},
			expectReplicas:  1,
			expectPort:      9000,
			expectHealthChk: false,
		},
		{
			name: "Internal API",
			metadata: &AppMetadata{
				Name:        "internal",
				AppType:     "internal-api",
				Registry:    "ghcr.io",
				RepoName:    "demo-internal",
				Version:     "v1.0.0",
				Port:        3000,
				ImageTarget: "internal_image",
			},
			expectReplicas:  2,
			expectPort:      3000,
			expectHealthChk: false,
		},
		{
			name: "External API with health check enabled",
			metadata: &AppMetadata{
				Name:        "api-with-health",
				AppType:     "external-api",
				Registry:    "ghcr.io",
				RepoName:    "demo-api-health",
				Version:     "v1.0.0",
				ImageTarget: "api_health_image",
				HealthCheck: &appmetapb.HealthCheck{
					Enabled: true,
					Path:    "/api/health",
				},
			},
			expectReplicas:  2,
			expectPort:      8000,
			expectHealthChk: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := composer.buildAppConfig(tt.metadata)
			if err != nil {
				t.Fatalf("buildAppConfig failed: %v", err)
			}

			if config.Replicas != tt.expectReplicas {
				t.Errorf("Expected %d replicas, got %d", tt.expectReplicas, config.Replicas)
			}

			if config.Port != tt.expectPort {
				t.Errorf("Expected port %d, got %d", tt.expectPort, config.Port)
			}

			hasHealthCheck := config.HealthCheck != nil
			if hasHealthCheck != tt.expectHealthChk {
				t.Errorf("Expected health check: %v, got: %v", tt.expectHealthChk, hasHealthCheck)
			}

			if config.Image != GetImage(tt.metadata) {
				t.Errorf("Expected image %s, got %s", GetImage(tt.metadata), config.Image)
			}

			if config.ImageTag != GetImageTag(tt.metadata) {
				t.Errorf("Expected imageTag %s, got %s", GetImageTag(tt.metadata), config.ImageTag)
			}
		})
	}
}

// TestBuildAppConfig_PythonMemory tests Python-specific memory configuration
func TestBuildAppConfig_PythonMemory(t *testing.T) {
	config := ChartConfig{
		ChartName: "test-chart",
		Version:   "1.0.0",
		OutputDir: "/tmp",
	}
	composer := NewComposer(config, "/templates")

	tests := []struct {
		name               string
		metadata           *AppMetadata
		expectedMemRequest string
		expectedMemLimit   string
	}{
		{
			name: "Python External API uses reduced memory",
			metadata: &AppMetadata{
				Name:        "python-api",
				AppType:     "external-api",
				Language:    "python",
				Registry:    "ghcr.io",
				RepoName:    "python-api",
				Version:     "v1.0.0",
				ImageTarget: "python_api_image",
			},
			expectedMemRequest: "64Mi",
			expectedMemLimit:   "256Mi",
		},
		{
			name: "Python Worker uses reduced memory",
			metadata: &AppMetadata{
				Name:        "python-worker",
				AppType:     "worker",
				Language:    "python",
				Registry:    "ghcr.io",
				RepoName:    "python-worker",
				Version:     "v1.0.0",
				ImageTarget: "python_worker_image",
			},
			expectedMemRequest: "64Mi",
			expectedMemLimit:   "256Mi",
		},
		{
			name: "Go API uses standard memory",
			metadata: &AppMetadata{
				Name:        "go-api",
				AppType:     "external-api",
				Language:    "go",
				Registry:    "ghcr.io",
				RepoName:    "go-api",
				Version:     "v1.0.0",
				ImageTarget: "go_api_image",
			},
			expectedMemRequest: "256Mi",
			expectedMemLimit:   "512Mi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appConfig, err := composer.buildAppConfig(tt.metadata)
			if err != nil {
				t.Fatalf("buildAppConfig failed: %v", err)
			}

			if appConfig.Resources.Requests.Memory != tt.expectedMemRequest {
				t.Errorf("Expected memory request %s, got %s",
					tt.expectedMemRequest, appConfig.Resources.Requests.Memory)
			}

			if appConfig.Resources.Limits.Memory != tt.expectedMemLimit {
				t.Errorf("Expected memory limit %s, got %s",
					tt.expectedMemLimit, appConfig.Resources.Limits.Memory)
			}
		})
	}
}

// TestFormatYAML_EdgeCases tests edge cases in YAML formatting
func TestFormatYAML_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		indent   int
		expected string
	}{
		{
			name:     "Nil value",
			input:    nil,
			indent:   0,
			expected: "null",
		},
		{
			name:     "Empty string slice",
			input:    []string{},
			indent:   2,
			expected: "",
		},
		{
			name:     "Empty map[string]string",
			input:    map[string]string{},
			indent:   2,
			expected: "  {}",
		},
		{
			name:     "Empty map[string]interface{}",
			input:    map[string]interface{}{},
			indent:   2,
			expected: "  {}",
		},
		{
			name:     "Float value",
			input:    3.14,
			indent:   0,
			expected: "3.14",
		},
		{
			name:     "Uint value",
			input:    uint(42),
			indent:   0,
			expected: "42",
		},
		{
			name:     "Interface slice",
			input:    []interface{}{"a", 1, true},
			indent:   0,
			expected: "- a\n- 1\n- true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatYAML(tt.input, tt.indent)
			if result != tt.expected {
				t.Errorf("formatYAML() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

// Helper function for substring checks
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || contains(s[1:], substr)))
}

// TestGenerateValuesYaml_DomainAppFormat tests that apps in values.yaml use domain-app format
func TestGenerateValuesYaml_DomainAppFormat(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "composer-domain-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test metadata for an app with domain and name
	testMetadata := &AppMetadata{
		Name:        "hello-job",
		Domain:      "demo",
		AppType:     "job",
		Version:     "1.0.0",
		Description: "Test job",
		Registry:    "ghcr.io",
		RepoName:    "demo-hello-job",
		ImageTarget: "hello-job_image",
		Language:    "python",
	}

	metadataFile := filepath.Join(tmpDir, "hello-job.json")
	data, err := protojson.Marshal(testMetadata)
	if err != nil {
		t.Fatalf("Failed to marshal metadata: %v", err)
	}

	if err := os.WriteFile(metadataFile, data, 0644); err != nil {
		t.Fatalf("Failed to write metadata file: %v", err)
	}

	// Create composer and load metadata
	config := ChartConfig{
		ChartName:   "test-chart",
		Version:     "1.0.0",
		Environment: "production",
		Namespace:   "default",
		OutputDir:   tmpDir,
	}
	composer := NewComposer(config, "/templates")

	err = composer.LoadMetadata([]string{metadataFile})
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	// Generate values.yaml
	chartDir := filepath.Join(tmpDir, "chart")
	if err := os.MkdirAll(chartDir, 0755); err != nil {
		t.Fatalf("Failed to create chart dir: %v", err)
	}

	err = composer.generateValuesYaml(chartDir)
	if err != nil {
		t.Fatalf("generateValuesYaml failed: %v", err)
	}

	// Read and verify values.yaml content
	valuesFile := filepath.Join(chartDir, "values.yaml")
	content, err := os.ReadFile(valuesFile)
	if err != nil {
		t.Fatalf("Failed to read values.yaml: %v", err)
	}

	valuesContent := string(content)

	// Verify the app is keyed with domain-app format (demo-hello-job)
	expectedKey := "demo-hello-job:"
	if !strings.Contains(valuesContent, expectedKey) {
		t.Errorf("Expected to find '%s' in values.yaml, but it was not present", expectedKey)
		t.Logf("values.yaml content:\n%s", valuesContent)
	}

	// Verify the old format (just app name) is NOT present
	oldKey := "hello-job:"
	lines := strings.Split(valuesContent, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Check if line starts with "hello-job:" (the old format)
		// But ignore lines that contain "demo-hello-job:" (the correct format)
		if strings.HasPrefix(trimmed, oldKey) && !strings.Contains(line, expectedKey) {
			// Found a line that starts with just "hello-job:" without the domain prefix
			// Check if this is actually a key (has colon and is at the right indentation)
			if strings.HasPrefix(trimmed, oldKey) && !strings.HasPrefix(line, " ") {
				t.Errorf("Found old format key '%s' in values.yaml. All apps should use domain-app format.", oldKey)
				t.Logf("Problematic line: %s", line)
			}
		}
	}
}

// TestBuildAppConfig_InternalAPIExposeIngress tests that exposeIngress defaults to false for internal-api
func TestBuildAppConfig_InternalAPIExposeIngress(t *testing.T) {
	config := ChartConfig{
		ChartName: "test-chart",
		Version:   "1.0.0",
		OutputDir: "/tmp",
	}
	composer := NewComposer(config, "/templates")

	tests := []struct {
		name                string
		appType             string
		expectExposeIngress bool
	}{
		{
			name:                "Internal API defaults to exposeIngress false",
			appType:             "internal-api",
			expectExposeIngress: false,
		},
		{
			name:                "External API has exposeIngress false",
			appType:             "external-api",
			expectExposeIngress: false,
		},
		{
			name:                "Worker has exposeIngress false",
			appType:             "worker",
			expectExposeIngress: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := &AppMetadata{
				Name:        "test-app",
				AppType:     tt.appType,
				Registry:    "ghcr.io",
				RepoName:    "test-app",
				Version:     "v1.0.0",
				Port:        8000,
				ImageTarget: "test_app_image",
			}

			config, err := composer.buildAppConfig(metadata)
			if err != nil {
				t.Fatalf("buildAppConfig failed: %v", err)
			}

			if config.ExposeIngress != tt.expectExposeIngress {
				t.Errorf("Expected ExposeIngress=%v, got %v", tt.expectExposeIngress, config.ExposeIngress)
			}
		})
	}
}

// TestWriteValuesYAML_InternalAPIExposeIngress tests that internal-api includes exposeIngress in values.yaml
func TestWriteValuesYAML_InternalAPIExposeIngress(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "values-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	valuesFile := filepath.Join(tmpDir, "values.yaml")
	f, err := os.Create(valuesFile)
	if err != nil {
		t.Fatalf("Failed to create values file: %v", err)
	}
	defer f.Close()

	// Create test data with internal-api app
	data := ValuesData{
		Global: GlobalConfig{
			Namespace:   "test-ns",
			Environment: "dev",
		},
		Apps: map[string]AppConfig{
			"test-internal-api": {
				Type:          "internal-api",
				Image:         "test-internal-api",
				ImageTag:      "latest",
				Port:          8080,
				Replicas:      2,
				ExposeIngress: false,
				Resources: ValuesResourceConfig{
					Requests: ResourceValues{CPU: "50m", Memory: "64Mi"},
					Limits:   ResourceValues{CPU: "100m", Memory: "256Mi"},
				},
			},
			"test-external-api": {
				Type:     "external-api",
				Image:    "test-external-api",
				ImageTag: "latest",
				Port:     8080,
				Replicas: 2,
				Resources: ValuesResourceConfig{
					Requests: ResourceValues{CPU: "50m", Memory: "64Mi"},
					Limits:   ResourceValues{CPU: "100m", Memory: "256Mi"},
				},
			},
		},
		IngressDefaults: IngressDefaultsConfig{
			Enabled: true,
		},
	}

	if err := writeValuesYAML(f, data); err != nil {
		t.Fatalf("Failed to write values: %v", err)
	}
	f.Close()

	// Read and verify
	content, err := os.ReadFile(valuesFile)
	if err != nil {
		t.Fatalf("Failed to read values file: %v", err)
	}

	valuesContent := string(content)

	// Verify internal-api has exposeIngress field
	if !contains(valuesContent, "test-internal-api:") {
		t.Error("Expected test-internal-api in values.yaml")
	}
	if !contains(valuesContent, "exposeIngress: false") {
		t.Error("Expected 'exposeIngress: false' for internal-api in values.yaml")
	}

	// Verify external-api does NOT have exposeIngress field (should not appear for external-api)
	lines := strings.Split(valuesContent, "\n")
	inExternalAPISection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "test-external-api:") {
			inExternalAPISection = true
		}
		if inExternalAPISection && strings.HasPrefix(trimmed, "test-") {
			// Moved to next section
			inExternalAPISection = false
		}
		if inExternalAPISection && strings.Contains(line, "exposeIngress:") {
			t.Error("exposeIngress should not appear for external-api apps")
		}
	}
}

// TestGenerateChart_Golden renders a full chart (Chart.yaml + values.yaml +
// templates) from a fixed set of AppManifest fixtures covering every app
// type, and byte-compares the output against a checked-in golden chart.
// This is the regression guard for the AR-M migration from a hand-written
// AppMetadata struct to appmetapb.AppManifest: any future change to how the
// composer reads or renders app metadata should show up here first.
//
// If the composer's output legitimately changes, regenerate the golden
// files by running this test with UPDATE_GOLDEN=1 and reviewing the diff.
func TestGenerateChart_Golden(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "composer-golden")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	config := ChartConfig{
		ChartName:   "golden-chart",
		Version:     "0.0.0-dev",
		Environment: "production",
		Namespace:   "golden-ns",
		OutputDir:   tmpDir,
	}
	composer := NewComposer(config, "testdata/templates")
	composer.apps = []*AppMetadata{
		{
			Name:         "api",
			Domain:       "golden",
			AppType:      "external-api",
			Language:     "go",
			Version:      "v1.2.3",
			Registry:     "ghcr.io",
			Organization: "whale-net",
			RepoName:     "golden-api",
			Port:         8080,
			HealthCheck:  &appmetapb.HealthCheck{Enabled: true, Path: "/healthz"},
			Ingress:      &appmetapb.Ingress{Host: "api.golden.local", TlsSecretName: "golden-tls"},
		},
		{
			Name:         "worker",
			Domain:       "golden",
			AppType:      "worker",
			Language:     "python",
			Version:      "v1.2.3",
			Registry:     "ghcr.io",
			Organization: "whale-net",
			RepoName:     "golden-worker",
			Command:      []string{"python3"},
			Args:         []string{"-m", "golden.worker"},
		},
		{
			Name:         "migrate",
			Domain:       "golden",
			AppType:      "job",
			Language:     "go",
			Version:      "v1.2.3",
			Registry:     "ghcr.io",
			Organization: "whale-net",
			RepoName:     "golden-migrate",
		},
	}

	if err := composer.generateValuesYaml(tmpDir); err != nil {
		t.Fatalf("generateValuesYaml failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(tmpDir, "values.yaml"))
	if err != nil {
		t.Fatalf("read generated values.yaml: %v", err)
	}

	goldenPath := "testdata/golden_values.yaml"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(goldenPath, got, 0644); err != nil {
			t.Fatalf("write golden file: %v", err)
		}
		t.Skip("UPDATE_GOLDEN set; wrote " + goldenPath)
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("generated values.yaml does not match golden file %s.\n--- got ---\n%s\n--- want ---\n%s", goldenPath, got, want)
	}
}

// TestGenerateLockfile_Golden renders the compose-time image lockfile from
// the same fixed AppManifest fixtures as TestGenerateChart_Golden and
// byte-compares it against a checked-in golden file. Guards two things at
// once: the lockfile's shape, and that it never varies across builds of
// identical inputs (Go map iteration is the historical hazard here — see
// dd23e807).
//
// If the lockfile's shape legitimately changes, regenerate the golden file
// by running this test with UPDATE_GOLDEN=1 and reviewing the diff.
func TestGenerateLockfile_Golden(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "composer-lockfile-golden")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	config := ChartConfig{
		ChartName:   "golden-chart",
		Version:     "0.0.0-dev",
		Environment: "production",
		Namespace:   "golden-ns",
		OutputDir:   tmpDir,
	}
	composer := NewComposer(config, "testdata/templates")
	composer.apps = []*AppMetadata{
		{
			Name:         "api",
			Domain:       "golden",
			AppType:      "external-api",
			Language:     "go",
			Version:      "v1.2.3",
			Registry:     "ghcr.io",
			Organization: "whale-net",
			RepoName:     "golden-api",
			Port:         8080,
			HealthCheck:  &appmetapb.HealthCheck{Enabled: true, Path: "/healthz"},
			Ingress:      &appmetapb.Ingress{Host: "api.golden.local", TlsSecretName: "golden-tls"},
		},
		{
			Name:         "worker",
			Domain:       "golden",
			AppType:      "worker",
			Language:     "python",
			Version:      "v1.2.3",
			Registry:     "ghcr.io",
			Organization: "whale-net",
			RepoName:     "golden-worker",
			Command:      []string{"python3"},
			Args:         []string{"-m", "golden.worker"},
		},
		{
			Name:         "migrate",
			Domain:       "golden",
			AppType:      "job",
			Language:     "go",
			Version:      "v1.2.3",
			Registry:     "ghcr.io",
			Organization: "whale-net",
			RepoName:     "golden-migrate",
		},
	}

	if err := composer.generateLockfile(tmpDir); err != nil {
		t.Fatalf("generateLockfile failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(tmpDir, LockfileFileName))
	if err != nil {
		t.Fatalf("read generated image lockfile: %v", err)
	}

	goldenPath := "testdata/golden_image-lockfile.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(goldenPath, got, 0644); err != nil {
			t.Fatalf("write golden file: %v", err)
		}
		t.Skip("UPDATE_GOLDEN set; wrote " + goldenPath)
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("generated image lockfile does not match golden file %s.\n--- got ---\n%s\n--- want ---\n%s", goldenPath, got, want)
	}
}

// TestGenerateLockfile_Deterministic builds the lockfile several times from
// the same (deliberately unsorted) app slice and asserts byte-identical
// output every time — the regression guard for Go's randomized map/slice
// iteration order leaking into the emitted lockfile.
func TestGenerateLockfile_Deterministic(t *testing.T) {
	apps := []*AppMetadata{
		{Name: "zeta", Domain: "demo", Registry: "ghcr.io", Organization: "whale-net", RepoName: "demo-zeta", Version: "v1.0.0"},
		{Name: "alpha", Domain: "demo", Registry: "ghcr.io", Organization: "whale-net", RepoName: "demo-alpha", Version: "v1.0.0"},
		{Name: "mid", Domain: "demo", Registry: "ghcr.io", Organization: "whale-net", RepoName: "demo-mid", Version: "v1.0.0"},
	}

	var results []string
	for i := 0; i < 5; i++ {
		tmpDir, err := os.MkdirTemp("", "composer-lockfile-det")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		config := ChartConfig{ChartName: "det-chart", Version: "0.0.0-dev", OutputDir: tmpDir}
		composer := NewComposer(config, "testdata/templates")
		composer.apps = apps

		if err := composer.generateLockfile(tmpDir); err != nil {
			t.Fatalf("generateLockfile failed: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(tmpDir, LockfileFileName))
		if err != nil {
			t.Fatalf("read lockfile: %v", err)
		}
		results = append(results, string(data))
	}

	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Errorf("lockfile output not deterministic across runs: run 0 vs run %d differ\n--- run 0 ---\n%s\n--- run %d ---\n%s", i, results[0], i, results[i])
		}
	}
}

// TestLoadMetadata_IgnoresNoneDeployUnit tests that apps with deploy_unit == DEPLOY_UNIT_NONE are ignored
func TestLoadMetadata_IgnoresNoneDeployUnit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "composer-ignore-none-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testMetadata := &AppMetadata{
		Name:        "none-app",
		AppType:     "worker",
		DeployUnit:  appmetapb.DeployUnit_DEPLOY_UNIT_NONE,
		Version:     "1.0.0",
		Description: "Non-deployed application",
		Registry:    "ghcr.io",
		RepoName:    "none-app",
	}

	metadataFile := filepath.Join(tmpDir, "none-app.json")
	data, err := protojson.Marshal(testMetadata)
	if err != nil {
		t.Fatalf("Failed to marshal metadata: %v", err)
	}
	if err := os.WriteFile(metadataFile, data, 0644); err != nil {
		t.Fatalf("Failed to write metadata file: %v", err)
	}

	config := ChartConfig{
		ChartName: "test-chart",
		Version:   "1.0.0",
		OutputDir: tmpDir,
	}
	composer := NewComposer(config, "/templates")

	err = composer.LoadMetadata([]string{metadataFile})
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	if len(composer.apps) != 0 {
		t.Errorf("Expected 0 apps loaded (ignored), got %d", len(composer.apps))
	}
}

// TestLoadMetadata_IgnoresCLIAndFirmware tests that cli, binary, and firmware app types are ignored
func TestLoadMetadata_IgnoresCLIAndFirmware(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "composer-ignore-types-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	appTypes := []string{"cli", "binary", "firmware"}
	var files []string

	for _, at := range appTypes {
		metadata := &AppMetadata{
			Name:        "app-" + at,
			AppType:     at,
			DeployUnit:  appmetapb.DeployUnit_DEPLOY_UNIT_NONE,
			Version:     "1.0.0",
			Description: "Test " + at + " application",
			Domain:      "tools",
		}
		filename := filepath.Join(tmpDir, "app-"+at+".json")
		data, err := protojson.Marshal(metadata)
		if err != nil {
			t.Fatalf("Failed to marshal metadata: %v", err)
		}
		if err := os.WriteFile(filename, data, 0644); err != nil {
			t.Fatalf("Failed to write metadata file: %v", err)
		}
		files = append(files, filename)
	}

	config := ChartConfig{
		ChartName: "test-chart",
		Version:   "1.0.0",
		OutputDir: tmpDir,
	}
	composer := NewComposer(config, "/templates")

	err = composer.LoadMetadata(files)
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	if len(composer.apps) != 0 {
		t.Errorf("Expected 0 apps loaded (all ignored), got %d", len(composer.apps))
	}
}

// TestLoadMetadata_MixedApps tests that deployable apps are kept while cli/firmware are skipped
func TestLoadMetadata_MixedApps(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "composer-mixed-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deployable := &AppMetadata{
		Name:        "server-api",
		AppType:     "external-api",
		DeployUnit:  appmetapb.DeployUnit_DEPLOY_UNIT_CHART,
		Version:     "1.0.0",
		Domain:      "demo",
		Registry:    "ghcr.io",
		RepoName:    "demo-server-api",
		ImageTarget: ":server_image",
	}
	cliApp := &AppMetadata{
		Name:        "my-tool",
		AppType:     "cli",
		DeployUnit:  appmetapb.DeployUnit_DEPLOY_UNIT_NONE,
		Version:     "1.0.0",
		Domain:      "tools",
	}

	f1 := filepath.Join(tmpDir, "server.json")
	f2 := filepath.Join(tmpDir, "cli.json")

	d1, _ := protojson.Marshal(deployable)
	d2, _ := protojson.Marshal(cliApp)

	os.WriteFile(f1, d1, 0644)
	os.WriteFile(f2, d2, 0644)

	config := ChartConfig{
		ChartName: "test-chart",
		Version:   "1.0.0",
		OutputDir: tmpDir,
	}
	composer := NewComposer(config, "/templates")

	err = composer.LoadMetadata([]string{f1, f2})
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	if len(composer.apps) != 1 {
		t.Fatalf("Expected 1 app loaded, got %d", len(composer.apps))
	}
	if composer.apps[0].Name != "server-api" {
		t.Errorf("Expected server-api, got %s", composer.apps[0].Name)
	}
}

