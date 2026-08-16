package components

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/a-h/templ"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// --- Vocabulary (NFR-11, FR-33): exactly one badge label+colour per value --

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	var w io.Writer = &sb
	if err := c.Render(context.Background(), w); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	return sb.String()
}

func TestPromotabilityBadge_OneLabelColourPairPerValue(t *testing.T) {
	values := []pb.Promotability{
		pb.Promotability_PROMOTABILITY_PROMOTABLE,
		pb.Promotability_PROMOTABILITY_VIA_CHART,
		pb.Promotability_PROMOTABILITY_NOT_PROMOTABLE,
		pb.Promotability_PROMOTABILITY_UNSPECIFIED, // the "unknown" branch
	}
	seen := make(map[string]pb.Promotability)
	for _, v := range values {
		body := render(t, PromotabilityBadge(v))
		if prior, ok := seen[body]; ok && prior != v {
			t.Errorf("promotability values %v and %v render identically (%q) — each value must have exactly one badge label+colour", prior, v, body)
		}
		seen[body] = v
	}
	// The unspecified/unknown value must never render as one of the
	// success-coded values -- never healthy-looking for an unknown state
	// (NFR-6).
	unknownBody := render(t, PromotabilityBadge(pb.Promotability_PROMOTABILITY_UNSPECIFIED))
	if strings.Contains(unknownBody, "badge-success") {
		t.Errorf("an unknown/unspecified promotability must never render with the success badge colour, got %q", unknownBody)
	}
}

func TestArtifactStateBadge_OneLabelColourPairPerValue(t *testing.T) {
	values := []pb.ArtifactState{
		pb.ArtifactState_ARTIFACT_STATE_ALLOCATED,
		pb.ArtifactState_ARTIFACT_STATE_PUBLISHING,
		pb.ArtifactState_ARTIFACT_STATE_PUBLISHED,
		pb.ArtifactState_ARTIFACT_STATE_FAILED,
		pb.ArtifactState_ARTIFACT_STATE_UNSPECIFIED,
	}
	seen := make(map[string]pb.ArtifactState)
	for _, v := range values {
		body := render(t, ArtifactStateBadge(v))
		if prior, ok := seen[body]; ok && prior != v {
			t.Errorf("artifact-state values %v and %v render identically (%q)", prior, v, body)
		}
		seen[body] = v
	}
}

func TestProvenanceBadge_AdoptedIsDistinctFromObservedAndUnknown(t *testing.T) {
	adopted := render(t, ProvenanceBadge(pb.ArtifactProvenance_ARTIFACT_PROVENANCE_ADOPTED))
	observed := render(t, ProvenanceBadge(pb.ArtifactProvenance_ARTIFACT_PROVENANCE_OBSERVED))
	unknown := render(t, ProvenanceBadge(pb.ArtifactProvenance_ARTIFACT_PROVENANCE_UNSPECIFIED))

	if adopted == observed || adopted == unknown || observed == unknown {
		t.Errorf("provenance values must render distinctly, got adopted=%q observed=%q unknown=%q", adopted, observed, unknown)
	}
	if !strings.Contains(adopted, "Adopted") {
		t.Errorf("adopted provenance must be tagged with an explicit 'Adopted' label, got %q", adopted)
	}
}

// --- Digest (NFR-4): truncated for density, but recoverable in full -------

func TestDigestDisplay_TruncatedButFullValueRecoverable(t *testing.T) {
	full := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	body := render(t, DigestDisplay("v1.2.3", full))

	if strings.Contains(body, full) == false {
		t.Fatalf("the full digest must still be recoverable from the rendered output (e.g. via title attribute), body: %s", body)
	}
	if strings.Contains(body, "v1.2.3") == false {
		t.Errorf("version must render alongside digest wherever 'what's deployed' is shown (NFR-4), body: %s", body)
	}
	// A visibly truncated form (with an ellipsis) must be present -- proving
	// there IS truncated display text, not just the full value stashed in an
	// attribute somewhere.
	if !strings.Contains(body, "sha256:0123456789ab…") {
		t.Errorf("expected a visibly truncated digest form, got body: %s", body)
	}
}

func TestDigestDisplay_ShortNonSha256ValuePassesThroughUntruncated(t *testing.T) {
	// A non-"sha256:"-prefixed short value (e.g. test/placeholder data)
	// should not be mangled -- digestTruncated's fallback path.
	short := "not-a-digest"
	body := render(t, DigestDisplay("", short))
	if !strings.Contains(body, short) {
		t.Errorf("expected a short non-digest value to pass through unmodified, body: %s", body)
	}
}
