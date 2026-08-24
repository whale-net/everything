package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// buildTestChartArchive packages files into a gzip+tar archive, writing
// entries in the given order with the given timestamps -- letting tests
// synthesize two archives with identical decoded content but different raw
// bytes, mimicking helm package's non-deterministic output (issue #814).
func buildTestChartArchive(t *testing.T, files map[string]string, order []string, gzipModTime, tarModTime time.Time) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		t.Fatalf("gzip writer: %v", err)
	}
	gz.ModTime = gzipModTime
	tw := tar.NewWriter(gz)
	for _, name := range order {
		content := files[name]
		hdr := &tar.Header{
			Name:    name,
			Mode:    0644,
			Size:    int64(len(content)),
			ModTime: tarModTime,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write tar content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

// versionedFakeUploader is a ChartUploader whose FetchChart response varies
// by requested version, needed for tests that exercise the orphan-collision
// advance loop (which probes multiple candidate versions in sequence).
type versionedFakeUploader struct {
	data        map[string][]byte
	uploadCalls int
	uploadedVer []string
}

func (u *versionedFakeUploader) UploadChart(ctx context.Context, repoURL, username, password, chartPath string) error {
	u.uploadCalls++
	base := filepath.Base(chartPath)
	u.uploadedVer = append(u.uploadedVer, base)
	return nil
}

func (u *versionedFakeUploader) FetchChart(ctx context.Context, repoURL, username, password, publishedName, version string) ([]byte, error) {
	data, ok := u.data[version]
	if !ok {
		return nil, fmt.Errorf("404 not found")
	}
	return data, nil
}

type fakeHelmPackager struct {
	packagedPath string
	packageErr   error
	content      []byte // when set, written verbatim instead of the literal fake bytes
	calls        int
	// lastAppVersions captures the appVersions map passed on the most
	// recent Package call, so tests can assert what resolveChartAppVersions
	// actually resolved (issue #901's plan-pin precedence).
	lastAppVersions map[string]string
	// lastStrict captures the strict flag passed on the most recent Package
	// call, so tests can assert release-charts vs finalize-chart pass the
	// right value (release_charts.go's ChartDir-empty vs ChartDir-set
	// distinction in releaser_chart.go's Build).
	lastStrict bool
}

func (f *fakeHelmPackager) Package(chartDir, chartName, version, outDir string, appVersions map[string]string, strict bool) (string, error) {
	f.calls++
	f.lastAppVersions = appVersions
	f.lastStrict = strict
	if f.packageErr != nil {
		return "", f.packageErr
	}
	if f.packagedPath != "" {
		return f.packagedPath, nil
	}
	body := f.content
	if body == nil {
		body = []byte("fake-chart-bytes-v1")
	}
	// Create dummy packaged file in outDir
	published := strings.TrimPrefix(chartName, "helm-")
	path := filepath.Join(outDir, fmt.Sprintf("%s-%s.tgz", published, version))
	if err := os.WriteFile(path, body, 0644); err != nil {
		return "", err
	}
	return path, nil
}

type fakeChartUploader struct {
	uploadedPath     string
	uploadedURL      string
	uploadErr        error
	uploadCalls      int
	fetchedChartData []byte
	fetchErr         error
	fetchCalls       int
}

func (f *fakeChartUploader) UploadChart(ctx context.Context, repoURL, username, password, chartPath string) error {
	f.uploadCalls++
	f.uploadedURL = repoURL
	f.uploadedPath = chartPath
	return f.uploadErr
}

func (f *fakeChartUploader) FetchChart(ctx context.Context, repoURL, username, password, publishedName, version string) ([]byte, error) {
	f.fetchCalls++
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return f.fetchedChartData, nil
}

func setupChartTestFixtures(t *testing.T) (BazelRunner, GitRunner, DockerRunner, FileSystem, string) {
	t.Helper()
	workspaceRoot := t.TempDir()

	// Setup Bazel runner that returns chart and app metadata
	chartQueryOutput := "//demo:hello-fastapi_chart_metadata"
	chartCqueryOutput := "//demo:hello-fastapi_chart_metadata\t{\"name\":\"helm-demo-hello-fastapi\",\"domain\":\"demo\",\"apps\":[\"hello-fastapi\"],\"namespace\":\"demo\",\"environment\":\"production\"}"
	appQueryOutput := "//demo/hello_fastapi:hello-fastapi_metadata"
	appCqueryOutput := "//demo/hello_fastapi:hello-fastapi_metadata\t" + string(sampleMetaJSON("hello-fastapi", "demo"))

	bazel := newFakeBazel(
		fakeBazelCall{argsContain: []string{"query", "kind(helm_chart_metadata"}, output: chartQueryOutput},
		fakeBazelCall{argsContain: []string{"cquery", "hello-fastapi_chart_metadata"}, output: chartCqueryOutput},
		fakeBazelCall{argsContain: []string{"query", "kind(app_metadata"}, output: appQueryOutput},
		fakeBazelCall{argsContain: []string{"cquery", "hello-fastapi_metadata"}, output: appCqueryOutput},
		fakeBazelCall{argsContain: []string{"build"}, output: ""},
	)

	git := newFakeGit(
		fakeGitCall{argsContain: []string{"tag", "--list", "helm-demo-hello-fastapi.*"}, output: "helm-demo-hello-fastapi.v0.1.0"},
		fakeGitCall{argsContain: []string{"tag", "--list", "demo-hello-fastapi.*"}, output: "demo-hello-fastapi.v1.0.0"},
		fakeGitCall{argsContain: []string{"rev-parse", "HEAD"}, output: "abcdef1234567890"},
		fakeGitCall{argsContain: []string{"rev-parse"}, output: "found", err: nil},
		fakeGitCall{argsContain: []string{"tag", "-a"}, output: ""},
	)

	docker := newFakeDocker(
		fakeDockerCall{argsContain: []string{"buildx", "imagetools", "inspect"}, output: "Digest: sha256:1111222233334444555566667777888899990000aaaa11112222333344445555"},
	)

	fs := newFakeFS()

	return bazel, git, docker, fs, workspaceRoot
}

func TestExecuteReleaseCharts_DryRun(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}
	artClient := NewFakeArtifactRegistryClient()

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:         "helm-demo-hello-fastapi",
		Version:        "v0.2.0",
		BuildID:        "build-123",
		ChartRepoURL:   "https://charts.whalenet.dev",
		DryRun:         true,
		Bazel:          bazel,
		Git:            git,
		Docker:         docker,
		FS:             fs,
		Packager:       packager,
		Uploader:       uploader,
		WorkspaceRoot:  workspaceRoot,
		ArtifactClient: artClient,
	})
	if err != nil {
		t.Fatalf("unexpected error in dry run: %v", err)
	}

	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart result, got %d", len(res.Charts))
	}
	c := res.Charts[0]
	if c.ChartName != "helm-demo-hello-fastapi" {
		t.Errorf("expected chart name 'helm-demo-hello-fastapi', got %q", c.ChartName)
	}
	if c.Published {
		t.Errorf("expected published false in dry run, got true")
	}
	if packager.calls != 0 {
		t.Errorf("expected 0 packager calls in dry run, got %d", packager.calls)
	}
	if uploader.uploadCalls != 0 {
		t.Errorf("expected 0 upload calls in dry run, got %d", uploader.uploadCalls)
	}
	if len(artClient.BeginPublishCalls) != 0 || len(artClient.RecordArtifactCalls) != 0 {
		t.Errorf("expected 0 registry mutations in dry run")
	}
}

func TestExecuteReleaseCharts_HermeticityViolation(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}
	artClient := NewFakeArtifactRegistryClient()

	fakeHermeticity := &fakeHermeticityChecker{
		enforced: true,
		violations: []ChartPinViolation{
			{AppFullName: "demo-hello-fastapi", Version: "v1.0.0", Reason: "version not in registry"},
		},
	}

	withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
		_, err := ExecuteReleaseCharts(ReleaseChartsParams{
			Charts:         "helm-demo-hello-fastapi",
			Version:        "v0.2.0",
			BuildID:        "build-123",
			ChartRepoURL:   "https://charts.whalenet.dev",
			Bazel:          bazel,
			Git:            git,
			Docker:         docker,
			FS:             fs,
			Packager:       packager,
			Uploader:       uploader,
			Hermeticity:    fakeHermeticity,
			WorkspaceRoot:  workspaceRoot,
			ArtifactClient: artClient,
		})
		if err == nil {
			t.Fatalf("expected error on hermeticity violation, got nil")
		}
		if !strings.Contains(err.Error(), "demo-hello-fastapi@v1.0.0") {
			t.Errorf("expected error to name violating app, got: %v", err)
		}
	})
}

func TestExecuteReleaseCharts_SuccessfulRelease(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}
	artClient := NewFakeArtifactRegistryClient()

	fakeHermeticity := &fakeHermeticityChecker{enforced: false}

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:               "demo-hello-fastapi",
		Version:              "v0.2.0",
		BuildID:              "build-999",
		ChartRepoURL:         "https://charts.whalenet.dev",
		IdempotencyKeyPrefix: "run-42-1",
		CreateGitTag:         true,
		Bazel:                bazel,
		Git:                  git,
		Docker:               docker,
		FS:                   fs,
		Packager:             packager,
		Uploader:             uploader,
		Hermeticity:          fakeHermeticity,
		WorkspaceRoot:        workspaceRoot,
		ArtifactClient:       artClient,
	})
	if err != nil {
		t.Fatalf("unexpected error in release-charts: %v", err)
	}

	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart result, got %d", len(res.Charts))
	}
	c := res.Charts[0]
	if !c.Published {
		t.Errorf("expected published true, got false")
	}
	if c.PublishedName != "demo-hello-fastapi" {
		t.Errorf("expected published name 'demo-hello-fastapi', got %q", c.PublishedName)
	}
	if uploader.uploadCalls != 1 {
		t.Errorf("expected 1 upload call, got %d", uploader.uploadCalls)
	}

	// Verify BeginPublish call
	if len(artClient.BeginPublishCalls) != 1 {
		t.Fatalf("expected 1 BeginPublish call, got %d", len(artClient.BeginPublishCalls))
	}
	beginReq := artClient.BeginPublishCalls[0]
	if beginReq.Kind != pb.ArtifactKind_ARTIFACT_KIND_CHART {
		t.Errorf("expected kind ARTIFACT_KIND_CHART, got %v", beginReq.Kind)
	}
	if beginReq.OwnerFullName != "demo-hello-fastapi" {
		t.Errorf("expected owner 'demo-hello-fastapi', got %q", beginReq.OwnerFullName)
	}
	if beginReq.Version != "v0.2.0" {
		t.Errorf("expected version 'v0.2.0', got %q", beginReq.Version)
	}
	if beginReq.BuildId != "build-999" {
		t.Errorf("expected buildId 'build-999', got %q", beginReq.BuildId)
	}
	if beginReq.IdempotencyKey != "run-42-1-demo-hello-fastapi-chart-begin" {
		t.Errorf("expected idempotency key 'run-42-1-demo-hello-fastapi-chart-begin', got %q", beginReq.IdempotencyKey)
	}

	// Verify RecordArtifact call
	if len(artClient.RecordArtifactCalls) != 1 {
		t.Fatalf("expected 1 RecordArtifact call, got %d", len(artClient.RecordArtifactCalls))
	}
	recReq := artClient.RecordArtifactCalls[0]
	if recReq.Kind != pb.ArtifactKind_ARTIFACT_KIND_CHART {
		t.Errorf("expected kind ARTIFACT_KIND_CHART, got %v", recReq.Kind)
	}
	if recReq.OwnerFullName != "demo-hello-fastapi" {
		t.Errorf("expected owner 'demo-hello-fastapi', got %q", recReq.OwnerFullName)
	}
	if recReq.Version != "v0.2.0" {
		t.Errorf("expected version 'v0.2.0', got %q", recReq.Version)
	}
	if recReq.Repository != "https://charts.whalenet.dev/demo-hello-fastapi" {
		t.Errorf("expected repository 'https://charts.whalenet.dev/demo-hello-fastapi', got %q", recReq.Repository)
	}
	if recReq.IdempotencyKey != "run-42-1-demo-hello-fastapi-chart-record" {
		t.Errorf("expected idempotency key 'run-42-1-demo-hello-fastapi-chart-record', got %q", recReq.IdempotencyKey)
	}
	if len(recReq.Contains) != 1 {
		t.Fatalf("expected 1 contained image, got %d", len(recReq.Contains))
	}
	cont := recReq.Contains[0]
	if cont.AppFullName != "demo-hello-fastapi" {
		t.Errorf("expected contained app 'demo-hello-fastapi', got %q", cont.AppFullName)
	}
	if cont.Digest != "sha256:1111222233334444555566667777888899990000aaaa11112222333344445555" {
		t.Errorf("expected resolved image digest, got %q", cont.Digest)
	}
}

// TestExecuteReleaseCharts_AppVersionsPlanPinTakesPrecedence is issue #901's
// end-to-end (ExecuteReleaseCharts, not just resolveChartAppVersions
// directly) red/green case: ReleaseChartsParams.AppVersions, when it
// carries the release batch's already-resolved plan version for a
// composed member app, must reach the packager verbatim -- overriding the
// git-tag fixture ("demo-hello-fastapi.v1.0.0") that would otherwise be
// used.
func TestExecuteReleaseCharts_AppVersionsPlanPinTakesPrecedence(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}
	artClient := NewFakeArtifactRegistryClient()
	fakeHermeticity := &fakeHermeticityChecker{enforced: false}

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:               "demo-hello-fastapi",
		Version:              "v0.2.0",
		BuildID:              "build-999",
		ChartRepoURL:         "https://charts.whalenet.dev",
		IdempotencyKeyPrefix: "run-42-1",
		AppVersions: map[string]string{
			"demo-hello-fastapi": "v9.9.9", // this batch's resolved plan version -- must win over the v1.0.0 git tag
		},
		Bazel:          bazel,
		Git:            git,
		Docker:         docker,
		FS:             fs,
		Packager:       packager,
		Uploader:       uploader,
		Hermeticity:    fakeHermeticity,
		WorkspaceRoot:  workspaceRoot,
		ArtifactClient: artClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart result, got %d", len(res.Charts))
	}
	if got := packager.lastAppVersions["demo-hello-fastapi"]; got != "v9.9.9" {
		t.Errorf("expected packager to receive plan-pinned app version v9.9.9, got %q (full appVersions: %v)", got, packager.lastAppVersions)
	}
}

func TestExecuteReleaseCharts_UploadFailure_CallsFailPublish(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{uploadErr: fmt.Errorf("connection refused")}
	packager := &fakeHelmPackager{}
	artClient := NewFakeArtifactRegistryClient()

	_, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:               "demo-hello-fastapi",
		Version:              "v0.2.0",
		BuildID:              "build-999",
		ChartRepoURL:         "https://charts.whalenet.dev",
		IdempotencyKeyPrefix: "run-42-1",
		Bazel:                bazel,
		Git:                  git,
		Docker:               docker,
		FS:                   fs,
		Packager:             packager,
		Uploader:             uploader,
		WorkspaceRoot:        workspaceRoot,
		ArtifactClient:       artClient,
	})
	if err == nil {
		t.Fatalf("expected error on upload failure, got nil")
	}

	if len(artClient.BeginPublishCalls) != 1 {
		t.Fatalf("expected 1 BeginPublish call, got %d", len(artClient.BeginPublishCalls))
	}
	if len(artClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call after upload error, got %d", len(artClient.FailPublishCalls))
	}
	failReq := artClient.FailPublishCalls[0]
	if failReq.Kind != pb.ArtifactKind_ARTIFACT_KIND_CHART {
		t.Errorf("expected kind ARTIFACT_KIND_CHART, got %v", failReq.Kind)
	}
	if failReq.OwnerFullName != "demo-hello-fastapi" {
		t.Errorf("expected owner 'demo-hello-fastapi', got %q", failReq.OwnerFullName)
	}
	if failReq.IdempotencyKey != "run-42-1-demo-hello-fastapi-chart-fail" {
		t.Errorf("expected idempotency key 'run-42-1-demo-hello-fastapi-chart-fail', got %q", failReq.IdempotencyKey)
	}
	if !strings.Contains(failReq.Reason, "connection refused") {
		t.Errorf("expected failure reason to contain error details, got: %q", failReq.Reason)
	}
}

func TestExecuteReleaseCharts_NoOpRebuild(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	packager := &fakeHelmPackager{}
	artClient := NewFakeArtifactRegistryClient()

	// Fake previous chart data with the exact same bytes ("fake-chart-bytes-v1")
	prevBytes := []byte("fake-chart-bytes-v1")
	uploader := &fakeChartUploader{fetchedChartData: prevBytes}

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:               "demo-hello-fastapi",
		Version:              "v0.2.0",
		BuildID:              "build-999",
		ChartRepoURL:         "https://charts.whalenet.dev",
		IdempotencyKeyPrefix: "run-42-1",
		Bazel:                bazel,
		Git:                  git,
		Docker:               docker,
		FS:                   fs,
		Packager:             packager,
		Uploader:             uploader,
		WorkspaceRoot:        workspaceRoot,
		ArtifactClient:       artClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart result, got %d", len(res.Charts))
	}
	c := res.Charts[0]
	if !c.DigestUnchanged {
		t.Errorf("expected DigestUnchanged true, got false")
	}
	if c.Published {
		t.Errorf("expected Published false for no-op rebuild, got true")
	}
	if uploader.uploadCalls != 0 {
		t.Errorf("expected 0 uploads on no-op rebuild, got %d", uploader.uploadCalls)
	}
	if len(artClient.BeginPublishCalls) != 1 {
		t.Errorf("expected 1 BeginPublish call on no-op rebuild, got %d", len(artClient.BeginPublishCalls))
	}
	if len(artClient.FailPublishCalls) != 1 {
		t.Errorf("expected 1 FailPublish call on no-op rebuild, got %d", len(artClient.FailPublishCalls))
	}
	if len(artClient.RecordArtifactCalls) != 0 {
		t.Errorf("expected 0 RecordArtifact calls on no-op rebuild, got %d", len(artClient.RecordArtifactCalls))
	}
}

func TestFilterHelmCharts(t *testing.T) {
	charts := []HelmChartMetadata{
		{ChartManifest: &appmetapb.ChartManifest{Name: "helm-demo-hello-fastapi", Domain: "demo"}},
		{ChartManifest: &appmetapb.ChartManifest{Name: "helm-manmanv2-web", Domain: "manmanv2"}},
		{ChartManifest: &appmetapb.ChartManifest{Name: "helm-manmanv2-api", Domain: "manmanv2"}},
	}

	// All without demo
	res1 := filterHelmCharts("all", charts, false)
	if len(res1) != 2 {
		t.Errorf("expected 2 charts for 'all' without demo, got %d", len(res1))
	}

	// All with demo
	res2 := filterHelmCharts("all", charts, true)
	if len(res2) != 3 {
		t.Errorf("expected 3 charts for 'all' with demo, got %d", len(res2))
	}

	// By domain
	res3 := filterHelmCharts("manmanv2", charts, false)
	if len(res3) != 2 {
		t.Errorf("expected 2 charts for 'manmanv2', got %d", len(res3))
	}

	// By published name
	res4 := filterHelmCharts("demo-hello-fastapi", charts, false)
	if len(res4) != 1 {
		t.Errorf("expected 1 chart for 'demo-hello-fastapi', got %d", len(res4))
	}
}

func TestExecuteReleaseCharts_LockfileContainedImages(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}
	artClient := NewFakeArtifactRegistryClient()

	// Write image-lockfile.json in chart directory
	chartDir := filepath.Join(workspaceRoot, "bazel-bin", "demo", "helm-demo-hello-fastapi_chart", "hello-fastapi")
	_ = os.MkdirAll(chartDir, 0755)
	lockfileJSON := `{
		"chart_name": "hello-fastapi",
		"images": [
			{
				"app_full_name": "demo-hello-fastapi",
				"domain": "demo",
				"name": "hello-fastapi",
				"repository": "ghcr.io/whale-net/demo-hello-fastapi",
				"version": "latest"
			}
		]
	}`
	_ = os.WriteFile(filepath.Join(chartDir, "image-lockfile.json"), []byte(lockfileJSON), 0644)

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:               "demo-hello-fastapi",
		Version:              "v0.2.0",
		BuildID:              "build-999",
		ChartRepoURL:         "https://charts.whalenet.dev",
		IdempotencyKeyPrefix: "run-42-1",
		Bazel:                bazel,
		Git:                  git,
		Docker:               docker,
		FS:                   fs,
		Packager:             packager,
		Uploader:             uploader,
		WorkspaceRoot:        workspaceRoot,
		ArtifactClient:       artClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart, got %d", len(res.Charts))
	}
	if len(res.Charts[0].ContainedImages) != 1 {
		t.Fatalf("expected 1 contained image, got %d", len(res.Charts[0].ContainedImages))
	}
	img := res.Charts[0].ContainedImages[0]
	if img.AppFullName != "demo-hello-fastapi" {
		t.Errorf("expected app full name 'demo-hello-fastapi', got %q", img.AppFullName)
	}
	if img.Version != "v1.0.0" {
		t.Errorf("expected version resolved to released app version 'v1.0.0', got %q", img.Version)
	}
}

func TestReleaseChartsCmd_CLIExecution(t *testing.T) {
	_, stderr, err := runTest([]string{"release-charts", "--help"})
	if err != nil {
		t.Fatalf("unexpected error running release-charts --help: %v (stderr: %s)", err, stderr)
	}
}

// TestReleaseChartsCmd_AppVersionsFlag_MalformedJSON proves --app-versions
// (issue #901) is parsed eagerly with a clear error, rather than silently
// swallowing a malformed value that would otherwise leave AppVersions nil
// and mask the caller's mistake.
func TestReleaseChartsCmd_AppVersionsFlag_MalformedJSON(t *testing.T) {
	_, stderr, err := runTest([]string{
		"release-charts",
		"--charts", "demo-hello-fastapi",
		"--chart-repo-url", "https://charts.whalenet.dev",
		"--dry-run",
		"--app-versions", "{not valid json",
	})
	if err == nil {
		t.Fatal("expected error for malformed --app-versions JSON, got nil")
	}
	if !strings.Contains(err.Error(), "--app-versions") && !strings.Contains(stderr, "--app-versions") {
		t.Errorf("expected error to mention --app-versions, got err=%v stderr=%q", err, stderr)
	}
}

func TestExecuteReleaseCharts_HermeticityRegistryError_Proceeds(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}
	artClient := NewFakeArtifactRegistryClient()

	fakeHermeticity := &fakeHermeticityChecker{
		err: fmt.Errorf("registry connection refused"),
	}

	withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
		res, err := ExecuteReleaseCharts(ReleaseChartsParams{
			Charts:         "demo-hello-fastapi",
			Version:        "v0.2.0",
			BuildID:        "build-999",
			ChartRepoURL:   "https://charts.whalenet.dev",
			Bazel:          bazel,
			Git:            git,
			Docker:         docker,
			FS:             fs,
			Packager:       packager,
			Uploader:       uploader,
			Hermeticity:    fakeHermeticity,
			WorkspaceRoot:  workspaceRoot,
			ArtifactClient: artClient,
		})
		if err != nil {
			t.Fatalf("expected release to proceed despite hermeticity registry error, got: %v", err)
		}
		if len(res.Charts) != 1 || !res.Charts[0].Published {
			t.Errorf("expected chart to be published, got %+v", res.Charts)
		}
	})
}

func TestExecuteReleaseCharts_BazelBuildFailure(t *testing.T) {
	_, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}

	chartQueryOutput := "//demo:hello-fastapi_chart_metadata"
	chartCqueryOutput := "//demo:hello-fastapi_chart_metadata\t{\"name\":\"helm-demo-hello-fastapi\",\"domain\":\"demo\",\"apps\":[\"hello-fastapi\"],\"namespace\":\"demo\",\"environment\":\"production\"}"
	appQueryOutput := "//demo/hello_fastapi:hello-fastapi_metadata"
	appCqueryOutput := "//demo/hello_fastapi:hello-fastapi_metadata\t" + string(sampleMetaJSON("hello-fastapi", "demo"))

	failingBazel := newFakeBazel(
		fakeBazelCall{argsContain: []string{"query", "kind(helm_chart_metadata"}, output: chartQueryOutput},
		fakeBazelCall{argsContain: []string{"cquery", "hello-fastapi_chart_metadata"}, output: chartCqueryOutput},
		fakeBazelCall{argsContain: []string{"query", "kind(app_metadata"}, output: appQueryOutput},
		fakeBazelCall{argsContain: []string{"cquery", "hello-fastapi_metadata"}, output: appCqueryOutput},
		fakeBazelCall{argsContain: []string{"build"}, err: fmt.Errorf("helm_chart target build failed")},
	)

	_, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:        "demo-hello-fastapi",
		Version:       "v0.2.0",
		ChartRepoURL:  "https://charts.whalenet.dev",
		Bazel:         failingBazel,
		Git:           git,
		Docker:        docker,
		FS:            fs,
		Packager:      packager,
		Uploader:      uploader,
		WorkspaceRoot: workspaceRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "helm_chart target build failed") {
		t.Errorf("expected bazel build error, got: %v", err)
	}
	if packager.calls != 0 {
		t.Errorf("expected 0 packager calls on build failure, got %d", packager.calls)
	}
}

func TestExecuteReleaseCharts_PackagerFailure(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	failingPackager := &fakeHelmPackager{packageErr: fmt.Errorf("chart packaging error")}

	_, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:        "demo-hello-fastapi",
		Version:       "v0.2.0",
		ChartRepoURL:  "https://charts.whalenet.dev",
		Bazel:         bazel,
		Git:           git,
		Docker:        docker,
		FS:            fs,
		Packager:      failingPackager,
		Uploader:      uploader,
		WorkspaceRoot: workspaceRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "chart packaging error") {
		t.Errorf("expected packager error, got: %v", err)
	}
	if uploader.uploadCalls != 0 {
		t.Errorf("expected 0 uploader calls on packaging failure, got %d", uploader.uploadCalls)
	}
}

func TestExecuteReleaseCharts_NonExistentChart(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}

	_, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:        "nonexistent-chart-xyz",
		Version:       "v0.2.0",
		ChartRepoURL:  "https://charts.whalenet.dev",
		Bazel:         bazel,
		Git:           git,
		Docker:        docker,
		FS:            fs,
		Packager:      packager,
		Uploader:      uploader,
		WorkspaceRoot: workspaceRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "no helm charts matched") {
		t.Errorf("expected no matched charts error, got: %v", err)
	}
}

func TestExecuteReleaseCharts_RecordArtifactFails_TriggersFailPublish(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}
	fakeArtifactClient := NewFakeArtifactRegistryClient()
	fakeArtifactClient.RecordArtifactFn = func(ctx context.Context, in *pb.RecordArtifactRequest, opts ...grpc.CallOption) (*pb.RecordArtifactResponse, error) {
		return nil, fmt.Errorf("chart pins unrecorded image digest")
	}

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:         "demo-hello-fastapi",
		Version:        "v0.2.0",
		ChartRepoURL:   "https://charts.whalenet.dev",
		BuildID:        "test-build-id-123",
		Bazel:          bazel,
		Git:            git,
		Docker:         docker,
		FS:             fs,
		Packager:       packager,
		Uploader:       uploader,
		WorkspaceRoot:  workspaceRoot,
		ArtifactClient: fakeArtifactClient,
	})
	if err != nil {
		t.Fatalf("unexpected ExecuteReleaseCharts error: %v", err)
	}
	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart result, got %d", len(res.Charts))
	}
	if len(fakeArtifactClient.BeginPublishCalls) != 1 {
		t.Errorf("expected 1 BeginPublish call, got %d", len(fakeArtifactClient.BeginPublishCalls))
	}
	if len(fakeArtifactClient.RecordArtifactCalls) != 1 {
		t.Errorf("expected 1 RecordArtifact call, got %d", len(fakeArtifactClient.RecordArtifactCalls))
	}
	if len(fakeArtifactClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call on RecordArtifact error, got %d", len(fakeArtifactClient.FailPublishCalls))
	}
	if !strings.Contains(fakeArtifactClient.FailPublishCalls[0].Reason, "chart record artifact failed") {
		t.Errorf("expected fail reason to mention chart record artifact failed, got: %s", fakeArtifactClient.FailPublishCalls[0].Reason)
	}
}

func TestExecuteReleaseCharts_AutoIncrementMinor(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:         "helm-demo-hello-fastapi",
		IncrementMinor: true,
		ChartRepoURL:   "https://charts.whalenet.dev",
		DryRun:         true,
		Bazel:          bazel,
		Git:            git,
		Docker:         docker,
		FS:             fs,
		Packager:       packager,
		Uploader:       uploader,
		WorkspaceRoot:  workspaceRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error in dry run: %v", err)
	}

	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart result, got %d", len(res.Charts))
	}
	// Initial tag fixture was helm-demo-hello-fastapi.v0.1.0 -> minor bump should be v0.2.0
	if res.Charts[0].EffectiveVersion != "v0.2.0" {
		t.Errorf("expected EffectiveVersion 'v0.2.0', got %q", res.Charts[0].EffectiveVersion)
	}
}

func TestExecuteReleaseCharts_AutoIncrementMajor(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:         "helm-demo-hello-fastapi",
		IncrementMajor: true,
		ChartRepoURL:   "https://charts.whalenet.dev",
		DryRun:         true,
		Bazel:          bazel,
		Git:            git,
		Docker:         docker,
		FS:             fs,
		Packager:       packager,
		Uploader:       uploader,
		WorkspaceRoot:  workspaceRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error in dry run: %v", err)
	}

	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart result, got %d", len(res.Charts))
	}
	// Initial tag fixture was helm-demo-hello-fastapi.v0.1.0 -> major bump should be v1.0.0
	if res.Charts[0].EffectiveVersion != "v1.0.0" {
		t.Errorf("expected EffectiveVersion 'v1.0.0', got %q", res.Charts[0].EffectiveVersion)
	}
}

// TestExecuteReleaseCharts_AllocateDomainUsesRegistryNotTags is issue #829's
// fix for the real production chart call site: with the domain opted into
// App Registry and AllocateVersion succeeding, the allocated version must be
// used verbatim -- ignoring the git-tag fixture entirely (which would have
// produced v0.2.0 via a minor bump from helm-demo-hello-fastapi.v0.1.0).
func TestExecuteReleaseCharts_AllocateDomainUsesRegistryNotTags(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}
	artClient := NewFakeArtifactRegistryClient()
	artClient.AllocateVersionFn = func(ctx context.Context, in *pb.AllocateVersionRequest, opts ...grpc.CallOption) (*pb.AllocateVersionResponse, error) {
		if in.OwnerFullName != "demo-hello-fastapi" || in.Kind != pb.ArtifactKind_ARTIFACT_KIND_CHART || in.Increment != "minor" {
			t.Errorf("unexpected AllocateVersionRequest: %+v", in)
		}
		return &pb.AllocateVersionResponse{Version: "v5.0.0"}, nil
	}
	fakeHermeticity := &fakeHermeticityChecker{enforced: false}

	var res *ReleaseChartsResult
	var err error
	withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
		res, err = ExecuteReleaseCharts(ReleaseChartsParams{
			Charts:               "demo-hello-fastapi",
			IncrementMinor:       true,
			BuildID:              "build-999",
			ChartRepoURL:         "https://charts.whalenet.dev",
			IdempotencyKeyPrefix: "run-42-1",
			Bazel:                bazel,
			Git:                  git,
			Docker:               docker,
			FS:                   fs,
			Packager:             packager,
			Uploader:             uploader,
			Hermeticity:          fakeHermeticity,
			WorkspaceRoot:        workspaceRoot,
			ArtifactClient:       artClient,
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(artClient.AllocateVersionCalls) != 1 {
		t.Fatalf("expected 1 AllocateVersion call, got %d", len(artClient.AllocateVersionCalls))
	}
	if len(res.Charts) != 1 || res.Charts[0].EffectiveVersion != "v5.0.0" {
		t.Fatalf("expected registry-allocated version 'v5.0.0', got %+v", res.Charts)
	}
}

// TestExecuteReleaseCharts_NotAllocatedFallsBackToTags proves domains not yet
// cut over to adoption stage "allocate" are unaffected: AllocateVersion's
// FailedPrecondition falls back to the pre-#829 tag-based bump, unchanged.
func TestExecuteReleaseCharts_NotAllocatedFallsBackToTags(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}
	artClient := NewFakeArtifactRegistryClient()
	artClient.AllocateVersionFn = func(ctx context.Context, in *pb.AllocateVersionRequest, opts ...grpc.CallOption) (*pb.AllocateVersionResponse, error) {
		return nil, status.Error(codes.FailedPrecondition, `domain "demo" is at adoption stage "observe"`)
	}
	fakeHermeticity := &fakeHermeticityChecker{enforced: false}

	var res *ReleaseChartsResult
	var err error
	withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
		res, err = ExecuteReleaseCharts(ReleaseChartsParams{
			Charts:               "demo-hello-fastapi",
			IncrementMinor:       true,
			BuildID:              "build-999",
			ChartRepoURL:         "https://charts.whalenet.dev",
			IdempotencyKeyPrefix: "run-42-1",
			Bazel:                bazel,
			Git:                  git,
			Docker:               docker,
			FS:                   fs,
			Packager:             packager,
			Uploader:             uploader,
			Hermeticity:          fakeHermeticity,
			WorkspaceRoot:        workspaceRoot,
			ArtifactClient:       artClient,
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Initial tag fixture was helm-demo-hello-fastapi.v0.1.0 -> minor bump should be v0.2.0
	if len(res.Charts) != 1 || res.Charts[0].EffectiveVersion != "v0.2.0" {
		t.Fatalf("expected tag-based fallback version 'v0.2.0', got %+v", res.Charts)
	}
}

// TestExecuteReleaseCharts_DryRunNeverCallsAllocateVersion proves a dry run
// never reserves a real version: AllocateVersion has a write side effect (it
// inserts an "allocated" artifact row), unlike the read-only hermeticity
// check, so it must stay off even when opted in with a live client.
func TestExecuteReleaseCharts_DryRunNeverCallsAllocateVersion(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}
	artClient := NewFakeArtifactRegistryClient()
	fakeHermeticity := &fakeHermeticityChecker{enforced: false}

	withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
		_, err := ExecuteReleaseCharts(ReleaseChartsParams{
			Charts:         "demo-hello-fastapi",
			IncrementMinor: true,
			ChartRepoURL:   "https://charts.whalenet.dev",
			DryRun:         true,
			Bazel:          bazel,
			Git:            git,
			Docker:         docker,
			FS:             fs,
			Packager:       packager,
			Uploader:       uploader,
			Hermeticity:    fakeHermeticity,
			WorkspaceRoot:  workspaceRoot,
			ArtifactClient: artClient,
		})
		if err != nil {
			t.Fatalf("unexpected error in dry run: %v", err)
		}
	})
	if len(artClient.AllocateVersionCalls) != 0 {
		t.Errorf("expected AllocateVersion to never be called during a dry run, got %d calls", len(artClient.AllocateVersionCalls))
	}
}

// TestExecuteReleaseCharts_RetryAfterFailureReusesCandidateVersion locks in
// the safety property behind #814: when a prior run failed before anything
// was actually published (no git tag created -- tags are only created after
// a successful ChartMuseum upload -- and no chart present at the candidate
// version in ChartMuseum), the next run's freshly auto-incremented candidate
// must be used as-is rather than advanced past. autoIncrementHelmVersion
// always recomputes from git tags, so a retry naturally proposes the same
// candidate the failed run would have used; this test asserts the
// orphan-collision check (release_charts.go's FetchChart-at-candidate
// branch) leaves that candidate alone when ChartMuseum has nothing there,
// and does not advance the version or skip the publish.
func TestExecuteReleaseCharts_RetryAfterFailureReusesCandidateVersion(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	packager := &fakeHelmPackager{}
	artClient := NewFakeArtifactRegistryClient()

	// Nothing exists at the candidate version yet -- simulates a retry after
	// a run that failed before ever reaching the ChartMuseum upload step.
	uploader := &fakeChartUploader{fetchErr: fmt.Errorf("404 not found")}

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:               "helm-demo-hello-fastapi",
		IncrementPatch:       true,
		BuildID:              "build-999",
		ChartRepoURL:         "https://charts.whalenet.dev",
		IdempotencyKeyPrefix: "run-42-2",
		Bazel:                bazel,
		Git:                  git,
		Docker:               docker,
		FS:                   fs,
		Packager:             packager,
		Uploader:             uploader,
		WorkspaceRoot:        workspaceRoot,
		ArtifactClient:       artClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart result, got %d", len(res.Charts))
	}

	c := res.Charts[0]
	// Initial tag fixture is helm-demo-hello-fastapi.v0.1.0 -> patch bump
	// (the same candidate a failed run and its retry both compute) is
	// v0.1.1. It must NOT have been advanced past (e.g. to v0.1.2) by the
	// orphan-collision logic, since nothing was actually published at v0.1.1.
	if c.EffectiveVersion != "v0.1.1" {
		t.Errorf("expected candidate version to be reused as 'v0.1.1' (no orphan advance), got %q", c.EffectiveVersion)
	}
	if !c.Published {
		t.Errorf("expected the reused candidate to be published, got Published=false")
	}
	if uploader.uploadCalls != 1 {
		t.Errorf("expected exactly 1 upload at the reused candidate version, got %d", uploader.uploadCalls)
	}
}

// TestChartArchivesContentEqual_IdenticalContentDifferentBytes reproduces
// the exact #814 bug at the unit level: two packaging runs of the same
// unchanged chart source produce archives with identical decoded content
// but different raw bytes (different tar entry order, different gzip/tar
// timestamps -- exactly what non-deterministic `helm package` output looks
// like). chartArchivesContentEqual must treat them as equal.
func TestChartArchivesContentEqual_IdenticalContentDifferentBytes(t *testing.T) {
	files := map[string]string{
		"demo-hello-fastapi/Chart.yaml":  "apiVersion: v2\nname: demo-hello-fastapi\nversion: v0.1.1\n",
		"demo-hello-fastapi/values.yaml": "apps:\n  demo-hello-fastapi:\n    imageTag: v1.0.0\n",
	}

	archiveA := buildTestChartArchive(t, files,
		[]string{"demo-hello-fastapi/Chart.yaml", "demo-hello-fastapi/values.yaml"},
		time.Unix(0, 0), time.Unix(0, 0))
	archiveB := buildTestChartArchive(t, files,
		[]string{"demo-hello-fastapi/values.yaml", "demo-hello-fastapi/Chart.yaml"}, // reversed order
		time.Now(), time.Now()) // different timestamps

	if bytes.Equal(archiveA, archiveB) {
		t.Fatalf("test setup invalid: archives must differ at the raw byte level")
	}
	if !chartArchivesContentEqual(archiveA, archiveB) {
		t.Errorf("expected archives with identical decoded content but different raw bytes to compare equal")
	}
}

// TestChartArchivesContentEqual_RealContentChangeDetected confirms the
// widened comparison doesn't become a rubber stamp: an archive with a
// genuinely modified file must still be reported as different.
func TestChartArchivesContentEqual_RealContentChangeDetected(t *testing.T) {
	order := []string{"demo-hello-fastapi/Chart.yaml", "demo-hello-fastapi/values.yaml"}
	base := map[string]string{
		"demo-hello-fastapi/Chart.yaml":  "apiVersion: v2\nname: demo-hello-fastapi\nversion: v0.1.1\n",
		"demo-hello-fastapi/values.yaml": "apps:\n  demo-hello-fastapi:\n    imageTag: v1.0.0\n",
	}
	changed := map[string]string{
		"demo-hello-fastapi/Chart.yaml":  base["demo-hello-fastapi/Chart.yaml"],
		"demo-hello-fastapi/values.yaml": "apps:\n  demo-hello-fastapi:\n    imageTag: v1.0.1\n", // real change
	}

	archiveA := buildTestChartArchive(t, base, order, time.Unix(0, 0), time.Unix(0, 0))
	archiveB := buildTestChartArchive(t, changed, order, time.Unix(0, 0), time.Unix(0, 0))

	if chartArchivesContentEqual(archiveA, archiveB) {
		t.Errorf("expected archives with genuinely different file content to compare unequal")
	}
}

// TestExecuteReleaseCharts_NonDeterministicRepackageStillReusesVersion is the
// end-to-end reproduction of #814: ChartMuseum already holds a chart at the
// candidate version, packaged in a prior (failed/retried) run. The freshly
// repackaged chart has identical decoded content but different raw tarball
// bytes (as real `helm package` invocations produce). The release must
// recognize this as a no-op and must NOT advance to a new orphaned version.
func TestExecuteReleaseCharts_NonDeterministicRepackageStillReusesVersion(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	artClient := NewFakeArtifactRegistryClient()

	files := map[string]string{
		"demo-hello-fastapi/Chart.yaml":  "apiVersion: v2\nname: demo-hello-fastapi\nversion: v0.1.1\n",
		"demo-hello-fastapi/values.yaml": "apps:\n  demo-hello-fastapi:\n    imageTag: v1.0.0\n",
	}
	// What ChartMuseum already has, published by an earlier run of this
	// exact chart content.
	publishedArchive := buildTestChartArchive(t, files,
		[]string{"demo-hello-fastapi/Chart.yaml", "demo-hello-fastapi/values.yaml"},
		time.Unix(0, 0), time.Unix(0, 0))
	// What this run's fresh `helm package` invocation produces for the same,
	// unchanged chart source: identical content, different bytes.
	repackagedArchive := buildTestChartArchive(t, files,
		[]string{"demo-hello-fastapi/values.yaml", "demo-hello-fastapi/Chart.yaml"},
		time.Now(), time.Now())

	packager := &fakeHelmPackager{content: repackagedArchive}
	uploader := &versionedFakeUploader{data: map[string][]byte{
		"v0.1.1": publishedArchive,
	}}

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:               "helm-demo-hello-fastapi",
		IncrementPatch:       true,
		BuildID:              "build-999",
		ChartRepoURL:         "https://charts.whalenet.dev",
		IdempotencyKeyPrefix: "run-42-3",
		Bazel:                bazel,
		Git:                  git,
		Docker:               docker,
		FS:                   fs,
		Packager:             packager,
		Uploader:             uploader,
		WorkspaceRoot:        workspaceRoot,
		ArtifactClient:       artClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart result, got %d", len(res.Charts))
	}

	c := res.Charts[0]
	if c.EffectiveVersion != "v0.1.1" {
		t.Errorf("expected version to be reused as 'v0.1.1' (no orphan advance from non-deterministic repackage), got %q", c.EffectiveVersion)
	}
	if !c.DigestUnchanged {
		t.Errorf("expected DigestUnchanged true for semantically-identical repackage, got false")
	}
	if c.Published {
		t.Errorf("expected Published false for no-op repackage, got true")
	}
	if uploader.uploadCalls != 0 {
		t.Errorf("expected no upload for a no-op repackage, got %d", uploader.uploadCalls)
	}
}

// TestExecuteReleaseCharts_RealContentChangeStillAdvancesVersion confirms
// the widened no-op detection doesn't break legitimate collision handling:
// when ChartMuseum holds genuinely different content at the candidate
// version (a real orphaned upload, not just non-deterministic repackaging),
// the release must still advance past it and publish at a new version.
func TestExecuteReleaseCharts_RealContentChangeStillAdvancesVersion(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	artClient := NewFakeArtifactRegistryClient()

	order := []string{"demo-hello-fastapi/Chart.yaml", "demo-hello-fastapi/values.yaml"}
	orphaned := map[string]string{
		"demo-hello-fastapi/Chart.yaml":  "apiVersion: v2\nname: demo-hello-fastapi\nversion: v0.1.1\n",
		"demo-hello-fastapi/values.yaml": "apps:\n  demo-hello-fastapi:\n    imageTag: v0.9.0\n", // different real content
	}
	fresh := map[string]string{
		"demo-hello-fastapi/Chart.yaml":  "apiVersion: v2\nname: demo-hello-fastapi\nversion: v0.1.1\n",
		"demo-hello-fastapi/values.yaml": "apps:\n  demo-hello-fastapi:\n    imageTag: v1.0.0\n",
	}

	orphanedArchive := buildTestChartArchive(t, orphaned, order, time.Unix(0, 0), time.Unix(0, 0))
	freshArchive := buildTestChartArchive(t, fresh, order, time.Unix(0, 0), time.Unix(0, 0))

	packager := &fakeHelmPackager{content: freshArchive}
	uploader := &versionedFakeUploader{data: map[string][]byte{
		// v0.1.1 (the natural patch-bump candidate) is occupied by
		// genuinely different, orphaned content; v0.1.2 is free.
		"v0.1.1": orphanedArchive,
	}}

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:               "helm-demo-hello-fastapi",
		IncrementPatch:       true,
		BuildID:              "build-999",
		ChartRepoURL:         "https://charts.whalenet.dev",
		IdempotencyKeyPrefix: "run-42-4",
		Bazel:                bazel,
		Git:                  git,
		Docker:               docker,
		FS:                   fs,
		Packager:             packager,
		Uploader:             uploader,
		WorkspaceRoot:        workspaceRoot,
		ArtifactClient:       artClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart result, got %d", len(res.Charts))
	}

	c := res.Charts[0]
	if c.EffectiveVersion != "v0.1.2" {
		t.Errorf("expected version to advance to 'v0.1.2' for genuine content collision, got %q", c.EffectiveVersion)
	}
	if c.DigestUnchanged {
		t.Errorf("expected DigestUnchanged false for genuine content collision, got true")
	}
	if !c.Published {
		t.Errorf("expected Published true after advancing past real collision, got false")
	}
	if uploader.uploadCalls != 1 {
		t.Errorf("expected exactly 1 upload at the advanced version, got %d", uploader.uploadCalls)
	}
}

// TestExecuteReleaseCharts_MinorBumpWithSharedDigestNotCollapsed guards
// against a specific regression on top of #814: the "no-op rebuild against
// previous tag" check (release_charts.go's prevTag block, just below the
// orphan-collision loop) must not silently collapse an explicit minor bump
// back onto the previous (lower-minor) version just because the freshly
// packaged chart happens to produce identical content. Before this fix, the
// check only compared digests -- an intentional `IncrementMinor` release
// whose rendered chart content is unchanged from the prior minor release
// would be discarded entirely, reusing the old version and never
// tagging/publishing/recording the new one. This mirrors the equivalent
// major/minor gate already present in release_app.go for images and
// binaries (see parseSemverTriple usage there).
func TestExecuteReleaseCharts_MinorBumpWithSharedDigestNotCollapsed(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	artClient := NewFakeArtifactRegistryClient()

	files := map[string]string{
		"demo-hello-fastapi/Chart.yaml":  "apiVersion: v2\nname: demo-hello-fastapi\nversion: v0.1.0\n",
		"demo-hello-fastapi/values.yaml": "apps:\n  demo-hello-fastapi:\n    imageTag: v1.0.0\n",
	}
	order := []string{"demo-hello-fastapi/Chart.yaml", "demo-hello-fastapi/values.yaml"}
	sharedArchive := buildTestChartArchive(t, files, order, time.Unix(0, 0), time.Unix(0, 0))

	packager := &fakeHelmPackager{content: sharedArchive}
	uploader := &versionedFakeUploader{data: map[string][]byte{
		// Fixture's previous chart tag is helm-demo-hello-fastapi.v0.1.0.
		// Nothing exists yet at the new minor candidate (v0.2.0) -- only the
		// packaged content, not the version, happens to be unchanged.
		"v0.1.0": sharedArchive,
	}}

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:               "helm-demo-hello-fastapi",
		IncrementMinor:       true,
		BuildID:              "build-999",
		ChartRepoURL:         "https://charts.whalenet.dev",
		IdempotencyKeyPrefix: "run-42-5",
		Bazel:                bazel,
		Git:                  git,
		Docker:               docker,
		FS:                   fs,
		Packager:             packager,
		Uploader:             uploader,
		WorkspaceRoot:        workspaceRoot,
		ArtifactClient:       artClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart result, got %d", len(res.Charts))
	}

	c := res.Charts[0]
	// Must NOT collapse back to v0.1.0 -- the explicit minor bump has to be
	// recorded and tagged as v0.2.0, even though its content digest matches
	// the previous minor release.
	if c.EffectiveVersion != "v0.2.0" {
		t.Errorf("expected explicit minor bump to be preserved as 'v0.2.0', got %q (collapsed back to previous version)", c.EffectiveVersion)
	}
	if !c.Published {
		t.Errorf("expected Published true -- a major/minor bump with a shared digest still establishes a new version baseline, got false")
	}
	if uploader.uploadCalls != 1 {
		t.Errorf("expected exactly 1 upload for the new minor version, got %d", uploader.uploadCalls)
	}
	if len(uploader.uploadedVer) != 1 || !strings.Contains(uploader.uploadedVer[0], "v0.2.0") {
		t.Errorf("expected upload at v0.2.0, got %v", uploader.uploadedVer)
	}
}

// TestExecuteReleaseCharts_SameMinorPatchRetryReusesPreviousVersion locks in
// the legitimate no-op case the major/minor gate above must not break: a
// same-minor patch-level retry (e.g. re-running release-charts after a
// transient failure, with no explicit version bump requested) whose
// repackaged content is unchanged from the immediately previous published
// version must still be recognized as a no-op and reuse that previous
// version, rather than tagging/publishing a new one.
func TestExecuteReleaseCharts_SameMinorPatchRetryReusesPreviousVersion(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	artClient := NewFakeArtifactRegistryClient()

	files := map[string]string{
		"demo-hello-fastapi/Chart.yaml":  "apiVersion: v2\nname: demo-hello-fastapi\nversion: v0.1.0\n",
		"demo-hello-fastapi/values.yaml": "apps:\n  demo-hello-fastapi:\n    imageTag: v1.0.0\n",
	}
	order := []string{"demo-hello-fastapi/Chart.yaml", "demo-hello-fastapi/values.yaml"}
	sharedArchive := buildTestChartArchive(t, files, order, time.Unix(0, 0), time.Unix(0, 0))

	packager := &fakeHelmPackager{content: sharedArchive}
	uploader := &versionedFakeUploader{data: map[string][]byte{
		// Fixture's previous chart tag is helm-demo-hello-fastapi.v0.1.0.
		// Nothing exists yet at the freshly computed patch candidate
		// (v0.1.1) -- this is a same-minor retry, not a real republish.
		"v0.1.0": sharedArchive,
	}}

	res, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:               "helm-demo-hello-fastapi",
		IncrementPatch:       true,
		BuildID:              "build-999",
		ChartRepoURL:         "https://charts.whalenet.dev",
		IdempotencyKeyPrefix: "run-42-6",
		Bazel:                bazel,
		Git:                  git,
		Docker:               docker,
		FS:                   fs,
		Packager:             packager,
		Uploader:             uploader,
		WorkspaceRoot:        workspaceRoot,
		ArtifactClient:       artClient,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Charts) != 1 {
		t.Fatalf("expected 1 chart result, got %d", len(res.Charts))
	}

	c := res.Charts[0]
	if !c.DigestUnchanged {
		t.Errorf("expected DigestUnchanged true for same-minor no-op retry, got false")
	}
	if c.EffectiveVersion != "v0.1.0" {
		t.Errorf("expected same-minor retry to reuse previous version 'v0.1.0', got %q", c.EffectiveVersion)
	}
	if c.Published {
		t.Errorf("expected Published false for no-op retry, got true")
	}
	if uploader.uploadCalls != 0 {
		t.Errorf("expected 0 uploads for no-op retry, got %d", uploader.uploadCalls)
	}
}

// TestExecuteReleaseCharts_AllocateStage_RecordArtifactFailureIsFatal proves
// that for a chart in an allocate-stage domain (issue #834), RecordArtifact
// failure calls FailPublish to clean up the publishing row AND returns a fatal error.
func TestExecuteReleaseCharts_AllocateStage_RecordArtifactFailureIsFatal(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}
	fakeArtifactClient := NewFakeArtifactRegistryClient()
	fakeArtifactClient.CheckChartHermeticityFn = func(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
		return &pb.CheckChartHermeticityResponse{Enforced: true}, nil
	}
	fakeArtifactClient.RecordArtifactFn = func(ctx context.Context, in *pb.RecordArtifactRequest, opts ...grpc.CallOption) (*pb.RecordArtifactResponse, error) {
		return nil, fmt.Errorf("database constraint violation")
	}

	_, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:         "demo-hello-fastapi",
		Version:        "v0.2.0",
		ChartRepoURL:   "https://charts.whalenet.dev",
		BuildID:        "test-build-id-123",
		Bazel:          bazel,
		Git:            git,
		Docker:         docker,
		FS:             fs,
		Packager:       packager,
		Uploader:       uploader,
		WorkspaceRoot:  workspaceRoot,
		ArtifactClient: fakeArtifactClient,
	})
	if err == nil {
		t.Fatal("expected fatal error for chart RecordArtifact failure on allocate domain, got nil")
	}
	if !strings.Contains(err.Error(), "RecordArtifact failed") {
		t.Errorf("expected error to mention RecordArtifact failed, got: %v", err)
	}
	if len(fakeArtifactClient.RecordArtifactCalls) != 1 {
		t.Errorf("expected 1 RecordArtifact call, got %d", len(fakeArtifactClient.RecordArtifactCalls))
	}
	if len(fakeArtifactClient.FailPublishCalls) != 1 {
		t.Fatalf("expected 1 FailPublish call on RecordArtifact failure, got %d", len(fakeArtifactClient.FailPublishCalls))
	}
}

// TestExecuteReleaseCharts_AllocateStage_BeginPublishFailureIsFatal proves
// that for a chart in an allocate-stage domain (issue #834), BeginPublish
// failure returns a fatal error and aborts chart release.
func TestExecuteReleaseCharts_AllocateStage_BeginPublishFailureIsFatal(t *testing.T) {
	bazel, git, docker, fs, workspaceRoot := setupChartTestFixtures(t)
	uploader := &fakeChartUploader{}
	packager := &fakeHelmPackager{}
	fakeArtifactClient := NewFakeArtifactRegistryClient()
	fakeArtifactClient.CheckChartHermeticityFn = func(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
		return &pb.CheckChartHermeticityResponse{Enforced: true}, nil
	}
	fakeArtifactClient.BeginPublishFn = func(ctx context.Context, in *pb.BeginPublishRequest, opts ...grpc.CallOption) (*pb.BeginPublishResponse, error) {
		return nil, fmt.Errorf("connection refused")
	}

	_, err := ExecuteReleaseCharts(ReleaseChartsParams{
		Charts:         "demo-hello-fastapi",
		Version:        "v0.2.0",
		ChartRepoURL:   "https://charts.whalenet.dev",
		BuildID:        "test-build-id-123",
		Bazel:          bazel,
		Git:            git,
		Docker:         docker,
		FS:             fs,
		Packager:       packager,
		Uploader:       uploader,
		WorkspaceRoot:  workspaceRoot,
		ArtifactClient: fakeArtifactClient,
	})
	if err == nil {
		t.Fatal("expected fatal error for chart BeginPublish failure on allocate domain, got nil")
	}
	if !strings.Contains(err.Error(), "BeginPublish failed") {
		t.Errorf("expected error to mention BeginPublish failed, got: %v", err)
	}
	if uploader.uploadCalls != 0 {
		t.Errorf("expected 0 uploads when BeginPublish fails, got %d", uploader.uploadCalls)
	}
}
