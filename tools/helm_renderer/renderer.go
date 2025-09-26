// Template rendering functionality
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

// RenderTemplates renders all available templates to the output directory
func RenderTemplates(templateDir, outputDir string, data *ChartData) error {
	// Define template mappings
	templates := map[string]string{
		"Chart.yaml.tmpl":      "Chart.yaml",
		"values.yaml.tmpl":     "values.yaml",
		"deployment.yaml.tmpl": "templates/deployment.yaml",
		"service.yaml.tmpl":    "templates/service.yaml",
		"job.yaml.tmpl":        "templates/job.yaml",
		"pdb.yaml.tmpl":        "templates/pdb.yaml",
		"ingress.yaml.tmpl":    "templates/ingress.yaml",
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

// renderTemplate renders a single template file to the specified output path
func renderTemplate(templatePath, outputPath string, data *ChartData) error {
	// Parse template with functions
	tmpl, err := template.New(filepath.Base(templatePath)).Funcs(GetTemplateFuncs()).ParseFiles(templatePath)
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
