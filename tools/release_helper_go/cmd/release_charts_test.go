package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

type fakeHelmPackager struct {
	packagedPath string
	packageErr   error
	calls        int
}

func (f *fakeHelmPackager) Package(chartDir, chartName, version, outDir string, appVersions map[string]string) (string, error) {
	f.calls++
	if f.packageErr != nil {
		return "", f.packageErr
	}
	if f.packagedPath != "" {
		return f.packagedPath, nil
	}
	// Create dummy packaged file in outDir
	published := strings.TrimPrefix(chartName, "helm-")
	path := filepath.Join(outDir, fmt.Sprintf("%s-%s.tgz", published, version))
	if err := os.WriteFile(path, []byte("fake-chart-bytes-v1"), 0644); err != nil {
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
	if len(artClient.BeginPublishCalls) != 0 || len(artClient.RecordArtifactCalls) != 0 {
		t.Errorf("expected 0 registry mutations on no-op rebuild")
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


