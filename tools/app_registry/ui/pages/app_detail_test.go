package pages

import (
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/matrix"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// This file covers #773: a DEPLOY_UNIT_NONE, non-binary ("build-only") app
// has no environment/chart pairing at all (matrix.IsDeployable(app) is
// false for it -- see matrix_test.go's TestIsDeployable), so AppDetail must
// not render the per-environment stat grid or the "Current artifact by
// environment" table for it -- there is no environment for either to ever
// have a version in. A deployable app (standalone image here) must still
// render both, unchanged.

func buildOnlyApp() *pb.App {
	return &pb.App{
		AppId:      "app-build-only",
		Domain:     "demo",
		Name:       "batch-job",
		FullName:   "demo-batch-job",
		AppType:    "job",
		DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE,
	}
}

func deployableApp() *pb.App {
	return &pb.App{
		AppId:      "app-deployable",
		Domain:     "platform",
		Name:       "worker",
		FullName:   "platform-worker",
		AppType:    "worker",
		DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE,
	}
}

func oneDevEnv() []matrix.AppEnvState {
	return []matrix.AppEnvState{
		{Env: &pb.Environment{EnvironmentId: "e1", Key: "dev", Rank: 0}},
	}
}

func TestAppDetail_NonDeployableApp_NoVersionByEnvSection(t *testing.T) {
	data := &matrix.AppDetailData{App: buildOnlyApp(), States: oneDevEnv()}

	html := renderComponent(t, AppDetail(nil, data))

	if strings.Contains(html, "Current artifact by environment") {
		t.Errorf("non-deployable app must not render the per-environment artifact table, got: %s", html)
	}
	if !strings.Contains(html, "Build-only app") {
		t.Errorf("expected a build-only note in place of the version-by-environment section, got: %s", html)
	}
}

func TestAppDetail_DeployableApp_RendersVersionByEnvSection(t *testing.T) {
	data := &matrix.AppDetailData{App: deployableApp(), States: oneDevEnv()}

	html := renderComponent(t, AppDetail(nil, data))

	if !strings.Contains(html, "Current artifact by environment") {
		t.Errorf("deployable app must render the per-environment artifact table, got: %s", html)
	}
	if strings.Contains(html, "Build-only app") {
		t.Errorf("deployable app must not render the build-only note, got: %s", html)
	}
}

// --- #774: latest version shown even with no env, and ranked ahead of the
// real per-environment states for a deployable app ------------------------

func publishedArtifact() *pb.Artifact {
	return &pb.Artifact{
		Version:    "v1.2.3",
		Digest:     "sha256:deadbeef",
		Provenance: pb.ArtifactProvenance_ARTIFACT_PROVENANCE_OBSERVED,
		State:      pb.ArtifactState_ARTIFACT_STATE_PUBLISHED,
	}
}

func TestAppDetail_NonDeployableApp_ShowsLatestVersion(t *testing.T) {
	data := &matrix.AppDetailData{App: buildOnlyApp(), States: oneDevEnv(), LatestArtifact: publishedArtifact()}

	html := renderComponent(t, AppDetail(nil, data))

	if !strings.Contains(html, "v1.2.3") {
		t.Errorf("non-deployable app with a published artifact must still show its latest version, got: %s", html)
	}
	if strings.Contains(html, "No published artifact yet") {
		t.Errorf("must not render the no-artifact-yet note when LatestArtifact is set, got: %s", html)
	}
}

func TestAppDetail_NonDeployableApp_NoArtifactYet(t *testing.T) {
	data := &matrix.AppDetailData{App: buildOnlyApp(), States: oneDevEnv()}

	html := renderComponent(t, AppDetail(nil, data))

	if !strings.Contains(html, "No published artifact yet") {
		t.Errorf("expected the no-artifact-yet note when LatestArtifact is nil, got: %s", html)
	}
}

func TestAppDetail_DeployableApp_LatestPrecedesDevEnv(t *testing.T) {
	data := &matrix.AppDetailData{App: deployableApp(), States: oneDevEnv(), LatestArtifact: publishedArtifact()}

	html := renderComponent(t, AppDetail(nil, data))

	latestIdx := strings.Index(html, "Latest")
	devIdx := strings.Index(html, ">dev<")
	if latestIdx == -1 || devIdx == -1 {
		t.Fatalf("expected both a Latest and a dev entry in the rendered page, got: %s", html)
	}
	if latestIdx > devIdx {
		t.Errorf("expected Latest to be ranked ahead of (render before) dev, got Latest at %d, dev at %d: %s", latestIdx, devIdx, html)
	}
	if !strings.Contains(html, "v1.2.3") {
		t.Errorf("expected the latest artifact's version to render, got: %s", html)
	}
}

// --- #794: every digest on the app page also links to its artifact -------

func onePromotedDevEnv() []matrix.AppEnvState {
	return []matrix.AppEnvState{
		{
			Env:      &pb.Environment{EnvironmentId: "e1", Key: "dev", Rank: 0},
			Promoted: true,
			Version:  "v1.0.0",
			Digest:   "sha256:cafefeed",
			Artifact: &pb.Artifact{Provenance: pb.ArtifactProvenance_ARTIFACT_PROVENANCE_OBSERVED},
		},
	}
}

func TestAppDetail_LatestArtifact_LinksToArtifactDetail(t *testing.T) {
	data := &matrix.AppDetailData{App: buildOnlyApp(), States: oneDevEnv(), LatestArtifact: publishedArtifact()}

	html := renderComponent(t, AppDetail(nil, data))

	want := `href="/artifacts/sha256:deadbeef"`
	if !strings.Contains(html, want) {
		t.Errorf("expected an artifact link %q for the latest artifact's digest, got: %s", want, html)
	}
}

func TestAppDetail_PromotedEnv_LinksToArtifactDetail(t *testing.T) {
	data := &matrix.AppDetailData{App: deployableApp(), States: onePromotedDevEnv()}

	html := renderComponent(t, AppDetail(nil, data))

	want := `href="/artifacts/sha256:cafefeed"`
	if !strings.Contains(html, want) {
		t.Errorf("expected an artifact link %q for the promoted dev env's digest, got: %s", want, html)
	}
}

// --- Story 3 (issue #1032): promotion timeline links + inline sync badge --

func onePromotionEvent() []*pb.PromotionEvent {
	return []*pb.PromotionEvent{
		{EventId: "evt-1", PromotionId: "promo-1", Action: pb.PromotionAction_PROMOTION_ACTION_PROMOTE, Actor: "alice", OccurredAt: 1000},
	}
}

// TestAppDetail_PromotionTimeline_LinksToPromotionDetails proves each
// timeline row links to the Promotion Details page (issue #1032) by
// promotion_id, for FR7's full lifecycle view.
func TestAppDetail_PromotionTimeline_LinksToPromotionDetails(t *testing.T) {
	data := &matrix.AppDetailData{App: deployableApp(), States: oneDevEnv(), Events: onePromotionEvent()}

	html := renderComponent(t, AppDetail(nil, data))

	want := `href="/promotions/promo-1"`
	if !strings.Contains(html, want) {
		t.Errorf("expected a link %q from the promotion timeline row, got: %s", want, html)
	}
}

// TestAppDetail_PromotionTimeline_RendersInlineSyncBadgeWhenPresent proves
// Story 3's inline sync/health badge renders on a timeline row when
// SyncOutcomes has a resolved entry for that row's promotion_id.
func TestAppDetail_PromotionTimeline_RendersInlineSyncBadgeWhenPresent(t *testing.T) {
	data := &matrix.AppDetailData{
		App: deployableApp(), States: oneDevEnv(), Events: onePromotionEvent(),
		SyncOutcomes: map[string]*pb.PromotionDetails{
			"promo-1": {Outcome: pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY},
		},
	}

	html := renderComponent(t, AppDetail(nil, data))

	if !strings.Contains(html, ">Synced<") {
		t.Errorf("expected the inline Synced badge to render on the timeline row, got: %s", html)
	}
}

// TestAppDetail_PromotionTimeline_OmitsBadgeWhenLookupMissing proves a
// promotion_id absent from SyncOutcomes (a failed/best-effort lookup, see
// fetchPromotionSyncOutcomes' doc comment) omits the badge entirely for
// that row -- never a false "Unknown".
func TestAppDetail_PromotionTimeline_OmitsBadgeWhenLookupMissing(t *testing.T) {
	data := &matrix.AppDetailData{
		App: deployableApp(), States: oneDevEnv(), Events: onePromotionEvent(),
		SyncOutcomes: map[string]*pb.PromotionDetails{}, // no entry for promo-1
	}

	html := renderComponent(t, AppDetail(nil, data))

	// Check the outcome badge's own label text, not raw badge-* classes --
	// the page legitimately renders other badges (e.g. DeployUnitBadge's
	// "image" label) that share those same daisyUI modifier classes.
	for _, label := range []string{">Synced<", ">Sync failed<", ">Pending<", ">Unknown<"} {
		if strings.Contains(html, label) {
			t.Errorf("expected no sync-outcome badge label (%s) when the lookup is missing, got: %s", label, html)
		}
	}
}

// TestAppDetail_PromotionTimeline_OmitsBadgeWhenSyncOutcomesNil proves the
// same omission holds when SyncOutcomes itself is nil (buildAppDetail's
// zero value on an EventsErr path, or a caller that never populated it) --
// an unconditional map index on a nil map is a documented, safe Go
// no-op-returns-zero-value, not a panic, but this pins that behavior
// against a future regression.
func TestAppDetail_PromotionTimeline_OmitsBadgeWhenSyncOutcomesNil(t *testing.T) {
	data := &matrix.AppDetailData{App: deployableApp(), States: oneDevEnv(), Events: onePromotionEvent(), SyncOutcomes: nil}

	html := renderComponent(t, AppDetail(nil, data))

	if strings.Contains(html, ">Unknown<") {
		t.Errorf("a nil SyncOutcomes map must never render a false 'Unknown' badge, got: %s", html)
	}
}
