package helm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// AppMetadata represents the metadata for a single application from release_app
type AppMetadata struct {
	Name         string            `json:"name"`
	AppType      string            `json:"app_type"`
	Version      string            `json:"version"`
	Description  string            `json:"description"`
	Registry     string            `json:"registry"`
	Organization string            `json:"organization"`
	RepoName     string            `json:"repo_name"`
	ImageTarget  string            `json:"image_target"`
	Domain       string            `json:"domain"`
	Language     string            `json:"language"`
	Port         int               `json:"port,omitempty"`
	Replicas     int               `json:"replicas,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	Dependencies []string          `json:"dependencies,omitempty"`
	HealthCheck  *HealthCheckMeta  `json:"health_check,omitempty"`
	Ingress      *IngressMeta      `json:"ingress,omitempty"`
	Command      []string          `json:"command,omitempty"`
	Args         []string          `json:"args,omitempty"`
}

// HealthCheckMeta represents health check configuration from metadata
type HealthCheckMeta struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
}

// IngressMeta represents ingress configuration from metadata
type IngressMeta struct {
	Host          string `json:"host"`
	TLSSecretName string `json:"tls_secret_name"`
}

// GetImage returns the full image name (registry/organization/repo_name)
func (m *AppMetadata) GetImage() string {
	if m.Registry != "" && m.Organization != "" && m.RepoName != "" {
		return fmt.Sprintf("%s/%s/%s", m.Registry, m.Organization, m.RepoName)
	}
	// Fallback to repo_name only if registry/organization is not set
	return m.RepoName
}

// GetImageTag returns the version tag
func (m *AppMetadata) GetImageTag() string {
	if m.Version != "" {
		return m.Version
	}
	return "latest"
}

// HealthCheckConfig defines health check configuration
type HealthCheckConfig struct {
	Path                string `yaml:"path"`
	Port                int    `yaml:"port,omitempty"`
	InitialDelaySeconds int    `yaml:"initialDelaySeconds,omitempty"`
	PeriodSeconds       int    `yaml:"periodSeconds,omitempty"`
	TimeoutSeconds      int    `yaml:"timeoutSeconds,omitempty"`
	SuccessThreshold    int    `yaml:"successThreshold,omitempty"`
	FailureThreshold    int    `yaml:"failureThreshold,omitempty"`
}

// ValuesResourceConfig is ResourceConfig formatted for values.yaml
type ValuesResourceConfig struct {
	Requests ResourceValues `yaml:"requests"`
	Limits   ResourceValues `yaml:"limits"`
}

// ResourceValues defines CPU and memory values
type ResourceValues struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
}

// ToValuesFormat converts ResourceConfig to ValuesResourceConfig
func (r ResourceConfig) ToValuesFormat() ValuesResourceConfig {
	return ValuesResourceConfig{
		Requests: ResourceValues{
			CPU:    r.RequestsCPU,
			Memory: r.RequestsMemory,
		},
		Limits: ResourceValues{
			CPU:    r.LimitsCPU,
			Memory: r.LimitsMemory,
		},
	}
}

// AppConfig represents the configuration for a single app in values.yaml
type AppConfig struct {
	Type        string               `yaml:"type"`
	Image       string               `yaml:"image"`
	ImageTag    string               `yaml:"imageTag"`
	Port        int                  `yaml:"port,omitempty"`
	Replicas    int                  `yaml:"replicas"`
	Resources   ValuesResourceConfig `yaml:"resources"`
	HealthCheck *HealthCheckConfig   `yaml:"healthCheck,omitempty"`
	Command     []string             `yaml:"command,omitempty"`
	Args        []string             `yaml:"args,omitempty"`
	Env         map[string]string    `yaml:"env,omitempty"`
	Ingress     *AppIngressConfig    `yaml:"ingress,omitempty"` // Per-app ingress config
}

// AppIngressConfig represents per-app ingress configuration
type AppIngressConfig struct {
	Host          string `yaml:"host,omitempty"`
	TLSSecretName string `yaml:"tlsSecretName,omitempty"`
}

// IngressDefaultsConfig represents default ingress settings applied to all external-api ingresses
// Each external-api app gets its own dedicated Ingress resource
type IngressDefaultsConfig struct {
	Enabled     bool              `yaml:"enabled"`
	ClassName   string            `yaml:"className,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

// ValuesData represents the structure of values.yaml
type ValuesData struct {
	Global          GlobalConfig            `yaml:"global"`
	Apps            map[string]AppConfig    `yaml:"apps"`
	IngressDefaults IngressDefaultsConfig   `yaml:"ingressDefaults"`
}

// GlobalConfig represents global configuration
type GlobalConfig struct {
	Namespace   string `yaml:"namespace"`
	Environment string `yaml:"environment"`
}

// TemplateData represents the data passed to Kubernetes resource templates
type TemplateData struct {
	Name        string
	Environment string
	Namespace   string
	Type        AppType
	Image       string
	ImageTag    string
	Port        int
	Replicas    int
	Resources   ValuesResourceConfig
	HealthCheck *HealthCheckConfig
	Command     []string
	Args        []string
	Env         map[string]string
	Labels      map[string]string
	Annotations map[string]string
}

// ChartConfig represents configuration for chart generation
type ChartConfig struct {
	ChartName   string
	Version     string
	Environment string
	Namespace   string
	OutputDir   string
}

// Composer handles Helm chart composition
type Composer struct {
	config        ChartConfig
	apps          []AppMetadata
	manifests     []ManifestFile
	templateDir   string
	templateFuncs template.FuncMap
}

// ManifestFile represents a manual Kubernetes manifest
type ManifestFile struct {
	Path     string
	Content  []byte
	Filename string
}

// NewComposer creates a new Composer instance
func NewComposer(config ChartConfig, templateDir string) *Composer {
	c := &Composer{
		config:      config,
		templateDir: templateDir,
	}
	c.setupTemplateFuncs()
	return c
}

// setupTemplateFuncs configures template helper functions
func (c *Composer) setupTemplateFuncs() {
	c.templateFuncs = template.FuncMap{
		"toYaml": func(v interface{}) string {
			// Simple YAML marshaller for basic types
			return formatYAML(v, 2)
		},
		"default": func(defaultVal interface{}, val interface{}) interface{} {
			if val == nil {
				return defaultVal
			}
			// Check for empty strings
			if s, ok := val.(string); ok && s == "" {
				return defaultVal
			}
			// Check for zero values
			if i, ok := val.(int); ok && i == 0 {
				return defaultVal
			}
			return val
		},
		"required": func(warn string, val interface{}) (interface{}, error) {
			if val == nil {
				return nil, fmt.Errorf("required value not provided: %s", warn)
			}
			if s, ok := val.(string); ok && s == "" {
				return nil, fmt.Errorf("required value not provided: %s", warn)
			}
			return val, nil
		},
	}
}

// formatYAML converts a value to YAML format with indentation
func formatYAML(v interface{}, indent int) string {
	if v == nil {
		return "null"
	}

	prefix := strings.Repeat(" ", indent)

	switch val := v.(type) {
	case string:
		return fmt.Sprintf("%s%s", prefix, val)
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%s%v", prefix, val)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%s%v", prefix, val)
	case float32, float64:
		return fmt.Sprintf("%s%v", prefix, val)
	case bool:
		return fmt.Sprintf("%s%v", prefix, val)
	case []string:
		lines := make([]string, len(val))
		for i, s := range val {
			lines[i] = fmt.Sprintf("%s- %q", prefix, s)
		}
		return strings.Join(lines, "\n")
	case []interface{}:
		lines := make([]string, len(val))
		for i, item := range val {
			lines[i] = fmt.Sprintf("%s- %v", prefix, item)
		}
		return strings.Join(lines, "\n")
	case map[string]string:
		if len(val) == 0 {
			return prefix + "{}"
		}
		lines := make([]string, 0, len(val))
		for k, v := range val {
			lines = append(lines, fmt.Sprintf("%s%s: %q", prefix, k, v))
		}
		return strings.Join(lines, "\n")
	case map[string]interface{}:
		if len(val) == 0 {
			return prefix + "{}"
		}
		lines := make([]string, 0, len(val))
		for k, v := range val {
			lines = append(lines, fmt.Sprintf("%s%s: %v", prefix, k, v))
		}
		return strings.Join(lines, "\n")
	default:
		// Fallback for unknown types
		return fmt.Sprintf("%s%v", prefix, val)
	}
}

// LoadMetadata loads app metadata from JSON files
func (c *Composer) LoadMetadata(metadataFiles []string) error {
	for _, file := range metadataFiles {
		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("failed to read metadata file %s: %w", file, err)
		}

		var metadata AppMetadata
		if err := json.Unmarshal(data, &metadata); err != nil {
			return fmt.Errorf("failed to parse metadata file %s: %w", file, err)
		}

		c.apps = append(c.apps, metadata)
	}
	return nil
}

// LoadManifests loads manual Kubernetes manifest files
func (c *Composer) LoadManifests(manifestFiles []string) error {
	for _, file := range manifestFiles {
		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("failed to read manifest file %s: %w", file, err)
		}

		manifest := ManifestFile{
			Path:     file,
			Content:  data,
			Filename: filepath.Base(file),
		}

		c.manifests = append(c.manifests, manifest)
	}
	return nil
}

// GenerateChart generates a complete Helm chart
func (c *Composer) GenerateChart() error {
	// Create output directory structure
	chartDir := filepath.Join(c.config.OutputDir, c.config.ChartName)
	templatesDir := filepath.Join(chartDir, "templates")

	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		return fmt.Errorf("failed to create chart directory: %w", err)
	}

	// Generate Chart.yaml
	if err := c.generateChartYaml(chartDir); err != nil {
		return fmt.Errorf("failed to generate Chart.yaml: %w", err)
	}

	// Generate values.yaml
	if err := c.generateValuesYaml(chartDir); err != nil {
		return fmt.Errorf("failed to generate values.yaml: %w", err)
	}

	// Generate Kubernetes resource templates
	if err := c.generateResourceTemplates(templatesDir); err != nil {
		return fmt.Errorf("failed to generate resource templates: %w", err)
	}

	return nil
}

// generateChartYaml generates the Chart.yaml file
func (c *Composer) generateChartYaml(chartDir string) error {
	chartTemplate := filepath.Join(c.templateDir, "base", "Chart.yaml.tmpl")
	outputFile := filepath.Join(chartDir, "Chart.yaml")

	tmpl, err := template.ParseFiles(chartTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse Chart.yaml template: %w", err)
	}

	data := map[string]string{
		"ChartName":    c.config.ChartName,
		"Description":  "Composed Helm chart for multiple applications",
		"ChartVersion": c.config.Version,
		"AppVersion":   c.config.Version,
	}

	f, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("failed to create Chart.yaml: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("failed to render Chart.yaml: %w", err)
	}

	return nil
}

// buildAppConfig creates an AppConfig from AppMetadata with smart defaults
func (c *Composer) buildAppConfig(app AppMetadata) (AppConfig, error) {
	appType, err := ResolveAppType(app.Name, app.AppType)
	if err != nil {
		return AppConfig{}, fmt.Errorf("failed to resolve app type: %w", err)
	}

	// Get default resources for this app type and language
	resources := appType.DefaultResourceConfigForLanguage(app.Language)

	// Set replicas: use metadata if provided, otherwise default based on type
	replicas := app.Replicas
	if replicas == 0 {
		replicas = 1
		if appType == ExternalAPI || appType == InternalAPI {
			replicas = 2
		}
	}

	// Set default port
	port := app.Port
	if port == 0 && (appType == ExternalAPI || appType == InternalAPI) {
		port = 8000
	}

	config := AppConfig{
		Type:      appType.String(),
		Image:     app.GetImage(),
		ImageTag:  app.GetImageTag(),
		Port:      port,
		Replicas:  replicas,
		Resources: resources.ToValuesFormat(),
		Command:   app.Command, // Use command from metadata
		Args:      app.Args,    // Use args from metadata
	}

	// Add health check for APIs based on metadata or defaults
	if appType == ExternalAPI || appType == InternalAPI {
		if app.HealthCheck != nil && app.HealthCheck.Enabled {
			// Use health check path from metadata
			config.HealthCheck = &HealthCheckConfig{
				Path:                app.HealthCheck.Path,
				InitialDelaySeconds: 10,
				PeriodSeconds:       10,
				TimeoutSeconds:      5,
				SuccessThreshold:    1,
				FailureThreshold:    3,
			}
		}
		// If HealthCheck is nil or Enabled is false, don't add health check (nil)
	}

	// Add per-app ingress configuration if provided
	if app.Ingress != nil && app.Ingress.Host != "" {
		config.Ingress = &AppIngressConfig{
			Host:          app.Ingress.Host,
			TLSSecretName: app.Ingress.TLSSecretName,
		}
	}

	return config, nil
}

// generateValuesYaml generates the values.yaml file
func (c *Composer) generateValuesYaml(chartDir string) error {
	valuesData := ValuesData{
		Global: GlobalConfig{
			Namespace:   c.config.Namespace,
			Environment: c.config.Environment,
		},
		Apps: make(map[string]AppConfig),
		IngressDefaults: IngressDefaultsConfig{
			Enabled: c.hasExternalAPIs(),
		},
	}

	// Build app configurations
	for _, app := range c.apps {
		config, err := c.buildAppConfig(app)
		if err != nil {
			return fmt.Errorf("failed to build config for %s: %w", app.Name, err)
		}
		// Use domain-app format as the key (e.g., "demo-hello-job" instead of just "hello-job")
		appKey := fmt.Sprintf("%s-%s", app.Domain, app.Name)
		valuesData.Apps[appKey] = config
	}

	outputFile := filepath.Join(chartDir, "values.yaml")
	f, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("failed to create values.yaml: %w", err)
	}
	defer f.Close()

	if err := writeValuesYAML(f, valuesData); err != nil {
		return fmt.Errorf("failed to write values.yaml: %w", err)
	}

	return nil
}

// YAMLWriter provides methods for writing structured YAML
type YAMLWriter struct {
	f      *os.File
	indent int
}

// NewYAMLWriter creates a new YAML writer
func NewYAMLWriter(f *os.File) *YAMLWriter {
	return &YAMLWriter{f: f, indent: 0}
}

// WriteKey writes a key with optional value (for starting sections)
func (w *YAMLWriter) WriteKey(key string, value ...string) {
	prefix := strings.Repeat(" ", w.indent)
	if len(value) > 0 {
		fmt.Fprintf(w.f, "%s%s: %s\n", prefix, key, value[0])
	} else {
		fmt.Fprintf(w.f, "%s%s:\n", prefix, key)
	}
}

// WriteString writes a string value
func (w *YAMLWriter) WriteString(key, value string) {
	if value != "" {
		prefix := strings.Repeat(" ", w.indent)
		fmt.Fprintf(w.f, "%s%s: %s\n", prefix, key, value)
	}
}

// WriteInt writes an integer value
func (w *YAMLWriter) WriteInt(key string, value int) {
	prefix := strings.Repeat(" ", w.indent)
	fmt.Fprintf(w.f, "%s%s: %d\n", prefix, key, value)
}

// WriteIntIf writes an integer value only if condition is true
func (w *YAMLWriter) WriteIntIf(key string, value int, condition bool) {
	if condition {
		w.WriteInt(key, value)
	}
}

// WriteBool writes a boolean value
func (w *YAMLWriter) WriteBool(key string, value bool) {
	prefix := strings.Repeat(" ", w.indent)
	fmt.Fprintf(w.f, "%s%s: %t\n", prefix, key, value)
}

// WriteList writes a list of quoted strings
func (w *YAMLWriter) WriteList(key string, items []string) {
	if len(items) == 0 {
		return
	}
	w.WriteKey(key)
	w.indent += 2
	for _, item := range items {
		prefix := strings.Repeat(" ", w.indent)
		fmt.Fprintf(w.f, "%s- %q\n", prefix, item)
	}
	w.indent -= 2
}

// WriteMap writes a map of string key-value pairs
func (w *YAMLWriter) WriteMap(key string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	w.WriteKey(key)
	w.indent += 2
	for k, v := range m {
		prefix := strings.Repeat(" ", w.indent)
		fmt.Fprintf(w.f, "%s%s: %q\n", prefix, k, v)
	}
	w.indent -= 2
}

// WriteEmptyList writes an empty array with optional comment
func (w *YAMLWriter) WriteEmptyList(key string, comment ...string) {
	prefix := strings.Repeat(" ", w.indent)
	if len(comment) > 0 {
		// Write key with empty list
		fmt.Fprintf(w.f, "%s%s: []\n", prefix, key)
		// Write comments after
		for _, c := range comment {
			fmt.Fprintf(w.f, "%s# %s\n", prefix, c)
		}
	} else {
		// Just write key with empty list
		fmt.Fprintf(w.f, "%s%s: []\n", prefix, key)
	}
}

// WriteStructList writes a list of structured objects using a callback
func (w *YAMLWriter) WriteStructList(key string, count int, writeItem func(index int)) {
	if count == 0 {
		return
	}
	w.WriteKey(key)
	w.indent += 2
	for i := 0; i < count; i++ {
		prefix := strings.Repeat(" ", w.indent)
		fmt.Fprintf(w.f, "%s-", prefix)
		w.indent += 1
		writeItem(i)
		w.indent -= 1
	}
	w.indent -= 2
}

// StartSection begins a new indented section
func (w *YAMLWriter) StartSection(key string) {
	w.WriteKey(key)
	w.indent += 2
}

// EndSection completes a section and dedents
func (w *YAMLWriter) EndSection() {
	w.indent -= 2
}

// Newline writes a blank line
func (w *YAMLWriter) Newline() {
	fmt.Fprintf(w.f, "\n")
}

// writeValuesYAML writes the values data in YAML format
func writeValuesYAML(f *os.File, data ValuesData) error {
	w := NewYAMLWriter(f)

	// Write global section
	w.StartSection("global")
	w.WriteString("namespace", data.Global.Namespace)
	w.WriteString("environment", data.Global.Environment)
	w.EndSection()
	w.Newline()

	// Write apps section
	w.StartSection("apps")
	for name, app := range data.Apps {
		w.StartSection(name)
		w.WriteString("type", app.Type)
		w.WriteString("image", app.Image)
		w.WriteString("imageTag", app.ImageTag)
		w.WriteIntIf("port", app.Port, app.Port > 0)
		w.WriteInt("replicas", app.Replicas)

		// Resources
		w.StartSection("resources")
		w.StartSection("requests")
		w.WriteString("cpu", app.Resources.Requests.CPU)
		w.WriteString("memory", app.Resources.Requests.Memory)
		w.EndSection()
		w.StartSection("limits")
		w.WriteString("cpu", app.Resources.Limits.CPU)
		w.WriteString("memory", app.Resources.Limits.Memory)
		w.EndSection()
		w.EndSection()

		// Health check if present
		if app.HealthCheck != nil {
			w.StartSection("healthCheck")
			w.WriteString("path", app.HealthCheck.Path)
			w.WriteIntIf("port", app.HealthCheck.Port, app.HealthCheck.Port > 0)
			w.WriteInt("initialDelaySeconds", app.HealthCheck.InitialDelaySeconds)
			w.WriteInt("periodSeconds", app.HealthCheck.PeriodSeconds)
			w.WriteInt("timeoutSeconds", app.HealthCheck.TimeoutSeconds)
			w.WriteInt("successThreshold", app.HealthCheck.SuccessThreshold)
			w.WriteInt("failureThreshold", app.HealthCheck.FailureThreshold)
			w.EndSection()
		}

		// Per-app ingress config if present
		if app.Ingress != nil {
			w.StartSection("ingress")
			w.WriteString("host", app.Ingress.Host)
			w.WriteString("tlsSecretName", app.Ingress.TLSSecretName)
			w.EndSection()
		}

		// Command, Args, Env
		w.WriteList("command", app.Command)
		w.WriteList("args", app.Args)
		w.WriteMap("env", app.Env)

		w.EndSection()
		w.Newline()
	}
	w.EndSection()

	// Write ingress defaults section
	// Note: Each external-api app gets its own dedicated Ingress resource
	// These are default settings applied to all ingresses
	w.StartSection("ingressDefaults")
	w.WriteBool("enabled", data.IngressDefaults.Enabled)
	w.WriteString("className", data.IngressDefaults.ClassName)
	w.WriteMap("annotations", data.IngressDefaults.Annotations)
	w.EndSection()
	w.Newline()

	// Write manifests section (for manual Kubernetes objects)
	w.StartSection("manifests")
	w.WriteBool("enabled", true)
	w.EndSection()

	return nil
}

// generateResourceTemplates generates Kubernetes resource templates
func (c *Composer) generateResourceTemplates(templatesDir string) error {
	// Determine which templates are needed
	templateMap := make(map[string]bool)
	for _, app := range c.apps {
		appType, err := ResolveAppType(app.Name, app.AppType)
		if err != nil {
			return fmt.Errorf("failed to resolve app type for %s: %w", app.Name, err)
		}

		for _, tmpl := range appType.TemplateArtifacts() {
			templateMap[tmpl] = true
		}
	}

	// Copy and process templates
	for tmpl := range templateMap {
		// Source files have .tmpl extension
		srcPath := filepath.Join(c.templateDir, tmpl+".tmpl")
		// Destination files keep original name (without .tmpl)
		dstPath := filepath.Join(templatesDir, tmpl)

		if err := c.copyTemplate(srcPath, dstPath); err != nil {
			return fmt.Errorf("failed to copy template %s: %w", tmpl, err)
		}
	}

	// Process and copy manual manifests
	if err := c.processManualManifests(templatesDir); err != nil {
		return fmt.Errorf("failed to process manual manifests: %w", err)
	}

	return nil
}

// copyTemplate copies a template file
func (c *Composer) copyTemplate(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to read template: %w", err)
	}

	if err := os.WriteFile(dst, data, 0644); err != nil {
		return fmt.Errorf("failed to write template: %w", err)
	}

	return nil
}

// processManualManifests processes and copies manual Kubernetes manifests
func (c *Composer) processManualManifests(templatesDir string) error {
	for i, manifest := range c.manifests {
		// Generate a unique filename with prefix to avoid conflicts
		dstFilename := fmt.Sprintf("manifest-%02d-%s", i, manifest.Filename)
		dstPath := filepath.Join(templatesDir, dstFilename)

		// Process the manifest to inject Helm templating
		processed, err := c.injectHelmTemplating(manifest.Content)
		if err != nil {
			return fmt.Errorf("failed to process manifest %s: %w", manifest.Filename, err)
		}

		if err := os.WriteFile(dstPath, processed, 0644); err != nil {
			return fmt.Errorf("failed to write manifest %s: %w", manifest.Filename, err)
		}
	}
	return nil
}

// injectHelmTemplating processes a Kubernetes manifest to inject Helm template directives
func (c *Composer) injectHelmTemplating(content []byte) ([]byte, error) {
	// Parse the YAML to understand its structure
	contentStr := string(content)

	// Add Helm template header comment
	helmContent := "{{- if .Values.manifests.enabled | default true }}\n"

	// Replace namespace references with Helm template
	// Match: namespace: <value>
	// TODO: This string-based approach is fragile and could incorrectly match namespace
	// references in comments or string values. Consider using a proper YAML parser
	// (e.g., gopkg.in/yaml.v3 with node manipulation) for more reliable template injection.
	contentStr = strings.ReplaceAll(contentStr,
		"namespace: default",
		"namespace: {{ .Values.global.namespace }}")

	// For any other namespace value, replace with template
	lines := strings.Split(contentStr, "\n")
	for i, line := range lines {
		// Match lines like "  namespace: some-namespace"
		if strings.Contains(line, "namespace:") && !strings.Contains(line, "{{") {
			// Extract indentation
			indent := len(line) - len(strings.TrimLeft(line, " \t"))
			spaces := strings.Repeat(" ", indent)
			lines[i] = spaces + "namespace: {{ .Values.global.namespace }}"
		}

		// Add environment label if labels section exists
		if strings.Contains(line, "labels:") && i+1 < len(lines) {
			// Check if next line is already indented (part of labels)
			nextLine := lines[i+1]
			if len(nextLine) > 0 && (nextLine[0] == ' ' || nextLine[0] == '\t') {
				// Add environment label after the labels: line
				indent := len(nextLine) - len(strings.TrimLeft(nextLine, " \t"))
				spaces := strings.Repeat(" ", indent)
				envLabel := spaces + "environment: {{ .Values.global.environment }}"
				// Insert if not already present
				if !strings.Contains(contentStr, "environment:") {
					lines = append(lines[:i+1], append([]string{envLabel}, lines[i+1:]...)...)
				}
			}
		}
	}

	contentStr = strings.Join(lines, "\n")
	helmContent += contentStr
	helmContent += "\n{{- end }}\n"

	return []byte(helmContent), nil
}

// hasExternalAPIs checks if any apps are external APIs
func (c *Composer) hasExternalAPIs() bool {
	for _, app := range c.apps {
		appType, err := ResolveAppType(app.Name, app.AppType)
		if err != nil {
			continue
		}
		if appType == ExternalAPI {
			return true
		}
	}
	return false
}
