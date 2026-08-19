package cmd

import (
	"context"
	"fmt"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// ImageReleaser implements ArtifactReleaser for container image targets.
type ImageReleaser struct {
	Domain        string
	App           *AppMetadata
	PushTarget    string
	RepoPath      string
	GitSHA        string
	Bazel         BazelRunner
	Docker        DockerRunner
	currentDigest string
}

// Kind returns ARTIFACT_KIND_IMAGE.
func (r *ImageReleaser) Kind() pb.ArtifactKind {
	return pb.ArtifactKind_ARTIFACT_KIND_IMAGE
}

// Build invokes Bazel to build and push the container image with the given version, latest, and git sha tags.
func (r *ImageReleaser) Build(ctx context.Context, version string) (string, error) {
	fullName := r.App.FullName()
	tags := []string{version, "latest"}
	if r.GitSHA != "" {
		tags = append(tags, r.GitSHA)
	}

	bazelArgs := []string{"run", r.PushTarget, "--"}
	for _, t := range tags {
		bazelArgs = append(bazelArgs, "--tag", t)
	}

	fmt.Printf("Building and releasing %s version %s via %s...\n", fullName, version, r.PushTarget)
	if _, err := r.Bazel.Run(bazelArgs...); err != nil {
		return "", fmt.Errorf("bazel run %s: %w", r.PushTarget, err)
	}

	r.currentDigest = extractImageDigest(r.Docker, r.RepoPath, version)
	return r.currentDigest, nil
}

// FetchPublished checks Docker / GHCR for the published image digest at the specified version.
func (r *ImageReleaser) FetchPublished(ctx context.Context, version string) (bool, string, bool, error) {
	digest := extractImageDigest(r.Docker, r.RepoPath, version)
	if digest == "" {
		return false, "", false, nil
	}
	matches := (r.currentDigest != "" && digest == r.currentDigest)
	return true, digest, matches, nil
}

// Publish is a no-op for images because bazel run <pushTarget> already pushes the image tags in Build.
func (r *ImageReleaser) Publish(ctx context.Context, version string) error {
	return nil
}
