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
//
// This single-target reader uses the on-disk JSON output for backward
// compatibility with callers that hold a specific target label. Discovery
// of all apps goes through ListAllApps which is significantly faster.
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

// appMetadataStarlarkExpr emits "<label>\t<json>" per matched target,
// pulling metadata from the AppMetadataInfo provider so no actions run.
const appMetadataStarlarkExpr = `str(target.label) + "\t" + json.encode(providers(target)["//tools/bazel:release.bzl%AppMetadataInfo"].metadata)`

// ListAllApps discovers every app_metadata target via a two-step Bazel call:
//
//  1. `bazel query` (loading only) lists the metadata target labels.
//  2. `bazel cquery` scoped to those labels reads the AppMetadataInfo
//     provider for each. Limiting cquery to the metadata closure avoids
//     analysing unrelated targets in `//...` whose failures would otherwise
//     break discovery.
//
// No metadata JSON files are produced — analysis alone yields the data.
func ListAllApps(bazel BazelRunner, _ FileSystem, _ string) ([]AppMetadata, error) {
	labelsOut, err := bazel.Run("query", "kind(app_metadata, //...)", "--output=label")
	if err != nil {
		return nil, fmt.Errorf("bazel query app_metadata: %w", err)
	}

	labels := splitNonEmpty(labelsOut)
	if len(labels) == 0 {
		return nil, nil
	}

	// `--keep_going` lets us survive transitive analysis errors elsewhere in
	// the workspace (a sibling rule with a stale macro, a missing repo, etc.).
	// Bazel still prints the labels it successfully analyzed; we only fail
	// hard if the output is empty.
	out, err := bazel.Run("cquery", strings.Join(labels, " + "), "--output=starlark",
		"--starlark:expr="+appMetadataStarlarkExpr, "--keep_going")
	if err != nil && out == "" {
		return nil, fmt.Errorf("bazel cquery app_metadata: %w", err)
	}

	var apps []AppMetadata
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		label, jsonPart, ok := strings.Cut(line, "\t")
		if !ok {
			fmt.Printf("Warning: malformed cquery line: %q\n", line)
			continue
		}
		var meta AppMetadata
		if err := json.Unmarshal([]byte(jsonPart), &meta); err != nil {
			fmt.Printf("Warning: parse metadata for %s: %v\n", label, err)
			continue
		}
		meta.BazelTarget = canonicalLabel(label)
		meta.BinaryTarget = canonicalLabel(meta.BinaryTarget)
		meta.ImageTarget = canonicalLabel(meta.ImageTarget)
		meta.OpenAPISpecTarget = canonicalLabel(meta.OpenAPISpecTarget)
		apps = append(apps, meta)
	}

	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}

func splitNonEmpty(out string) []string {
	var result []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

// canonicalLabel strips Bazel's canonical-repo "@@" prefix so labels look
// like "//pkg:name", which is the form rdeps queries and downstream tools
// expect.
func canonicalLabel(s string) string {
	return strings.TrimPrefix(s, "@@")
}
