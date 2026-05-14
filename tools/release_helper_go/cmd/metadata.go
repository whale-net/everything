package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// AppMetadata mirrors the JSON produced by the app_metadata Starlark rule.
type AppMetadata struct {
	Name              string `json:"name"`
	Domain            string `json:"domain"`
	Language          string `json:"language"`
	Registry          string `json:"registry"`
	Organization      string `json:"organization"`
	RepoName          string `json:"repo_name"`
	ImageTarget       string `json:"image_target"`
	BinaryTarget      string `json:"binary_target"`
	OpenAPISpecTarget string `json:"openapi_spec_target,omitempty"`
	// BazelTarget is the metadata target label — set by ListAllApps, not in JSON.
	BazelTarget string `json:"-"`
}

// FullName returns the canonical "domain-name" identifier.
func (m AppMetadata) FullName() string { return m.Domain + "-" + m.Name }

// metadataFilePath derives the bazel-bin output path for a metadata target.
// Target format: //demo/hello_go:hello-go_metadata
// Output file:   {workspaceRoot}/bazel-bin/demo/hello_go/hello-go_metadata_metadata.json
func metadataFilePath(workspaceRoot, targetLabel string) (string, error) {
	// Strip leading "//"
	if !strings.HasPrefix(targetLabel, "//") {
		return "", fmt.Errorf("invalid bazel target: %q", targetLabel)
	}
	rest := targetLabel[2:]
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid bazel target (no colon): %q", targetLabel)
	}
	packagePath := parts[0]
	targetName := parts[1]
	fileName := targetName + "_metadata.json"
	return filepath.Join(workspaceRoot, "bazel-bin", packagePath, fileName), nil
}

// GetAppMetadata builds a metadata target and reads its output JSON.
func GetAppMetadata(targetLabel string, bazel BazelRunner, fs FileSystem, workspaceRoot string) (AppMetadata, error) {
	if _, err := bazel.Run("build", targetLabel); err != nil {
		return AppMetadata{}, fmt.Errorf("bazel build %s: %w", targetLabel, err)
	}
	filePath, err := metadataFilePath(workspaceRoot, targetLabel)
	if err != nil {
		return AppMetadata{}, err
	}
	data, err := fs.ReadFile(filePath)
	if err != nil {
		return AppMetadata{}, fmt.Errorf("read metadata file %s: %w", filePath, err)
	}
	var meta AppMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return AppMetadata{}, fmt.Errorf("parse metadata JSON: %w", err)
	}
	meta.BazelTarget = targetLabel
	return meta, nil
}

// ListAllApps queries Bazel for all app_metadata targets and loads each one.
func ListAllApps(bazel BazelRunner, fs FileSystem, workspaceRoot string) ([]AppMetadata, error) {
	out, err := bazel.Run("query", "kind(app_metadata, //...)", "--output=label")
	if err != nil {
		return nil, fmt.Errorf("bazel query app_metadata: %w", err)
	}

	var apps []AppMetadata
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "_metadata") {
			continue
		}
		meta, err := GetAppMetadata(line, bazel, fs, workspaceRoot)
		if err != nil {
			// Mirror Python: warn and continue
			fmt.Printf("Warning: could not load metadata for %s: %v\n", line, err)
			continue
		}
		apps = append(apps, meta)
	}

	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}
