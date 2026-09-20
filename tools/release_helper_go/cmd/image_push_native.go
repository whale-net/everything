package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// nativeImagePushEnabled reports whether build-app should push the built OCI
// image index directly via go-containerregistry (pkg/v1/remote) instead of
// `bazel run <target>_image_push` -- rules_oci's generated bash script,
// which shells out to a vendored `crane` CLI binary and `jq` (see
// tools/bazel/container_image.bzl's oci_push wiring). Opt-in via
// RELEASE_NATIVE_IMAGE_PUSH=true (issue #2099) so the native path can be
// validated in CI before becoming the default -- this runs in the real
// release pipeline, so a silent regression here would break every app
// release, not just one.
func nativeImagePushEnabled() bool {
	return defaultEnv("RELEASE_NATIVE_IMAGE_PUSH") == "true"
}

// remoteAuthOption mirrors craneAuthOption (releaser_ghcr_retag.go) for the
// pkg/v1/remote API used here: pushing an OCI image *index* built by
// rules_oci's nested-index layout (multiplatform_image, container_image.bzl)
// needs remote.WriteIndex directly rather than pkg/crane's single-image
// helpers.
func remoteAuthOption(token string) remote.Option {
	if token == "" {
		return remote.WithAuthFromKeychain(authn.DefaultKeychain)
	}
	return remote.WithAuth(&authn.Basic{Username: "x-access-token", Password: token})
}

// pushOCILayoutIndex pushes the OCI image index at imageDir (a local layout
// directory, as produced by `bazel build <image index target>` -- see
// container_image.bzl's oci_image_index) to repository, then tags it with
// each of tags. Returns the pushed manifest's digest.
//
// multiplatform_image wraps the platform-transitioned index in an outer
// index with exactly one manifest entry (see that macro's "NOTE ON
// STRUCTURE" comment) -- the content actually pushed to the registry is the
// *inner* index referenced by that single entry, not the outer wrapper's own
// digest. This mirrors exactly what oci_push's generated script does: read
// `.manifests[0].digest` out of the outer index.json via jq, then
// `crane push <layout-dir> <repo>@<that digest>`.
func pushOCILayoutIndex(imageDir, repository string, tags []string, token string) (string, error) {
	outerIdx, err := layout.ImageIndexFromPath(imageDir)
	if err != nil {
		return "", fmt.Errorf("load OCI layout %s: %w", imageDir, err)
	}
	outerManifest, err := outerIdx.IndexManifest()
	if err != nil {
		return "", fmt.Errorf("read OCI index manifest at %s: %w", imageDir, err)
	}
	if len(outerManifest.Manifests) == 0 {
		return "", fmt.Errorf("OCI index at %s has no manifests", imageDir)
	}
	pushDigest := outerManifest.Manifests[0].Digest

	pushIdx, err := outerIdx.ImageIndex(pushDigest)
	if err != nil {
		return "", fmt.Errorf("resolve inner index %s at %s: %w", pushDigest, imageDir, err)
	}

	digestRef, err := name.NewDigest(fmt.Sprintf("%s@%s", repository, pushDigest.String()))
	if err != nil {
		return "", fmt.Errorf("parse digest ref for %s: %w", repository, err)
	}
	if err := remote.WriteIndex(digestRef, pushIdx, remoteAuthOption(token)); err != nil {
		return "", fmt.Errorf("push index to %s: %w", digestRef, err)
	}

	for _, tag := range tags {
		if err := craneTag(digestRef.String(), tag, token); err != nil {
			return "", fmt.Errorf("tag %s as %s: %w", digestRef, tag, err)
		}
	}

	return pushDigest.String(), nil
}

// buildAndPushImageNative builds meta's OCI image index (`bazel build`, not
// `bazel run`) and pushes it natively via pushOCILayoutIndex, replacing the
// `bazel run <target>_image_push` bash/crane-CLI path end to end. Returns
// the pushed digest.
func buildAndPushImageNative(bazel BazelRunner, meta AppMetadata, repository string, tags []string, token string) (string, error) {
	indexTarget := meta.ImageTarget
	if indexTarget == "" {
		return "", fmt.Errorf("%s has no image_target metadata", meta.FullName())
	}

	if _, err := bazelRunToDisk(bazel, "build", indexTarget); err != nil {
		return "", fmt.Errorf("bazel build %s: %w", indexTarget, err)
	}

	bazelBinOut, err := bazel.Run("info", "bazel-bin", "--config=ci-images")
	if err != nil {
		return "", fmt.Errorf("bazel info bazel-bin: %w", err)
	}
	bazelBin := strings.TrimSpace(bazelBinOut)

	pkg, targetName := splitLabel(indexTarget)
	imageDir := filepath.Join(bazelBin, pkg, targetName)

	return pushOCILayoutIndex(imageDir, repository, tags, token)
}

// splitLabel splits a "//pkg/path:name" Bazel label into its package
// directory and target name.
func splitLabel(label string) (pkg, name string) {
	rest := strings.TrimPrefix(label, "//")
	pkg, name, _ = strings.Cut(rest, ":")
	return pkg, name
}
