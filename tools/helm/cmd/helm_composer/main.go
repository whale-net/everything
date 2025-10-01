package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/whale-net/everything/tools/helm"
)

func main() {
	// Define CLI flags
	var (
		metadataFiles string
		manifestFiles string
		chartName     string
		version       string
		environment   string
		namespace     string
		outputDir     string
		templateDir   string
	)

	flag.StringVar(&metadataFiles, "metadata", "", "Comma-separated list of metadata JSON files")
	flag.StringVar(&manifestFiles, "manifests", "", "Comma-separated list of manual Kubernetes manifest YAML files")
	flag.StringVar(&chartName, "chart-name", "composed-chart", "Name of the Helm chart")
	flag.StringVar(&version, "version", "1.0.0", "Chart version")
	flag.StringVar(&environment, "environment", "production", "Environment name")
	flag.StringVar(&namespace, "namespace", "default", "Kubernetes namespace")
	flag.StringVar(&outputDir, "output", ".", "Output directory for generated chart")
	flag.StringVar(&templateDir, "template-dir", "", "Directory containing template files")

	flag.Parse()

	// Validate required flags
	if metadataFiles == "" {
		fmt.Fprintf(os.Stderr, "Error: --metadata flag is required\n")
		flag.Usage()
		os.Exit(1)
	}

	if templateDir == "" {
		// Default to templates directory relative to binary
		execPath, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to determine executable path: %v\n", err)
			os.Exit(1)
		}
		templateDir = filepath.Join(filepath.Dir(execPath), "templates")
	}

	// Parse metadata file list
	metadataList := strings.Split(metadataFiles, ",")
	for i := range metadataList {
		metadataList[i] = strings.TrimSpace(metadataList[i])
	}

	// Parse manifest file list
	var manifestList []string
	if manifestFiles != "" {
		manifestList = strings.Split(manifestFiles, ",")
		for i := range manifestList {
			manifestList[i] = strings.TrimSpace(manifestList[i])
		}
	}

	// Create composer configuration
	config := helm.ChartConfig{
		ChartName:   chartName,
		Version:     version,
		Environment: environment,
		Namespace:   namespace,
		OutputDir:   outputDir,
	}

	// Create composer instance
	composer := helm.NewComposer(config, templateDir)

	// Load metadata files
	if err := composer.LoadMetadata(metadataList); err != nil {
		fmt.Fprintf(os.Stderr, "Error loading metadata: %v\n", err)
		os.Exit(1)
	}

	// Load manifest files if provided
	if len(manifestList) > 0 {
		if err := composer.LoadManifests(manifestList); err != nil {
			fmt.Fprintf(os.Stderr, "Error loading manifests: %v\n", err)
			os.Exit(1)
		}
	}

	// Generate chart
	if err := composer.GenerateChart(); err != nil {
		fmt.Fprintf(os.Stderr, "Error generating chart: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully generated Helm chart: %s\n", filepath.Join(outputDir, chartName))
	fmt.Printf("  Chart: %s (version %s)\n", chartName, version)
	fmt.Printf("  Environment: %s\n", environment)
	fmt.Printf("  Namespace: %s\n", namespace)
	fmt.Printf("  Apps: %d\n", len(metadataList))
}
