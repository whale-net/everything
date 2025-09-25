// Helm template renderer for Bazel-generated charts
// Uses Go's text/template package (same engine as Helm)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
)

// ChartData represents the data structure for chart generation
// Helm-compatible data structures
type Release struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
}

type Chart struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

// Values represents Helm values.yaml content (using map for flexible field access)
type Values map[string]interface{}

// ChartData represents the data available to templates during rendering (Helm-compatible)
type ChartData struct {
	// Helm standard objects
	Release Release `json:"Release"`
	Chart   Chart   `json:"Chart"`
	Values  Values  `json:"Values"`

	// Our custom data
	ChartName         string            `json:"chartName"`
	Version           string            `json:"version"`
	Description       string            `json:"description"`
	Domain            string            `json:"domain"`
	DeployOrderWeight int               `json:"deployOrderWeight"`
	ChartValues       map[string]string `json:"chartValues"`
	Apps              []App             `json:"apps"`
	Artifacts         []Artifact        `json:"artifacts"`
}

// App represents a release_app
type App struct {
	Name     string `json:"name"`
	RepoName string `json:"repo_name"`
	Registry string `json:"registry"`
	Version  string `json:"version"`
	Language string `json:"language"`
	Domain   string `json:"domain"`
}

// Artifact represents a k8s_artifact
type Artifact struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	ManifestPath     string `json:"manifest_path"`
	HookWeight       int    `json:"hook_weight"`
	HookDeletePolicy string `json:"hook_delete_policy"`
}

// Template functions available to templates (similar to Helm's Sprig)
var templateFuncs = template.FuncMap{
	"default": func(defaultVal interface{}, value interface{}) interface{} {
		if value == nil || value == "" {
			return defaultVal
		}
		return value
	},
	"quote": func(str string) string {
		return fmt.Sprintf(`"%s"`, str)
	},
	"indent": func(spaces int, text string) string {
		padding := strings.Repeat(" ", spaces)
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			if line != "" {
				lines[i] = padding + line
			}
		}
		return strings.Join(lines, "\n")
	},
	"nindent": func(spaces int, text string) string {
		return "\n" + strings.Repeat(" ", spaces) + strings.ReplaceAll(text, "\n", "\n"+strings.Repeat(" ", spaces))
	},
	"trunc": func(length int, text string) string {
		if len(text) <= length {
			return text
		}
		return text[:length]
	},
	"trimSuffix": func(suffix, text string) string {
		return strings.TrimSuffix(text, suffix)
	},
	// Common Helm template functions
	"dict": func(values ...interface{}) map[string]interface{} {
		if len(values)%2 != 0 {
			panic("dict requires an even number of arguments")
		}
		result := make(map[string]interface{})
		for i := 0; i < len(values); i += 2 {
			key := fmt.Sprintf("%v", values[i])
			result[key] = values[i+1]
		}
		return result
	},
	"include": func(name string, data interface{}) (string, error) {
		// This is a placeholder - in real Helm this would render another template
		return fmt.Sprintf("{{ include \"%s\" . }}", name), nil
	},
	"toYaml": func(obj interface{}) string {
		// Simple YAML-like formatting for basic objects
		switch v := obj.(type) {
		case map[string]interface{}:
			var lines []string
			for k, val := range v {
				lines = append(lines, fmt.Sprintf("%s: %v", k, val))
			}
			return strings.Join(lines, "\n")
		case []interface{}:
			var lines []string
			for _, val := range v {
				lines = append(lines, fmt.Sprintf("- %v", val))
			}
			return strings.Join(lines, "\n")
		default:
			return fmt.Sprintf("%v", v)
		}
	},
	"upper": func(s string) string {
		return strings.ToUpper(s)
	},
	"lower": func(s string) string {
		return strings.ToLower(s)
	},
	"contains": func(substr, str string) bool {
		return strings.Contains(str, substr)
	},
	"printf": func(format string, args ...interface{}) string {
		return fmt.Sprintf(format, args...)
	},
	"typeOf": func(obj interface{}) string {
		return fmt.Sprintf("%T", obj)
	},
	"index": func(obj interface{}, keys ...interface{}) interface{} {
		current := obj
		for _, key := range keys {
			switch o := current.(type) {
			case map[string]interface{}:
				current = o[fmt.Sprintf("%v", key)]
			case []interface{}:
				if i, ok := key.(int); ok && i >= 0 && i < len(o) {
					current = o[i]
				} else {
					return nil
				}
			default:
				return nil
			}
		}
		return current
	},
	"replace": func(old, new, src string) string {
		return strings.ReplaceAll(src, old, new)
	},
	"split": func(sep, src string) []string {
		return strings.Split(src, sep)
	},
	"join": func(sep string, elems []string) string {
		return strings.Join(elems, sep)
	},
	"trim": func(cutset, src string) string {
		return strings.Trim(src, cutset)
	},
	"trimPrefix": func(prefix, src string) string {
		return strings.TrimPrefix(src, prefix)
	},
	"hasPrefix": func(prefix, src string) bool {
		return strings.HasPrefix(src, prefix)
	},
	"hasSuffix": func(suffix, src string) bool {
		return strings.HasSuffix(src, suffix)
	},
	"len": func(obj interface{}) int {
		switch o := obj.(type) {
		case []interface{}:
			return len(o)
		case []App:
			return len(o)
		case []Artifact:
			return len(o)
		case map[string]interface{}:
			return len(o)
		case string:
			return len(o)
		default:
			return 0
		}
	},
	"hasKey": func(obj interface{}, key string) bool {
		switch o := obj.(type) {
		case map[string]interface{}:
			_, exists := o[key]
			return exists
		default:
			return false
		}
	},
}

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Usage: %s <template-dir> <output-dir> <chart-name> <description> <domain> [app-metadata-files...] [--k8s-artifacts file1,file2...] [--chart-values key1=val1,key2=val2...] [--deploy-weight N]\n", os.Args[0])
		os.Exit(1)
	}

	templateDir := os.Args[1]
	outputDir := os.Args[2]
	chartName := os.Args[3]
	description := os.Args[4]
	domain := os.Args[5]

	// Parse remaining arguments
	args := parseArgs(os.Args[6:])

	// Load and process metadata
	data, err := collectChartData(chartName, description, domain, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error collecting chart data: %v\n", err)
		os.Exit(1)
	}

	// Create output directory
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output directory: %v\n", err)
		os.Exit(1)
	}

	// Render templates
	if err := renderTemplates(templateDir, outputDir, data); err != nil {
		fmt.Fprintf(os.Stderr, "Error rendering templates: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully rendered Helm chart: %s\n", data.ChartName)
	fmt.Printf("  Apps: %d\n", len(data.Apps))
	fmt.Printf("  Artifacts: %d\n", len(data.Artifacts))
}

// Arguments parsed from command line
type Args struct {
	AppMetadataFiles []string
	K8sArtifacts     []string
	ChartValues      map[string]string
	DeployWeight     int
}

// parseArgs parses command line arguments after the required positional args
func parseArgs(args []string) *Args {
	result := &Args{
		ChartValues:  make(map[string]string),
		DeployWeight: 0,
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--k8s-artifacts" && i+1 < len(args):
			// Parse comma-separated artifact files
			if args[i+1] != "" {
				result.K8sArtifacts = strings.Split(args[i+1], ",")
			}
			i++
		case arg == "--chart-values" && i+1 < len(args):
			// Parse key=value pairs
			for _, pair := range strings.Split(args[i+1], ",") {
				if kv := strings.SplitN(pair, "=", 2); len(kv) == 2 {
					result.ChartValues[kv[0]] = kv[1]
				}
			}
			i++
		case arg == "--deploy-weight" && i+1 < len(args):
			// Parse deploy weight
			if weight, err := strconv.Atoi(args[i+1]); err == nil {
				result.DeployWeight = weight
			}
			i++
		default:
			// Assume it's an app metadata file
			result.AppMetadataFiles = append(result.AppMetadataFiles, arg)
		}
	}

	return result
}

// collectChartData loads app and artifact metadata and creates ChartData
func collectChartData(chartName, description, domain string, args *Args) (*ChartData, error) {
	data := &ChartData{
		Release: Release{
			Name:      chartName,
			Namespace: "default",
			Service:   "Helm",
		},
		Chart: Chart{
			Name:        chartName,
			Version:     "1.0.0", // Default version
			Description: description,
		},
		Values: Values{
			"ingress": map[string]interface{}{
				"enabled": false,
				"host":    "localhost",
			},
			"service": map[string]interface{}{
				"type": "ClusterIP",
				"port": 80,
			},
			"domain": domain,
			"images": make(map[string]interface{}),
			"env": map[string]interface{}{
				"app_env": "dev",
			},
			domain: map[string]interface{}{
				"apps": make(map[string]interface{}),
			},
		},
		ChartName:         chartName,
		Description:       description,
		Domain:            domain,
		Version:           "1.0.0", // Default version
		DeployOrderWeight: args.DeployWeight,
		ChartValues:       args.ChartValues,
	}

	// Load app metadata files
	for _, metadataFile := range args.AppMetadataFiles {
		if _, err := os.Stat(metadataFile); os.IsNotExist(err) {
			continue // Skip non-existent files
		}

		file, err := os.Open(metadataFile)
		if err != nil {
			fmt.Printf("Warning: Could not open %s: %v\n", metadataFile, err)
			continue
		}

		var app App
		if err := json.NewDecoder(file).Decode(&app); err != nil {
			fmt.Printf("Warning: Could not parse %s: %v\n", metadataFile, err)
			file.Close()
			continue
		}
		file.Close()

		data.Apps = append(data.Apps, app)

		// Add to images map for template access
		images := data.Values["images"].(map[string]interface{})
		images[app.Name] = map[string]interface{}{
			"name":       fmt.Sprintf("%s/whale-net/%s", app.Registry, app.RepoName),
			"tag":        app.Version,
			"repository": fmt.Sprintf("%s/whale-net/%s", app.Registry, app.RepoName),
		}

		// Add to domain.apps for template access
		domainApps := data.Values[data.Domain].(map[string]interface{})["apps"].(map[string]interface{})
		domainApps[app.Name] = map[string]interface{}{
			"enabled":  true,
			"version":  app.Version,
			"replicas": 1,
			"port":     8000,
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{
					"memory": "128Mi",
					"cpu":    "100m",
				},
				"limits": map[string]interface{}{
					"memory": "512Mi",
					"cpu":    "500m",
				},
			},
		}

		fmt.Printf("Loaded app metadata: %s (Registry: %s, RepoName: %s, Version: %s)\n", app.Name, app.Registry, app.RepoName, app.Version)
	}

	// Load k8s artifact metadata files
	for _, artifactFile := range args.K8sArtifacts {
		if _, err := os.Stat(artifactFile); os.IsNotExist(err) {
			continue // Skip non-existent files
		}

		file, err := os.Open(artifactFile)
		if err != nil {
			fmt.Printf("Warning: Could not open %s: %v\n", artifactFile, err)
			continue
		}

		var artifact Artifact
		if err := json.NewDecoder(file).Decode(&artifact); err != nil {
			fmt.Printf("Warning: Could not parse %s: %v\n", artifactFile, err)
			file.Close()
			continue
		}
		file.Close()

		data.Artifacts = append(data.Artifacts, artifact)
		fmt.Printf("Loaded k8s artifact: %s\n", artifact.Name)
	}

	return data, nil
}

func renderTemplates(templateDir, outputDir string, data *ChartData) error {
	// Define template mappings
	templates := map[string]string{
		"Chart.yaml.tmpl":      "Chart.yaml",
		"values.yaml.tmpl":     "values.yaml",
		"deployment.yaml.tmpl": "templates/deployment.yaml",
		"service.yaml.tmpl":    "templates/service.yaml",
		"job.yaml.tmpl":        "templates/job.yaml",
		"configmap.yaml.tmpl":  "templates/configmap.yaml",
		"_helpers.tpl.tmpl":    "templates/_helpers.tpl",
		"NOTES.txt.tmpl":       "templates/NOTES.txt",
	}

	// Create templates directory
	templatesDir := filepath.Join(outputDir, "templates")
	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		return fmt.Errorf("creating templates directory: %w", err)
	}

	// Render each template that exists
	for templateFile, outputFile := range templates {
		templatePath := filepath.Join(templateDir, templateFile)

		// Skip if template doesn't exist
		if _, err := os.Stat(templatePath); os.IsNotExist(err) {
			continue
		}

		outputPath := filepath.Join(outputDir, outputFile)
		if err := renderTemplate(templatePath, outputPath, data); err != nil {
			return fmt.Errorf("rendering %s: %w", templateFile, err)
		}

		fmt.Printf("Rendered: %s -> %s\n", templateFile, outputFile)
	}

	return nil
}

func renderTemplate(templatePath, outputPath string, data *ChartData) error {
	// Parse template with functions
	tmpl, err := template.New(filepath.Base(templatePath)).Funcs(templateFuncs).ParseFiles(templatePath)
	if err != nil {
		return fmt.Errorf("parsing template: %w", err)
	}

	// Create output directory if needed
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	// Create output file
	output, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer output.Close()

	// Execute template
	if err := tmpl.Execute(output, data); err != nil {
		return fmt.Errorf("executing template: %w", err)
	}

	return nil
}
