package main
// Helm template renderer for Bazel-generated charts
// Uses Go's text/template package (same engine as Helm)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// ChartData represents the data structure for chart generation
type ChartData struct {
	ChartName         string            `json:"chart_name"`
	Description       string            `json:"description"`
	Domain            string            `json:"domain"`
	DeployOrderWeight int               `json:"deploy_order_weight"`
	Apps              []App             `json:"apps"`
	Artifacts         []Artifact        `json:"artifacts"`
	ChartValues       map[string]string `json:"chart_values"`
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
	Name              string `json:"name"`
	Type              string `json:"type"`
	ManifestPath      string `json:"manifest_path"`
	HookWeight        int    `json:"hook_weight"`
	HookDeletePolicy  string `json:"hook_delete_policy"`
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
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "Usage: %s <template-dir> <output-dir> <data-file>\n", os.Args[0])
		os.Exit(1)
	}

	templateDir := os.Args[1]
	outputDir := os.Args[2]
	dataFile := os.Args[3]

	// Load chart data
	data, err := loadChartData(dataFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading data: %v\n", err)
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

func loadChartData(dataFile string) (*ChartData, error) {
	file, err := os.Open(dataFile)
	if err != nil {
		return nil, fmt.Errorf("opening data file: %w", err)
	}
	defer file.Close()

	var data ChartData
	if err := json.NewDecoder(file).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding JSON: %w", err)
	}

	return &data, nil
}

func renderTemplates(templateDir, outputDir string, data *ChartData) error {
	// Define template mappings
	templates := map[string]string{
		"Chart.yaml.tmpl":         "Chart.yaml",
		"values.yaml.tmpl":        "values.yaml",
		"deployment.yaml.tmpl":    "templates/deployment.yaml",
		"service.yaml.tmpl":       "templates/service.yaml",
		"job.yaml.tmpl":           "templates/job.yaml",
		"configmap.yaml.tmpl":     "templates/configmap.yaml",
		"_helpers.tpl.tmpl":       "templates/_helpers.tpl",
		"NOTES.txt.tmpl":          "templates/NOTES.txt",
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