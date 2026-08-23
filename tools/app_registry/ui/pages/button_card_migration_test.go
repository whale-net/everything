package pages

import (
	"regexp"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/components"
)

// This file guards the FR2 htmxui.Button/Card adoption across
// pages/*.templ's remaining raw daisyUI btn/card call sites (issue #1005's
// second Testing pass, commit d80be6dc). Nothing pre-existing asserted the
// migrated markup itself:
//
//   - The four "info card wrapped in a plain mb-4 div" call sites
//     (promote.templ's commitOutcome, rollback.templ's rollbackOutcome,
//     release_status.templ's releaseStatusDetail, release_trigger.templ's
//     releaseDraft) rely on the wrapping <div class="mb-4"> carrying the
//     margin *because* Card's own root <div> already owns a literal `class`
//     attribute that attrs cannot safely extend (a second `class` value
//     there is a duplicate HTML attribute the browser silently discards,
//     not merged -- see htmxui.Card's doc comment and button_templ.go's/
//     card_templ.go's generated RenderAttributes call, which writes attrs
//     verbatim after the literal class). A regression that collapsed the
//     wrapper (e.g. moved mb-4 back onto Card's own attrs) would render
//     with no visible margin and no compile error -- these tests catch
//     that structurally, by asserting the wrapper div is the immediate
//     parent of Card's own chrome div.
//   - Every migrated htmxui.Button call site must still render the exact
//     daisyUI variant/size class set the raw markup it replaced had. A
//     mismapped ButtonVariant/ButtonSize argument (e.g. dropping a variant,
//     or picking the wrong size) is a silent visual regression no existing
//     handler test would catch, since none of them assert button classes.
//
// mutation-tested: TestPromoteCommitOutcome_CardWrapsCardChromeDirectly and
// TestDeployments_ApplyButtonHasNoColorVariant were each verified red (by
// temporarily moving the mb-4 wrapper's class onto Card's attrs, and by
// swapping ButtonSizeSM for ButtonPrimary+ButtonSizeSM respectively) before
// being reverted to their current, passing form.

// cardWrapperRE matches Card's fixed chrome class immediately inside a
// plain `mb-4` wrapper div, ignoring insignificant whitespace templ may (or
// may not) emit between the two tags.
var cardWrapperRE = regexp.MustCompile(`<div class="mb-4">\s*<div class="card bg-base-100 border border-base-300 shadow-sm">`)

// --- promote.templ: commitOutcome's Card-wrapped info cards ---------------

func TestPromoteCommitOutcome_CardWrapsCardChromeDirectly(t *testing.T) {
	s := PromoteViewState{
		Owner:  "platform-worker",
		Kind:   "image",
		EnvKey: "dev",
		Committed: &CommitOutcome{
			Promotion: &pb.Promotion{
				PromotionId:     "promo-1",
				EnvironmentKey:  "dev",
				Version:         "v1.2.3",
				Digest:          "sha256:abcdef0123456789",
				ValidFrom:       1000,
			},
			Event: &pb.PromotionEvent{
				Actor:      "alice",
				Reason:     "verify fix",
				OccurredAt: 1000,
			},
		},
	}
	body := renderComponent(t, Promote(adminUser(), s))

	// Both the current-state card and the submission-event card must be
	// individually wrapped -- a regression that dropped the wrapper from
	// only one of the two call sites (as opposed to both) must still be
	// caught, so this counts matches rather than checking presence of any
	// one.
	if got := len(cardWrapperRE.FindAllString(body, -1)); got != 2 {
		t.Errorf("expected exactly 2 info cards wrapped by a plain mb-4 div immediately around Card's own chrome div, got %d; body = %q", got, body)
	}
	if !strings.Contains(body, "Current recorded state of this target") {
		t.Errorf("expected the current-state card body to render, got %q", body)
	}
	if !strings.Contains(body, "This submission's event") {
		t.Errorf("expected the submission-event card body to render, got %q", body)
	}
	// mb-4 must never land on Card's own root class attribute (that would
	// require a second, silently-discarded `class` attribute); it must
	// remain the wrapper's sole class.
	if strings.Contains(body, `class="card bg-base-100 border border-base-300 shadow-sm mb-4"`) {
		t.Errorf("mb-4 must not be merged onto Card's own class attribute, got %q", body)
	}
}

// --- rollback.templ: rollbackOutcome's Card-wrapped info card -------------

func TestRollbackOutcome_CardWrapsCardChromeDirectly(t *testing.T) {
	s := RollbackViewState{
		Owner:  "platform-worker",
		Kind:   "image",
		EnvKey: "dev",
		Committed: &RollbackOutcome{
			Promotion: &pb.Promotion{
				PromotionId:    "promo-2",
				EnvironmentKey: "dev",
				Version:        "v1.2.2",
				Digest:         "sha256:0123456789abcdef",
				ValidFrom:      2000,
			},
		},
	}
	body := renderComponent(t, Rollback(promoterDevUser(), s))

	if !cardWrapperRE.MatchString(body) {
		t.Errorf("expected rollbackOutcome's info card wrapped by a plain mb-4 div immediately around Card's own chrome div, got %q", body)
	}
	if !strings.Contains(body, "Current recorded state of this target") {
		t.Errorf("expected the current-state card body to render, got %q", body)
	}
}

// --- release_status.templ: releaseStatusDetail's Card-wrapped info cards --

func TestReleaseStatusDetail_CardsAndRetryButton(t *testing.T) {
	s := ReleaseStatusViewState{
		ReleaseRunID: "run-7",
		Release: &pb.GetReleaseResponse{
			ReleaseRunId:   "run-7",
			RequestedScope: "platform",
			TriggeredBy:    "bob",
		},
	}
	body := renderComponent(t, ReleaseStatus(adminUser(), s))

	if !cardWrapperRE.MatchString(body) {
		t.Errorf("expected releaseStatusDetail's info cards wrapped by a plain mb-4 div immediately around Card's own chrome div, got %q", body)
	}
	if !strings.Contains(body, "Run") || !strings.Contains(body, "Targets (0)") {
		t.Errorf("expected the run and targets card bodies to render, got %q", body)
	}
	// Retry re-submits with btn-warning, matching the raw markup it replaced.
	if !strings.Contains(body, `class="btn btn-warning"`) {
		t.Errorf("expected the Retry button to render btn-warning (htmxui.ButtonWarning, ButtonSizeMD), got %q", body)
	}
	if !strings.Contains(body, ">Retry<") {
		t.Errorf("expected the Retry button label, got %q", body)
	}
}

// --- release_trigger.templ: releaseDraft's Card-wrapped draft table -------

func TestReleaseTriggerDraft_CardWrapsCardChromeDirectlyAndTriggerButton(t *testing.T) {
	s := ReleaseTriggerViewState{
		ResolvedTargets: []*pb.ReleaseTargetInput{
			{OwnerFullName: "platform-worker", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE},
		},
		VersionInputs: map[string]TargetVersionInput{
			"platform-worker": {Mode: "bump", Bump: "patch"},
		},
	}
	body := renderComponent(t, ReleaseTrigger(adminUser(), s))

	if !cardWrapperRE.MatchString(body) {
		t.Errorf("expected releaseDraft's card wrapped by a plain mb-4 div immediately around Card's own chrome div, got %q", body)
	}
	if !strings.Contains(body, "Draft — 1 target(s)") {
		t.Errorf("expected the draft card body to render, got %q", body)
	}
	if !strings.Contains(body, `class="btn btn-warning"`) {
		t.Errorf("expected the Trigger release button to render btn-warning, got %q", body)
	}
	if !strings.Contains(body, ">Trigger release<") {
		t.Errorf("expected the Trigger release button label, got %q", body)
	}
	// The Resolve-selection submit (step 1) is a distinct btn-primary
	// control and must still render regardless of the draft step below it.
	if !strings.Contains(body, `class="btn btn-primary"`) {
		t.Errorf("expected the Resolve selection button to render btn-primary, got %q", body)
	}
	if !strings.Contains(body, ">Resolve selection<") {
		t.Errorf("expected the Resolve selection button label, got %q", body)
	}
}

// --- Plain (no-color-variant) htmxui.Button call sites ---------------------
//
// builds.templ, environment_diff.templ pass htmxui.ButtonPrimary; apps_catalog.templ,
// deployments.templ, and drift_audit.templ deliberately pass "" (no color
// variant -- daisyUI's default neutral button), matching the raw
// `btn btn-sm` markup they replaced. A variant regression here (e.g.
// accidentally adding ButtonPrimary) is silent -- no test asserted these
// classes before this file.

func TestBuilds_LookupButtonPrimarySmall(t *testing.T) {
	body := renderComponent(t, Builds(adminUser(), nil, ""))
	if !strings.Contains(body, `class="btn btn-primary btn-sm"`) {
		t.Errorf("expected the Look up button to render btn-primary btn-sm, got %q", body)
	}
	if !strings.Contains(body, "Look up") {
		t.Errorf("expected the Look up button label, got %q", body)
	}
}

func TestDeployments_ApplyButtonHasNoColorVariant(t *testing.T) {
	envs := devStageProdEnvs()
	m := buildTestMatrix(envs)
	body := renderComponent(t, Deployments(adminUser(), m, false))

	if !strings.Contains(body, `class="btn btn-sm"`) {
		t.Errorf("expected the Apply button to render plain btn-sm (no color variant), got %q", body)
	}
	if strings.Contains(body, `class="btn btn-primary btn-sm"`) || strings.Contains(body, `class="btn btn-neutral btn-sm"`) {
		t.Errorf("expected the Apply button to carry no color variant class, got %q", body)
	}
}

func TestDriftAudit_FilterButtonHasNoColorVariant(t *testing.T) {
	envs := devStageProdEnvs()
	m := buildTestMatrix(envs)
	body := renderComponent(t, DriftAudit(adminUser(), m, nil, nil, nil, ""))

	if !strings.Contains(body, `class="btn btn-sm"`) {
		t.Errorf("expected the Filter button to render plain btn-sm (no color variant), got %q", body)
	}
}

func TestAppsCatalog_FilterButtonHasNoColorVariant(t *testing.T) {
	body := renderComponent(t, AppsCatalog(adminUser(), nil, nil, AppsCatalogFilter{}))

	if !strings.Contains(body, `class="btn btn-sm"`) {
		t.Errorf("expected the Filter button to render plain btn-sm (no color variant), got %q", body)
	}
}

func TestEnvironmentDiff_DiffButtonPrimarySmall(t *testing.T) {
	body := renderComponent(t, EnvironmentDiff(adminUser(), nil, "", "", 0, nil))

	if !strings.Contains(body, `class="btn btn-primary btn-sm"`) {
		t.Errorf("expected the Diff button to render btn-primary btn-sm, got %q", body)
	}
}

// --- environment_form.templ: disabled Save carries variant + title --------
//
// role_gating_test.go's TestEnvironmentForm_AdminOnly_SaveAndArchiveLiveNoPromoterImplied
// covers the enabled Save path; nothing covered the disabled branch
// (htmxui.Button's disabled=true + title attrs forwarding).

func TestEnvironmentForm_SaveButtonDisabledCarriesPrimaryClassAndTitle(t *testing.T) {
	body := renderComponent(t, EnvironmentForm(noRolesUser(), "edit", environmentFormEditInput(), false, ""))

	if !strings.Contains(body, `class="btn btn-primary btn-disabled"`) {
		t.Errorf("expected the disabled Save button to still carry btn-primary (plus btn-disabled), got %q", body)
	}
	if !strings.Contains(body, `title="Requires role: `+components.RoleAdmin+`"`) {
		t.Errorf("expected the disabled Save button's title to name the missing role, got %q", body)
	}
	if !strings.Contains(body, ">Save environment<") {
		t.Errorf("expected the Save environment label, got %q", body)
	}
}
