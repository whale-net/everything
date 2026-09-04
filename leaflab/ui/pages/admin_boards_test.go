package pages

import (
	"errors"
	"strings"
	"testing"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/ui/components"
)

// TestAdminBoards_OneRowPerOwnedBoard_WithOwnerDisplayName is Testing
// criterion 15's first half: AdminBoards renders one row per owned board,
// each carrying its owner's display name.
func TestAdminBoards_OneRowPerOwnedBoard_WithOwnerDisplayName(t *testing.T) {
	boards := []*leaflabapipb.OwnedBoard{
		{BoardId: 100, DeviceId: "leaflab-aaaaaaaaaaaa", BoardName: "greenhouse", Owner: &leaflabapipb.LeafLabUser{LeaflabUserId: 2, DisplayName: "Alice"}},
		{BoardId: 200, DeviceId: "leaflab-bbbbbbbbbbbb", BoardName: "grow-tent", Owner: &leaflabapipb.LeafLabUser{LeaflabUserId: 3, DisplayName: "Bob"}},
	}
	body := renderPage(t, AdminBoards(layoutData(), boards, nil, nil))

	for _, want := range []string{"greenhouse", "leaflab-aaaaaaaaaaaa", "Alice", "grow-tent", "leaflab-bbbbbbbbbbbb", "Bob"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the rendered output, got %q", want, body)
		}
	}
	// One row per board -- exactly two reassign forms and two clear forms.
	if got := strings.Count(body, "/admin/boards/100/reassign"); got != 1 {
		t.Errorf("expected exactly 1 reassign form for board 100, got %d", got)
	}
	if got := strings.Count(body, "/admin/boards/200/reassign"); got != 1 {
		t.Errorf("expected exactly 1 reassign form for board 200, got %d", got)
	}
}

// TestOwnedBoardsTable_FallsBackToDeviceIDWhenUnnamed is Testing criterion
// 15's second half: a board with an empty board_name falls back to
// device_id, mirroring boards.templ's boardDisplayName precedent (helpers.go
// is shared, not reimplemented here).
func TestOwnedBoardsTable_FallsBackToDeviceIDWhenUnnamed(t *testing.T) {
	boards := []*leaflabapipb.OwnedBoard{
		{BoardId: 100, DeviceId: "leaflab-aaaaaaaaaaaa", BoardName: "", Owner: &leaflabapipb.LeafLabUser{LeaflabUserId: 2, DisplayName: "Alice"}},
	}
	body := renderPage(t, OwnedBoardsTable(boards, nil))

	if !strings.Contains(body, "leaflab-aaaaaaaaaaaa") {
		t.Errorf("expected the device_id fallback in the rendered output, got %q", body)
	}
}

// TestAdminBoards_LoadError_RendersAlert_NotForbidden proves a non-nil
// loadErr renders the generic load-error alert (a genuine failure, e.g.
// Internal/transport) -- distinct from AdminForbidden, which
// handlers_admin.go renders instead for codes.PermissionDenied without ever
// calling AdminBoards at all (see AdminBoards' own doc comment).
func TestAdminBoards_LoadError_RendersAlert_NotForbidden(t *testing.T) {
	body := renderPage(t, AdminBoards(layoutData(), nil, nil, errors.New("boom")))

	if !strings.Contains(body, "Failed to load owned boards") {
		t.Errorf("expected the load-error alert, got %q", body)
	}
	if strings.Contains(body, "restricted to users holding the admin role") {
		t.Errorf("expected the generic load-error alert, not the AdminForbidden message, got %q", body)
	}
}

// TestAdminBoards_ZeroOwnedBoards_RendersEmptyState proves zero owned
// boards (a legitimate, non-error state) renders the empty-state message,
// not an error and not a raw empty table.
func TestAdminBoards_ZeroOwnedBoards_RendersEmptyState(t *testing.T) {
	body := renderPage(t, AdminBoards(layoutData(), nil, nil, nil))

	if !strings.Contains(body, "No boards are currently owned.") {
		t.Errorf("expected the empty-state message, got %q", body)
	}
}

// TestAdminForbidden_RendersRestrictedMessage is the page-level half of
// Testing criterion 17: pages.AdminForbidden itself renders the
// access-restricted message regardless of who calls it -- handlers_admin.go
// is what's responsible for choosing to call it on PermissionDenied
// (handlers_admin_test.go covers that decision).
func TestAdminForbidden_RendersRestrictedMessage(t *testing.T) {
	body := renderPage(t, AdminForbidden(layoutData()))

	if !strings.Contains(body, "restricted to users holding the admin role") {
		t.Errorf("expected the restricted-access message, got %q", body)
	}
}

// -- Testing criterion 16: admin nav link visibility ------------------------

// TestLayout_AdminNavLink_RendersOnlyWhenIsAdmin proves components.Layout's
// nav renders the "Admin" link iff LayoutData.IsAdmin is true -- exercised
// through AdminBoards (any page built on components.Layout would do; this
// screen is the one the link points at) rather than a bespoke fixture page.
func TestLayout_AdminNavLink_RendersOnlyWhenIsAdmin(t *testing.T) {
	const adminLink = `<a href="/admin/boards">Admin</a>`

	adminData := components.LayoutData{Title: "Board Ownership", IsAdmin: true}
	adminBody := renderPage(t, AdminBoards(adminData, nil, nil, nil))
	if !strings.Contains(adminBody, adminLink) {
		t.Errorf("expected the Admin nav link when IsAdmin=true, got %q", adminBody)
	}

	nonAdminData := components.LayoutData{Title: "Board Ownership", IsAdmin: false}
	nonAdminBody := renderPage(t, AdminBoards(nonAdminData, nil, nil, nil))
	if strings.Contains(nonAdminBody, adminLink) {
		t.Errorf("expected no Admin nav link when IsAdmin=false, got %q", nonAdminBody)
	}
}
