package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// writeBuildFile creates workspaceRoot/pkg/BUILD.bazel with the given
// content, creating parent directories as needed. pkg == "" writes to the
// workspace root itself.
func writeBuildFile(t *testing.T, workspaceRoot, pkg, content string) {
	t.Helper()
	dir := filepath.Join(workspaceRoot, filepath.FromSlash(pkg))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "BUILD.bazel"), []byte(content), 0o644); err != nil {
		t.Fatalf("write BUILD.bazel in %s: %v", dir, err)
	}
}

func TestDiscoverFastMinimalContainerApp(t *testing.T) {
	root := t.TempDir()
	writeBuildFile(t, root, "demo/hello_python", `
load("//tools/bazel:release.bzl", "release_app")

release_app(
    name = "hello-python",
    language = "python",
    domain = "demo",
)
`)

	apps, err := ListAllAppsFast(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected 1 app, got %d: %+v", len(apps), apps)
	}
	app := apps[0]
	if app.Name != "hello-python" {
		t.Errorf("Name = %q, want hello-python", app.Name)
	}
	if app.Domain != "demo" {
		t.Errorf("Domain = %q, want demo", app.Domain)
	}
	if app.RepoName != "demo-hello-python" {
		t.Errorf("RepoName = %q, want demo-hello-python", app.RepoName)
	}
	if app.Version != "latest" {
		t.Errorf("Version = %q, want latest (default)", app.Version)
	}
	if app.Registry != "ghcr.io" {
		t.Errorf("Registry = %q, want ghcr.io (default)", app.Registry)
	}
	if app.Organization != "whale-net" {
		t.Errorf("Organization = %q, want whale-net (default)", app.Organization)
	}
	if app.DeployUnit != appmetapb.DeployUnit_DEPLOY_UNIT_CHART {
		t.Errorf("DeployUnit = %v, want DEPLOY_UNIT_CHART (default for container apps)", app.DeployUnit)
	}
	if app.BinaryTarget != "//demo/hello_python:hello-python" {
		t.Errorf("BinaryTarget = %q, want //demo/hello_python:hello-python", app.BinaryTarget)
	}
	if app.ImageTarget != "//demo/hello_python:hello-python_image" {
		t.Errorf("ImageTarget = %q, want //demo/hello_python:hello-python_image", app.ImageTarget)
	}
	if app.BazelTarget != "//demo/hello_python:hello-python_metadata" {
		t.Errorf("BazelTarget = %q, want //demo/hello_python:hello-python_metadata", app.BazelTarget)
	}
	if app.HealthCheck != nil {
		t.Errorf("HealthCheck = %+v, want nil (not enabled)", app.HealthCheck)
	}
	if app.Ingress != nil {
		t.Errorf("Ingress = %+v, want nil (not set)", app.Ingress)
	}
	if app.Resources != nil {
		t.Errorf("Resources = %+v, want nil (not set)", app.Resources)
	}
}

func TestDiscoverFastCliApp(t *testing.T) {
	root := t.TempDir()
	writeBuildFile(t, root, "tools/mycli", `
load("//tools/bazel:release.bzl", "release_app")

release_app(
    name = "mycli",
    app_type = "cli",
    binary_name = ":mycli_bin",
    language = "go",
    domain = "tools",
)
`)

	apps, err := ListAllAppsFast(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(apps))
	}
	app := apps[0]
	if app.ImageTarget != "" {
		t.Errorf("ImageTarget = %q, want empty (cli app is not containerized)", app.ImageTarget)
	}
	if app.DeployUnit != appmetapb.DeployUnit_DEPLOY_UNIT_NONE {
		t.Errorf("DeployUnit = %v, want DEPLOY_UNIT_NONE (default for cli apps)", app.DeployUnit)
	}
	if app.BinaryTarget != "//tools/mycli:mycli_bin" {
		t.Errorf("BinaryTarget = %q, want //tools/mycli:mycli_bin (custom binary_name)", app.BinaryTarget)
	}
}

func TestDiscoverFastFullFieldCoverage(t *testing.T) {
	root := t.TempDir()
	writeBuildFile(t, root, "manmanv2/api", `
load("//tools/bazel:release.bzl", "release_app")

release_app(
    name = "control-api",
    app_name = "control-api",
    app_type = "external-api",
    language = "go",
    domain = "manmanv2",
    description = "Control API",
    version = "v1.2.3",
    registry = "custom.registry",
    organization = "my-org",
    port = 8080,
    replicas = 3,
    health_check_enabled = True,
    health_check_path = "/healthz",
    ingress_host = "api.example.local",
    ingress_tls_secret = "api-tls",
    command = ["python3"],
    args = ["-m", "main"],
    resources_requests_cpu = "100m",
    resources_requests_memory = "128Mi",
    resources_limits_cpu = "200m",
    resources_limits_memory = "256Mi",
    deploy_unit = "image",
)
`)

	apps, err := ListAllAppsFast(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(apps))
	}
	app := apps[0].AppManifest
	if app.Port != 8080 || app.Replicas != 3 {
		t.Errorf("Port/Replicas = %d/%d, want 8080/3", app.Port, app.Replicas)
	}
	if app.HealthCheck == nil || !app.HealthCheck.Enabled || app.HealthCheck.Path != "/healthz" {
		t.Errorf("HealthCheck = %+v, want enabled=true path=/healthz", app.HealthCheck)
	}
	if app.Ingress == nil || app.Ingress.Host != "api.example.local" || app.Ingress.TlsSecretName != "api-tls" {
		t.Errorf("Ingress = %+v, want host=api.example.local tls=api-tls", app.Ingress)
	}
	if app.Resources == nil || app.Resources.RequestsCpu != "100m" || app.Resources.LimitsMemory != "256Mi" {
		t.Errorf("Resources = %+v, unexpected", app.Resources)
	}
	if len(app.Command) != 1 || app.Command[0] != "python3" {
		t.Errorf("Command = %v, want [python3]", app.Command)
	}
	if len(app.Args) != 2 || app.Args[1] != "main" {
		t.Errorf("Args = %v, want [-m main]", app.Args)
	}
	if app.DeployUnit != appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE {
		t.Errorf("DeployUnit = %v, want DEPLOY_UNIT_IMAGE (explicit override)", app.DeployUnit)
	}
	if app.Registry != "custom.registry" || app.Organization != "my-org" {
		t.Errorf("Registry/Organization = %q/%q, want custom.registry/my-org", app.Registry, app.Organization)
	}
}

func TestDiscoverFastMissingDomainErrors(t *testing.T) {
	root := t.TempDir()
	writeBuildFile(t, root, "demo/broken", `
load("//tools/bazel:release.bzl", "release_app")

release_app(
    name = "broken",
    language = "go",
)
`)

	if _, err := ListAllAppsFast(root); err == nil {
		t.Fatal("expected error for release_app missing domain")
	}
}

func TestDiscoverFastExcludesDirectAppMetadataCalls(t *testing.T) {
	// Mirrors tools/appmeta/testdata/BUILD.bazel: a raw app_metadata(...)
	// rule call (as opposed to the release_app(...) macro) is how a testonly
	// fixture opts out of real bazel query discovery (`except
	// attr(testonly, 1, //...)`). The fast scanner only recognizes
	// release_app(...) calls, so it excludes these for free -- verify that
	// holds.
	root := t.TempDir()
	writeBuildFile(t, root, "tools/appmeta/testdata", `
load("//tools/bazel:release.bzl", "app_metadata")

app_metadata(
    name = "fixture-app_metadata",
    testonly = True,
    app_name = "fixture-app",
    binary_target = ":placeholder",
    language = "python",
    repo_name = "appmeta-fixture-app",
    domain = "appmeta",
)
`)

	apps, err := ListAllAppsFast(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("expected 0 apps (direct app_metadata call must be excluded), got %d: %+v", len(apps), apps)
	}
}

func TestDiscoverFastHelmChartInlineApps(t *testing.T) {
	root := t.TempDir()
	writeBuildFile(t, root, "demo/hello_python", `
load("//tools/bazel:release.bzl", "release_app")

release_app(
    name = "hello-python",
    language = "python",
    domain = "demo",
)
`)
	writeBuildFile(t, root, "demo", `
load("//tools/bazel:release.bzl", "release_helm_chart")

release_helm_chart(
    name = "fastapi_chart",
    chart_name = "hello-fastapi",
    namespace = "demo",
    domain = "demo",
    apps = ["//demo/hello_python:hello-python_metadata"],
)
`)

	charts, err := ListAllHelmChartsFast(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(charts) != 1 {
		t.Fatalf("expected 1 chart, got %d", len(charts))
	}
	c := charts[0]
	if c.Name != "helm-demo-hello-fastapi" {
		t.Errorf("Name = %q, want helm-demo-hello-fastapi", c.Name)
	}
	if c.Version != "0.0.0-dev" {
		t.Errorf("Version = %q, want 0.0.0-dev (default)", c.Version)
	}
	if c.Environment != "production" {
		t.Errorf("Environment = %q, want production (default)", c.Environment)
	}
	if c.ChartTarget != ":fastapi_chart" {
		t.Errorf("ChartTarget = %q, want :fastapi_chart (attr.string, not a resolved label)", c.ChartTarget)
	}
	if len(c.AppRefs) != 1 || c.AppRefs[0] != "demo/hello-python" {
		t.Errorf("AppRefs = %v, want [demo/hello-python]", c.AppRefs)
	}
	if len(c.Apps) != 1 || c.Apps[0] != "hello-python" {
		t.Errorf("Apps = %v, want [hello-python]", c.Apps)
	}
}

func TestDiscoverFastHelmChartSharedConstantIndirection(t *testing.T) {
	// Mirrors tools/app_registry/BUILD.bazel's `apps = APP_REGISTRY_APPS`
	// pattern: a release_helm_chart's apps attr can reference a top-level
	// list constant defined earlier in the same BUILD.bazel file, not just
	// an inline list literal.
	root := t.TempDir()
	writeBuildFile(t, root, "svc/a", `
load("//tools/bazel:release.bzl", "release_app")
release_app(name = "svc-a", language = "go", domain = "svc")
`)
	writeBuildFile(t, root, "svc/b", `
load("//tools/bazel:release.bzl", "release_app")
release_app(name = "svc-b", language = "go", domain = "svc")
`)
	writeBuildFile(t, root, "svc", `
load("//tools/bazel:release.bzl", "release_helm_chart")

SVC_APPS = [
    "//svc/a:svc-a_metadata",
    "//svc/b:svc-b_metadata",
]

release_helm_chart(
    name = "svc_chart",
    namespace = "svc",
    domain = "svc",
    apps = SVC_APPS,
)
`)

	charts, err := ListAllHelmChartsFast(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(charts) != 1 {
		t.Fatalf("expected 1 chart, got %d", len(charts))
	}
	if len(charts[0].AppRefs) != 2 {
		t.Fatalf("expected 2 app_refs resolved through the SVC_APPS constant, got %v", charts[0].AppRefs)
	}
}

func TestDiscoverFastHelmChartUnknownAppRefErrors(t *testing.T) {
	root := t.TempDir()
	writeBuildFile(t, root, "demo", `
load("//tools/bazel:release.bzl", "release_helm_chart")

release_helm_chart(
    name = "broken_chart",
    namespace = "demo",
    domain = "demo",
    apps = ["//demo/does_not_exist:nope_metadata"],
)
`)

	if _, err := ListAllHelmChartsFast(root); err == nil {
		t.Fatal("expected error for release_helm_chart referencing an unknown app_metadata target")
	}
}

func TestListAllAppsRespectsFastFlag(t *testing.T) {
	root := t.TempDir()
	writeBuildFile(t, root, "demo/hello_go", `
load("//tools/bazel:release.bzl", "release_app")
release_app(name = "hello-go", language = "go", domain = "demo")
`)

	fastDiscovery = true
	defer func() { fastDiscovery = false }()

	// A bazel runner that fails on any call proves ListAllApps never shells
	// out when --fast is set.
	bazel := newFakeBazel(fakeBazelCall{argsContain: []string{"query"}, err: fmt.Errorf("bazel must not be invoked in --fast mode")})

	apps, err := ListAllApps(bazel, newFakeFS(), root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(apps) != 1 || apps[0].Name != "hello-go" {
		t.Fatalf("expected [hello-go], got %+v", apps)
	}
}

func TestFindBuildFilesSkipsBazelOutputRoots(t *testing.T) {
	root := t.TempDir()
	writeBuildFile(t, root, "real/pkg", "# real package\n")
	// A bazel-out-shaped directory that must never be descended into.
	writeBuildFile(t, root, "bazel-out/k8-fastbuild/bin/real/pkg", "# must not be found\n")
	writeBuildFile(t, root, ".git/hooks", "# must not be found\n")

	files, err := findBuildFiles(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, f := range files {
		if strings.Contains(f, "bazel-out") || strings.Contains(f, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			t.Errorf("findBuildFiles returned a file under a skipped directory: %s", f)
		}
	}
	if len(files) != 1 {
		t.Fatalf("expected exactly 1 real BUILD.bazel, got %d: %v", len(files), files)
	}
}
