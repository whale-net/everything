package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/grpc"
)

// fakeClientBuilder is a thin builder over FakeArtifactRegistryClient so tests
// can express their stubs inline without setting function fields directly.
type fakeClientBuilder struct {
	*FakeArtifactRegistryClient
}

func newFakeArtifactClient() *fakeClientBuilder {
	return &fakeClientBuilder{FakeArtifactRegistryClient: NewFakeArtifactRegistryClient()}
}

func (b *fakeClientBuilder) withCheckChartHermeticity(domain string, enforced bool) *fakeClientBuilder {
	prev := b.CheckChartHermeticityFn
	b.CheckChartHermeticityFn = func(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
		if in.ChartDomain == domain {
			return &pb.CheckChartHermeticityResponse{Enforced: enforced}, nil
		}
		if prev != nil {
			return prev(ctx, in, opts...)
		}
		return &pb.CheckChartHermeticityResponse{Enforced: false}, nil
	}
	return b
}

func (b *fakeClientBuilder) withGetArtifact(ownerFullName string, kind pb.ArtifactKind, version string) *fakeClientBuilder {
	prev := b.GetArtifactFn
	b.GetArtifactFn = func(ctx context.Context, in *pb.GetArtifactRequest, opts ...grpc.CallOption) (*pb.GetArtifactResponse, error) {
		if in.OwnerFullName == ownerFullName && in.Kind == kind && in.LatestPublished {
			return &pb.GetArtifactResponse{Artifact: &pb.Artifact{Version: version}}, nil
		}
		if prev != nil {
			return prev(ctx, in, opts...)
		}
		return &pb.GetArtifactResponse{}, nil
	}
	return b
}

func (b *fakeClientBuilder) withGetArtifactError(ownerFullName string, kind pb.ArtifactKind, retErr error) *fakeClientBuilder {
	prev := b.GetArtifactFn
	b.GetArtifactFn = func(ctx context.Context, in *pb.GetArtifactRequest, opts ...grpc.CallOption) (*pb.GetArtifactResponse, error) {
		if in.OwnerFullName == ownerFullName && in.Kind == kind {
			return nil, retErr
		}
		if prev != nil {
			return prev(ctx, in, opts...)
		}
		return &pb.GetArtifactResponse{}, nil
	}
	return b
}

// ── findChartApp / resolveChartAppVersions ─────────────────────────────────

func testApps() []AppMetadata {
	return []AppMetadata{
		{AppManifest: &appmetapb.AppManifest{Name: "control-api", Domain: "manmanv2"}, BazelTarget: "//manmanv2/api:control-api_metadata"},
		{AppManifest: &appmetapb.AppManifest{Name: "event-processor", Domain: "manmanv2"}, BazelTarget: "//manmanv2/processor:event-processor_metadata"},
	}
}

// TestResolveChartAppVersionsKeysByFullName pins the app-versions map to
// composer.go's values.yaml key convention ("<domain>-<name>", i.e.
// AppMetadata.FullName()). packageChartWithVersion looks up
// appVersions[appKey] against the values.yaml "apps" map, which composer.go
// keys by FullName() — keying by the bare app name here would make every
// lookup silently miss and leave the chart's baked-in "latest" imageTag in
// place, which is exactly the bug this test guards against.
func TestResolveChartAppVersionsKeysByFullName(t *testing.T) {
	chart := HelmChartMetadata{ChartManifest: &appmetapb.ChartManifest{
		Name: "helm-manmanv2-control-services", Domain: "manmanv2",
		Apps: []string{"control-api", "event-processor"},
	}}
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--list", "manmanv2-control-api.*"}, output: "manmanv2-control-api.v1.2.3"},
		fakeGitCall{argsContain: []string{"tag", "--list", "manmanv2-event-processor.*"}, output: "manmanv2-event-processor.v0.2.18"},
	)

	versions, err := resolveChartAppVersions(context.Background(), chart, testApps(), git, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[string]string{
		"manmanv2-control-api":     "v1.2.3",
		"manmanv2-event-processor": "v0.2.18",
	}
	for key, ver := range want {
		if got := versions[key]; got != ver {
			t.Errorf("versions[%q] = %q, want %q (full versions map: %v)", key, got, ver, versions)
		}
	}
	// Bare app names must NOT be used as keys.
	if _, ok := versions["event-processor"]; ok {
		t.Errorf("versions must be keyed by FullName(), not bare app name; got bare-name key in %v", versions)
	}
}

// TestResolveChartAppVersionsAllocateUsesRegistry verifies that an
// allocate-stage domain resolves app versions from the App Registry, not git.
func TestResolveChartAppVersionsAllocateUsesRegistry(t *testing.T) {
	chart := HelmChartMetadata{ChartManifest: &appmetapb.ChartManifest{
		Name: "helm-manmanv2-control-services", Domain: "manmanv2",
		Apps: []string{"control-api"},
	}}
	// git would return v0.0.1, registry returns v1.9.0 — registry must win.
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--list", "manmanv2-control-api.*"}, output: "manmanv2-control-api.v0.0.1"},
	)
	client := newFakeArtifactClient().
		withCheckChartHermeticity("manmanv2", true). // domain is at allocate
		withGetArtifact("manmanv2-control-api", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "v1.9.0")

	versions, err := resolveChartAppVersions(context.Background(), chart, testApps(), git, client, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := versions["manmanv2-control-api"]; got != "v1.9.0" {
		t.Errorf("expected registry version v1.9.0, got %q", got)
	}
}

// TestResolveChartAppVersionsAllocateRegistryErrorFails verifies that a
// registry error for an allocate-stage domain is a hard failure — no git fallback.
func TestResolveChartAppVersionsAllocateRegistryErrorFails(t *testing.T) {
	chart := HelmChartMetadata{ChartManifest: &appmetapb.ChartManifest{
		Name: "helm-manmanv2-control-services", Domain: "manmanv2",
		Apps: []string{"control-api"},
	}}
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--list", "manmanv2-control-api.*"}, output: "manmanv2-control-api.v0.0.1"},
	)
	client := newFakeArtifactClient().
		withCheckChartHermeticity("manmanv2", true). // domain is at allocate
		withGetArtifactError("manmanv2-control-api", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, fmt.Errorf("registry unavailable"))

	_, err := resolveChartAppVersions(context.Background(), chart, testApps(), git, client, nil)
	if err == nil {
		t.Fatal("expected error when registry fails for allocate-stage domain, got nil")
	}
	if !strings.Contains(err.Error(), "allocate stage") {
		t.Errorf("error should mention allocate stage, got: %v", err)
	}
}

// TestResolveChartAppVersionsPlanPinTakesPrecedence is issue #901's
// red/green case: a composed member app present in resolvedPlanVersions
// (i.e. it was itself resolved as part of this release batch's plan) must
// use that pinned version directly, skipping both the App Registry query
// and the git-tag fallback entirely. The fake client is wired to error if
// GetArtifact is called for this app at all, so any regression that still
// queries the registry fails loudly rather than just happening to pick a
// different version.
func TestResolveChartAppVersionsPlanPinTakesPrecedence(t *testing.T) {
	chart := HelmChartMetadata{ChartManifest: &appmetapb.ChartManifest{
		Name: "helm-manmanv2-control-services", Domain: "manmanv2",
		Apps: []string{"control-api"},
	}}
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--list", "manmanv2-control-api.*"}, output: "manmanv2-control-api.v0.0.1"},
	)
	client := newFakeArtifactClient().
		withCheckChartHermeticity("manmanv2", true). // domain is at allocate
		withGetArtifactError("manmanv2-control-api", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, fmt.Errorf("GetArtifact must not be called when a plan pin is present"))

	resolvedPlanVersions := map[string]string{
		"manmanv2-control-api": "v2.5.0", // the release batch's actual resolved version
	}

	versions, err := resolveChartAppVersions(context.Background(), chart, testApps(), git, client, resolvedPlanVersions)
	if err != nil {
		t.Fatalf("unexpected error (plan pin should have bypassed the registry call): %v", err)
	}
	if got := versions["manmanv2-control-api"]; got != "v2.5.0" {
		t.Errorf("versions[manmanv2-control-api] = %q, want plan-pinned %q", got, "v2.5.0")
	}
}

// TestResolveChartAppVersionsPlanPinAbsentFallsBackUnchanged verifies that a
// composed member app NOT present in resolvedPlanVersions (not part of this
// release batch) still resolves via the existing independent-query path —
// no regression to the common case where resolvedPlanVersions is non-nil
// but simply doesn't cover every composed app.
func TestResolveChartAppVersionsPlanPinAbsentFallsBackUnchanged(t *testing.T) {
	chart := HelmChartMetadata{ChartManifest: &appmetapb.ChartManifest{
		Name: "helm-manmanv2-control-services", Domain: "manmanv2",
		Apps: []string{"control-api"},
	}}
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--list", "manmanv2-control-api.*"}, output: "manmanv2-control-api.v0.0.1"},
	)
	client := newFakeArtifactClient().
		withCheckChartHermeticity("manmanv2", true). // domain is at allocate
		withGetArtifact("manmanv2-control-api", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "v1.9.0")

	resolvedPlanVersions := map[string]string{
		"manmanv2-event-processor": "v9.9.9", // a different app entirely; control-api is absent
	}

	versions, err := resolveChartAppVersions(context.Background(), chart, testApps(), git, client, resolvedPlanVersions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := versions["manmanv2-control-api"]; got != "v1.9.0" {
		t.Errorf("expected unpinned app to fall back to registry version v1.9.0, got %q", got)
	}
}

func TestFindChartAppNotFound(t *testing.T) {
	if _, err := findChartApp("nonexistent", "manmanv2", testApps()); err == nil {
		t.Fatal("expected error for unknown app")
	}
}

func TestFindChartAppPrefersChartDomain(t *testing.T) {
	apps := []AppMetadata{
		{AppManifest: &appmetapb.AppManifest{Name: "worker", Domain: "other"}},
		{AppManifest: &appmetapb.AppManifest{Name: "worker", Domain: "manmanv2"}},
	}
	matched, err := findChartApp("worker", "manmanv2", apps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched.Domain != "manmanv2" {
		t.Errorf("expected match in chart's own domain, got domain %q", matched.Domain)
	}
}

// ── packageChartWithVersion's values.yaml imageTag guardrail ───────────────
//
// These tests only exercise the appVersions-key-mismatch failure path,
// which returns before packageChartWithVersion ever shells out to `helm
// package` — the Bazel go_test target has no `helm` binary as a data
// dependency, so a success-path test would fail on `helm: executable file
// not found` rather than testing anything meaningful here.

func TestPackageChartWithVersionErrorsOnKeyMismatch(t *testing.T) {
	chartDir := t.TempDir()
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), "name: control-services\nversion: 0.0.0-dev\n")
	// values.yaml keyed the way composer.go actually keys it.
	writeFile(t, filepath.Join(chartDir, "values.yaml"), "apps:\n  manmanv2-event-processor:\n    imageTag: latest\n")

	// A bare app name, as the pre-fix resolveChartAppVersions produced,
	// must not silently no-op against the domain-prefixed values.yaml key.
	appVersions := map[string]string{"event-processor": "v0.2.18"}

	_, err := packageChartWithVersion(chartDir, "helm-manmanv2-control-services", "v0.2.18", t.TempDir(), appVersions)
	if err == nil {
		t.Fatal("expected error when appVersions key doesn't match values.yaml apps key, got nil")
	}
	if !strings.Contains(err.Error(), "event-processor") {
		t.Errorf("expected error to name the unmatched key, got: %v", err)
	}
}

func TestPackageChartWithVersionErrorsOnMissingAppsMap(t *testing.T) {
	chartDir := t.TempDir()
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), "name: control-services\nversion: 0.0.0-dev\n")
	writeFile(t, filepath.Join(chartDir, "values.yaml"), "global:\n  namespace: manmanv2\n")

	appVersions := map[string]string{"manmanv2-event-processor": "v0.2.18"}
	_, err := packageChartWithVersion(chartDir, "helm-manmanv2-control-services", "v0.2.18", t.TempDir(), appVersions)
	if err == nil {
		t.Fatal("expected error when values.yaml has no apps map, got nil")
	}
}

// Regression test for issue #1259: finalize-chart passes the whole release
// batch's appVersions map (shared across every chart in the batch) to
// packageChartWithVersion. A chart must ignore batch entries for apps it
// doesn't compose rather than hard-failing on them.
func TestPackageChartWithVersionIgnoresAppsOutsideChartScope(t *testing.T) {
	chartDir := t.TempDir()
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), "name: hello-fastapi-enhanced\nversion: 0.0.0-dev\n")
	writeFile(t, filepath.Join(chartDir, "values.yaml"), "apps:\n  demo-hello-fastapi:\n    imageTag: latest\n")

	// A batch-wide appVersions map containing apps not composed by this
	// chart must not trip the "no entry to set imageTag on" guardrail.
	appVersions := map[string]string{
		"demo-hello-fastapi": "v1.0.0",
		"demo-hello-go":      "v1.0.0",
		"demo-hello-worker":  "v1.0.0",
	}

	// helm isn't available in the Bazel test sandbox, so this is expected to
	// still fail once it reaches `helm package` -- we only need to confirm
	// it gets past the values.yaml guardrail without erroring on the
	// out-of-scope batch apps.
	_, err := packageChartWithVersion(chartDir, "helm-demo-hello-fastapi-enhanced", "v1.0.0", t.TempDir(), appVersions)
	if err != nil && strings.Contains(err.Error(), "values.yaml") {
		t.Fatalf("unexpected values.yaml guardrail error for out-of-scope batch app: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
