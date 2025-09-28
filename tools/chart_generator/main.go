package main

// Simple Helm chart generator for helm_chart_native
// Generates charts using whale-net library chart dependencies

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

type AppMetadata struct {
	Name      string `json:"name"`
	ImageName string `json:"image_name"`
	Version   string `json:"version"`
	Domain    string `json:"domain"`
}

type JobConfig struct {
	Name             string            `json:"name"`
	Image            map[string]string `json:"image"`
	Command          []string          `json:"command,omitempty"`
	Args             []string          `json:"args,omitempty"`
	HookType         string            `json:"hookType,omitempty"`
	HookWeight       string            `json:"hookWeight,omitempty"`
	HookDeletePolicy string            `json:"hookDeletePolicy,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
}

type AppConfig struct {
	Enabled     bool              `json:"enabled"`
	Replicas    int               `json:"replicas"`
	Image       map[string]string `json:"image"`
	Service     map[string]any    `json:"service"`
	Healthcheck map[string]any    `json:"healthcheck"`
	Resources   map[string]any    `json:"resources"`
	Env         map[string]string `json:"env"`
	Autoscaling map[string]any    `json:"autoscaling"`
}

type ChartData struct {
	ChartName   string `json:"chartName"`
	Description string `json:"description"`
	Domain      string `json:"domain"`
	Version     string `json:"version"`
}

type ValuesData struct {
	Domain  string                `json:"domain"`
	Global  map[string]any        `json:"global"`
	Apps    map[string]*AppConfig `json:"apps"`
	Ingress map[string]any        `json:"ingress"`
	Service map[string]any        `json:"service"`
	Jobs    []*JobConfig          `json:"jobs"`
}

type Args struct {
	ChartName    string
	Description  string
	Domain       string
	Version      string
	OutputDir    string
	TemplateDir  string
	AppMetadata  []string
	CustomValues []string
	Jobs         []string
}

func parseArgs() *Args {
	args := &Args{}
	flag.StringVar(&args.ChartName, "chart-name", "", "Chart name")
	flag.StringVar(&args.Description, "description", "", "Chart description")
	flag.StringVar(&args.Domain, "domain", "", "Domain name")
	flag.StringVar(&args.Version, "version", "1.0.0", "Chart version")
	flag.StringVar(&args.OutputDir, "output-dir", "", "Output directory")
	flag.StringVar(&args.TemplateDir, "template-dir", "", "Template directory")

	var appMetadata, customValues, jobs string
	flag.StringVar(&appMetadata, "app-metadata", "", "Comma-separated app metadata files")
	flag.StringVar(&customValues, "custom-values", "", "Comma-separated custom values (key=value)")
	flag.StringVar(&jobs, "jobs", "", "Comma-separated job configurations")

	flag.Parse()

	if appMetadata != "" {
		args.AppMetadata = strings.Split(appMetadata, ",")
	}
	if customValues != "" {
		args.CustomValues = strings.Split(customValues, ",")
	}
	if jobs != "" {
		args.Jobs = strings.Split(jobs, ",")
	}

	return args
}

func loadAppMetadata(filePath string) (*AppMetadata, error) {
	data, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", filePath, err)
	}

	var metadata AppMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filePath, err)
	}

	return &metadata, nil
}

func parseJobConfig(jobStr string) (*JobConfig, error) {
	parts := strings.SplitN(jobStr, ":", 2)
	if len(parts) < 1 {
		return nil, fmt.Errorf("invalid job format: %s", jobStr)
	}

	job := &JobConfig{
		Name:  parts[0],
		Image: make(map[string]string),
		Env:   make(map[string]string),
	}

	if len(parts) > 1 {
		configPairs := strings.Split(parts[1], ",")
		for _, pair := range configPairs {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) == 2 {
				key, value := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])

				// Handle nested keys like image.repository
				if strings.HasPrefix(key, "image.") {
					imageKey := strings.TrimPrefix(key, "image.")
					job.Image[imageKey] = value
				} else {
					// Handle other job config fields
					switch key {
					case "hookType":
						job.HookType = value
					case "hookWeight":
						job.HookWeight = value
					case "hookDeletePolicy":
						job.HookDeletePolicy = value
					default:
						job.Env[key] = value
					}
				}
			}
		}
	}

	return job, nil
}

func parseCustomValues(values []string) map[string]any {
	result := make(map[string]any)
	for _, value := range values {
		kv := strings.SplitN(value, "=", 2)
		if len(kv) == 2 {
			result[kv[0]] = kv[1]
		}
	}
	return result
}

func setNestedValue(target map[string]any, key string, value any) {
	parts := strings.Split(key, ".")
	current := target

	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = value
		} else {
			if _, exists := current[part]; !exists {
				current[part] = make(map[string]any)
			}
			if nested, ok := current[part].(map[string]any); ok {
				current = nested
			}
		}
	}
}

func generateChartData(args *Args) *ChartData {
	return &ChartData{
		ChartName:   args.ChartName,
		Description: args.Description,
		Domain:      args.Domain,
		Version:     args.Version,
	}
}

func generateValuesData(args *Args) (*ValuesData, error) {
	values := &ValuesData{
		Domain: args.Domain,
		Global: map[string]any{
			"env":           "dev",
			"imageRegistry": "ghcr.io/whale-net",
		},
		Apps: make(map[string]*AppConfig),
		Ingress: map[string]any{
			"enabled":     false,
			"className":   "nginx",
			"annotations": make(map[string]any),
			"hosts":       []any{},
			"tls":         []any{},
		},
		Service: map[string]any{
			"type": "ClusterIP",
		},
		Jobs: []*JobConfig{},
	}

	// Process app metadata
	for _, metadataFile := range args.AppMetadata {
		metadata, err := loadAppMetadata(metadataFile)
		if err != nil {
			log.Printf("Warning: failed to load %s: %v", metadataFile, err)
			continue
		}

		appConfig := &AppConfig{
			Enabled:  true,
			Replicas: 1,
			Image: map[string]string{
				"repository": metadata.ImageName,
				"tag":        metadata.Version,
				"pullPolicy": "IfNotPresent",
			},
			Service: map[string]any{
				"enabled": true,
				"type":    "ClusterIP",
				"port":    8000,
			},
			Healthcheck: map[string]any{
				"enabled":             true,
				"path":                "/health",
				"initialDelaySeconds": 30,
				"periodSeconds":       10,
			},
			Resources: map[string]any{
				"requests": map[string]string{
					"memory": "128Mi",
					"cpu":    "100m",
				},
				"limits": map[string]string{
					"memory": "512Mi",
					"cpu":    "500m",
				},
			},
			Env: make(map[string]string),
			Autoscaling: map[string]any{
				"enabled": false,
			},
		}

		values.Apps[metadata.Name] = appConfig
	}

	// Parse job configurations
	for _, jobStr := range args.Jobs {
		job, err := parseJobConfig(jobStr)
		if err != nil {
			log.Printf("Warning: failed to parse job %s: %v", jobStr, err)
			continue
		}
		values.Jobs = append(values.Jobs, job)
	}

	// Apply custom values
	customValues := parseCustomValues(args.CustomValues)
	for key, value := range customValues {
		setNestedValue(map[string]any{
			"global":  values.Global,
			"ingress": values.Ingress,
			"service": values.Service,
		}, key, value)
	}

	return values, nil
}

func renderTemplate(templatePath, outputPath string, data interface{}) error {
	tmpl, err := template.ParseFiles(templatePath)
	if err != nil {
		return fmt.Errorf("parsing template %s: %w", templatePath, err)
	}

	output, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output file %s: %w", outputPath, err)
	}
	defer output.Close()

	if err := tmpl.Execute(output, data); err != nil {
		return fmt.Errorf("executing template %s: %w", templatePath, err)
	}

	return nil
}

func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source file %s: %w", src, err)
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating dest file %s: %w", dst, err)
	}
	defer destFile.Close()

	_, err = sourceFile.WriteTo(destFile)
	if err != nil {
		return fmt.Errorf("copying file content: %w", err)
	}

	return nil
}

func renderValuesYAML(outputPath string, data *ValuesData) error {
	output, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating values.yaml: %w", err)
	}
	defer output.Close()

	// Generate YAML manually for better control
	fmt.Fprintf(output, "# Generated values.yaml for %s domain\n", data.Domain)
	fmt.Fprintf(output, "# This file is automatically generated by helm_chart_native.bzl\n\n")

	fmt.Fprintf(output, "domain: %s\n\n", data.Domain)

	// Global section
	fmt.Fprintf(output, "global:\n")
	for key, value := range data.Global {
		fmt.Fprintf(output, "  %s: %v\n", key, value)
	}
	fmt.Fprintf(output, "\n")

	// Apps section
	fmt.Fprintf(output, "apps:\n")
	for appName, appConfig := range data.Apps {
		fmt.Fprintf(output, "  %s:\n", appName)
		fmt.Fprintf(output, "    enabled: %t\n", appConfig.Enabled)
		fmt.Fprintf(output, "    replicas: %d\n", appConfig.Replicas)

		fmt.Fprintf(output, "    image:\n")
		for key, value := range appConfig.Image {
			fmt.Fprintf(output, "      %s: %s\n", key, value)
		}

		fmt.Fprintf(output, "    service:\n")
		for key, value := range appConfig.Service {
			fmt.Fprintf(output, "      %s: %v\n", key, value)
		}

		fmt.Fprintf(output, "    healthcheck:\n")
		for key, value := range appConfig.Healthcheck {
			fmt.Fprintf(output, "      %s: %v\n", key, value)
		}

		// Resources section (nested)
		fmt.Fprintf(output, "    resources:\n")
		if resources, ok := appConfig.Resources["requests"].(map[string]string); ok {
			fmt.Fprintf(output, "      requests:\n")
			for key, value := range resources {
				fmt.Fprintf(output, "        %s: %s\n", key, value)
			}
		}
		if limits, ok := appConfig.Resources["limits"].(map[string]string); ok {
			fmt.Fprintf(output, "      limits:\n")
			for key, value := range limits {
				fmt.Fprintf(output, "        %s: %s\n", key, value)
			}
		}

		fmt.Fprintf(output, "    env: {}\n")
		fmt.Fprintf(output, "    autoscaling:\n")
		for key, value := range appConfig.Autoscaling {
			fmt.Fprintf(output, "      %s: %v\n", key, value)
		}
		fmt.Fprintf(output, "\n")
	}

	// Other sections
	fmt.Fprintf(output, "ingress:\n")
	for key, value := range data.Ingress {
		fmt.Fprintf(output, "  %s: %v\n", key, value)
	}
	fmt.Fprintf(output, "\n")

	fmt.Fprintf(output, "service:\n")
	for key, value := range data.Service {
		fmt.Fprintf(output, "  %s: %v\n", key, value)
	}
	fmt.Fprintf(output, "\n")

	if len(data.Jobs) > 0 {
		fmt.Fprintf(output, "jobs:\n")
		for _, job := range data.Jobs {
			fmt.Fprintf(output, "- name: %s\n", job.Name)
			if len(job.Image) > 0 {
				fmt.Fprintf(output, "  image:\n")
				for key, value := range job.Image {
					fmt.Fprintf(output, "    %s: %s\n", key, value)
				}
			}
			if job.HookType != "" {
				fmt.Fprintf(output, "  hookType: %s\n", job.HookType)
			}
			if job.HookWeight != "" {
				fmt.Fprintf(output, "  hookWeight: %s\n", job.HookWeight)
			}
			if job.HookDeletePolicy != "" {
				fmt.Fprintf(output, "  hookDeletePolicy: %s\n", job.HookDeletePolicy)
			}
		}
	}

	return nil
}

func main() {
	args := parseArgs()

	if args.ChartName == "" || args.Description == "" || args.Domain == "" || args.OutputDir == "" || args.TemplateDir == "" {
		flag.Usage()
		log.Fatal("Missing required arguments")
	}

	// Create output directory
	if err := os.MkdirAll(args.OutputDir, 0755); err != nil {
		log.Fatalf("Creating output directory: %v", err)
	}

	// Create templates subdirectory
	templatesDir := filepath.Join(args.OutputDir, "templates")
	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		log.Fatalf("Creating templates directory: %v", err)
	}

	// Generate chart data
	chartData := generateChartData(args)

	// Generate values data
	valuesData, err := generateValuesData(args)
	if err != nil {
		log.Fatalf("Generating values data: %v", err)
	}

	// Render Chart.yaml
	chartTemplate := filepath.Join(args.TemplateDir, "Chart.yaml.tmpl")
	chartOutput := filepath.Join(args.OutputDir, "Chart.yaml")
	if err := renderTemplate(chartTemplate, chartOutput, chartData); err != nil {
		log.Fatalf("Rendering Chart.yaml: %v", err)
	}

	// Render values.yaml
	valuesOutput := filepath.Join(args.OutputDir, "values.yaml")
	if err := renderValuesYAML(valuesOutput, valuesData); err != nil {
		log.Fatalf("Rendering values.yaml: %v", err)
	}

	// Copy static template files (these are Helm templates, not Go templates)
	templateFiles := []string{"apps.yaml.tmpl", "jobs.yaml.tmpl", "ingress.yaml.tmpl", "NOTES.txt.tmpl"}
	for _, tmplFile := range templateFiles {
		templatePath := filepath.Join(args.TemplateDir, tmplFile)
		outputName := strings.TrimSuffix(tmplFile, ".tmpl")
		outputPath := filepath.Join(templatesDir, outputName)

		if err := copyFile(templatePath, outputPath); err != nil {
			log.Fatalf("Copying %s: %v", tmplFile, err)
		}
	}

	fmt.Printf("Successfully generated Helm chart '%s' in %s\n", args.ChartName, args.OutputDir)
	fmt.Printf("Generated with %d apps and %d jobs\n", len(valuesData.Apps), len(valuesData.Jobs))
}
