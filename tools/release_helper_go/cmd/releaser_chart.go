package cmd

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// ChartReleaser implements ArtifactReleaser and ContainedImagesProvider for Helm charts.
type ChartReleaser struct {
	Chart         HelmChartMetadata
	AllApps       []AppMetadata
	RepoURL       string
	RepoUser      string
	RepoPass      string
	AppVersions   map[string]string
	WorkspaceRoot string
	Bazel         BazelRunner
	Docker        DockerRunner
	FS            FileSystem
	Packager      HelmPackager
	Uploader      ChartUploader
	OutDir        string

	// ChartDir, when non-empty, points at an already-built chart source
	// tree (Chart.yaml/values.yaml/templates/image-lockfile.json) to
	// package directly -- Build skips chartOutputPaths derivation and the
	// `bazel build` step entirely. Set by finalize-chart (issue #928): the
	// merged release-trigger GHA job builds this tree once (build-chart)
	// and ships it as a workflow artifact; the Temporal-driven finalize
	// step re-packages it here with the resolved final version/app
	// versions, without needing bazel or a monorepo checkout of its own.
	// Empty (the default) preserves the original "bazel build then
	// package" behavior release-charts still uses directly.
	ChartDir string
	// AppDigests, when non-empty, supplies each composed app's already-
	// resolved image digest (keyed by AppMetadata.FullName()), used by
	// Build to populate ContainedImages without a live `docker buildx
	// imagetools inspect` call -- see resolveContainedImagesFromDigests's
	// doc comment. Set by finalize-chart alongside ChartDir: at finalize
	// time the digests are already known from the build job's own
	// per-app manifest (a digest never changes across a GHCR retag), so
	// there is no need for the finalize environment to hold a `docker`
	// credential just to re-derive it.
	AppDigests map[string]string

	chartPath       string
	chartBytes      []byte
	chartDigest     string
	containedImages []*pb.ContainedImage
}

// Kind returns ARTIFACT_KIND_CHART.
func (r *ChartReleaser) Kind() pb.ArtifactKind {
	return pb.ArtifactKind_ARTIFACT_KIND_CHART
}

// Build packages the chart with HelmPackager and computes its content
// digest. When r.ChartDir is empty (the release-charts path), it first
// invokes Bazel to build the chart into chartOutputPaths' derived
// directory; when r.ChartDir is set (the finalize-chart path, issue #928),
// that already-built directory is packaged directly and no Bazel/git
// checkout is required.
func (r *ChartReleaser) Build(ctx context.Context, version string) (string, error) {
	chartDir := r.ChartDir
	if chartDir == "" {
		var chartTarget string
		chartTarget, chartDir = chartOutputPaths(r.WorkspaceRoot, r.Chart)
		fmt.Printf("Building bazel target: %s\n", chartTarget)
		// chartDir below is read straight off bazel-bin.
		if _, err := bazelRunToDisk(r.Bazel, "build", chartTarget); err != nil {
			return "", fmt.Errorf("bazel build %s: %w", chartTarget, err)
		}
	}

	outDir := r.OutDir
	if outDir == "" {
		var err error
		outDir, err = os.MkdirTemp("", "helm-release-*")
		if err != nil {
			return "", fmt.Errorf("create temp dir: %w", err)
		}
		r.OutDir = outDir
	}

	chartPath, err := r.Packager.Package(chartDir, r.Chart.Name, version, outDir, r.AppVersions)
	if err != nil {
		return "", fmt.Errorf("package chart %s: %w", r.Chart.Name, err)
	}
	r.chartPath = chartPath

	chartBytes, err := os.ReadFile(chartPath)
	if err != nil {
		return "", fmt.Errorf("read packaged chart %s: %w", chartPath, err)
	}
	r.chartBytes = chartBytes

	hasher := sha256.New()
	hasher.Write(chartBytes)
	r.chartDigest = fmt.Sprintf("sha256:%x", hasher.Sum(nil))

	if len(r.AppDigests) > 0 {
		r.containedImages = resolveContainedImagesFromDigests(chartDir, r.AppVersions, r.AppDigests, r.FS)
	} else {
		r.containedImages = resolveContainedImages(chartDir, r.AppVersions, r.Chart, r.AllApps, r.Docker, r.FS)
	}
	return r.chartDigest, nil
}

// FetchPublished fetches chart package bytes from ChartMuseum and compares content equality.
func (r *ChartReleaser) FetchPublished(ctx context.Context, version string) (bool, string, bool, error) {
	if r.Uploader == nil {
		return false, "", false, nil
	}
	publishedName := strings.TrimPrefix(r.Chart.Name, "helm-")
	data, err := r.Uploader.FetchChart(ctx, r.RepoURL, r.RepoUser, r.RepoPass, publishedName, version)
	if err != nil || len(data) == 0 {
		return false, "", false, nil
	}
	matches := chartArchivesContentEqual(r.chartBytes, data)
	hasher := sha256.New()
	hasher.Write(data)
	fetchedDigest := fmt.Sprintf("sha256:%x", hasher.Sum(nil))
	return true, fetchedDigest, matches, nil
}

// Publish uploads the packaged chart to ChartMuseum.
func (r *ChartReleaser) Publish(ctx context.Context, version string) error {
	if r.Uploader == nil {
		return nil
	}
	publishedName := strings.TrimPrefix(r.Chart.Name, "helm-")
	fmt.Printf("Uploading %s version %s to %s...\n", publishedName, version, r.RepoURL)
	if err := r.Uploader.UploadChart(ctx, r.RepoURL, r.RepoUser, r.RepoPass, r.chartPath); err != nil {
		return fmt.Errorf("upload chart %s to ChartMuseum: %w", publishedName, err)
	}
	return nil
}

// ContainedImages returns the resolved image pins bundled inside the chart.
func (r *ChartReleaser) ContainedImages() []*pb.ContainedImage {
	return r.containedImages
}

// ChartPath returns the path to the packaged chart archive on disk.
func (r *ChartReleaser) ChartPath() string {
	return r.chartPath
}
