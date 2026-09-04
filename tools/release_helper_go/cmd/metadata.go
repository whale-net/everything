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

// appMetadataQuery discovers every app_metadata target that is eligible for
// release. `except attr(testonly, 1, //...)` excludes fixtures like
// //tools/appmeta/testdata:fixture-app_metadata — testonly is how a fixture
// opts out of release discovery, so this covers any future fixture too,
// without hardcoding a domain name next to the "demo" exclusion below.
const appMetadataQuery = "kind(app_metadata, //...) except attr(testonly, 1, //...)"

// bazelLabelJSON is one "<label>\t<json>" line from a metadata cquery,
// split into its label and JSON parts.
type bazelLabelJSON struct {
	Label string
	JSON  string
}

// discoverBazelMetadata runs the two-step Bazel metadata discovery shared by
// ListAllApps and ListAllHelmCharts:
//
//  1. `bazel query` (loading only) lists target labels matching query.
//  2. `bazel cquery` scoped to exactly those labels evaluates starlarkExpr
//     against each target (reading a provider directly, so no build
//     actions run). Scoping to the discovered labels avoids analysing
//     unrelated targets in `//...` whose failures would otherwise break
//     discovery.
//
// name identifies the metadata kind in error messages (e.g. "app_metadata").
// Any cquery error is returned rather than swallowed: real metadata is
// missing at that point, and callers plan releases off this list, so
// failing hard beats silently returning a partial one.
func discoverBazelMetadata(bazel BazelRunner, name, query, starlarkExpr string) ([]bazelLabelJSON, error) {
	labelsOut, err := bazel.Run("query", query, "--universe_scope=//...", "--noimplicit_deps", "--nodep_deps", "--output=label")
	if err != nil {
		return nil, fmt.Errorf("bazel query %s: %w", name, err)
	}

	labels := splitNonEmpty(labelsOut)
	if len(labels) == 0 {
		return nil, nil
	}

	out, err := bazel.Run("cquery", strings.Join(labels, " + "), "--output=starlark",
		"--starlark:expr="+starlarkExpr)
	if err != nil {
		return nil, fmt.Errorf("bazel cquery %s: %w", name, err)
	}

	var pairs []bazelLabelJSON
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		label, jsonPart, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("malformed cquery line: %q", line)
		}
		pairs = append(pairs, bazelLabelJSON{Label: label, JSON: jsonPart})
	}
	return pairs, nil
}

// ListAllApps discovers every releasable app_metadata target via
// discoverBazelMetadata, then parses each result's JSON into an
// AppMetadata.
//
// When --fast is set (fastDiscovery, root.go), this skips bazel entirely
// and statically parses BUILD.bazel files instead -- see discover_fast.go.
// workspaceRoot is unused on the bazel path (bazel resolves paths itself
// relative to its own working directory) but is required by the fast path,
// so it's threaded through unconditionally rather than only when needed.
func ListAllApps(bazel BazelRunner, _ FileSystem, workspaceRoot string) ([]AppMetadata, error) {
	if fastDiscovery {
		return ListAllAppsFast(workspaceRoot)
	}
	pairs, err := discoverBazelMetadata(bazel, "app_metadata", appMetadataQuery, appMetadataStarlarkExpr)
	if err != nil {
		return nil, err
	}

	var apps []AppMetadata
	for _, p := range pairs {
		manifest := &appmetapb.AppManifest{}
		if err := protojson.Unmarshal([]byte(p.JSON), manifest); err != nil {
			return nil, fmt.Errorf("parse metadata for %s: %w", p.Label, err)
		}
		meta := AppMetadata{AppManifest: manifest, BazelTarget: canonicalLabel(p.Label)}
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

// AppMetadataInput is one app's identity as supplied directly by a caller
// that has already resolved its target list against a source of truth
// other than `bazel query` (e.g. App Registry's own App rows) -- see
// AppMetadataFromInputs.
type AppMetadataInput struct {
	Domain  string `json:"domain"`
	Name    string `json:"name"`
	AppType string `json:"app_type"`
}

// AppMetadataFromInputs builds []AppMetadata directly from inputs, with no
// bazel query/cquery call -- the bazel-free counterpart to ListAllApps for a
// caller (tools/app_registry/worker/release/plan.go's ResolvePlan) that
// already has an explicit, pre-validated target list and only needs
// Domain/Name/AppType (FullName() and determineArtifactKind's only inputs
// for an explicit --apps list -- see this file's ListAllApps/plan.go's
// assignVersions). BazelTarget is left empty: it is only consumed by
// buildPlanResult's GHA matrix `bazel_target` field and the OpenAPI-spec
// plumbing, neither of which ResolvePlan's shell-out reads back (it only
// parses the JSON output's `versions` map).
func AppMetadataFromInputs(inputs []AppMetadataInput) []AppMetadata {
	out := make([]AppMetadata, 0, len(inputs))
	for _, in := range inputs {
		out = append(out, AppMetadata{AppManifest: &appmetapb.AppManifest{
			Domain:  in.Domain,
			Name:    in.Name,
			AppType: in.AppType,
		}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FullName() < out[j].FullName() })
	return out
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
