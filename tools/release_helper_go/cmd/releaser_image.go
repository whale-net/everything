package cmd

import (
	"context"
	"fmt"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// ImageReleaser implements ArtifactReleaser for container image targets.
type ImageReleaser struct {
	Domain     string
	App        *AppMetadata
	PushTarget string
	RepoPath   string
	GitSHA     string
	Bazel      BazelRunner
	Docker     DockerRunner
	// Tagger performs Publish's registry-side retag; nil uses
	// defaultImageTagger (production). Tests inject a fake here instead of
	// hitting the real registry.
	Tagger        ImageTagger
	currentDigest string
}

// Kind returns ARTIFACT_KIND_IMAGE.
func (r *ImageReleaser) Kind() pb.ArtifactKind {
	return pb.ArtifactKind_ARTIFACT_KIND_IMAGE
}

// Build invokes Bazel to build and push the container image, tagged only
// with build-scoped tags ("latest" and the git SHA) -- never the release
// version tag itself. The version tag is applied separately in Publish;
// see that method's doc comment for why.
func (r *ImageReleaser) Build(ctx context.Context, version string) (string, error) {
	fullName := r.App.FullName()
	if r.GitSHA == "" {
		return "", fmt.Errorf("GitSHA is required to build %s (used as the build-scoped tag Publish later retags to the release version)", fullName)
	}
	tags := []string{"latest", r.GitSHA}

	bazelArgs := []string{"run", r.PushTarget, "--"}
	for _, t := range tags {
		bazelArgs = append(bazelArgs, "--tag", t)
	}

	fmt.Printf("Building and releasing %s (build tag %s) via %s...\n", fullName, r.GitSHA, r.PushTarget)
	if _, err := r.Bazel.Run(bazelArgs...); err != nil {
		return "", fmt.Errorf("bazel run %s: %w", r.PushTarget, err)
	}

	r.currentDigest = extractImageDigest(r.Docker, r.RepoPath, r.GitSHA)
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

// Publish retags the build-scoped image (the git SHA tag pushed in Build)
// with the release version tag, via a registry-side retag -- no rebuild or
// re-push of layer data. Deliberately split out of Build: ExecuteRelease
// only calls Publish once its no-op/collision decision (step 3, "Collision
// resolution & no-op detection") concludes this candidate should actually
// be published. Tagging the version directly in Build (the old behavior)
// created the version tag in the registry before that decision ran, so a
// build later classified as a no-op rebuild (FailPublish called,
// RecordArtifact never called) still left a real, visible version tag in
// GHCR pointing at content App Registry considers unpublished.
func (r *ImageReleaser) Publish(ctx context.Context, version string) error {
	if r.currentDigest == "" {
		return fmt.Errorf("no digest to tag for %s (Build must run before Publish)", r.RepoPath)
	}
	tagger := r.Tagger
	if tagger == nil {
		tagger = defaultImageTagger
	}
	src := fmt.Sprintf("%s@%s", r.RepoPath, r.currentDigest)
	fmt.Printf("Tagging %s -> %s:%s...\n", src, r.RepoPath, version)
	if err := tagger.Tag(src, version, ""); err != nil {
		return fmt.Errorf("tag %s as %s: %w", src, version, err)
	}
	return nil
}
