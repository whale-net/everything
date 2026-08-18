package cmd

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// AppMetadata is one release_app manifest, as discovered via bazel cquery.
// appmetapb.AppManifest — see //tools/appmeta/README.md — is the schema of
// record for the JSON the app_metadata rule emits; AppMetadata wraps it with
// the discovery-time Bazel target label, which is not itself part of the
// manifest.
type AppMetadata struct {
	*appmetapb.AppManifest
	// BazelTarget is the metadata target label — set by ListAllApps, not in
	// the manifest JSON.
	BazelTarget string `json:"bazel_target,omitempty"`
}

// FullName returns the canonical "domain-name" identifier.
func (m AppMetadata) FullName() string { return m.Domain + "-" + m.Name }

// determineArtifactKind returns the ArtifactKind protobuf enum for an application.
func determineArtifactKind(meta AppMetadata) pb.ArtifactKind {
	switch meta.AppType {
	case "cli", "binary":
		return pb.ArtifactKind_ARTIFACT_KIND_BINARY
	case "firmware":
		return pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE
	default:
		return pb.ArtifactKind_ARTIFACT_KIND_IMAGE
	}
}

// appMetadataStarlarkExpr emits "<label>\t<json>" per matched target,
// pulling metadata from the AppMetadataInfo provider so no actions run.
const appMetadataStarlarkExpr = `str(target.label) + "\t" + json.encode(providers(target)["//tools/bazel:release.bzl%AppMetadataInfo"].metadata)`

// discoveryUniversePackages lists the top-level package patterns in the monorepo
// containing releasable applications, libraries, firmware, and tooling.
// Scoping --universe_scope and queries to these specific packages prevents Bazel
// from evaluating //generated/... which would otherwise trigger eager external
// repository downloads (e.g. @openapi_generator_cli jar from Maven Central).
var discoveryUniversePackages = []string{
	"//demo/...",
	"//firmware/...",
	"//friendly_computing_machine/...",
	"//leaflab/...",
	"//libs/...",
	"//manman/...",
	"//manmanv2/...",
	"//tools/...",
}

var (
	discoveryPackagesPattern = strings.Join(discoveryUniversePackages, " + ")
	discoveryUniverseScope   = "--universe_scope=" + strings.Join(discoveryUniversePackages, ",")
	// appMetadataQuery discovers every app_metadata target that is eligible for
	// release. `except attr(testonly, 1, ...)` excludes fixtures like
	// //tools/appmeta/testdata:fixture-app_metadata — testonly is how a fixture
	// opts out of release discovery, so this covers any future fixture too.
	appMetadataQuery = fmt.Sprintf("kind(app_metadata, %s) except attr(testonly, 1, %s)", discoveryPackagesPattern, discoveryPackagesPattern)
)

// ListAllApps discovers every releasable app_metadata target via a two-step
// Bazel call:
//
//  1. `bazel query` (loading only) lists the metadata target labels.
//  2. `bazel cquery` scoped to those labels reads the AppMetadataInfo
//     provider for each. Limiting cquery to the metadata closure avoids
//     analysing unrelated targets in `//...` whose failures would otherwise
//     break discovery.
//
// No metadata JSON files are produced — analysis alone yields the data.
func ListAllApps(bazel BazelRunner, _ FileSystem, _ string) ([]AppMetadata, error) {
	labelsOut, err := bazel.Run("query", appMetadataQuery, discoveryUniverseScope, "--noimplicit_deps", "--nodep_deps", "--output=label")
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
		manifest := &appmetapb.AppManifest{}
		if err := protojson.Unmarshal([]byte(jsonPart), manifest); err != nil {
			return nil, fmt.Errorf("parse metadata for %s: %w", label, err)
		}
		meta := AppMetadata{AppManifest: manifest, BazelTarget: canonicalLabel(label)}
		meta.BinaryTarget = canonicalLabel(meta.BinaryTarget)
		meta.ImageTarget = canonicalLabel(meta.ImageTarget)
		meta.OpenapiSpecTarget = canonicalLabel(meta.OpenapiSpecTarget)
		apps = append(apps, meta)
	}

	// Sort by full name (domain-name), not just name: two apps in different
	// domains can share a bare name (e.g. "migration"), and sorting on name
	// alone leaves their relative order dependent on cquery's output order
	// rather than deterministic across inputs.
	sort.Slice(apps, func(i, j int) bool { return apps[i].FullName() < apps[j].FullName() })
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
