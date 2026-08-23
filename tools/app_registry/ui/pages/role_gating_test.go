package pages

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/components"
	"github.com/whale-net/everything/tools/app_registry/ui/matrix"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// This file covers #652 (NFR-17, FR-42-46, FR-59): role gating over
// representative role subsets, exercised directly against the templ page
// components that actually branch on role (components.HasRole/Gate is only
// ever called from pages.Deployments, pages.Environments,
// pages.EnvironmentForm, and -- as of issue #1033's FR10/FR11 retry action --
// pages.PromotionDetails; every other read screen (01/11/12/13/20/21/22/
// 30/31/32/40) passes user through to components.Shell only for chrome and
// never branches on Roles; that is asserted structurally by
// components/roles_test.go and components/banner_test.go covering Shell's
// one role-sensitive piece, MisconfigBanner, plus a grep-verifiable absence
// of components.HasRole/Gate calls in every other pages/*.templ file).

// --- Representative role subsets (issue's four required principals) -------

func noRolesUser() *htmxauth.UserInfo     { return &htmxauth.UserInfo{Roles: []string{}} }
func absentRolesUser() *htmxauth.UserInfo { return &htmxauth.UserInfo{Roles: nil} }
func promoterDevUser() *htmxauth.UserInfo {
	return &htmxauth.UserInfo{Roles: []string{components.EnvironmentPromoterRole("dev")}}
}
func adminUser() *htmxauth.UserInfo    { return &htmxauth.UserInfo{Roles: []string{components.RoleAdmin}} }
func allRolesUser() *htmxauth.UserInfo { return &htmxauth.UserInfo{Roles: htmxauth.AllRoles} }

func renderComponent(t *testing.T, comp templ.Component) string {
	t.Helper()
	var buf strings.Builder
	if err := comp.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	return buf.String()
}

// buildTestMatrix constructs a matrix with one standalone-image row
// ("platform-worker") not currently promoted to any environment -- so every
// cell renders the "not promoted" promote affordance
// (pages/deployments.templ's matrixCell), the shape every role-subset
// assertion below inspects.
func buildTestMatrix(envs []*pb.Environment) *matrix.Matrix {
	apps := []*pb.App{
		{AppId: "app-1", Domain: "platform", FullName: "platform-worker", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
	}
	return matrix.Build(nil, apps, envs, map[string]matrix.ColumnResult{})
}

// promoteHref matches entityActionHref's exact rendering (url.Values.Encode
// sorts query params alphabetically, and templ HTML-escapes "&" to "&amp;"
// in an href attribute) for the fixed owner/kind buildTestMatrix's row
// uses.
func promoteHref(envKey string) string {
	return `href="/promote?env=` + envKey + `&amp;kind=image&amp;owner=platform-worker"`
}

func devStageProdEnvs() []*pb.Environment {
	return []*pb.Environment{
		{Key: "dev", DisplayName: "Dev", Rank: 0},
		{Key: "stage", DisplayName: "Stage", Rank: 1},
		{Key: "prod", DisplayName: "Prod", Rank: 2},
	}
}

// --- Subset 1: no roles (read-only viewer) --------------------------------

// FR-46: the deployments matrix is fully usable for a no-roles principal --
// every row/column renders (no empty regions), and FR-44: no enabled write
// control anywhere; every promote affordance is present but disabled, with
// the missing role named.
func TestDeployments_NoRoles_FullyReadableNoEnabledWriteControls(t *testing.T) {
	envs := devStageProdEnvs()
	m := buildTestMatrix(envs)
	body := renderComponent(t, Deployments(noRolesUser(), m, false))

	if !strings.Contains(body, "platform-worker") {
		t.Fatalf("expected the row to render (no empty region); body = %s", body)
	}
	for _, env := range envs {
		if !strings.Contains(body, env.GetKey()) {
			t.Errorf("expected environment column %q to render; body = %s", env.GetKey(), body)
		}
	}
	// FR-44: every promote control must render disabled, naming the
	// missing per-environment promoter role.
	for _, env := range envs {
		role := components.EnvironmentPromoterRole(env.GetKey())
		if !strings.Contains(body, `title="Requires role: `+role+`"`) {
			t.Errorf("expected a disabled control naming %q; body = %s", role, body)
		}
	}
	if strings.Contains(body, `href="/promote?`) {
		t.Errorf("no-roles viewer must never see a live, enabled promote link; body = %s", body)
	}
}

// FR-38/FR-44: environments screen (09) is fully readable for a no-roles
// principal; the Add/Edit admin controls render disabled, naming
// app-registry-admin, never omitted with no explanation.
func TestEnvironments_NoRoles_FullyReadableAdminControlsDisabled(t *testing.T) {
	envs := devStageProdEnvs()
	body := renderComponent(t, Environments(noRolesUser(), envs, false, nil))

	for _, env := range envs {
		if !strings.Contains(body, env.GetKey()) {
			t.Errorf("expected environment row %q to render; body = %s", env.GetKey(), body)
		}
	}
	if !strings.Contains(body, `title="Requires role: `+components.RoleAdmin+`"`) {
		t.Errorf("expected the disabled 'Add environment'/'Edit' controls to name %q; body = %s", components.RoleAdmin, body)
	}
	if strings.Contains(body, `href="/environments/new"`) || strings.Contains(body, `href="/environments/edit?`) {
		t.Errorf("no-roles viewer must never see a live, enabled admin control; body = %s", body)
	}
}

// --- Subset 2: app-registry-promoter-dev only ------------------------------

// FR-43: promote/rollback controls appear ONLY in the dev column --
// nowhere else, including stage, prod, and a newly created (non-dev/
// stage/prod) environment. No environment-admin control appears either.
func TestDeployments_PromoterDevOnly_OnlyDevColumnEnabled(t *testing.T) {
	envs := append(devStageProdEnvs(), &pb.Environment{Key: "canary-us-east", DisplayName: "Canary", Rank: 3})
	m := buildTestMatrix(envs)
	body := renderComponent(t, Deployments(promoterDevUser(), m, false))

	if strings.Contains(body, promoteHref("stage")) {
		t.Errorf("promoter-dev must not gain a live promote link into stage; body = %s", body)
	}
	if strings.Contains(body, promoteHref("prod")) {
		t.Errorf("promoter-dev must not gain a live promote link into prod; body = %s", body)
	}
	if strings.Contains(body, promoteHref("canary-us-east")) {
		t.Errorf("promoter-dev must not gain a live promote link into a newly created environment; body = %s", body)
	}
	if !strings.Contains(body, promoteHref("dev")) {
		t.Errorf("promoter-dev must have a live promote link into dev; body = %s", body)
	}
	for _, env := range []string{"stage", "prod", "canary-us-east"} {
		role := components.EnvironmentPromoterRole(env)
		if !strings.Contains(body, `title="Requires role: `+role+`"`) {
			t.Errorf("expected a disabled control naming %q for env %q; body = %s", role, env, body)
		}
	}
}

// FR-42: promoter-dev holds no environment-admin control.
func TestEnvironments_PromoterDevOnly_NoAdminControls(t *testing.T) {
	envs := devStageProdEnvs()
	body := renderComponent(t, Environments(promoterDevUser(), envs, false, nil))
	if strings.Contains(body, `href="/environments/new"`) || strings.Contains(body, `href="/environments/edit?`) {
		t.Errorf("promoter-dev must never see a live environment-admin control; body = %s", body)
	}
	if !strings.Contains(body, `title="Requires role: `+components.RoleAdmin+`"`) {
		t.Errorf("expected the disabled admin controls naming %q; body = %s", components.RoleAdmin, body)
	}
}

// --- Subset 3: app-registry-admin only --------------------------------------

// Environment create/edit/archive controls (09/53) are available; no role
// implies another, so an admin holds no promote/rollback control anywhere.
func TestEnvironments_AdminOnly_AdminControlsLiveNoPromoteImplied(t *testing.T) {
	envs := devStageProdEnvs()
	body := renderComponent(t, Environments(adminUser(), envs, false, nil))
	if !strings.Contains(body, `href="/environments/new"`) {
		t.Errorf("admin must see a live 'Add environment' control; body = %s", body)
	}
	for _, env := range envs {
		if !strings.Contains(body, `href="/environments/edit?key=`+env.GetKey()+`"`) {
			t.Errorf("admin must see a live 'Edit' control for %q; body = %s", env.GetKey(), body)
		}
	}
}

func TestDeployments_AdminOnly_NoPromoteControlInAnyColumn(t *testing.T) {
	envs := devStageProdEnvs()
	m := buildTestMatrix(envs)
	body := renderComponent(t, Deployments(adminUser(), m, false))
	if strings.Contains(body, `href="/promote?`) {
		t.Errorf("admin-only (no promoter role) must not gain any live promote link; body = %s", body)
	}
	for _, env := range envs {
		role := components.EnvironmentPromoterRole(env.GetKey())
		if !strings.Contains(body, `title="Requires role: `+role+`"`) {
			t.Errorf("expected a disabled control naming %q for env %q; body = %s", role, env.GetKey(), body)
		}
	}
}

// admin holds no promoter role even for the environment it just created --
// screen 53's promote-eligibility note is informational, not a grant.
func TestEnvironmentForm_AdminOnly_SaveAndArchiveLiveNoPromoterImplied(t *testing.T) {
	body := renderComponent(t, EnvironmentForm(adminUser(), "edit", EnvironmentFormInput{Key: "dev"}, false, ""))
	// The Save control is htmxui.Button-rendered (FR2): assert on its
	// attributes/label independently rather than one exact-order literal
	// tag string, since Button's generated markup writes class before the
	// caller's attrs (attribute order carries no rendered meaning).
	if !strings.Contains(body, `class="btn btn-primary"`) || !strings.Contains(body, `type="submit"`) || !strings.Contains(body, `Save environment`) || strings.Contains(body, "btn-disabled") {
		t.Errorf("admin must see a live, enabled Save control; body = %s", body)
	}
	if strings.Contains(body, "Read-only.") {
		t.Errorf("admin must not see the read-only warning banner; body = %s", body)
	}
}

// --- Subset 4: all roles ----------------------------------------------------

func TestDeployments_AllRoles_EveryColumnEnabled(t *testing.T) {
	envs := append(devStageProdEnvs(), &pb.Environment{Key: "canary-us-east", Rank: 3})
	m := buildTestMatrix(envs)
	body := renderComponent(t, Deployments(allRolesUser(), m, false))
	for _, env := range envs {
		if !strings.Contains(body, promoteHref(env.GetKey())) {
			t.Errorf("all-roles principal must have a live promote link into %q; body = %s", env.GetKey(), body)
		}
	}
	if strings.Contains(body, "Requires role:") {
		t.Errorf("all-roles principal must see no disabled/missing-role control; body = %s", body)
	}
}

func TestEnvironments_AllRoles_AdminControlsLive(t *testing.T) {
	envs := devStageProdEnvs()
	body := renderComponent(t, Environments(allRolesUser(), envs, false, nil))
	if strings.Contains(body, "Requires role:") {
		t.Errorf("all-roles principal must see no disabled/missing-role control; body = %s", body)
	}
	if !strings.Contains(body, `href="/environments/new"`) {
		t.Errorf("all-roles principal must have a live 'Add environment' control; body = %s", body)
	}
}

// --- PromotionDetails "Retry refresh/sync" button (FR10/FR11, issue #1033) -

// promotionDetailsState builds a minimal PromotionDetailsViewState whose
// Details is non-nil (so PromotionDetails renders promotionDetailsBody, and
// therefore retryArgoSyncButton) for a given promotion_id -- the only field
// retryArgoSyncButton's own href depends on.
func promotionDetailsState(promotionID string) PromotionDetailsViewState {
	return PromotionDetailsViewState{
		PromotionID: promotionID,
		Details: &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
			Promotion: &pb.Promotion{PromotionId: promotionID},
		}},
	}
}

// TestPromotionDetails_NoRoles_RetryButtonDisabled proves FR11: a no-roles
// viewer sees the retry control rendered disabled, naming app-registry-admin
// -- never omitted with no explanation, never a live control.
func TestPromotionDetails_NoRoles_RetryButtonDisabled(t *testing.T) {
	body := renderComponent(t, PromotionDetails(noRolesUser(), promotionDetailsState("promo-1")))
	if !strings.Contains(body, "Retry refresh/sync") {
		t.Fatalf("expected the retry control to render (disabled, not omitted); body = %s", body)
	}
	if !strings.Contains(body, `title="Requires role: `+components.RoleAdmin+`"`) {
		t.Errorf("expected the disabled retry control to name %q; body = %s", components.RoleAdmin, body)
	}
	if strings.Contains(body, `action="/promotions/promo-1/retry"`) && !strings.Contains(body, "btn-disabled") {
		t.Errorf("no-roles viewer must never see a live, enabled retry control; body = %s", body)
	}
}

// TestPromotionDetails_PromoterDevOnly_RetryButtonDisabled proves FR10/FR11
// that holding a promoter role -- real write power elsewhere on this exact
// promotion (Promote/Rollback) -- is still not admin; no role implies
// another (same principle role_gating_test.go's other subsets assert).
func TestPromotionDetails_PromoterDevOnly_RetryButtonDisabled(t *testing.T) {
	body := renderComponent(t, PromotionDetails(promoterDevUser(), promotionDetailsState("promo-1")))
	if !strings.Contains(body, `title="Requires role: `+components.RoleAdmin+`"`) {
		t.Errorf("expected the disabled retry control to name %q for a promoter-only session; body = %s", components.RoleAdmin, body)
	}
}

// TestPromotionDetails_AdminOnly_RetryButtonLive proves FR10: an
// app-registry-admin session gets a live, enabled retry submit button
// posting to /promotions/{id}/retry.
func TestPromotionDetails_AdminOnly_RetryButtonLive(t *testing.T) {
	body := renderComponent(t, PromotionDetails(adminUser(), promotionDetailsState("promo-1")))
	if !strings.Contains(body, `action="/promotions/promo-1/retry"`) {
		t.Errorf("expected a form posting to /promotions/promo-1/retry; body = %s", body)
	}
	if !strings.Contains(body, `<button type="submit" class="btn btn-sm btn-warning">Retry refresh/sync</button>`) {
		t.Errorf("expected a live, enabled retry submit button; body = %s", body)
	}
	if strings.Contains(body, "Requires role:") {
		t.Errorf("admin must see no disabled/missing-role retry control; body = %s", body)
	}
}

// TestPromotionDetails_AllRoles_RetryButtonLive is the all-roles-subset
// parity check every other feature in this file gets.
func TestPromotionDetails_AllRoles_RetryButtonLive(t *testing.T) {
	body := renderComponent(t, PromotionDetails(allRolesUser(), promotionDetailsState("promo-1")))
	if strings.Contains(body, "Requires role:") {
		t.Errorf("all-roles principal must see no disabled/missing-role retry control; body = %s", body)
	}
	if !strings.Contains(body, `action="/promotions/promo-1/retry"`) {
		t.Errorf("expected a form posting to /promotions/promo-1/retry; body = %s", body)
	}
}

// TestPromotionDetails_RetryErr_RendersBanner proves handleRetryArgoSync's
// RetryErr (a failed RetryArgoSync submit, distinct from LoadErr) surfaces
// as its own inline error banner, alongside the promotion's own content --
// never a silent failure and never mistaken for a failed GetPromotionDetails
// read.
func TestPromotionDetails_RetryErr_RendersBanner(t *testing.T) {
	s := promotionDetailsState("promo-1")
	s.RetryErr = "permission denied"
	body := renderComponent(t, PromotionDetails(adminUser(), s))
	if !strings.Contains(body, "alert-error") || !strings.Contains(body, "Retry failed: permission denied") {
		t.Errorf("expected a RetryErr banner naming the failure; body = %s", body)
	}
}

// --- FR-59: absent vs empty (both distinct from AUTH_MODE=none) ------------

// A no-roles (present, empty) principal never sees the misconfiguration
// banner -- this is a legitimate read-only viewer, not a deployment error.
func TestShell_EmptyRoles_NoMisconfigBanner(t *testing.T) {
	body := renderComponent(t, Environments(noRolesUser(), nil, false, nil))
	if strings.Contains(body, "Roles claim missing from this session") {
		t.Errorf("empty (present) roles must never trigger the FR-59 banner; body = %s", body)
	}
}

// Absent roles (nil claim) trips the persistent banner on every screen that
// goes through components.Shell -- asserted here via one representative
// read-sensitive screen; the banner itself (including its exact wording and
// alert-error styling) is covered directly in
// components/banner_test.go, and read content still renders underneath it.
func TestShell_AbsentRoles_MisconfigBannerAndReadContentBothRender(t *testing.T) {
	envs := devStageProdEnvs()
	body := renderComponent(t, Environments(absentRolesUser(), envs, false, nil))
	if !strings.Contains(body, "Roles claim missing from this session") {
		t.Errorf("absent roles claim must trigger the FR-59 banner; body = %s", body)
	}
	for _, env := range envs {
		if !strings.Contains(body, env.GetKey()) {
			t.Errorf("read content must still render underneath the banner (FR-59: never lock out diagnosis); body = %s", body)
		}
	}
}

func TestShell_AuthModeNoneSentinel_NoMisconfigBannerEverythingAvailable(t *testing.T) {
	envs := devStageProdEnvs()
	body := renderComponent(t, Environments(allRolesUser(), envs, false, nil))
	if strings.Contains(body, "Roles claim missing from this session") {
		t.Errorf("AUTH_MODE=none must never show the FR-59 banner; body = %s", body)
	}
	if !strings.Contains(body, `href="/environments/new"`) {
		t.Errorf("AUTH_MODE=none must have every control available; body = %s", body)
	}
}
