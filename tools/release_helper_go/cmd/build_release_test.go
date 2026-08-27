package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseReleaseMatrixItems(t *testing.T) {
	matrix := map[string]interface{}{
		"include": []map[string]string{
			{"app": "hello-go", "domain": "demo", "version": "v1.0.0"},
		},
	}
	items, err := parseReleaseMatrixItems(matrix)
	if err != nil {
		t.Fatalf("parseReleaseMatrixItems failed: %v", err)
	}
	if len(items) != 1 || items[0].App != "hello-go" || items[0].Domain != "demo" {
		t.Fatalf("unexpected items: %+v", items)
	}

	if items, err := parseReleaseMatrixItems(nil); err != nil || items != nil {
		t.Fatalf("expected nil, nil for nil matrix, got %+v, %v", items, err)
	}
}

func TestParseOpenAPIMatrixItems(t *testing.T) {
	matrix := map[string]interface{}{
		"include": []map[string]string{
			{"app": "control-api", "domain": "manmanv2", "openapi_target": "//manmanv2/control_api:openapi_spec"},
		},
	}
	entries, err := parseOpenAPIMatrixItems(matrix)
	if err != nil {
		t.Fatalf("parseOpenAPIMatrixItems failed: %v", err)
	}
	if len(entries) != 1 || entries[0].OpenAPITarget != "//manmanv2/control_api:openapi_spec" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

func TestParseDescriptorSetMatrixItems(t *testing.T) {
	matrix := map[string]interface{}{
		"include": []map[string]string{
			{"app": "leaflab-api", "domain": "leaflab", "descriptor_set_target": "//leaflab/api:leaflab_api_descriptor_set"},
		},
	}
	entries, err := parseDescriptorSetMatrixItems(matrix)
	if err != nil {
		t.Fatalf("parseDescriptorSetMatrixItems failed: %v", err)
	}
	if len(entries) != 1 || entries[0].DescriptorSetTarget != "//leaflab/api:leaflab_api_descriptor_set" {
		t.Fatalf("unexpected entries: %+v", entries)
	}

	if entries, err := parseDescriptorSetMatrixItems(nil); err != nil || entries != nil {
		t.Fatalf("expected nil, nil for nil matrix, got %+v, %v", entries, err)
	}
}

// TestBuildDescriptorSets_ResolvesOutputViaCquery exercises BuildDescriptorSets'
// cquery-based file resolution (build_release.go's descriptorSetOutputStarlarkExpr) --
// unlike BuildOpenAPISpecs, it can't guess the output path from the target's
// own package directory, since a descriptor_set_target can be a filegroup
// re-exporting a proto_descriptor_set target declared in a different
// package (leaflab-api's real shape: //leaflab/api:leaflab_api_descriptor_set
// wraps //leaflab/api/proto:leaflabapi_descriptor_set).
func TestBuildDescriptorSets_ResolvesOutputViaCquery(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "descriptor-set-test")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	execRoot := filepath.Join(tmpDir, "execroot")
	// Mirrors a real build: the file lives under a *different* package
	// directory (proto/) than the release_app() call's own package (api/).
	realFileDir := filepath.Join(execRoot, "bazel-out", "k8-fastbuild", "bin", "leaflab", "api", "proto")
	if err := os.MkdirAll(realFileDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	descriptorContent := []byte("fake-file-descriptor-set-bytes")
	if err := os.WriteFile(filepath.Join(realFileDir, "leaflabapi_descriptor_set.pb"), descriptorContent, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fakeBazel := newFakeBazel(
		fakeBazelCall{argsContain: []string{"info", "execution_root", "--config=ci-images"}, output: execRoot},
		fakeBazelCall{argsContain: []string{"build", "--config=ci-images", "//leaflab/api:leaflab_api_descriptor_set"}, output: ""},
		fakeBazelCall{
			argsContain: []string{"cquery", "//leaflab/api:leaflab_api_descriptor_set", "--output=starlark"},
			output:      "bazel-out/k8-fastbuild/bin/leaflab/api/proto/leaflabapi_descriptor_set.pb",
		},
	)

	outDir := filepath.Join(tmpDir, "out")
	results, err := BuildDescriptorSets(fakeBazel, []descriptorSetEntry{
		{App: "leaflab-api", Domain: "leaflab", DescriptorSetTarget: "//leaflab/api:leaflab_api_descriptor_set"},
	}, outDir)
	if err != nil {
		t.Fatalf("BuildDescriptorSets failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	got, err := os.ReadFile(filepath.Join(outDir, "leaflab-leaflab-api_descriptor_set.pb"))
	if err != nil {
		t.Fatalf("expected descriptor set copied to output dir: %v", err)
	}
	if string(got) != string(descriptorContent) {
		t.Fatalf("descriptor set content mismatch: got %q want %q", got, descriptorContent)
	}
}

func TestBuildOpenAPISpecs_PrimaryPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "openapi-primary-test")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	bazelBin := filepath.Join(tmpDir, "bazel-bin")
	// Primary form: <bazel-bin>/<package dir>/<domain>-<app>_openapi_spec.json
	specDir := filepath.Join(bazelBin, "manmanv2", "control_api")
	if err := os.MkdirAll(specDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	specContent := []byte(`{"openapi":"3.0.0"}`)
	if err := os.WriteFile(filepath.Join(specDir, "manmanv2-control-api_openapi_spec.json"), specContent, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fakeBazel := newFakeBazel(
		fakeBazelCall{argsContain: []string{"info", "bazel-bin", "--config=ci-images"}, output: bazelBin},
		fakeBazelCall{argsContain: []string{"build", "--config=ci-images", "//manmanv2/control_api:openapi_spec"}, output: ""},
	)

	outDir := filepath.Join(tmpDir, "out")
	results, err := BuildOpenAPISpecs(fakeBazel, []openAPISpecEntry{
		{App: "control-api", Domain: "manmanv2", OpenAPITarget: "//manmanv2/control_api:openapi_spec"},
	}, outDir)
	if err != nil {
		t.Fatalf("BuildOpenAPISpecs failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	got, err := os.ReadFile(filepath.Join(outDir, "manmanv2-control-api_openapi_spec.json"))
	if err != nil {
		t.Fatalf("expected spec copied to output dir: %v", err)
	}
	if string(got) != string(specContent) {
		t.Fatalf("spec content mismatch: got %q want %q", got, specContent)
	}
}

func TestBuildOpenAPISpecs_FallbackPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "openapi-fallback-test")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	bazelBin := filepath.Join(tmpDir, "bazel-bin")
	// No file at the primary path -- only at the fallback form:
	// <bazel-bin>/<target path with ':' -> '/'>.json
	fallbackDir := filepath.Join(bazelBin, "manmanv2", "control_api")
	if err := os.MkdirAll(fallbackDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	specContent := []byte(`{"openapi":"3.1.0"}`)
	if err := os.WriteFile(filepath.Join(fallbackDir, "openapi_spec.json"), specContent, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fakeBazel := newFakeBazel(
		fakeBazelCall{argsContain: []string{"info", "bazel-bin", "--config=ci-images"}, output: bazelBin},
		fakeBazelCall{argsContain: []string{"build", "--config=ci-images", "@@//manmanv2/control_api:openapi_spec"}, output: ""},
	)

	outDir := filepath.Join(tmpDir, "out")
	results, err := BuildOpenAPISpecs(fakeBazel, []openAPISpecEntry{
		{App: "control-api", Domain: "manmanv2", OpenAPITarget: "@@//manmanv2/control_api:openapi_spec"},
	}, outDir)
	if err != nil {
		t.Fatalf("BuildOpenAPISpecs failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	got, err := os.ReadFile(filepath.Join(outDir, "manmanv2-control-api_openapi_spec.json"))
	if err != nil {
		t.Fatalf("expected spec copied to output dir via fallback path: %v", err)
	}
	if string(got) != string(specContent) {
		t.Fatalf("spec content mismatch: got %q want %q", got, specContent)
	}
}

func TestExecuteBuildReleaseArtifacts_EmptyPlan(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "build-release-empty-test")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	fakeBazel := newFakeBazel() // no calls expected

	result, err := ExecuteBuildReleaseArtifacts(BuildReleaseArtifactsParams{
		Plan:                   PlanResult{},
		GitSHA:                 "deadbeef",
		DryRun:                 false,
		AppsOutputDir:          filepath.Join(tmpDir, "apps"),
		ChartsOutputDir:        filepath.Join(tmpDir, "charts"),
		OpenAPIOutputDir:       filepath.Join(tmpDir, "openapi"),
		DescriptorSetOutputDir: filepath.Join(tmpDir, "descriptor-sets"),
		CLIBinariesOutputDir:   filepath.Join(tmpDir, "cli"),
		Bazel:                  fakeBazel,
		FS:                     defaultFS,
		WorkspaceRoot:          tmpDir,
	})
	if err != nil {
		t.Fatalf("ExecuteBuildReleaseArtifacts failed: %v", err)
	}
	if len(result.Apps) != 0 || len(result.Charts) != 0 || len(result.OpenAPISpecs) != 0 || len(result.DescriptorSets) != 0 || len(result.CLIBinaries) != 0 {
		t.Fatalf("expected an empty plan to build nothing, got %+v", result)
	}
	if len(fakeBazel.recorded) != 0 {
		t.Fatalf("expected an empty plan to make zero bazel calls, got %v", fakeBazel.recorded)
	}
}

func TestExecuteBuildReleaseArtifacts_DryRunSkipsAppsSpecsAndCLI(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "build-release-dryrun-test")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	fakeBazel := newFakeBazel() // no calls expected -- everything requested is dry-run-gated

	plan := PlanResult{
		Matrix: map[string]interface{}{
			"include": []map[string]string{
				{"app": "release_helper_go", "domain": "tools"},
			},
		},
		HasSpecs: true,
		OpenAPIMatrix: map[string]interface{}{
			"include": []map[string]string{
				{"app": "control-api", "domain": "manmanv2", "openapi_target": "//manmanv2/control_api:openapi_spec"},
			},
		},
		HasDescriptorSets: true,
		DescriptorSetMatrix: map[string]interface{}{
			"include": []map[string]string{
				{"app": "leaflab-api", "domain": "leaflab", "descriptor_set_target": "//leaflab/api:leaflab_api_descriptor_set"},
			},
		},
	}

	result, err := ExecuteBuildReleaseArtifacts(BuildReleaseArtifactsParams{
		Plan:                   plan,
		GitSHA:                 "deadbeef",
		DryRun:                 true,
		AppsOutputDir:          filepath.Join(tmpDir, "apps"),
		ChartsOutputDir:        filepath.Join(tmpDir, "charts"),
		OpenAPIOutputDir:       filepath.Join(tmpDir, "openapi"),
		DescriptorSetOutputDir: filepath.Join(tmpDir, "descriptor-sets"),
		CLIBinariesOutputDir:   filepath.Join(tmpDir, "cli"),
		Bazel:                  fakeBazel,
		FS:                     defaultFS,
		WorkspaceRoot:          tmpDir,
	})
	if err != nil {
		t.Fatalf("ExecuteBuildReleaseArtifacts failed: %v", err)
	}
	if len(result.Apps) != 0 {
		t.Fatalf("expected dry-run to skip app images, got %+v", result.Apps)
	}
	if len(result.OpenAPISpecs) != 0 {
		t.Fatalf("expected dry-run to skip openapi specs, got %+v", result.OpenAPISpecs)
	}
	if len(result.DescriptorSets) != 0 {
		t.Fatalf("expected dry-run to skip descriptor sets, got %+v", result.DescriptorSets)
	}
	if len(result.CLIBinaries) != 0 {
		t.Fatalf("expected dry-run to skip cli binaries, got %+v", result.CLIBinaries)
	}
	if len(fakeBazel.recorded) != 0 {
		t.Fatalf("expected dry-run to make zero bazel calls, got %v", fakeBazel.recorded)
	}
}

// TestExecuteBuildReleaseArtifacts_CLIBinariesAndAppSkip exercises the full
// dispatch path for a matrix containing release_helper_go: the app-image
// step gracefully skips it (it's a "cli" app, not an image app -- same as
// release-v2.yml's former "Build app images" step handled any non-image app
// in the matrix), while the CLI-binaries step packages it for all four
// platforms. Mirrors TestPackageAppAssets_CLI's fake bazel-bin layout.
func TestExecuteBuildReleaseArtifacts_CLIBinariesAndAppSkip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "build-release-cli-test")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	wsRoot := filepath.Join(tmpDir, "ws")
	binDir := filepath.Join(wsRoot, "bazel-bin", "tools", "release_helper_go", "release_helper_go_")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "release_helper_go"), []byte("fake-cli-binary"), 0755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	customJSON := []byte(`{"name":"release_helper_go","domain":"tools","language":"go","app_type":"cli","registry":"ghcr.io","organization":"whale-net","repo_name":"tools-release_helper_go","binary_target":"@@//tools/release_helper_go:release_helper_go","version":"latest"}`)
	_, fakeBazelInfra := buildFakeInfra([]fakeApp{
		{pkg: "tools/release_helper_go", targetSuffix: "release_helper_go_metadata", name: "release_helper_go", domain: "tools", customJSON: customJSON},
	})
	// Extend the discovery fake with the four cross-compile build calls
	// PackageAppAssets issues.
	fakeBazelInfra.calls = append(fakeBazelInfra.calls,
		fakeBazelCall{argsContain: []string{"build", "--platforms=//tools:linux_x86_64"}, output: ""},
		fakeBazelCall{argsContain: []string{"build", "--platforms=//tools:linux_arm64"}, output: ""},
		fakeBazelCall{argsContain: []string{"build", "--platforms=//tools:darwin_arm64"}, output: ""},
		fakeBazelCall{argsContain: []string{"build", "--platforms=//tools:darwin_x86_64"}, output: ""},
	)

	plan := PlanResult{
		Matrix: map[string]interface{}{
			"include": []map[string]string{
				{"app": "release_helper_go", "domain": "tools", "version": "v1.0.0"},
			},
		},
	}

	result, err := ExecuteBuildReleaseArtifacts(BuildReleaseArtifactsParams{
		Plan:                 plan,
		GitSHA:               "deadbeef",
		DryRun:               false,
		AppsOutputDir:        filepath.Join(tmpDir, "apps"),
		ChartsOutputDir:      filepath.Join(tmpDir, "charts"),
		OpenAPIOutputDir:     filepath.Join(tmpDir, "openapi"),
		CLIBinariesOutputDir: filepath.Join(tmpDir, "cli"),
		Bazel:                fakeBazelInfra,
		FS:                   defaultFS,
		WorkspaceRoot:        wsRoot,
	})
	if err != nil {
		t.Fatalf("ExecuteBuildReleaseArtifacts failed: %v", err)
	}

	if len(result.Apps) != 0 {
		t.Fatalf("expected release_helper_go (a cli app) to be skipped for image build, got %+v", result.Apps)
	}

	assets, ok := result.CLIBinaries["release_helper_go"]
	if !ok {
		t.Fatalf("expected release_helper_go to be packaged as a cli binary, got %+v", result.CLIBinaries)
	}
	if len(assets) != 4 {
		t.Fatalf("expected 4 platform assets, got %d: %+v", len(assets), assets)
	}
}
