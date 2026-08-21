package cmd

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// craneAuthOption builds the crane.Option authenticating against the
// registry. When token is non-empty it is used as a basic-auth password
// with username "x-access-token" -- the convention GitHub itself uses for
// authenticating with an installation/PAT token over HTTPS, and (per GHCR's
// support for the standard Docker Registry v2 token exchange) works
// identically for `docker login`/registry API calls. When token is empty,
// falls back to authn.DefaultKeychain (reads ~/.docker/config.json), so a
// caller that has already run `docker login` (or `crane auth login`) still
// works without setting GHCR_TOKEN explicitly -- useful for local testing.
func craneAuthOption(token string) crane.Option {
	if token == "" {
		return crane.WithAuthFromKeychain(authn.DefaultKeychain)
	}
	return crane.WithAuth(&authn.Basic{Username: "x-access-token", Password: token})
}

// craneTag retags src (a full reference, e.g. "repo@sha256:..." or
// "repo:oldtag") with newTag (a bare tag, not a full reference) in the same
// repository -- a registry-side retag with no image rebuild/re-pull/re-push
// of layer data, matching `crane tag <src> <newTag>`.
func craneTag(src, newTag, token string) error {
	return crane.Tag(src, newTag, craneAuthOption(token))
}

// craneDigest resolves ref (e.g. "repo:tag") to its current digest, or
// returns an error if ref does not exist / is not reachable. Mirrors
// extractImageDigest's role for ImageReleaser, but via the registry API
// directly rather than shelling out to `docker buildx imagetools inspect`
// -- see GHCRRetagReleaser's doc comment on why finalize-app avoids a
// `docker` dependency.
func craneDigest(ref, token string) (string, error) {
	return crane.Digest(ref, craneAuthOption(token))
}

// GHCRRetagReleaser implements ArtifactReleaser for finalize-app (issue
// #928): the "assign final version" half of what release-app used to do in
// one step. It never rebuilds or re-pushes image content -- Build performs
// a pure registry-side retag (crane.Tag) of the digest the merged
// release-trigger GHA job already pushed under a build-scoped tag (see
// build_app.go), which is the whole point of splitting build from finalize:
// the expensive, cross-arch image build already happened once, in GHA: this
// step is just "point a version tag at content that already exists."
//
// Chose go-containerregistry's Go library (pkg/crane) over shelling out to
// a `crane`/`skopeo` CLI binary (see issue #928's "Research crane tag,
// docker buildx imagetools create, or skopeo copy" known-unknown):
// finalize-app runs from the same release_helper_go binary ResolvePlan
// already shells out to (see worker/release/plan.go's doc comment on that
// binary's existing operational precondition) -- adding a pure static Go
// dependency compiles hermetically through this repo's existing Bazel/Go
// toolchain with no new external binary to vendor, download, or
// cross-compile, sidestepping the exact "silent at build time, fails at
// runtime" cross-compilation risk docs/DOCKER.md warns about for this
// repo's image tooling.
type GHCRRetagReleaser struct {
	// Repository is the image reference without a tag (e.g.
	// "ghcr.io/whale-net/demo-hello-go").
	Repository string
	// Digest is the already-pushed image's digest (sha256:...), as recorded
	// in the merged build job's per-app manifest (build_app.go's
	// BuildAppManifest). Never changes across a retag.
	Digest string
	// AuthToken authenticates the retag/digest-lookup calls against the
	// registry -- see craneAuthOption's doc comment.
	AuthToken string

	taggedVersion string
}

// Kind returns ARTIFACT_KIND_IMAGE.
func (r *GHCRRetagReleaser) Kind() pb.ArtifactKind {
	return pb.ArtifactKind_ARTIFACT_KIND_IMAGE
}

// Build retags r.Digest with version in the same repository. Returns
// r.Digest unchanged -- a retag never produces new content, so the digest
// ExecuteRelease records is exactly the one the build job already pushed.
func (r *GHCRRetagReleaser) Build(ctx context.Context, version string) (string, error) {
	if r.Digest == "" {
		return "", fmt.Errorf("no digest to retag for %s (finalize-app requires the build job's manifest digest)", r.Repository)
	}
	src := fmt.Sprintf("%s@%s", r.Repository, r.Digest)
	fmt.Printf("Retagging %s -> %s:%s (no rebuild)...\n", src, r.Repository, version)
	if err := craneTag(src, version, r.AuthToken); err != nil {
		return "", fmt.Errorf("retag %s to %s: %w", src, version, err)
	}
	r.taggedVersion = version
	return r.Digest, nil
}

// FetchPublished checks the registry for an existing tag at version,
// reporting whether its digest matches r.Digest (ExecuteRelease's no-op /
// collision-resolution signal).
func (r *GHCRRetagReleaser) FetchPublished(ctx context.Context, version string) (bool, string, bool, error) {
	ref := fmt.Sprintf("%s:%s", r.Repository, version)
	digest, err := craneDigest(ref, r.AuthToken)
	if err != nil || digest == "" {
		return false, "", false, nil
	}
	return true, digest, digest == r.Digest, nil
}

// Publish is a no-op: the retag in Build already made version visible in
// the registry (mirrors ImageReleaser.Publish's identical no-op, for the
// identical reason -- see that type's doc comment).
func (r *GHCRRetagReleaser) Publish(ctx context.Context, version string) error {
	return nil
}
