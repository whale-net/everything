package cmd

import (
	"encoding/json"
	"fmt"
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

	// cquery is scoped to exactly the discovered labels, so any error here
	// means real metadata is missing — fail hard rather than silently
	// returning a partial app list to callers that plan releases off it.
	out, err := bazel.Run("cquery", strings.Join(labels, " + "), "--output=starlark",
		"--starlark:expr="+appMetadataStarlarkExpr)
	if err != nil {
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
			return nil, fmt.Errorf("malformed cquery line: %q", line)
		}
		var meta AppMetadata
		if err := json.Unmarshal([]byte(jsonPart), &meta); err != nil {
			return nil, fmt.Errorf("parse metadata for %s: %w", label, err)
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
