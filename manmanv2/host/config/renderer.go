package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	manman "github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"gopkg.in/yaml.v3"
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
	GameID             int64
	GameConfigID       int64
	ServerGameConfigID int64
	SessionID          int64
	BaseDataDir        string // e.g., /tmp/manman-data/sgc-dev-1
}

// RenderedFile represents a rendered configuration file
type RenderedFile struct {
	Path     string // Relative path within the container (e.g., /data/server.properties)
	Content  string // Rendered content
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
		r.logger.Info("processing configuration", "strategy_name", config.StrategyName, "strategy_type", config.StrategyType, "patch_format", config.PatchFormat)

		var file *RenderedFile
		var err error

		switch config.StrategyType {
		case manman.StrategyTypeFileProperties:
			file, err = r.renderPropertiesFileFromConfig(config, baseDataDir)

		case manman.StrategyTypeEnvVars:
			file, err = r.renderEnvVarsFromConfig(config, baseDataDir)

		case manman.StrategyTypeCLIArgs:
			file, err = r.renderCLIArgsFromConfig(config, baseDataDir)

		case manman.StrategyTypeFileJSON:
			file, err = r.renderJSONFileFromConfig(config, baseDataDir)

		case manman.StrategyTypeFileYAML:
			file, err = r.renderYAMLFileFromConfig(config, baseDataDir)

		case manman.StrategyTypeFileINI:
			file, err = r.renderINIFileFromConfig(config, baseDataDir)

		case manman.StrategyTypeFileXML, manman.StrategyTypeFileLua, manman.StrategyTypeFileCustom:
			file, err = r.renderOpaqueFileFromConfig(config, baseDataDir)

		case manman.StrategyTypeVolume:
			// Volumes are mounted directly by the host-manager's Docker integration,
			// not written to disk via the config renderer.
			r.logger.Debug("volume strategies are applied as bind mounts, skipping", "strategy_name", config.StrategyName)

		default:
			r.logger.Warn("unknown strategy type", "strategy_type", config.StrategyType, "strategy_name", config.StrategyName)
		}

		if err != nil {
			return nil, fmt.Errorf("failed to render %s for %s: %w", config.StrategyType, config.StrategyName, err)
		}
		if file != nil {
			renderedFiles = append(renderedFiles, file)
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

// resolveHostPath maps a configuration's container-relative target path to an
// absolute host path. A target path is required for every file-backed strategy.
func (r *Renderer) resolveHostPath(config *pb.RenderedConfiguration, baseDataDir string) (string, error) {
	if config.TargetPath == "" {
		return "", fmt.Errorf("no target path specified for configuration %s", config.StrategyName)
	}
	relativePath := strings.TrimPrefix(config.TargetPath, "/")
	return filepath.Join(baseDataDir, relativePath), nil
}

// loadBase resolves the starting content for a configuration: the strategy's
// base_template if provided, otherwise the existing file on disk (merge mode),
// otherwise empty.
func (r *Renderer) loadBase(config *pb.RenderedConfiguration, hostPath string) (string, error) {
	if config.BaseContent != "" {
		r.logger.Debug("using base template", "strategy_name", config.StrategyName)
		return config.BaseContent, nil
	}

	r.logger.Debug("base template empty, checking for existing file", "path", hostPath)
	existingContent, err := os.ReadFile(hostPath)
	if err != nil {
		if os.IsNotExist(err) {
			r.logger.Debug("no existing file found, starting empty")
			return "", nil
		}
		return "", fmt.Errorf("failed to read existing file %s: %w", hostPath, err)
	}

	r.logger.Debug("read existing file, merging changes", "bytes", len(existingContent))
	return string(existingContent), nil
}

// hasOverride reports whether a configuration carries patch content distinct
// from its base template.
func hasOverride(config *pb.RenderedConfiguration) bool {
	return config.RenderedContent != "" && config.RenderedContent != config.BaseContent
}

// renderPropertiesFileFromConfig renders a Java properties file from API configuration
func (r *Renderer) renderPropertiesFileFromConfig(config *pb.RenderedConfiguration, baseDataDir string) (*RenderedFile, error) {
	hostPath, err := r.resolveHostPath(config, baseDataDir)
	if err != nil {
		return nil, err
	}

	base, err := r.loadBase(config, hostPath)
	if err != nil {
		return nil, err
	}
	properties := parsePropertiesFile(base)

	if hasOverride(config) {
		r.logger.Debug("applying overrides from rendered content")
		overrides := parsePropertiesFile(config.RenderedContent)
		for key, value := range overrides {
			properties[key] = value
		}
	}

	return &RenderedFile{
		Path:     config.TargetPath,
		Content:  renderPropertiesMap(properties),
		HostPath: hostPath,
	}, nil
}

// renderEnvVarsFromConfig renders environment variables in KEY=VALUE form.
// Env var strategies aren't always file-backed (they may be consumed directly
// as container env), so a missing target path is not an error.
func (r *Renderer) renderEnvVarsFromConfig(config *pb.RenderedConfiguration, baseDataDir string) (*RenderedFile, error) {
	if config.TargetPath == "" {
		r.logger.Debug("env_vars strategy has no target path, skipping file render", "strategy_name", config.StrategyName)
		return nil, nil
	}

	hostPath, err := r.resolveHostPath(config, baseDataDir)
	if err != nil {
		return nil, err
	}

	base, err := r.loadBase(config, hostPath)
	if err != nil {
		return nil, err
	}
	properties := parsePropertiesFile(base)

	if hasOverride(config) {
		overrides := parsePropertiesFile(config.RenderedContent)
		for key, value := range overrides {
			properties[key] = value
		}
	}

	return &RenderedFile{
		Path:     config.TargetPath,
		Content:  renderPropertiesMap(properties),
		HostPath: hostPath,
	}, nil
}

// renderCLIArgsFromConfig renders CLI arguments, one per line, preserving the
// original order and overriding by flag name (the token before "="). Like
// env_vars, cli_args may not be file-backed.
func (r *Renderer) renderCLIArgsFromConfig(config *pb.RenderedConfiguration, baseDataDir string) (*RenderedFile, error) {
	if config.TargetPath == "" {
		r.logger.Debug("cli_args strategy has no target path, skipping file render", "strategy_name", config.StrategyName)
		return nil, nil
	}

	hostPath, err := r.resolveHostPath(config, baseDataDir)
	if err != nil {
		return nil, err
	}

	base, err := r.loadBase(config, hostPath)
	if err != nil {
		return nil, err
	}
	args := parseArgLines(base)

	if hasOverride(config) {
		args = mergeArgEntries(args, parseArgLines(config.RenderedContent))
	}

	return &RenderedFile{
		Path:     config.TargetPath,
		Content:  renderArgLines(args),
		HostPath: hostPath,
	}, nil
}

// renderJSONFileFromConfig renders a JSON file, applying the patch according
// to patch_format: json_patch applies RFC 6902-style operations, anything
// else (including the json_merge_patch default) applies an RFC 7386 merge.
func (r *Renderer) renderJSONFileFromConfig(config *pb.RenderedConfiguration, baseDataDir string) (*RenderedFile, error) {
	hostPath, err := r.resolveHostPath(config, baseDataDir)
	if err != nil {
		return nil, err
	}

	base, err := r.loadBase(config, hostPath)
	if err != nil {
		return nil, err
	}

	var doc interface{} = map[string]interface{}{}
	if strings.TrimSpace(base) != "" {
		if err := json.Unmarshal([]byte(base), &doc); err != nil {
			return nil, fmt.Errorf("failed to parse base JSON: %w", err)
		}
	}

	if hasOverride(config) {
		switch config.PatchFormat {
		case manman.PatchFormatJSONPatch:
			doc, err = applyJSONPatch(doc, config.RenderedContent)
			if err != nil {
				return nil, fmt.Errorf("failed to apply json_patch: %w", err)
			}
		default:
			var patch interface{}
			if err := json.Unmarshal([]byte(config.RenderedContent), &patch); err != nil {
				return nil, fmt.Errorf("failed to parse patch JSON: %w", err)
			}
			doc = mergeStructured(doc, patch)
		}
	}

	rendered, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to render JSON: %w", err)
	}

	return &RenderedFile{
		Path:     config.TargetPath,
		Content:  string(rendered),
		HostPath: hostPath,
	}, nil
}

// renderYAMLFileFromConfig renders a YAML file by deep-merging the patch onto
// the base document (yaml_merge semantics).
func (r *Renderer) renderYAMLFileFromConfig(config *pb.RenderedConfiguration, baseDataDir string) (*RenderedFile, error) {
	hostPath, err := r.resolveHostPath(config, baseDataDir)
	if err != nil {
		return nil, err
	}

	base, err := r.loadBase(config, hostPath)
	if err != nil {
		return nil, err
	}

	var doc interface{}
	if strings.TrimSpace(base) != "" {
		if err := yaml.Unmarshal([]byte(base), &doc); err != nil {
			return nil, fmt.Errorf("failed to parse base YAML: %w", err)
		}
	}
	if doc == nil {
		doc = map[string]interface{}{}
	}

	if hasOverride(config) {
		var patch interface{}
		if err := yaml.Unmarshal([]byte(config.RenderedContent), &patch); err != nil {
			return nil, fmt.Errorf("failed to parse patch YAML: %w", err)
		}
		doc = mergeStructured(doc, patch)
	}

	rendered, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("failed to render YAML: %w", err)
	}

	return &RenderedFile{
		Path:     config.TargetPath,
		Content:  string(rendered),
		HostPath: hostPath,
	}, nil
}

// renderINIFileFromConfig renders an INI file, merging patches section-by-section.
func (r *Renderer) renderINIFileFromConfig(config *pb.RenderedConfiguration, baseDataDir string) (*RenderedFile, error) {
	hostPath, err := r.resolveHostPath(config, baseDataDir)
	if err != nil {
		return nil, err
	}

	base, err := r.loadBase(config, hostPath)
	if err != nil {
		return nil, err
	}
	sections := parseINIFile(base)

	if hasOverride(config) {
		sections = mergeINISections(sections, parseINIFile(config.RenderedContent))
	}

	return &RenderedFile{
		Path:     config.TargetPath,
		Content:  renderINISections(sections),
		HostPath: hostPath,
	}, nil
}

// renderOpaqueFileFromConfig handles formats with no generically-parseable
// structure (XML, Lua, custom). Since we can't merge unknown structure safely,
// a patch replaces the content wholesale rather than being merged onto the base.
func (r *Renderer) renderOpaqueFileFromConfig(config *pb.RenderedConfiguration, baseDataDir string) (*RenderedFile, error) {
	hostPath, err := r.resolveHostPath(config, baseDataDir)
	if err != nil {
		return nil, err
	}

	content := config.RenderedContent
	if content == "" {
		content, err = r.loadBase(config, hostPath)
		if err != nil {
			return nil, err
		}
	}

	return &RenderedFile{
		Path:     config.TargetPath,
		Content:  content,
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

// argEntry is one CLI argument, keyed by its flag name for override matching.
type argEntry struct {
	Key  string
	Line string
}

// parseArgLines parses CLI arguments, one per line. The key used for override
// matching is the token before the first "=", or the whole line if there is none.
func parseArgLines(content string) []argEntry {
	var entries []argEntry
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key := line
		if idx := strings.Index(line, "="); idx != -1 {
			key = line[:idx]
		}
		entries = append(entries, argEntry{Key: key, Line: line})
	}
	return entries
}

// mergeArgEntries overrides base entries with matching-key overrides in place
// (preserving base order), appending overrides whose key isn't in base.
func mergeArgEntries(base, overrides []argEntry) []argEntry {
	result := make([]argEntry, len(base))
	copy(result, base)

	index := make(map[string]int, len(result))
	for i, e := range result {
		index[e.Key] = i
	}

	for _, o := range overrides {
		if i, ok := index[o.Key]; ok {
			result[i] = o
		} else {
			result = append(result, o)
			index[o.Key] = len(result) - 1
		}
	}

	return result
}

// renderArgLines renders arg entries back to one-per-line text.
func renderArgLines(entries []argEntry) string {
	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = e.Line
	}
	return strings.Join(lines, "\n")
}

// parseINIFile parses an INI document into sections; "" is the global
// (pre-first-header) section.
func parseINIFile(content string) map[string]map[string]string {
	sections := map[string]map[string]string{"": {}}
	current := ""

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := sections[current]; !ok {
				sections[current] = map[string]string{}
			}
			continue
		}

		sepIndex := strings.IndexAny(line, "=:")
		if sepIndex == -1 {
			continue
		}
		key := strings.TrimSpace(line[:sepIndex])
		value := strings.TrimSpace(line[sepIndex+1:])
		sections[current][key] = value
	}

	return sections
}

// mergeINISections merges override sections/keys onto base sections/keys.
func mergeINISections(base, overrides map[string]map[string]string) map[string]map[string]string {
	result := make(map[string]map[string]string, len(base))
	for section, kv := range base {
		copied := make(map[string]string, len(kv))
		for k, v := range kv {
			copied[k] = v
		}
		result[section] = copied
	}

	for section, kv := range overrides {
		if _, ok := result[section]; !ok {
			result[section] = map[string]string{}
		}
		for k, v := range kv {
			result[section][k] = v
		}
	}

	return result
}

// renderINISections renders sections back to INI text: global keys first,
// then named sections in alphabetical order, keys sorted within each section.
func renderINISections(sections map[string]map[string]string) string {
	names := make([]string, 0, len(sections))
	for name := range sections {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var blocks []string
	if global := sections[""]; len(global) > 0 {
		blocks = append(blocks, renderINIKeyValues(global))
	}
	for _, name := range names {
		block := fmt.Sprintf("[%s]", name)
		if body := renderINIKeyValues(sections[name]); body != "" {
			block += "\n" + body
		}
		blocks = append(blocks, block)
	}

	return strings.Join(blocks, "\n\n")
}

func renderINIKeyValues(kv map[string]string) string {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = fmt.Sprintf("%s=%s", k, kv[k])
	}
	return strings.Join(lines, "\n")
}

// mergeStructured deep-merges patch onto target following RFC 7386 JSON Merge
// Patch semantics: object keys merge recursively, a null value deletes the
// key, and any non-object patch value replaces target wholesale. Shared with
// both JSON and YAML rendering since yaml.v3 decodes maps as map[string]interface{}.
func mergeStructured(target, patch interface{}) interface{} {
	patchObj, ok := patch.(map[string]interface{})
	if !ok {
		return patch
	}

	targetObj, ok := target.(map[string]interface{})
	result := make(map[string]interface{}, len(patchObj))
	if ok {
		for k, v := range targetObj {
			result[k] = v
		}
	}

	for k, v := range patchObj {
		if v == nil {
			delete(result, k)
			continue
		}
		result[k] = mergeStructured(result[k], v)
	}

	return result
}

// jsonPatchOp is a single RFC 6902 JSON Patch operation. Only add, replace,
// and remove are supported (no move/copy/test).
type jsonPatchOp struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

// applyJSONPatch applies a JSON array of RFC 6902-style operations to doc.
func applyJSONPatch(doc interface{}, opsContent string) (interface{}, error) {
	var ops []jsonPatchOp
	if err := json.Unmarshal([]byte(opsContent), &ops); err != nil {
		return nil, fmt.Errorf("invalid json_patch operations: %w", err)
	}

	for _, op := range ops {
		updated, err := applyPointerOp(doc, splitJSONPointer(op.Path), op.Op, op.Value)
		if err != nil {
			return nil, fmt.Errorf("op %s %s: %w", op.Op, op.Path, err)
		}
		doc = updated
	}

	return doc, nil
}

// splitJSONPointer splits an RFC 6901 JSON Pointer into unescaped tokens.
func splitJSONPointer(path string) []string {
	if path == "" || path == "/" {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, p := range parts {
		p = strings.ReplaceAll(p, "~1", "/")
		p = strings.ReplaceAll(p, "~0", "~")
		parts[i] = p
	}
	return parts
}

// applyPointerOp applies one op at the given pointer tokens, returning the
// (possibly new) value of node after the change.
func applyPointerOp(node interface{}, tokens []string, op string, value interface{}) (interface{}, error) {
	if len(tokens) == 0 {
		switch op {
		case "add", "replace":
			return value, nil
		case "remove":
			return nil, nil
		default:
			return nil, fmt.Errorf("unsupported op %q", op)
		}
	}

	key := tokens[0]
	rest := tokens[1:]

	switch container := node.(type) {
	case map[string]interface{}:
		newMap := make(map[string]interface{}, len(container))
		for k, v := range container {
			newMap[k] = v
		}
		if len(rest) == 0 {
			switch op {
			case "add", "replace":
				newMap[key] = value
			case "remove":
				delete(newMap, key)
			default:
				return nil, fmt.Errorf("unsupported op %q", op)
			}
			return newMap, nil
		}
		child, ok := newMap[key]
		if !ok && op != "add" {
			return nil, fmt.Errorf("path %q not found", key)
		}
		updated, err := applyPointerOp(child, rest, op, value)
		if err != nil {
			return nil, err
		}
		newMap[key] = updated
		return newMap, nil

	case []interface{}:
		newArr := make([]interface{}, len(container))
		copy(newArr, container)

		if len(rest) == 0 {
			switch op {
			case "replace":
				idx, err := arrayIndex(key, len(newArr), false)
				if err != nil {
					return nil, err
				}
				newArr[idx] = value
				return newArr, nil
			case "add":
				idx, err := arrayIndex(key, len(newArr), true)
				if err != nil {
					return nil, err
				}
				newArr = append(newArr, nil)
				copy(newArr[idx+1:], newArr[idx:])
				newArr[idx] = value
				return newArr, nil
			case "remove":
				idx, err := arrayIndex(key, len(newArr), false)
				if err != nil {
					return nil, err
				}
				newArr = append(newArr[:idx], newArr[idx+1:]...)
				return newArr, nil
			default:
				return nil, fmt.Errorf("unsupported op %q", op)
			}
		}

		idx, err := arrayIndex(key, len(newArr), false)
		if err != nil {
			return nil, err
		}
		updated, err := applyPointerOp(newArr[idx], rest, op, value)
		if err != nil {
			return nil, err
		}
		newArr[idx] = updated
		return newArr, nil

	default:
		return nil, fmt.Errorf("cannot navigate path segment %q into %T", key, node)
	}
}

// arrayIndex resolves a JSON Pointer array token ("-" or a decimal index) to
// a slice position. forInsert allows the one-past-the-end position.
func arrayIndex(token string, length int, forInsert bool) (int, error) {
	if token == "-" {
		if forInsert {
			return length, nil
		}
		return 0, fmt.Errorf("index \"-\" not valid for this operation")
	}

	idx, err := strconv.Atoi(token)
	if err != nil {
		return 0, fmt.Errorf("invalid array index %q: %w", token, err)
	}
	max := length - 1
	if forInsert {
		max = length
	}
	if idx < 0 || idx > max {
		return 0, fmt.Errorf("array index %d out of range", idx)
	}
	return idx, nil
}
