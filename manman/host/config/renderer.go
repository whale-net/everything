package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	pb "github.com/whale-net/everything/manman/protos"
)

// Renderer handles configuration strategy rendering
type Renderer struct {
	logger *log.Logger
}

// NewRenderer creates a new configuration renderer
func NewRenderer(logger *log.Logger) *Renderer {
	if logger == nil {
		logger = log.Default()
	}
	return &Renderer{
		logger: logger,
	}
}

// RenderContext contains all the context needed for rendering
type RenderContext struct {
	GameID          int64
	GameConfigID    int64
	ServerGameConfigID int64
	SessionID       int64
	BaseDataDir     string // e.g., /tmp/manman-data/sgc-dev-1
}

// RenderedFile represents a rendered configuration file
type RenderedFile struct {
	Path    string // Relative path within the container (e.g., /data/server.properties)
	Content string // Rendered content
	HostPath string // Absolute path on host where file should be written
}

// RenderConfigurations renders all configuration strategies from API response
func (r *Renderer) RenderConfigurations(configurations []*pb.RenderedConfiguration, baseDataDir string) ([]*RenderedFile, error) {
	r.logger.Printf("[config-renderer] Starting configuration rendering for %d configurations", len(configurations))

	if len(configurations) == 0 {
		r.logger.Printf("[config-renderer] No configurations to render")
		return nil, nil
	}

	var renderedFiles []*RenderedFile

	for _, config := range configurations {
		r.logger.Printf("[config-renderer] Processing configuration: %s (type: %s)", config.StrategyName, config.StrategyType)

		// Render based on strategy type
		switch config.StrategyType {
		case "file_properties":
			file, err := r.renderPropertiesFileFromConfig(config, baseDataDir)
			if err != nil {
				return nil, fmt.Errorf("failed to render properties file for %s: %w", config.StrategyName, err)
			}
			if file != nil {
				renderedFiles = append(renderedFiles, file)
			}

		case "env_vars":
			// TODO: Implement env vars rendering
			r.logger.Printf("[config-renderer] Env vars rendering not yet implemented for: %s", config.StrategyName)

		case "cli_args":
			// TODO: Implement CLI args rendering
			r.logger.Printf("[config-renderer] CLI args rendering not yet implemented for: %s", config.StrategyName)

		case "file_json":
			// TODO: Implement JSON file rendering
			r.logger.Printf("[config-renderer] JSON file rendering not yet implemented for: %s", config.StrategyName)

		case "file_yaml":
			// TODO: Implement YAML file rendering
			r.logger.Printf("[config-renderer] YAML file rendering not yet implemented for: %s", config.StrategyName)

		default:
			r.logger.Printf("[config-renderer] Unknown strategy type: %s for: %s", config.StrategyType, config.StrategyName)
		}
	}

	r.logger.Printf("[config-renderer] Rendered %d configuration files", len(renderedFiles))
	return renderedFiles, nil
}

// WriteRenderedFiles writes all rendered files to disk
func (r *Renderer) WriteRenderedFiles(files []*RenderedFile) error {
	for _, file := range files {
		r.logger.Printf("[config-renderer] Writing file: %s", file.HostPath)

		// Ensure parent directory exists
		dir := filepath.Dir(file.HostPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}

		// Write file
		if err := os.WriteFile(file.HostPath, []byte(file.Content), 0644); err != nil {
			return fmt.Errorf("failed to write file %s: %w", file.HostPath, err)
		}

		r.logger.Printf("[config-renderer] Successfully wrote file: %s (%d bytes)", file.HostPath, len(file.Content))
	}

	return nil
}

// renderPropertiesFileFromConfig renders a Java properties file from API configuration
func (r *Renderer) renderPropertiesFileFromConfig(config *pb.RenderedConfiguration, baseDataDir string) (*RenderedFile, error) {
	// Determine host path
	if config.TargetPath == "" {
		return nil, fmt.Errorf("no target path specified for configuration %s", config.StrategyName)
	}

	// Map container path to host path: /data/foo -> {BaseDataDir}/data/foo
	relativePath := strings.TrimPrefix(config.TargetPath, "/")
	hostPath := filepath.Join(baseDataDir, relativePath)

	var properties map[string]string

	// Two modes:
	// 1. base_template provided (BaseContent non-empty): Use it as starting point
	// 2. base_template empty: Read existing file and merge (for auto-generating games)
	if config.BaseContent != "" {
		r.logger.Printf("[config-renderer] Using base template for %s", config.StrategyName)
		properties = parsePropertiesFile(config.BaseContent)
	} else {
		r.logger.Printf("[config-renderer] Base template empty, checking for existing file: %s", hostPath)
		// Try to read existing file
		existingContent, err := os.ReadFile(hostPath)
		if err != nil {
			if os.IsNotExist(err) {
				r.logger.Printf("[config-renderer] No existing file found, starting with empty properties")
				properties = make(map[string]string)
			} else {
				return nil, fmt.Errorf("failed to read existing file %s: %w", hostPath, err)
			}
		} else {
			r.logger.Printf("[config-renderer] Read existing file (%d bytes), merging changes", len(existingContent))
			properties = parsePropertiesFile(string(existingContent))
		}
	}

	// Apply overrides from rendered content (parameter bindings, patches)
	// If RenderedContent has values, they override the base/existing
	if config.RenderedContent != "" && config.RenderedContent != config.BaseContent {
		r.logger.Printf("[config-renderer] Applying overrides from rendered content")
		overrides := parsePropertiesFile(config.RenderedContent)
		for key, value := range overrides {
			properties[key] = value
		}
	}

	// Render final content
	finalContent := renderPropertiesMap(properties)

	return &RenderedFile{
		Path:     config.TargetPath,
		Content:  finalContent,
		HostPath: hostPath,
	}, nil
}

// parsePropertiesFile parses a Java properties file into a map
func parsePropertiesFile(content string) map[string]string {
	properties := make(map[string]string)
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}

		// Find the first = or : separator
		sepIndex := strings.IndexAny(line, "=:")
		if sepIndex == -1 {
			continue
		}

		key := strings.TrimSpace(line[:sepIndex])
		value := strings.TrimSpace(line[sepIndex+1:])

		properties[key] = value
	}

	return properties
}

// renderPropertiesMap renders a map into Java properties format
func renderPropertiesMap(properties map[string]string) string {
	var lines []string

	// Sort keys for consistent output
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}

	// Simple alphabetical sort (good enough for now)
	for i := 0; i < len(keys)-1; i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", key, properties[key]))
	}

	return strings.Join(lines, "\n")
}
