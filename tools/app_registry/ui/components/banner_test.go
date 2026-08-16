package components

import (
	"context"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

func renderMisconfigBanner(t *testing.T, user *htmxauth.UserInfo) string {
	t.Helper()
	var buf strings.Builder
	if err := MisconfigBanner(user).Render(context.Background(), &buf); err != nil {
		t.Fatalf("MisconfigBanner render failed: %v", err)
	}
	return buf.String()
}

// FR-59: roles claim absent (nil Roles) -> persistent, explicit error
// banner naming the probable cause.
func TestMisconfigBanner_RolesAbsent_RendersErrorBanner(t *testing.T) {
	body := renderMisconfigBanner(t, &htmxauth.UserInfo{Roles: nil})
	if !strings.Contains(body, "alert-error") {
		t.Errorf("expected alert-error banner for absent roles claim; body = %s", body)
	}
	if !strings.Contains(body, "Add to ID token") {
		t.Errorf("expected the banner to name the probable cause (missing Keycloak realm-roles mapper); body = %s", body)
	}
}

// FR-59: roles claim present but empty (legitimate read-only viewer) ->
// no banner at all.
func TestMisconfigBanner_RolesEmpty_RendersNothing(t *testing.T) {
	body := renderMisconfigBanner(t, &htmxauth.UserInfo{Roles: []string{}})
	if strings.TrimSpace(body) != "" {
		t.Errorf("expected no banner for a present-but-empty roles claim; body = %s", body)
	}
}

// FR-50/FR-59: AUTH_MODE=none's AllRoles sentinel must never trip the
// misconfiguration banner -- it is non-nil, so RolesMisconfigured's nil
// check already exempts it, but assert the rendered banner directly too
// since this is the surface an operator actually sees.
func TestMisconfigBanner_AuthModeNoneSentinel_RendersNothing(t *testing.T) {
	body := renderMisconfigBanner(t, &htmxauth.UserInfo{Roles: htmxauth.AllRoles})
	if strings.TrimSpace(body) != "" {
		t.Errorf("expected no banner under AUTH_MODE=none; body = %s", body)
	}
}

func TestMisconfigBanner_NilUser_RendersNothing(t *testing.T) {
	body := renderMisconfigBanner(t, nil)
	if strings.TrimSpace(body) != "" {
		t.Errorf("expected no banner for a nil user; body = %s", body)
	}
}
