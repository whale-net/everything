package components

import (
	"context"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

func renderLayout(t *testing.T, data LayoutData) string {
	t.Helper()
	var buf strings.Builder
	if err := Layout(data).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Layout render failed: %v", err)
	}
	return buf.String()
}

// TestLayout_RendersSignedInUser proves the shared chrome actually
// surfaces the signed-in Keycloak operator's identity (this task's NFR1
// scope, issue #2236's Testing section: "shell renders the signed-in
// user"), not merely that it accepts a *htmxauth.UserInfo field without
// using it.
//
// Red/green discipline (verified by hand, then reverted): temporarily
// changing headerRight's call to
// `@htmxui.UserMenu(htmxui.UserMenuData{IdentityLabel: "", LogoutHref: "/logout"})`
// (dropping the userIdentityLabel(data.User) wiring) made this test fail
// with "expected identity label"; restoring userIdentityLabel(data.User)
// made it pass again.
func TestLayout_RendersSignedInUser(t *testing.T) {
	user := &htmxauth.UserInfo{Name: "Alice Operator", PreferredUsername: "alice"}
	body := renderLayout(t, LayoutData{Title: "whagent-net", User: user})

	if !strings.Contains(body, "Alice Operator") {
		t.Errorf("expected signed-in user's identity label %q in rendered shell, got %q", "Alice Operator", body)
	}
	if !strings.Contains(body, "data-htmxui-user-menu") {
		t.Errorf("expected a UserMenu instance in rendered shell, got %q", body)
	}
	if !strings.Contains(body, `href="/logout"`) {
		t.Errorf("expected logout affordance pointing at /logout, got %q", body)
	}
}

// TestLayout_PreferredUsernameFallsBackWhenNameEmpty guards
// userIdentityLabel's fallback: a token missing the optional "name" claim
// still carries "preferred_username", and the shell must still show
// something rather than a blank identity label.
func TestLayout_PreferredUsernameFallsBackWhenNameEmpty(t *testing.T) {
	user := &htmxauth.UserInfo{PreferredUsername: "alice"}
	body := renderLayout(t, LayoutData{Title: "whagent-net", User: user})

	if !strings.Contains(body, "alice") {
		t.Errorf("expected PreferredUsername fallback %q in rendered shell, got %q", "alice", body)
	}
}

// TestLayout_NilUserOmitsUserMenu guards the signed-out path (nil User,
// e.g. AUTH_MODE=none with no session yet): Layout must not render a
// dangling logout link with no identity behind it (mirrors
// htmxui.UserMenu's own "empty label renders nothing" contract).
func TestLayout_NilUserOmitsUserMenu(t *testing.T) {
	body := renderLayout(t, LayoutData{Title: "whagent-net"})
	if strings.Contains(body, "/logout") {
		t.Errorf("expected no logout affordance when User is nil, got %q", body)
	}
	if strings.Contains(body, "data-htmxui-user-menu") {
		t.Errorf("expected no UserMenu instance when User is nil, got %q", body)
	}
}
