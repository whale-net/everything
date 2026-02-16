package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	pb "github.com/whale-net/everything/manmanv2/protos"
)

// Renderer handles configuration strategy rendering
type Renderer struct {
	logger *slog.Logger
}

// NewRenderer creates a new configuration renderer
func NewRenderer(logger *slog.Logger) *Renderer {
	if logger == nil {
		logger = slog.Default()
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
	r.logger.Info("starting configuration rendering", "count", len(configurations))

	if len(configurations) == 0 {
		r.logger.Debug("no configurations to render")
		return nil, nil
	}

	var renderedFiles []*RenderedFile

	for _, config := range configurations {
		r.logger.Info("processing configuration", "strategy_name", config.StrategyName, "strategy_type", config.StrategyType)

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
			r.logger.Warn("env vars rendering not yet implemented", "strategy_name", config.StrategyName)

		case "cli_args":
			r.logger.Warn("CLI args rendering not yet implemented", "strategy_name", config.StrategyName)

		case "file_json":
			r.logger.Warn("JSON file rendering not yet implemented", "strategy_name", config.StrategyName)

		case "file_yaml":
			r.logger.Warn("YAML file rendering not yet implemented", "strategy_name", config.StrategyName)

		default:
			r.logger.Warn("unknown strategy type", "strategy_type", config.StrategyType, "strategy_name", config.StrategyName)
		}
	}

	r.logger.Info("rendered configuration files", "count", len(renderedFiles))
	return renderedFiles, nil
}

// WriteRenderedFiles writes all rendered files to disk
func (r *Renderer) WriteRenderedFiles(files []*RenderedFile) error {
	for _, file := range files {
		r.logger.Info("writing file", "path", file.HostPath)

		// Ensure parent directory exists
		dir := filepath.Dir(file.HostPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}

		// Write file
		if err := os.WriteFile(file.HostPath, []byte(file.Content), 0644); err != nil {
			return fmt.Errorf("failed to write file %s: %w", file.HostPath, err)
		}

		r.logger.Debug("wrote file", "path", file.HostPath, "bytes", len(file.Content))
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
		r.logger.Debug("using base template", "strategy_name", config.StrategyName)
		properties = parsePropertiesFile(config.BaseContent)
	} else {
		r.logger.Debug("base template empty, checking for existing file", "path", hostPath)
		existingContent, err := os.ReadFile(hostPath)
		if err != nil {
			if os.IsNotExist(err) {
				r.logger.Debug("no existing file found, starting with empty properties")
				properties = make(map[string]string)
			} else {
				return nil, fmt.Errorf("failed to read existing file %s: %w", hostPath, err)
			}
		} else {
			r.logger.Debug("read existing file, merging changes", "bytes", len(existingContent))
			properties = parsePropertiesFile(string(existingContent))
		}
	}

	if config.RenderedContent != "" && config.RenderedContent != config.BaseContent {
		r.logger.Debug("applying overrides from rendered content")
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
