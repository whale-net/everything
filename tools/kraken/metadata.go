package kraken

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AppMetadata represents the release metadata for an app.
type AppMetadata struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	BinaryTarget      string `json:"binary_target"`
	ImageTarget       string `json:"image_target"`
	Description       string `json:"description"`
	Language          string `json:"language"`
	Registry          string `json:"registry"`
	RepoName          string `json:"repo_name"`
	Domain            string `json:"domain"`
	OpenAPISpecTarget string `json:"openapi_spec_target,omitempty"`
}

// AppInfo represents a discovered app with its bazel target.
type AppInfo struct {
	Name        string `json:"name"`
	Domain      string `json:"domain"`
	BazelTarget string `json:"bazel_target"`
	Language    string `json:"language,omitempty"`
	Version     string `json:"version,omitempty"`
}

// GetAppMetadata retrieves metadata for an app by building and reading its metadata target.
func GetAppMetadata(bazelTarget string) (*AppMetadata, error) {
	_, err := RunBazel([]string{"build", bazelTarget}, true, nil)
	if err != nil {
		return nil, err
	}

	if !strings.HasPrefix(bazelTarget, "//") {
		return nil, fmt.Errorf("invalid bazel target format: %s", bazelTarget)
	}

	targetParts := strings.SplitN(bazelTarget[2:], ":", 2)
	if len(targetParts) != 2 {
		return nil, fmt.Errorf("invalid bazel target format: %s", bazelTarget)
	}

	packagePath := targetParts[0]
	targetName := targetParts[1]

	workspaceRoot, err := FindWorkspaceRoot()
	if err != nil {
		return nil, err
	}

	metadataFile := filepath.Join(workspaceRoot, "bazel-bin", packagePath, targetName+"_metadata.json")
	if _, err := os.Stat(metadataFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("metadata file not found: %s", metadataFile)
	}

	data, err := os.ReadFile(metadataFile)
	if err != nil {
		return nil, fmt.Errorf("reading metadata file: %w", err)
	}

	var metadata AppMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("parsing metadata JSON: %w", err)
	}

	return &metadata, nil
}

// ListAllApps lists all apps in the monorepo that have release metadata.
func ListAllApps() ([]AppInfo, error) {
	result, err := RunBazel([]string{"query", "kind(app_metadata, //...)", "--output=label"}, true, nil)
	if err != nil {
		return nil, err
	}

	var apps []AppInfo
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "_metadata") {
			continue
		}

		metadata, err := GetAppMetadata(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not get metadata for %s: %v\n", line, err)
			continue
		}

		app := AppInfo{
			Name:        metadata.Name,
			Domain:      metadata.Domain,
			BazelTarget: line,
			Language:    metadata.Language,
		}
		apps = append(apps, app)
	}

	sort.Slice(apps, func(i, j int) bool {
		return apps[i].Name < apps[j].Name
	})

	return apps, nil
}

// ImageTargets holds the image-related bazel targets for an app.
type ImageTargets struct {
	Base string
	Push string
}

// GetImageTargets gets all image-related targets for an app.
func GetImageTargets(bazelTarget string) (*ImageTargets, error) {
	targetParts := strings.SplitN(bazelTarget[2:], ":", 2)
	packagePath := targetParts[0]

	metadata, err := GetAppMetadata(bazelTarget)
	if err != nil {
		return nil, err
	}

	baseTarget := fmt.Sprintf("//%s:%s", packagePath, metadata.ImageTarget)

	return &ImageTargets{
		Base: baseTarget,
		Push: baseTarget + "_push",
	}, nil
}
