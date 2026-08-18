package cmd

import (
	"context"
	"fmt"
	"path/filepath"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// BinaryReleaser implements ArtifactReleaser for CLI and firmware non-image binaries.
type BinaryReleaser struct {
	Domain         string
	App            *AppMetadata
	WorkspaceRoot  string
	GitSHA         string
	KindType       pb.ArtifactKind
	Bazel          BazelRunner
	ArtifactClient pb.ArtifactRegistryClient
	currentDigest  string
}

// Kind returns the artifact kind (defaults to determined kind: BINARY or FIRMWARE).
func (r *BinaryReleaser) Kind() pb.ArtifactKind {
	if r.KindType != pb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED {
		return r.KindType
	}
	return determineArtifactKind(*r.App)
}

// Build invokes Bazel to build the binary target and computes its digest.
func (r *BinaryReleaser) Build(ctx context.Context, version string) (string, error) {
	fullName := r.App.FullName()
	fmt.Printf("Building non-image application %s (%s)...\n", fullName, r.App.AppType)
	if r.App.BinaryTarget != "" {
		if _, err := r.Bazel.Run("build", r.App.BinaryTarget); err != nil {
			return "", fmt.Errorf("bazel build %s: %w", r.App.BinaryTarget, err)
		}
	}

	pkg, binName := binaryTargetPkgAndName(*r.App)
	candidatePaths := []string{
		filepath.Join(r.WorkspaceRoot, "bazel-bin", pkg, binName+"_", binName),
		filepath.Join(r.WorkspaceRoot, "bazel-bin", pkg, binName),
		filepath.Join(r.WorkspaceRoot, "bazel-bin", pkg, r.App.Name+"_", r.App.Name),
		filepath.Join(r.WorkspaceRoot, "bazel-bin", pkg, r.App.Name),
	}
	var binaryDigest string
	for _, cp := range candidatePaths {
		if h, _, err := computeFileSHA256(cp); err == nil {
			binaryDigest = "sha256:" + h
			break
		}
	}
	if binaryDigest == "" && r.GitSHA != "" {
		binaryDigest = "sha256:" + r.GitSHA
	}
	r.currentDigest = binaryDigest
	return r.currentDigest, nil
}

// FetchPublished queries App Registry for previously published artifact digest.
func (r *BinaryReleaser) FetchPublished(ctx context.Context, version string) (bool, string, bool, error) {
	if r.ArtifactClient == nil {
		return false, "", false, nil
	}
	resp, err := r.ArtifactClient.GetArtifact(ctx, &pb.GetArtifactRequest{
		OwnerFullName: r.App.FullName(),
		Kind:          r.Kind(),
		Version:       version,
	})
	if err != nil || resp == nil || resp.Artifact == nil {
		return false, "", false, nil
	}
	prevDigest := resp.Artifact.Digest
	matches := (r.currentDigest != "" && prevDigest != "" && r.currentDigest == prevDigest)
	return true, prevDigest, matches, nil
}

// Publish is a no-op for release-app binary releases (binaries are recorded in App Registry and/or uploaded to GitHub release).
func (r *BinaryReleaser) Publish(ctx context.Context, version string) error {
	return nil
}
