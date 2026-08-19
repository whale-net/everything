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

	chartPath       string
	chartBytes      []byte
	chartDigest     string
	containedImages []*pb.ContainedImage
}

// Kind returns ARTIFACT_KIND_CHART.
func (r *ChartReleaser) Kind() pb.ArtifactKind {
	return pb.ArtifactKind_ARTIFACT_KIND_CHART
}

// Build invokes Bazel to build the chart, packages it with HelmPackager, and computes its content digest.
func (r *ChartReleaser) Build(ctx context.Context, version string) (string, error) {
	chartTarget, chartDir := chartOutputPaths(r.WorkspaceRoot, r.Chart)
	fmt.Printf("Building bazel target: %s\n", chartTarget)
	if _, err := r.Bazel.Run("build", chartTarget); err != nil {
		return "", fmt.Errorf("bazel build %s: %w", chartTarget, err)
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

	r.containedImages = resolveContainedImages(chartDir, r.AppVersions, r.Chart, r.AllApps, r.Docker, r.FS)
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
