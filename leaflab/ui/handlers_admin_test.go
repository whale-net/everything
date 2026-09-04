package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
)

// --- handleAdminBoards (#1777: FR11, the screen half of FR14) --------------

// TestHandleAdminBoards_Admin_RendersOwnedBoardsAndUsers covers the happy
// path: an admin caller (ListOwnedBoards succeeds) gets the boards table
// rendered, sourced entirely from leaflab-api's response.
func TestHandleAdminBoards_Admin_RendersOwnedBoardsAndUsers(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		ownedBoardsResp: &leaflabapipb.ListOwnedBoardsResponse{
			Boards: []*leaflabapipb.OwnedBoard{
				{BoardId: 100, DeviceId: "leaflab-aaaaaaaaaaaa", Owner: &leaflabapipb.LeafLabUser{LeaflabUserId: 2, DisplayName: "Alice"}},
			},
		},
		usersResp: &leaflabapipb.ListUsersResponse{
			Users: []*leaflabapipb.LeafLabUser{{LeaflabUserId: 2, DisplayName: "Alice"}, {LeaflabUserId: 3, DisplayName: "Bob"}},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := httptest.NewRequest(http.MethodGet, "/admin/boards", nil)
	rec := httptest.NewRecorder()
	app.handleAdminBoards(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"leaflab-aaaaaaaaaaaa", "Alice", "Bob"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the rendered page, got %q", want, body)
		}
	}
}

// TestHandleAdminBoards_PermissionDenied_RendersForbiddenPage_Not500 is
// Testing criterion 17: a codes.PermissionDenied response from
// ListOwnedBoards (a signed-in non-admin) renders the dedicated
// pages.AdminForbidden page -- a real HTTP 403, not a 500 and not a
// redirect loop.
func TestHandleAdminBoards_PermissionDenied_RendersForbiddenPage_Not500(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		ownedBoardsErr: status.Error(codes.PermissionDenied, "caller does not hold the admin role"),
	}
	app := &App{api: &LeafLabClient{api: fake}}

	req := httptest.NewRequest(http.MethodGet, "/admin/boards", nil)
	rec := httptest.NewRecorder()
	app.handleAdminBoards(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (dedicated forbidden page, not a 500 or a redirect)", rec.Code, http.StatusForbidden)
	}
	if !strings.Contains(rec.Body.String(), "restricted to users holding the admin role") {
		t.Errorf("expected the AdminForbidden page's message, got %q", rec.Body.String())
	}
}

// TestHandleAdminBoards_Unauthenticated_RedirectsToLogin mirrors
// TestHandleBoards_Unauthenticated_RedirectsToLogin for the admin route --
// distinct from PermissionDenied (signed in, not admin): Unauthenticated
// (not signed in at all) still gets the same re-authenticate redirect every
// other route uses, not the AdminForbidden page.
func TestHandleAdminBoards_Unauthenticated_RedirectsToLogin(t *testing.T) {
	fake := &fakeLeafLabAPIClient{ownedBoardsErr: status.Error(codes.Unauthenticated, "token revoked")}
	app := &App{api: &LeafLabClient{api: fake}}

	req := httptest.NewRequest(http.MethodGet, "/admin/boards", nil)
	rec := httptest.NewRecorder()
	app.handleAdminBoards(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/auth/login?next=/admin/boards"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// TestHandleAdminBoards_GenericError_RendersErrorState_Not403 proves a
// non-PermissionDenied, non-Unauthenticated failure still renders the page
// (status 200) with a visible load error, not the AdminForbidden 403 page
// and not a 500 -- distinguishes a genuine load failure from FR14's denial.
func TestHandleAdminBoards_GenericError_RendersErrorState_Not403(t *testing.T) {
	fake := &fakeLeafLabAPIClient{ownedBoardsErr: status.Error(codes.Internal, "boom")}
	app := &App{api: &LeafLabClient{api: fake}}

	req := httptest.NewRequest(http.MethodGet, "/admin/boards", nil)
	rec := httptest.NewRecorder()
	app.handleAdminBoards(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (error rendered on the page, not a 403 and not a 500)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Failed to load owned boards") {
		t.Errorf("expected the load-error message in the rendered page, got %q", rec.Body.String())
	}
}

// --- handleReassignBoardOwner / handleClearBoardOwner (#1777: FR12, FR13) --

func newReassignBoardOwnerRequest(boardID, newOwnerID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/admin/boards/"+boardID+"/reassign", strings.NewReader("new_owner_leaflab_user_id="+newOwnerID))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("board_id", boardID)
	return req
}

func newClearBoardOwnerRequest(boardID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/admin/boards/"+boardID+"/clear", nil)
	req.SetPathValue("board_id", boardID)
	return req
}

// TestHandleReassignBoardOwner_Success_RerendersTableFragment covers the
// happy path: a successful reassign is followed by a re-fetch, and the
// "#owned-boards-table" fragment reflects the (re-fetched) current state.
func TestHandleReassignBoardOwner_Success_RerendersTableFragment(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		ownedBoardsResp: &leaflabapipb.ListOwnedBoardsResponse{
			Boards: []*leaflabapipb.OwnedBoard{
				{BoardId: 100, DeviceId: "leaflab-aaaaaaaaaaaa", Owner: &leaflabapipb.LeafLabUser{LeaflabUserId: 3, DisplayName: "Bob"}},
			},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleReassignBoardOwner(rec, newReassignBoardOwnerRequest("100", "3"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Bob") {
		t.Errorf("expected the re-fetched owner in the fragment, got %q", rec.Body.String())
	}
}

// TestHandleReassignBoardOwner_MalformedBoardID_NotFound mirrors
// TestHandleBoardDetail_MalformedBoardID_NotFound: a non-numeric board_id
// path segment short-circuits to a real 404 before any RPC is attempted.
func TestHandleReassignBoardOwner_MalformedBoardID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleReassignBoardOwner(rec, newReassignBoardOwnerRequest("not-a-number", "3"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandleReassignBoardOwner_FailedPrecondition_StillRerendersFragment
// proves a rejected reassign (e.g. unowned board, or reassign to the
// current owner) still re-renders the fragment (status 200), not a 500 --
// mirroring handleRenameBoard's inline-error convention, since the
// fragment always reflects what leaflab-api actually has.
func TestHandleReassignBoardOwner_FailedPrecondition_StillRerendersFragment(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		reassignErr: status.Error(codes.FailedPrecondition, "board 100 is already owned by user 3"),
		ownedBoardsResp: &leaflabapipb.ListOwnedBoardsResponse{
			Boards: []*leaflabapipb.OwnedBoard{
				{BoardId: 100, DeviceId: "leaflab-aaaaaaaaaaaa", Owner: &leaflabapipb.LeafLabUser{LeaflabUserId: 2, DisplayName: "Alice"}},
			},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleReassignBoardOwner(rec, newReassignBoardOwnerRequest("100", "3"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (re-rendered fragment, not a 500)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Alice") {
		t.Errorf("expected the unchanged current owner still shown, got %q", rec.Body.String())
	}
}

// TestHandleClearBoardOwner_Success_RerendersTableFragment covers the happy
// path: a successful clear is followed by a re-fetch, and the board no
// longer appears in the "#owned-boards-table" fragment (it's unowned now).
func TestHandleClearBoardOwner_Success_RerendersTableFragment(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		ownedBoardsResp: &leaflabapipb.ListOwnedBoardsResponse{}, // board 100 no longer owned
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleClearBoardOwner(rec, newClearBoardOwnerRequest("100"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "leaflab-aaaaaaaaaaaa") {
		t.Errorf("expected the cleared board to be absent from the re-fetched fragment, got %q", rec.Body.String())
	}
}

// TestHandleClearBoardOwner_MalformedBoardID_NotFound mirrors
// TestHandleReassignBoardOwner_MalformedBoardID_NotFound.
func TestHandleClearBoardOwner_MalformedBoardID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleClearBoardOwner(rec, newClearBoardOwnerRequest("not-a-number"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
