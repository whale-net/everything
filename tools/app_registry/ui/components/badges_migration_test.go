package components

import (
	"regexp"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// This file guards the FR2 migration of badges.templ onto htmxui.Badge
// (issue #1005): every domain vocabulary switch (promotability,
// artifact-state, provenance, deploy-unit, release-run-target-state) must
// still map to the exact same label + daisyUI colour class it did before
// the refactor. read_screens_test.go's
// distinctness checks would not catch e.g. two values being swapped onto
// each other's colour (still "distinct" from every other value, just
// wrong), or the soft/size modifiers Badge's extra parameters control
// silently dropping -- these tests assert the exact class set per value
// instead.

var classAttrRE = regexp.MustCompile(`class="([^"]*)"`)

// classTokens extracts every whitespace-separated class token from the
// first class="..." attribute in body. hasClass checks exact token
// membership, not a substring match -- "badge-success" must not
// accidentally match a hypothetical "badge-success-ish" or vice versa (the
// #1003/#1004 mutation-testing precedent).
func classTokens(body string) []string {
	m := classAttrRE.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	return strings.Fields(m[1])
}

func hasClass(body, class string) bool {
	for _, tok := range classTokens(body) {
		if tok == class {
			return true
		}
	}
	return false
}

// wantBadge asserts body is a single <span> carrying label as its text and
// exactly the given class set (no more, no fewer) -- e.g. a stray
// badge-soft or badge-sm leaking in (or dropping out) would be caught here
// even though it wouldn't change the value's distinctness from its
// siblings.
func wantBadge(t *testing.T, body, label string, wantClasses []string) {
	t.Helper()
	if !strings.Contains(body, ">"+label+"<") {
		t.Errorf("expected exact label text %q, got %q", label, body)
	}
	got := classTokens(body)
	if len(got) != len(wantClasses) {
		t.Errorf("expected exactly %d classes %v, got %d %v in %q", len(wantClasses), wantClasses, len(got), got, body)
		return
	}
	for _, want := range wantClasses {
		found := false
		for _, g := range got {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected class %q among %v, got %q", want, got, body)
		}
	}
}

func TestPromotabilityBadge_ExactLabelAndClassPerValue(t *testing.T) {
	cases := []struct {
		v      pb.Promotability
		label  string
		colour string
	}{
		{pb.Promotability_PROMOTABILITY_PROMOTABLE, "Promotable", "badge-success"},
		{pb.Promotability_PROMOTABILITY_VIA_CHART, "Via chart", "badge-info"},
		{pb.Promotability_PROMOTABILITY_NOT_PROMOTABLE, "Not promotable", "badge-neutral"},
		{pb.Promotability_PROMOTABILITY_UNSPECIFIED, "Unknown", "badge-warning"},
	}
	for _, c := range cases {
		body := render(t, PromotabilityBadge(c.v))
		wantBadge(t, body, c.label, []string{"badge", c.colour})
	}
}

func TestArtifactStateBadge_ExactLabelAndClassPerValue(t *testing.T) {
	cases := []struct {
		v      pb.ArtifactState
		label  string
		colour string
	}{
		{pb.ArtifactState_ARTIFACT_STATE_ALLOCATED, "Allocated", "badge-neutral"},
		{pb.ArtifactState_ARTIFACT_STATE_PUBLISHING, "Publishing", "badge-info"},
		{pb.ArtifactState_ARTIFACT_STATE_PUBLISHED, "Published", "badge-success"},
		{pb.ArtifactState_ARTIFACT_STATE_FAILED, "Failed", "badge-error"},
		{pb.ArtifactState_ARTIFACT_STATE_UNSPECIFIED, "Unknown", "badge-warning"},
	}
	for _, c := range cases {
		body := render(t, ArtifactStateBadge(c.v))
		wantBadge(t, body, c.label, []string{"badge", c.colour})
	}
}

func TestProvenanceBadge_ExactLabelAndClassPerValue(t *testing.T) {
	cases := []struct {
		v      pb.ArtifactProvenance
		label  string
		colour string
	}{
		{pb.ArtifactProvenance_ARTIFACT_PROVENANCE_OBSERVED, "Observed", "badge-ghost"},
		{pb.ArtifactProvenance_ARTIFACT_PROVENANCE_ADOPTED, "Adopted", "badge-warning"},
		{pb.ArtifactProvenance_ARTIFACT_PROVENANCE_UNSPECIFIED, "Unknown", "badge-warning"},
	}
	for _, c := range cases {
		body := render(t, ProvenanceBadge(c.v))
		wantBadge(t, body, c.label, []string{"badge", c.colour})
	}
}

// DeployUnitBadge is the one badge call site that passes soft=true and a
// non-default size (BadgeSizeSM) through to htmxui.Badge -- the modifier
// classes most at risk of getting silently dropped in a refactor, since
// none of the other badge helpers exercise them.
func TestDeployUnitBadge_ExactLabelAndClassPerValue(t *testing.T) {
	cases := []struct {
		name    string
		isChart bool
		u       appmetapb.DeployUnit
		label   string
		colour  string
	}{
		{"chart entity", true, appmetapb.DeployUnit_DEPLOY_UNIT_UNSPECIFIED, "chart", "badge-success"},
		{"via-chart app", false, appmetapb.DeployUnit_DEPLOY_UNIT_CHART, "image (via chart)", "badge-info"},
		{"standalone image app", false, appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE, "image", "badge-success"},
		{"build-only app", false, appmetapb.DeployUnit_DEPLOY_UNIT_NONE, "none (build-only)", "badge-neutral"},
		{"unknown deploy unit", false, appmetapb.DeployUnit_DEPLOY_UNIT_UNSPECIFIED, "unknown", "badge-warning"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := render(t, DeployUnitBadge(c.isChart, c.u))
			wantBadge(t, body, c.label, []string{"badge", "badge-soft", c.colour, "badge-sm"})
		})
	}
}

func TestReleaseRunTargetStateBadge_ExactLabelAndClassPerValue(t *testing.T) {
	cases := []struct {
		v      pb.ReleaseRunTargetState
		label  string
		colour string
	}{
		{pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_QUEUED, "Queued", "badge-neutral"},
		{pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_BUILDING, "Building", "badge-info"},
		{pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_PUBLISHING, "Publishing", "badge-info"},
		{pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_RECORDING, "Recording", "badge-info"},
		{pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED, "Succeeded", "badge-success"},
		{pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_FAILED, "Failed", "badge-error"},
		{pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_UNSPECIFIED, "Unknown", "badge-warning"},
	}
	for _, c := range cases {
		body := render(t, ReleaseRunTargetStateBadge(c.v))
		wantBadge(t, body, c.label, []string{"badge", c.colour})
	}
}

// PromotionSyncOutcomeBadge (issue #1032) does not exist on this branch yet
// -- badges.templ's vocabulary here ends at ReleaseRunTargetStateBadge, so
// there is nothing further to cover.
