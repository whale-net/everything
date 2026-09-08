package cmd

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
)

// buildNestedTestLayout writes an OCI layout to dir shaped like
// multiplatform_image's real output: an outer index wrapping exactly one
// manifest entry that is itself an index (see pushOCILayoutIndex's doc
// comment) -- mirroring rules_oci's nested-index structure so the test
// exercises the same unwrap logic the bash push script's
// `.manifests[0].digest` jq expression relies on.
func buildNestedTestLayout(t *testing.T, dir string) (innerDigest string) {
	t.Helper()

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image() = %v", err)
	}
	innerIdx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: img})
	outerIdx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: innerIdx})

	if _, err := layout.Write(dir, outerIdx); err != nil {
		t.Fatalf("layout.Write() = %v", err)
	}

	d, err := innerIdx.Digest()
	if err != nil {
		t.Fatalf("innerIdx.Digest() = %v", err)
	}
	return d.String()
}

func TestPushOCILayoutIndex(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	dir := t.TempDir()
	wantDigest := buildNestedTestLayout(t, dir)

	repository := host + "/test/image"
	gotDigest, err := pushOCILayoutIndex(dir, repository, []string{"v1.0.0", "latest"}, "")
	if err != nil {
		t.Fatalf("pushOCILayoutIndex() = %v", err)
	}
	if gotDigest != wantDigest {
		t.Errorf("pushOCILayoutIndex() digest = %q, want %q", gotDigest, wantDigest)
	}

	for _, tag := range []string{"v1.0.0", "latest"} {
		got, err := craneDigest(repository+":"+tag, "")
		if err != nil {
			t.Fatalf("craneDigest(%s) = %v", tag, err)
		}
		if got != wantDigest {
			t.Errorf("tag %s resolved to %q, want %q", tag, got, wantDigest)
		}
	}
}

func TestSplitLabel(t *testing.T) {
	pkg, name := splitLabel("//audience_score_system/web:web_image")
	if pkg != "audience_score_system/web" || name != "web_image" {
		t.Errorf("splitLabel() = (%q, %q), want (%q, %q)", pkg, name, "audience_score_system/web", "web_image")
	}
}

func TestNativeImagePushEnabled(t *testing.T) {
	withEnv(map[string]string{"RELEASE_NATIVE_IMAGE_PUSH": "true"}, func() {
		if !nativeImagePushEnabled() {
			t.Errorf("nativeImagePushEnabled() = false, want true")
		}
	})
	withEnv(map[string]string{}, func() {
		if nativeImagePushEnabled() {
			t.Errorf("nativeImagePushEnabled() = true, want false")
		}
	})
}
