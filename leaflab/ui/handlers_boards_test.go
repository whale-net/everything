package main

// Covers #1330's Testing section for the boards screen itself (the
// auth/session/degraded-page coverage lives in handlers_auth_test.go,
// carried over from #1329):
//   - authenticated render comes from the gRPC response, not a direct DB
//     query (this BFF has no DB handle for leaflab domain data at all --
//     App only ever holds a session pool, see main.go's NewApp)
//   - FR61 pagination: two pages, no duplicates, cap respected
//   - NFR18.2 parity: the same ListBoards call with the same token from a
//     plain gRPC client returns the same board set the screen rendered

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// board is a small fixture constructor -- LastSeenAt only needs to be
// non-nil for these tests, its exact value is exercised by
// components/elapsed_test.go instead.
func board(deviceID string, boardID int64) *pb.BoardInfo {
	return &pb.BoardInfo{
		DeviceId:   deviceID,
		BoardId:    boardID,
		LastSeenAt: &pb.Instant{UnixMillis: 1000},
	}
}

// --- render comes from the gRPC response, not a DB ------------------------

// TestHandleBoards_RendersRowsFromGRPCResponse proves the rendered table
// contains exactly the boards fakeLeafLabClient's ListBoards returned --
// the App struct handlers_boards_test.go builds here holds no DB handle at
// all (unlike App in main.go, which only ever opens one for htmxauth
// sessions), so there is no path by which this render could have come from
// anywhere but the gRPC response.
func TestHandleBoards_RendersRowsFromGRPCResponse(t *testing.T) {
	fake := &fakeLeafLabClient{
		boardsResp: &pb.ListBoardsResponse{
			Boards:    []*pb.BoardInfo{board("device-alpha", 101), board("device-beta", 202)},
			ServerNow: &pb.Instant{UnixMillis: 2000},
		},
	}
	app := &App{auth: devAuth(t), api: &APIClient{LeafLab: fake}}

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	w := httptest.NewRecorder()
	app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBoards))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"device-alpha", "101", "device-beta", "202"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected rendered board data %q in body, got: %s", want, body)
		}
	}
	if fake.boardsCalls != 1 {
		t.Errorf("ListBoards calls = %d, want 1", fake.boardsCalls)
	}
}

// --- FR61 pagination --------------------------------------------------

// pagingFakeLeafLabClient is a paging-aware fakeLeafLabClient replacement:
// it returns a distinct capped page per page_token, so pagination tests
// can drive handleBoards (page one) then handleBoardsRows (every "Load
// more" continuation) and assert the combined result has no duplicates and
// respects the server's own cap -- exactly the FR61 contract
// boards_data.go's fetchBoardsPage relies on ("leaflab-api's own
// contract.ClampPageSize... enforces the FR61 page cap... server-side").
type pagingFakeLeafLabClient struct {
	pb.LeafLabAPIClient

	// pages maps a page_token ("" for page one) to the response the real
	// server would give for that token -- the fake stands in for
	// leaflab-api's own keyset paging, not for anything this BFF computes
	// itself (the BFF has no cap or offset logic of its own to test here).
	pages map[string]*pb.ListBoardsResponse

	calls        []string // page tokens requested, in order
	pageSizeSeen []int32  // page_size the BFF request carried, in order
}

func (f *pagingFakeLeafLabClient) ListBoards(ctx context.Context, in *pb.ListBoardsRequest, opts ...grpc.CallOption) (*pb.ListBoardsResponse, error) {
	tok := in.GetPage().GetPageToken()
	f.calls = append(f.calls, tok)
	f.pageSizeSeen = append(f.pageSizeSeen, in.GetPage().GetPageSize())
	resp, ok := f.pages[tok]
	if !ok {
		return &pb.ListBoardsResponse{}, nil
	}
	return resp, nil
}

// TestPagination_TwoPages_NoDuplicatesCapRespected drives the full FR61
// "load more" continuation: handleBoards renders page one (server-capped
// at 2 boards, matching a low cap for this test) with a next_page_token,
// then handleBoardsRows -- the exact route/handler
// components.BoardsRows's "Load more" button hits -- fetches page two
// using that token. Asserts: each page returned exactly the boards the
// fake capped it at (cap respected, since the BFF applies no page_size of
// its own -- boards_data.go's fetchBoardsPage leaves page_size unset, so
// the fake standing in for leaflab-api's own ClampPageSize is the only
// cap in play), the combined board ID set across both pages has no
// duplicates, and covers exactly the full board set (no gaps).
func TestPagination_TwoPages_NoDuplicatesCapRespected(t *testing.T) {
	const pageCap = 2
	page1 := []*pb.BoardInfo{board("dev-1", 1), board("dev-2", 2)}
	page2 := []*pb.BoardInfo{board("dev-3", 3)}

	fake := &pagingFakeLeafLabClient{
		pages: map[string]*pb.ListBoardsResponse{
			"": {
				Boards:    page1,
				Page:      &pb.PageResponse{NextPageToken: "page-2-token"},
				ServerNow: &pb.Instant{UnixMillis: 9999},
			},
			"page-2-token": {
				Boards:    page2,
				Page:      &pb.PageResponse{NextPageToken: ""}, // last page
				ServerNow: &pb.Instant{UnixMillis: 9999},
			},
		},
	}
	app := &App{auth: devAuth(t), api: &APIClient{LeafLab: fake}}

	// Page one, via handleBoards -- the full page render.
	req1 := httptest.NewRequest(http.MethodGet, "/boards", nil)
	w1 := httptest.NewRecorder()
	app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBoards))(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("page one status = %d, want %d; body = %s", w1.Code, http.StatusOK, w1.Body.String())
	}
	body1 := w1.Body.String()
	if len(page1) != pageCap {
		t.Fatalf("test fixture bug: page1 has %d boards, want cap %d", len(page1), pageCap)
	}
	for _, b := range page1 {
		if !strings.Contains(body1, b.GetDeviceId()) {
			t.Errorf("page one missing board %q, body: %s", b.GetDeviceId(), body1)
		}
	}
	if strings.Contains(body1, "dev-3") {
		t.Errorf("page one must not contain page two's board (no client-side accumulation, no premature duplication), body: %s", body1)
	}
	if !strings.Contains(body1, "/boards/rows?page_token=page-2-token") {
		t.Errorf("expected a Load more control targeting the next page token, body: %s", body1)
	}

	// Extract the "Load more" hx-get target the way a browser would use
	// it, proving the continuation the page rendered is the one we then
	// drive below (not a hand-picked token).
	loadMoreIdx := strings.Index(body1, "/boards/rows?page_token=")
	if loadMoreIdx == -1 {
		t.Fatalf("could not find load-more control in page one body: %s", body1)
	}
	rest := body1[loadMoreIdx:]
	endIdx := strings.IndexAny(rest, `"`)
	if endIdx == -1 {
		t.Fatalf("could not parse load-more hx-get target from: %s", rest)
	}
	loadMoreTarget := rest[:endIdx]

	// Page two, via handleBoardsRows -- the fragment route the "Load
	// more" button's hx-get hits.
	req2 := httptest.NewRequest(http.MethodGet, loadMoreTarget, nil)
	w2 := httptest.NewRecorder()
	app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBoardsRows))(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("page two status = %d, want %d; body = %s", w2.Code, http.StatusOK, w2.Body.String())
	}
	body2 := w2.Body.String()
	if !strings.Contains(body2, "dev-3") {
		t.Errorf("page two missing its own board, body: %s", body2)
	}
	for _, b := range page1 {
		if strings.Contains(body2, b.GetDeviceId()) {
			t.Errorf("page two must not repeat page one's board %q (no duplicates across the continuation), body: %s", b.GetDeviceId(), body2)
		}
	}
	if strings.Contains(body2, "Load more") {
		t.Errorf("page two is the last page (empty next_page_token) -- must not render another Load more control, body: %s", body2)
	}

	// Exactly two ListBoards calls were made: page one with an empty
	// token, page two with the token page one handed back -- proving the
	// continuation used the server's own opaque token, not something the
	// BFF derived itself (an offset, an index, etc.).
	if len(fake.calls) != 2 {
		t.Fatalf("ListBoards calls = %v, want exactly 2", fake.calls)
	}
	if fake.calls[0] != "" {
		t.Errorf("first call page_token = %q, want empty (page one)", fake.calls[0])
	}
	if fake.calls[1] != "page-2-token" {
		t.Errorf("second call page_token = %q, want %q", fake.calls[1], "page-2-token")
	}

	// No cap/page_size logic of the BFF's own: fetchBoardsPage always
	// leaves page_size unset (0), trusting leaflab-api's own
	// contract.ClampPageSize entirely (NFR18.2 -- the same rule a
	// programmatic caller gets).
	for i, sz := range fake.pageSizeSeen {
		if sz != 0 {
			t.Errorf("call %d page_size = %d, want 0 (unset -- the BFF applies no page-size logic of its own)", i, sz)
		}
	}

	// Combined coverage: exactly the full board set, no duplicates, no
	// gaps.
	seen := map[int64]bool{}
	all := append(append([]*pb.BoardInfo{}, page1...), page2...)
	for _, b := range all {
		if seen[b.GetBoardId()] {
			t.Fatalf("test fixture bug: duplicate board id %d in fixture itself", b.GetBoardId())
		}
		seen[b.GetBoardId()] = true
	}
	for _, b := range all {
		want := b.GetDeviceId()
		inBody1 := strings.Contains(body1, want)
		inBody2 := strings.Contains(body2, want)
		if inBody1 == inBody2 {
			t.Errorf("board %q must appear in exactly one page's rendered output, page1=%v page2=%v", want, inBody1, inBody2)
		}
	}
}

// --- NFR18.2 parity: screen vs. a plain gRPC client -----------------------

// TestListBoards_ParityBetweenScreenAndPlainGRPCClient proves handleBoards
// renders exactly the board set a plain gRPC client gets back for the same
// ListBoardsRequest -- no filtering, reshaping, or additional
// authorization decision of the BFF's own layered on top of what
// leaflab-api returned (NFR18.2: "the browser transport is an additional
// door onto the same service, not a second service with its own rules").
// fetchBoardsPage (boards_data.go) is the single call site both the screen
// and this "plain client" simulation go through, so this test's real
// value is asserting the render step (components.BoardsPage) doesn't drop,
// add, or reorder anything relative to what that one RPC call returned.
func TestListBoards_ParityBetweenScreenAndPlainGRPCClient(t *testing.T) {
	fake := &fakeLeafLabClient{
		boardsResp: &pb.ListBoardsResponse{
			Boards: []*pb.BoardInfo{
				board("device-one", 1),
				board("device-two", 2),
				board("device-three", 3),
			},
			ServerNow: &pb.Instant{UnixMillis: 4242},
		},
	}
	app := &App{auth: devAuth(t), api: &APIClient{LeafLab: fake}}

	// The screen path: an authenticated browser request to /boards.
	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	w := httptest.NewRecorder()
	app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBoards))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	rendered := w.Body.String()

	// The "plain gRPC client" path: the same request the screen made
	// (empty page token, page one), issued directly against the same
	// backend (same fake standing in for the same leaflab-api instance
	// with the same forwarded token) -- exactly what a programmatic caller
	// authenticated as the same user would get.
	plainResp, err := app.api.LeafLab.ListBoards(context.Background(), &pb.ListBoardsRequest{
		Page: &pb.PageRequest{PageToken: ""},
	})
	if err != nil {
		t.Fatalf("plain gRPC client ListBoards: %v", err)
	}

	if len(plainResp.GetBoards()) != len(fake.boardsResp.GetBoards()) {
		t.Fatalf("plain client board count = %d, want %d", len(plainResp.GetBoards()), len(fake.boardsResp.GetBoards()))
	}
	for _, b := range plainResp.GetBoards() {
		if !strings.Contains(rendered, b.GetDeviceId()) {
			t.Errorf("board %q returned to the plain gRPC client is missing from the screen's rendered output -- NFR18.2 parity violated, body: %s", b.GetDeviceId(), rendered)
		}
		if !strings.Contains(rendered, strconv.FormatInt(b.GetBoardId(), 10)) {
			t.Errorf("board id %d returned to the plain gRPC client is missing from the screen's rendered output, body: %s", b.GetBoardId(), rendered)
		}
	}
	// And the reverse: the screen renders nothing the plain client didn't
	// also get (no BFF-only board sneaking into the render).
	for _, b := range fake.boardsResp.GetBoards() {
		found := false
		for _, pb2 := range plainResp.GetBoards() {
			if pb2.GetBoardId() == b.GetBoardId() {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("board %q rendered by the screen was not in the plain gRPC client's response -- NFR18.2 parity violated", b.GetDeviceId())
		}
	}
	if fake.boardsCalls != 2 { // one from the screen, one from the "plain client" call above
		t.Errorf("ListBoards calls = %d, want 2 (screen + plain client)", fake.boardsCalls)
	}
}
