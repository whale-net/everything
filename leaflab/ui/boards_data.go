package main

import (
	"context"
	"fmt"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// fetchBoardsPage calls leaflab-api's ListBoards RPC through the
// token-forwarding client (app.api.LeafLab) -- the signed-in user's own
// forwarded access token, never a service account credential (NFR18.1).
// pageToken is passed straight through as FR61's opaque keyset page
// token; an empty token requests the first page.
//
// page_size is deliberately left unset: leaflab-api's own
// contract.ClampPageSize (leaflab/api/contract/paging.go) enforces the
// FR61 page cap and default size server-side -- "a request above the cap
// is clamped, not rejected" per ListBoardsRequest's own doc comment -- so
// this BFF has no cap logic of its own to duplicate or drift out of sync
// (NFR18.2: the same page-size rule a programmatic gRPC caller gets).
//
// The single call site both handlers_boards.go handlers use: handleBoards
// for the full page-one render, handleBoardsRows for every FR61 "load
// more" continuation.
func (app *App) fetchBoardsPage(ctx context.Context, pageToken string) (*pb.ListBoardsResponse, error) {
	resp, err := app.api.LeafLab.ListBoards(ctx, &pb.ListBoardsRequest{
		Page: &pb.PageRequest{PageToken: pageToken},
	})
	if err != nil {
		return nil, fmt.Errorf("ListBoards: %w", err)
	}
	return resp, nil
}
