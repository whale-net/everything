// Data loading and chart metadata collection
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// CollectChartData loads app and artifact metadata and creates ChartData
func CollectChartData(args *Args) (*ChartData, error) {
	// Use version from args or default
	version := args.Version
	if version == "" {
		version = "1.0.0"
	}

	data := &ChartData{
		Release: Release{
			Name:      args.ChartName,
			Namespace: "default",
			Service:   "Helm",
		},
		Chart: Chart{
			Name:        args.ChartName,
			Version:     version,
			Description: args.Description,
		},
		Values:            createDefaultValues(args.Domain),
		ChartName:         args.ChartName,
		Description:       args.Description,
		Domain:            args.Domain,
		Version:           version,
		DeployOrderWeight: args.DeployOrder,
		ChartValues:       args.ChartValues,
	}

	// Load app metadata files
	if err := loadAppMetadata(data, args.AppMetadataFiles); err != nil {
		return nil, fmt.Errorf("loading app metadata: %w", err)
	}

	// Load k8s artifact metadata files
	if err := loadArtifactMetadata(data, args.K8sArtifacts); err != nil {
		return nil, fmt.Errorf("loading artifact metadata: %w", err)
	}

	return data, nil
}

// createDefaultValues creates the default Values map structure
func createDefaultValues(domain string) Values {
	return Values{
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
	}
}

// loadAppMetadata loads app metadata files and populates the ChartData
func loadAppMetadata(data *ChartData, metadataFiles []string) error {
	for _, metadataFile := range metadataFiles {
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
		domainApps[app.Name] = createAppConfig(app)

		fmt.Printf("Loaded app metadata: %s (Registry: %s, RepoName: %s, Version: %s)\n",
			app.Name, app.Registry, app.RepoName, app.Version)
	}

	return nil
}

// createAppConfig creates the default app configuration for Values
func createAppConfig(app App) map[string]interface{} {
	return map[string]interface{}{
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
}

// loadArtifactMetadata loads k8s artifact metadata files
func loadArtifactMetadata(data *ChartData, artifactFiles []string) error {
	for _, artifactFile := range artifactFiles {
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

	return nil
}
