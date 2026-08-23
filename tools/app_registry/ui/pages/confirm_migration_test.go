package pages

import (
	"strings"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/ui/components"
)

// This file guards the FR1b migration of promote.templ, rollback.templ,
// and environment_form.templ's three independent danger-zone/confirm
// blocks onto the single shared htmxui.Confirm component (issue #1005).
// The pre-existing handler tests in this package exercise submission
// behavior (idempotency keys, form re-render) but never assert the
// rendered Confirm markup itself -- the override-acknowledge checkbox,
// the destructive/neutral button styling, the disabled+reason wiring, or
// the danger-zone card chrome -- so a mismapped ConfirmProps field
// (wrong Intent, dropped Disabled/DisabledReason, a re-ordered slot) would
// pass every existing test. These tests close that gap.

// --- promote.templ: override-acknowledge checkbox + disabled wiring -------

func TestPromote_ConfirmRendersOverrideCheckboxWhenRequireOverride(t *testing.T) {
	s := PromoteViewState{
		Owner:           "platform-worker",
		Kind:            "image",
		EnvKey:          "dev",
		RequireOverride: true,
		OverrideAck:     true,
	}
	body := renderComponent(t, Promote(promoterDevUser(), s))

	want := `<input type="checkbox" name="override_ack" value="true" class="checkbox checkbox-error" checked>`
	if !strings.Contains(body, want) {
		t.Errorf("expected override-ack checkbox %q rendered inside Confirm's children slot, got %q", want, body)
	}
	// Neutral intent (promote.templ passes htmxui.ConfirmNeutral) must render
	// a primary, not error-variant, submit button.
	if !strings.Contains(body, `class="btn btn-primary"`) {
		t.Errorf("expected neutral-intent Confirm to render a btn-primary submit control, got %q", body)
	}
	if strings.Contains(body, "btn-error") {
		t.Errorf("expected no btn-error class on promote's neutral-intent Confirm, got %q", body)
	}
}

func TestPromote_ConfirmOmitsOverrideCheckboxWhenNotRequireOverride(t *testing.T) {
	s := PromoteViewState{
		Owner:  "platform-worker",
		Kind:   "image",
		EnvKey: "dev",
	}
	body := renderComponent(t, Promote(promoterDevUser(), s))
	if strings.Contains(body, "override_ack") {
		t.Errorf("expected no override_ack checkbox when RequireOverride is false, got %q", body)
	}
}

func TestPromote_ConfirmDisabledCarriesMissingRoleAsTitle(t *testing.T) {
	s := PromoteViewState{
		Owner:  "platform-worker",
		Kind:   "image",
		EnvKey: "dev",
	}
	// noRolesUser holds no promoter role for "dev" -- GateCellAction denies,
	// so Confirm's submit must render disabled with the missing-role reason
	// as its title tooltip (not silently omitted or swapped for a different
	// message).
	body := renderComponent(t, Promote(noRolesUser(), s))
	role := components.EnvironmentPromoterRole("dev")
	wantTitle := `title="Requires role: ` + role + `"`
	if !strings.Contains(body, wantTitle) {
		t.Errorf("expected disabled submit control with title %q, got %q", wantTitle, body)
	}
	if !strings.Contains(body, "disabled") {
		t.Errorf("expected the submit control itself to carry the disabled attribute, got %q", body)
	}
}

// --- rollback.templ: destructive styling, cancel affordance, NoHistory ----

func TestRollback_ConfirmDestructiveStylingAndCancelHref(t *testing.T) {
	s := RollbackViewState{
		Owner:  "platform-worker",
		Kind:   "image",
		EnvKey: "dev",
	}
	body := renderComponent(t, Rollback(promoterDevUser(), s))

	if !strings.Contains(body, "btn-error") {
		t.Errorf("expected rollback's destructive-intent Confirm to render a btn-error submit control, got %q", body)
	}
	if !strings.Contains(body, `href="/deployments"`) {
		t.Errorf("expected Confirm's CancelHref wired to /deployments, got %q", body)
	}
	if !strings.Contains(body, "Confirm rollback") {
		t.Errorf("expected the rollback submit label present, got %q", body)
	}
}

func TestRollback_NoHistoryRendersDisabledConfirmWithReason(t *testing.T) {
	s := RollbackViewState{
		Owner:           "platform-worker",
		Kind:            "image",
		EnvKey:          "dev",
		NoHistory:       true,
		NoHistoryReason: "no prior promotion exists for this target",
	}
	body := renderComponent(t, Rollback(promoterDevUser(), s))

	wantTitle := `title="no prior promotion exists for this target"`
	if !strings.Contains(body, wantTitle) {
		t.Errorf("expected NoHistoryReason wired to Confirm's DisabledReason title, got %q", body)
	}
	if !strings.Contains(body, "disabled") {
		t.Errorf("expected the NoHistory Confirm's submit control to be disabled, got %q", body)
	}
	// This branch never renders the full rollback form (no Reason textarea,
	// no cancel link) -- Confirm is called bare with just Disabled+Reason.
	if strings.Contains(body, `name="reason"`) {
		t.Errorf("expected no reason textarea rendered in the NoHistory branch, got %q", body)
	}
}

// --- environment_form.templ: danger-zone chrome, archive button, disabled -

func environmentFormEditInput() EnvironmentFormInput {
	return EnvironmentFormInput{Key: "canary-us-east", DisplayName: "Canary US East"}
}

func TestEnvironmentForm_DangerZoneRendersDestructiveConfirmWhenNotArchived(t *testing.T) {
	body := renderComponent(t, EnvironmentForm(adminUser(), "edit", environmentFormEditInput(), false, ""))

	if !strings.Contains(body, "⚠️ Danger Zone") {
		t.Errorf("expected the danger-zone ZoneTitle header to render, got %q", body)
	}
	if !strings.Contains(body, "border-error") {
		t.Errorf("expected the danger-zone bordered card chrome, got %q", body)
	}
	wantLabel := ">Archive canary-us-east<"
	if !strings.Contains(body, wantLabel) {
		t.Errorf("expected archive submit label %q, got %q", wantLabel, body)
	}
	if !strings.Contains(body, "btn-error") {
		t.Errorf("expected the archive Confirm's destructive-intent submit control (btn-error), got %q", body)
	}
}

func TestEnvironmentForm_DangerZoneConfirmDisabledForNonAdmin(t *testing.T) {
	body := renderComponent(t, EnvironmentForm(noRolesUser(), "edit", environmentFormEditInput(), false, ""))

	wantTitle := `title="Requires role: ` + components.RoleAdmin + `"`
	if !strings.Contains(body, wantTitle) {
		t.Errorf("expected the archive Confirm's disabled submit to carry the missing-role title %q, got %q", wantTitle, body)
	}
}

func TestEnvironmentForm_ArchivedRendersStaticCardNotConfirm(t *testing.T) {
	body := renderComponent(t, EnvironmentForm(adminUser(), "edit", environmentFormEditInput(), true, ""))

	if !strings.Contains(body, "⚠️ Danger Zone") {
		t.Errorf("expected the danger-zone header to still render for an archived environment, got %q", body)
	}
	if strings.Contains(body, "Archive canary-us-east") {
		t.Errorf("expected no archive submit control once the environment is already archived, got %q", body)
	}
	if strings.Contains(body, `action="/environments/archive"`) {
		t.Errorf("expected no archive form once already archived, got %q", body)
	}
}
