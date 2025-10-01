// Package metadata provides app metadata utilities for the release helper.
package metadata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/whale-net/everything/tools/release/pkg/core"
)

// AppMetadata represents release metadata for an app.
type AppMetadata struct {
	Name         string            `json:"name"`
	AppType      string            `json:"app_type"`
	Version      string            `json:"version"`
	Description  string            `json:"description"`
	Registry     string            `json:"registry"`
	RepoName     string            `json:"repo_name"`
	ImageTarget  string            `json:"image_target"`
	Domain       string            `json:"domain"`
	Language     string            `json:"language"`
	Port         int               `json:"port,omitempty"`
	Replicas     int               `json:"replicas,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	Dependencies []string          `json:"dependencies,omitempty"`
}

// AppInfo represents basic app information.
type AppInfo struct {
	BazelTarget string `json:"bazel_target"`
	Name        string `json:"name"`
	Domain      string `json:"domain"`
}

// GetAppMetadata retrieves metadata for an app by building and reading its metadata target.
func GetAppMetadata(bazelTarget string) (*AppMetadata, error) {
	// Build the metadata target
	_, err := core.RunBazel([]string{"build", bazelTarget}, false, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build metadata target: %w", err)
	}

	// Extract path from target for finding the generated file
	if !strings.HasPrefix(bazelTarget, "//") {
		return nil, fmt.Errorf("invalid bazel target format: %s", bazelTarget)
	}

	targetParts := strings.Split(bazelTarget[2:], ":")
	if len(targetParts) != 2 {
		return nil, fmt.Errorf("invalid bazel target format: %s", bazelTarget)
	}

	packagePath := targetParts[0]
	targetName := targetParts[1]

	// Read the generated JSON file
	workspaceRoot, err := core.FindWorkspaceRoot()
	if err != nil {
		return nil, fmt.Errorf("failed to find workspace root: %w", err)
	}

	metadataFile := filepath.Join(workspaceRoot, "bazel-bin", packagePath, targetName+"_metadata.json")
	data, err := os.ReadFile(metadataFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata file %s: %w", metadataFile, err)
	}

	var metadata AppMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata JSON: %w", err)
	}

	return &metadata, nil
}

// ListAllApps lists all apps in the monorepo that have release metadata.
func ListAllApps() ([]AppInfo, error) {
	// Query for all metadata targets
	result, err := core.RunBazel([]string{"query", "kind(app_metadata, //...)", "--output=label"}, true, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query app metadata: %w", err)
	}

	var apps []AppInfo
	for _, line := range result.Lines() {
		if !strings.Contains(line, "_metadata") {
			continue
		}

		// Get metadata to extract app name and domain
		metadata, err := GetAppMetadata(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not get metadata for %s: %v\n", line, err)
			continue
		}

		apps = append(apps, AppInfo{
			BazelTarget: line,
			Name:        metadata.Name,
			Domain:      metadata.Domain,
		})
	}

	// Sort by name
	sort.Slice(apps, func(i, j int) bool {
		return apps[i].Name < apps[j].Name
	})

	return apps, nil
}

// ImageTargets represents all image-related targets for an app.
type ImageTargets struct {
	Base      string
	AMD64     string
	ARM64     string
	PushBase  string
	PushAMD64 string
	PushARM64 string
}

// GetImageTargets returns all image-related targets for an app.
func GetImageTargets(bazelTarget string) (*ImageTargets, error) {
	// Extract package path from metadata target
	if !strings.HasPrefix(bazelTarget, "//") {
		return nil, fmt.Errorf("invalid bazel target format: %s", bazelTarget)
	}

	targetParts := strings.Split(bazelTarget[2:], ":")
	if len(targetParts) != 2 {
		return nil, fmt.Errorf("invalid bazel target format: %s", bazelTarget)
	}
	packagePath := targetParts[0]

	// Get the metadata to find the actual image target name
	metadata, err := GetAppMetadata(bazelTarget)
	if err != nil {
		return nil, fmt.Errorf("failed to get metadata: %w", err)
	}

	imageTargetName := metadata.ImageTarget

	// Build full image target paths
	baseImageTarget := fmt.Sprintf("//%s:%s", packagePath, imageTargetName)

	return &ImageTargets{
		Base:      baseImageTarget,
		AMD64:     baseImageTarget + "_amd64",
		ARM64:     baseImageTarget + "_arm64",
		PushBase:  baseImageTarget + "_push",
		PushAMD64: baseImageTarget + "_push_amd64",
		PushARM64: baseImageTarget + "_push_arm64",
	}, nil
}
